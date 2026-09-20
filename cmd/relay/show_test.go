package main

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/relay"
)

// TestShowSectionFlagsConflict pins `relay show`'s section-flag rules:
// none given defaults to plan, exactly one wins, more than one is a usage
// error. showSectionFlags is a pure function, so this never executes the
// subcommand -- CI has no herdr.
func TestShowSectionFlagsConflict(t *testing.T) {
	section, err := showSectionFlags(false, false, false, false, false, false)
	if err != nil {
		t.Fatalf("no flags: err = %v, want nil", err)
	}
	if section != relay.ShowPlan {
		t.Errorf("no flags: section = %q, want %q (default)", section, relay.ShowPlan)
	}

	section, err = showSectionFlags(false, true, false, false, false, false)
	if err != nil {
		t.Fatalf("--report: err = %v, want nil", err)
	}
	if section != relay.ShowReport {
		t.Errorf("--report: section = %q, want %q", section, relay.ShowReport)
	}

	if _, err := showSectionFlags(true, true, false, false, false, false); err == nil {
		t.Error("--plan --report: err = nil, want a usage error (more than one section)")
	}
}
