package relay

import (
	"testing"
	"time"
)

func names(rows []BindingStatus) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSortRowsAttentionOrder: every display state, in every input order,
// lands NEEDS YOU, HELD, ACTIVE, DONE.
func TestSortRowsAttentionOrder(t *testing.T) {
	rows := []BindingStatus{
		{Name: "d", Display: "DONE"},
		{Name: "a", Display: "ACTIVE"},
		{Name: "n", Display: "NEEDS YOU"},
		{Name: "h", Display: "HELD"},
	}
	want := []string{"n", "h", "a", "d"}
	// Rotate the input through every starting point: four permutations
	// that each begin with a different state.
	for shift := 0; shift < len(rows); shift++ {
		in := append(append([]BindingStatus{}, rows[shift:]...), rows[:shift]...)
		got := names(SortRows(in, true))
		if !equalNames(got, want) {
			t.Errorf("shift %d: got %v want %v", shift, got, want)
		}
	}
}

// TestSortRowsWithinGroup: newest Last.TS first, nil Last last, name as
// the tiebreak.
func TestSortRowsWithinGroup(t *testing.T) {
	t0 := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)
	rows := []BindingStatus{
		{Name: "old", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-time.Hour)}},
		{Name: "none", Display: "ACTIVE"},
		{Name: "new", Display: "ACTIVE", Last: &LastEvent{TS: t0}},
		{Name: "tie-b", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-2 * time.Hour)}},
		{Name: "tie-a", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-2 * time.Hour)}},
	}
	got := names(SortRows(rows, true))
	want := []string{"new", "old", "tie-a", "tie-b", "none"}
	if !equalNames(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// TestSortRowsNameOrder: attention=false is plain name order regardless
// of state, and the input slice is untouched either way.
func TestSortRowsNameOrder(t *testing.T) {
	rows := []BindingStatus{
		{Name: "b", Display: "DONE"},
		{Name: "a", Display: "NEEDS YOU"},
		{Name: "c", Display: "ACTIVE"},
	}
	before := names(rows)
	got := names(SortRows(rows, false))
	if !equalNames(got, []string{"a", "b", "c"}) {
		t.Errorf("name order: got %v", got)
	}
	SortRows(rows, true)
	if !equalNames(names(rows), before) {
		t.Errorf("input mutated: %v -> %v", before, names(rows))
	}
}
