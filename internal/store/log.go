package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// maxLogEntries bounds a single ReadLog, so a long-lived binding can never
// make the daemon's tick unbounded.
const maxLogEntries = 10000

// Direction is which way a message travelled.
type Direction string

const (
	DirToBuilder Direction = "to_builder"
	DirToPlanner Direction = "to_planner"
)

// Kind is what sort of message it was.
type Kind string

const (
	KindPlan     Kind = "plan"
	KindReport   Kind = "report"
	KindQuestion Kind = "question"
	KindAnswer   Kind = "answer"
)

// LogEntry is one relayed message. An unconfirmed DirToPlanner entry is also
// relay's pending-delivery record, which is what makes a crash mid-delivery
// recoverable without a second file to keep in sync.
type LogEntry struct {
	TS          time.Time  `json:"ts"`
	Round       int        `json:"round"`
	Direction   Direction  `json:"direction"`
	Kind        Kind       `json:"kind"`
	Path        string     `json:"path,omitempty"`
	Payload     string     `json:"payload,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	Confirmed   bool       `json:"confirmed"`
	Note        string     `json:"note,omitempty"`
}

func (s *Store) logPath(name string) string {
	return filepath.Join(s.Dir(name), "log.jsonl")
}

// AppendLog appends one entry, acquiring the state lock for the operation.
func (s *Store) AppendLog(name string, e LogEntry) error {
	return s.WithLock(func(tx *Tx) error { return tx.AppendLog(name, e) })
}

// ReadLog returns every entry in order, acquiring the state lock for the
// operation.
func (s *Store) ReadLog(name string) ([]LogEntry, error) {
	var entries []LogEntry
	err := s.WithLock(func(tx *Tx) error {
		var err error
		entries, err = tx.ReadLog(name)
		return err
	})
	return entries, err
}

// PendingForPlanner returns the newest undelivered payload bound for the
// planner, acquiring the state lock for the operation.
func (s *Store) PendingForPlanner(name string) (LogEntry, bool, error) {
	var e LogEntry
	var found bool
	err := s.WithLock(func(tx *Tx) error {
		var err error
		e, found, err = tx.PendingForPlanner(name)
		return err
	})
	return e, found, err
}

// ConfirmLatest marks the newest unconfirmed planner-bound entry as
// delivered, acquiring the state lock for the operation.
func (s *Store) ConfirmLatest(name string) error {
	return s.WithLock(func(tx *Tx) error { return tx.ConfirmLatest(name) })
}

// Tx methods provide locked access to the log. All assume the lock is held.

// AppendLog appends one entry under the held lock.
func (t *Tx) AppendLog(name string, e LogEntry) error {
	return t.s.appendLog(name, e)
}

// ReadLog reads every entry under the held lock.
func (t *Tx) ReadLog(name string) ([]LogEntry, error) {
	return t.s.readLog(name)
}

// PendingForPlanner reads the newest undelivered planner payload under the
// held lock.
func (t *Tx) PendingForPlanner(name string) (LogEntry, bool, error) {
	return t.s.pendingForPlanner(name)
}

// ConfirmLatest rewrites the log under the held lock, marking the newest
// unconfirmed planner-bound entry as delivered.
func (t *Tx) ConfirmLatest(name string) error {
	return t.s.confirmLatest(name)
}

// Unexported methods implement the actual logic, assuming the lock is held
// via Tx. They never take the lock themselves.

// appendLog appends one entry, stamping TS when the caller left it zero.
func (s *Store) appendLog(name string, e LogEntry) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}

	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode log entry: %w", err)
	}

	if err := os.MkdirAll(s.Dir(name), bindingDirMode); err != nil {
		return fmt.Errorf("create binding dir: %w", err)
	}

	f, err := os.OpenFile(s.logPath(name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, bindingFileMode)
	if err != nil {
		return fmt.Errorf("open log for %q: %w", name, err)
	}
	defer f.Close()

	if _, err := f.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("append log for %q: %w", name, err)
	}

	return nil
}

// readLog returns every entry in order, capped at maxLogEntries.
func (s *Store) readLog(name string) ([]LogEntry, error) {
	f, err := os.Open(s.logPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open log for %q: %w", name, err)
	}
	defer f.Close()

	entries := make([]LogEntry, 0, 64)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() && len(entries) < maxLogEntries {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("decode log entry for %q: %w", name, err)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan log for %q: %w", name, err)
	}

	return entries, nil
}

// pendingForPlanner returns the newest undelivered payload bound for the
// planner.
func (s *Store) pendingForPlanner(name string) (LogEntry, bool, error) {
	entries, err := s.readLog(name)
	if err != nil {
		return LogEntry{}, false, err
	}

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Direction == DirToPlanner && !e.Confirmed {
			return e, true, nil
		}
	}

	return LogEntry{}, false, nil
}

// confirmLatest marks the newest unconfirmed planner-bound entry as
// delivered by rewriting the log. The log is small and append-only, so a
// full rewrite is simpler and safer than in-place mutation.
func (s *Store) confirmLatest(name string) error {
	entries, err := s.readLog(name)
	if err != nil {
		return err
	}

	confirmed := false
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Direction == DirToPlanner && !entries[i].Confirmed {
			now := time.Now().UTC()
			entries[i].Confirmed = true
			entries[i].DeliveredAt = &now
			confirmed = true
			break
		}
	}
	if !confirmed {
		return nil
	}

	var buf []byte
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("encode log entry: %w", err)
		}
		buf = append(buf, raw...)
		buf = append(buf, '\n')
	}

	return writeFileAtomic(s.logPath(name), buf, bindingFileMode)
}
