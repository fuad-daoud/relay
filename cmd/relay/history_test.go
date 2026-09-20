package main

import "testing"

// TestHistoryUsageFlagsConflict pins the two usage errors `relay history`
// rejects before ever touching the database: --archived with --live
// together, and an --outcome outside db's enum. validateHistoryFlags is a
// pure function, so this never executes the subcommand -- CI has no herdr.
func TestHistoryUsageFlagsConflict(t *testing.T) {
	if err := validateHistoryFlags(false, false, ""); err != nil {
		t.Errorf("validateHistoryFlags(false, false, \"\") = %v, want nil", err)
	}
	if err := validateHistoryFlags(true, false, "reported"); err != nil {
		t.Errorf("validateHistoryFlags(true, false, \"reported\") = %v, want nil", err)
	}
	if err := validateHistoryFlags(true, true, ""); err == nil {
		t.Error("validateHistoryFlags(true, true, \"\") = nil, want an error (--archived and --live conflict)")
	}
	if err := validateHistoryFlags(false, false, "not-a-real-outcome"); err == nil {
		t.Error(`validateHistoryFlags(false, false, "not-a-real-outcome") = nil, want an error`)
	}
}
