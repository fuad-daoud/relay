package relay

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// sentSwitchable binds webshop to agy/other/m with the three-builder order
// and hands it round 1, so a rate limit on provider "other" leaves claude
// and opencode (provider "test") available to switch to.
func sentSwitchable(t *testing.T, f *fakePanes) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []stubAgent{plannerAgent()}
	f.newPane = "w2:p4"
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", "agy/other/m", testClaudeRef, testOpencodeRef)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/other/m", PlannerPane: "w2:p3", CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	f.agents = append(f.agents, builderAgent(stubWorking))
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
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
func gone() []stubAgent { return []stubAgent{plannerAgent()} }

// present is the agents list Reconcile sees when webshop's builder is
// there, working.
func present() []stubAgent {
	return []stubAgent{plannerAgent(), builderAgent(stubWorking)}
}

// TestSwitchRearmsSessionCursor pins switchBuilder's pane re-arm (#184,
// round 2 correction to base plan §4): the replacement pane's own session
// record starts empty, so the cursor is cut at offset 0 for the SAME
// round's log -- but the marker line stays headless-only (the round-1
// halt), so the log file itself must not exist yet.
func TestSwitchRearmsSessionCursor(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentSwitchable(t, f)

	got, err := reconcile(t, rt, b, gone())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, err = reconcile(t, at(rt, 31*time.Second), got, gone())
	if err != nil {
		t.Fatalf("Reconcile at +31s: %v", err)
	}

	if got.Builder.StreamRound != got.Round {
		t.Errorf("StreamRound = %d, want round %d", got.Builder.StreamRound, got.Round)
	}
	if got.Builder.StreamOffset != 0 {
		t.Errorf("StreamOffset = %d, want 0 (the replacement's own record starts empty)", got.Builder.StreamOffset)
	}
	if want := rt.Store.BuilderLogPath(got.Name, got.Round); got.Builder.LogPath != want {
		t.Errorf("LogPath = %q, want %q", got.Builder.LogPath, want)
	}

	logPath := rt.Store.BuilderLogPath(got.Name, got.Round)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("pane switch created a log file: %s", logPath)
	}
}

func TestGoneThenBackClearsMissing(t *testing.T) {
	f := &fakePanes{}
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

func TestGatedIgnoresSpawnFailedGate(t *testing.T) {
	f := &fakePanes{}
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

// TestRepeatedHaltKeepsHaltAt pins the fix in this round: HaltAt marks when
// a halt begins, not every tick that repeats it. haltBinding called three
// times with the same message keeps HaltAt at the first call's time; a
// different message restamps it; and clearing Halt by hand (as Send does)
// and repeating the same message restamps it too, since that is a fresh
// halt beginning from the caller's point of view.
//
// Mutation check: stamp HaltAt unconditionally (round 1's behavior) and the
// first assertion below fails, since the second and third calls would each
// advance it by a minute.
func TestRepeatedHaltKeepsHaltAt(t *testing.T) {
	f := &fakePanes{}
	rt, b := sentBinding(t, f)

	first, err := haltBinding(context.Background(), rt, b, "webshop: same reason")
	if err != nil {
		t.Fatalf("haltBinding #1: %v", err)
	}
	firstHaltAt := first.HaltAt
	if firstHaltAt.IsZero() {
		t.Fatal("HaltAt is zero after the first halt, want set")
	}

	second, err := haltBinding(context.Background(), at(rt, time.Minute), first, "webshop: same reason")
	if err != nil {
		t.Fatalf("haltBinding #2: %v", err)
	}
	if !second.HaltAt.Equal(firstHaltAt) {
		t.Errorf("HaltAt = %v after a repeated halt, want unchanged %v", second.HaltAt, firstHaltAt)
	}
	if second.Halt != "same reason" {
		t.Errorf("Halt = %q after a repeated halt, want unchanged %q", second.Halt, "same reason")
	}

	third, err := haltBinding(context.Background(), at(rt, 2*time.Minute), second, "webshop: same reason")
	if err != nil {
		t.Fatalf("haltBinding #3: %v", err)
	}
	if !third.HaltAt.Equal(firstHaltAt) {
		t.Errorf("HaltAt = %v after a third repeated halt, want unchanged %v", third.HaltAt, firstHaltAt)
	}

	// A different message is a new halt: HaltAt restamps to that call's time.
	fourth, err := haltBinding(context.Background(), at(rt, 3*time.Minute), third, "webshop: different reason")
	if err != nil {
		t.Fatalf("haltBinding #4: %v", err)
	}
	wantFourthHaltAt := baseTime.Add(3 * time.Minute)
	if !fourth.HaltAt.Equal(wantFourthHaltAt) {
		t.Errorf("HaltAt = %v after a new-text halt, want %v", fourth.HaltAt, wantFourthHaltAt)
	}
	if fourth.Halt != "different reason" {
		t.Errorf("Halt = %q after a new-text halt, want %q", fourth.Halt, "different reason")
	}

	// Halt cleared by hand (as Send does) and the same message again is,
	// from haltBinding's point of view, a new halt beginning: HaltAt
	// restamps even though the text matches what it was before clearing.
	fourth.Halt = ""
	fifth, err := haltBinding(context.Background(), at(rt, 4*time.Minute), fourth, "webshop: different reason")
	if err != nil {
		t.Fatalf("haltBinding #5: %v", err)
	}
	wantFifthHaltAt := baseTime.Add(4 * time.Minute)
	if !fifth.HaltAt.Equal(wantFifthHaltAt) {
		t.Errorf("HaltAt = %v after a cleared-then-repeated halt, want %v", fifth.HaltAt, wantFifthHaltAt)
	}
	if fifth.Halt != "different reason" {
		t.Errorf("Halt = %q after a cleared-then-repeated halt, want %q", fifth.Halt, "different reason")
	}
}
