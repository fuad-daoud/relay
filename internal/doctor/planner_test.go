package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relay/internal/planner"
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
// relay plugin enabled in the user's or the project's settings reads OK, an
// existing claude candidate with neither reads FAIL, and no claude candidate
// at all leaves the row out entirely.
func TestDoctorPluginRow(t *testing.T) {
	enabled := `{"enabledPlugins":{"relay@relay":true}}`

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
		writeDoctorFile(t, filepath.Join(home, ".claude", "settings.json"), `{"enabledPlugins":{"relay@relay":false}}`)
		checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		if c := findCheck(Report{Checks: checks}, "", "plugin"); c == nil || c.Severity != SevFail {
			t.Fatalf("plugin row = %+v, want FAIL for relay@relay: false", c)
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
// resolved planner with no live channel claim.
func TestDoctorPlannerRow(t *testing.T) {
	rec := &planner.Record{ID: "pl_aaaaaaaabbbb", Name: "architect-1", HarnessKind: "claude", SessionID: "sess"}

	t.Run("resolved with a live claim", func(t *testing.T) {
		checks := PlannerChecks(PlannerCheckInput{Detected: true, Resolved: rec, ClaimLive: true})
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
		checks := PlannerChecks(PlannerCheckInput{Detected: true, Resolved: rec})
		c := findCheck(Report{Checks: checks}, "", "planner")
		if c == nil || c.Severity != SevFail {
			t.Fatalf("planner row = %+v, want FAIL without a live claim", c)
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
// relay cannot find is a fact relay could not establish, not a broken one.
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
		dir := filepath.Join(home, ".claude", "plugins", "cache", "relay", "relay", "0.1.0")
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			`{"plugins":{"relay@relay":[{"installPath":"`+dir+`"}]}}`)
		writeDoctorFile(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"relay planner init --hook claude"}]}]}}`)
		checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin hook")
		if c == nil || c.Severity != SevOK {
			t.Fatalf("plugin hook row = %+v, want ok", c)
		}
	})

	t.Run("hook missing", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "plugins", "cache", "relay", "relay", "0.1.0")
		writeDoctorFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			`{"plugins":{"relay@relay":[{"installPath":"`+dir+`"}]}}`)
		writeDoctorFile(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"relay doctor"}]}]}}`)
		checks := PlannerChecks(PlannerCheckInput{Claude: true, Home: home, Repo: t.TempDir()})
		c := findCheck(Report{Checks: checks}, "", "plugin hook")
		if c == nil || c.Severity != SevFail {
			t.Fatalf("plugin hook row = %+v, want FAIL", c)
		}
	})
}
