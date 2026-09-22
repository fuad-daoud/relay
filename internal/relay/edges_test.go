package relay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// edgePairStore seeds two minimal, unrelated active bindings ("api" and
// "client") in one store, with no herdr agents at all: enough for
// AddEdge/ListEdges/RemoveEdge, which never touch herdr.
func edgePairStore(t *testing.T) Runtime {
	t.Helper()
	rt := newRuntime(t, &fakePanes{})
	for _, name := range []string{"api", "client"} {
		b := store.Binding{Name: name, CWD: "/repo-" + name, Round: 1, State: store.StateActive}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}
	return rt
}

func TestAddEdgeValidatesAndLogs(t *testing.T) {
	rt := edgePairStore(t)
	prompt := writePlan(t, "handoff plan")

	got, err := AddEdge(context.Background(), rt, "api", store.Edge{
		When: "report", Then: "send", Target: "client", Prompt: prompt,
	})
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	if len(got.ID) != 6 {
		t.Errorf("ID = %q, want 6 hex characters", got.ID)
	}
	if got.Mode != "queue" {
		t.Errorf("Mode = %q, want the default %q", got.Mode, "queue")
	}
	if got.Round != 1 {
		t.Errorf("Round = %d, want the source's current round 1", got.Round)
	}
	if got.AddedAt.IsZero() {
		t.Errorf("AddedAt is zero")
	}

	stored, err := ListEdges(rt, "api")
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(stored) != 1 || stored[0].ID != got.ID {
		t.Fatalf("stored edges = %+v, want exactly the one added", stored)
	}

	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if last.Kind != store.KindEdge || last.Direction != store.DirToPlanner || !last.Confirmed {
		t.Fatalf("log entry = %+v, want a confirmed DirToPlanner KindEdge entry", last)
	}
	if !strings.Contains(last.Note, "edge added "+got.ID) {
		t.Errorf("log note = %q, want it to name the edge", last.Note)
	}

	cases := []struct {
		name string
		e    store.Edge
		want string
	}{
		{"bad when", store.Edge{When: "banana", Then: "send", Target: "client", Prompt: prompt}, "--when"},
		{"bad then", store.Edge{When: "report", Then: "ask", Target: "client", Prompt: prompt}, "--then"},
		{"bad mode", store.Edge{When: "report", Then: "send", Target: "client", Prompt: prompt, Mode: "sometimes"}, "--mode"},
		{"missing target", store.Edge{When: "report", Then: "send", Prompt: prompt}, "--target"},
		{"target is source", store.Edge{When: "report", Then: "send", Target: "api", Prompt: prompt}, "must not be the source"},
		{"unknown target", store.Edge{When: "report", Then: "send", Target: "ghost", Prompt: prompt}, "target"},
		{"relative prompt", store.Edge{When: "report", Then: "send", Target: "client", Prompt: "client-plan.md"}, "absolute"},
		{"unreadable prompt", store.Edge{When: "report", Then: "send", Target: "client", Prompt: filepath.Join(t.TempDir(), "missing.md")}, "read prompt"},
		{"round before source's", store.Edge{When: "report", Then: "send", Target: "client", Prompt: prompt, Round: -3}, "before"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := AddEdge(context.Background(), rt, "api", c.e); err == nil {
				t.Fatalf("AddEdge(%s): want an error", c.name)
			} else if !strings.Contains(err.Error(), c.want) {
				t.Errorf("AddEdge(%s) error = %q, want it to mention %q", c.name, err, c.want)
			}
		})
	}

	// None of the refused attempts stored anything: still exactly the one
	// valid edge from above.
	stored, err = ListEdges(rt, "api")
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored edges after refusals = %+v, want still exactly 1", stored)
	}
}

func TestRemoveEdge(t *testing.T) {
	rt := edgePairStore(t)
	prompt := writePlan(t, "handoff plan")

	added, err := AddEdge(context.Background(), rt, "api", store.Edge{
		When: "report", Then: "send", Target: "client", Prompt: prompt,
	})
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	if err := RemoveEdge(context.Background(), rt, "api", added.ID); err != nil {
		t.Fatalf("RemoveEdge: %v", err)
	}
	stored, err := ListEdges(rt, "api")
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored edges after remove = %+v, want none", stored)
	}

	if err := RemoveEdge(context.Background(), rt, "api", added.ID); err == nil {
		t.Fatal("RemoveEdge of an already-removed id: want an error")
	}
	if err := RemoveEdge(context.Background(), rt, "api", "nonexistent"); err == nil {
		t.Fatal("RemoveEdge of an unknown id: want an error")
	}

	// Removing a fired edge is fine -- it is not special-cased.
	fired, err := AddEdge(context.Background(), rt, "api", store.Edge{
		When: "report", Then: "send", Target: "client", Prompt: prompt, Mode: "fire",
	})
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i := range b.Edges {
		if b.Edges[i].ID == fired.ID {
			b.Edges[i].Fired = true
			b.Edges[i].Result = "sent round 1 to client"
		}
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := RemoveEdge(context.Background(), rt, "api", fired.ID); err != nil {
		t.Fatalf("RemoveEdge of a fired edge: %v", err)
	}
}

