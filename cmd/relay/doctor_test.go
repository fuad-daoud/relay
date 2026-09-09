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
	kinds := assembleKinds(tbl, st)
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

	kinds2 := assembleKinds(tbl, st)
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
	if !strings.Contains(outUnverified, "could not establish a usable builder.") {
		t.Errorf("expected 'could not establish a usable builder.', got: %s", outUnverified)
	}
}

type stubDoctorEnv struct {
	ver       string
	intErr    error
	daemonRun bool
	lookPaths map[string]string
}

func (s *stubDoctorEnv) HerdrVersion(ctx context.Context) (string, error) {
	return s.ver, nil
}
func (s *stubDoctorEnv) IntegrationStatus(ctx context.Context) (map[string]herdr.IntegrationState, error) {
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
	return nil
}

func TestBindWarningLines(t *testing.T) {
	// 1. Probe error yields zero lines
	repProbeErr := doctor.Report{
		Checks: []doctor.Check{
			{Group: "claude", Name: "integration", Severity: doctor.SevWarn, Detail: "integration status unavailable: timeout"},
		},
	}
	lines := bindWarningLines(repProbeErr, false)
	if len(lines) != 0 {
		t.Errorf("probe error must yield zero lines, got: %v", lines)
	}

	// 2. Adopted bind yields only integration row
	repMulti := doctor.Report{
		Checks: []doctor.Check{
			{Group: "claude", Name: "binary", Severity: doctor.SevWarn, Detail: "not on PATH -- skipping the rest of this harness"},
			{Group: "claude", Name: "integration", Severity: doctor.SevFail, Detail: "not installed -- this binding will report `unknown` forever and never finish a round", Fix: "herdr integration install claude"},
			{Group: "claude", Name: "plan-executor", Severity: doctor.SevWarn, Detail: "missing: ~/.claude/agents/plan-executor.md", Fix: "relay agent print --kind claude > ~/.claude/agents/plan-executor.md"},
		},
	}
	adoptedLines := bindWarningLines(repMulti, true)
	if len(adoptedLines) == 0 {
		t.Fatal("adopted bind should yield warning lines for non-OK integration")
	}
	joinedAdopted := strings.Join(adoptedLines, "\n")
	if !strings.Contains(joinedAdopted, "integration not installed") {
		t.Errorf("adopted bind missing integration warning: %s", joinedAdopted)
	}
	if strings.Contains(joinedAdopted, "binary") {
		t.Errorf("adopted bind must not include binary check: %s", joinedAdopted)
	}
	if strings.Contains(joinedAdopted, "plan-executor") {
		t.Errorf("adopted bind must not include plan-executor check: %s", joinedAdopted)
	}

	// 3. Non-adopted bind yields all warning rows
	normalLines := bindWarningLines(repMulti, false)
	joinedNormal := strings.Join(normalLines, "\n")
	if !strings.Contains(joinedNormal, "binary") {
		t.Errorf("normal bind missing binary warning: %s", joinedNormal)
	}
	if !strings.Contains(joinedNormal, "integration") {
		t.Errorf("normal bind missing integration warning: %s", joinedNormal)
	}
	if !strings.Contains(joinedNormal, "plan-executor") {
		t.Errorf("normal bind missing plan-executor warning: %s", joinedNormal)
	}

	// 4. All OK yields zero lines
	repAllOk := doctor.Report{
		Checks: []doctor.Check{
			{Group: "claude", Name: "binary", Severity: doctor.SevOK, Detail: "/usr/bin/claude"},
			{Group: "claude", Name: "integration", Severity: doctor.SevOK, Detail: "current"},
			{Group: "claude", Name: "plan-executor", Severity: doctor.SevOK, Detail: "~/.claude/agents/plan-executor.md"},
		},
	}
	if okLines := bindWarningLines(repAllOk, false); len(okLines) != 0 {
		t.Errorf("all OK must yield zero lines, got: %v", okLines)
	}
}
