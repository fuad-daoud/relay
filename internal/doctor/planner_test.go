package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/planner"
)

func writeDoctorFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestDoctorPluginRow is the plan's required case for §4.8's first row: the
// relevo plugin enabled in the user's or the project's settings reads OK, an
// existing claude candidate with neither reads FAIL, and no claude candidate
// at all leaves the row out entirely.
func TestDoctorPluginRow(t *testing.T) {
	enabled := `{"enabledPlugins":{"relevo@relevo":true}}`

	t.Run("user settings enable it", func(t *testing.T) {
		home := t.TempDir()
		writeDoctorFile(t, filepath.Join(home, ".claude", "settings.json"), enabled)
		checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin")
		if c == nil {
			t.Fatal("the plugin row must be present when a claude candidate exists")
		}
		if c.Severity != SevOK {
			t.Errorf("plugin row = %v (%s), want ok", c.Severity, c.Detail)
		}
	})

	t.Run("project settings enable it", func(t *testing.T) {
		repo := t.TempDir()
		writeDoctorFile(t, filepath.Join(repo, ".claude", "settings.json"), enabled)
		checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: t.TempDir(), Repo: repo})
		c := findCheck(Report{Checks: checks}, "", "plugin")
		if c == nil || c.Severity != SevOK {
			t.Fatalf("plugin row = %+v, want ok from the project's settings", c)
		}
	})

	t.Run("neither settings file enables it", func(t *testing.T) {
		checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: t.TempDir(), Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin")
		if c == nil {
			t.Fatal("the plugin row must be present when a claude candidate exists")
		}
		if c.Severity != SevFail {
			t.Errorf("plugin row = %v (%s), want FAIL", c.Severity, c.Detail)
		}
		if c.Fix == "" {
			t.Error("the failing plugin row must carry the fix")
		}
	})

	t.Run("disabled is not enabled", func(t *testing.T) {
		home := t.TempDir()
		writeDoctorFile(t, filepath.Join(home, ".claude", "settings.json"), `{"enabledPlugins":{"relevo@relevo":false}}`)
		checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		if c := findCheck(Report{Checks: checks}, "", "plugin"); c == nil || c.Severity != SevFail {
			t.Fatalf("plugin row = %+v, want FAIL for relevo@relevo: false", c)
		}
	})

	t.Run("no claude candidate", func(t *testing.T) {
		checks := PlannerChecks(PlannerCheckInput{Claude: false, Home: t.TempDir(), Repo: t.TempDir()})
		if c := findCheck(Report{Checks: checks}, "", "plugin"); c != nil {
			t.Errorf("no claude candidate must leave the plugin row out, got %+v", c)
		}
		if c := findCheck(Report{Checks: checks}, "", "plugin hook"); c != nil {
			t.Errorf("no claude candidate must leave the plugin hook row out, got %+v", c)
		}
	})
}

// TestDoctorPlannerRow is the plan's required case for §4.8's third row: it
// exists only inside a Claude Code session, and fails on a resolve miss or a
// session with no relevo mcp child process.
func TestDoctorPlannerRow(t *testing.T) {
	rec := &planner.Record{ID: "pl_aaaaaaaabbbb", Name: "architect-1", HarnessKind: "claude", SessionID: "sess"}

	t.Run("resolved with a live claim", func(t *testing.T) {
		checks := PlannerChecks(PlannerCheckInput{Detected: true, Resolved: rec, MCPChild: true, ClaimLive: true})
		c := findCheck(Report{Checks: checks}, "", "planner")
		if c == nil || c.Severity != SevOK {
			t.Fatalf("planner row = %+v, want ok", c)
		}
	})

	t.Run("resolve missed", func(t *testing.T) {
		checks := PlannerChecks(PlannerCheckInput{Detected: true})
		c := findCheck(Report{Checks: checks}, "", "planner")
		if c == nil || c.Severity != SevFail {
			t.Fatalf("planner row = %+v, want FAIL when Resolve missed", c)
		}
	})

	t.Run("no live claim", func(t *testing.T) {
		checks := PlannerChecks(PlannerCheckInput{Detected: true, Resolved: rec, MCPChild: true})
		c := findCheck(Report{Checks: checks}, "", "planner")
		if c == nil || c.Severity != SevInfo {
			t.Fatalf("planner row = %+v, want INFO without a live claim (tools mode is not a fault)", c)
		}
	})

	t.Run("not inside Claude Code", func(t *testing.T) {
		checks := PlannerChecks(PlannerCheckInput{Detected: false, Resolved: rec, ClaimLive: true})
		if c := findCheck(Report{Checks: checks}, "", "planner"); c != nil {
			t.Errorf("the planner row exists only when Detect says claude, got %+v", c)
		}
	})

	t.Run("stale records are an info row", func(t *testing.T) {
		checks := PlannerChecks(PlannerCheckInput{Stale: []string{"old-1", "old-2"}})
		c := findCheck(Report{Checks: checks}, "", "planners")
		if c == nil || c.Severity != SevInfo {
			t.Fatalf("stale row = %+v, want an info row", c)
		}
		if c.Severity == SevFail || c.Severity == SevWarn {
			t.Error("the stale row must not fail or warn")
		}
	})

	t.Run("no stale records means no row", func(t *testing.T) {
		checks := PlannerChecks(PlannerCheckInput{})
		if c := findCheck(Report{Checks: checks}, "", "planners"); c != nil {
			t.Errorf("no stale records must leave the row out, got %+v", c)
		}
	})
}

