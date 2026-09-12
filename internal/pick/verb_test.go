package pick

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

// TestRowsForFiltersPerVerb pins the spec §4 table.
func TestRowsForFiltersPerVerb(t *testing.T) {
	// State is what HideDone keys on; Display is derived from it in a real
	// report. A row with Display "DONE" and no State cannot occur, so the
	// fixture sets both -- round 1 set only Display and the test could not
	// pass against the real HideDone.
	rep := relay.Report{Bindings: []relay.BindingStatus{
		{Name: "active", State: string(store.StateActive), Display: "ACTIVE", BuilderStatus: herdr.StatusWorking},
		{Name: "blocked", State: string(store.StateNeedsYou), Display: "NEEDS YOU", BuilderStatus: herdr.StatusBlocked},
		{Name: "finished", State: string(store.StateDone), Display: "DONE", BuilderStatus: herdr.StatusIdle},
	}}
	names := func(rows []relay.BindingStatus) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Name)
		}
		return out
	}
	cases := []struct {
		verb Verb
		want []string
	}{
		{VerbDone, []string{"active", "blocked"}},
		{VerbUnbind, []string{"active", "blocked", "finished"}},
		{VerbAnswer, []string{"blocked"}},
	}
	for _, c := range cases {
		got := names(rowsFor(c.verb, rep))
		if len(got) != len(c.want) {
			t.Errorf("%s: rows = %v, want %v", c.verb, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: rows = %v, want %v", c.verb, got, c.want)
				break
			}
		}
	}
}

func TestEmptyTextPerVerb(t *testing.T) {
	cases := map[Verb]string{
		VerbDone:   "no bindings to mark done",
		VerbUnbind: "nothing bound",
		VerbAnswer: "no builder is blocked",
	}
	for verb, want := range cases {
		if got := emptyText(verb); got != want {
			t.Errorf("%s: %q, want %q", verb, got, want)
		}
	}
}
