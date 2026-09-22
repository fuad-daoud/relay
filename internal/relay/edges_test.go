package relay

import (
	"os"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// edgePairStore seeds two minimal, unrelated active bindings ("api" and
// "client") in one store, with no herdr agents at all: enough for
// AddEdge/ListEdges/RemoveEdge, which never touch herdr.
// edgeSourceBinding binds "api" on a pane builder and sends it one round,
// sentBinding-style, but under the name the edge tests use throughout (#37).
// edgeSourceBindingHeadless is edgeSourceBinding for a headless "api",
// mirroring sentHeadless under the edge tests' binding names.
// addClientBinding saves an active "client" binding with a headless builder,
// so Send starts it a process on the runtime's Runner. live is false to make
// the target unsendable (a broken binding), the "target is gone" case.
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
// TestEdgeSkippedWhenArtifactMissing pins that a missing artifact skips the
// edge rather than leaving it pending forever: at close, a round's artifacts
// are final.
// TestEdgeFireSendsToTarget pins the fire path (#37): evaluateEdges arms the
// edge (Result "firing") under the source's own lock at close, and running
// the armed pending -- what the Daemon does after Save -- actually Sends the
// prompt to the target and settles the edge Fired with "sent round N".
//
// Mutation check (run and report): make runFires a no-op and this fails on
// f.prompts and the edge's final Result.
// TestEdgeFireFailureIsQueued pins the fallback (#37 §6): a fire that cannot
// reach its target never blocks the source, and is downgraded to the same
// queue-mode payload evaluateEdges would have queued, with the failure
// named.
// TestEdgeFiresOnce pins that an edge fires exactly once, on its own round's
// close: a second close of a later round neither re-evaluates it nor leaves
// it untouched by mistake -- an edge declared for that later round fires
// then.
// TestArmedEdgeSurvivesRestart pins the crash-recovery contract (#37 §4): an
// edge a daemon armed (Result "firing", Fired false) but never got to run --
// the daemon died between the round's Save and runFires -- is picked back up
// on the next Tick, because armedFires scans every binding's saved state,
// not just what a given tick's Reconcile touched.
// TestEdgeHeadlessClosePath mirrors TestEdgeQueueOnClose for a headless
// source binding: reconcileHeadless's close path evaluates edges exactly as
// the pane path does.
