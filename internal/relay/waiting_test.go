package relay

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// mapQuestion turns a map keyed "name:round" into a questionOf func for
// WaitingOn's tests.
func mapQuestion(m map[string]string) func(name string, round int) string {
	return func(name string, round int) string {
		return m[fmt.Sprintf("%s:%d", name, round)]
	}
}

func TestQuestionFirstLine(t *testing.T) {
	rt := newRuntime(t)
	if err := rt.Store.Save(store.Binding{Name: "api", CWD: "/repo"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := rt.Store.QuestionPath("api", 1)
	if err := os.WriteFile(path, []byte("\nDo you want to proceed?\nmore text\n"), 0o644); err != nil {
		t.Fatalf("write question: %v", err)
	}

	qf := questionFirstLine(rt)
	if got := qf("api", 1); got != "Do you want to proceed?" {
		t.Errorf("questionFirstLine = %q, want %q", got, "Do you want to proceed?")
	}
	if got := qf("api", 2); got != "" {
		t.Errorf("questionFirstLine for a missing file = %q, want empty", got)
	}
}

func TestWaitingLine(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	t.Run("blocked with a sub-hour age", func(t *testing.T) {
		w := Waiting{
			Name: "e2e-herdr", Round: 4, Cause: "blocked",
			Line: "Do you want to proceed? > 1. Yes", Since: now.Add(-23 * time.Minute),
			Hint: "relay answer --name e2e-herdr",
		}
		want := "waiting on you: e2e-herdr round 4 blocked 23m -- Do you want to proceed? > 1. Yes  (relay answer --name e2e-herdr)"
		if got := WaitingLine(w, now); got != want {
			t.Errorf("WaitingLine =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("halted with an hour-plus age", func(t *testing.T) {
		w := Waiting{
			Name: "spaceapi", Round: 2, Cause: "halted",
			Line:  "builder gone for 30s (agy/google/x); already switched 2 time(s) this round (max_switches 2)",
			Since: now.Add(-(1*time.Hour + 5*time.Minute)),
			Hint:  "relay status --name spaceapi",
		}
		want := "waiting on you: spaceapi round 2 halted 1h 5m -- builder gone for 30s (agy/google/x); already switched 2 time(s) this round (max_switches 2)  (relay status --name spaceapi)"
		if got := WaitingLine(w, now); got != want {
			t.Errorf("WaitingLine =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("orphaned with a zero Since has no age", func(t *testing.T) {
		w := Waiting{
			Name: "old", Round: 3, Cause: "orphaned",
			Line: "planner pane is gone", Hint: "relay bind --resume --name old",
		}
		want := "waiting on you: old round 3 orphaned -- planner pane is gone  (relay bind --resume --name old)"
		if got := WaitingLine(w, now); got != want {
			t.Errorf("WaitingLine =\n%q\nwant\n%q", got, want)
		}
	})
}

func TestWaitingOnYou(t *testing.T) {
	rt := newRuntime(t)

	a := store.Binding{Name: "a", CWD: "/repo/a", Round: 1, State: store.StateNeedsYou}
	if err := rt.Store.Save(a); err != nil {
		t.Fatalf("Save a: %v", err)
	}
	qPath := rt.Store.QuestionPath("a", 1)
	if err := os.WriteFile(qPath, []byte("Do you want to proceed?"), 0o644); err != nil {
		t.Fatalf("write question: %v", err)
	}
	if err := rt.Store.AppendLog("a", store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToPlanner, Kind: store.KindQuestion,
		Path: qPath, Payload: "Builder is blocked at a dialog.",
	}); err != nil {
		t.Fatalf("AppendLog a: %v", err)
	}

	b := store.Binding{Name: "b", CWD: "/repo/b", Round: 1, State: store.StateActive}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save b: %v", err)
	}

	c := store.Binding{Name: "c", CWD: "/repo/c", Round: 1, State: store.StateDone}
	if err := rt.Store.Save(c); err != nil {
		t.Fatalf("Save c: %v", err)
	}

	d := store.Binding{
		Name: "d", CWD: "/repo/d", Round: 2, State: store.StateNeedsYou,
		Halt: "round 2 has run past 2h0m0s", HaltAt: rt.Now().UTC(),
	}
	if err := rt.Store.Save(d); err != nil {
		t.Fatalf("Save d: %v", err)
	}

	lines, err := WaitingOnYou(rt, "d")
	if err != nil {
		t.Fatalf("WaitingOnYou: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want exactly 1", lines)
	}
	if !strings.HasPrefix(lines[0], "waiting on you: a round ") {
		t.Errorf("line = %q, want prefix %q", lines[0], "waiting on you: a round ")
	}
	if !strings.Contains(lines[0], "Do you want to proceed?") {
		t.Errorf("line = %q, want it to contain the question", lines[0])
	}
	if !strings.Contains(lines[0], "(relay answer --name a)") {
		t.Errorf("line = %q, want the answer hint", lines[0])
	}

	all, err := WaitingOnYou(rt, "")
	if err != nil {
		t.Fatalf("WaitingOnYou: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("lines = %v, want exactly 2", all)
	}
	if !strings.HasPrefix(all[0], "waiting on you: a round ") || !strings.HasPrefix(all[1], "waiting on you: d round ") {
		t.Errorf("lines = %v, want a then d (List order)", all)
	}
}
