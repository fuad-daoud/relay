package relay

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// sentSwitchable binds webshop to agy/other/m with the three-builder order
// and hands it round 1, so a rate limit on provider "other" leaves claude
// and opencode (provider "test") available to switch to.
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

// gone is the agents list Reconcile sees when webshop's builder cannot be
// located.
// TestSwitchRearmsSessionCursor pins switchBuilder's pane re-arm (#184,
// round 2 correction to base plan §4): the replacement pane's own session
// record starts empty, so the cursor is cut at offset 0 for the SAME
// round's log -- but the marker line stays headless-only (the round-1
// halt), so the log file itself must not exist yet.
// TestGatedSwitchDoesNotCount checks the two halves of §4.5 together: a
// gated switch does not advance RoundSwitches, but the >= limit check at
// the top of switchBuilder still applies to it -- a binding already at the
// limit still halts instead of switching again.
// TestExhaustionHalts pins spec §5.3: two switches succeed, the third
// trigger halts once RoundSwitches reaches the default limit of 2.
// TestExhaustionAfterResendStillSaysWhy pins #250 item 2: the second halt of
// a re-sent round still records its reason (Halt is always set, not only on
// the notifying branch), and a human's re-send is a fresh attempt -- it
// resets the round's switch budget and notification dedup, so the next
// exhaustion in the same round notifies again.
//
// Mutation check: move the `b.Halt =` line in haltBinding back inside the
// `if b.HaltNotifiedRound != b.Round` guard and this test must fail, because
// the second halt below would leave Halt empty (HaltNotifiedRound is
// already back at b.Round from the first halt's dedup).
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
