package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/store"
)

// runnerOf is the fakeRunner a fixture installed on rt.
func runnerOf(t *testing.T, rt Runtime) *fakeRunner {
	t.Helper()
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("runtime has no fakeRunner")
	}
	return fr
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

// TestSwitchRearmsSessionCursor pins switchBuilder's replacement-pane re-arm
// (#184, round 2 correction to base plan §4): the replacement process starts
// on the SAME round's plan, and the switch is recorded in the log.
//
// Mutation check (run and report): break switchBuilder's gated-candidate walk
// and this fails.
func TestGatedSwitchesAtOnce(t *testing.T) {
	rt, b := sentSwitchable(t)
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	fr := runnerOf(t, rt)
	if len(fr.kills) != 1 {
		t.Fatalf("kills = %+v, want the old round's process killed", fr.kills)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("starts = %d, want 2 (round 1 and its replacement)", len(fr.specs))
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
	if got.Round != 1 {
		t.Errorf("Round = %d, want 1", got.Round)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0; a gated switch is uncounted", got.RoundSwitches)
	}

	sw := switches(t, rt)
	if len(sw) != 1 {
		t.Fatalf("switch entries = %d, want 1", len(sw))
	}
	wantNote := "switched builder (rate-limited: 5h window): picked claude/test/m for builder: order #2; skipped agy/other/m (rate-limited until cleared)"
	if !strings.HasPrefix(sw[0].Note, wantNote) {
		t.Errorf("switch note = %q, want prefix %q", sw[0].Note, wantNote)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active after a gated switch", got.State)
	}
}

// TestGatedSwitchDoesNotCount checks the two halves of §4.5 together: a
// gated switch does not advance RoundSwitches, but the >= limit check at
// the top of switchBuilder still applies to it -- a binding already at the
// limit still halts instead of switching again.
func TestGatedSwitchDoesNotCount(t *testing.T) {
	rt, b := sentSwitchable(t)
	limit := rt.Policy.SwitchLimit()
	b.RoundSwitches = limit - 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(runnerOf(t, rt).specs) != 2 {
		t.Fatalf("starts = %d, want 2 (round 1 and its replacement)", len(runnerOf(t, rt).specs))
	}
	sw := switches(t, rt)
	if len(sw) != 1 {
		t.Fatalf("switch entries = %d, want 1", len(sw))
	}
	if got.RoundSwitches != limit-1 {
		t.Errorf("RoundSwitches = %d, want %d; a gated switch does not advance the count", got.RoundSwitches, limit-1)
	}

	rt2, b2 := sentSwitchable(t)
	b2.RoundSwitches = rt2.Policy.SwitchLimit()
	if err := rt2.Store.Save(b2); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Unavailable(rt2, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	got2, err := reconcile(t, rt2, b2)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got2.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you; the limit check still applies to a gated switch", got2.State)
	}
	if len(runnerOf(t, rt2).specs) != 1 {
		t.Errorf("starts = %d, want none beyond round 1", len(runnerOf(t, rt2).specs))
	}
}

func TestGatedIgnoresSpawnFailedGate(t *testing.T) {
	rt, b := sentSwitchable(t)
	recordSpawnFailure(rt, "agy/other/m", "webshop", errors.New("x"))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := len(runnerOf(t, rt).specs); got != 1 {
		t.Errorf("starts = %d, want none beyond round 1 -- a running builder is not a failed spawn", got)
	}
	if len(runnerOf(t, rt).kills) != 0 {
		t.Errorf("kills = %+v, want none", runnerOf(t, rt).kills)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
}

func TestNoOrderHalts(t *testing.T) {
	rt, b := sentSwitchable(t)
	rt.Policy = policy.Policy{}
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if len(runnerOf(t, rt).specs) != 1 {
		t.Errorf("starts = %d, want none beyond round 1", len(runnerOf(t, rt).specs))
	}
	if !strings.Contains(got.Halt, "cannot switch") ||
		!strings.Contains(got.Halt, `candidates serve "builder"`) {
		t.Fatalf("Halt = %q, want it to contain 'cannot switch' and `candidates serve \"builder\"`", got.Halt)
	}
}

func TestMaxSwitchesZeroHalts(t *testing.T) {
	rt, b := sentSwitchable(t)
	zero := 0
	rt.Policy.MaxSwitches = &zero
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if got := len(runnerOf(t, rt).specs); got != 1 {
		t.Errorf("starts = %d, want none beyond round 1", got)
	}
	if len(runnerOf(t, rt).kills) != 0 {
		t.Errorf("kills = %+v, want none", runnerOf(t, rt).kills)
	}
	if !strings.Contains(got.Halt, "already switched 0 time(s) this round (max_switches 0)") {
		t.Fatalf("Halt = %q, want it to contain the max_switches 0 message", got.Halt)
	}
}

// TestExhaustionAfterResendStillSaysWhy pins #250 item 2: the second halt of
// a re-sent round still records its reason (Halt is always set, not only on
// the notifying branch), and a human's re-send is a fresh attempt -- it
// resets the round's switch budget and notification dedup, so the next
// exhaustion in the same round records its reason again.
//
// Mutation check: move the `b.Halt =` line in haltBinding back inside the
// `if b.HaltNotifiedRound != b.Round` guard and this test must fail, because
// the second halt below would leave Halt empty (HaltNotifiedRound is
// already back at b.Round from the first halt's dedup).
func TestExhaustionAfterResendStillSaysWhy(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 2)
	b.RoundSwitches = rt.Policy.SwitchLimit()
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "max_switches") {
		t.Fatalf("Halt = %q, want it to contain %q", got.Halt, "max_switches")
	}
	if got.HaltNotifiedRound != got.Round {
		t.Fatalf("HaltNotifiedRound = %d after the first halt, want %d", got.HaltNotifiedRound, got.Round)
	}

	// The human asks for another attempt: same round, a fresh Send.
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it again"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q after resend, want empty", got.Halt)
	}
	if got.HaltNotifiedRound != 0 {
		t.Errorf("HaltNotifiedRound = %d after resend, want 0", got.HaltNotifiedRound)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d after resend, want 0", got.RoundSwitches)
	}
	if got.RoundExcluded != nil {
		t.Errorf("RoundExcluded = %v after resend, want nil", got.RoundExcluded)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s after resend, want active", got.State)
	}

	// Drive the same round to exhaustion again with the fresh budget.
	fr.script(got.Builder.PID, false)
	fr.exit(got.Builder.PID, 2)
	got.RoundSwitches = rt.Policy.SwitchLimit()
	if err := rt.Store.Save(got); err != nil {
		t.Fatal(err)
	}
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "max_switches") {
		t.Errorf("Halt = %q, want it to contain %q again", got.Halt, "max_switches")
	}

	// haltBinding's own invariant, isolated from Send's coupling (Send
	// happens to reset HaltNotifiedRound every time it clears Halt, which
	// means the two exhaustion halts above enter the notify guard either
	// way and cannot, by themselves, distinguish Halt being set inside vs.
	// outside it). Call haltBinding directly with the round already
	// notified (dedup active, guard skipped) and confirm it still records
	// Halt without renotifying.
	if got.HaltNotifiedRound != got.Round {
		t.Fatalf("test setup: HaltNotifiedRound = %d, want %d (round already notified)", got.HaltNotifiedRound, got.Round)
	}
	got.Halt = ""
	beforeNotified := got.HaltNotifiedRound
	deduped, err := haltBinding(context.Background(), rt, got, "webshop: deduped halt check")
	if err != nil {
		t.Fatalf("haltBinding: %v", err)
	}
	if deduped.Halt == "" {
		t.Error("Halt is empty on a deduped halt, want the reason recorded")
	}
	if deduped.HaltNotifiedRound != beforeNotified {
		t.Errorf("HaltNotifiedRound = %d after a deduped halt, want unchanged %d", deduped.HaltNotifiedRound, beforeNotified)
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
	rt, b := sentBinding(t)

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

func TestAllGatedHalts(t *testing.T) {
	rt, b := sentSwitchable(t)
	if _, err := Unavailable(rt, "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if got := len(runnerOf(t, rt).specs); got != 1 {
		t.Errorf("starts = %d, want none beyond round 1", got)
	}
	if len(runnerOf(t, rt).kills) != 0 {
		t.Errorf("kills = %+v, want none -- the halt precedes the close", runnerOf(t, rt).kills)
	}
	if !strings.Contains(got.Halt, `every candidate serving "builder" is gated`) {
		t.Fatalf("Halt = %q, want it to contain the ErrAllGated text", got.Halt)
	}
}
