package relay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

// sentSwitchable binds webshop to agy/other/m with the three-builder order
// and hands it round 1, so a rate limit on provider "other" leaves claude
// and opencode (provider "test") available to switch to.
func sentSwitchable(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []herdr.Agent{plannerAgent()}
	f.newPane = "w2:p4"
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", "agy/other/m", testClaudeRef, testOpencodeRef)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/other/m", PlannerPane: "w2:p3", CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	f.agents = append(f.agents, builderAgent(herdr.StatusWorking))
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f.prompts, f.starts, f.closed, f.notices = nil, nil, nil, nil
	f.newPane = "w2:p9" // where a replacement would land
	return rt, b
}

// at returns rt with its clock moved to baseTime + d.
func at(rt Runtime, d time.Duration) Runtime {
	rt.Now = func() time.Time { return baseTime.Add(d) }
	return rt
}

// switches returns the switch entries in webshop's log.
func switches(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindSwitch {
			out = append(out, e)
		}
	}
	return out
}

// gone is the agents list Reconcile sees when webshop's builder cannot be
// located.
func gone() []herdr.Agent { return []herdr.Agent{plannerAgent()} }

// present is the agents list Reconcile sees when webshop's builder is
// there, working.
func present() []herdr.Agent {
	return []herdr.Agent{plannerAgent(), builderAgent(herdr.StatusWorking)}
}

func TestGoneStampsMissingAndWaits(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)

	got, err := reconcile(t, rt, b, gone())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want broken", got.State)
	}
	if got.BuilderMissingSince != baseTime {
		t.Errorf("BuilderMissingSince = %s, want %s", got.BuilderMissingSince, baseTime)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none", f.starts)
	}

	got, err = reconcile(t, at(rt, 29*time.Second), got, gone())
	if err != nil {
		t.Fatalf("Reconcile at +29s: %v", err)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want still broken at +29s", got.State)
	}
	if got.BuilderMissingSince != baseTime {
		t.Errorf("BuilderMissingSince = %s, want unchanged %s", got.BuilderMissingSince, baseTime)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none at +29s", f.starts)
	}
}

func TestGoneSwitchesAfterGrace(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)

	got, err := reconcile(t, rt, b, gone())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err = reconcile(t, at(rt, 31*time.Second), got, gone())
	if err != nil {
		t.Fatalf("Reconcile at +31s: %v", err)
	}

	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
	if len(f.starts) != 1 || f.starts[0].Kind != "agy" {
		t.Fatalf("starts = %+v, want one agy start", f.starts)
	}
	if got.Builder.PaneID != "w2:p9" {
		t.Errorf("Builder.PaneID = %q, want w2:p9", got.Builder.PaneID)
	}
	if got.BuilderCandidate != "agy/other/m" {
		t.Errorf("BuilderCandidate = %q, want agy/other/m", got.BuilderCandidate)
	}
	if got.Round != 1 {
		t.Errorf("Round = %d, want 1 (unchanged)", got.Round)
	}
	if got.RoundSwitches != 1 {
		t.Errorf("RoundSwitches = %d, want 1", got.RoundSwitches)
	}
	if !got.BuilderMissingSince.IsZero() {
		t.Errorf("BuilderMissingSince = %s, want zero", got.BuilderMissingSince)
	}
	wantStart := baseTime.Add(31 * time.Second).UTC()
	if got.RoundStartedAt != wantStart {
		t.Errorf("RoundStartedAt = %s, want %s", got.RoundStartedAt, wantStart)
	}
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0].Text, rt.Store.PlanPath("webshop", 1)) {
		t.Fatalf("prompts = %+v, want one containing the round's plan path", f.prompts)
	}
	if len(f.closed) != 0 {
		t.Errorf("closed = %+v, want none -- the gone trigger has no pane to close", f.closed)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "switched builder to agy/other/m") {
		t.Fatalf("notices = %+v, want one containing 'switched builder to agy/other/m'", f.notices)
	}

	sw := switches(t, rt)
	if len(sw) != 1 {
		t.Fatalf("switch entries = %d, want 1", len(sw))
	}
	if !sw[0].Confirmed || sw[0].Round != 1 {
		t.Errorf("switch entry = %+v, want Confirmed and Round 1", sw[0])
	}
	wantPrefix := "switched builder (gone for 31s): picked agy/other/m for builder: order #1"
	if !strings.HasPrefix(sw[0].Note, wantPrefix) {
		t.Errorf("switch note = %q, want prefix %q", sw[0].Note, wantPrefix)
	}
}

func TestGoneThenBackClearsMissing(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)

	got, err := reconcile(t, rt, b, gone())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err = reconcile(t, at(rt, 10*time.Second), got, present())
	if err != nil {
		t.Fatalf("Reconcile at +10s: %v", err)
	}

	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
	if !got.BuilderMissingSince.IsZero() {
		t.Errorf("BuilderMissingSince = %s, want zero after a hit", got.BuilderMissingSince)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none -- a flicker never accumulates toward a switch", f.starts)
	}
}

