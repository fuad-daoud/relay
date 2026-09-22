package ui

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

func newTestBinding(name string) store.Binding {
	return store.Binding{
		Name:             name,
		CWD:              "/tmp/test",
		Planner:          store.Endpoint{PaneID: "w1:p1", SessionID: "planner-session", Kind: "claude"},
		Builder:          store.Endpoint{AgentName: name + "-builder", PaneID: "w1:p2", Kind: "opencode"},
		BuilderCandidate: "agy",
		Round:            2,
		State:            store.StateActive,
	}
}

func TestTabOrderStartsWithPlan(t *testing.T) {
	if tabPlan != 0 {
		t.Fatalf("tabPlan = %d, want 0 (first in the tab order)", tabPlan)
	}
	wantOrder := [tabCount]string{"plan", "report", "terminal", "diff", "log"}
	if tabTitles != wantOrder {
		t.Fatalf("tabTitles = %v, want %v", tabTitles, wantOrder)
	}
}

// TestFetchReportTakesRound pins #183: fetchReport now reads round's own
// report entry, not the newest one logged -- stepping back to an earlier
// round must show that round's report, not a later round's.
// TestFetchTerminalNonCurrentPaneRoundIsEmptyProse pins #183: a plain pane
// builder's terminal only ever shows the binding's live screen, which
// only ever belongs to its current round; a past round it never captured
// a log for reads as prose, not as an error, and must never reach herdr.
// TestFetchLogFiltersRound pins #183: fetchLog now scopes to round rather
// than dumping the whole binding log.
// A spawned builder records an AgentName that herdr can forget across a server
// restart. fetchTerminal has already located the live agent, so it must address
// that agent, not replay a name that may no longer resolve.
// TestFetchTerminalRemoteBuilder pins the #303 remote branch: a remote
// builder shows the round's local builder log when relay has one, and
// otherwise a single line naming the server.
