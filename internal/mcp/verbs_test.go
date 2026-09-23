package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

// mcpTestPlannerA and mcpTestPlannerB are valid planner ids (pl_ plus 12
// characters of [a-z2-7]); the status filter keys on them.
const (
	mcpTestPlannerA = "pl_aaaaaaaabbbb"
	mcpTestPlannerB = "pl_ccccccccdddd"
)

// stubRunner implements relay.Runner with no-op stubs, for a headless
// binding whose PID is 0 (Alive is never actually called on that path, but
// Runtime.Runner must be non-nil or sendPreflight refuses before it gets
// that far).
type stubRunner struct{}

func (stubRunner) Start(ctx context.Context, spec relay.ProcSpec) (relay.ProcHandle, error) {
	return relay.ProcHandle{}, nil
}
func (stubRunner) Alive(ctx context.Context, h relay.ProcHandle) (bool, error) { return false, nil }
func (stubRunner) ExitCode(ctx context.Context, h relay.ProcHandle, logPath string) (int, bool) {
	return 0, false
}
func (stubRunner) Kill(ctx context.Context, h relay.ProcHandle) error { return nil }
func (stubRunner) Rusage(context.Context, relay.ProcHandle, string) (relay.ProcRusage, bool) {
	return relay.ProcRusage{}, false
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

// TestRelayVerbsStatusFiltersByPlannerThenName is the one status.go RelayVerbs
// behaviour the plan calls out as needing a real test: bindings are scoped to
// this planner id unless All is set, narrowed further by Name, with the same
// DONE-hiding the CLI applies by default.
func TestRelayVerbsStatusFiltersByPlannerThenName(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relay.Runtime{
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine-a", CWD: "/repo/mine-a", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerA, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "mine-done", CWD: "/repo/mine-done", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerA, Round: 1, State: store.StateDone})
	saveVerbBinding(t, s, store.Binding{Name: "other", CWD: "/repo/other", Planner: store.Endpoint{PaneID: "w9:p9"}, PlannerID: mcpTestPlannerB, Round: 1, State: store.StateActive})

	v := &RelayVerbs{RT: rt, Planner: mcpTestPlannerA}

	res, err := v.Status(context.Background(), StatusArgs{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rep, ok := res.(relay.Report)
	if !ok {
		t.Fatalf("result = %#v, want relay.Report", res)
	}
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine-a" {
		t.Fatalf("default status = %+v, want only mine-a (this planner, DONE hidden)", rep.Bindings)
	}

	res, err = v.Status(context.Background(), StatusArgs{All: true})
	if err != nil {
		t.Fatalf("Status all: %v", err)
	}
	rep = res.(relay.Report)
	if len(rep.Bindings) != 3 {
		t.Fatalf("all status = %d bindings, want 3", len(rep.Bindings))
	}

	res, err = v.Status(context.Background(), StatusArgs{Name: "mine-done"})
	if err != nil {
		t.Fatalf("Status by name: %v", err)
	}
	rep = res.(relay.Report)
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
	rt := relay.Runtime{
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine", CWD: "/repo/mine", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerA, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "cousin", CWD: "/repo/cousin", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerB, Round: 1, State: store.StateActive})

	v := &RelayVerbs{RT: rt, Planner: mcpTestPlannerA}
	res, err := v.Status(context.Background(), StatusArgs{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rep := res.(relay.Report)
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine" {
		t.Fatalf("status = %+v, want only mine (the same pane's cousin is another planner)", rep.Bindings)
	}
	if rep.Bindings[0].PlannerID != mcpTestPlannerA {
		t.Errorf("row PlannerID = %q, want %q", rep.Bindings[0].PlannerID, mcpTestPlannerA)
	}
}

// TestRelayVerbsSendDryRunHeadless exercises Send's real forwarding into
// relay.SendDryRun: a headless binding needs no harness at all (only
// Store, Candidates and Runner), so this runs against the real function,
// not a seam.
func TestRelayVerbsSendDryRunHeadless(t *testing.T) {
	s := store.New(t.TempDir())
	set := writeCandidates(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}]`)
	rt := relay.Runtime{
		Store:      s,
		Candidates: set,
		Runner:     stubRunner{},
		LedgerPath: filepath.Join(t.TempDir(), "ledger.json"),
		Now:        func() time.Time { return time.Unix(0, 0) },
	}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner:          store.Endpoint{PaneID: "w2:p3"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless},
		BuilderCandidate: "agy/test/m",
		Round:            1, State: store.StateActive,
	})

	v := &RelayVerbs{RT: rt, Planner: mcpTestPlannerA}
	plan := writeTempPlan(t, "# do the thing")

	res, err := v.Send(context.Background(), SendArgs{Name: "webshop", File: plan, DryRun: true})
	if err != nil {
		t.Fatalf("Send dry-run: %v", err)
	}
	d, ok := res.(relay.DryRun)
	if !ok {
		t.Fatalf("result = %#v, want relay.DryRun", res)
	}
	if d.Mode != "headless" {
		t.Errorf("Mode = %q, want headless", d.Mode)
	}
	if d.Name != "webshop" || d.Round != 1 {
		t.Errorf("DryRun = %+v, want Name webshop, Round 1", d)
	}
}

// TestRelayVerbsSendRealRunHeadless proves DryRun false takes the real
// relay.Send path, not SendDryRun: for a headless binding this needs no
// live pane either, so it runs to completion against stubRunner.
func TestRelayVerbsSendRealRunHeadless(t *testing.T) {
	s := store.New(t.TempDir())
	set := writeCandidates(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}]`)
	rt := relay.Runtime{
		Store:      s,
		Candidates: set,
		Runner:     stubRunner{},
		LedgerPath: filepath.Join(t.TempDir(), "ledger.json"),
		Now:        func() time.Time { return time.Unix(0, 0) },
	}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner:          store.Endpoint{PaneID: "w2:p3"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless},
		BuilderCandidate: "agy/test/m",
		Round:            1, State: store.StateActive,
	})

	v := &RelayVerbs{RT: rt, Planner: mcpTestPlannerA}
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

// TestRelayVerbsDoneForwardsAndReportsText proves Done calls relay.Done
// (which flips the binding's stored State to done) and reports relay.DoneText
// alongside the structured DoneResult.
func TestRelayVerbsDoneForwardsAndReportsText(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relay.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{Mode: store.ModeHeadless},
		Round:   1, State: store.StateActive,
	})

	v := &RelayVerbs{RT: rt, Planner: mcpTestPlannerA}
	res, err := v.Done(context.Background(), DoneArgs{Name: "webshop"})
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	dr, ok := res.(doneResult)
	if !ok {
		t.Fatalf("result = %#v, want doneResult", res)
	}
	if dr.Text == "" {
		t.Error("Text must be set from relay.DoneText")
	}

	updated, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if updated.State != store.StateDone {
		t.Errorf("State = %q, want done", updated.State)
	}
}

func TestRelayVerbsDoneErrorPropagates(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relay.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	v := &RelayVerbs{RT: rt, Planner: mcpTestPlannerA}

	if _, err := v.Done(context.Background(), DoneArgs{Name: "nonexistent"}); err == nil {
		t.Fatal("Done on a binding that does not exist must error")
	}
}