func TestGatedSwitchesAtOnce(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b, present())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(f.closed) != 1 || f.closed[0] != "w2:p4" {
		t.Fatalf("closed = %+v, want [w2:p4]", f.closed)
	}
	if len(f.starts) != 1 || f.starts[0].Kind != "claude" {
		t.Fatalf("starts = %+v, want one claude start", f.starts)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
	if got.Round != 1 {
		t.Errorf("Round = %d, want 1", got.Round)
	}
	if got.RoundSwitches != 1 {
		t.Errorf("RoundSwitches = %d, want 1", got.RoundSwitches)
	}

	sw := switches(t, rt)
	if len(sw) != 1 {
		t.Fatalf("switch entries = %d, want 1", len(sw))
	}
	wantNote := "switched builder (rate-limited: 5h window): picked claude/test/m for builder: order #2; skipped agy/other/m (rate-limited until cleared)"
	if !strings.HasPrefix(sw[0].Note, wantNote) {
		t.Errorf("switch note = %q, want prefix %q", sw[0].Note, wantNote)
	}
	if len(f.notices) != 1 {
		t.Errorf("notices = %+v, want exactly one", f.notices)
	}
}

func TestGatedIgnoresSpawnFailedGate(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)
	recordSpawnFailure(rt, "agy/other/m", "webshop", errors.New("x"))

	got, err := reconcile(t, rt, b, present())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none -- a running builder is not a failed spawn", f.starts)
	}
	if len(f.closed) != 0 {
		t.Errorf("closed = %+v, want none", f.closed)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
}

func TestNoOrderHalts(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)
	rt.Policy = policy.Policy{}
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b, present())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none", f.starts)
	}
	if len(f.notices) != 1 ||
		!strings.Contains(f.notices[0], "cannot switch") ||
		!strings.Contains(f.notices[0], `candidates serve "builder"`) {
		t.Fatalf("notices = %+v, want one containing 'cannot switch' and `candidates serve \"builder\"`", f.notices)
	}
}

func TestMaxSwitchesZeroHalts(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)
	zero := 0
	rt.Policy.MaxSwitches = &zero
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b, present())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none", f.starts)
	}
	if len(f.closed) != 0 {
		t.Errorf("closed = %+v, want none", f.closed)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "already switched 0 time(s) this round (max_switches 0)") {
		t.Fatalf("notices = %+v, want one containing the max_switches 0 message", f.notices)
	}
}

// TestExhaustionHalts pins spec §5.3: two switches succeed, the third
// trigger halts once RoundSwitches reaches the default limit of 2.
func TestExhaustionHalts(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)

	got, err := reconcile(t, rt, b, gone()) // +0: stamp
	if err != nil {
		t.Fatalf("Reconcile +0: %v", err)
	}
	got, err = reconcile(t, at(rt, 31*time.Second), got, gone()) // +31s: switch #1
	if err != nil {
		t.Fatalf("Reconcile +31s: %v", err)
	}
	got, err = reconcile(t, at(rt, 40*time.Second), got, gone()) // +40s: stamp
	if err != nil {
		t.Fatalf("Reconcile +40s: %v", err)
	}
	got, err = reconcile(t, at(rt, 71*time.Second), got, gone()) // +71s: switch #2
	if err != nil {
		t.Fatalf("Reconcile +71s: %v", err)
	}
	got, err = reconcile(t, at(rt, 80*time.Second), got, gone()) // +80s: stamp
	if err != nil {
		t.Fatalf("Reconcile +80s: %v", err)
	}
	got, err = reconcile(t, at(rt, 111*time.Second), got, gone()) // +111s: exhausted
	if err != nil {
		t.Fatalf("Reconcile +111s: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if len(f.starts) != 2 {
		t.Errorf("starts = %d, want 2", len(f.starts))
	}
	if got.RoundSwitches != 2 {
		t.Errorf("RoundSwitches = %d, want 2", got.RoundSwitches)
	}
	if got.HaltNotifiedRound != 1 {
		t.Errorf("HaltNotifiedRound = %d, want 1", got.HaltNotifiedRound)
	}

	var haltMsg string
	count := 0
	for _, n := range f.notices {
		if strings.Contains(n, "already switched") {
			haltMsg = n
			count++
		}
	}
	if count != 1 {
		t.Fatalf("notices containing 'already switched' = %d, want 1", count)
	}
	t.Logf("halt message: %s", haltMsg)

	got, err = reconcile(t, at(rt, 113*time.Second), got, gone()) // +113s: dedup
	if err != nil {
		t.Fatalf("Reconcile +113s: %v", err)
	}
	_ = got

	count = 0
	for _, n := range f.notices {
		if strings.Contains(n, "already switched") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("notices containing 'already switched' after a second halted tick = %d, want 1 (dedup)", count)
	}
}

