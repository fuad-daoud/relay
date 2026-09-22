package relay

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// picks returns the pick entries in a binding's log, in order.
func picks(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog(%q): %v", name, err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindPick {
			out = append(out, e)
		}
	}
	return out
}

// kinds returns the kinds in a binding's log, in order, for position checks.
func kinds(t *testing.T, rt Runtime, name string) []store.Kind {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog(%q): %v", name, err)
	}
	out := make([]store.Kind, len(entries))
	for i, e := range entries {
		out[i] = e.Kind
	}
	return out
}

// TestResumeRebindLogsPickAtCurrentRound mirrors
// TestResumeAllowsRebindWhenSessionlessBuilderPaneIsGone. seedBound's own
// initial Bind (Candidate: testAgyRef, explicit) already writes a leading
// pick entry, so this checks the entry the resume itself adds, not the
// total count.
// TestStrandedAskLogsNoPick checks the delta a stranded ask adds, not the
// total: seedForAsk's own seedBound already writes a leading pick entry for
// the builder bind.
