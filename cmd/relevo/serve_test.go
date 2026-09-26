package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

func TestServeUsageOnNoArgs(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"serve"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "usage: relevo serve") {
		t.Errorf("expected usage on stderr, got %q", string(stderr))
	}
}

func TestServeGCWithoutAbandonedExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"serve", "gc"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "--abandoned") {
		t.Errorf("expected mention of --abandoned on stderr, got %q", string(stderr))
	}
}

func TestServeUnbindWithoutOwnerExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"serve", "unbind", "some-binding"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "--owner") {
		t.Errorf("expected mention of --owner on stderr, got %q", string(stderr))
	}
}

// TestShowStateWithoutOwnerExits2 is TestServeLogWithoutOwnerExits2's port
// to the new form (§8): `--state` names the serve root, so `show` refuses it
// without `--owner`, naming --owner, exit 2.
func TestShowStateWithoutOwnerExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"show", "some-binding", "--state", t.TempDir()})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "--owner") {
		t.Errorf("expected mention of --owner on stderr, got %q", string(stderr))
	}
}

func TestServeFlagDefaults(t *testing.T) {
	fs, sf := serveFlagSet()

	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}

	if sf.listen != ":7777" {
		t.Errorf("default listen = %q, want :7777", sf.listen)
	}
	if sf.state != "" {
		t.Errorf("default state = %q, want empty", sf.state)
	}
	if sf.interval != 2*time.Second {
		t.Errorf("default interval = %v, want 2s", sf.interval)
	}
	if sf.insecureHTTP != false {
		t.Errorf("default insecureHTTP = %v, want false", sf.insecureHTTP)
	}
	if sf.maxBundleBytes != 512<<20 {
		t.Errorf("default maxBundleBytes = %d, want %d", sf.maxBundleBytes, 512<<20)
	}
	if sf.maxBuilders != 0 {
		t.Errorf("default maxBuilders = %d, want 0 (policy/default)", sf.maxBuilders)
	}
}

