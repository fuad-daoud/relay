package relay

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// sentBinding puts a binding one Send into round 1, with a working builder.
// sentBindingWithBuilderSession is sentBinding but the builder pane is
// spawned with a known session id recorded on the binding, which the
// broken-recovery tests need to exercise the session-id match.
// reconcile wraps Reconcile with the state lock a daemon would hold across
// the whole read-reconcile-write, since Reconcile itself takes a *store.Tx
// rather than locking on its own.
// TestReconcileDrainsPaneSessionRecord pins Reconcile's pane path (#184):
// once refreshEndpoint has run, drainSession renders whatever the builder's
// own session record holds since the last tick into the round's log, the
// way reconcileHeadless drains a headless stream.
// TestReconcileWaitsOutNudgeGraceBeforeScraping is the regression test for a
// scrape that raced the builder: relay nudged on one tick and scraped on the
// next, two seconds later, then advanced the round -- so the builder's real
// report was written to an abandoned round's path and never relayed.
// TestReconcileSkipsPaused: Reconcile returns immediately for a PAUSED
// binding, exactly as it does for DONE. A stale builder pane left in herdr's
// list must not mark it broken and must not be nudged.
// TestReconcileUnbreaksWhenPaneAndKindMatchWithoutSession covers the case where an
// agent is inspected before any session has been reported or recorded, so there is
// nothing yet to backfill and pane plus kind is the whole identity. Unlike
// TestReconcileUnbreaksSessionlessBuilderAndBackfillsSession (which exercises the
// path where herdr already reports a session to backfill), this exercises recovery
// during the pre-session window (e.g. freshly spawned idle agy builder).
// TestBindRacesSessionLookupAndReconcileRecovers reproduces #20 end to end: the
// builder is spawned, the post-spawn session lookup loses its race with the
// agent's own registration, herdr flickers and the binding is flagged BROKEN --
// and relay recovers it by itself rather than stranding a working builder.
// TestQueueReportRecordsRusage: a headless round's report entry gets
// Rusage from rt.Runner.Rusage when the runner has one, and stays nil
// when it does not (#244, #216).
// closeOnMarkerUnderLock calls closeOnMarker the way Reconcile does: inside
// the store lock, with the binding's current log.
func closeOnMarkerUnderLock(t *testing.T, rt Runtime, b store.Binding) (store.Binding, bool) {
	t.Helper()
	out, closed, _ := closeOnMarkerUnderLockGating(t, rt, b)
	return out, closed
}

// closeOnMarkerUnderLockGating is closeOnMarkerUnderLock plus the gating
// return, for the gate lifecycle tests (#132).
func closeOnMarkerUnderLockGating(t *testing.T, rt Runtime, b store.Binding) (store.Binding, bool, bool) {
	t.Helper()
	var out store.Binding
	var closed, gating bool
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		out, closed, gating, _, err = closeOnMarker(context.Background(), rt, tx, b, entries, "")
		return err
	})
	if err != nil {
		t.Fatalf("closeOnMarker: %v", err)
	}
	return out, closed, gating
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("touch %s: %v", path, err)
	}
}

// TestCloseOnMarkerWithoutReportIsNoreport pins §4.1: the builder said it was
// done, so relay closes on that and says the report is missing, instead of
// waiting for idle and scraping a worse artefact. Mutation: fall through to
// scrapeReport -> f.reads is non-empty and the note is "scraped".
// TestReconcileClosesOnMarkerWhileBuilderStillWorking pins §4.2: the marker
// is checked every tick, before herdr's status is consulted. Mutation: move
// the closeOnMarker call inside the idle branch -> this fails because the
// builder is reported working.
// TestReconcileReportWithoutMarkerIsNotAClose pins §4.3: a report on disk is
// not evidence the builder is finished. Mutation: gate on the report instead
// of the marker -> the round advances on the first tick.
// TestReconcileQuiescentWithReportClosesUnmarked pins the fallback: after the
// nudge and a still screen, the report that exists is delivered with the
// omission named, never scraped over. Mutation: drop the "unmarked" note ->
// fails; scrape instead of queueing the report -> the body check fails.
// TestReconcileQuiescentWithoutReportStillScrapes is the regression pin for
// the scrape path: no report and no marker after quiescence is exactly what
// it was before the marker existed.
// TestReconcileScrapedBodyIsNeverTailParsed pins #221: a scraped body is a
// terminal capture, never the builder's own report file, so queueReport must
// not call parseReportTail on it regardless of whether the text looks like a
// well-formed relay block.
// TestReconcileQuiescentOnLimitSwitchesInsteadOfScraping checks the pane
// quiescence decision point (spec §5): a match on the screen switches the
// builder uncounted instead of scraping a report.
// TestReconcileQuiescentWithReportOnLimitGatesAndClosesUnmarked checks that
// a report already on disk still wins over the gate (spec §4.4): the gate is
// recorded, but the round closes unmarked instead of switching.
// anySpecArgv reports whether any process the runner started was handed argv
// naming path.
func anySpecArgv(fr *fakeRunner, path string) bool {
	for _, sp := range fr.specs {
		if strings.Contains(strings.Join(sp.Argv, " "), path) {
			return true
		}
	}
	return false
}

// gates returns the gate entries in webshop's log.
func gates(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindGate {
			out = append(out, e)
		}
	}
	return out
}

