package main

import (
	"testing"
)

// TestReapFlagsReachTheCommand pins a flag shape that reads fine and silently
// does the opposite of what it says. `all := *fs.Bool(...)` dereferences the
// pointer BEFORE parseFlags runs, so the value is the default forever and what
// the user typed is discarded.
//
// The consequence is destructive, not cosmetic: README documents
// `relay reap [NAME] [--dry-run]` as the listing that "changes nothing", and a
// dry run that never arrives closes every terminal consult's pane for real.
func TestReapFlagsReachTheCommand(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantAll    bool
		wantDryRun bool
		wantName   string
	}{
		{name: "dry-run alone", args: []string{"--dry-run"}, wantDryRun: true},
		{name: "all alone", args: []string{"--all"}, wantAll: true},
		{name: "both", args: []string{"--all", "--dry-run"}, wantAll: true, wantDryRun: true},
		{name: "neither", args: nil},
		{name: "name with dry-run", args: []string{"--name", "webshop", "--dry-run"}, wantDryRun: true, wantName: "webshop"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := parseReapFlags(tc.args)
			if err != nil {
				t.Fatalf("parseReapFlags(%q): %v", tc.args, err)
			}
			if got.all != tc.wantAll {
				t.Errorf("--all = %v, want %v (the flag never reached Reap)", got.all, tc.wantAll)
			}
			if got.dryRun != tc.wantDryRun {
				t.Errorf("--dry-run = %v, want %v (a dry run that closes panes for real)", got.dryRun, tc.wantDryRun)
			}
			if got.name != tc.wantName {
				t.Errorf("--name = %q, want %q", got.name, tc.wantName)
			}
		})
	}
}

// TestAskFallsBackToThePlannersWorkspace pins the one pane-spawning command
// that ignored HERDR_WORKSPACE_ID. bind, fork and add all read it; ask used
// only its --workspace flag, so `relay ask --new-tab` opened the consult in
// herdr's default workspace instead of the planner's -- somewhere the human is
// not looking, and somewhere `relay status` will misreport.
func TestAskFallsBackToThePlannersWorkspace(t *testing.T) {
	t.Setenv("HERDR_WORKSPACE_ID", "ws-planner")

	if got := workspaceOrEnv(""); got != "ws-planner" {
		t.Errorf("workspace with no flag = %q, want the planner's %q", got, "ws-planner")
	}
	if got := workspaceOrEnv("ws-explicit"); got != "ws-explicit" {
		t.Errorf("workspace = %q, want the explicit flag %q to win", got, "ws-explicit")
	}
}

// TestAskWorkspaceStaysEmptyWithoutEnv keeps the fallback from inventing a
// workspace: CreateTab omits --workspace entirely when the id is empty, and
// that is the correct behaviour outside a herdr pane.
func TestAskWorkspaceStaysEmptyWithoutEnv(t *testing.T) {
	t.Setenv("HERDR_WORKSPACE_ID", "")

	if got := workspaceOrEnv(""); got != "" {
		t.Errorf("workspace = %q, want empty when neither flag nor env is set", got)
	}
}