// TestServeFlagMaxBuilders pins #285's flag: --max-builders parses into
// serveFlags.maxBuilders, which cmdServeRun assigns straight to
// serve.Config.MaxBuilders. This only exercises flag parsing -- no server
// starts, no harness, no systemd (this package's TestMain isolates HOME,
// XDG_CONFIG_HOME and XDG_STATE_HOME already).
func TestServeFlagMaxBuilders(t *testing.T) {
	fs, sf := serveFlagSet()

	if err := fs.Parse([]string{"--max-builders", "3"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if sf.maxBuilders != 3 {
		t.Errorf("maxBuilders = %d, want 3", sf.maxBuilders)
	}
}

// TestScopeFromPolicy pins scopeFromPolicy's defaults and overrides (#244,
// #216, #295): scopes are on unless the resolved block explicitly turns them
// off, a zero CPUWeight defaults to 100, and every other field -- the CPU
// quota included -- passes through.
func TestScopeFromPolicy(t *testing.T) {
	enabledFalse := false
	cases := map[string]struct {
		sc   *policy.ScopePolicy
		want *spawn.ScopeSpec
	}{
		"nil block defaults on": {
			sc:   nil,
			want: &spawn.ScopeSpec{CPUWeight: 100},
		},
		"enabled false is nil": {
			sc:   &policy.ScopePolicy{Enabled: &enabledFalse},
			want: nil,
		},
		"zero weight defaults to 100": {
			sc:   &policy.ScopePolicy{},
			want: &spawn.ScopeSpec{CPUWeight: 100},
		},
		"quota passes through": {
			sc:   &policy.ScopePolicy{CPUQuota: "200%"},
			want: &spawn.ScopeSpec{CPUWeight: 100, CPUQuota: "200%"},
		},
		"gate quota passes through": {
			sc:   &policy.ScopePolicy{CPUQuota: "200%", GateCPUQuota: "300%"},
			want: &spawn.ScopeSpec{CPUWeight: 100, CPUQuota: "200%", GateCPUQuota: "300%"},
		},
		"slice and limits pass through": {
			sc: &policy.ScopePolicy{
				Slice: "relevo.slice", CPUWeight: 200, CPUQuota: "200%", MemoryMax: "2G", TasksMax: 64,
			},
			want: &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 200, CPUQuota: "200%", MemoryMax: "2G", TasksMax: 64},
		},
		"allowed cpus passes through": {
			sc:   &policy.ScopePolicy{CPUQuota: "200%", AllowedCPUs: "0-2"},
			want: &spawn.ScopeSpec{CPUWeight: 100, CPUQuota: "200%", AllowedCPUs: "0-2"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := scopeFromPolicy(c.sc)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("scopeFromPolicy = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestScopeStatusText pins what the startup line says about the resolved
// scope (#285, #295) and the gate quota suffix (#313).
func TestScopeStatusText(t *testing.T) {
	cases := map[string]struct {
		sc   *spawn.ScopeSpec
		want string
	}{
		"nil is off":            {sc: nil, want: "off"},
		"bare is on":            {sc: &spawn.ScopeSpec{}, want: "on"},
		"quota":                 {sc: &spawn.ScopeSpec{CPUQuota: "200%"}, want: "on (200%)"},
		"slice":                 {sc: &spawn.ScopeSpec{Slice: "relevo.slice"}, want: "on (slice relevo.slice)"},
		"slice and quota":       {sc: &spawn.ScopeSpec{Slice: "relevo.slice", CPUQuota: "200%"}, want: "on (slice relevo.slice, 200%)"},
		"gate only":             {sc: &spawn.ScopeSpec{GateCPUQuota: "300%"}, want: "on (gate 300%)"},
		"quota and gate":        {sc: &spawn.ScopeSpec{CPUQuota: "200%", GateCPUQuota: "300%"}, want: "on (200%, gate 300%)"},
		"slice and gate":        {sc: &spawn.ScopeSpec{Slice: "relevo.slice", GateCPUQuota: "300%"}, want: "on (slice relevo.slice, gate 300%)"},
		"slice, quota and gate": {sc: &spawn.ScopeSpec{Slice: "relevo.slice", CPUQuota: "200%", GateCPUQuota: "300%"}, want: "on (slice relevo.slice, 200%, gate 300%)"},
		"cpus":                  {sc: &spawn.ScopeSpec{AllowedCPUs: "0-2"}, want: "on (cpus 0-2, one per round)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := scopeStatusText(c.sc); got != c.want {
				t.Errorf("scopeStatusText = %q, want %q", got, c.want)
			}
		})
	}
}

// TestServeTierRuntimeHasClock pins the #226 regression: cmdServeRun's
// startup runtime is only used to log the builder tier, but that chain
// (PickServedCandidate -> Gates) calls rt.Now(), so a runtime without a
// clock panics before relevo serve ever listens. It must build the runtime
// the way cmdServeRun does and call relevo.ServedBuilderTier on it -- no
// shell-outs, no harness, nothing outside t.TempDir().
func TestServeTierRuntimeHasClock(t *testing.T) {
	root := t.TempDir()

	candidatesJSON := `[{"harness":"claude","provider":"t","model":"m","roles":["builder"]}]`
	candidatesPath := filepath.Join(root, "candidates.json")
	if err := os.WriteFile(candidatesPath, []byte(candidatesJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	candidates, err := candidate.Load(candidatesPath)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	pol, err := policy.Load(filepath.Join(root, "policy.json"))
	if err != nil {
		t.Fatalf("policy.Load: %v", err)
	}

	rt := serveTierRuntime(candidates, pol, nil, root, nil)
	if rt.Now == nil {
		t.Fatal("serveTierRuntime returned a Runtime without a clock")
	}
	tier := relevo.ServedBuilderTier(rt) // this is the line that panicked in production
	if tier == "" {
		t.Fatal("expected a tier")
	}
}

// TestServeTierRuntimeCarriesTheRegistry pins that the runtime cmdServeRun
// logs the builder tier from carries the same registry the served rounds
// resolve through, so the log cannot name a tier the rounds do not run at.
func TestServeTierRuntimeCarriesTheRegistry(t *testing.T) {
	root := t.TempDir()
	candidatesJSON := `[{"harness":"claude","provider":"t","model":"m","roles":["builder"]}]`
	candidatesPath := filepath.Join(root, "candidates.json")
	if err := os.WriteFile(candidatesPath, []byte(candidatesJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	set, err := candidate.Load(candidatesPath)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	pol := policy.Policy{MaxTier: "yolo"}
	reg, err := roles.Build(nil, set, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	rt := serveTierRuntime(set, pol, reg, root, nil)
	if rt.Registry != reg {
		t.Fatal("serveTierRuntime dropped the registry: the startup log would resolve the tier without it")
	}
}

func TestServeAdminConfigHasRunnerAndClock(t *testing.T) {
	cfg := serveAdminConfig("/x", nil)
	if cfg.Root != "/x" {
		t.Errorf("Root = %q, want /x", cfg.Root)
	}
	if cfg.Runner == nil {
		t.Error("Runner is nil")
	}
	if cfg.Now == nil {
		t.Error("Now is nil")
	}
}

// TestServeUIRefusesUninitialisedRoot: relevo serve ui resolves its root
// like the other admin verbs, so an uninitialised --state dir fails before
// any tty check (CI-safe: no tty, no harness) and creates nothing.
func TestServeUIRefusesUninitialisedRoot(t *testing.T) {
	dir := t.TempDir()
	err := cmdServeUI([]string{"--state", dir})
	if err == nil {
		t.Fatal("cmdServeUI error = nil, want an uninitialised-root error")
	}
	if !strings.Contains(err.Error(), "no serve state at ") {
		t.Errorf("error = %q, want no serve state message", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "serve")); !os.IsNotExist(statErr) {
		t.Errorf("serve dir exists or stat failed: %v", statErr)
	}
}

func TestServeStatusRefusesUninitialisedRoot(t *testing.T) {
	dir := t.TempDir()
	err := cmdServeStatus([]string{"--state", dir})
	if err == nil {
		t.Fatal("cmdServeStatus error = nil, want an uninitialised-root error")
	}
	if !strings.Contains(err.Error(), "no serve state at ") {
		t.Errorf("error = %q, want no serve state message", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "serve")) {
		t.Errorf("error = %q, want root %q", err, filepath.Join(dir, "serve"))
	}
	if _, statErr := os.Stat(filepath.Join(dir, "serve", "tmp")); !os.IsNotExist(statErr) {
		t.Errorf("serve/tmp exists or stat failed: %v", statErr)
	}
}

// TestGateServeRefusesUninitialisedRoot: `relevo gate --serve` resolves its
// root like the other admin verbs, so an uninitialised --state dir fails at
// adminRoot before anything else can run. CI-safe: it reaches no harness.
func TestGateServeRefusesUninitialisedRoot(t *testing.T) {
	dir := t.TempDir()
	_, _, runErr := captureOutput(t, func() error {
		return run([]string{"gate", "--serve", "--state", dir})
	})
	if runErr == nil {
		t.Fatal("run error = nil, want an uninitialised-root error")
	}
	if !strings.Contains(runErr.Error(), "no serve state at ") {
		t.Errorf("error = %q, want no serve state message", runErr)
	}
	if !strings.Contains(runErr.Error(), filepath.Join(dir, "serve")) {
		t.Errorf("error = %q, want root %q", runErr, filepath.Join(dir, "serve"))
	}
	if _, statErr := os.Stat(filepath.Join(dir, "serve", "tmp")); !os.IsNotExist(statErr) {
		t.Errorf("serve/tmp exists or stat failed: %v", statErr)
	}
}

// TestServeGateSubverbsWereRemoved pins D1: `serve gates`, `serve available`
// and `serve unavailable` exit 2, each naming `relevo gate --serve`.
func TestServeGateSubverbsWereRemoved(t *testing.T) {
	for _, sub := range []string{"gates", "available", "unavailable"} {
		t.Run(sub, func(t *testing.T) {
			stdout, stderr, runErr := captureOutput(t, func() error {
				return run([]string{"serve", sub})
			})
			var ec exitCodeErr
			if !errors.As(runErr, &ec) || ec.code != 2 {
				t.Fatalf("run = %v, want exit code 2", runErr)
			}
			if len(stdout) != 0 {
				t.Errorf("expected nothing on stdout, got %q", string(stdout))
			}
			if !strings.Contains(string(stderr), "relevo gate --serve") {
				t.Errorf("stderr = %q, want it to name relevo gate --serve", stderr)
			}
		})
	}
}

// TestServeReadSubverbsWereRemoved pins §4.3: `serve log`, `serve show` and
// `serve tab` exit 2, each naming the form that replaces it.
func TestServeReadSubverbsWereRemoved(t *testing.T) {
	cases := []struct {
		sub  string
		want string
	}{
		{"log", "relevo show <name> --owner <label> --log"},
		{"show", "relevo show <name> --owner <label>"},
		{"tab", "relevo history --tab --owner <label|all>"},
	}
	for _, c := range cases {
		t.Run(c.sub, func(t *testing.T) {
			stdout, stderr, runErr := captureOutput(t, func() error {
				return run([]string{"serve", c.sub})
			})
			var ec exitCodeErr
			if !errors.As(runErr, &ec) || ec.code != 2 {
				t.Fatalf("run = %v, want exit code 2", runErr)
			}
			if len(stdout) != 0 {
				t.Errorf("expected nothing on stdout, got %q", string(stdout))
			}
			if !strings.Contains(string(stderr), c.want) {
				t.Errorf("stderr = %q, want it to name %q", stderr, c.want)
			}
		})
	}
}

func TestServeFlagAfterPositionalIsHonoured(t *testing.T) {
	dir := t.TempDir()
	_, _, runErr := captureOutput(t, func() error {
		return run([]string{"gate", "--serve", "sometoken", "--state", dir})
	})
	if runErr == nil {
		t.Fatal("run error = nil, want an uninitialised-root error")
	}
	if !strings.Contains(runErr.Error(), dir) {
		t.Errorf("error = %q, want it to contain %q", runErr, dir)
	}
}
