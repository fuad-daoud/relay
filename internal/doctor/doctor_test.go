package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
)

type fakeEnv struct {
	herdrVer      string
	herdrErr      error
	intStatus     map[string]herdr.IntegrationState
	intErr        error
	daemonRunning bool
	daemonErr     error
	lookPaths     map[string]string // binary -> path
	existingFiles map[string]bool   // path -> exists
	homeDir       string
}

func (f *fakeEnv) HerdrVersion(ctx context.Context) (string, error) {
	if f.herdrErr != nil {
		return "", f.herdrErr
	}
	return f.herdrVer, nil
}

func (f *fakeEnv) IntegrationStatus(ctx context.Context) (map[string]herdr.IntegrationState, error) {
	if f.intErr != nil {
		return nil, f.intErr
	}
	return f.intStatus, nil
}

func (f *fakeEnv) DaemonRunning(ctx context.Context) (bool, error) {
	if f.daemonErr != nil {
		return false, f.daemonErr
	}
	return f.daemonRunning, nil
}

func (f *fakeEnv) LookPath(binary string) (string, error) {
	if path, ok := f.lookPaths[binary]; ok {
		return path, nil
	}
	return "", errors.New("executable file not found in $PATH")
}

func (f *fakeEnv) HomePath(rel string) (string, error) {
	home := f.homeDir
	if home == "" {
		home = "/fake/home"
	}
	return filepath.Join(home, rel), nil
}

func (f *fakeEnv) Stat(path string) error {
	if f.existingFiles != nil && f.existingFiles[path] {
		return nil
	}
	return os.ErrNotExist
}

func findCheck(report Report, group, name string) *Check {
	for i := range report.Checks {
		if report.Checks[i].Group == group && report.Checks[i].Name == name {
			return &report.Checks[i]
		}
	}
	return nil
}

func countChecksForGroup(report Report, group string) int {
	count := 0
	for _, c := range report.Checks {
		if c.Group == group {
			count++
		}
	}
	return count
}

func TestDoctorHerdrFloorChecks(t *testing.T) {
	tests := []struct {
		name         string
		ver          string
		err          error
		wantSeverity Severity
		wantFail     bool
	}{
		{
			name:         "herdr absent",
			err:          errors.New("herdr: executable file not found in $PATH"),
			wantSeverity: SevFail,
			wantFail:     true,
		},
		{
			name:         "herdr unparseable",
			ver:          "not-a-semver",
			wantSeverity: SevFail,
			wantFail:     true,
		},
		{
			name:         "herdr below floor",
			ver:          "0.8.1",
			wantSeverity: SevFail,
			wantFail:     true,
		},
		{
			name:         "herdr exactly at floor",
			ver:          "0.8.2",
			wantSeverity: SevOK,
			wantFail:     false,
		},
		{
			name:         "herdr above floor",
			ver:          "0.9.0",
			wantSeverity: SevOK,
			wantFail:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := &fakeEnv{
				herdrVer: tc.ver,
				herdrErr: tc.err,
			}
			report := Run(context.Background(), env, nil)
			c := findCheck(report, "", "herdr")
			if c == nil {
				t.Fatal("herdr check not found in report")
			}
			if c.Severity != tc.wantSeverity {
				t.Errorf("herdr severity = %v, want %v (detail: %q)", c.Severity, tc.wantSeverity, c.Detail)
			}
			if tc.wantFail && report.Failures() == 0 {
				t.Errorf("expected failures > 0, got 0")
			}
		})
	}
}

func TestDoctorIntegrationStatusErrorDegradesToWarning(t *testing.T) {
	env := &fakeEnv{
		herdrVer: "0.9.0",
		intErr:   errors.New("connection timed out"),
		lookPaths: map[string]string{
			"claude": "/usr/bin/claude",
		},
	}

	report := Run(context.Background(), env, []string{"claude"})
	c := findCheck(report, "claude", "integration")
	if c == nil {
		t.Fatal("claude integration check not found")
	}
	if c.Severity != SevWarn {
		t.Errorf("integration severity = %v, want SevWarn on error", c.Severity)
	}
}

func TestDoctorMissingBinarySuppressesRemainingRows(t *testing.T) {
	env := &fakeEnv{
		herdrVer:  "0.9.0",
		lookPaths: map[string]string{}, // nothing on PATH
	}

	report := Run(context.Background(), env, []string{"opencode"})
	count := countChecksForGroup(report, "opencode")
	if count != 1 {
		t.Errorf("opencode should have exactly 1 check (binary), got %d", count)
	}
	binCheck := findCheck(report, "opencode", "binary")
	if binCheck == nil {
		t.Fatal("opencode binary check missing")
	}
	if binCheck.Severity != SevWarn {
		t.Errorf("opencode binary check severity = %v, want SevWarn", binCheck.Severity)
	}
	if binCheck.Detail != "not on PATH -- skipping the rest of this harness" {
		t.Errorf("opencode binary detail = %q, want 'not on PATH -- skipping the rest of this harness'", binCheck.Detail)
	}
}

