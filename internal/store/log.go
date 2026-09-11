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

// maxLogEntries is a corruption guard, not a rotation policy: a log growing
// past it means something has gone wrong (a runaway loop, a corrupted
// file), so ReadLog refuses to read further rather than silently returning
// a truncated log that would make PendingForPlanner and ConfirmIndex miss
// the newest entries.
const maxLogEntries = 10000

// Direction is which way a message travelled.
type Direction string

const (
	DirToBuilder Direction = "to_builder"
	DirToPlanner Direction = "to_planner"

	// DirToConsult is an outbound message to a consult. It is additive: every
	// consumer of Direction tests equality against a specific value, and there
	// is no exhaustive switch in the tree. Reusing DirToBuilder would instead
	// redefine what a persisted value means.
	DirToConsult Direction = "to_consult"
)

// Kind is what sort of message it was.
type Kind string

const (
	KindPlan     Kind = "plan"
	KindReport   Kind = "report"
	KindQuestion Kind = "question"
	KindAnswer   Kind = "answer"
	KindDiff     Kind = "diff"
	KindDrift    Kind = "drift"
	KindFork     Kind = "fork"
	KindPick     Kind = "pick" // relay -> log only: which candidate a spawn resolved to and why (#61 step 2)

	KindAsk      Kind = "ask"      // planner -> consult, the staged question
	KindFindings Kind = "findings" // consult -> planner, the findings path
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

// PendingForPlanner returns the oldest undelivered payload bound for the
// planner, acquiring the state lock for the operation. It drops the entry's
// index: a caller that only reads cannot confirm, and a caller that intends to
// confirm must hold the lock across both calls and so must go through Tx.
func (s *Store) PendingForPlanner(name string) (LogEntry, bool, error) {
	var e LogEntry
	var found bool
	err := s.WithLock(func(tx *Tx) error {
		var err error
		e, _, found, err = tx.PendingForPlanner(name)
		return err
	})
	return e, found, err
}

// ConfirmIndex marks the entry at idx as delivered, acquiring the state lock
// for the operation.
func (s *Store) ConfirmIndex(name string, idx int) error {
	return s.WithLock(func(tx *Tx) error { return tx.ConfirmIndex(name, idx) })
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

// PendingForPlanner reads the oldest undelivered planner payload and its index
// under the held lock.
func (t *Tx) PendingForPlanner(name string) (LogEntry, int, bool, error) {
	return t.s.pendingForPlanner(name)
}

// ConfirmIndex rewrites the log under the held lock, marking the entry at idx
// delivered.
func (t *Tx) ConfirmIndex(name string, idx int) error {
	return t.s.confirmIndex(name, idx)
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

// readLog returns every entry in order, refusing to read past
// maxLogEntries rather than silently truncating: since the log is
// append-only, truncating would drop the newest entries, which is exactly
// what pendingForPlanner and confirmIndex need.
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

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var e LogEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("decode log entry for %q: %w", name, err)
		}

		entries = append(entries, e)
		if len(entries) > maxLogEntries {
			return nil, fmt.Errorf("log for %q exceeds %d entries", name, maxLogEntries)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan log for %q: %w", name, err)
	}

	return entries, nil
}

// pendingForPlanner returns the OLDEST undelivered payload bound for the
// planner and its index in the log.
//
// Oldest-first because arrival order is the only order relay can defend
// without judging content. Under the newest-first scan this replaced, a round
// report queued before two consult findings was delivered after both of them.
func (s *Store) pendingForPlanner(name string) (LogEntry, int, bool, error) {
	entries, err := s.readLog(name)
	if err != nil {
		return LogEntry{}, 0, false, err
	}

	for i, e := range entries {
		if e.Direction == DirToPlanner && !e.Confirmed {
			return e, i, true, nil
		}
	}

	return LogEntry{}, 0, false, nil
}

// confirmIndex marks one entry as delivered by rewriting the log. The log is
// small and append-only, so a full rewrite is simpler and safer than in-place
// mutation.
//
// It takes an index rather than re-deriving "the entry we must have meant"
// because the pair it replaced -- pendingForPlanner and confirmLatest -- agreed
// only by both scanning for the newest unconfirmed entry. That coupling was
// implicit and survived exactly as long as nobody edited one of them. Callers
// hold the state lock across both calls, so the index is stable.
func (s *Store) confirmIndex(name string, idx int) error {
	entries, err := s.readLog(name)
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(entries) {
		return fmt.Errorf("confirm entry %d for %q: log has %d entries", idx, name, len(entries))
	}
	if entries[idx].Confirmed {
		return nil
	}

	now := time.Now().UTC()
	entries[idx].Confirmed = true
	entries[idx].DeliveredAt = &now

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
