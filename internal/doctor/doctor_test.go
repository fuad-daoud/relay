package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
)

// shippedDoc returns the exact bytes this relay ships for role/kind, so a
// fixture can be built that matches the shipped copy and does not spuriously
// trip the doctor's ship-drift check (step 3, #91/#94).
func shippedDoc(t *testing.T, role, kind string) string {
	t.Helper()
	b, err := harness.AgentDoc(role, kind)
	if err != nil {
		t.Fatalf("AgentDoc(%s, %s): %v", role, kind, err)
	}
	return string(b)
}

type fakeEnv struct {
	herdrVer      string
	herdrErr      error
	intStatus     map[string]herdr.IntegrationState
	intErr        error
	daemonRunning bool
	daemonErr     error
	lookPaths     map[string]string // binary -> path
	existingFiles map[string]bool   // path -> exists
	fileContents  map[string]string // path -> content; absent reads as empty
	homeDir       string
	homeErr       error
	versions      map[string]string // binary path -> version output
	versionErr    error
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
	if f.homeErr != nil {
		return "", f.homeErr
	}
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

// ReadFile returns recorded content. A path in existingFiles but absent from
// fileContents reads as empty, which must produce no model suffix and no error.
func (f *fakeEnv) ReadFile(path string) ([]byte, error) {
	return []byte(f.fileContents[path]), nil
}

func (f *fakeEnv) BinaryVersion(ctx context.Context, path string) (string, error) {
	if f.versionErr != nil {
		return "", f.versionErr
	}
	v, ok := f.versions[path]
	if !ok {
		return "", errors.New("no version recorded for " + path)
	}
	return v, nil
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
	wantDetail := "not installed -- this binding will report `unknown` forever and never finish a round"
	if opencodeInt.Detail != wantDetail {
		t.Errorf("demoted row detail = %q, want %q", opencodeInt.Detail, wantDetail)
	}
	if opencodeInt.Fix != "herdr integration install opencode" {
		t.Errorf("demoted row fix = %q, want 'herdr integration install opencode'", opencodeInt.Fix)
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

func TestDoctorAdoptedBindingSurvivesMissingBinary(t *testing.T) {
	// Binary is absent on PATH.
	// For normal Run: binary check suppresses remaining rows (0 integration rows).
	// For adopted Run: integration row survives.
	env := &fakeEnv{
		herdrVer:  "0.9.0",
		lookPaths: map[string]string{}, // binary absent
		intStatus: map[string]herdr.IntegrationState{
			"claude": {Installed: false, Detail: "not installed"},
		},
	}

	// Normal run suppresses integration
	normalRep := Run(context.Background(), env, []string{"claude"})
	if findCheck(normalRep, "claude", "integration") != nil {
		t.Fatal("normal Run should suppress integration row when binary is absent")
	}

	// Adopted run computes and preserves integration row
	adoptedRep := Run(context.Background(), env, []string{"claude"}, WithAdopted(true))
	c := findCheck(adoptedRep, "claude", "integration")
	if c == nil {
		t.Fatal("adopted Run must compute integration row even when binary is absent")
	}
	if c.Severity != SevFail {
		t.Errorf("expected SevFail on missing integration, got %v", c.Severity)
	}
}

func TestDoctorUnparseableOrMissingIntegrationIsWarningNotFailure(t *testing.T) {
	// A known target missing from IntegrationStatus map, or whose line failed to parse
	// (e.g. from ParseIntegrationStatus with empty_paren), must report SevWarn with
	// detail that status could not be read, no fix command, and must NOT report SevFail.
	rawHerdrOutput := `
claude: state ()
opencode: not installed (/path/to/opencode.js)
`
	parsedStatus := herdr.ParseIntegrationStatus([]byte(rawHerdrOutput))
	if _, bad := parsedStatus["claude"]; bad {
		t.Fatal("unparseable claude line should be skipped by parser")
	}

	env := &fakeEnv{
		herdrVer:      "0.9.0",
		daemonRunning: true,
		lookPaths: map[string]string{
			"claude": "/usr/bin/claude",
		},
		intStatus: parsedStatus,
	}

	report := Run(context.Background(), env, []string{"claude"})
	c := findCheck(report, "claude", "integration")
	if c == nil {
		t.Fatal("claude integration check missing")
	}
	if c.Severity != SevWarn {
		t.Errorf("expected SevWarn for unread status, got %v", c.Severity)
	}
	if c.Detail != "could not read herdr integration status for claude" {
		t.Errorf("detail = %q, want 'could not read herdr integration status for claude'", c.Detail)
	}
	if c.Fix != "" {
		t.Errorf("fix should be empty, got %q", c.Fix)
	}
	if report.Failures() != 0 {
		t.Errorf("expected 0 failures, got %d", report.Failures())
	}
	if report.UsableBuilder {
		t.Errorf("UsableBuilder should be false when status could not be read")
	}
}

func TestDoctorUsableBuilderOutdatedCountsAsComplete(t *testing.T) {
	// A checked kind with binary on PATH and integration outdated still counts as complete:
	// UsableBuilder is true, and Failures() is 0.
	env := &fakeEnv{
		herdrVer:      "0.9.0",
		daemonRunning: true,
		lookPaths: map[string]string{
			"claude": "/usr/bin/claude",
		},
		intStatus: map[string]herdr.IntegrationState{
			"claude": {Installed: true, Outdated: true, Detail: "outdated (v8 < v9)"},
		},
		homeDir: "/fake/home",
		existingFiles: map[string]bool{
			"/fake/home/.claude/agents/plan-executor.md": true,
		},
	}

	report := Run(context.Background(), env, []string{"claude"})
	if !report.UsableBuilder {
		t.Error("UsableBuilder should be true when integration is outdated (counts as complete)")
	}
	if report.Failures() != 0 {
		t.Errorf("expected 0 failures, got %d", report.Failures())
	}
	c := findCheck(report, "claude", "integration")
	if c == nil || c.Severity != SevWarn {
		t.Errorf("outdated integration should be SevWarn, got: %+v", c)
	}
}

func TestDoctorUsableBuilderMissingRoleFileDoesNotBreakCompleteness(t *testing.T) {
	// A missing role file does not break completeness:
	// binary on PATH + integration current -> UsableBuilder is true, Failures() is 0,
	// plan-executor check is SevWarn.
	env := &fakeEnv{
		herdrVer:      "0.9.0",
		daemonRunning: true,
		lookPaths: map[string]string{
			"claude": "/usr/bin/claude",
		},
		intStatus: map[string]herdr.IntegrationState{
			"claude": {Installed: true, Detail: "current (v9)"},
		},
		homeDir:       "/fake/home",
		existingFiles: map[string]bool{}, // role file missing
	}

	report := Run(context.Background(), env, []string{"claude"})
	if !report.UsableBuilder {
		t.Error("UsableBuilder should be true even when role file is missing")
	}
	if report.Failures() != 0 {
		t.Errorf("expected 0 failures, got %d", report.Failures())
	}
	roleCheck := findCheck(report, "claude", "plan-executor")
	if roleCheck == nil || roleCheck.Severity != SevWarn {
		t.Errorf("missing role file check should be SevWarn, got: %+v", roleCheck)
	}
}

func TestDoctorHomePathFailureReportsErrorWithoutFix(t *testing.T) {
	// A failure resolving home directory should report SevWarn with detail
	// explaining home could not be resolved, Fix empty, ProbeFailed true.
	env := &fakeEnv{
		herdrVer:      "0.9.0",
		daemonRunning: true,
		lookPaths: map[string]string{
			"claude": "/usr/bin/claude",
		},
		intStatus: map[string]herdr.IntegrationState{
			"claude": {Installed: true, Detail: "current (v9)"},
		},
		homeErr: errors.New("cannot determine user home"),
	}

	report := Run(context.Background(), env, []string{"claude"})
	c := findCheck(report, "claude", "plan-executor")
	if c == nil {
		t.Fatal("plan-executor check not found")
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want SevWarn", c.Severity)
	}
	if !c.ProbeFailed {
		t.Error("ProbeFailed must be true on HomePath error")
	}
	if c.Fix != "" {
		t.Errorf("fix must be empty, got %q", c.Fix)
	}
	if !strings.Contains(c.Detail, "could not resolve home directory") {
		t.Errorf("detail = %q, want containing 'could not resolve home directory'", c.Detail)
	}
}

func TestFrontmatterModel(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "pinned model",
			raw:  "---\nname: researcher\nmodel: haiku\n---\n\nbody\n",
			want: "haiku",
		},
		{
			name: "model with a slash",
			raw:  "---\nmodel: openrouter/z-ai/glm-5.3-flash\n---\n",
			want: "openrouter/z-ai/glm-5.3-flash",
		},
		{
			name: "trailing whitespace trimmed",
			raw:  "---\nmodel:   haiku   \n---\n",
			want: "haiku",
		},
		{name: "no model key", raw: "---\nname: researcher\n---\n", want: ""},
		{name: "no frontmatter", raw: "just a body\n", want: ""},
		{name: "empty file", raw: "", want: ""},
		{
			name: "model after the frontmatter is not a pin",
			raw:  "---\nname: x\n---\n\nmodel: not-a-pin\n",
			want: "",
		},
		{
			name: "unterminated frontmatter",
			raw:  "---\nmodel: haiku\n",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := frontmatterModel([]byte(tc.raw)); got != tc.want {
				t.Errorf("frontmatterModel(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestDoctorEmitsOneRowPerRole(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
		"/fake/home/.claude/agents/researcher.md":    true,
	}
	env.fileContents = map[string]string{
		"/fake/home/.claude/agents/plan-executor.md": shippedDoc(t, "plan-executor", "claude"),
		"/fake/home/.claude/agents/researcher.md":    shippedDoc(t, "researcher", "claude"),
	}

	report := Run(context.Background(), env, []string{"claude"})

	for _, role := range []string{"plan-executor", "researcher"} {
		c := findCheck(report, "claude", role)
		if c == nil {
			t.Fatalf("no %q row for claude", role)
		}
		if c.Severity != SevOK {
			t.Errorf("%s severity = %v, want ok", role, c.Severity)
		}
	}
}

func TestDoctorMissingRoleFileNamesTheRoleInTheFix(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
	}

	report := Run(context.Background(), env, []string{"claude"})

	c := findCheck(report, "claude", "researcher")
	if c == nil {
		t.Fatal("no researcher row for claude")
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want warn", c.Severity)
	}
	if !strings.Contains(c.Fix, "--role researcher") {
		t.Errorf("fix must name the role, got %q", c.Fix)
	}
}

func TestDoctorReportsTheInstalledModelPin(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
		"/fake/home/.claude/agents/researcher.md":    true,
	}
	env.fileContents = map[string]string{
		"/fake/home/.claude/agents/researcher.md": "---\nname: researcher\nmodel: haiku\n---\n",
	}

	report := Run(context.Background(), env, []string{"claude"})

	c := findCheck(report, "claude", "researcher")
	if c == nil {
		t.Fatal("no researcher row")
	}
	if !strings.Contains(c.Detail, "model: haiku") {
		t.Errorf("detail = %q, want it to report the pinned model", c.Detail)
	}
}

func TestDoctorOmitsModelSuffixWhenUnpinned(t *testing.T) {
	// plan-executor.claude.md ships with no model: line at all (the pin is a
	// researcher/reviewer worked example, not a plan-executor one), so it is
	// the one role whose shipped copy is itself the "unpinned" fixture and
	// therefore does not also trip the doctor's ship-drift check (step 3).
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
		"/fake/home/.claude/agents/researcher.md":    true,
	}
	env.fileContents = map[string]string{
		"/fake/home/.claude/agents/plan-executor.md": shippedDoc(t, "plan-executor", "claude"),
		"/fake/home/.claude/agents/researcher.md":    shippedDoc(t, "researcher", "claude"),
	}

	report := Run(context.Background(), env, []string{"claude"})

	c := findCheck(report, "claude", "plan-executor")
	if c == nil {
		t.Fatal("no plan-executor row")
	}
	if strings.Contains(c.Detail, "model:") {
		t.Errorf("detail = %q, want no model suffix", c.Detail)
	}
	if c.Severity != SevOK {
		t.Errorf("severity = %v, want ok -- an unpinned model is not a fault", c.Severity)
	}
}

// newFakeEnvForKind is a fakeEnv where everything except the role files is
// healthy, so a test can vary existingFiles and fileContents alone.
func newFakeEnvForKind(t *testing.T, kind string) *fakeEnv {
	t.Helper()
	return &fakeEnv{
		herdrVer:      herdr.MinVersion,
		daemonRunning: true,
		lookPaths:     map[string]string{kind: "/usr/bin/" + kind},
		intStatus: map[string]herdr.IntegrationState{
			kind: {Installed: true, Detail: "current (v9)"},
		},
		homeDir: "/fake/home",
	}
}

// agyEnv is an agy machine in good order: binary on PATH, integration
// installed, three role files present and pinning inherit. Tests perturb
// one thing at a time from here.
func agyEnv(t *testing.T) *fakeEnv {
	t.Helper()
	home := "/home/u"
	env := &fakeEnv{
		herdrVer:      "0.9.0",
		homeDir:       home,
		lookPaths:     map[string]string{"agy": "/home/fuad/.local/bin/agy"},
		intStatus:     map[string]herdr.IntegrationState{"antigravity-cli": {Installed: true, Detail: "current (v3)"}},
		existingFiles: map[string]bool{},
		fileContents:  map[string]string{},
		versions:      map[string]string{"/home/fuad/.local/bin/agy": "1.2.1"},
	}
	for _, name := range []string{"plan-executor", "researcher", "reviewer"} {
		p := home + "/.gemini/config/agents/" + name + ".md"
		env.existingFiles[p] = true
		env.fileContents[p] = shippedDoc(t, name, "agy")
	}
	return env
}

func TestDoctorAgyVersionFloor(t *testing.T) {
	cases := []struct {
		name     string
		version  string
		err      error
		wantSev  Severity
		wantDet  string
		wantFix  string
		wantProb bool
	}{
		{name: "at floor", version: "1.1.6", wantSev: SevOK, wantDet: "1.1.6 (floor 1.1.6)"},
		{name: "above floor", version: "1.2.1", wantSev: SevOK, wantDet: "1.2.1 (floor 1.1.6)"},
		{name: "below floor", version: "1.1.5", wantSev: SevFail, wantDet: "1.1.5 (below floor 1.1.6)", wantFix: "upgrade agy to >= 1.1.6"},
		{name: "garbage", version: "garbage", wantSev: SevWarn, wantDet: `unparseable version "garbage"`},
		{name: "probe fails", err: errors.New("boom"), wantSev: SevWarn, wantDet: "could not read version: boom", wantProb: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := agyEnv(t) // step 4's helper: paths, integration, three role files pinning inherit
			env.versions = map[string]string{"/home/fuad/.local/bin/agy": tc.version}
			env.versionErr = tc.err
			report := Run(context.Background(), env, []string{"agy"})
			c := findCheck(report, "agy", "version")
			if c == nil {
				t.Fatal("agy version row not found")
			}
			if c.Severity != tc.wantSev || c.Detail != tc.wantDet || c.Fix != tc.wantFix || c.ProbeFailed != tc.wantProb {
				t.Errorf("row = %+v, want sev %v detail %q fix %q probeFailed %v", *c, tc.wantSev, tc.wantDet, tc.wantFix, tc.wantProb)
			}
		})
	}
}

