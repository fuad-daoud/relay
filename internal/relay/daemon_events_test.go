package relay

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestAgentCacheApplyStatusChangeUpdatesExistingAgent(t *testing.T) {
	c := &agentCache{}
	c.Replace([]herdr.Agent{{PaneID: "w1:p1", Status: herdr.StatusWorking}})

	pane, refresh := c.Apply(herdr.Event{Kind: "pane_agent_status_changed", PaneID: "w1:p1", AgentStatus: "blocked"})
	if pane != "w1:p1" || refresh {
		t.Fatalf("Apply = (%q, %v), want (w1:p1, false)", pane, refresh)
	}

	got := c.Snapshot()
	if len(got) != 1 || got[0].Status != "blocked" {
		t.Errorf("Snapshot = %+v, want one agent with status blocked", got)
	}
}

func TestAgentCacheApplyStatusChangeUnknownPaneRefreshes(t *testing.T) {
	c := &agentCache{}
	c.Replace([]herdr.Agent{{PaneID: "w1:p1", Status: herdr.StatusWorking}})

	pane, refresh := c.Apply(herdr.Event{Kind: "pane_agent_status_changed", PaneID: "w1:p9", AgentStatus: "blocked"})
	if pane != "w1:p9" || !refresh {
		t.Fatalf("Apply = (%q, %v), want (w1:p9, true) for an unknown pane", pane, refresh)
	}
	// Mutation check: an implementation that always reports refresh=false
	// would pass every other assertion here but fail this one.
}

func TestAgentCacheApplyExitedRemovesAgent(t *testing.T) {
	c := &agentCache{}
	c.Replace([]herdr.Agent{{PaneID: "w1:p1"}, {PaneID: "w1:p2"}})

	pane, refresh := c.Apply(herdr.Event{Kind: "pane_exited", PaneID: "w1:p1"})
	if pane != "w1:p1" || refresh {
		t.Fatalf("Apply = (%q, %v), want (w1:p1, false)", pane, refresh)
	}

	got := c.Snapshot()
	if len(got) != 1 || got[0].PaneID != "w1:p2" {
		t.Errorf("Snapshot = %+v, want only w1:p2 left", got)
	}
}

func TestAgentCacheApplyClosedRemovesAgent(t *testing.T) {
	c := &agentCache{}
	c.Replace([]herdr.Agent{{PaneID: "w1:p1"}, {PaneID: "w1:p2"}})

	pane, refresh := c.Apply(herdr.Event{Kind: "pane_closed", PaneID: "w1:p2"})
	if pane != "w1:p2" || refresh {
		t.Fatalf("Apply = (%q, %v), want (w1:p2, false)", pane, refresh)
	}

	got := c.Snapshot()
	if len(got) != 1 || got[0].PaneID != "w1:p1" {
		t.Errorf("Snapshot = %+v, want only w1:p1 left", got)
	}
}

func TestAgentCacheApplyDetectedRefreshes(t *testing.T) {
	c := &agentCache{}
	c.Replace([]herdr.Agent{{PaneID: "w1:p1"}})

	pane, refresh := c.Apply(herdr.Event{Kind: "pane_agent_detected"})
	if pane != "" || !refresh {
		t.Fatalf("Apply = (%q, %v), want (\"\", true)", pane, refresh)
	}
}

func TestAgentCacheApplyUnknownKindIsANoop(t *testing.T) {
	c := &agentCache{}
	c.Replace([]herdr.Agent{{PaneID: "w1:p1", Status: herdr.StatusWorking}})

	pane, refresh := c.Apply(herdr.Event{Kind: "something_else", PaneID: "w1:p1"})
	if pane != "" || refresh {
		t.Fatalf("Apply = (%q, %v), want (\"\", false) for an unrecognised kind", pane, refresh)
	}
	if got := c.Snapshot(); got[0].Status != herdr.StatusWorking {
		t.Errorf("an unknown event kind must not mutate the cache, got %+v", got)
	}
}

func TestBoundPanes(t *testing.T) {
	bs := []store.Binding{
		{
			Name:    "a",
			State:   store.StateActive,
			Planner: store.Endpoint{PaneID: "w1:p1"},
			Builder: store.Endpoint{PaneID: "w1:p2"},
		},
		{
			// DONE: excluded entirely.
			Name:    "done",
			State:   store.StateDone,
			Planner: store.Endpoint{PaneID: "w2:p1"},
			Builder: store.Endpoint{PaneID: "w2:p2"},
		},
		{
			// PAUSED: excluded entirely.
			Name:    "paused",
			State:   store.StatePaused,
			Planner: store.Endpoint{PaneID: "w3:p1"},
			Builder: store.Endpoint{PaneID: "w3:p2"},
		},
		{
			// Remote builder: builder pane excluded, planner pane kept.
			Name:    "remote",
			State:   store.StateActive,
			Planner: store.Endpoint{PaneID: "w4:p1"},
			Builder: store.Endpoint{PaneID: "w4:p2", Mode: store.ModeRemote},
		},
		{
			// Headless builder: builder pane excluded, planner pane kept.
			Name:    "headless",
			State:   store.StateActive,
			Planner: store.Endpoint{PaneID: "w5:p1"},
			Builder: store.Endpoint{PaneID: "w5:p2", Mode: store.ModeHeadless},
		},
		{
			// Duplicate planner pane against "a": must not appear twice.
			Name:    "dup",
			State:   store.StateActive,
			Planner: store.Endpoint{PaneID: "w1:p1"},
			Builder: store.Endpoint{PaneID: "w6:p2"},
		},
	}

	got := boundPanes(bs)
	want := []string{"w1:p1", "w1:p2", "w4:p1", "w5:p1", "w6:p2"}
	if !equalPanes(got, want) {
		t.Errorf("boundPanes = %v, want %v", got, want)
	}
}

func TestBackoffSchedule(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Second}, // clamped to attempt 1
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second},
		{100, 30 * time.Second},
	}
	for _, c := range cases {
		if got := backoffAfter(c.attempt); got != c.want {
			t.Errorf("backoffAfter(%d) = %s, want %s", c.attempt, got, c.want)
		}
	}
}