func TestAllGatedHalts(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b, present())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none", f.starts)
	}
	if len(f.closed) != 0 {
		t.Errorf("closed = %+v, want none -- the halt precedes the close", f.closed)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], `every candidate serving "builder" is gated`) {
		t.Fatalf("notices = %+v, want one containing the ErrAllGated text", f.notices)
	}
}

func TestSpawnFailureWalksOn(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	f.startErr = errors.New("agent start: exit 1")

	got, err := reconcile(t, rt, b, present())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateBroken {
		t.Errorf("state = %s, want broken", got.State)
	}
	if got.RoundSwitches != 1 {
		t.Errorf("RoundSwitches = %d, want 1", got.RoundSwitches)
	}
	if len(f.closed) != 1 || f.closed[0] != "w2:p4" {
		t.Fatalf("closed = %+v, want [w2:p4]", f.closed)
	}
	if sw := switches(t, rt); len(sw) != 0 {
		t.Errorf("switch entries = %+v, want none -- the switch never completed", sw)
	}
	l := loadLedger(t, rt)
	spawnFailed := 0
	for _, e := range l.Entries {
		if e.Kind == ledger.SpawnFailed && e.Subject == testClaudeRef {
			spawnFailed++
		}
	}
	if spawnFailed != 1 {
		t.Errorf("spawn_failed entries for %s = %d, want 1", testClaudeRef, spawnFailed)
	}

	f.startErr = nil
	f.newPane = "w2:p10"

	got, err = reconcile(t, rt, got, gone()) // +0: stamp missing
	if err != nil {
		t.Fatalf("Reconcile +0: %v", err)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want broken", got.State)
	}

	got, err = reconcile(t, at(rt, 31*time.Second), got, gone()) // +31s: walk to opencode
	if err != nil {
		t.Fatalf("Reconcile +31s: %v", err)
	}

	if len(f.starts) != 2 || f.starts[1].Kind != "opencode" {
		t.Fatalf("starts = %+v, want a second start with Kind opencode (claude is now gated by its spawn failure)", f.starts)
	}
	if got.RoundSwitches != 2 {
		t.Errorf("RoundSwitches = %d, want 2", got.RoundSwitches)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
}

func TestAdoptedBuilderIsNeverSwitched(t *testing.T) {
	existing := herdr.Agent{Kind: "claude", Status: herdr.StatusIdle, PaneID: "w2:p8", CWD: "/repo"}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent(), existing}}
	rt := newRuntime(t, f)

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", BuilderPane: "w2:p8", PlannerPane: "w2:p3", CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	f.agents = []herdr.Agent{plannerAgent(), {Kind: "claude", Status: herdr.StatusWorking, PaneID: "w2:p8", CWD: "/repo"}}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f.prompts, f.starts = nil, nil

	got, err := reconcile(t, rt, b, gone())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want broken", got.State)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none -- an adopted builder is never switched", f.starts)
	}

	got, err = reconcile(t, at(rt, 60*time.Second), got, gone())
	if err != nil {
		t.Fatalf("Reconcile at +60s: %v", err)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want still broken at +60s", got.State)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none at +60s", f.starts)
	}
}

func TestNoOpenRoundIsNeverSwitched(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)
	b.RoundStartedAt = time.Time{} // as queueReport leaves it between rounds

	got, err := reconcile(t, rt, b, gone())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want broken", got.State)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none -- no round is open", f.starts)
	}

	got, err = reconcile(t, at(rt, 60*time.Second), got, gone())
	if err != nil {
		t.Fatalf("Reconcile at +60s: %v", err)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want still broken at +60s", got.State)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none at +60s", f.starts)
	}
}

func TestSwitchTickDoesNotDeliver(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)

	entry := store.LogEntry{
		Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Payload: "Builder finished round 1. Report: /x/001-report.md",
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return Queue(context.Background(), rt, tx, b.Name, entry)
	}); err != nil {
		t.Fatalf("Queue: %v", err)
	}

	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	if _, err := reconcile(t, rt, b, present()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if _, found, err := rt.Store.PendingForPlanner("webshop"); err != nil || !found {
		t.Fatalf("the queued payload must still be pending: found=%v err=%v", found, err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("prompts = %+v, want exactly the one builder handoff prompt -- the pending payload must not be delivered", f.prompts)
	}
}

func TestCloseFailureHalts(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentSwitchable(t, f)
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	f.closeErr = errors.New("nope")

	got, err := reconcile(t, rt, b, present())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none", f.starts)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "could not close its pane w2:p4") {
		t.Fatalf("notices = %+v, want one containing 'could not close its pane w2:p4'", f.notices)
	}
}
