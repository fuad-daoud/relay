package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relay/internal/usage"
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
	KindPick     Kind = "pick"   // relay -> log only: which candidate a spawn resolved to and why (#61 step 2)
	KindSwitch   Kind = "switch" // relay -> log only: the builder was replaced mid-round, and why (#61 step 6)
	KindExit     Kind = "exit"   // relay -> log only: a headless builder exited without a report (#99)
	KindGate     Kind = "gate"   // relay -> log only: the gate started (#132)

	KindPause  Kind = "pause"  // relay -> log only: paused after round N, worktree and pane released (#137)
	KindResume Kind = "resume" // relay -> log only: resumed from PAUSED (#137)

	// KindStop is relay -> log only: a stop was requested (grace ...), or the
	// round closed because of one (stopped/graceful, stopped/killed,
	// stopped/abandoned) (#138).
	KindStop Kind = "stop"

	KindAsk      Kind = "ask"      // planner -> consult, the staged question
	KindFindings Kind = "findings" // consult -> planner, the findings path

	// KindLand is relay -> log only: `relay land` rebased the binding's
	// branch onto its base, ran the gate and pushed (#136). The note names
	// the branch, the base and the PR URL when one was created.
	KindLand Kind = "land"

	// KindEdge is a planner-declared handoff between bindings (#37): a
	// queued entry (DirToPlanner, a real payload) when an edge's Mode is
	// "queue" and its artifact is ready, or relay -> log only when the edge
	// is declared, fires, is skipped for a missing artifact, or its fire
	// fails.
	KindEdge Kind = "edge"

	// KindQueue is relay -> log only: a served round was queued, admitted or
	// re-queued (#285).
	KindQueue Kind = "queue"

	// KindRetired is relay -> log only: a legacy pane binding was closed at
	// upgrade because pane builders were removed (#303, §5.6). The binding's
	// state becomes DONE and its worktree is left exactly as it is.
	KindRetired Kind = "retired"
)

// LogEntry is one relayed message. An unconfirmed DirToPlanner entry is also
// relay's pending-delivery record, which is what makes a crash mid-delivery
// recoverable without a second file to keep in sync.
type LogEntry struct {
	// Seq is the 1-based position of the entry in its binding's log; assigned
	// by appendLog, filled on read for files written before Seq existed.
	// Callers never set it; appendLog overwrites whatever a caller passed.
	Seq         int        `json:"seq,omitempty"`
	TS          time.Time  `json:"ts"`
	Round       int        `json:"round"`
	Direction   Direction  `json:"direction"`
	Kind        Kind       `json:"kind"`
	Path        string     `json:"path,omitempty"`
	Payload     string     `json:"payload,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	Confirmed   bool       `json:"confirmed"`
	Note        string     `json:"note,omitempty"`
	Late        bool       `json:"late,omitempty"`
	// Tier is the permission tier the round was sent at, on plan entries (#141).
	Tier string `json:"tier,omitempty"`

	// Gate is the acceptance check's result, on report entries of a gated
	// round (#132). Nil when the binding has no gate or the entry predates it.
	Gate *GateRecord `json:"gate,omitempty"`

	// Commits and Tree are the round's commit facts, on diff entries only
	// (#130): commits added since the round's baseline HEAD, and whether the
	// worktree was "clean" or "dirty" at close. Tree == "" means the facts
	// are unknown and Commits is meaningless.
	Commits int    `json:"commits,omitempty"`
	Tree    string `json:"tree,omitempty"`

	// Usage is what the round consumed, on report entries, and what a
	// consult consumed, on findings entries (#142). Nil on every other
	// kind and on entries written before the field existed. Its Cost.Basis
	// says whether the dollars were measured, estimated or unknown; a
	// reader that treats nil as "free" is wrong -- nil is unknown.
	Usage *usage.Usage `json:"usage,omitempty"`

	// Rusage is what the supervisor measured for the round's systemd scope
	// (#244, #216), on report entries of a headless round the server ran
	// as a scope. Nil when the round was not a scope (a plain spawn, a
	// pane builder, or a pre-scope server), or when the trailer was absent.
	Rusage *Rusage `json:"rusage,omitempty"`

	// Outcome, HaltedAt, ChangedPaths, CommandsRun, and NotDone are parsed
	// from the builder's trailing relay block, on report entries only (#133).
	// Outcome is one of "done", "halted", "blocked", "deferred", or "unstructured";
	// an empty Outcome means the entry predates the field (readers treat "" like
	// "unstructured"). HaltedAt, ChangedPaths, CommandsRun, and NotDone are zero
	// when none was given.
	Outcome      string   `json:"outcome,omitempty"`
	HaltedAt     string   `json:"halted_at,omitempty"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	CommandsRun  []string `json:"commands_run,omitempty"`
	NotDone      []string `json:"not_done,omitempty"`

	// Verdict and Reasons are what a verify consult's findings parsed to
	// (#144), on findings entries of a verify consult with a readable block.
	// Verdict is "accepted" or "rejected"; empty on every other entry.
	// Reasons is empty when none was given or the block did not parse.
	Verdict string   `json:"verdict,omitempty"`
	Reasons []string `json:"reasons,omitempty"`

	// Flagged is the count of instruction-shaped lines detected in the report
	// or question body, on report and question entries (#139). Zero means nothing
	// was flagged or the entry predates the field.
	Flagged int `json:"flagged,omitempty"`

	// FlaggedBy says which judge produced Flagged (#211): "regex", "jev", or
	// "both". Empty when Flagged is 0 or the entry predates the field.
	FlaggedBy string `json:"flagged_by,omitempty"`

	// Classify is the classifier's record for this entry (#211), on report and
	// question entries when a classifier was configured for the round. Nil when
	// none was configured or the entry predates the field. When Note is set the
	// classifier did not answer and Paragraphs, Above, Max and InputTokens are
	// zero; the regex count in Flagged stands alone.
	Classify *ClassifyRecord `json:"classify,omitempty"`

	// BuilderSession names the harness session that built the round, on
	// report entries (#147). Nil when neither herdr nor the round's stream
	// named one -- never guessed.
	BuilderSession *BuilderSession `json:"builder_session,omitempty"`
}

