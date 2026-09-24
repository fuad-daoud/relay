package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// testGateKV is a real t.TempDir() database for a Runtime literal's Gates
// field (P3b plan §4.5, §7).
func testGateKV(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// mcpTestPlannerA and mcpTestPlannerB are valid planner ids (pl_ plus 12
// characters of [a-z2-7]); the status filter keys on them.
const (
	mcpTestPlannerA = "pl_aaaaaaaabbbb"
	mcpTestPlannerB = "pl_ccccccccdddd"
)

// stubRunner implements relevo.Runner with no-op stubs, for a headless
// binding whose PID is 0 (Alive is never actually called on that path, but
// Runtime.Runner must be non-nil or sendPreflight refuses before it gets
// that far).
type stubRunner struct{}

func (stubRunner) Start(ctx context.Context, spec relevo.ProcSpec) (relevo.ProcHandle, error) {
	return relevo.ProcHandle{}, nil
}
func (stubRunner) Alive(ctx context.Context, h relevo.ProcHandle) (bool, error) { return false, nil }
func (stubRunner) ExitCode(ctx context.Context, h relevo.ProcHandle, logPath string) (int, bool) {
	return 0, false
}
func (stubRunner) Kill(ctx context.Context, h relevo.ProcHandle) error { return nil }
func (stubRunner) Rusage(context.Context, relevo.ProcHandle, string) (relevo.ProcRusage, bool) {
	return relevo.ProcRusage{}, false
}

func writeCandidates(t *testing.T, body string) *candidate.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write candidates fixture: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("load candidates fixture: %v", err)
	}
	return set
}

func writeTempPlan(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

func saveVerbBinding(t *testing.T, s *store.Store, b store.Binding) {
	t.Helper()
	if err := s.Save(b); err != nil {
		t.Fatalf("save binding %q: %v", b.Name, err)
	}
}

// TestRelevoVerbsStatusFiltersByPlannerThenName is the one status.go RelevoVerbs
// behaviour the plan calls out as needing a real test: bindings are scoped to
// this planner id unless All is set, narrowed further by Name, with the same
// DONE-hiding the CLI applies by default.
func TestRelevoVerbsStatusFiltersByPlannerThenName(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine-a", CWD: "/repo/mine-a", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerA, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "mine-done", CWD: "/repo/mine-done", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerA, Round: 1, State: store.StateDone})
	saveVerbBinding(t, s, store.Binding{Name: "other", CWD: "/repo/other", Planner: store.Endpoint{PaneID: "w9:p9"}, PlannerID: mcpTestPlannerB, Round: 1, State: store.StateActive})

	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}

	res, err := v.Status(context.Background(), StatusArgs{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rep, ok := res.(relevo.Report)
	if !ok {
		t.Fatalf("result = %#v, want relevo.Report", res)
	}
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine-a" {
		t.Fatalf("default status = %+v, want only mine-a (this planner, DONE hidden)", rep.Bindings)
	}

	res, err = v.Status(context.Background(), StatusArgs{All: true})
	if err != nil {
		t.Fatalf("Status all: %v", err)
	}
	rep = res.(relevo.Report)
	if len(rep.Bindings) != 3 {
		t.Fatalf("all status = %d bindings, want 3", len(rep.Bindings))
	}

	res, err = v.Status(context.Background(), StatusArgs{Name: "mine-done"})
	if err != nil {
		t.Fatalf("Status by name: %v", err)
	}
	rep = res.(relevo.Report)
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine-done" {
		t.Fatalf("status by name = %+v, want only mine-done (DONE included when named)", rep.Bindings)
	}

	if _, err := v.Status(context.Background(), StatusArgs{Name: "other"}); err == nil {
		t.Fatal("Status naming a binding on a different planner must error")
	}
}

// TestMCPStatusFiltersByPlanner is the plan's required case for §4.5: the
// status tool filters by planner id. The two bindings here share a pane, so
// only the id can tell them apart.
func TestMCPStatusFiltersByPlanner(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine", CWD: "/repo/mine", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerA, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "cousin", CWD: "/repo/cousin", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerB, Round: 1, State: store.StateActive})

	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}
	res, err := v.Status(context.Background(), StatusArgs{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rep := res.(relevo.Report)
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine" {
		t.Fatalf("status = %+v, want only mine (the same pane's cousin is another planner)", rep.Bindings)
	}
	if rep.Bindings[0].PlannerID != mcpTestPlannerA {
		t.Errorf("row PlannerID = %q, want %q", rep.Bindings[0].PlannerID, mcpTestPlannerA)
	}
}

