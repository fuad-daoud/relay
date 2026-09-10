package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/doctor"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestAssembleKinds(t *testing.T) {
	tempHome := t.TempDir()
	st := store.New(filepath.Join(tempHome, "store"))
	tbl := alias.DefaultTable()

	// Default table has agy, claude, opencode
	kinds, err := assembleKinds(tbl, st)
	if err != nil {
		t.Fatalf("assembleKinds: %v", err)
	}
	expected := []string{"agy", "claude", "opencode"}
	if len(kinds) != len(expected) {
		t.Fatalf("kinds len = %d, want %d: %v", len(kinds), len(expected), kinds)
	}
	for i, k := range expected {
		if kinds[i] != k {
			t.Errorf("kinds[%d] = %q, want %q", i, kinds[i], k)
		}
	}

	// Add a binding with a custom builder kind
	b := store.Binding{
		Name: "custom",
		CWD:  tempHome,
		Builder: store.Endpoint{
			Kind: "custom-kind",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("st.Save: %v", err)
	}

	kinds2, err := assembleKinds(tbl, st)
	if err != nil {
		t.Fatalf("assembleKinds: %v", err)
	}
	foundCustom := false
	for _, k := range kinds2 {
		if k == "custom-kind" {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Errorf("custom-kind not found in assembled kinds: %v", kinds2)
	}
}

func TestAssembleKindsSurfacesErrors(t *testing.T) {
	// A store pointing at a file (not a directory) causes st.List() to fail.
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "file-not-dir")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	st := store.New(filePath)
	tbl := alias.DefaultTable()

	kinds, err := assembleKinds(tbl, st)
	if err == nil {
		t.Error("assembleKinds should surface error from store.List(), got nil")
	}
	// ...and must still hand back what it did learn. A diagnostic that refuses
	// to diagnose because one of its own inputs is unreadable is worse than one
	// that reports the gap and checks the rest.
	if len(kinds) == 0 {
		t.Error("assembleKinds must still return the alias-derived kinds when the store is unreadable")
	}
}

// The unreadable-store gap is reported as a global row, positioned with the
// other global rows rather than after the per-kind blocks.
func TestInsertGlobalCheckKeepsRenderOrder(t *testing.T) {
	checks := []doctor.Check{
		{Name: "herdr", Severity: doctor.SevOK},
		{Name: "daemon", Severity: doctor.SevOK},
		{Group: "claude", Name: "binary", Severity: doctor.SevOK},
	}
	out := insertGlobalCheck(checks, doctor.Check{Name: "bindings", Severity: doctor.SevWarn, Detail: "could not list bindings"})
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4", len(out))
	}
	if out[2].Name != "bindings" {
		t.Errorf("inserted row at %d (%q), want index 2 -- after the last global row", 2, out[2].Name)
	}
	if out[3].Group != "claude" {
		t.Errorf("per-kind rows must stay after the globals, got %+v", out[3])
	}
}

func TestRenderReportVerdict(t *testing.T) {
	// Success case
	repOk := doctor.Report{
		Checks: []doctor.Check{
			{Group: "", Name: "herdr", Severity: doctor.SevOK, Detail: "0.9.0 (floor 0.8.2)"},
			{Group: "", Name: "daemon", Severity: doctor.SevOK, Detail: "running"},
			{Group: "claude", Name: "binary", Severity: doctor.SevOK, Detail: "/usr/bin/claude"},
			{Group: "claude", Name: "integration", Severity: doctor.SevWarn, Detail: "outdated (v8 < v9)", Fix: "herdr integration install claude"},
		},
		UsableBuilder: true,
	}

	var buf bytes.Buffer
	renderReport(&buf, repOk)
	out := buf.String()

	if !strings.Contains(out, "1 warning, 0 failures -- relay can run.") {
		t.Errorf("expected success footer, got: %s", out)
	}
	if !strings.Contains(out, "    fix: herdr integration install claude") {
		t.Errorf("expected indented fix line, got: %s", out)
	}

	// Failure case
	repFail := doctor.Report{
		Checks: []doctor.Check{
			{Group: "", Name: "herdr", Severity: doctor.SevFail, Detail: "not found"},
		},
		UsableBuilder: false,
	}

	buf.Reset()
	renderReport(&buf, repFail)
	outFail := buf.String()

	if !strings.Contains(outFail, "1 failure, 0 warnings -- no usable builder. Fix the failure above.") {
		t.Errorf("expected failure footer, got: %s", outFail)
	}

	// Middle case: no failures, !UsableBuilder (via erroring IntegrationStatus)
	envStub := &stubDoctorEnv{
		ver:       "0.9.0",
		intErr:    errors.New("timeout connecting to herdr"),
		daemonRun: true,
		lookPaths: map[string]string{"claude": "/usr/bin/claude"},
	}
	repUnverified := doctor.Run(context.Background(), envStub, []string{"claude"})
	if repUnverified.Failures() != 0 {
		t.Fatalf("expected 0 failures, got %d", repUnverified.Failures())
	}
	if repUnverified.UsableBuilder {
		t.Fatal("expected UsableBuilder = false when IntegrationStatus errors")
	}

	buf.Reset()
	renderReport(&buf, repUnverified)
	outUnverified := buf.String()
	if !strings.Contains(outUnverified, "could not establish a usable builder: no checked harness") {
		t.Errorf("expected 'could not establish a usable builder.', got: %s", outUnverified)
	}
}