// BuilderSession is the harness session a closed round's report names the
// builder by (#147).
type BuilderSession struct {
	Kind string `json:"kind"` // harness kind: claude | opencode | agy | codex
	ID   string `json:"id"`   // the harness's own session/conversation/thread id, verbatim
}

type ClassifyRecord struct {
	Provider    string  `json:"provider"`          // "jev"
	Model       string  `json:"model,omitempty"`   // from the response when it answered, else the configured name
	Threshold   float64 `json:"threshold"`         // the policy threshold used
	Paragraphs  int     `json:"paragraphs"`        // paragraphs sent (after Trim)
	Partial     bool    `json:"partial,omitempty"` // Trim dropped paragraphs from the middle
	Above       int     `json:"above"`             // paragraphs with p >= Threshold
	Max         float64 `json:"injection_max"`     // highest p seen, even below Threshold
	InputTokens int     `json:"input_tokens,omitempty"`
	Note        string  `json:"note,omitempty"` // failure reason, "classify: ..." form; empty on success
}

// GateRecord is the acceptance check's result, on report entries of a gated
// round (#132).
type GateRecord struct {
	Command    string `json:"command"`
	Result     string `json:"result"`    // "pass" | "fail" | "timeout" | "error"
	ExitCode   int    `json:"exit_code"` // meaningful for pass/fail
	DurationMS int64  `json:"duration_ms"`
	LogPath    string `json:"log_path"`
	Note       string `json:"note,omitempty"` // why "error": start failed, no runner, ...
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

// ReadLogAfter returns the entries whose Seq is greater than after, acquiring
// the state lock for the operation. It returns nil, nil when none match.
func (s *Store) ReadLogAfter(name string, after int) ([]LogEntry, error) {
	var entries []LogEntry
	err := s.WithLock(func(tx *Tx) error {
		var err error
		entries, err = tx.ReadLogAfter(name, after)
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

// ReadLogAfter reads the entries whose Seq is greater than after under the
// held lock.
func (t *Tx) ReadLogAfter(name string, after int) ([]LogEntry, error) {
	return t.s.readLogAfter(name, after)
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

// appendLog appends one entry, stamping TS when the caller left it zero and
// assigning Seq from the file itself.
func (s *Store) appendLog(name string, e LogEntry) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}

	// Seq is the number of newline-terminated lines already in the file, plus
	// one. Reading the whole file is cheap -- maxLogEntries caps it at 10000
	// lines -- and counting bytes '\n' needs no decode. A file that is absent
	// counts zero.
	raw, err := os.ReadFile(s.logPath(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read log for %q: %w", name, err)
	}
	e.Seq = bytes.Count(raw, []byte{'\n'}) + 1

	encoded, err := json.Marshal(e)
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

	if _, err := f.Write(append(encoded, '\n')); err != nil {
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

	entries, err := decodeLog(f)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", name, err)
	}
	return entries, nil
}

// readLogAfter returns the entries whose Seq is greater than after. A log
// with no such entry yields nil, nil.
func (s *Store) readLogAfter(name string, after int) ([]LogEntry, error) {
	entries, err := s.readLog(name)
	if err != nil {
		return nil, err
	}

	var out []LogEntry
	for _, e := range entries {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out, nil
}

// decodeLog scans a log.jsonl stream, one LogEntry per line, refusing to
// read past maxLogEntries rather than silently truncating: since a log is
// append-only, truncating would drop the newest entries. Shared by readLog
// (the live file) and ReadArchivedLog (a tarball member).
//
// A file written before Seq existed has no seq key: each decoded entry's Seq
// is then its 1-based position among the decoded entries, so such a file
// reads back exactly as a new one would, and nothing is rewritten.
func decodeLog(r io.Reader) ([]LogEntry, error) {
	entries := make([]LogEntry, 0, 64)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var e LogEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("decode log entry: %w", err)
		}

		// i counts decoded entries, so a skipped empty line does not advance
		// the numbering.
		if e.Seq == 0 {
			e.Seq = len(entries) + 1
		}

		entries = append(entries, e)
		if len(entries) > maxLogEntries {
			return nil, fmt.Errorf("log exceeds %d entries", maxLogEntries)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan log: %w", err)
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
