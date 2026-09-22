package relay

import (
	"errors"
	"strings"
	"testing"
)

// reconcileOnce runs the FULL Reconcile, not reconcileConsults. The difference
// is the point: tickConsults calls the inner function directly and so cannot
// see any gate Reconcile puts in front of it.
// TestConsultOnADoneBindingIsStillFinished pins the gap between `relay done`
// and a consult that is still working. Reconcile would otherwise return on
// StateDone before it ever reached the consults, leaving the record
// ConsultRunning and its findings undelivered. Consults reconcile first, so a
// DONE binding still delivers them.
// TestStrandKeepsTheCauseWhenRecordingAlsoFails pins an error path that
// discarded the only useful half. When the process started but recording the
// consult failed, strand records the cause -- and if that record's Save ALSO
// failed, the save error was returned and the spawn failure, the half that
// explains what actually went wrong, was thrown away.
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
