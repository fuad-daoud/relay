package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/store"
)

// shippedDoc returns the exact bytes this relevo ships for role/kind, so a
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
	daemonRunning bool
	daemonErr     error
	// daemonInfo* are what DaemonInfo reports: the record, whether it exists,
	// and an optional read error (#371).
	daemonInfo    store.DaemonInfo
	daemonInfoOK  bool
	daemonInfoErr error
	lookPaths     map[string]string // binary -> path
	existingFiles map[string]bool   // path -> exists
	fileContents  map[string]string // path -> content; absent reads as empty
	homeDir       string
	homeErr       error
	versions      map[string]string // binary path -> version output
	versionErr    error
	probeErr      error
	commandOut    []byte
	commandErr    error

	// release* are what ReleaseState reports: the running version, the cached
	// latest, whether that cache is usable, and the install kind (#293).
	releaseRunning string
	releaseLatest  string
	releaseOK      bool
	releaseKind    release.Kind

	// manifest is what LoadManifest returns (#371 §4.10): a home-relative
	// definition path to the sha relevo last wrote there. nil reads as no
	// manifest recorded.
	manifest map[string]string
}

func (f *fakeEnv) DaemonRunning(ctx context.Context) (bool, error) {
	if f.daemonErr != nil {
		return false, f.daemonErr
	}
	return f.daemonRunning, nil
}

