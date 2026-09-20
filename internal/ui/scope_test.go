package ui

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

// TestScopeRowsUnionOrder pins the merge rule (§5.8): live rows first, in
// the order given (the caller has already sorted them); then hist rows not
// named in live, in the order given (relay.Bindings's own newest-first);
// a name in both appears exactly once, as live.
func TestScopeRowsUnionOrder(t *testing.T) {
	live := []relay.BindingStatus{
		{Name: "b"},
		{Name: "a"},
	}
	hist := []relay.HistoryBinding{
		{Name: "a", LastActivity: time.Now()},                      // also live: must not duplicate
		{Name: "z", LastActivity: time.Now()},                      // newest
		{Name: "y", LastActivity: time.Now().Add(-24 * time.Hour)}, // older
	}

	rows := scopeRows(live, hist)

	if len(rows) != 4 {
		t.Fatalf("len(rows) = %d, want 4 (b, a, z, y)", len(rows))
	}

	want := []string{"b", "a", "z", "y"}
	for i, w := range want {
		if got := rows[i].name(); got != w {
			t.Errorf("rows[%d] = %q, want %q", i, got, w)
		}
	}

	if rows[0].live == nil || rows[0].hist != nil {
		t.Error("rows[0] (b) must be the live row")
	}
	if rows[1].live == nil || rows[1].hist != nil {
		t.Error("a name in both live and hist must appear once, as live")
	}
	if rows[2].hist == nil || rows[2].live != nil {
		t.Error("rows[2] (z) must be a hist row")
	}
	if rows[3].hist == nil || rows[3].live != nil {
		t.Error("rows[3] (y) must be a hist row")
	}
}

func TestScopeRowsEmpty(t *testing.T) {
	if rows := scopeRows(nil, nil); len(rows) != 0 {
		t.Errorf("scopeRows(nil, nil) = %v, want empty", rows)
	}
}