func TestDoctorAgyRoleSelectedByPreamble(t *testing.T) {
	env := &fakeEnv{
		herdrVer: "0.9.0",
		lookPaths: map[string]string{
			"agy": "/home/fuad/.local/bin/agy",
		},
		intStatus: map[string]herdr.IntegrationState{
			"antigravity-cli": {Installed: true, Detail: "current (v3)"},
		},
	}

	report := Run(context.Background(), env, []string{"agy"})
	roleCheck := findCheck(report, "agy", "plan-executor")
	if roleCheck == nil {
		t.Fatal("agy plan-executor check not found")
	}
	if roleCheck.Severity != SevOK {
		t.Errorf("agy role severity = %v, want SevOK", roleCheck.Severity)
	}
	if roleCheck.Detail != "selected by preamble, not a file" {
		t.Errorf("agy role detail = %q, want 'selected by preamble, not a file'", roleCheck.Detail)
	}
}

func TestDoctorUnknownKindDegradesWithoutFailing(t *testing.T) {
	env := &fakeEnv{
		herdrVer: "0.9.0",
		lookPaths: map[string]string{
			"codex": "/usr/bin/codex",
		},
		intStatus: map[string]herdr.IntegrationState{
			// codex is not in herdr integration status
		},
	}

	report := Run(context.Background(), env, []string{"codex"})
	roleCheck := findCheck(report, "codex", "plan-executor")
	if roleCheck == nil {
		t.Fatal("codex plan-executor check not found")
	}
	if roleCheck.Severity != SevOK {
		t.Errorf("codex role check severity = %v, want SevOK", roleCheck.Severity)
	}

	intCheck := findCheck(report, "codex", "integration")
	if intCheck == nil {
		t.Fatal("codex integration check not found")
	}
	if intCheck.Severity != SevOK {
		t.Errorf("codex integration severity = %v, want SevOK when not in herdr integration status", intCheck.Severity)
	}
}

func TestDoctorSecondPassDemotionBesideCompleteHarness(t *testing.T) {
	// claude is complete: binary on PATH and integration installed.
	// opencode is present: binary on PATH, but integration NOT installed.
	// Second pass rule: opencode integration demotes from SevFail to SevWarn, UsableBuilder is true, Failures() == 0.
	env := &fakeEnv{
		herdrVer:      "0.9.0",
		daemonRunning: true,
		lookPaths: map[string]string{
			"claude":   "/usr/bin/claude",
			"opencode": "/usr/bin/opencode",
		},
		intStatus: map[string]herdr.IntegrationState{
			"claude":   {Installed: true, Detail: "current (v9)"},
			"opencode": {Installed: false, Detail: "not installed"},
		},
		homeDir: "/fake/home",
		existingFiles: map[string]bool{
			"/fake/home/.claude/agents/plan-executor.md": true,
		},
	}

	report := Run(context.Background(), env, []string{"claude", "opencode"})
	if !report.UsableBuilder {
		t.Errorf("UsableBuilder = false, want true because claude is complete")
	}
	if report.Failures() != 0 {
		t.Errorf("Failures() = %d, want 0", report.Failures())
	}

	opencodeInt := findCheck(report, "opencode", "integration")
	if opencodeInt == nil {
		t.Fatal("opencode integration check not found")
	}
	if opencodeInt.Severity != SevWarn {
		t.Errorf("opencode integration severity = %v, want SevWarn (demoted from SevFail)", opencodeInt.Severity)
	}
}

func TestDoctorSecondPassStaysFailWhenNoCompleteHarness(t *testing.T) {
	// claude binary present, but integration NOT installed.
	// opencode binary NOT present.
	// No harness is complete -> UsableBuilder is false, claude integration stays SevFail, Failures() == 1.
	env := &fakeEnv{
		herdrVer:      "0.9.0",
		daemonRunning: true,
		lookPaths: map[string]string{
			"claude": "/usr/bin/claude",
		},
		intStatus: map[string]herdr.IntegrationState{
			"claude": {Installed: false, Detail: "not installed"},
		},
	}

	report := Run(context.Background(), env, []string{"claude", "opencode"})
	if report.UsableBuilder {
		t.Errorf("UsableBuilder = true, want false")
	}
	if report.Failures() != 1 {
		t.Errorf("Failures() = %d, want 1", report.Failures())
	}

	claudeInt := findCheck(report, "claude", "integration")
	if claudeInt == nil {
		t.Fatal("claude integration check not found")
	}
	if claudeInt.Severity != SevFail {
		t.Errorf("claude integration severity = %v, want SevFail", claudeInt.Severity)
	}
}