type stubDoctorEnv struct {
	ver       string
	herdrErr  error
	intErr    error
	intStates map[string]herdr.IntegrationState
	daemonRun bool
	lookPaths map[string]string
	statErr   error // nil means every role file exists
}

func (s *stubDoctorEnv) HerdrVersion(ctx context.Context) (string, error) {
	return s.ver, s.herdrErr
}
func (s *stubDoctorEnv) IntegrationStatus(ctx context.Context) (map[string]herdr.IntegrationState, error) {
	if s.intStates != nil {
		return s.intStates, s.intErr
	}
	return nil, s.intErr
}
func (s *stubDoctorEnv) DaemonRunning(ctx context.Context) (bool, error) {
	return s.daemonRun, nil
}
func (s *stubDoctorEnv) LookPath(binary string) (string, error) {
	if p, ok := s.lookPaths[binary]; ok {
		return p, nil
	}
	return "", os.ErrNotExist
}
func (s *stubDoctorEnv) HomePath(rel string) (string, error) {
	return filepath.Join("/tmp", rel), nil
}
func (s *stubDoctorEnv) Stat(path string) error {
	return s.statErr
}

// ReadFile satisfies doctor.Env. These tests assert on severities and fix
// commands, not on role-file contents, so every file reads as empty -- which
// doctor must render as a role row with no model suffix.
func (s *stubDoctorEnv) ReadFile(path string) ([]byte, error) {
	return nil, nil
}

// A probe relay could not complete is not actionable and stays off the hot path.
// An actionable row in the same report must survive it -- the all-or-nothing
// filter this replaces dropped both, and its test could not tell the difference
// because the stub's Stat always succeeded.
func TestBindWarningLinesSkipsOnlyTheRowItCouldNotEstablish(t *testing.T) {
	env := &stubDoctorEnv{
		ver:       "0.9.0",
		intErr:    errors.New("connection reset by peer"),
		daemonRun: true,
		lookPaths: map[string]string{"claude": "/usr/bin/claude"},
		statErr:   os.ErrNotExist,
	}
	rep := doctor.Run(context.Background(), env, []string{"claude"})
	joined := strings.Join(bindWarningLines(rep), "\n")

	if strings.Contains(joined, "status unavailable") {
		t.Errorf("a row relay could not establish must not reach the hot path: %s", joined)
	}
	if !strings.Contains(joined, "plan-executor") {
		t.Errorf("an actionable row must survive a failed probe elsewhere: %s", joined)
	}
}

// The exception: a failed probe whose verdict is SevFail means relay cannot run.
// Silence would be the worst possible answer, so it prints anyway.
func TestBindWarningLinesReportsAFailureEvenWhenTheProbeFailed(t *testing.T) {
	env := &stubDoctorEnv{herdrErr: errors.New("exec: \"herdr\": not found"), daemonRun: true}
	rep := doctor.Run(context.Background(), env, []string{"claude"})
	joined := strings.Join(bindWarningLines(rep), "\n")
	if !strings.Contains(joined, "herdr") {
		t.Errorf("a SevFail probe failure must still be reported at bind: %s", joined)
	}
}

func TestBindWarningLinesSilentWhenNothingIsWrong(t *testing.T) {
	env := &stubDoctorEnv{
		ver:       "0.9.0",
		daemonRun: true,
		lookPaths: map[string]string{"claude": "/usr/bin/claude"},
		intStates: map[string]herdr.IntegrationState{"claude": {Installed: true, Detail: "current (v9)"}},
	}
	rep := doctor.Run(context.Background(), env, []string{"claude"})
	if lines := bindWarningLines(rep); len(lines) != 0 {
		t.Errorf("a healthy machine must yield zero lines, got: %v", lines)
	}
}

