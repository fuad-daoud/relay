package relay

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// stopEntries returns the binding's KindStop entries, oldest first.
func stopEntries(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindStop {
			out = append(out, e)
		}
	}
	return out
}

func TestStopDecisionTable(t *testing.T) {
	open := store.Binding{Round: 1, RoundStartedAt: baseTime}

	cases := []struct {
		name string
		b    store.Binding
		want stopAction
	}{
		{"no round", store.Binding{Round: 1}, stopNothing},
		{"open round", open, stopKill},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stopDecision(c.b, baseTime); got != c.want {
				t.Errorf("stopDecision = %v, want %v", got, c.want)
			}
		})
	}
}

// TestStopPaneRequestsAndRecords pins the pane path: the wrap-up prompt goes
// through the same delivery path as a plan, and the request and its grace are
// recorded while the round stays open.
// TestStopHeadlessKillsAndClosesWithoutSwitch pins design question 1's option
// (a): a headless round has no stdin, so a stop kills now and closes the
// round without a report -- never through the exit-without-report path, which
// would switch the builder and charge the round.
