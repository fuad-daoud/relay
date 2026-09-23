package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestOriginLine(t *testing.T) {
	t.Run("to builder byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 2, store.DirToBuilder, store.KindPlan)
		want := `relevo: round 2 · to builder "b1" · from the planner (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("to planner report byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 2, store.DirToPlanner, store.KindReport)
		want := `relevo: round 2 · to planner · about builder "b1" (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("to planner question byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 3, store.DirToPlanner, store.KindQuestion)
		want := `relevo: round 3 · to planner · about builder "b1" (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("to planner findings byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 1, store.DirToPlanner, store.KindFindings)
		want := `relevo: consult · to planner · about builder "b1" (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestWithOrigin(t *testing.T) {
	origin := `relevo: round 1 · to planner · about builder "b1" (not the human)`

	t.Run("plain payload inserts exactly one blank line", func(t *testing.T) {
		payload := "Builder finished round 1\nReport: /path/to/report"
		got := WithOrigin(payload, origin)
		want := origin + "\n\n" + payload
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("already prefixed returns unchanged", func(t *testing.T) {
		prefixed := origin + "\n\nBuilder finished round 1"
		got := WithOrigin(prefixed, origin)
		if got != prefixed {
			t.Fatalf("got %q, want %q", got, prefixed)
		}
	})

	t.Run("already prefixed with leading whitespace returns unchanged", func(t *testing.T) {
		prefixed := "  " + origin + "\n\nBuilder finished round 1"
		got := WithOrigin(prefixed, origin)
		if got != prefixed {
			t.Fatalf("got %q, want %q", got, prefixed)
		}
	})
}
