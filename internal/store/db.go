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
// its migrations -- on first use. It is the path every write takes, so the
// first Save/AppendLog/import creates <root>/relevo.db. Path helpers,
// WithLock alone and DaemonRunning never call it, so serve/admin.go's
// lock-only store and ingest.go's store.New("/") open no database (P3a plan
// §3.2, §4.2). The daemon-info write does call it now (P3b plan §4.3).
//
// A database whose schema is newer than this binary's is never migrated and is
// refused from every data method with db.ErrNewerSchema; path helpers keep
// working.
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
			// Leave no handle open on a database this binary must not touch.
			_ = d.Close()
			s.dbErr = db.ErrNewerSchema
			return
		}
		s.dbh = d
	})
	return s.dbh, s.dbErr
}

// dbForRead returns the store's database handle without creating the
// database: when <root>/relevo.db does not exist it returns (nil, nil), which
// every read path treats as "no rows" -- load returns ErrNotFound, list an
// empty list, readLog and readLogAfter nil entries, ViewedAt (zero, false).
// A read must never conjure a database into a root that has none, so only a
// write -- or the import of a present legacy bind.json/log.jsonl -- creates
// the file (P3a plan §B1).
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

// DB returns the store's database handle, opening and migrating it on first
// use. It is the exported form of dbForWrite, for the packages that keep a
// small JSON record in the schema-v2 kv table (P3b plan §4.2).
func (s *Store) DB() (*db.DB, error) { return s.dbForWrite() }

// DBIfExists returns the store's database handle without creating the
// database: (nil, nil) when <root>/relevo.db does not exist. It is the
// exported form of dbForRead, for readers that must not conjure a database
// into a root that has none.
func (s *Store) DBIfExists() (*db.DB, error) { return s.dbForRead() }