// TestRelevoVerbsSendDryRunHeadless exercises Send's real forwarding into
// relevo.SendDryRun: a headless binding needs no harness at all (only
// Store, Candidates and Runner), so this runs against the real function,
// not a seam.
func TestRelevoVerbsSendDryRunHeadless(t *testing.T) {
	s := store.New(t.TempDir())
	set := writeCandidates(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}]`)
	rt := relevo.Runtime{
		Store:      s,
		Candidates: set,
		Runner:     stubRunner{},
		Gates:      testGateKV(t),
		Now:        func() time.Time { return time.Unix(0, 0) },
	}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner:          store.Endpoint{PaneID: "w2:p3"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless},
		BuilderCandidate: "agy/test/m",
		Round:            1, State: store.StateActive,
	})

	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}
	plan := writeTempPlan(t, "# do the thing")

	res, err := v.Send(context.Background(), SendArgs{Name: "webshop", File: plan, DryRun: true})
	if err != nil {
		t.Fatalf("Send dry-run: %v", err)
	}
	d, ok := res.(relevo.DryRun)
	if !ok {
		t.Fatalf("result = %#v, want relevo.DryRun", res)
	}
	if d.Mode != "headless" {
		t.Errorf("Mode = %q, want headless", d.Mode)
	}
	if d.Name != "webshop" || d.Round != 1 {
		t.Errorf("DryRun = %+v, want Name webshop, Round 1", d)
	}
}

// TestRelevoVerbsSendRealRunHeadless proves DryRun false takes the real
// relevo.Send path, not SendDryRun: for a headless binding this needs no
// live pane either, so it runs to completion against stubRunner.
func TestRelevoVerbsSendRealRunHeadless(t *testing.T) {
	s := store.New(t.TempDir())
	set := writeCandidates(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}]`)
	rt := relevo.Runtime{
		Store:      s,
		Candidates: set,
		Runner:     stubRunner{},
		Gates:      testGateKV(t),
		Now:        func() time.Time { return time.Unix(0, 0) },
	}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner:          store.Endpoint{PaneID: "w2:p3"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless},
		BuilderCandidate: "agy/test/m",
		Round:            1, State: store.StateActive,
	})

	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}
	plan := writeTempPlan(t, "# do the thing")

	res, err := v.Send(context.Background(), SendArgs{Name: "webshop", File: plan})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	sr, ok := res.(sendResult)
	if !ok {
		t.Fatalf("result = %#v, want sendResult", res)
	}
	if sr.Round != 1 {
		t.Errorf("Round = %d, want 1", sr.Round)
	}
	// #303 §4.5: the tools-mode send result names the round budget so the
	// model's background wait cannot time out before the round does. store
	// filled the 24h default in, since the fixture set no --timeout.
	if sr.WaitBudget != "24h0m0s" {
		t.Errorf("WaitBudget = %q, want 24h0m0s", sr.WaitBudget)
	}
}

// TestRelevoVerbsDoneForwardsAndReportsText proves Done calls relevo.Done
// (which flips the binding's stored State to done) and reports relevo.DoneText
// alongside the structured DoneResult.
func TestRelevoVerbsDoneForwardsAndReportsText(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{Mode: store.ModeHeadless},
		Round:   1, State: store.StateActive,
	})

	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}
	res, err := v.Done(context.Background(), DoneArgs{Name: "webshop"})
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	dr, ok := res.(doneResult)
	if !ok {
		t.Fatalf("result = %#v, want doneResult", res)
	}
	if dr.Text == "" {
		t.Error("Text must be set from relevo.DoneText")
	}

	updated, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if updated.State != store.StateDone {
		t.Errorf("State = %q, want done", updated.State)
	}
}

func TestRelevoVerbsDoneErrorPropagates(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}

	if _, err := v.Done(context.Background(), DoneArgs{Name: "nonexistent"}); err == nil {
		t.Fatal("Done on a binding that does not exist must error")
	}
}
