package relay

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func builderAgent(status string) herdr.Agent {
	return herdr.Agent{
		Kind: "agy", Status: status, CWD: "/repo", PaneID: "w2:p4",
		Title: "upjo-builder",
	}
}

// sentBinding puts a binding one Send into round 1, with a working builder.
func sentBinding(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	rt, _ := seedBound(t, f)
	if _, err := Send(context.Background(), rt, "upjo", writePlan(t, "do it")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("upjo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f.prompts = nil
	return rt, b
}

// reconcile wraps Reconcile with the state lock a daemon would hold across
// the whole read-reconcile-write, since Reconcile itself takes a *store.Tx
// rather than locking on its own.
func reconcile(t *testing.T, rt Runtime, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = Reconcile(context.Background(), rt, tx, b, agents)
		return err
	})
	return out, err
}

func TestReconcileQueuesReportWhenBuilderIdleAndFileExists(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("upjo", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 after a report", got.Round)
	}

	pending, found, err := rt.Store.PendingForPlanner("upjo")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, rt.Store.ReportPath("upjo", 1)) {
		t.Errorf("payload must name the report path, got %q", pending.Payload)
	}
}

func TestReconcileNudgesOnceWhenReportFileMissing(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Errorf("a nudge must not advance the round, got %d", got.Round)
	}
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0].Text, rt.Store.ReportPath("upjo", 1)) {
		t.Fatalf("expected one nudge naming the report path, got %+v", f.prompts)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("upjo"); pending {
		t.Error("a nudge must not queue anything for the planner")
	}
}

func TestReconcileScrapesAfterNudgeFails(t *testing.T) {
	f := &fakeHerdr{readOut: "I implemented the guard clause but could not write the file."}
	rt, b := sentBinding(t, f)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	got, err := reconcile(t, rt, b, agents) // scrape
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 after the scrape fallback", got.Round)
	}

	pending, found, err := rt.Store.PendingForPlanner("upjo")
	if err != nil || !found {
		t.Fatalf("scrape must be queued: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, "SCRAPED") {
		t.Errorf("a scraped report must be labelled unreliable, got %q", pending.Payload)
	}

	body, err := os.ReadFile(rt.Store.ReportPath("upjo", 1))
	if err != nil {
		t.Fatalf("scrape must be written to the report path: %v", err)
	}
	if !strings.Contains(string(body), "guard clause") {
		t.Errorf("scrape body = %q", body)
	}
}

func TestReconcileMarksBrokenWhenBuilderGone(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerWith(herdr.StatusIdle, false)})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateBroken {
		t.Errorf("state = %s, want broken", got.State)
	}
}

func TestReconcileIgnoresWorkingBuilder(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusWorking)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 || len(f.prompts) != 0 {
		t.Errorf("a working builder must be left alone: round=%d prompts=%+v", got.Round, f.prompts)
	}
}

func TestReconcileNeverTreatsUnknownAsDone(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("upjo", 1), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusUnknown)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Error("herdr documents that unknown does not prove completion; the round must not advance")
	}
	if _, pending, _ := rt.Store.PendingForPlanner("upjo"); pending {
		t.Error("nothing may be queued off an unknown status")
	}
}