// edgeSourceBinding binds "api" on a pane builder and sends it one round,
// sentBinding-style, but under the name the edge tests use throughout (#37).
func edgeSourceBinding(t *testing.T, f *fakePanes) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []stubAgent{plannerAgent()}
	f.newPane = "w2:p4"
	rt := newRuntime(t, f)

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "api", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo-api",
	}); err != nil {
		t.Fatalf("Bind api: %v", err)
	}
	f.agents = append(f.agents, stubAgent{
		Name: "api-builder", Kind: "agy", Status: stubIdle, CWD: "/repo-api", PaneID: "w2:p4", Title: "api-builder",
	})

	if _, err := Send(context.Background(), rt, "api", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send api: %v", err)
	}
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("Load api: %v", err)
	}
	f.prompts = nil
	return rt, b
}

// edgeSourceBindingHeadless is edgeSourceBinding for a headless "api",
// mirroring sentHeadless under the edge tests' binding names.
func edgeSourceBindingHeadless(t *testing.T, f *fakePanes, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []stubAgent{plannerAgent()}
	rt := newRuntime(t, f)
	rt.Runner = fr

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "api", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo-api",
	}); err != nil {
		t.Fatalf("Bind --headless api: %v", err)
	}

	if _, err := Send(context.Background(), rt, "api", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send api: %v", err)
	}
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("Load api: %v", err)
	}
	return rt, b
}

// addClientBinding saves an active "client" binding with a pane builder, and
// -- when live -- a matching herdr agent for that pane, so Send can reach it.
// live is false to simulate a target Send cannot reach (gone/broken).
func addClientBinding(t *testing.T, rt Runtime, f *fakePanes, live bool) store.Binding {
	t.Helper()
	client := store.Binding{
		Name: "client", CWD: "/repo-client",
		Builder:          store.Endpoint{PaneID: "w2:p9"},
		BuilderCandidate: testAgyRef,
		Round:            1,
		State:            store.StateActive,
	}
	if err := rt.Store.Save(client); err != nil {
		t.Fatalf("save client: %v", err)
	}
	if live {
		f.agents = append(f.agents, stubAgent{
			Name: "client-builder", Kind: "agy", Status: stubIdle, CWD: "/repo-client", PaneID: "w2:p9", Title: "client-builder",
		})
	}
	stored, err := rt.Store.Load("client")
	if err != nil {
		t.Fatalf("Load client: %v", err)
	}
	return stored
}

// closeAPIRound writes api's round N report and done marker, so the next
// reconcile call closes it on its marker.
func closeAPIRound(t *testing.T, rt Runtime, round int) {
	t.Helper()
	if err := os.WriteFile(rt.Store.ReportPath("api", round), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("api", round))
}

// reloadAPI re-reads "api" from the store: every test here calls AddEdge
// after edgeSourceBinding/edgeSourceBindingHeadless already returned a
// snapshot, and AddEdge writes to the store directly, so the snapshot must
// be refreshed before it is handed to reconcile.
func reloadAPI(t *testing.T, rt Runtime) store.Binding {
	t.Helper()
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("Load api: %v", err)
	}
	return b
}

func apiAgents() []stubAgent {
	return []stubAgent{
		plannerWith(stubWorking, false),
		{Name: "api-builder", Kind: "agy", Status: stubIdle, CWD: "/repo-api", PaneID: "w2:p4", Title: "api-builder"},
	}
}

// findEdgeLogEntry returns the newest KindEdge log entry that carries a
// payload -- the queue-mode delivery evaluateEdges (or runFires' failure
// fallback) queues -- distinct from the Payload-less KindEdge entries
// AddEdge and a successful fire log. name's own report is queued first in
// every close this file drives, so PendingForPlanner would return that
// instead; scanning the log for KindEdge specifically is what actually
// answers "was the edge's own handoff queued".
func findEdgeLogEntry(t *testing.T, rt Runtime, name string) (store.LogEntry, bool) {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog %s: %v", name, err)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == store.KindEdge && entries[i].Payload != "" {
			return entries[i], true
		}
	}
	return store.LogEntry{}, false
}

