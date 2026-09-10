package relay

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestWithinTree(t *testing.T) {
	tests := []struct {
		name      string
		tree, cwd string
		want      bool
	}{
		{name: "exact match", tree: "/repo", cwd: "/repo", want: true},
		{name: "nested one level", tree: "/repo", cwd: "/repo/internal", want: true},
		{name: "nested deep", tree: "/repo", cwd: "/repo/internal/ui/x", want: true},
		{name: "sibling sharing a prefix", tree: "/repo", cwd: "/repo-doctor", want: false},
		{name: "sibling sharing a longer prefix", tree: "/foo", cwd: "/foo-bar/baz", want: false},
		{name: "trailing slash on tree", tree: "/repo/", cwd: "/repo/internal", want: true},
		{name: "trailing slash on cwd", tree: "/repo", cwd: "/repo/internal/", want: true},
		{name: "parent directory is not inside", tree: "/repo/worktrees/a", cwd: "/repo", want: false},
		{name: "unrelated", tree: "/repo", cwd: "/somewhere/else", want: false},
		{name: "empty tree", tree: "", cwd: "/repo", want: false},
		{name: "empty cwd", tree: "/repo", cwd: "", want: false},
		{name: "both empty", tree: "", cwd: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinTree(tc.tree, tc.cwd); got != tc.want {
				t.Errorf("withinTree(%q, %q) = %v, want %v", tc.tree, tc.cwd, got, tc.want)
			}
		})
	}
}

func TestKnownEndpointsCollectsBothSidesOfEveryBinding(t *testing.T) {
	bindings := []store.Binding{
		{Name: "a", Planner: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"}},
		{Name: "b", Planner: store.Endpoint{PaneID: "w1:p3"}, Builder: store.Endpoint{PaneID: "w1:p4"}},
	}
	got := knownEndpoints(bindings)
	if len(got) != 4 {
		t.Fatalf("got %d endpoints, want 4", len(got))
	}
	seen := map[string]bool{}
	for _, ep := range got {
		seen[ep.PaneID] = true
	}
	for _, want := range []string{"w1:p1", "w1:p2", "w1:p3", "w1:p4"} {
		if !seen[want] {
			t.Errorf("endpoint %q missing from %v", want, got)
		}
	}
}

func TestKnownEndpointsHandlesNoBindings(t *testing.T) {
	if got := knownEndpoints(nil); len(got) != 0 {
		t.Errorf("knownEndpoints(nil) = %v, want empty", got)
	}
}

// agentAt is a live agent in a directory, with no recorded session.
func agentAt(pane, kind, cwd string) herdr.Agent {
	return herdr.Agent{PaneID: pane, Kind: kind, Status: herdr.StatusIdle, CWD: cwd}
}

func TestForeignAgentsExcludesThisBindingsOwnEndpoints(t *testing.T) {
	binding := store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w1:p1", Kind: "claude"},
		Builder: store.Endpoint{PaneID: "w1:p2", Kind: "agy"},
	}
	agents := []herdr.Agent{
		agentAt("w1:p1", "claude", "/repo"),
		agentAt("w1:p2", "agy", "/repo"),
	}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{binding}), binding.CWD)
	if got != nil {
		t.Errorf("own planner and builder must never be foreign, got %+v", got)
	}
}

// This is the false positive the "referenced by no binding at all" rule
// exists to prevent: a second binding's planner legitimately sharing a tree.
func TestForeignAgentsExcludesAnotherBindingsPlanner(t *testing.T) {
	subject := store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w1:p1", Kind: "claude"},
		Builder: store.Endpoint{PaneID: "w1:p2", Kind: "agy"},
	}
	other := store.Binding{
		Name: "other", CWD: "/other",
		Planner: store.Endpoint{PaneID: "w1:p9", Kind: "claude"},
		Builder: store.Endpoint{PaneID: "w1:p8", Kind: "agy"},
	}
	agents := []herdr.Agent{
		agentAt("w1:p1", "claude", "/repo"),
		agentAt("w1:p2", "agy", "/repo"),
		agentAt("w1:p9", "claude", "/repo"), // other's planner, in subject's tree
	}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{subject, other}), subject.CWD)
	if got != nil {
		t.Errorf("an agent known to another binding must not be foreign, got %+v", got)
	}
}

