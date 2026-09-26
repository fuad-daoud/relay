package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/fuad-daoud/relevo/internal/db"
)

// dbForWrite returns the store's database handle, opening it -- and applying
// its migrations -- on first use: the path every write takes. Path helpers,
// WithLock alone and DaemonRunning never call it, so a lock-only or path-only
// store opens no database.
//
// A database whose schema is newer than this binary's is refused with
// db.ErrNewerSchema.
func (s *Store) dbForWrite() (*db.DB, error) {
	if s.shared != nil {
		return s.shared, nil
	}
	s.dbOnce.Do(func() {
		if err := os.MkdirAll(s.root, bindingDirMode); err != nil {
			s.dbErr = fmt.Errorf("open store db %s: %w", s.DBPath(), err)
			return
		}
		d, err := db.Open(s.DBPath())
		if err != nil {
			s.dbErr = fmt.Errorf("open store db %s: %w", s.DBPath(), err)
			return
		}
		if d.Newer() {
			_ = d.Close()
			s.dbErr = db.ErrNewerSchema
			return
		}
		s.dbh = d
	})
	return s.dbh, s.dbErr
}

// dbForRead returns the store's database handle without creating the database:
// (nil, nil) when <root>/relevo.db does not exist, which every read path treats
// as "no rows". Only a write, or the import of a present legacy file, creates
// it.
func (s *Store) dbForRead() (*db.DB, error) {
	if s.shared != nil {
		return s.shared, nil
	}
	if _, err := os.Stat(s.DBPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open store db %s: %w", s.DBPath(), err)
	}
	return s.dbForWrite()
}

// DB is the exported form of dbForWrite, opening and migrating on first use.
func (s *Store) DB() (*db.DB, error) { return s.dbForWrite() }

// DBIfExists is the exported form of dbForRead.
func (s *Store) DBIfExists() (*db.DB, error) { return s.dbForRead() }

// importPresent adopts binding name's files, if any: a present bind.json
// upserts the record, a present log.jsonl replaces that record's events, and a
// present .viewed sidecar becomes the record's viewed_at; each file is deleted
// once its write committed, so a failed import leaves it where it was.
//
// It always runs under the flock, because every caller already holds it. A log
// with no record yet (a fork directory before its Save) is left for save() to
// adopt.
//
// A root with none of the three files present imports nothing and opens no
// database: the presence of a legacy file is the one exception to reads never
// creating the database.
func (s *Store) importPresent(name string) error {
	bp, lp := s.bindingPath(name), s.logPath(name)

	bpRaw, bpPresent, err := readIfPresent(bp, "binding", name)
	if err != nil {
		return err
	}
	lpRaw, lpPresent, err := readIfPresent(lp, "log for", name)
	if err != nil {
		return err
	}
	viewedPath := s.ViewedPath(name)
	viewedInfo, viewedPresent, err := statIfPresent(viewedPath, name)
	if err != nil {
		return err
	}

	if !bpPresent && !lpPresent && !viewedPresent {
		return nil
	}

	d, err := s.dbForWrite()
	if err != nil {
		return err
	}

	if bpPresent {
		if err := s.importBindingFile(d, name, bpRaw); err != nil {
			return err
		}
	}
	if lpPresent {
		adopted, err := s.importLogFile(d, name, lpRaw)
		if err != nil {
			return err
		}
		// A log without a record: leave everything alone and import nothing.
		if !adopted {
			return nil
		}
	}

	// Every DB write committed: only now are the files removed.
	if bpPresent {
		if err := os.Remove(bp); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove imported binding %q: %w", name, err)
		}
	}
	if lpPresent {
		if err := os.Remove(lp); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove imported log %q: %w", name, err)
		}
	}

	// The sidecar's mtime becomes viewed_at only when the record has no stamp
	// of its own, so an older file never overwrites a newer stamp.
	if viewedPresent {
		return s.adoptViewed(d, name, viewedPath, viewedInfo)
	}
	return nil
}

// readIfPresent reads a legacy state file and reports whether it exists.
func readIfPresent(path, what, name string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("read %s %q: %w", what, name, err)
		}
		return nil, false, nil
	}
	return raw, true, nil
}

func statIfPresent(path, name string) (os.FileInfo, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("stat viewed %q: %w", name, err)
		}
		return nil, false, nil
	}
	return info, true, nil
}

// importBindingFile adopts a bind.json: record_json is the file's binding
// re-marshalled compactly.
func (s *Store) importBindingFile(d *db.DB, name string, raw []byte) error {
	b, err := decodeBinding(raw, name)
	if err != nil {
		return err
	}
	compact, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal binding %q: %w", name, err)
	}
	if _, err := d.RecordPut(db.Record{
		Owner:     s.owner,
		Name:      b.Name,
		State:     string(b.State),
		Round:     b.Round,
		CWD:       b.CWD,
		JSON:      string(compact),
		CreatedAt: b.CreatedAt,
		UpdatedAt: b.UpdatedAt,
	}); err != nil {
		return fmt.Errorf("import binding %q: %w", name, err)
	}
	return nil
}