func (f *fakeEnv) DaemonInfo() (store.DaemonInfo, bool, error) {
	if f.daemonInfoErr != nil {
		return store.DaemonInfo{}, false, f.daemonInfoErr
	}
	return f.daemonInfo, f.daemonInfoOK, nil
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

func (f *fakeEnv) Probe(dir string) error {
	return f.probeErr
}

func (f *fakeEnv) Command(ctx context.Context, bin string, args ...string) ([]byte, error) {
	if f.commandErr != nil {
		return nil, f.commandErr
	}
	return f.commandOut, nil
}

func (f *fakeEnv) ReleaseState() (string, string, bool, release.Kind) {
	return f.releaseRunning, f.releaseLatest, f.releaseOK, f.releaseKind
}

// LoadManifest satisfies doctor.Env (#371 §4.10): the recorded manifest, or
// nil for a machine that has none.
func (f *fakeEnv) LoadManifest() (map[string]string, error) {
	return f.manifest, nil
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

// TestConfigCheck pins #372 §4.4's config row: no warnings is OK, and warnings
// render as a Warn listing every one.
func TestConfigCheck(t *testing.T) {
	ok := ConfigCheck(nil)
	if ok.Name != "config" || ok.Group != "" || ok.Severity != SevOK {
		t.Errorf("ConfigCheck(nil) = %+v, want an OK global config row", ok)
	}

	warnings := []string{
		`policy.json: unknown key "orders" (a typo, or a key a newer relevo reads)`,
		`candidates.json: nope/p/m: unknown harness "nope" (skipped)`,
	}
	w := ConfigCheck(warnings)
	if w.Severity != SevWarn {
		t.Errorf("ConfigCheck(2) severity = %v, want SevWarn", w.Severity)
	}
	for _, want := range []string{"orders", "unknown harness"} {
		if !strings.Contains(w.Detail, want) {
			t.Errorf("ConfigCheck(2) detail %q does not contain %q", w.Detail, want)
		}
	}
}

// TestDoctorConfigRow pins that Run renders the config row from
// WithConfigWarnings.
func TestDoctorConfigRow(t *testing.T) {
	rep := Run(context.Background(), &fakeEnv{}, nil,
		WithConfigWarnings([]string{"w1", "w2"}))

	c := findCheck(rep, "", "config")
	if c == nil {
		t.Fatal("no config row in the report")
	}
	if c.Severity != SevWarn || !strings.Contains(c.Detail, "w1") || !strings.Contains(c.Detail, "w2") {
		t.Errorf("config row = %+v, want a Warn listing both warnings", c)
	}

	clean := Run(context.Background(), &fakeEnv{}, nil, WithConfigWarnings(nil))
	if c := findCheck(clean, "", "config"); c == nil || c.Severity != SevOK {
		t.Errorf("config row with no warnings = %+v, want OK", c)
	}
}

func TestDoctorMissingBinarySuppressesRemainingRows(t *testing.T) {
	env := &fakeEnv{
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
		lookPaths: map[string]string{
			"droid": "/usr/bin/droid",
		},
	}

	report := Run(context.Background(), env, []string{"droid"})
	roleCheck := findCheck(report, "droid", "plan-executor")
	if roleCheck == nil {
		t.Fatal("droid plan-executor check not found")
	}
	if roleCheck.Severity != SevOK {
		t.Errorf("droid role check severity = %v, want SevOK", roleCheck.Severity)
	}
	if report.Failures() != 0 {
		t.Errorf("an unknown kind must not fail doctor, got %d failures", report.Failures())
	}
}

// TestDoctorRolesRow covers §4.10's role-staleness row: stale when the daemon
// would write or update a definition, OK with the "differs from every copy
// relevo has shipped (kept as your edit)" detail when the user's own edit is
// being kept, and OK when everything is current. A harness whose binary is not
// on PATH gets no row, because it gets no other per-harness row either.
func TestDoctorRolesRow(t *testing.T) {
	claudeRoles := []string{"plan-executor", "researcher", "reviewer", "architect"}
	relPath := func(role string) string { return ".claude/agents/" + role + ".md" }

	envFor := func(contents map[string]string) *fakeEnv {
		existing := map[string]bool{}
		files := map[string]string{}
		for role, content := range contents {
			p := "/fake/home/" + relPath(role)
			existing[p] = true
			files[p] = content
		}
		return &fakeEnv{
			daemonRunning: true,
			lookPaths:     map[string]string{"claude": "/usr/bin/claude"},
			homeDir:       "/fake/home",
			existingFiles: existing,
			fileContents:  files,
		}
	}

	t.Run("stale", func(t *testing.T) {
		rep := Run(context.Background(), envFor(nil), []string{"claude"})
		c := findCheck(rep, "claude", "roles")
		if c == nil {
			t.Fatal("claude has its binary on PATH, so it needs a roles row")
		}
		if c.Severity != SevWarn {
			t.Errorf("severity = %v, want SevWarn", c.Severity)
		}
		if !strings.Contains(c.Detail, "role definitions are stale") {
			t.Errorf("detail = %q, want it to say the definitions are stale", c.Detail)
		}
		if c.Fix != "relevo config agents" {
			t.Errorf("fix = %q, want relevo config agents", c.Fix)
		}
	})

	t.Run("user edited is kept", func(t *testing.T) {
		contents := map[string]string{}
		for _, role := range claudeRoles {
			contents[role] = "---\nmodel: haiku\n---\nmine\n"
		}
		rep := Run(context.Background(), envFor(contents), []string{"claude"})
		c := findCheck(rep, "claude", "roles")
		if c == nil {
			t.Fatal("claude has its binary on PATH, so it needs a roles row")
		}
		if c.Severity != SevOK || c.Detail != "differs from every copy relevo has shipped (kept as your edit)" {
			t.Errorf("row = %+v, want OK with detail %q", *c, "differs from every copy relevo has shipped (kept as your edit)")
		}
	})

	t.Run("current", func(t *testing.T) {
		contents := map[string]string{}
		for _, role := range claudeRoles {
			contents[role] = shippedDoc(t, role, "claude")
		}
		rep := Run(context.Background(), envFor(contents), []string{"claude"})
		c := findCheck(rep, "claude", "roles")
		if c == nil {
			t.Fatal("claude has its binary on PATH, so it needs a roles row")
		}
		if c.Severity != SevOK || c.Detail != "up to date" {
			t.Errorf("row = %+v, want OK and up to date", *c)
		}
	})

	t.Run("binary off PATH has no row", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}}
		rep := Run(context.Background(), env, []string{"claude"})
		if c := findCheck(rep, "claude", "roles"); c != nil {
			t.Errorf("roles row %+v, want none when the binary is off PATH", *c)
		}
	})
}

func TestDoctorAdoptedBindingSurvivesMissingBinary(t *testing.T) {
	env := &fakeEnv{
		lookPaths: map[string]string{}, // binary absent
	}

	// A normal Run reports the binary it could not find.
	normalRep := Run(context.Background(), env, []string{"claude"})
	if findCheck(normalRep, "claude", "binary") == nil {
		t.Fatal("normal Run must report the binary it could not find on PATH")
	}

	// An adopted pane's binary is the user's own concern: no binary row, and
	// the role rows that binary gates stay off too.
	adoptedRep := Run(context.Background(), env, []string{"claude"}, WithAdopted(true))
	if c := findCheck(adoptedRep, "claude", "binary"); c != nil {
		t.Fatalf("adopted Run must not report a binary row, got %+v", *c)
	}
	for _, role := range []string{"plan-executor", "researcher", "reviewer"} {
		if c := findCheck(adoptedRep, "claude", role); c != nil {
			t.Errorf("adopted Run must not report a %s role row, got %+v", role, *c)
		}
	}
}

