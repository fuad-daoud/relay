package store

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ArchivedBinding is one archived record: the binding it was, the record id
// its events and sealed round files hang off, and when it was archived.
type ArchivedBinding struct {
	Binding    Binding
	RecordID   string
	ArchivedAt time.Time
}

// SealAll seals every round present among name's NNN-* files, ignoring
// Sealable: the caller has already stopped the builder.
func (t *Tx) SealAll(name string) (int, error) {
	rounds, err := t.s.RoundsOnDisk(name)
	if err != nil {
		return 0, err
	}

	total := 0
	for _, r := range rounds {
		n, err := t.SealRound(name, r)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// ListArchived returns every archived record, oldest archived_at first,
// importing the tarballs in .archive/ first.
func (s *Store) ListArchived() ([]ArchivedBinding, error) {
	if err := s.importTarballs(); err != nil {
		return nil, err
	}

	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	recs, err := d.RecordListArchived(s.owner)
	if err != nil {
		return nil, fmt.Errorf("read archived records: %w", err)
	}

	out := make([]ArchivedBinding, 0, len(recs))
	for _, rec := range recs {
		b, err := decodeBinding([]byte(rec.JSON), rec.Name)
		if err != nil {
			return nil, err
		}
		var at time.Time
		if rec.ArchivedAt != nil {
			at = *rec.ArchivedAt
		}
		out = append(out, ArchivedBinding{Binding: b, RecordID: rec.ID, ArchivedAt: at})
	}
	return out, nil
}

// ArchivedLog returns the events of the archived record recordID.
func (s *Store) ArchivedLog(recordID string) ([]LogEntry, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	events, err := d.EventsOf(recordID, 0)
	if err != nil {
		return nil, fmt.Errorf("read archived log %s: %w", recordID, err)
	}
	return logEntriesOf(events)
}

func (s *Store) ArchivedAtOf(recordID string) (time.Time, bool) {
	d, err := s.dbForRead()
	if err != nil || d == nil {
		return time.Time{}, false
	}
	rec, ok, err := d.RecordGetByID(recordID)
	if err != nil || !ok || rec.ArchivedAt == nil {
		return time.Time{}, false
	}
	return *rec.ArchivedAt, true
}

// ArchivedRecord returns recordID decoded into a Binding, and whether it was found.
func (s *Store) ArchivedRecord(recordID string) (Binding, bool, error) {
	d, err := s.dbForRead()
	if err != nil {
		return Binding{}, false, err
	}
	if d == nil {
		return Binding{}, false, nil
	}
	rec, ok, err := d.RecordGetByID(recordID)
	if err != nil || !ok {
		return Binding{}, false, err
	}
	b, err := decodeBinding([]byte(rec.JSON), rec.Name)
	if err != nil {
		return Binding{}, false, err
	}
	return b, true, nil
}

// ArchivedFile returns the sealed bytes of one of recordID's round files.
func (s *Store) ArchivedFile(recordID, name string) ([]byte, bool, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, false, err
	}
	if d == nil {
		return nil, false, nil
	}
	body, _, ok, err := d.RoundFileGet(recordID, name)
	if err != nil {
		return nil, false, err
	}
	return body, ok, nil
}

// ArchivedFiles returns the basenames of recordID's sealed round files.
func (s *Store) ArchivedFiles(recordID string) ([]string, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	return d.RoundFileList(recordID)
}

// importTarballs imports every tarball present in .archive/ and removes each
// one it imported. A tarball that does not parse is left in place with one
// warning per process, so a corrupt file never fails a read verb.
func (s *Store) importTarballs() error {
	paths, err := filepath.Glob(filepath.Join(s.ArchiveDir(), "*.tar.gz"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)

	for _, path := range paths {
		if err := s.importTarball(path); err != nil {
			warnTarballOnce(path, err)
		}
	}

	entries, err := os.ReadDir(s.ArchiveDir())
	if err == nil && len(entries) == 0 {
		if rerr := os.Remove(s.ArchiveDir()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			slog.Warn("archive: could not remove the empty .archive directory", "err", rerr)
		}
	}
	return nil
}

// importTarball imports one gc tarball: its bind.json becomes a record
// archived at once, its log.jsonl the record's events, and each NNN-* member a
// round_file row, all in one transaction. The tarball is removed only after
// that transaction commits, so a crash mid-import leaves it for the next pass.
func (s *Store) importTarball(path string) error {
	fileName, stamp, ok := parseArchiveStamp(filepath.Base(path))
	if !ok {
		// Not today's <name>-YYYYMMDD-HHMMSS.tar.gz layout: not ours.
		return nil
	}

	files, inner, err := readTarFiles(path)
	if err != nil {
		return err
	}
	name := inner
	if name == "" {
		name = fileName
	}

	rawBinding, ok := files["bind.json"]
	if !ok {
		return fmt.Errorf("%s: no bind.json member", path)
	}
	b, err := decodeBinding(rawBinding.body, name)
	if err != nil {
		return err
	}
	if b.Name == "" {
		b.Name = name
	}
	rawJSON, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal binding %q: %w", name, err)
	}

	d, err := s.dbForWrite()
	if err != nil {
		return err
	}

	// A live binding holding the name is never touched: insert archived directly.
	live, _, err := d.RecordGet(s.owner, name)
	if err != nil {
		return err
	}
	rec := db.Record{
		Owner:     s.owner,
		Name:      name,
		State:     string(b.State),
		Round:     b.Round,
		CWD:       b.CWD,
		JSON:      string(rawJSON),
		CreatedAt: b.CreatedAt,
		UpdatedAt: b.UpdatedAt,
	}

	err = d.Tx(func(dtx *db.Tx) error {
		id, perr := importArchivedRecord(dtx, rec, live.ID != "", stamp)
		if perr != nil {
			return perr
		}
		if err := importTarballLog(dtx, path, id, files); err != nil {
			return err
		}
		return importTarballRoundFiles(dtx, id, files)
	})
	if err != nil {
		return err
	}

	// Committed: only now is the tarball removed. A removal that fails is
	// logged, never returned: the next pass re-imports the same bytes
	// idempotently.
	if rerr := os.Remove(path); rerr != nil {
		slog.Warn("import: could not remove the imported tarball", "path", path, "err", rerr)
	}
	return nil
}

// importArchivedRecord puts and archives the imported record.
func importArchivedRecord(dtx *db.Tx, rec db.Record, live bool, stamp time.Time) (string, error) {
	if live {
		return dtx.RecordPutArchived(rec, stamp)
	}
	id, err := dtx.RecordPut(rec)
	if err != nil {
		return "", err
	}
	if err := dtx.RecordArchive(rec.Owner, rec.Name, stamp); err != nil {
		return "", err
	}
	return id, nil
}

// importTarballLog writes the tarball's log.jsonl member as the record's
// events; entry_json keeps each line's exact bytes.
func importTarballLog(dtx *db.Tx, path, id string, files map[string]tarFile) error {
	logRaw, ok := files["log.jsonl"]
	if !ok {
		return nil
	}
	entries, err := decodeLog(bytes.NewReader(logRaw.body))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	lines := splitLogLines(logRaw.body)
	if len(lines) != len(entries) {
		return fmt.Errorf("%s: %d entries but %d lines", path, len(entries), len(lines))
	}
	evs := make([]db.RecordEvent, 0, len(entries))
	for i, e := range entries {
		evs = append(evs, recordEventOf(e, string(lines[i])))
	}
	return dtx.EventReplaceAll(id, evs)
}

// importTarballRoundFiles writes each member whose leading name is NNN-* as a
// round_file row, a nested "NNN-<actor>/<rel>" name included.
func importTarballRoundFiles(dtx *db.Tx, id string, files map[string]tarFile) error {
	now := time.Now().UTC()
	for base, f := range files {
		round, isRound := roundOfFile(base)
		if !isRound {
			continue
		}
		if err := dtx.RoundFilePut(id, base, round, f.body, f.mtime, now); err != nil {
			return err
		}
	}
	return nil
}

// tarballWarned remembers the tarball paths this process has already warned
// about, so a corrupt one is logged once per process.
var tarballWarned sync.Map

func warnTarballOnce(path string, err error) {
	if _, loaded := tarballWarned.LoadOrStore(path, struct{}{}); loaded {
		return
	}
	slog.Warn("import: leaving a tarball in .archive alone", "path", path, "err", err)
}

// parseArchiveStamp splits a tarball's file name into the binding name and the
// stamp archive() wrote into it: <name>-YYYYMMDD-HHMMSS.tar.gz.
func parseArchiveStamp(base string) (name string, at time.Time, ok bool) {
	base = strings.TrimSuffix(base, ".tar.gz")
	if len(base) < len(archiveStampLayout)+2 {
		return "", time.Time{}, false
	}
	cut := len(base) - len(archiveStampLayout)
	if base[cut-1] != '-' {
		return "", time.Time{}, false
	}
	at, err := time.Parse(archiveStampLayout, base[cut:])
	if err != nil {
		return "", time.Time{}, false
	}
	return base[:cut-1], at.UTC(), true
}

// tarFile is one member of a gc tarball: its bytes and its own mtime, which
// round_file stores so StatFile keeps answering with the file's real stamp.
type tarFile struct {
	body  []byte
	mtime time.Time
}

// readTarFiles reads every regular member of path whose name is
// "<top>/<member>", keyed by the member path below top, and returns the
// top-level directory the first member named. The member is a flat basename or
// a nested "NNN-<actor>/<rel>", the round_file name it imports under. A member
// over maxArchiveFileBytes fails the read rather than ballooning memory.
func readTarFiles(path string) (files map[string]tarFile, top string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	defer func() { _ = gz.Close() }()

	files = map[string]tarFile{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", path, err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}

		trimmed := strings.Trim(h.Name, "/")
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) != 2 || parts[1] == "" || containsDotDot(parts[1]) {
			// Not "<name>/<member>", or a path that escapes: not a member we
			// understand. The member below <name> may be nested
			// ("<name>/004-reviewer/summary.md"): that rest is the round_file
			// name it imports under.
			continue
		}
		if top == "" {
			top = parts[0]
		}
		if parts[0] != top {
			continue
		}
		if h.Size > maxArchiveFileBytes {
			return nil, "", fmt.Errorf("%s: %s is %d bytes, over the %d byte archive limit",
				path, parts[1], h.Size, maxArchiveFileBytes)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, "", fmt.Errorf("%s: read %s: %w", path, parts[1], err)
		}
		files[parts[1]] = tarFile{body: body, mtime: h.ModTime.UTC()}
	}
	return files, top, nil
}