// importLogFile adopts a log.jsonl; adopted is false when no record exists yet
// for it. entry_json keeps each line's exact bytes.
func (s *Store) importLogFile(d *db.DB, name string, raw []byte) (bool, error) {
	entries, err := decodeLog(bytes.NewReader(raw))
	if err != nil {
		return false, fmt.Errorf("%q: %w", name, err)
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return false, fmt.Errorf("import log %q: %w", name, err)
	}
	if !ok {
		return false, nil
	}

	lines := splitLogLines(raw)
	evs, err := recordEventsOf(entries, lines)
	if err != nil {
		return false, fmt.Errorf("import log %q: %w", name, err)
	}
	if err := d.EventReplaceAll(rec.ID, evs); err != nil {
		return false, fmt.Errorf("import log %q: %w", name, err)
	}
	return true, nil
}

func (s *Store) adoptViewed(d *db.DB, name, path string, info os.FileInfo) error {
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return fmt.Errorf("import viewed %q: %w", name, err)
	}
	if !ok {
		return nil
	}
	if rec.ViewedAt == nil {
		if err := d.RecordSetViewed(s.owner, name, info.ModTime()); err != nil {
			return fmt.Errorf("import viewed %q: %w", name, err)
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove imported viewed %q: %w", name, err)
	}
	return nil
}

// importAll adopts every binding directory under the root that holds a
// bind.json, before list() reads the database.
//
// A directory whose bind.json was written by a newer relevo is left as it is
// and skipped, so one such file cannot make List fail for every other binding;
// load still returns ErrNewerFormat for that name.
func (s *Store) importAll() error {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state root: %w", err)
	}

	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := os.Stat(s.bindingPath(e.Name())); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat binding %q: %w", e.Name(), err)
		}
		if err := s.importPresent(e.Name()); err != nil {
			var newer *ErrNewerFormat
			if errors.As(err, &newer) {
				warnNewerFormatOnce(e.Name(), err)
				continue
			}
			return err
		}
	}
	return nil
}

// newerFormatWarned keeps a newer-format binding logged once per name.
var newerFormatWarned sync.Map

func warnNewerFormatOnce(name string, err error) {
	if _, loaded := newerFormatWarned.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	slog.Warn("leaving a binding written by a newer relevo alone", "binding", name, "err", err)
}

// legacyPresent reports whether name has any file importPresent adopts. A
// non-not-exist stat error is returned, not treated as absent, so a failing
// root never skips an import, which writes.
func (s *Store) legacyPresent(name string) (bool, error) {
	for _, path := range []string{s.bindingPath(name), s.logPath(name), s.ViewedPath(name)} {
		if _, err := os.Stat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("stat %q: %w", path, err)
		}
	}
	return false, nil
}

// anyLegacyPresent applies legacyPresent to every binding directory under the
// root, so a whole-root read knows whether an importAll needs the lock. It
// covers a superset of importAll's files, so it can only lock too often, never
// skip an import.
func (s *Store) anyLegacyPresent() (bool, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read state root: %w", err)
	}

	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		present, err := s.legacyPresent(e.Name())
		if err != nil {
			return false, err
		}
		if present {
			return true, nil
		}
	}
	return false, nil
}

// splitLogLines returns a log.jsonl's non-empty lines, in order, so an
// imported entry's entry_json can keep the line's exact bytes.
func splitLogLines(raw []byte) [][]byte {
	var out [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		out = append(out, append([]byte(nil), line...))
	}
	return out
}

// recordEventOf projects a LogEntry onto the binding_event columns.
func recordEventOf(e LogEntry, entryJSON string) db.RecordEvent {
	return db.RecordEvent{
		Seq:         e.Seq,
		TS:          e.TS,
		Round:       e.Round,
		Direction:   string(e.Direction),
		Kind:        string(e.Kind),
		Confirmed:   e.Confirmed,
		DeliveredAt: e.DeliveredAt,
		Route:       e.Route,
		JSON:        entryJSON,
	}
}

// recordEventsOf projects decoded entries and their exact JSON onto the event
// rows EventReplaceAll takes, in order.
func recordEventsOf(entries []LogEntry, entryJSON [][]byte) ([]db.RecordEvent, error) {
	if len(entries) != len(entryJSON) {
		return nil, fmt.Errorf("%d entries but %d lines", len(entries), len(entryJSON))
	}
	evs := make([]db.RecordEvent, 0, len(entries))
	for i, e := range entries {
		evs = append(evs, recordEventOf(e, string(entryJSON[i])))
	}
	return evs, nil
}

// logEntriesOf decodes stored events back into LogEntry values. entry_json is
// authoritative for the content; Seq, Confirmed, DeliveredAt and Route come
// from the promoted columns.
func logEntriesOf(events []db.RecordEvent) ([]LogEntry, error) {
	if len(events) == 0 {
		return nil, nil
	}
	out := make([]LogEntry, 0, len(events))
	for _, ev := range events {
		var e LogEntry
		if err := json.Unmarshal([]byte(ev.JSON), &e); err != nil {
			return nil, fmt.Errorf("decode log entry: %w", err)
		}
		e.Seq = ev.Seq
		e.Confirmed = ev.Confirmed
		e.DeliveredAt = ev.DeliveredAt
		e.Route = ev.Route
		out = append(out, e)
	}
	return out, nil
}