func TestDoctorNoVersionRowWithoutAFloor(t *testing.T) {
	for _, kind := range []string{"claude", "opencode"} {
		env := &fakeEnv{
			herdrVer:  "0.9.0",
			lookPaths: map[string]string{kind: "/usr/bin/" + kind},
			intStatus: map[string]herdr.IntegrationState{kind: {Installed: true}},
		}
		report := Run(context.Background(), env, []string{kind})
		if c := findCheck(report, kind, "version"); c != nil {
			t.Errorf("%s has no MinVersion, got version row %+v", kind, *c)
		}
	}
}

func TestDoctorAgyRoleWarnsOnATierPin(t *testing.T) {
	env := agyEnv(t)
	env.versions = map[string]string{"/home/fuad/.local/bin/agy": "1.2.1"}
	env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = "---\nname: researcher\nmodel: pro\n---\nbody\n"
	report := Run(context.Background(), env, []string{"agy"})

	c := findCheck(report, "agy", "researcher")
	if c == nil {
		t.Fatal("agy researcher row not found")
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want SevWarn", c.Severity)
	}
	if c.Detail != "~/.gemini/config/agents/researcher.md (model: pro) -- pins a tier; the candidate's --model is ignored" {
		t.Errorf("detail = %q", c.Detail)
	}
	if c.Fix != "set model: inherit in ~/.gemini/config/agents/researcher.md" {
		t.Errorf("fix = %q", c.Fix)
	}
	for _, name := range []string{"plan-executor", "reviewer"} {
		if c := findCheck(report, "agy", name); c == nil || c.Severity != SevOK {
			t.Errorf("%s row = %+v, want SevOK", name, c)
		}
	}
}

