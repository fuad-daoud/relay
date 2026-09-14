package relay

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

func TestDefaultWaitRound(t *testing.T) {
	t.Run("no entries falls back to b.Round", func(t *testing.T) {
		b := store.Binding{Round: 1}
		if got := DefaultWaitRound(b, nil); got != 1 {
			t.Errorf("DefaultWaitRound = %d, want 1", got)
		}
	})

	t.Run("an open round's plan entry is the default", func(t *testing.T) {
		b := store.Binding{Round: 3}
		entries := []store.LogEntry{
			{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPlan},
		}
		if got := DefaultWaitRound(b, entries); got != 3 {
			t.Errorf("DefaultWaitRound = %d, want 3", got)
		}
	})

	t.Run("after a close, the newest planned round wins over b.Round", func(t *testing.T) {
		b := store.Binding{Round: 4}
		entries := []store.LogEntry{
			{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPlan},
			{Round: 3, Direction: store.DirToPlanner, Kind: store.KindReport},
		}
		if got := DefaultWaitRound(b, entries); got != 3 {
			t.Errorf("DefaultWaitRound = %d, want 3", got)
		}
	})

	t.Run("a nudge is not a send", func(t *testing.T) {
		b := store.Binding{Round: 4}
		entries := []store.LogEntry{
			{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPlan},
			{Round: 3, Direction: store.DirToPlanner, Kind: store.KindReport},
			{Round: 4, Direction: store.DirToBuilder, Kind: store.KindPlan, Note: nudgeNote},
		}
		if got := DefaultWaitRound(b, entries); got != 3 {
			t.Errorf("DefaultWaitRound = %d, want 3 (the round-4 entry is a nudge, not a send)", got)
		}
	})
}

func TestWaitOutcome(t *testing.T) {
	noQuestion := mapQuestion(nil)

	t.Run("a marked close is WaitClosed", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("unmarked is WaitUnmarked with the report path", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: "unmarked"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitUnmarked, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("scraped is WaitUnmarked with the report path", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: "scraped"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitUnmarked, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("noreport is WaitUnmarked with a dash", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: "noreport"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitUnmarked, Line: "-", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("DONE with no report is WaitGone", func(t *testing.T) {
		b := store.Binding{Round: 1, State: store.StateDone}
		got := WaitOutcome(b, nil, 1, noQuestion)
		want := WaitResult{Code: WaitGone, Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("needs_you with a halt is WaitNeedsYou", func(t *testing.T) {
		b := store.Binding{
			Round: 1, State: store.StateNeedsYou,
			Halt: "round 1 has run past 2h0m0s", HaltAt: time.Unix(1757000000, 0).UTC(),
		}
		got := WaitOutcome(b, nil, 1, noQuestion)
		if got.Code != WaitNeedsYou || got.Line != b.Halt || !got.Done {
			t.Errorf("WaitOutcome = %+v", got)
		}
	})

	t.Run("active with an open round and no report is not done", func(t *testing.T) {
		b := store.Binding{Round: 1, State: store.StateActive}
		got := WaitOutcome(b, nil, 1, noQuestion)
		if got.Done {
			t.Errorf("WaitOutcome = %+v, want Done == false", got)
		}
	})

	t.Run("DONE with a report for the asked round: the report wins", func(t *testing.T) {
		b := store.Binding{Round: 2, State: store.StateDone}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("broken after a clean close, asked for the closed round, still reports the close", func(t *testing.T) {
		b := store.Binding{Round: 2, State: store.StateBroken}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("the same broken binding asked for the next, unsent round needs a human", func(t *testing.T) {
		b := store.Binding{Round: 2, State: store.StateBroken}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 2, noQuestion)
		if got.Code != WaitNeedsYou || !got.Done {
			t.Errorf("WaitOutcome = %+v", got)
		}
	})
}