// TestEdgeQueueOnClose pins the default mode (#37): a queue-mode edge whose
// artifact exists at round close queues a DirToPlanner payload naming the
// exact send command, is marked Fired with Result "queued", and never
// touches the target binding.
//
// Mutation check (run and report): skip the evaluateEdges call this round
// added to reconcile.go's close path, and this fails.
func TestEdgeQueueOnClose(t *testing.T) {
	f := &fakePanes{}
	rt, api := edgeSourceBinding(t, f)
	addClientBinding(t, rt, f, true)
	prompt := writePlan(t, "handoff to client")

	if _, err := AddEdge(context.Background(), rt, "api", store.Edge{
		When: "report", Then: "send", Target: "client", Prompt: prompt,
	}); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	closeAPIRound(t, rt, 1)
	api = reloadAPI(t, rt)

	got, err := reconcile(t, rt, api, apiAgents())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2 after the report closed it", got.Round)
	}
	if len(got.Edges) != 1 {
		t.Fatalf("Edges = %+v, want exactly 1", got.Edges)
	}
	if !got.Edges[0].Fired || got.Edges[0].Result != "queued" {
		t.Errorf("edge = %+v, want Fired with Result %q", got.Edges[0], "queued")
	}

	entry, found := findEdgeLogEntry(t, rt, "api")
	if !found {
		t.Fatalf("no queued edge payload in api's log")
	}
	want := "relay send --name client --file " + prompt
	if !strings.Contains(entry.Payload, want) {
		t.Errorf("payload = %q, want it to contain %q", entry.Payload, want)
	}

	client, err := rt.Store.Load("client")
	if err != nil {
		t.Fatalf("Load client: %v", err)
	}
	if client.Round != 1 || len(client.Edges) != 0 {
		t.Errorf("client = %+v, want untouched at round 1 with no edges", client)
	}
	if len(f.prompts) != 0 {
		t.Errorf("f.prompts = %+v, want none: a queued edge never prompts the target", f.prompts)
	}
}

// TestEdgeSkippedWhenArtifactMissing pins that a missing artifact skips the
// edge rather than leaving it pending forever: at close, a round's artifacts
// are final.
func TestEdgeSkippedWhenArtifactMissing(t *testing.T) {
	f := &fakePanes{}
	rt, api := edgeSourceBinding(t, f)
	addClientBinding(t, rt, f, true)
	prompt := writePlan(t, "handoff to client")

	if _, err := AddEdge(context.Background(), rt, "api", store.Edge{
		When: "diff", Then: "send", Target: "client", Prompt: prompt,
	}); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	closeAPIRound(t, rt, 1)
	api = reloadAPI(t, rt)
	// No diff is ever captured for /repo-api: it is not a real git
	// checkout, so CaptureDrift/CaptureRoundDiff produce nothing -- exactly
	// the "no diff" case this test wants.

	got, err := reconcile(t, rt, api, apiAgents())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(got.Edges) != 1 {
		t.Fatalf("Edges = %+v, want exactly 1", got.Edges)
	}
	if !got.Edges[0].Fired || got.Edges[0].Result != "skipped: no diff" {
		t.Errorf("edge = %+v, want Fired with Result %q", got.Edges[0], "skipped: no diff")
	}

	if _, found := findEdgeLogEntry(t, rt, "api"); found {
		t.Errorf("a skipped edge must not queue a payload")
	}
}

// TestEdgeFireFailureIsQueued pins the fallback (#37 §6): a fire that cannot
// reach its target never blocks the source, and is downgraded to the same
// queue-mode payload evaluateEdges would have queued, with the failure
// named.
func TestEdgeFireFailureIsQueued(t *testing.T) {
	f := &fakePanes{}
	rt, api := edgeSourceBinding(t, f)
	// live=false: no herdr agent answers client's pane, so Send cannot
	// locate its builder -- the "target is gone" case.
	addClientBinding(t, rt, f, false)
	prompt := writePlan(t, "handoff to client")

	if _, err := AddEdge(context.Background(), rt, "api", store.Edge{
		When: "report", Then: "send", Target: "client", Prompt: prompt, Mode: "fire",
	}); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	closeAPIRound(t, rt, 1)
	api = reloadAPI(t, rt)

	got, err := reconcile(t, rt, api, apiAgents())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	runFires(context.Background(), rt, []firePending{{Source: "api", Edge: got.Edges[0]}})

	stored, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("Load api: %v", err)
	}
	if len(stored.Edges) != 1 || !stored.Edges[0].Fired {
		t.Fatalf("edge after a failed fire = %+v, want Fired", stored.Edges)
	}
	if !strings.HasPrefix(stored.Edges[0].Result, "fire failed") {
		t.Errorf("Result = %q, want it to start with %q", stored.Edges[0].Result, "fire failed")
	}

	entry, found := findEdgeLogEntry(t, rt, "api")
	if !found {
		t.Fatalf("a failed fire must queue a payload")
	}
	if !strings.Contains(entry.Payload, "fire failed") {
		t.Errorf("payload = %q, want it to name the failure", entry.Payload)
	}
}

