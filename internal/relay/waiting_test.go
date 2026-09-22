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

func TestWaitingOn(t *testing.T) {
	const (
		answerHint = "relay answer --name api"
		statusHint = "relay status --name api"
		rebindHint = "relay bind --resume --name api --rebind"
		bindHint   = "relay bind --resume --name api"
	)

	t.Run("active is not waiting", func(t *testing.T) {
		b := store.Binding{Name: "api", Round: 4, State: store.StateActive}
		if _, ok := WaitingOn(b, nil, mapQuestion(nil)); ok {
			t.Error("an active binding must not be waiting")
		}
	})

	t.Run("held is not waiting", func(t *testing.T) {
		b := store.Binding{Name: "api", Round: 4, State: store.StateHeld}
		if _, ok := WaitingOn(b, nil, mapQuestion(nil)); ok {
			t.Error("a held binding must not be waiting")
		}
	})

	t.Run("done is not waiting", func(t *testing.T) {
		b := store.Binding{Name: "api", Round: 4, State: store.StateDone}
		if _, ok := WaitingOn(b, nil, mapQuestion(nil)); ok {
			t.Error("a done binding must not be waiting")
		}
	})

	t.Run("needs_you with a question entry is blocked", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{Name: "api", Round: 4, State: store.StateNeedsYou}
		entries := []store.LogEntry{
			{Round: 4, Direction: store.DirToPlanner, Kind: store.KindQuestion, TS: ts, Path: "/x/api/004-question.md"},
		}
		q := mapQuestion(map[string]string{"api:4": "Do you want to proceed?"})

		w, ok := WaitingOn(b, entries, q)
		if !ok {
			t.Fatal("want ok")
		}
		if w.Cause != "blocked" || w.Line != "Do you want to proceed?" || !w.Since.Equal(ts) || w.Hint != answerHint {
			t.Errorf("Waiting = %+v", w)
		}
	})

	t.Run("blocked wins over a halt on the same binding", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{
			Name: "api", Round: 4, State: store.StateNeedsYou,
			Halt: "round 4 has run past 2h0m0s", HaltAt: ts.Add(time.Hour),
		}
		entries := []store.LogEntry{
			{Round: 4, Direction: store.DirToPlanner, Kind: store.KindQuestion, TS: ts, Path: "/x/api/004-question.md"},
		}
		q := mapQuestion(map[string]string{"api:4": "Do you want to proceed?"})

		w, ok := WaitingOn(b, entries, q)
		if !ok {
			t.Fatal("want ok")
		}
		if w.Cause != "blocked" {
			t.Errorf("Cause = %q, want blocked: blocked must be checked before halted", w.Cause)
		}
	})

	t.Run("needs_you with only a halt is halted", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{
			Name: "api", Round: 4, State: store.StateNeedsYou,
			Halt: "round 4 has run past 2h0m0s", HaltAt: ts,
		}

		w, ok := WaitingOn(b, nil, mapQuestion(nil))
		if !ok {
			t.Fatal("want ok")
		}
		if w.Cause != "halted" || w.Line != "round 4 has run past 2h0m0s" || !w.Since.Equal(ts) || w.Hint != statusHint {
			t.Errorf("Waiting = %+v", w)
		}
	})

	t.Run("needs_you with neither is honest", func(t *testing.T) {
		b := store.Binding{Name: "api", Round: 4, State: store.StateNeedsYou}

		w, ok := WaitingOn(b, nil, mapQuestion(nil))
		if !ok {
			t.Fatal("want ok")
		}
		if w.Cause != "needs you" ||
			w.Line != "no reason recorded (binding predates the halt record)" ||
			!w.Since.IsZero() || w.Hint != statusHint {
			t.Errorf("Waiting = %+v", w)
		}
	})

	t.Run("broken and switchable is not yet a human's problem", func(t *testing.T) {
		b := store.Binding{
			Name: "api", Round: 4, State: store.StateBroken,
			BuilderCandidate: "agy/x/y", RoundStartedAt: time.Unix(1757000000, 0),
		}
		if _, ok := WaitingOn(b, nil, mapQuestion(nil)); ok {
			t.Error("a switchable broken binding must not be waiting")
		}
	})

	t.Run("broken and adopted, identified", func(t *testing.T) {
		since := time.Unix(1757000000, 0).UTC()
		b := store.Binding{
			Name: "api", Round: 4, State: store.StateBroken,
			Builder: store.Endpoint{AgentName: "b"}, BuilderMissingSince: since,
		}

		w, ok := WaitingOn(b, nil, mapQuestion(nil))
		if !ok {
			t.Fatal("want ok")
		}
		want := DiagnoseBuilder(b).Detail(b.Round)
		if w.Cause != "broken" || w.Line != want || !w.Since.Equal(since) || w.Hint != rebindHint {
			t.Errorf("Waiting = %+v, want line %q", w, want)
		}
	})

	t.Run("broken and unidentified hints status", func(t *testing.T) {
		b := store.Binding{Name: "api", Round: 4, State: store.StateBroken}

		w, ok := WaitingOn(b, nil, mapQuestion(nil))
		if !ok {
			t.Fatal("want ok")
		}
		if w.Hint != statusHint {
			t.Errorf("Hint = %q, want %q", w.Hint, statusHint)
		}
	})

	t.Run("broken with a candidate but a closed round still counts", func(t *testing.T) {
		b := store.Binding{
			Name: "api", Round: 4, State: store.StateBroken,
			BuilderCandidate: "agy/x/y", RoundStartedAt: time.Time{},
			Builder: store.Endpoint{AgentName: "b"},
		}
		if _, ok := WaitingOn(b, nil, mapQuestion(nil)); !ok {
			t.Error("a broken binding with a candidate but a closed round must count")
		}
	})

	t.Run("orphaned", func(t *testing.T) {
		b := store.Binding{Name: "api", Round: 4, State: store.StateOrphaned}

		w, ok := WaitingOn(b, nil, mapQuestion(nil))
		if !ok {
			t.Fatal("want ok")
		}
		if w.Cause != "orphaned" || w.Line != "planner pane is gone" || !w.Since.IsZero() || w.Hint != bindHint {
			t.Errorf("Waiting = %+v", w)
		}
	})

	t.Run("a long dialog line is capped at 120 runes", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{Name: "api", Round: 4, State: store.StateNeedsYou}
		entries := []store.LogEntry{
			{Round: 4, Direction: store.DirToPlanner, Kind: store.KindQuestion, TS: ts, Path: "/x/api/004-question.md"},
		}
		q := mapQuestion(map[string]string{"api:4": strings.Repeat("x", 200)})

		w, ok := WaitingOn(b, entries, q)
		if !ok {
			t.Fatal("want ok")
		}
		r := []rune(w.Line)
		if len(r) != 120 || r[119] != '…' {
			t.Errorf("Line = %q (%d runes), want 120 runes ending in …", w.Line, len(r))
		}
	})

	t.Run("a blank first line falls through to the second", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{Name: "api", Round: 4, State: store.StateNeedsYou}
		entries := []store.LogEntry{
			{Round: 4, Direction: store.DirToPlanner, Kind: store.KindQuestion, TS: ts, Path: "/x/api/004-question.md"},
		}
		q := mapQuestion(map[string]string{"api:4": "\n\n  second  \n"})

		w, ok := WaitingOn(b, entries, q)
		if !ok {
			t.Fatal("want ok")
		}
		if w.Line != "second" {
			t.Errorf("Line = %q, want %q", w.Line, "second")
		}
	})

	t.Run("an unreadable question file falls back to the captured path", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{Name: "api", Round: 4, State: store.StateNeedsYou}
		entries := []store.LogEntry{
			{Round: 4, Direction: store.DirToPlanner, Kind: store.KindQuestion, TS: ts, Path: "/x/api/001-question.md"},
		}

		w, ok := WaitingOn(b, entries, mapQuestion(nil))
		if !ok {
			t.Fatal("want ok")
		}
		want := "dialog captured at /x/api/001-question.md"
		if w.Line != want {
			t.Errorf("Line = %q, want %q", w.Line, want)
		}
	})
}

func TestQuestionFirstLine(t *testing.T) {
	f := &fakePanes{}
	rt := newRuntime(t, f)
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
	f := &fakePanes{}
	rt := newRuntime(t, f)

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
