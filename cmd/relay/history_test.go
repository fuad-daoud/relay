package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/histq"
)

// TestHistoryUsageFlagsConflict pins the two usage errors `relay history`
// rejects before ever touching the database: --archived with --live
// together, and an --outcome outside db's enum. validateHistoryFlags is a
// pure function, so this never executes the subcommand -- CI launches no harness.
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

// TestValidateHistoryBy pins that --by accepts exactly the ten histq axes
// and that its error lists them all.
func TestValidateHistoryBy(t *testing.T) {
	if err := validateHistoryBy(""); err != nil {
		t.Errorf(`validateHistoryBy("") = %v, want nil`, err)
	}
	for _, a := range histq.Axes() {
		if err := validateHistoryBy(string(a)); err != nil {
			t.Errorf("validateHistoryBy(%q) = %v, want nil", a, err)
		}
	}
	for _, bad := range []string{"nope", "Builders"} {
		err := validateHistoryBy(bad)
		if err == nil {
			t.Errorf("validateHistoryBy(%q) = nil, want an error", bad)
			continue
		}
		for _, a := range histq.Axes() {
			if !strings.Contains(err.Error(), string(a)) {
				t.Errorf("validateHistoryBy(%q) error %q does not list axis %q", bad, err.Error(), a)
			}
		}
	}
}

// TestGroupJSONShape pins the --json --by shape: the groups, with each
// group's Rows present only when --rows asked for them, and [] when empty.
func TestGroupJSONShape(t *testing.T) {
	groups := []histq.GroupRow{{
		Key:     "api",
		Rounds:  2,
		Commits: 3,
		Tokens:  1000,
		CostUSD: 1.25,
		Unknown: 1,
		Last:    time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		Rows:    []db.RoundRow{{BindingID: "b1", BindingName: "api"}},
	}}

	with, err := json.Marshal(groupJSON(groups, true))
	if err != nil {
		t.Fatalf("Marshal(with rows): %v", err)
	}
	if !strings.Contains(string(with), `"BindingName":"api"`) {
		t.Errorf("with --rows JSON = %s, want the group's Rows included", with)
	}

	without, err := json.Marshal(groupJSON(groups, false))
	if err != nil {
		t.Fatalf("Marshal(without rows): %v", err)
	}
	if strings.Contains(string(without), `"BindingName":"api"`) {
		t.Errorf("without --rows JSON = %s, want the group's Rows blank", without)
	}
	if !strings.Contains(string(without), `"Key":"api"`) {
		t.Errorf("without --rows JSON = %s, want the group itself kept", without)
	}

	empty, err := json.Marshal(groupJSON(nil, false))
	if err != nil {
		t.Fatalf("Marshal(nil): %v", err)
	}
	if string(empty) != "[]" {
		t.Errorf("Marshal(nil groups) = %s, want []", empty)
	}
}