func TestForeignAgentsReportsUnknownAgentInTree(t *testing.T) {
	binding := store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w1:p1", Kind: "claude"},
		Builder: store.Endpoint{PaneID: "w1:p2", Kind: "agy"},
	}
	stranger := herdr.Agent{
		PaneID: "w1:p7", Kind: "claude", Status: herdr.StatusIdle,
		CWD: "/repo", Title: "plan-executor",
	}
	agents := []herdr.Agent{agentAt("w1:p1", "claude", "/repo"), stranger}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{binding}), binding.CWD)
	if len(got) != 1 {
		t.Fatalf("got %d foreign agents, want 1: %+v", len(got), got)
	}
	want := ForeignAgent{
		PaneID: "w1:p7", Kind: "claude", Status: herdr.StatusIdle,
		CWD: "/repo", Title: "plan-executor",
	}
	if got[0] != want {
		t.Errorf("got %+v, want %+v", got[0], want)
	}
}

func TestForeignAgentsReportsNestedAgent(t *testing.T) {
	binding := store.Binding{Name: "webshop", CWD: "/repo"}
	agents := []herdr.Agent{agentAt("w1:p7", "opencode", "/repo/internal/ui")}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{binding}), binding.CWD)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1: %+v", len(got), got)
	}
	if got[0].CWD != "/repo/internal/ui" {
		t.Errorf("cwd = %q, want /repo/internal/ui", got[0].CWD)
	}
}

func TestForeignAgentsIgnoresAgentsOutsideTree(t *testing.T) {
	binding := store.Binding{Name: "webshop", CWD: "/repo"}
	agents := []herdr.Agent{
		agentAt("w1:p7", "claude", "/somewhere/else"),
		agentAt("w1:p8", "claude", "/repo-doctor"),
		agentAt("w1:p9", "claude", ""),
	}

	if got := ForeignAgents(agents, knownEndpoints([]store.Binding{binding}), binding.CWD); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

// Proves ForeignAgents uses SameAgent rather than reimplementing matching:
// the builder's pane id has changed, but its session id still identifies it.
func TestForeignAgentsMatchesByStoredSessionAcrossPaneMove(t *testing.T) {
	binding := store.Binding{
		Name: "webshop", CWD: "/repo",
		Builder: store.Endpoint{PaneID: "w1:p2", Kind: "agy", SessionID: "sess-abc"},
	}
	moved := herdr.Agent{
		PaneID: "w9:p99", Kind: "agy", Status: herdr.StatusWorking, CWD: "/repo",
		Session: herdr.Session{Value: "sess-abc"},
	}

	got := ForeignAgents([]herdr.Agent{moved}, knownEndpoints([]store.Binding{binding}), binding.CWD)
	if got != nil {
		t.Errorf("a builder identified by session must not be foreign after a pane move, got %+v", got)
	}
}

func TestForeignAgentsReturnsNilNotEmptySlice(t *testing.T) {
	got := ForeignAgents(nil, nil, "/repo")
	if got != nil {
		t.Errorf("got %#v, want nil so omitempty elides the JSON field", got)
	}
}

func TestForeignAgentsIgnoresEmptyTree(t *testing.T) {
	agents := []herdr.Agent{agentAt("w1:p7", "claude", "/repo")}
	if got := ForeignAgents(agents, nil, ""); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestForeignAgentsSortsByPaneID(t *testing.T) {
	binding := store.Binding{Name: "webshop", CWD: "/repo"}
	agents := []herdr.Agent{
		agentAt("w1:p9", "claude", "/repo"),
		agentAt("w1:p3", "agy", "/repo"),
		agentAt("w1:p6", "opencode", "/repo"),
	}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{binding}), binding.CWD)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].PaneID != "w1:p3" || got[1].PaneID != "w1:p6" || got[2].PaneID != "w1:p9" {
		t.Errorf("not sorted by pane id: %+v", got)
	}
}