// Global rows reach bind too: a stopped daemon makes a binding exactly as inert
// as a missing integration, which is the failure #24 is about.
func TestBindWarningLinesIncludesGlobalRows(t *testing.T) {
	rep := doctor.Report{Checks: []doctor.Check{
		{Name: "daemon", Severity: doctor.SevWarn, Detail: "not running", Fix: "relay daemon"},
		{Group: "claude", Name: "integration", Severity: doctor.SevOK, Detail: "current"},
	}}
	joined := strings.Join(bindWarningLines(rep), "\n")
	if !strings.Contains(joined, "daemon not running") {
		t.Errorf("global rows must reach bind, got: %s", joined)
	}
}

// deadlineEnv records whether the context it was handed carried a deadline.
type deadlineEnv struct {
	stubDoctorEnv
	sawDeadline bool
}

func (e *deadlineEnv) HerdrVersion(ctx context.Context) (string, error) {
	_, ok := ctx.Deadline()
	e.sawDeadline = ok
	return e.stubDoctorEnv.HerdrVersion(ctx)
}

// The bind preflight must be bounded: the herdr client allows 30s per call, so
// an unbounded preflight can add a minute to `relay bind`.
func TestBindPreflightBoundsTheHotPath(t *testing.T) {
	env := &deadlineEnv{stubDoctorEnv: stubDoctorEnv{ver: "0.9.0", daemonRun: true}}
	bindPreflight(context.Background(), env, "claude", false)
	if !env.sawDeadline {
		t.Error("bindPreflight must hand doctor.Run a deadline-bounded context")
	}
}

// An adopted pane's integration row must survive a binary that is not on PATH:
// the user launched that agent themselves, but the integration still decides
// whether the round can ever be observed to finish. Fails if the call site stops
// passing `adopted` through.
func TestBindPreflightPassesAdoptedThrough(t *testing.T) {
	env := &stubDoctorEnv{
		ver:       "0.9.0",
		daemonRun: true,
		intStates: map[string]herdr.IntegrationState{"claude": {Installed: false}},
	}

	adopted := strings.Join(bindPreflight(context.Background(), env, "claude", true), "\n")
	if !strings.Contains(adopted, "integration") {
		t.Errorf("adopted preflight must report the integration despite no binary: %s", adopted)
	}
	if strings.Contains(adopted, "binary") {
		t.Errorf("adopted preflight must not mention the binary: %s", adopted)
	}

	normal := strings.Join(bindPreflight(context.Background(), env, "claude", false), "\n")
	if !strings.Contains(normal, "binary") {
		t.Errorf("non-adopted preflight must report the absent binary: %s", normal)
	}
	if strings.Contains(normal, "integration") {
		t.Errorf("a missing binary must still suppress the rest of that harness: %s", normal)
	}
}

func TestBindWarningAdoptedRealRunMissingBinary(t *testing.T) {
	envStub := &stubDoctorEnv{
		ver:       "0.9.0",
		daemonRun: true,
		lookPaths: map[string]string{}, // binary absent
		intStates: map[string]herdr.IntegrationState{
			"claude": {Installed: false, Detail: "not installed"},
		},
	}

	// Normal Run: binary absent suppresses integration row
	normalRep := doctor.Run(context.Background(), envStub, []string{"claude"})
	normalLines := bindWarningLines(normalRep)
	joinedNormal := strings.Join(normalLines, "\n")
	if !strings.Contains(joinedNormal, "binary") {
		t.Errorf("expected binary warning for normal bind, got: %s", joinedNormal)
	}

	// Adopted Run: integration row survives through real Run
	adoptedRep := doctor.Run(context.Background(), envStub, []string{"claude"}, doctor.WithAdopted(true))
	adoptedLines := bindWarningLines(adoptedRep)
	joinedAdopted := strings.Join(adoptedLines, "\n")
	if strings.Contains(joinedAdopted, "binary") {
		t.Errorf("adopted bind must not have binary warning: %s", joinedAdopted)
	}
	if !strings.Contains(joinedAdopted, "integration") {
		t.Errorf("adopted bind must have integration warning, got: %s", joinedAdopted)
	}
}

func TestBindWarningDaemonDownRealRun(t *testing.T) {
	envStub := &stubDoctorEnv{
		ver:       "0.9.0",
		daemonRun: false,
		lookPaths: map[string]string{"claude": "/usr/bin/claude"},
	}
	rep := doctor.Run(context.Background(), envStub, []string{"claude"})
	lines := bindWarningLines(rep)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "daemon not running") {
		t.Errorf("expected daemon warning when daemon is down, got: %s", joined)
	}
}
