// Package relevo waiting: who is waiting on a human, and why, per
// docs/specs/2026-09-14-wait-and-waiting-on-you-design.md §4.1-4.4.
package relevo

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Waiting is what one binding is waiting on: a human decision it cannot make
// progress without, and how long it has been waiting for it.
type Waiting struct {
	Name  string    `json:"name"`            // binding
	Round int       `json:"round"`           // b.Round as stored
	Cause string    `json:"cause"`           // "blocked" | "halted" | "broken" | "needs you"
	Line  string    `json:"line"`            // what it is waiting on, one line, <= 120 runes, never empty
	Since time.Time `json:"since,omitempty"` // when it started waiting; zero when relevo does not know
	Hint  string    `json:"hint"`            // the relevo verb that resolves it, e.g. `relevo answer --name api`
}

// switchable mirrors Reconcile's inline expression (reconcile.go): a
// switchable broken binding is one the daemon will fix itself within
// switchGrace, so it is not (yet) a human's problem. Duplicated rather than
// factored out, since reconcile.go is not touched by this change.
func switchable(b store.Binding) bool {
	return b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()
}

// capLine returns the first non-blank line of s, trimmed, truncated to n
// runes with a trailing "…" when longer.
func capLine(s string, n int) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r := []rune(line)
		if len(r) > n {
			return string(r[:n-1]) + "…"
		}
		return line
	}
	return ""
}

// questionEntry returns the to_planner/question log entry for round, if one
// exists. Mirrors HasEntry's match, but returns the entry itself: WaitingOn
// needs its TS and Path.
func questionEntry(entries []store.LogEntry, round int) (store.LogEntry, bool) {
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToPlanner && e.Kind == store.KindQuestion && e.Note != nudgeNote {
			return e, true
		}
	}
	return store.LogEntry{}, false
}

// WaitingOn classifies a binding plus its log into what it is waiting on, per
// spec §4.1. Pure apart from questionOf, which the caller supplies
// (production: questionFirstLine; tests: a map).
//
// ok is false for every state except needs_you and broken, and for a
// switchable broken binding (decision 5): that one is transient, and the
// daemon is about to act on it itself.
func WaitingOn(b store.Binding, entries []store.LogEntry, questionOf func(name string, round int) string) (Waiting, bool) {
	switch b.State {
	case store.StateNeedsYou:
		if e, ok := questionEntry(entries, b.Round); ok {
			line := questionOf(b.Name, b.Round)
			if line == "" {
				line = "dialog captured at " + e.Path
			}
			return Waiting{
				Name: b.Name, Round: b.Round, Cause: "blocked",
				Line: capLine(line, 120), Since: e.TS,
				Hint: "relevo answer --name " + b.Name,
			}, true
		}
		if b.Halt != "" {
			return Waiting{
				Name: b.Name, Round: b.Round, Cause: "halted",
				Line: capLine(b.Halt, 120), Since: b.HaltAt,
				Hint: "relevo status --name " + b.Name,
			}, true
		}
		return Waiting{
			Name: b.Name, Round: b.Round, Cause: "needs you",
			Line: capLine("no reason recorded (binding predates the halt record)", 120),
			Hint: "relevo status --name " + b.Name,
		}, true

	case store.StateBroken:
		if switchable(b) {
			return Waiting{}, false
		}
		d := DiagnoseBuilder(b)
		return Waiting{
			Name: b.Name, Round: b.Round, Cause: "broken",
			Line: capLine(d.Detail(b.Round), 120), Since: b.BuilderMissingSince,
			Hint: "relevo bind --resume --name " + b.Name + " --rebind",
		}, true

	default:
		return Waiting{}, false
	}
}

// questionFirstLine reads the first non-blank line of the round's captured
// question file, trimmed. "" on any error (missing or unreadable file):
// never an error, since the classifier has a fallback (spec §4.2). The one
// place WaitingOn's callers touch the filesystem beyond the store.
func questionFirstLine(rt Runtime) func(name string, round int) string {
	return func(name string, round int) string {
		data, err := os.ReadFile(rt.Store.QuestionPath(name, round))
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				return line
			}
		}
		return ""
	}
}

// WaitingLine renders the one-line reminder a human reads, per spec §4.3:
//
//	waiting on you: <name> round <N> <cause>[ <age>] -- <line>  (<hint>)
//
// <age> is AgeText(now.Sub(w.Since)), omitted (with its leading space) when
// Since is zero.
func WaitingLine(w Waiting, now time.Time) string {
	head := fmt.Sprintf("waiting on you: %s round %d %s", w.Name, w.Round, w.Cause)
	if !w.Since.IsZero() {
		head += " " + AgeText(now.Sub(w.Since))
	}
	return fmt.Sprintf("%s -- %s  (%s)", head, w.Line, w.Hint)
}

// WaitingOnYou lists, in Store.List order, one line per binding (other than
// except) that is waiting on a human, per spec §4.4. Store-backed.
func WaitingOnYou(rt Runtime, except string) ([]string, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return nil, err
	}

	qf := questionFirstLine(rt)
	var lines []string
	for _, b := range bindings {
		if b.Name == except || b.State == store.StateDone {
			continue
		}
		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			return nil, err
		}
		w, ok := WaitingOn(b, entries, qf)
		if !ok {
			continue
		}
		lines = append(lines, WaitingLine(w, rt.Now().UTC()))
	}
	return lines, nil
}
