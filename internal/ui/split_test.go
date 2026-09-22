package ui

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

func threeRows() []relay.BindingStatus {
	return []relay.BindingStatus{
		{Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working", Last: &relay.LastEvent{TS: railNow.Add(-6 * time.Minute)}},
		{Name: "docs", Round: 1, Display: "DONE", BuilderKind: "agy", Last: &relay.LastEvent{TS: railNow.Add(-time.Hour)}},
		{Name: "webshop", Round: 4, Display: "NEEDS YOU", BuilderKind: "agy", BuilderStatus: "blocked", Last: &relay.LastEvent{TS: railNow.Add(-2 * time.Minute)}},
	}
}

// TestStickyFollowsKeyAcrossOwners: the rail's sticky is the row key, so a
// second client's same-named binding appearing above must not steal the
// cursor -- it stays on the key it was on.
// TestRailLinesGroupsByOwner: owner-labelled rows get a header per
// maximal owner run -- the label, the ShortOwner fingerprint and the run's
// count -- a blank line before each header, and global card indices; with
// every label empty the output is exactly today's.
func TestRailLinesGroupsByOwner(t *testing.T) {
	id := "SHA256:VLERFMZnvN5HSw/GCBr6FXPEgs4QeAfdU95BUhMMqI0"
	rows := []relay.BindingStatus{
		{Name: "api", Owner: id, OwnerLabel: "a", Round: 1, Display: "ACTIVE", BuilderKind: "agy"},
		{Name: "docs", Owner: id, OwnerLabel: "a", Round: 2, Display: "DONE", BuilderKind: "agy"},
		{Name: "webshop", Owner: "SHA256:abcdefghijklmnop", OwnerLabel: "b", Round: 3, Display: "ACTIVE", BuilderKind: "agy"},
	}
	lines := railLines(rows, -1, false, railNow, true, railDefault, false)
	if plain(lines[0].text) != "a (SHA256:VLERFMZnvN5H…) 2" {
		t.Errorf("first line = %q", plain(lines[0].text))
	}
	if lines[0].binding != -1 {
		t.Errorf("header must be untagged: %+v", lines[0])
	}
	// run a's cards: every card line tagged its global row index
	for i := 1; i <= 3; i++ {
		if lines[i].binding != 0 {
			t.Errorf("api card line %d binding = %d, want 0", i, lines[i].binding)
		}
	}
	for i := 4; i <= 6; i++ {
		if lines[i].binding != 1 {
			t.Errorf("docs card line %d binding = %d, want 1", i, lines[i].binding)
		}
	}
	// the blank line that precedes the b header
	if lines[7].binding != -1 || plain(lines[7].text) != "" {
		t.Errorf("line before the b header must be a blank gap: %+v %q", lines[7], plain(lines[7].text))
	}
	if lines[8].binding != -1 || plain(lines[8].text) != "b (SHA256:abcdefghijkl…) 1" {
		t.Errorf("b header = %q", plain(lines[8].text))
	}
	for i := 9; i <= 11; i++ {
		if lines[i].binding != 2 {
			t.Errorf("webshop card line %d binding = %d, want 2", i, lines[i].binding)
		}
	}
	// trailing blank closes the run
	if lines[12].binding != -1 {
		t.Errorf("trailing gap = %+v", lines[12])
	}
	if len(lines) != 13 {
		t.Fatalf("%d lines, want 13", len(lines))
	}

	// All labels empty: no untagged lines beyond today's -- identical to
	// today's output for the same rows.
	plainRows := []relay.BindingStatus{
		{Name: "api", Round: 1, Display: "ACTIVE", BuilderKind: "agy"},
		{Name: "docs", Round: 2, Display: "DONE", BuilderKind: "agy"},
		{Name: "webshop", Round: 3, Display: "ACTIVE", BuilderKind: "agy"},
	}
	got := railLines(plainRows, -1, false, railNow, true, railDefault, false)
	if len(got) != 9 {
		t.Fatalf("label-less rail has %d lines, want 9", len(got))
	}
	for i, l := range got {
		if l.binding != i/3 {
			t.Errorf("line %d binding = %d, want %d", i, l.binding, i/3)
		}
	}
	for i, first := range []int{0, 3, 6} {
		if p := plain(got[first].text); p != []string{"api r1", "docs r2", "webshop r3"}[i] {
			t.Errorf("card %d first line = %q", i, p)
		}
	}
}

// TestPaneHeadShowsClientLine: an owner-labelled row's planner line is
// replaced by the client line; a planner row keeps the planner line.
// TestFooterMarksHerdrUnreachable: a report that degraded because herdr did
// not answer still renders its rows and marks the footer once -- without the
// error text itself, the way the refresh marker works (list_test.go) -- and
// a report whose HerdrError is empty adds no marker.
// TestPaneHeadLiveUsageRow pins the live figure's place in the header
// (#234): a running round's `usage` row is the live one, exactly one, and
// the closed round's row does not appear beside it; spend keeps its row.
