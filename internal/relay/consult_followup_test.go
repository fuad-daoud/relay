package relay

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// setConsultStatus moves the seeded consult's pane to status, leaving the
// planner and builder agents alone.
func setConsultStatus(f *fakeHerdr, status string) {
	for i := range f.agents {
		if f.agents[i].PaneID == "w2:p9" {
			f.agents[i].Status = status
		}
	}
}

// reconcileOnce runs the FULL Reconcile, not reconcileConsults. The difference
// is the point: tickConsults calls the inner function directly and so cannot
// see any gate Reconcile puts in front of it.
func reconcileOnce(t *testing.T, rt Runtime, f *fakeHerdr, state store.State) store.Binding {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		b.State = state
		out, err = Reconcile(context.Background(), rt, tx, b, f.agents)
		if err != nil {
			return err
		}
		return tx.Save(out)
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return out
}

// TestConsultOnADoneBindingIsStillFinished pins the gap between `relay done`
// and a consult that is still working. Reconcile returned on StateDone before
// it ever reached the consults, so the record stayed ConsultRunning; Reap
// skips running consults, so the pane could never be closed, and gc then
// deleted the binding and the reap worklist with it -- leaving a live pane
// (~800 MB) with nothing in relay pointing at it.
func TestConsultOnADoneBindingIsStillFinished(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, c := seedConsult(t, f)
	setConsultStatus(f, herdr.StatusIdle)

	if err := os.WriteFile(c.FindingsPath, []byte("looks fine"), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}

	got := reconcileOnce(t, rt, f, store.StateDone)

	if len(got.Consults) != 1 {
		t.Fatalf("got %d consults, want 1", len(got.Consults))
	}
	if got.Consults[0].State == store.ConsultRunning {
		t.Error("a consult on a DONE binding is never advanced: it stays running, " +
			"reap refuses to close it, and gc deletes the worklist out from under its pane")
	}
	if got.Consults[0].State != store.ConsultDone {
		t.Errorf("consult wrote findings but landed in %q, want %q",
			got.Consults[0].State, store.ConsultDone)
	}
}

// TestIdleConsultWhoseNudgeNeverLandsStillTimesOut pins the one path out of
// reconcileConsults that reached no deadline. Every branch of the `idle` block
// ended in continue, so the consultTimeout check below it was reachable only
// for a consult that was NOT idle. A consult that went idle and whose nudge
// kept failing therefore sat at ConsultRunning forever, holding a ConsultCap
// slot -- exactly the "record that never becomes terminal" the consultTimeout
// doc comment claims to prevent.
func TestIdleConsultWhoseNudgeNeverLandsStillTimesOut(t *testing.T) {
	f := &fakeHerdr{}
	rt, clock, _ := seedConsult(t, f)
	setConsultStatus(f, herdr.StatusIdle)
	f.promptErr = errors.New("herdr is down")

	tickConsults(t, rt, f) // nudge attempted, fails

	clock.Advance(consultTimeout + time.Minute)
	b := tickConsults(t, rt, f)

	if b.Consults[0].State == store.ConsultRunning {
		t.Errorf("consult %s is still running %s after it was spawned with a nudge that never landed; "+
			"it can never be reaped and holds a cap slot forever",
			b.Consults[0].ID, consultTimeout+time.Minute)
	}
}

// TestFailedNudgeIsNotRecordedAsDelivered guards the fix's blast radius: a
// nudge that errored must not stamp NudgedAt, or the grace window would start
// counting from a nudge the consult never received.
func TestFailedNudgeIsNotRecordedAsDelivered(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, _ := seedConsult(t, f)
	setConsultStatus(f, herdr.StatusIdle)
	f.promptErr = errors.New("herdr is down")

	b := tickConsults(t, rt, f)

	if !b.Consults[0].NudgedAt.IsZero() {
		t.Error("a nudge that failed to send stamped NudgedAt; the grace window would run against a nudge nobody got")
	}
}

// TestStrandKeepsTheCauseWhenRecordingAlsoFails pins an error path that
// discarded the only useful half. When the pane spawned but StartAgent failed,
// strand records the consult as silent -- and if that record's Save ALSO
// failed, the save error was returned and the StartAgent failure, the half
// that explains what actually went wrong, was thrown away.
func TestStrandKeepsTheCauseWhenRecordingAlsoFails(t *testing.T) {
	cause := errors.New(`start consult "webshop-reviewer-7f2a": herdr refused`)
	saveErr := errors.New("state dir is read-only")

	got := strandError(cause, saveErr)

	if !errors.Is(got, cause) {
		t.Errorf("strandError lost the cause: %v", got)
	}
	if !strings.Contains(got.Error(), "read-only") {
		t.Errorf("strandError lost the save failure: %v", got)
	}
}

// TestStrandReturnsTheBareCauseWhenRecordingSucceeds keeps the common path
// unwrapped: callers match on the spawn failure and the successful record adds
// nothing worth saying.
func TestStrandReturnsTheBareCauseWhenRecordingSucceeds(t *testing.T) {
	cause := errors.New("herdr refused")

	if got := strandError(cause, nil); got != cause {
		t.Errorf("strandError(cause, nil) = %v, want the cause unchanged", got)
	}
}