// TestGateNotConfiguredIsUnchanged pins #132: a binding with no gate closes
// exactly as it did before the gate existed -- no process is started and the
// payload is unchanged.
// TestGateStartsOnMarkerAndHoldsTheRound pins #132's state machine: a
// configured gate starts on the marker tick and holds the round across
// ticks -- no nudge, no exit/switch handling -- until it finishes.
//
// Mutation check (run and report): make the pane call site ignore gating
// (fall through to the status switch instead of returning early); this test
// fails because the second tick's nudge reaches fakeHerdr; restore; passes.
// TestGatePassClosesWithAnnotation pins #132: a gate that exits 0 closes the
// round with a gate=pass annotation, a Gate record on the entry, and the
// gate's payload line.
// TestGateFailAddsTail pins #132: a gate that exits non-zero closes the
// round with gate=fail, and the payload carries the log's last
// gateTailLines non-empty lines, not the first.
// TestGateTimeoutKills pins #132: a gate that outlives its timeout is
// killed and the round closes with gate=timeout.
// TestGateNoRunnerIsErrorNotHang pins #132: a gate configured on a runtime
// with no Runner cannot hang the round -- it closes this tick with a
// gate=error annotation instead.
// gateRecordFor finds a binding's report entry for one round and returns its
// gate record, failing the test when the round has no report entry. It is how
// the regate tests inspect a round that closed earlier than the one currently
// in flight: PendingForPlanner only ever hands back the oldest.
func gateRecordFor(t *testing.T, rt Runtime, name string, round int) *store.GateRecord {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog(%s): %v", name, err)
	}
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToPlanner && e.Kind == store.KindReport {
			return e.Gate
		}
	}
	t.Fatalf("no report entry for round %d of %s", round, name)
	return nil
}

// failRoundWithGate drives b's current round through the real Reconcile call
// site until its gate exits non-zero: the first tick starts the gate, the
// second (after the fake runner is told the process exited) closes the round
// with gate=fail. It returns the binding Reconcile returned and the failing
// record, and fails the test if the round did not close on the gate.
// TestRegateFailOpensRepairRound pins #132 part 2: a failing gate with a
// budget stages round N+1 as a repair plan, hands it to the builder exactly as
// Send would, and logs `repair k/M` on the new round's plan entry.
// TestRegateBoundHaltsNeedsYou pins the count bound (#132 part 2): once the
// budget is spent, the next failing gate ends the loop with NEEDS YOU instead
// of another repair round.
// TestRegateIdenticalSignatureHaltsEarly pins the stall bound (#132 part 2):
// a second identical failure -- same content modulo the clock -- means the
// repair changed nothing that mattered, so the loop ends early.
// TestRegatePassResetsCount pins #132 part 2's reset: a passing gate clears
// the repair bookkeeping, so the next failing gate gets a fresh budget.
// TestNoRegateUnchanged pins the off switch (#132 part 2): Regate 0 is
// exactly today's behaviour -- the failure is reported, nothing is re-sent.
// TestSendResetsRepairBookkeeping pins #132 part 2: a human send is a fresh
// start -- it clears the repair bookkeeping and, when --regate is given, sets
// the binding's budget.
// roundReportEntry is webshop's queued report entry for round. It is the
// non-e2e form of e2e_test.go's reportEntry, which is behind the e2e tag.
func roundReportEntry(t *testing.T, rt Runtime, round int) store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToPlanner && e.Kind == store.KindReport {
			return e
		}
	}
	t.Fatalf("no report entry for round %d", round)
	return store.LogEntry{}
}

// TestReportEntryCarriesPaneSession pins #147: a pane builder's report entry
// names the session herdr reports for it, refreshed onto the endpoint on the
// tick that closes the round.
// TestReportEntryNoSessionIsNil pins #147's never-guess rule: a pane whose
// agent reports no session leaves BuilderSession nil, and the entry's JSON
// carries no builder_session key.
// TestReconcilePaneStampsStall pins #135's pane path: a pane builder that has
// been working with an unchanged tree, screen and round for longer than
// stall_after_ms is stamped StalledSince, and the daemon raises one
// builder_stalled hook event and one advisory notice. The stall is an
// observation: the process is untouched and the binding stays ACTIVE.
// TestReconcileNeedsYouGoesStale pins #135's stale path: a binding that has
// been NEEDS YOU past stale_after_ms is stamped from its halt time, fires one
// binding_stale event and one notice, and a second tick adds neither. A human
// Send clears the stamp and the notification bookkeeping.
// TestVerifyRoundStartsAReviewerInAThrowawayWorktree pins #144's close path:
// a round sent with --verify closes exactly as before, and the close then
// creates a detached worktree at the builder's HEAD and launches one read-only
// headless reviewer in it -- with a question that names the ask file, whose
// content asks the reviewer to verify round 1 with no gate.
//
// Mutation check (run and report): delete the wantVerify block from
// Reconcile's close path and this fails on addDetachedWorktreeCalls.
// TestVerifyGateLogIsPassed pins #144's gate handoff: the reviewer is told
// where the closed round's gate log is, because seeing the gate's own output
// is the point of running verify after the gate.
// candidateSetWithoutReviewerJSON serves builder and nothing else, so
// resolveCandidate refuses every reviewer.
const candidateSetWithoutReviewerJSON = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"agy","provider":"test","model":"m","roles":["builder"]}
]`

// TestVerifySkippedWhenNoReviewerCandidate pins #144's error handling: a
// round with no reviewer candidate closes normally, logs one "verify skipped:"
// note, starts nothing, and leaves no throwaway worktree behind.
// retiredEntries returns the binding's KindRetired entries.
func retiredEntries(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindRetired {
			out = append(out, e)
		}
	}
	return out
}

// TestReconcileRetiresLegacyPaneBinding pins #303 §5.6: a legacy pane binding
// still active at upgrade becomes DONE with one `retired` entry, its worktree
// left exactly as it is; a second tick adds none; a remote binding is
// untouched; and a PAUSED pane binding is retired too.