func TestDoctorUsableBuilderMissingRoleFileDoesNotBreakCompleteness(t *testing.T) {
	env := &fakeEnv{
		daemonRunning: true,
		lookPaths: map[string]string{
			"claude": "/usr/bin/claude",
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
		daemonRunning: true,
		lookPaths: map[string]string{
			"claude": "/usr/bin/claude",
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
	want := "relevo config agents --kind claude --role researcher"
	if c.Fix != want {
		t.Errorf("fix = %q, want %q", c.Fix, want)
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
		daemonRunning: true,
		lookPaths:     map[string]string{kind: "/usr/bin/" + kind},
		homeDir:       "/fake/home",
	}
}

// agyEnv is an agy machine in good order: binary on PATH, three role files
// present and pinning inherit. Tests perturb one thing at a time from here.
func agyEnv(t *testing.T) *fakeEnv {
	t.Helper()
	home := "/home/u"
	env := &fakeEnv{
		homeDir:       home,
		lookPaths:     map[string]string{"agy": "/home/fuad/.local/bin/agy"},
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
			env := agyEnv(t) // step 4's helper: paths and three role files pinning inherit
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
			lookPaths: map[string]string{kind: "/usr/bin/" + kind},
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
		homeDir:       "/home/u",
		lookPaths:     map[string]string{"claude": "/usr/bin/claude"},
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
		c.Fix != "relevo config agents --kind agy --role reviewer" {
		t.Errorf("row = %+v", c)
	}
}

// Step 3 (#91/#94), narrowed in round 2: relevo doctor warns when an
// installed definition differs from the shipped one only on a kind whose
// definition relevo owns outright (Role.ExpectModel set -- today, agy).
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
		if !strings.Contains(c.Detail, "differs from the definition this relevo ships") {
			t.Errorf("detail = %q, want it to mention shipped drift", c.Detail)
		}
		if c.Fix != "relevo config agents --kind agy --role researcher --force" {
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

func TestPinnedModelToml(t *testing.T) {
	cases := []struct {
		name string
		kind string
		raw  string
		want string
	}{
		{
			name: "codex top-level model before a table",
			kind: "codex",
			raw:  "# c\nmodel = \"gpt-5.6-luna\"\nmodel_reasoning_effort = \"medium\"\n[agents.x]\nmodel = \"other\"\n",
			want: "gpt-5.6-luna",
		},
		{
			name: "codex model_reasoning_effort must not match model",
			kind: "codex",
			raw:  "model_reasoning_effort = \"medium\"\n",
			want: "",
		},
		{
			name: "codex model inside a table is not a top-level pin",
			kind: "codex",
			raw:  "[agents.x]\nmodel = \"other\"\n",
			want: "",
		},
		{
			name: "claude frontmatter model",
			kind: "claude",
			raw:  "---\nmodel: opus\n---\n",
			want: "opus",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinnedModel(tc.kind, []byte(tc.raw)); got != tc.want {
				t.Errorf("pinnedModel(%q, ...) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

func TestDoctorCodexResearcherPin(t *testing.T) {
	newEnv := func(t *testing.T) *fakeEnv {
		t.Helper()
		home := "/home/u"
		env := &fakeEnv{
			homeDir:       home,
			lookPaths:     map[string]string{"codex": "/usr/bin/codex"},
			existingFiles: map[string]bool{},
			fileContents:  map[string]string{},
			versions:      map[string]string{"/usr/bin/codex": "0.155.1"},
		}
		for _, role := range []string{"plan-executor", "researcher", "reviewer", "architect"} {
			env.existingFiles[home+"/.codex/"+role+".config.toml"] = true
		}
		return env
	}

	t.Run("researcher pinned to the shipped model is OK", func(t *testing.T) {
		env := newEnv(t)
		env.fileContents["/home/u/.codex/researcher.config.toml"] = shippedDoc(t, "researcher", "codex")
		report := Run(context.Background(), env, []string{"codex"})
		c := findCheck(report, "codex", "researcher")
		want := "~/.codex/researcher.config.toml (model: gpt-5.6-luna)"
		if c == nil || c.Severity != SevOK || c.Detail != want {
			t.Errorf("row = %+v, want SevOK %q", c, want)
		}
	})

	t.Run("researcher drifted to another model warns with the install fix", func(t *testing.T) {
		env := newEnv(t)
		drifted := strings.ReplaceAll(shippedDoc(t, "researcher", "codex"), "gpt-5.6-luna", "gpt-5.6-terra")
		env.fileContents["/home/u/.codex/researcher.config.toml"] = drifted
		report := Run(context.Background(), env, []string{"codex"})
		c := findCheck(report, "codex", "researcher")
		wantDetail := "~/.codex/researcher.config.toml (model: gpt-5.6-terra) -- pins gpt-5.6-terra; relevo ships gpt-5.6-luna"
		wantFix := "relevo config agents --kind codex --role researcher --force"
		if c == nil || c.Severity != SevWarn || c.Detail != wantDetail || c.Fix != wantFix {
			t.Errorf("row = %+v, want SevWarn %q fix %q", c, wantDetail, wantFix)
		}
	})

	t.Run("plan-executor carries no pin suffix", func(t *testing.T) {
		env := newEnv(t)
		env.fileContents["/home/u/.codex/plan-executor.config.toml"] = shippedDoc(t, "plan-executor", "codex")
		report := Run(context.Background(), env, []string{"codex"})
		c := findCheck(report, "codex", "plan-executor")
		want := "~/.codex/plan-executor.config.toml"
		if c == nil || c.Severity != SevOK || c.Detail != want {
			t.Errorf("row = %+v, want SevOK %q", c, want)
		}
	})
}

func TestDoctorChecksOnlyTheDefinitionsGiven(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	// No role files installed at all.
	env.existingFiles = map[string]bool{}

	report := Run(context.Background(), env, []string{"claude"},
		WithDefinitions(map[string][]string{"claude": {"plan-executor", "researcher"}}))

	for _, name := range []string{"plan-executor", "researcher"} {
		c := findCheck(report, "claude", name)
		if c == nil {
			t.Fatalf("no %s row for claude", name)
		}
		if c.Severity != SevWarn {
			t.Errorf("%s severity = %v, want warn (file missing)", name, c.Severity)
		}
	}
	if c := findCheck(report, "claude", "reviewer"); c != nil {
		t.Errorf("reviewer row present although no candidate on claude can select it: %+v", *c)
	}
}

func TestDoctorKindAbsentFromDefinitionsKeepsEveryRow(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{}

	report := Run(context.Background(), env, []string{"claude"},
		WithDefinitions(map[string][]string{"opencode": {"plan-executor"}}))

	for _, name := range []string{"plan-executor", "researcher", "reviewer"} {
		if findCheck(report, "claude", name) == nil {
			t.Errorf("no %s row for claude; a kind absent from the map must keep every shipped definition", name)
		}
	}
}

// findUsageCheck is findCheck's counterpart for the usage checks (#142):
// those are matched by Name and a Detail substring rather than by Group,
// since sqlite3/prices rows are global (Group ""), and findCheck above
// already owns the name findCheck for the Group/Name lookup the rest of
// this file uses.
func findUsageCheck(rep Report, name, detailSub string) (Check, bool) {
	for _, c := range rep.Checks {
		if c.Name == name && strings.Contains(c.Detail, detailSub) {
			return c, true
		}
	}
	return Check{}, false
}

func TestUsageChecks(t *testing.T) {
	t.Run("sqlite3 missing with opencode configured", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
		rep := Run(context.Background(), env, nil, WithUsage(nil, true))
		c, ok := findUsageCheck(rep, "sqlite3", "")
		if !ok || c.Severity != SevWarn {
			t.Errorf("want a warn row for sqlite3: %+v", rep.Checks)
		}
		if !strings.Contains(c.Detail, "background wait") {
			t.Errorf("Detail = %q, want it to say an opencode planner's reports wait for the background wait", c.Detail)
		}
		if strings.Contains(c.Detail, "pane") {
			t.Errorf("Detail = %q, want it to name no pane", c.Detail)
		}
	})
	t.Run("sqlite3 not needed without opencode", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
		rep := Run(context.Background(), env, nil, WithUsage(nil, false))
		if _, ok := findUsageCheck(rep, "sqlite3", ""); ok {
			t.Error("no opencode candidate: no sqlite3 row")
		}
	})
	t.Run("prices absent is ok, stale is warn, malformed is warn", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
		rep := Run(context.Background(), env, nil, WithUsage(nil, false))
		if c, ok := findUsageCheck(rep, "prices", "default"); !ok || c.Severity != SevOK {
			t.Errorf("absent prices section: want an OK row naming the default: %+v", rep.Checks)
		}

		rep = Run(context.Background(), env, nil, WithUsage([]byte(`{"as_of":"2020-01-01","models":{}}`), false))
		if c, ok := findUsageCheck(rep, "prices", "2020-01-01"); !ok || c.Severity != SevWarn {
			t.Errorf("stale as_of: want a warn row: %+v", rep.Checks)
		}

		rep = Run(context.Background(), env, nil, WithUsage([]byte(`{"models": 5}`), false))
		if c, ok := findUsageCheck(rep, "prices", "does not validate"); !ok || c.Severity != SevWarn {
			t.Errorf("malformed: want a warn row: %+v", rep.Checks)
		}
	})
}

// TestExtraChecksAppended checks that WithExtraChecks appends its checks
// verbatim at the end of the report, after every check Run itself built --
// including the usage checks WithUsage enables (#100 step 5: cmd/relevo
// folds relevo.ProbeServers' server rows in this way, without Run knowing
// anything about servers.json or the network).
func TestExtraChecksAppended(t *testing.T) {
	env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
	extra := []Check{
		{Group: "", Name: "servers", Severity: SevOK, Detail: "zen: enrolled as laptop"},
	}

	rep := Run(context.Background(), env, nil, WithUsage(nil, false), WithExtraChecks(extra))

	if len(rep.Checks) == 0 {
		t.Fatal("report has no checks")
	}
	// Mutation target: have WithExtraChecks prepend, or Run drop cfg.extra
	// entirely, and this either finds "servers" somewhere other than last,
	// or not at all.
	last := rep.Checks[len(rep.Checks)-1]
	if last.Name != "servers" || last.Detail != "zen: enrolled as laptop" {
		t.Fatalf("last check = %+v, want the extra check appended after every check Run built (including usage)", last)
	}
	if c := findCheck(rep, "", "prices"); c == nil {
		t.Fatal("usage's own prices check must still run alongside an extra check")
	}
}
func TestClassifyCheck(t *testing.T) {
	t.Run("unconfigured", func(t *testing.T) {
		st := classify.Status{Configured: false}
		c := ClassifyCheck(st)
		if c.Name != "classify" {
			t.Errorf("Name = %q, want classify", c.Name)
		}
		if c.Severity != SevOK {
			t.Errorf("Severity = %v, want SevOK", c.Severity)
		}
		if !strings.Contains(c.Detail, "regex only (no classify block in policy.json)") {
			t.Errorf("Detail = %q", c.Detail)
		}
		if c.Fix != "" {
			t.Errorf("Fix = %q, want empty", c.Fix)
		}
	})

	t.Run("configured with env key", func(t *testing.T) {
		st := classify.Status{
			Configured: true,
			Model:      "jev-latest",
			KeySource:  "env",
		}
		c := ClassifyCheck(st)
		if c.Name != "classify" {
			t.Errorf("Name = %q, want classify", c.Name)
		}
		if c.Severity != SevOK {
			t.Errorf("Severity = %v, want SevOK", c.Severity)
		}
		if !strings.Contains(c.Detail, "jev-latest; key from TYPESAFE_API_KEY") {
			t.Errorf("Detail = %q", c.Detail)
		}
		if !strings.HasSuffix(c.Detail, "; not passed to builders") {
			t.Errorf("Detail %q does not end with '; not passed to builders'", c.Detail)
		}
		if c.Fix != "" {
			t.Errorf("Fix = %q, want empty", c.Fix)
		}
	})

	t.Run("configured with db key", func(t *testing.T) {
		st := classify.Status{
			Configured: true,
			Model:      "jev-custom",
			KeySource:  "db",
		}
		c := ClassifyCheck(st)
		if c.Name != "classify" {
			t.Errorf("Name = %q, want classify", c.Name)
		}
		if c.Severity != SevOK {
			t.Errorf("Severity = %v, want SevOK", c.Severity)
		}
		if !strings.Contains(c.Detail, "jev-custom; key from the database") {
			t.Errorf("Detail = %q", c.Detail)
		}
		if c.Fix != "" {
			t.Errorf("Fix = %q, want empty", c.Fix)
		}
	})

	t.Run("configured with missing key", func(t *testing.T) {
		st := classify.Status{
			Configured: true,
			Model:      "jev-latest",
			KeySource:  "",
		}
		c := ClassifyCheck(st)
		if c.Name != "classify" {
			t.Errorf("Name = %q, want classify", c.Name)
		}
		if c.Severity != SevWarn {
			t.Errorf("Severity = %v, want SevWarn", c.Severity)
		}
		if !strings.Contains(c.Detail, "jev-latest configured but no classifier key; the daemon falls back to regex") {
			t.Errorf("Detail = %q", c.Detail)
		}
		if !strings.Contains(c.Fix, "set TYPESAFE_API_KEY for the daemon, or store a key in the database") {
			t.Errorf("Fix = %q", c.Fix)
		}
	})
}

// TestOpencodeAllowlistRow checks the wiring (#236): the opencode branch grows
// the external_directory row when a state root is supplied, and no other kind
// does. Dropping the kind check in Run makes the second half fail.
func TestOpencodeAllowlistRow(t *testing.T) {
	const stateRoot = "/fake/home/.local/state/relevo"

	env := newFakeEnvForKind(t, "opencode")
	env.existingFiles = map[string]bool{}
	env.fileContents = map[string]string{}

	rep := Run(context.Background(), env, []string{"opencode"}, WithStateRoot(stateRoot))
	c := findCheck(rep, "opencode", "external_directory")
	if c == nil {
		t.Fatalf("no external_directory row for opencode: %+v", rep.Checks)
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want warn (no opencode config file)", c.Severity)
	}

	other := newFakeEnvForKind(t, "claude")
	other.existingFiles = map[string]bool{}
	other.fileContents = map[string]string{}
	rep = Run(context.Background(), other, []string{"claude"}, WithStateRoot(stateRoot))
	for _, c := range rep.Checks {
		if c.Name == "external_directory" {
			t.Errorf("claude must not carry the opencode allowlist row: %+v", c)
		}
	}
}

// releaseArchiveFix builds the KindRelease fix text the way doctor.releaseFix
// does, from this platform's own URLs, so the table row below passes on any
// platform.
func releaseArchiveFix(latest string) string {
	archive, checksums := release.AssetURLs(latest, runtime.GOOS, runtime.GOARCH)
	return fmt.Sprintf("relevo update (or download %s, check it against %s, and replace this relevo binary with the one inside)", archive, checksums)
}

// TestDoctorReleaseCheck walks §4.5's table through fakeEnv: every row, the
// Fix for each install variant, and the standing promise that a stale relevo
// is a warning, never a failure.
func TestDoctorReleaseCheck(t *testing.T) {
	tests := []struct {
		name         string
		env          fakeEnv
		wantSeverity Severity
		wantDetail   string
		wantFix      string
	}{
		{
			name:         "no cache: not checked",
			env:          fakeEnv{},
			wantSeverity: SevOK,
			wantDetail:   "not checked",
		},
		{
			name: "unparseable running side: not checked",
			env: fakeEnv{
				releaseRunning: "(devel)", releaseLatest: "v0.7.0",
				releaseOK: true, releaseKind: release.KindLocalBuild,
			},
			wantSeverity: SevOK,
			wantDetail:   "not checked",
		},
		{
			name: "unparseable latest side: not checked",
			env: fakeEnv{
				releaseRunning: "v0.6.0", releaseLatest: "not-a-tag",
				releaseOK: true, releaseKind: release.KindGoInstall,
			},
			wantSeverity: SevOK,
			wantDetail:   "not checked",
		},
		{
			name: "unknown install kind: not checked",
			env: fakeEnv{
				releaseRunning: "v0.6.0", releaseLatest: "v0.7.0",
				releaseOK: true, releaseKind: release.KindUnknown,
			},
			wantSeverity: SevOK,
			wantDetail:   "not checked",
		},
		{
			name: "local build: nothing to update to",
			env: fakeEnv{
				releaseRunning: "v0.7.0-8-gbd8aed0", releaseLatest: "v0.8.0",
				releaseOK: true, releaseKind: release.KindLocalBuild,
			},
			wantSeverity: SevOK,
			wantDetail:   "local build v0.7.0-8-gbd8aed0; nothing to update to",
		},
		{
			name: "latest not newer: current",
			env: fakeEnv{
				releaseRunning: "v0.7.0", releaseLatest: "v0.7.0",
				releaseOK: true, releaseKind: release.KindGoInstall,
			},
			wantSeverity: SevOK,
			wantDetail:   "v0.7.0 is current",
		},
		{
			name: "behind a go install: go install",
			env: fakeEnv{
				releaseRunning: "v0.6.0", releaseLatest: "v0.7.0",
				releaseOK: true, releaseKind: release.KindGoInstall,
			},
			wantSeverity: SevWarn,
			wantDetail:   "v0.6.0 is behind v0.7.0",
			wantFix:      "go install github.com/fuad-daoud/relevo/cmd/relevo@latest",
		},
		{
			// The release row's fix is built from this platform's own URLs,
			// exactly the way doctor.releaseFix builds it.
			name: "behind a release binary: archive and checksums",
			env: fakeEnv{
				releaseRunning: "v0.8.0", releaseLatest: "v0.9.0",
				releaseOK: true, releaseKind: release.KindRelease,
			},
			wantSeverity: SevWarn,
			wantDetail:   "v0.8.0 is behind v0.9.0",
			wantFix:      releaseArchiveFix("v0.9.0"),
		},
		{
			name: "release binary at the latest tag: current",
			env: fakeEnv{
				releaseRunning: "v0.9.0", releaseLatest: "v0.9.0",
				releaseOK: true, releaseKind: release.KindRelease,
			},
			wantSeverity: SevOK,
			wantDetail:   "v0.9.0 is current",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.env
			rep := Run(context.Background(), &env, nil)

			c := findCheck(rep, "", "release")
			if c == nil {
				t.Fatal("release check not found in report")
			}
			if c.Severity == SevFail {
				t.Errorf("release severity = SevFail; a stale relevo runs fine")
			}
			if c.Severity != tc.wantSeverity {
				t.Errorf("release severity = %v, want %v (detail %q)", c.Severity, tc.wantSeverity, c.Detail)
			}
			if c.Detail != tc.wantDetail {
				t.Errorf("release detail = %q, want %q", c.Detail, tc.wantDetail)
			}
			if c.Fix != tc.wantFix {
				t.Errorf("release fix = %q, want %q", c.Fix, tc.wantFix)
			}
		})
	}
}

// TestReleaseFix is the pure fix-text table: the release row is the literal
// string with the linux/amd64 URLs written out in full, and the kinds with no
// update path return "".
func TestReleaseFix(t *testing.T) {
	tests := []struct {
		name   string
		kind   release.Kind
		latest string
		goos   string
		goarch string
		want   string
	}{
		{
			name:   "release names the archive and checksums",
			kind:   release.KindRelease,
			latest: "v0.9.0",
			goos:   "linux",
			goarch: "amd64",
			want:   "relevo update (or download https://github.com/fuad-daoud/relevo/releases/download/v0.9.0/relevo_v0.9.0_linux_amd64.tar.gz, check it against https://github.com/fuad-daoud/relevo/releases/download/v0.9.0/checksums.txt, and replace this relevo binary with the one inside)",
		},
		{
			name:   "go install keeps its command",
			kind:   release.KindGoInstall,
			latest: "v0.9.0",
			goos:   "linux",
			goarch: "amd64",
			want:   "go install github.com/fuad-daoud/relevo/cmd/relevo@latest",
		},
		{
			name:   "unknown has no fix",
			kind:   release.KindUnknown,
			latest: "v0.9.0",
			goos:   "linux",
			goarch: "amd64",
			want:   "",
		},
		{
			name:   "local build has no fix",
			kind:   release.KindLocalBuild,
			latest: "v0.9.0",
			goos:   "linux",
			goarch: "amd64",
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := releaseFix(tc.kind, tc.latest, tc.goos, tc.goarch); got != tc.want {
				t.Errorf("releaseFix(%q, %q, %q, %q) = %q, want %q", tc.kind, tc.latest, tc.goos, tc.goarch, got, tc.want)
			}
		})
	}
}

// TestDoctorDaemonVersionStates covers the four states of §4.8's daemon row
// while the daemon is running (#371): no record, a refused binary, a version
// behind the CLI, and equal.
func TestDoctorDaemonVersionStates(t *testing.T) {
	tests := []struct {
		name         string
		info         store.DaemonInfo
		ok           bool
		wantSeverity Severity
		wantDetail   string
		wantFix      string
	}{
		{
			name:         "missing record",
			ok:           false,
			wantSeverity: SevWarn,
			wantDetail:   "running, but started before relevo recorded its version: it will not follow upgrades until restarted once",
			wantFix:      "systemctl --user restart relevo.service, or make service",
		},
		{
			name: "refused binary",
			info: store.DaemonInfo{
				Version:      "v1",
				Exe:          "/usr/local/bin/relevo",
				ReexecFailed: &store.ReexecFailure{Reason: "policy.json: unknown field"},
			},
			ok:           true,
			wantSeverity: SevWarn,
			wantDetail:   "runs v1; the relevo binary at /usr/local/bin/relevo failed preflight (policy.json: unknown field) and was not loaded",
			wantFix:      "fix the error above; the daemon retries when the file changes",
		},
		{
			name:         "version differs",
			info:         store.DaemonInfo{Version: "v1"},
			ok:           true,
			wantSeverity: SevOK,
			wantDetail:   "runs v1; switching to v2 within seconds",
		},
		{
			name:         "equal",
			info:         store.DaemonInfo{Version: "v2"},
			ok:           true,
			wantSeverity: SevOK,
			wantDetail:   "running v2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := &fakeEnv{daemonRunning: true, daemonInfo: tc.info, daemonInfoOK: tc.ok, releaseRunning: "v2"}
			c := findCheck(Run(context.Background(), env, nil), "", "daemon")
			if c == nil {
				t.Fatal("no daemon row in the report")
			}
			if c.Severity != tc.wantSeverity {
				t.Errorf("severity = %v, want %v", c.Severity, tc.wantSeverity)
			}
			if !strings.Contains(c.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", c.Detail, tc.wantDetail)
			}
			if tc.wantFix != "" && !strings.Contains(c.Fix, tc.wantFix) {
				t.Errorf("fix = %q, want it to contain %q", c.Fix, tc.wantFix)
			}
		})
	}
}

// TestDoctorDaemonRowsUnchanged pins the two rows #371 keeps byte-identical: a
// stopped daemon and a failed probe.
func TestDoctorDaemonRowsUnchanged(t *testing.T) {
	stopped := findCheck(Run(context.Background(), &fakeEnv{daemonRunning: false}, nil), "", "daemon")
	if stopped == nil {
		t.Fatal("no daemon row for a stopped daemon")
	}
	if stopped.Severity != SevWarn || stopped.Detail != "not running" || stopped.Fix != "relevo daemon" {
		t.Errorf("stopped daemon row = %+v, want Warn / not running / relevo daemon", *stopped)
	}

	failed := findCheck(Run(context.Background(), &fakeEnv{daemonErr: errors.New("nope")}, nil), "", "daemon")
	if failed == nil {
		t.Fatal("no daemon row for a failed probe")
	}
	if failed.Severity != SevWarn || failed.Detail != "probe error: nope" || failed.Fix != "relevo daemon" || !failed.ProbeFailed {
		t.Errorf("probe error row = %+v, want Warn / probe error: nope / relevo daemon / ProbeFailed", *failed)
	}
}

// TestCustomRoleRow pins §3.6: a definition in scope that relevo does not ship
// gets a row of its own -- SevWarn with the by-hand fix when the file is
// missing, SevOK with "(custom)" when it is there -- while a shipped
// definition beside it keeps its own row and its own `relevo config agents`
// fix.
func TestCustomRoleRow(t *testing.T) {
	const customPath = "/fake/home/.claude/agents/my-executor.md"

	t.Run("missing", func(t *testing.T) {
		env := newFakeEnvForKind(t, "claude")
		env.existingFiles = map[string]bool{}

		rep := Run(context.Background(), env, []string{"claude"},
			WithDefinitions(map[string][]string{"claude": {"my-executor", "plan-executor"}}))

		c := findCheck(rep, "claude", "my-executor")
		if c == nil {
			t.Fatal("a custom definition in scope needs a row")
		}
		if c.Severity != SevWarn {
			t.Errorf("missing custom row severity = %v, want SevWarn", c.Severity)
		}
		if !strings.Contains(c.Detail, "(custom)") {
			t.Errorf("detail = %q, want it marked custom", c.Detail)
		}
		wantFix := "install your agent definition at ~/.claude/agents/my-executor.md; relevo never installs a custom definition"
		if c.Fix != wantFix {
			t.Errorf("fix = %q, want %q", c.Fix, wantFix)
		}

		shipped := findCheck(rep, "claude", "plan-executor")
		if shipped == nil || shipped.Severity != SevWarn {
			t.Fatalf("shipped plan-executor row = %+v, want a missing-file warn", shipped)
		}
		if shipped.Fix != "relevo config agents --kind claude --role plan-executor" {
			t.Errorf("shipped fix = %q, want relevo config agents", shipped.Fix)
		}
		if strings.Contains(shipped.Detail, "(custom)") {
			t.Errorf("shipped detail = %q, want no custom marker", shipped.Detail)
		}
	})

	t.Run("present", func(t *testing.T) {
		env := newFakeEnvForKind(t, "claude")
		env.existingFiles = map[string]bool{customPath: true}

		rep := Run(context.Background(), env, []string{"claude"},
			WithDefinitions(map[string][]string{"claude": {"my-executor", "plan-executor"}}))

		c := findCheck(rep, "claude", "my-executor")
		if c == nil {
			t.Fatal("a custom definition in scope needs a row")
		}
		if c.Severity != SevOK || c.Detail != "~/.claude/agents/my-executor.md (custom)" {
			t.Errorf("row = %+v, want SevOK and the custom detail", *c)
		}
	})
}
