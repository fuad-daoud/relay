package pick

import (
	"testing"
)

// TestRowsForFiltersPerVerb pins the spec §4 table.
func TestEmptyTextPerVerb(t *testing.T) {
	cases := map[Verb]string{
		VerbDone:   "no bindings to mark done",
		VerbUnbind: "nothing bound",
	}
	for verb, want := range cases {
		if got := emptyText(verb); got != want {
			t.Errorf("%s: %q, want %q", verb, got, want)
		}
	}
}

func TestNeedsConfirmOnlyForLiveRowsUnderDestructiveVerbs(t *testing.T) {
	live := []string{"ACTIVE", "NEEDS YOU", "HELD"}
	for _, d := range live {
		if !needsConfirm(VerbDone, row("a", d, "working")) {
			t.Errorf("done on %s row: needsConfirm = false, want true", d)
		}
		if !needsConfirm(VerbUnbind, row("a", d, "working")) {
			t.Errorf("unbind on %s row: needsConfirm = false, want true", d)
		}
	}
	if needsConfirm(VerbUnbind, row("a", "DONE", "idle")) {
		t.Error("unbind on a DONE row must run at once")
	}
	if needsConfirm(VerbDone, row("a", "DONE", "idle")) {
		t.Error("done never lists DONE rows; the rule still says no confirm for one")
	}
}