// TestEdgeFiresOnce pins that an edge fires exactly once, on its own round's
// close: a second close of a later round neither re-evaluates it nor leaves
// it untouched by mistake -- an edge declared for that later round fires
// then.
func TestEdgeFiresOnce(t *testing.T) {
	f := &fakePanes{}
	rt, api := edgeSourceBinding(t, f)
	addClientBinding(t, rt, f, true)
	prompt1 := writePlan(t, "handoff round 1")
	prompt2 := writePlan(t, "handoff round 2")

	round1Edge, err := AddEdge(context.Background(), rt, "api", store.Edge{
		Round: 1, When: "report", Then: "send", Target: "client", Prompt: prompt1,
	})
	if err != nil {
		t.Fatalf("AddEdge round 1: %v", err)
	}
	round2Edge, err := AddEdge(context.Background(), rt, "api", store.Edge{
		Round: 2, When: "report", Then: "send", Target: "client", Prompt: prompt2,
	})
	if err != nil {
		t.Fatalf("AddEdge round 2: %v", err)
	}

	closeAPIRound(t, rt, 1)
	api = reloadAPI(t, rt)
	got, err := reconcile(t, rt, api, apiAgents())
	if err != nil {
		t.Fatalf("Reconcile round 1 close: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2", got.Round)
	}

	byID := func(b store.Binding, id string) store.Edge {
		for _, e := range b.Edges {
			if e.ID == id {
				return e
			}
		}
		t.Fatalf("no edge %s on %+v", id, b.Edges)
		return store.Edge{}
	}

	e1 := byID(got, round1Edge.ID)
	if !e1.Fired || e1.Result != "queued" {
		t.Fatalf("round 1 edge after round 1 closed = %+v, want Fired queued", e1)
	}
	e2 := byID(got, round2Edge.ID)
	if e2.Fired {
		t.Fatalf("round 2 edge after round 1 closed = %+v, want untouched", e2)
	}

	// Reconcile does not persist anything (reconcile.go's own doc comment);
	// the caller must Save before Round 2, Edges and everything else
	// evaluateEdges touched are visible to a later Load -- exactly what the
	// Daemon's tx.Save(next) does for a real tick.
	if err := rt.Store.Save(got); err != nil {
		t.Fatalf("Save after round 1 close: %v", err)
	}

	f.prompts = nil
	if _, err := Send(context.Background(), rt, "api", writePlan(t, "round 2 work"), SendOptions{}); err != nil {
		t.Fatalf("Send round 2: %v", err)
	}
	closeAPIRound(t, rt, 2)
	got = reloadAPI(t, rt)

	got, err = reconcile(t, rt, got, apiAgents())
	if err != nil {
		t.Fatalf("Reconcile round 2 close: %v", err)
	}
	if got.Round != 3 {
		t.Fatalf("round = %d, want 3", got.Round)
	}

	e1 = byID(got, round1Edge.ID)
	if !e1.Fired || e1.Result != "queued" {
		t.Errorf("round 1 edge after round 2 closed = %+v, want unchanged from round 1's close", e1)
	}
	e2 = byID(got, round2Edge.ID)
	if !e2.Fired || e2.Result != "queued" {
		t.Errorf("round 2 edge after round 2 closed = %+v, want Fired queued", e2)
	}
}

// TestEdgeHeadlessClosePath mirrors TestEdgeQueueOnClose for a headless
// source binding: reconcileHeadless's close path evaluates edges exactly as
// the pane path does.
func TestEdgeHeadlessClosePath(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, api := edgeSourceBindingHeadless(t, f, fr)
	addClientBinding(t, rt, f, true)
	prompt := writePlan(t, "handoff to client")

	if _, err := AddEdge(context.Background(), rt, "api", store.Edge{
		When: "report", Then: "send", Target: "client", Prompt: prompt,
	}); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	closeAPIRound(t, rt, 1)
	fr.script(api.Builder.PID, false)
	fr.exit(api.Builder.PID, 0)
	api = reloadAPI(t, rt)

	got, err := reconcile(t, rt, api, []stubAgent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2 after the report closed it", got.Round)
	}
	if len(got.Edges) != 1 || !got.Edges[0].Fired || got.Edges[0].Result != "queued" {
		t.Fatalf("edge = %+v, want Fired with Result %q", got.Edges, "queued")
	}

	entry, found := findEdgeLogEntry(t, rt, "api")
	if !found {
		t.Fatalf("no queued edge payload in api's log")
	}
	want := "relay send --name client --file " + prompt
	if !strings.Contains(entry.Payload, want) {
		t.Errorf("payload = %q, want it to contain %q", entry.Payload, want)
	}
}
