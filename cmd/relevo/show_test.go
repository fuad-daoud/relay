package main

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// TestShowSectionFlagsConflict pins `relevo show`'s section-flag rules:
// none given defaults to plan, exactly one wins, more than one is a usage
// error. showSectionFlags is a pure function, so this never executes the
// subcommand -- CI launches no harness.
func TestShowSectionFlagsConflict(t *testing.T) {
	section, err := showSectionFlags(false, false, false, false, false, false)
	if err != nil {
		t.Fatalf("no flags: err = %v, want nil", err)
	}
	if section != relevo.ShowPlan {
		t.Errorf("no flags: section = %q, want %q (default)", section, relevo.ShowPlan)
	}

	section, err = showSectionFlags(false, true, false, false, false, false)
	if err != nil {
		t.Fatalf("--report: err = %v, want nil", err)
	}
	if section != relevo.ShowReport {
		t.Errorf("--report: section = %q, want %q", section, relevo.ShowReport)
	}

	if _, err := showSectionFlags(true, true, false, false, false, false); err == nil {
		t.Error("--plan --report: err = nil, want a usage error (more than one section)")
	}
}
