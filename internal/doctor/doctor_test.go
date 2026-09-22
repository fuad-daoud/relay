package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/classify"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/release"
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
	daemonRunning bool
	daemonErr     error
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

// newFakeEnvForKind is a fakeEnv where everything except the role files is
// healthy, so a test can vary existingFiles and fileContents alone.
// agyEnv is an agy machine in good order: binary on PATH, integration
// installed, three role files present and pinning inherit. Tests perturb
// one thing at a time from here.
// Step 3 (#91/#94), narrowed in round 2: relay doctor warns when an
// installed definition differs from the shipped one only on a kind whose
// definition relay owns outright (Role.ExpectModel set -- today, agy).
// These four subtests exercise that on agy; the fifth exercises the
// opposite on claude, which the gate in doctor.roleCheck excludes.
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
		rep := Run(context.Background(), env, nil, WithUsage("/cfg/prices.json", true))
		c, ok := findUsageCheck(rep, "sqlite3", "")
		if !ok || c.Severity != SevWarn {
			t.Errorf("want a warn row for sqlite3: %+v", rep.Checks)
		}
		if !strings.Contains(c.Detail, "unknown") {
			t.Errorf("Detail = %q, want it to say opencode pane rounds record unknown", c.Detail)
		}
	})
	t.Run("sqlite3 not needed without opencode", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
		rep := Run(context.Background(), env, nil, WithUsage("/cfg/prices.json", false))
		if _, ok := findUsageCheck(rep, "sqlite3", ""); ok {
			t.Error("no opencode candidate: no sqlite3 row")
		}
	})
	t.Run("prices absent is ok, stale is warn, malformed is warn", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}, fileContents: map[string]string{}}
		rep := Run(context.Background(), env, nil, WithUsage("/cfg/prices.json", false))
		if c, ok := findUsageCheck(rep, "prices", "default"); !ok || c.Severity != SevOK {
			t.Errorf("absent prices.json: want an OK row naming the default: %+v", rep.Checks)
		}
		env.existingFiles["/cfg/prices.json"] = true
		env.fileContents["/cfg/prices.json"] = `{"as_of":"2020-01-01","models":{}}`
		rep = Run(context.Background(), env, nil, WithUsage("/cfg/prices.json", false))
		if c, ok := findUsageCheck(rep, "prices", "2020-01-01"); !ok || c.Severity != SevWarn {
			t.Errorf("stale as_of: want a warn row: %+v", rep.Checks)
		}
		env.fileContents["/cfg/prices.json"] = `{"models": 5}`
		rep = Run(context.Background(), env, nil, WithUsage("/cfg/prices.json", false))
		if c, ok := findUsageCheck(rep, "prices", "does not validate"); !ok || c.Severity != SevWarn {
			t.Errorf("malformed: want a warn row: %+v", rep.Checks)
		}
	})
}

// TestExtraChecksAppended checks that WithExtraChecks appends its checks
// verbatim at the end of the report, after every check Run itself built --
// including the usage checks WithUsage enables (#100 step 5: cmd/relay
// folds relay.ProbeServers' server rows in this way, without Run knowing
// anything about servers.json or the network).
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

	t.Run("configured with file key", func(t *testing.T) {
		st := classify.Status{
			Configured: true,
			Model:      "jev-custom",
			KeySource:  "file",
			KeyPath:    "/home/user/.config/relay/typesafe.key",
		}
		c := ClassifyCheck(st)
		if c.Name != "classify" {
			t.Errorf("Name = %q, want classify", c.Name)
		}
		if c.Severity != SevOK {
			t.Errorf("Severity = %v, want SevOK", c.Severity)
		}
		if !strings.Contains(c.Detail, "jev-custom; key from /home/user/.config/relay/typesafe.key") {
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
			KeyPath:    "/home/user/.config/relay/typesafe.key",
		}
		c := ClassifyCheck(st)
		if c.Name != "classify" {
			t.Errorf("Name = %q, want classify", c.Name)
		}
		if c.Severity != SevWarn {
			t.Errorf("Severity = %v, want SevWarn", c.Severity)
		}
		if !strings.Contains(c.Detail, "jev-latest configured but no key found; the daemon falls back to regex") {
			t.Errorf("Detail = %q", c.Detail)
		}
		if !strings.Contains(c.Fix, "set TYPESAFE_API_KEY for the daemon, or write the key to /home/user/.config/relay/typesafe.key (chmod 600)") {
			t.Errorf("Fix = %q", c.Fix)
		}
	})

	t.Run("configured with loose key file", func(t *testing.T) {
		st := classify.Status{
			Configured:   true,
			Model:        "jev-latest",
			KeySource:    "",
			KeyPath:      "/home/user/.config/relay/typesafe.key",
			KeyFileLoose: true,
			KeyFileMode:  0o644,
		}
		c := ClassifyCheck(st)
		if c.Name != "classify" {
			t.Errorf("Name = %q, want classify", c.Name)
		}
		if c.Severity != SevWarn {
			t.Errorf("Severity = %v, want SevWarn", c.Severity)
		}
		if !strings.Contains(c.Detail, "readable by others") || !strings.Contains(c.Detail, "0644") {
			t.Errorf("Detail = %q", c.Detail)
		}
		if !strings.HasPrefix(c.Fix, "chmod 600 ") {
			t.Errorf("Fix = %q, want prefix 'chmod 600 '", c.Fix)
		}
	})
}

// TestOpencodeAllowlistRow checks the wiring (#236): the opencode branch grows
// the external_directory row when a state root is supplied, and no other kind
// does. Dropping the kind check in Run makes the second half fail.
// TestDoctorReleaseCheck walks §4.5's table through fakeEnv: every row, the
// Fix for each install variant, and the standing promise that a stale relay
// is a warning, never a failure.