// TestDoctorPluginHookRow pins the "not checked, never FAIL" rule: an install
// relevo cannot find is a fact relevo could not establish, not a broken one.
func TestDoctorPluginHookRow(t *testing.T) {
	t.Run("no installed_plugins.json", func(t *testing.T) {
		checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: t.TempDir(), Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin hook")
		if c == nil || c.Severity != SevOK {
			t.Fatalf("plugin hook row = %+v, want ok (not checked)", c)
		}
	})

	t.Run("hook present", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "plugins", "cache", "relevo", "relevo", "0.1.0")
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			`{"plugins":{"relevo@relevo":[{"installPath":"`+dir+`"}]}}`)
		writeDoctorFile(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"relevo planner init --hook claude"}]}]}}`)
		checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin hook")
		if c == nil || c.Severity != SevOK {
			t.Fatalf("plugin hook row = %+v, want ok", c)
		}
	})

	t.Run("hook missing", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "plugins", "cache", "relevo", "relevo", "0.1.0")
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			`{"plugins":{"relevo@relevo":[{"installPath":"`+dir+`"}]}}`)
		writeDoctorFile(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"relevo doctor"}]}]}}`)
		checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin hook")
		if c == nil || c.Severity != SevFail {
			t.Fatalf("plugin hook row = %+v, want FAIL", c)
		}
	})
}

// TestDoctorPlannerRowNoClaimIsInfo pins D6's revision: a Claude Code planner
// with no live channel claim is INFO, never FAIL -- tools mode gets reports by
// background wait -- and the row carries the spec's push-upgrade text.
func TestDoctorPlannerRowNoClaimIsInfo(t *testing.T) {
	rec := &planner.Record{ID: "pl_aaaaaaaabbbb", Name: "architect-1", HarnessKind: "claude", SessionID: "sess"}
	checks := PlannerChecks(PlannerCheckInput{Detected: true, Resolved: rec, MCPChild: true})

	c := findCheck(Report{Checks: checks}, "", "planner")
	if c == nil {
		t.Fatal("the planner row is missing")
	}
	if c.Severity != SevInfo {
		t.Fatalf("planner row = %v (%s), want INFO", c.Severity, c.Detail)
	}
	for _, want := range []string{"background wait", "dangerously-load-development-channels plugin:relevo@relevo", "allowedChannelPlugins"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("planner row detail %q must name %q", c.Detail, want)
		}
	}
}

// TestDoctorPlannerRowNoMCPChildFails pins §4.8: from a Claude Code session,
// no relevo mcp process among the planner host's children is a FAIL, because
// nothing would ever reach the planner.
func TestDoctorPlannerRowNoMCPChildFails(t *testing.T) {
	rec := &planner.Record{ID: "pl_aaaaaaaabbbb", Name: "architect-1", HarnessKind: "claude", SessionID: "sess"}
	checks := PlannerChecks(PlannerCheckInput{Detected: true, Resolved: rec, ClaimLive: true})

	c := findCheck(Report{Checks: checks}, "", "planner")
	if c == nil {
		t.Fatal("the planner row is missing")
	}
	if c.Severity != SevFail {
		t.Fatalf("planner row = %v (%s), want FAIL with no relevo mcp child", c.Severity, c.Detail)
	}
	if c.Fix == "" {
		t.Error("the failing planner row must carry the fix")
	}
}