func TestDoctorClaudeRoleNeverWarnsOnAPin(t *testing.T) {
	// This test pins the decision that non-agy definitions are the user's to
	// edit: claude's Role.ExpectModel is "" (spec §3.2), so neither the
	// tier-pin branch nor the ship-drift check (round 2, #91/#94) fires here.
	env := &fakeEnv{
		herdrVer:      "0.9.0",
		homeDir:       "/home/u",
		lookPaths:     map[string]string{"claude": "/usr/bin/claude"},
		intStatus:     map[string]herdr.IntegrationState{"claude": {Installed: true}},
		existingFiles: map[string]bool{"/home/u/.claude/agents/plan-executor.md": true},
		fileContents:  map[string]string{"/home/u/.claude/agents/plan-executor.md": "---\nmodel: opus\n---\n"},
	}
	report := Run(context.Background(), env, []string{"claude"})
	c := findCheck(report, "claude", "plan-executor")
	if c == nil || c.Severity != SevOK || c.Detail != "~/.claude/agents/plan-executor.md (model: opus)" {
		t.Errorf("row = %+v, want SevOK with the pin reported", c)
	}
}

func TestDoctorAgyRolesAreCheckedLikeAnyKind(t *testing.T) {
	env := agyEnv(t)
	report := Run(context.Background(), env, []string{"agy"})
	for _, name := range []string{"plan-executor", "researcher", "reviewer"} {
		c := findCheck(report, "agy", name)
		if c == nil {
			t.Fatalf("agy %s row not found", name)
		}
		want := "~/.gemini/config/agents/" + name + ".md (model: inherit)"
		if c.Severity != SevOK || c.Detail != want {
			t.Errorf("%s row = %+v, want SevOK %q", name, *c, want)
		}
	}
}

