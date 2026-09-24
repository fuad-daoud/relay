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
// its events and sealed round files hang off, and when it was archived
// (P3d §3). ArchivedAt is the tarball's stamp for a record imported from
// .archive/, and the gc/unbind stamp for one archived by this binary.
type ArchivedBinding struct {
	Binding    Binding
	RecordID   string
	ArchivedAt time.Time
}

// SealAll seals every round present among name's NNN-* files, ignoring
// Sealable: the archive and delete paths call it once the builder has been
// stopped, so nothing that still reads an open round is in flight (P3d §4.1).
//
// It is the counterpart of the daemon's per-round seal (sealRounds), which
// consults Sealable because a round may still be read; here the whole binding
// is going away, so every round's files go into round_file.
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

// ListArchived returns every archived record, oldest archived_at first. It
// imports the tarballs in .archive/ first (a file that is present is
// imported, §4.2), so a root migrated from a pre-P3d relevo answers from the
// database on the first read.
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

// ArchivedLog returns the events of the archived record recordID, decoded the
// way readLog decodes a live binding's (P3d §4.1).
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

// ArchivedAtOf returns the archived stamp of the record recordID and whether
// a timestamp was found. It is what the archive source reports to the mirror
// (ingest.source), which needs the stamp the way tarSource did.
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

// ArchivedRecord returns the archived record recordID decoded into a Binding,
// and whether it was found. It fails when the record was written by a newer
// relevo, exactly as load does for a live one.
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

// ArchivedFile returns the sealed bytes of one of recordID's round files, and
// whether the record has such a row. It is how the archive source reads an
// NNN-* member (P3d §4.5).
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

// ArchivedFiles returns the basenames of recordID's sealed round files,
// sorted.
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
// one it imported (§4.2). A tarball that does not parse is left in place with
// one warning per process, so the next process retries it and a corrupt file
// never fails a read verb. .archive/ itself is removed once it is empty.
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

// importTarball imports one gc tarball: its bind.json becomes a record that is
// archived at once, its log.jsonl the record's events, and each NNN-* member a
// round_file row. All of it runs in one transaction, and the tarball is
// removed only after that transaction commits, so a crash mid-import leaves
// the tarball for the next attempt (§4.2, §6).
func (s *Store) importTarball(path string) error {
	fileName, stamp, ok := parseArchiveStamp(filepath.Base(path))
	if !ok {
		// Not today's <name>-YYYYMMDD-HHMMSS.tar.gz layout: not ours to
		// import, and not ours to remove.
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

	// §4.2 step 2: RecordPut a fresh record and RecordArchive it at once, so
	// it never collides with a live name. RecordPut's lookup is the live row,
	// so when a live binding already holds the name the archived record is
	// inserted directly instead -- the import must never touch a live
	// binding's row.
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
		var id string
		var perr error
		if live.ID != "" {
			id, perr = dtx.RecordPutArchived(rec, stamp)
		} else {
			if id, perr = dtx.RecordPut(rec); perr != nil {
				return perr
			}
			perr = dtx.RecordArchive(s.owner, name, stamp)
		}
		if perr != nil {
			return perr
		}

		if logRaw, ok := files["log.jsonl"]; ok {
			entries, derr := decodeLog(bytes.NewReader(logRaw.body))
			if derr != nil {
				return fmt.Errorf("%s: %w", path, derr)
			}
			// entry_json keeps each line's exact bytes, as P3a's import does
			// (§4.2 step 3).
			lines := splitLogLines(logRaw.body)
			if len(lines) != len(entries) {
				return fmt.Errorf("%s: %d entries but %d lines", path, len(entries), len(lines))
			}
			evs := make([]db.RecordEvent, 0, len(entries))
			for i, e := range entries {
				evs = append(evs, recordEventOf(e, string(lines[i])))
			}
			if err := dtx.EventReplaceAll(id, evs); err != nil {
				return err
			}
		}

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
	})
	if err != nil {
		return err
	}

	// Committed: only now is the tarball removed. A removal that fails is
	// logged, never returned: the import itself succeeded and the next pass
	// re-imports the same bytes idempotently.
	if rerr := os.Remove(path); rerr != nil {
		slog.Warn("import: could not remove the imported tarball", "path", path, "err", rerr)
	}
	return nil
}

// tarballWarned remembers the tarball paths this process has already warned
// about, so a corrupt one is logged once per process however often
// ListArchived runs.
var tarballWarned sync.Map

// warnTarballOnce logs the first -- and only the first -- failure to import
// one tarball in this process.
func warnTarballOnce(path string, err error) {
	if _, loaded := tarballWarned.LoadOrStore(path, struct{}{}); loaded {
		return
	}
	slog.Warn("import: leaving a tarball in .archive alone", "path", path, "err", err)
}

// parseArchiveStamp splits a tarball's file name into the binding name and the
// stamp archive() wrote into it: <name>-YYYYMMDD-HHMMSS.tar.gz. It replaces
// ListArchives' name parsing now that the file name is only the import's
// input.
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
// "<top>/<base>", the flat layout tarGzDir wrote, keyed by basename. top is
// the top-level directory the first member named ("" when it has none).
//
// A member over maxArchiveFileBytes fails the read rather than ballooning
// memory: relevo's own state files are small, and a runaway file means
// something has gone wrong. This is tarGzDir's inverse, moved here from
// store.go so the tarball reader and the tarball importer live together.
func readTarFiles(path string) (files map[string]tarFile, top string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	defer gz.Close()

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
		if len(parts) != 2 || parts[1] == "" || strings.Contains(parts[1], "/") {
			// Not "<name>/<file>", or nested deeper: tarGzDir writes one
			// flat directory, so this is not a member we understand.
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