// TestHasMCPChild is the pure rule's own test: the process table is injected,
// so no live process tree is needed.
func TestHasMCPChild(t *testing.T) {
	cases := []struct {
		name     string
		children []ChildProcess
		want     bool
	}{
		{"relevo mcp child", []ChildProcess{{PID: 2, Args: []string{"/usr/local/bin/relevo", "mcp"}}}, true},
		{"relevo mcp with flags", []ChildProcess{{PID: 2, Args: []string{"relevo", "--planner", "x", "mcp"}}}, true},
		{"another binary", []ChildProcess{{PID: 2, Args: []string{"relevo-wrapper", "mcp"}}}, false},
		{"relevo another verb", []ChildProcess{{PID: 2, Args: []string{"relevo", "status"}}}, false},
		{"no children", nil, false},
	}
	for _, tc := range cases {
		if got := HasMCPChild(tc.children); got != tc.want {
			t.Errorf("%s: HasMCPChild = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestDoctorPluginVersionRow is the plan's required case for the third plugin
// row: the installed plugin's version is compared with the running binary's,
// advisory only. Every situation relevo cannot prove -- no running version, no
// readable file, no relevo entry, an unparseable version -- reads "not checked"
// (OK), never a warning.
func TestDoctorPluginVersionRow(t *testing.T) {
	state := func(version string) string {
		return `{"version":2,"plugins":{"relevo@relevo":[{"version":"` + version + `","installPath":"/tmp/relevo/0.8.0"}]}}`
	}

	cases := []struct {
		name    string
		body    string // ~/.claude/plugins/installed_plugins.json; "" writes nothing
		running string
		wantSev Severity
		want    string
	}{
		{"no running version", state("0.8.0"), "", SevOK, "not checked"},
		{"missing file", "", "v0.8.0", SevOK, "not checked (no readable ~/" + claudePluginStateRel + ")"},
		{"not json", "not json", "v0.8.0", SevOK, "not checked (no readable ~/" + claudePluginStateRel + ")"},
		{"no relevo entry", `{"version":2,"plugins":{"other@relevo":[{"version":"0.8.0"}]}}`, "v0.8.0", SevOK, "not checked (relevo plugin not installed)"},
		{"dev build matches the release", state("0.8.0"), "v0.8.0-15-gd664545", SevOK, "plugin 0.8.0 matches relevo"},
		{"exact match", state("0.8.0"), "v0.8.0", SevOK, "plugin 0.8.0 matches relevo"},
		{"older plugin warns", state("0.7.0"), "v0.8.0", SevWarn, "plugin 0.7.0, relevo v0.8.0"},
		{"running is (devel)", state("0.8.0"), "(devel)", SevOK, "not checked (relevo is (devel))"},
		{"plugin version does not parse", state("garbage"), "v0.8.0", SevOK, "not checked (relevo is v0.8.0)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.body != "" {
				writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), tc.body)
			}
			checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: home, Repo: t.TempDir(), Running: tc.running})
			c := findCheck(Report{Checks: checks}, "", "plugin version")
			if c == nil {
				t.Fatal("the plugin version row must be present when a claude candidate exists")
			}
			if c.Severity != tc.wantSev {
				t.Errorf("severity = %v (%s), want %v", c.Severity, c.Detail, tc.wantSev)
			}
			if c.Detail != tc.want {
				t.Errorf("detail = %q, want %q", c.Detail, tc.want)
			}
			if tc.wantSev == SevWarn && c.Fix != "claude plugin update relevo@relevo" {
				t.Errorf("fix = %q, want the update command", c.Fix)
			}
		})
	}

	t.Run("no claude candidate means no row", func(t *testing.T) {
		home := t.TempDir()
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), state("0.8.0"))
		checks := PlannerChecks(PlannerCheckInput{Claude: false, Home: home, Repo: t.TempDir(), Running: "v0.8.0"})
		if c := findCheck(Report{Checks: checks}, "", "plugin version"); c != nil {
			t.Errorf("no claude candidate must leave the plugin version row out, got %+v", c)
		}
	})
}