func TestDoctorAgyMissingRoleHasAFix(t *testing.T) {
	env := agyEnv(t)
	delete(env.existingFiles, "/home/u/.gemini/config/agents/reviewer.md")
	report := Run(context.Background(), env, []string{"agy"})
	c := findCheck(report, "agy", "reviewer")
	if c == nil || c.Severity != SevWarn || c.Detail != "missing: ~/.gemini/config/agents/reviewer.md" ||
		c.Fix != "relay agent print --kind agy --role reviewer > ~/.gemini/config/agents/reviewer.md" {
		t.Errorf("row = %+v", c)
	}
}

// Step 3 (#91/#94), narrowed in round 2: relay doctor warns when an
// installed definition differs from the shipped one only on a kind whose
// definition relay owns outright (Role.ExpectModel set -- today, agy).
// These four subtests exercise that on agy; the fifth exercises the
// opposite on claude, which the gate in doctor.roleCheck excludes.
func TestDoctorRoleDriftFromShipped(t *testing.T) {
	shipped := shippedDoc(t, "researcher", "agy")

	t.Run("identical to shipped is OK", func(t *testing.T) {
		env := agyEnv(t)
		env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = shipped
		report := Run(context.Background(), env, []string{"agy"})
		c := findCheck(report, "agy", "researcher")
		wantDetail := "~/.gemini/config/agents/researcher.md (model: inherit)"
		if c == nil || c.Severity != SevOK || c.Detail != wantDetail {
			t.Errorf("row = %+v, want SevOK %q", c, wantDetail)
		}
	})

	t.Run("shipped plus a trailing newline is still OK", func(t *testing.T) {
		env := agyEnv(t)
		env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = shipped + "\n"
		report := Run(context.Background(), env, []string{"agy"})
		c := findCheck(report, "agy", "researcher")
		if c == nil || c.Severity != SevOK {
			t.Errorf("row = %+v, want SevOK -- a missing final newline from a `>` redirect must not warn", c)
		}
	})

	t.Run("one extra line warns with the print fix", func(t *testing.T) {
		env := agyEnv(t)
		env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = shipped + "\nextra line\n"
		report := Run(context.Background(), env, []string{"agy"})
		c := findCheck(report, "agy", "researcher")
		if c == nil || c.Severity != SevWarn {
			t.Fatalf("row = %+v, want SevWarn", c)
		}
		if !strings.Contains(c.Detail, "differs from the definition this relay ships") {
			t.Errorf("detail = %q, want it to mention shipped drift", c.Detail)
		}
		if c.Fix != "relay agent print --kind agy --role researcher > ~/.gemini/config/agents/researcher.md" {
			t.Errorf("fix = %q", c.Fix)
		}
	})

	t.Run("a tier-pin mismatch still reports the pin warning, not drift", func(t *testing.T) {
		// agyEnv's role files are already byte-identical to the shipped
		// copies (agyEnv/shippedDoc, above); overriding the pin necessarily
		// makes the installed copy differ from what's shipped too, but the
		// pin check runs first and its message is the more specific one.
		env := agyEnv(t)
		env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = "---\nname: researcher\nmodel: pro\n---\nbody\n"
		report := Run(context.Background(), env, []string{"agy"})
		c := findCheck(report, "agy", "researcher")
		wantDetail := "~/.gemini/config/agents/researcher.md (model: pro) -- pins a tier; the candidate's --model is ignored"
		if c == nil || c.Severity != SevWarn || c.Detail != wantDetail {
			t.Errorf("row = %+v, want SevWarn %q", c, wantDetail)
		}
	})

	t.Run("a claude definition that differs from shipped is still OK", func(t *testing.T) {
		// This is the test the ExpectModel gate in doctor.roleCheck is for:
		// claude's Role.ExpectModel is "", so the installed copy is the
		// user's to edit and doctor does not compare it against shipped.
		env := newFakeEnvForKind(t, "claude")
		env.existingFiles = map[string]bool{"/fake/home/.claude/agents/plan-executor.md": true}
		env.fileContents = map[string]string{
			"/fake/home/.claude/agents/plan-executor.md": shippedDoc(t, "plan-executor", "claude") + "\nextra line\n",
		}
		report := Run(context.Background(), env, []string{"claude"})
		c := findCheck(report, "claude", "plan-executor")
		if c == nil || c.Severity != SevOK {
			t.Errorf("row = %+v, want SevOK", c)
		}
	})
}