// importPresent adopts binding name's files, if any, into the database: a
// present bind.json upserts the record and is deleted, a present log.jsonl
// replaces that record's events and is deleted, and a present .viewed sidecar
// becomes the record's viewed_at and is deleted (#143). It is the rule that
// migrates existing state on first use and keeps hand-written fixtures working
// (P3a plan §4.3).
//
// It always runs under the flock, because every caller already holds it. A
// log.jsonl with no record yet -- a fork directory before its Save (§4.6) --
// is left in place for save() to adopt. A file is removed only after its DB
// write committed, so a failed import leaves the file where it was.
//
// A root with none of the three files present imports nothing and opens no
// database, so a read of a root that has never been written leaves no relevo.db
// behind (§B1). The presence of a legacy file is the one exception to reads
// never creating the database.
func (s *Store) importPresent(name string) error {
	bp, lp := s.bindingPath(name), s.logPath(name)

	bpRaw, bpErr := os.ReadFile(bp)
	if bpErr != nil && !errors.Is(bpErr, os.ErrNotExist) {
		return fmt.Errorf("read binding %q: %w", name, bpErr)
	}
	bpPresent := bpErr == nil

	lpRaw, lpErr := os.ReadFile(lp)
	if lpErr != nil && !errors.Is(lpErr, os.ErrNotExist) {
		return fmt.Errorf("read log for %q: %w", name, lpErr)
	}
	lpPresent := lpErr == nil

	// The pre-P3a "<dir>/.viewed" sidecar carries what binding_record.viewed_at
	// holds now (#143). One stat decides both whether the import has anything to
	// do and, when it has, the stamp a record without one takes.
	viewedPath := s.ViewedPath(name)
	viewedInfo, viewedErr := os.Stat(viewedPath)
	if viewedErr != nil && !errors.Is(viewedErr, os.ErrNotExist) {
		return fmt.Errorf("stat viewed %q: %w", name, viewedErr)
	}
	viewedPresent := viewedErr == nil

	if !bpPresent && !lpPresent && !viewedPresent {
		return nil
	}

	d, err := s.dbForWrite()
	if err != nil {
		return err
	}

	if bpPresent {
		b, err := decodeBinding(bpRaw, name)
		if err != nil {
			return err
		}
		// record_json is the file's binding re-marshalled compactly: the
		// record is authoritative from here on, and the file goes away.
		raw, err := json.Marshal(b)
		if err != nil {
			return fmt.Errorf("marshal binding %q: %w", name, err)
		}
		if _, err := d.RecordPut(db.Record{
			Owner:     s.owner,
			Name:      b.Name,
			State:     string(b.State),
			Round:     b.Round,
			CWD:       b.CWD,
			JSON:      string(raw),
			CreatedAt: b.CreatedAt,
			UpdatedAt: b.UpdatedAt,
		}); err != nil {
			return fmt.Errorf("import binding %q: %w", name, err)
		}
	}

	if lpPresent {
		entries, err := decodeLog(bytes.NewReader(lpRaw))
		if err != nil {
			return fmt.Errorf("%q: %w", name, err)
		}
		rec, ok, err := d.RecordGet(s.owner, name)
		if err != nil {
			return fmt.Errorf("import log %q: %w", name, err)
		}
		if !ok {
			// A log without a record (a fork directory before its Save):
			// leave it alone and import nothing.
			return nil
		}

		// entry_json keeps each line's exact bytes, so a key a newer relevo
		// wrote survives the import (§3.1, §4.3).
		lines := splitLogLines(lpRaw)
		if len(lines) != len(entries) {
			return fmt.Errorf("import log %q: %d entries but %d lines", name, len(entries), len(lines))
		}
		evs := make([]db.RecordEvent, 0, len(entries))
		for i, e := range entries {
			evs = append(evs, recordEventOf(e, string(lines[i])))
		}
		if err := d.EventReplaceAll(rec.ID, evs); err != nil {
			return fmt.Errorf("import log %q: %w", name, err)
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
	// of its own, so an older file never overwrites a newer stamp; either way
	// the file goes once the record it belongs to has been read.
	if viewedPresent {
		rec, ok, err := d.RecordGet(s.owner, name)
		if err != nil {
			return fmt.Errorf("import viewed %q: %w", name, err)
		}
		if ok {
			if rec.ViewedAt == nil {
				if err := d.RecordSetViewed(s.owner, name, viewedInfo.ModTime()); err != nil {
					return fmt.Errorf("import viewed %q: %w", name, err)
				}
			}
			if err := os.Remove(viewedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove imported viewed %q: %w", name, err)
			}
		}
	}
	return nil
}

// importAll adopts every binding directory under the root that holds a
// bind.json, before list() reads the database. It reads the root the way list()
// did before the database: non-directories and dot-directories are skipped.
//
// A directory whose bind.json was written by a newer relevo is left exactly as
// it is and skipped, so one such file cannot make List -- and so Tick, status
// and gc -- fail for every other binding (§B4). load and importPresent for
// that name still return ErrNewerFormat.
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

// newerFormatWarned remembers the binding names importAll has already warned
// about in this process, so a newer-format binding is logged once per name per
// process however often List runs (§B4).
var newerFormatWarned sync.Map

// warnNewerFormatOnce logs the first -- and only the first -- skip of a
// binding whose bind.json was written by a newer relevo.
func warnNewerFormatOnce(name string, err error) {
	if _, loaded := newerFormatWarned.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	slog.Warn("leaving a binding written by a newer relevo alone", "binding", name, "err", err)
}

// splitLogLines returns a log.jsonl's non-empty lines, in order, so an
// imported entry's entry_json can keep the line's exact bytes. decodeLog has
// already validated the stream and assigned Seq positions by the time this
// runs, so the two agree line for line.
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

// recordEventOf projects a decoded LogEntry and its exact JSON onto the
// binding_event columns.
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

// logEntriesOf decodes stored events back into LogEntry values. entry_json is
// authoritative for the entry's content; Seq, Confirmed, DeliveredAt and Route
// come from the promoted columns, which confirmIndex maintains (§4.5).
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
