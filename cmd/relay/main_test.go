package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/release"
	"github.com/fuad-daoud/relay/internal/store"
)

// TestMain points the whole package at a fresh temp root: HOME,
// XDG_CONFIG_HOME and XDG_STATE_HOME all move here, so no test in cmd/relay
// reads the user's real config or state (#235). Tests that t.Setenv the same
// variables keep working: t.Setenv restores to these values.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "relay-cmd-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", root)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}

// TestHelpListsServeVerbs pins that the top-level usage's `serve` line names
// all the server administration verbs, not just the original eight: `relay
// serve ui`, `gates`, `available` and `unavailable` exist in
// cmd/relay/serve.go's sub-usage but were missing here. `relay help` only
// prints a constant string, so this reaches no herdr and touches no state.
func TestHelpListsServeVerbs(t *testing.T) {
	stdout, _, runErr := captureOutput(t, func() error {
		return run([]string{"help"})
	})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if !strings.Contains(string(stdout), "gates") {
		t.Errorf("expected the top-level usage to mention gates, got %q", string(stdout))
	}
}

// TestBindResumeWithoutNameIsRejected covers a flag shape that reads fine and
// silently does the wrong thing: --resume is a bool and the name comes from
// --name, so `relay bind --resume webshop` drops the positional and the binding
// lookup then fails on the empty name ("relay: : binding not found").
//
// The check runs before any runtime is built, so this test touches neither
// the state directory nor herdr.
func TestBindResumeWithoutNameIsRejected(t *testing.T) {
	err := run([]string{"bind", "--resume", "webshop"})
	if err == nil {
		t.Fatal("relay bind --resume with a positional name must be rejected")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("error must point at --name, got %q", err)
	}
}

// TestSendDryRunRequiresFile pins that `relay send --dry-run` without a plan
// file is refused before a runtime is built, so a CI runner with no herdr
// still fails on the missing flag rather than on the environment.
func TestSendDryRunRequiresFile(t *testing.T) {
	err := run([]string{"send", "--dry-run", "--name", "x"})
	if err == nil {
		t.Fatal("relay send --dry-run without --file must be rejected")
	}
	if !strings.Contains(err.Error(), "--file") {
		t.Errorf("error must point at --file, got %q", err)
	}
}

// TestAskRoundNeedsAQuestion pins that `relay ask --round` without a question
// is refused before a runtime is built: a round ask takes --file or -q, and a
// CI runner with no herdr must fail on the missing flag, not on the
// environment.
func TestAskRoundNeedsAQuestion(t *testing.T) {
	err := run([]string{"ask", "--round", "1", "x"})
	if err == nil {
		t.Fatal("relay ask --round without a question must be rejected")
	}
	if !strings.Contains(err.Error(), "--file or -q") {
		t.Errorf("error must point at --file or -q, got %q", err)
	}
}

// TestSendRegateNegativeIsRejected pins #132 part 2's flag validation. The
// flag's -1 default means "not given", so a negative value the human typed is
// a bad value, not an omission: it exits 2, and the check runs before any
// runtime is built, so this touches neither the state directory nor herdr.
func TestSendRegateNegativeIsRejected(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"send", "--regate", "-1", "--file", "plan.md"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "--regate") {
		t.Errorf("expected the error to name --regate, got %q", string(stderr))
	}
}

// TestSendVerifyAndNoVerifyAreExclusive pins #144's flag pair: like add's
// --branch/--cwd, the refusal happens in validation, before newRuntime, so it
// reaches neither the state directory nor herdr.
func TestSendVerifyAndNoVerifyAreExclusive(t *testing.T) {
	_, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"send", "--verify", "--no-verify", "--file", "plan.md"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if !strings.Contains(string(stderr), "--verify") || !strings.Contains(string(stderr), "--no-verify") {
		t.Errorf("expected the error to name both flags, got %q", string(stderr))
	}
}

func TestPauseRefusesToGuessTheBinding(t *testing.T) {
	err := run([]string{"pause"})
	if err == nil {
		t.Fatal("relay pause with no binding must be refused")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("error must point at --name, got %q", err)
	}
}

func TestStopRefusesToGuessTheBinding(t *testing.T) {
	err := run([]string{"stop"})
	if err == nil {
		t.Fatal("relay stop with no binding must be refused")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("error must point at --name, got %q", err)
	}
}

// TestLandRefusesToGuessTheBinding pins #136: land pushes, so a bare `relay
// land` must refuse rather than act on whichever binding owns the cwd. The
// check runs before any runtime is built, so this test touches neither the
// state directory nor herdr, and a CI runner with no herdr still fails on the
// missing name, not on the environment.
func TestLandRefusesToGuessTheBinding(t *testing.T) {
	err := run([]string{"land"})
	if err == nil {
		t.Fatal("a bare relay land must be refused")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Fatalf("error must point at --name, got %q", err)
	}
	if !strings.Contains(err.Error(), "usage: relay land") {
		t.Fatalf("expected the usage line, got %v", err)
	}
}

// TestStopGraceMustBePositive pins #138's flag validation: --grace <= 0 is a
// bad value, not an omission, so it exits 2. The check runs before any runtime
// is built, so this touches neither the state directory nor herdr.
func TestExplicitBindingNeverGuesses(t *testing.T) {
	cases := []struct {
		name       string
		flag       string
		positional []string
		want       string
		ok         bool
	}{
		{"flag only", "ai", nil, "ai", true},
		{"positional only", "", []string{"ai"}, "ai", true},
		{"bare invocation is refused", "", nil, "", false},
		{"both at once is refused", "ai", []string{"ai"}, "", false},
		{"two positionals refused", "", []string{"ai", "webshop"}, "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := explicitBinding(c.flag, c.positional)
			if ok != c.ok || got != c.want {
				t.Errorf("explicitBinding(%q, %v) = (%q, %v), want (%q, %v)",
					c.flag, c.positional, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestDiffHelp(t *testing.T) {
	err := run([]string{"diff", "-h"})
	if !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestDiffCommand(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))

	repoDir := t.TempDir()
	// Init git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "file.txt")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// -c commit.gpgsign=false so a developer with signing enabled globally
	// does not have this fixture commit reach gpg; see internal/git's runGit.
	cmd = exec.Command("git", "-c", "user.name=T", "-c", "user.email=t@e",
		"-c", "commit.gpgsign=false", "commit", "-m", "init")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	patchContent := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,2 @@\n hello\n+world\n"

	s := store.New(filepath.Join(tempHome, ".local", "state", "relay"))
	b := store.Binding{
		Name:  "webshop",
		CWD:   repoDir,
		Round: 2, // Round 1 completed
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}

	// Write round 1 diff patch
	if err := os.WriteFile(s.DiffPath("webshop", 1), []byte(patchContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add log entries for round 1
	if err := s.AppendLog("webshop", store.LogEntry{
		Round:     1,
		Direction: store.DirToPlanner,
		Kind:      store.KindDiff,
		Note:      "1 file, +1 -0",
		Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Test default round output (round 1)
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := run([]string{"diff", "--name", "webshop"})

	w.Close()
	os.Stdout = origStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("run diff: %v", runErr)
	}
	if string(out) != patchContent {
		t.Fatalf("got stdout %q, want %q", string(out), patchContent)
	}

	// Verify patch reaches stdout byte-for-byte by piping into git apply --check
	applyCmd := exec.Command("git", "apply", "--check")
	applyCmd.Dir = repoDir
	applyCmd.Stdin = strings.NewReader(string(out))
	if applyOut, err := applyCmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply --check failed: %v\nOutput: %s", err, string(applyOut))
	}

	// #143: a successful `diff` stamps the binding's .viewed sidecar.
	// Store-only -- reaches no herdr.
	if _, ok := s.ViewedAt("webshop"); !ok {
		t.Fatal("diff must stamp .viewed on a successful print")
	}

	// Test --stat flag
	r2, w2, _ := os.Pipe()
	os.Stdout = w2
	runErr = run([]string{"diff", "--name", "webshop", "--stat"})
	w2.Close()
	os.Stdout = origStdout
	outStat, _ := io.ReadAll(r2)
	if runErr != nil {
		t.Fatalf("run diff --stat: %v", runErr)
	}
	if strings.TrimSpace(string(outStat)) != "1 file, +1 -0" {
		t.Fatalf("got stat %q, want %q", strings.TrimSpace(string(outStat)), "1 file, +1 -0")
	}

	// Test unknown round errors naming binding and round
	errUnknown := run([]string{"diff", "--name", "webshop", "--round", "99"})
	if errUnknown == nil {
		t.Fatal("expected error for unknown round")
	}
	if !strings.Contains(errUnknown.Error(), "99") || !strings.Contains(errUnknown.Error(), "webshop") {
		t.Fatalf("error %q must name round and binding", errUnknown.Error())
	}

	// Test binding with no completed round yet
	b.Round = 1
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	errNoCompleted := run([]string{"diff", "--name", "webshop"})
	if errNoCompleted == nil {
		t.Fatal("expected error for binding with no completed round")
	}
	if !strings.Contains(errNoCompleted.Error(), "no completed round yet") || !strings.Contains(errNoCompleted.Error(), "webshop") {
		t.Fatalf("error %q must name binding and explain no completed round", errNoCompleted.Error())
	}
}

// TestDiffAnchorsCommand pins `relay diff --anchors`: it store-only seeds a
// binding and a round 1 diff the way TestDiffCommand does, so it reaches no
// herdr, and asserts the printed patch carries the path:line gutter
// internal/patch's Annotate produces.
func TestDiffAnchorsCommand(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))

	patchContent := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,2 @@\n hello\n+world\n"

	s := store.New(filepath.Join(tempHome, ".local", "state", "relay"))
	b := store.Binding{
		Name:  "webshop",
		CWD:   "/repo",
		Round: 2, // Round 1 completed
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.DiffPath("webshop", 1), []byte(patchContent), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, runErr := captureOutput(t, func() error {
		return run([]string{"diff", "--name", "webshop", "--anchors"})
	})
	if runErr != nil {
		t.Fatalf("run diff --anchors: %v", runErr)
	}

	want := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\nfile.txt:1  @@ -1 +1,2 @@\nfile.txt:1  hello\nfile.txt:2 +world\n"
	if string(stdout) != want {
		t.Fatalf("got stdout %q, want %q", string(stdout), want)
	}
}

// TestReviewRequiresFile pins that `relay review` without --file is refused
// before a runtime is built, so a CI runner with no herdr still fails on the
// missing flag rather than on the environment.
func TestReviewRequiresFile(t *testing.T) {
	err := run([]string{"review", "--name", "webshop"})
	if err == nil {
		t.Fatal("relay review without --file must be rejected")
	}
	if !strings.Contains(err.Error(), "--file") {
		t.Errorf("error must point at --file, got %q", err)
	}
}

func TestDiffDriftCommand(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))

	repoDir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	driftPatchRound2 := "diff --git a/drift.txt b/drift.txt\n--- a/drift.txt\n+++ b/drift.txt\n@@ -1 +1,2 @@\n drift\n+round2\n"
	driftPatchRound1 := "diff --git a/drift.txt b/drift.txt\n--- a/drift.txt\n+++ b/drift.txt\n@@ -1 +1,2 @@\n drift\n+round1\n"

	s := store.New(filepath.Join(tempHome, ".local", "state", "relay"))
	b := store.Binding{
		Name:  "webshop",
		CWD:   repoDir,
		Round: 2,
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}

	// Write round 2 drift patch
	if err := os.WriteFile(s.DriftPath("webshop", 2), []byte(driftPatchRound2), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write round 1 drift patch
	if err := os.WriteFile(s.DriftPath("webshop", 1), []byte(driftPatchRound1), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add KindDrift log entry for round 2
	if err := s.AppendLog("webshop", store.LogEntry{
		Round:     2,
		Direction: store.DirToPlanner,
		Kind:      store.KindDrift,
		Note:      "1 file, +5 -1",
		Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. --drift defaults to the current round (round 2)
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := run([]string{"diff", "--name", "webshop", "--drift"})

	w.Close()
	os.Stdout = origStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("run diff --drift: %v", runErr)
	}
	if string(out) != driftPatchRound2 {
		t.Fatalf("got stdout %q, want round 2 drift %q", string(out), driftPatchRound2)
	}

	// 2. --round overrides (requests round 1)
	rRound, wRound, _ := os.Pipe()
	os.Stdout = wRound
	runErr = run([]string{"diff", "--name", "webshop", "--drift", "--round", "1"})
	wRound.Close()
	os.Stdout = origStdout
	outRound, _ := io.ReadAll(rRound)
	if runErr != nil {
		t.Fatalf("run diff --drift --round 1: %v", runErr)
	}
	if string(outRound) != driftPatchRound1 {
		t.Fatalf("got stdout %q, want round 1 drift %q", string(outRound), driftPatchRound1)
	}

	// 3. --stat prints the KindDrift Note
	rStat, wStat, _ := os.Pipe()
	os.Stdout = wStat
	runErr = run([]string{"diff", "--name", "webshop", "--drift", "--stat"})
	wStat.Close()
	os.Stdout = origStdout
	outStat, _ := io.ReadAll(rStat)
	if runErr != nil {
		t.Fatalf("run diff --drift --stat: %v", runErr)
	}
	if strings.TrimSpace(string(outStat)) != "1 file, +5 -1" {
		t.Fatalf("got stat %q, want %q", strings.TrimSpace(string(outStat)), "1 file, +5 -1")
	}

	// 4. a round with no drift errors mentioning --drift
	errNoDrift := run([]string{"diff", "--name", "webshop", "--drift", "--round", "99"})
	if errNoDrift == nil {
		t.Fatal("expected error for round with no drift")
	}
	if !strings.Contains(errNoDrift.Error(), "--drift") || !strings.Contains(errNoDrift.Error(), "99") || !strings.Contains(errNoDrift.Error(), "webshop") {
		t.Fatalf("error %q must name --drift, round 99, and webshop", errNoDrift.Error())
	}

	errNoDriftStat := run([]string{"diff", "--name", "webshop", "--drift", "--stat", "--round", "99"})
	if errNoDriftStat == nil {
		t.Fatal("expected error for --stat on round with no drift")
	}
	if !strings.Contains(errNoDriftStat.Error(), "--drift") || !strings.Contains(errNoDriftStat.Error(), "99") || !strings.Contains(errNoDriftStat.Error(), "webshop") {
		t.Fatalf("error %q must name --drift, round 99, and webshop", errNoDriftStat.Error())
	}
}

func TestForkHelp(t *testing.T) {
	err := run([]string{"fork", "-h"})
	if !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestForkValidation(t *testing.T) {
	// Each assertion names the specific validation being exercised. The usage
	// line lists every flag, so asserting on a bare "--round" would pass on the
	// usage string alone -- which is exactly how these cases passed while never
	// reaching the validation they claim to cover (#48).

	// Missing source
	err := run([]string{"fork", "--round", "1", "--new-name", "fork-1"})
	if err == nil || !strings.Contains(err.Error(), "needs the source binding name") {
		t.Fatalf("expected the refuse-to-guess error, got %v", err)
	}

	// Missing --round
	err = run([]string{"fork", "src", "--new-name", "fork-1"})
	if err == nil || !strings.Contains(err.Error(), "relay fork requires --round N") {
		t.Fatalf("expected the --round validation, got %v", err)
	}

	// Missing --new-name
	err = run([]string{"fork", "src", "--round", "1"})
	if err == nil || !strings.Contains(err.Error(), "relay fork requires --new-name NAME") {
		t.Fatalf("expected the --new-name validation, got %v", err)
	}
}

// TestCandidatesConfigHome pins #42: relay config is composed through
// userConfigRoot(), so XDG_CONFIG_HOME decides where candidates.json is read.
func TestCandidatesConfigHome(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	relayDir := filepath.Join(configHome, "relay")
	if err := os.MkdirAll(relayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `[{"harness":"claude","provider":"test","model":"m","roles":["builder"]}]`
	if err := os.WriteFile(filepath.Join(relayDir, "candidates.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if _, err := rt.Candidates.Lookup(candidate.Ref{Harness: "claude", Provider: "test", Model: "m"}); err != nil {
		t.Fatalf("rt.Candidates.Lookup: %v", err)
	}
}

// TestDiffHonoursConfigHome pins #235: cmdDiff's newRuntime() reads
// candidates.json through userConfigRoot(), so XDG_CONFIG_HOME decides which
// file it reads. A candidates file naming a harness the branch does not know
// must make `relay diff` fail with that harness, exactly the way the two diff
// tests used to fail when they picked the file up from the real home.
func TestDiffHonoursConfigHome(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	relayDir := filepath.Join(configHome, "relay")
	if err := os.MkdirAll(relayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `[{"harness":"nonesuch","provider":"p","model":"m","roles":["builder"]}]`
	if err := os.WriteFile(filepath.Join(relayDir, "candidates.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"diff", "--name", "anything"})
	if err == nil {
		t.Fatal("diff must fail when XDG_CONFIG_HOME names an unknown harness")
	}
	if !strings.Contains(err.Error(), `unknown harness "nonesuch"`) {
		t.Fatalf("diff must read candidates from XDG_CONFIG_HOME; got %v", err)
	}
}

func TestHooksConfigHome(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	cfg, err := resolveHooksConfig()
	if err != nil {
		t.Fatalf("resolveHooksConfig: %v", err)
	}
	wantHooksDir := filepath.Join(configHome, "relay", "hooks")
	if cfg.HooksDir != wantHooksDir {
		t.Fatalf("got HooksDir %q, want %q", cfg.HooksDir, wantHooksDir)
	}
}

// --assume-dead must be defined on the bind flag set. If it were not, parsing
// would fail with "flag provided but not defined" and never reach the --name
// check -- which runs before any runtime is built, so this test touches
// neither the state directory nor herdr.
func TestAddHelp(t *testing.T) {
	err := run([]string{"add", "-h"})
	if !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestAddValidation(t *testing.T) {
	// Missing --name
	err := run([]string{"add", "--builder", "claude/test/m"})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("expected an error about --name, got %v", err)
	}
}

// TestAddBranchWithCwdIsRefusedBeforeRuntime pins the flag-pair refusal: it
// happens in validation, before newRuntime, so it reaches no herdr.
func TestAddBranchWithCwdIsRefusedBeforeRuntime(t *testing.T) {
	err := run([]string{"add", "--branch", "x", "--cwd", "/tmp"})
	if err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("expected an 'exclusive' refusal, got %v", err)
	}
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", err)
	}
}

// TestAddBranchDerivesName pins that a branch alone is enough for the name to
// be derived: with no relay planner for this session the run stops on the
// no-planner error, before any herdr call, so the derived name is never
// printed and no builder is reached.
func TestAddBranchDerivesName(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("RELAY_PLANNER", "")
	t.Setenv("CLAUDECODE", "")

	err := run([]string{"add", "--branch", "feature/api-auth"})
	if err == nil {
		t.Fatal("add without a relay planner must refuse")
	}
	if !strings.Contains(err.Error(), "no relay planner for this session") {
		t.Fatalf("expected the no-planner error, got %v", err)
	}
	if strings.Contains(err.Error(), "api-auth") {
		t.Errorf("the derived name must not appear in the refusal: %v", err)
	}
}

func TestParseFlagsAcceptsFlagsAfterPositionals(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	round := fs.Int("round", 0, "")
	dry := fs.Bool("dry", false, "")

	// This is the README's documented shape: the binding name first, its flags
	// after. Go's flag package stops at the first bare word, so before #48 both
	// flags below were silently dropped.
	if err := parseFlags(fs, []string{"webshop", "--round", "2", "--dry"}); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if *round != 2 {
		t.Errorf("--round after a positional must still parse, got %d", *round)
	}
	if !*dry {
		t.Error("--dry after a positional must still parse")
	}
	if got := fs.Args(); len(got) != 1 || got[0] != "webshop" {
		t.Errorf("fs.Args() = %v, want [webshop]", got)
	}
}

func TestParseFlagsKeepsEveryPositionalInOrder(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "")

	if err := parseFlags(fs, []string{"alpha", "--name", "n", "beta"}); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if *name != "n" {
		t.Errorf("--name = %q, want n", *name)
	}
	// explicitBinding refuses two positionals, so collapsing or reordering them
	// would quietly turn a refusal into a wrong guess.
	got := fs.Args()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("fs.Args() = %v, want [alpha beta]", got)
	}
}

func TestParseFlagsStillRejectsUnknownFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	// Before #48 this was swallowed with the rest of the tail.
	if err := parseFlags(fs, []string{"webshop", "--bogus"}); err == nil {
		t.Fatal("an unknown flag after a positional must still be rejected")
	}
}

func TestParseFlagsStillHandlesHelp(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := parseFlags(fs, []string{"-h"}); !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestParseFlagsHandlesNoArguments(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := parseFlags(fs, nil); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got := fs.Args(); len(got) != 0 {
		t.Errorf("fs.Args() = %v, want empty", got)
	}
}

func TestBindingArgTakesEitherForm(t *testing.T) {
	cases := []struct {
		label      string
		flag       string
		positional []string
		want       string
		wantErr    bool
	}{
		{"flag only", "webshop", nil, "webshop", false},
		{"positional only", "", []string{"webshop"}, "webshop", false},
		{"neither is not an error", "", nil, "", false},
		{"both at once is refused", "webshop", []string{"other"}, "", true},
		{"same name twice is still refused", "webshop", []string{"webshop"}, "", true},
		{"two positionals refused", "", []string{"a", "b"}, "", true},
	}

	for _, c := range cases {
		got, err := bindingArg(c.flag, c.positional)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected a refusal, got %q", c.label, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.label, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.label, got, c.want)
		}
	}
}

func TestResolveBindingAcceptsAPositionalName(t *testing.T) {
	rt := relay.Runtime{Store: store.New(t.TempDir())}

	// The whole point of #50: a named binding must be used, not discarded in
	// favour of whatever owns the cwd.
	got, err := resolveBinding(rt, "", []string{"webshop"})
	if err != nil {
		t.Fatalf("resolveBinding: %v", err)
	}
	if got != "webshop" {
		t.Errorf("got %q, want webshop", got)
	}
}

func TestResolveBindingRefusesTwoNames(t *testing.T) {
	rt := relay.Runtime{Store: store.New(t.TempDir())}

	if _, err := resolveBinding(rt, "webshop", []string{"frontend"}); err == nil {
		t.Fatal("naming the binding twice must be refused rather than one silently winning")
	}
}

func TestResolveBindingStillFallsBackToCWD(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))

	s := store.New(filepath.Join(tempHome, ".local", "state", "relay"))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Seed a binding that owns the test's own working directory, so the
	// fallback has something to find without any chdir.
	if err := s.Save(store.Binding{
		Name: "here", CWD: cwd, Round: 1, State: store.StateActive,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := resolveBinding(relay.Runtime{Store: s}, "", nil)
	if err != nil {
		t.Fatalf("resolveBinding: %v", err)
	}
	if got != "here" {
		t.Errorf("a bare invocation must still fall back to the cwd binding, got %q", got)
	}
}

func TestFilterReportNarrowsToOneBinding(t *testing.T) {
	rep := relay.Report{Bindings: []relay.BindingStatus{
		{Name: "api"}, {Name: "frontend"}, {Name: "backend"},
	}}

	got, err := filterReport(rep, "frontend")
	if err != nil {
		t.Fatalf("filterReport: %v", err)
	}
	if len(got.Bindings) != 1 || got.Bindings[0].Name != "frontend" {
		t.Fatalf("got %+v, want just frontend", got.Bindings)
	}
}

func TestFilterReportKeepsEverythingWhenUnnamed(t *testing.T) {
	rep := relay.Report{Bindings: []relay.BindingStatus{
		{Name: "api"}, {Name: "frontend"},
	}}

	// A bare `relay status` lists every binding; that is its whole job.
	got, err := filterReport(rep, "")
	if err != nil {
		t.Fatalf("filterReport: %v", err)
	}
	if len(got.Bindings) != 2 {
		t.Fatalf("got %+v, want both bindings", got.Bindings)
	}
}

func TestFilterReportRejectsAnUnknownName(t *testing.T) {
	rep := relay.Report{Bindings: []relay.BindingStatus{{Name: "api"}}}

	// Silence here would look identical to "that binding is fine".
	if _, err := filterReport(rep, "nosuch"); err == nil {
		t.Fatal("an unknown binding name must be an error, not an empty report")
	}
}

// scopeReport is the whole of the status/watch DONE rule, tested as a pure
// function: a test that ran cmdStatus would need a real herdr on PATH, which
// CI does not have and which made the first version of this test pass only on
// the dev machine.
func TestScopeReportHidesDoneUnlessAllOrNamed(t *testing.T) {
	rep := relay.Report{Bindings: []relay.BindingStatus{
		{Name: "live", State: string(store.StateActive)},
		{Name: "finished", State: string(store.StateDone)},
	}}

	plain := scopeReport(rep, "", false)
	if len(plain.Bindings) != 1 || plain.Bindings[0].Name != "live" || plain.DoneHidden != 1 {
		t.Errorf("plain: got %+v, want only live with DoneHidden 1", plain)
	}

	all := scopeReport(rep, "", true)
	if len(all.Bindings) != 2 || all.DoneHidden != 0 {
		t.Errorf("--all: got %+v, want both rows and DoneHidden 0", all)
	}

	// filterReport has already narrowed to the named binding by the time
	// scopeReport runs; what matters is that the name switches the filter off.
	named := scopeReport(relay.Report{Bindings: rep.Bindings[1:]}, "finished", false)
	if len(named.Bindings) != 1 || named.DoneHidden != 0 {
		t.Errorf("--name: got %+v, want the DONE row with DoneHidden 0", named)
	}
}

func TestParseFor(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)

	if got, err := parseFor("", now); err != nil || !got.IsZero() {
		t.Fatalf(`parseFor("", now) = %v, %v, want zero time, nil`, got, err)
	}

	if got, err := parseFor("2h", now); err != nil || !got.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("parseFor(2h, now) = %v, %v, want %v, nil", got, err, now.Add(2*time.Hour))
	}

	if got, err := parseFor("90m", now); err != nil || !got.Equal(now.Add(90*time.Minute)) {
		t.Fatalf("parseFor(90m, now) = %v, %v, want %v, nil", got, err, now.Add(90*time.Minute))
	}

	if _, err := parseFor("0", now); err == nil {
		t.Error(`parseFor("0", now) must be an error: a zero duration is not a gate`)
	}
	if _, err := parseFor("-5m", now); err == nil {
		t.Error(`parseFor("-5m", now) must be an error: a negative duration is not a gate`)
	}
	if _, err := parseFor("soon", now); err == nil {
		t.Error(`parseFor("soon", now) must be an error: not a Go duration`)
	}
}

// TestBindRejectsTabFlag pins #79: placement is not a per-bind decision any
// more, so the old --tab spelling must be an unknown flag, not a silent no-op.
// It fails in parseFlags, before newRuntime, so it never reaches herdr.
func TestBindRejectsTabFlag(t *testing.T) {
	for _, args := range [][]string{
		{"bind", "--tab"},
		{"add", "--name", "x", "--tab"},
		{"fork", "x", "--round", "1", "--new-name", "y", "--tab"},
		{"ask", "--role", "reviewer", "--file", "q.md", "--new-tab"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Errorf("%v: got %v, want an unknown-flag error", args, err)
		}
	}
}

// TestBindRebindNeedsResume pins #92: --rebind only means something on a
// resume. It is refused before newRuntime, so no herdr is reached.
func TestBindRebindNeedsResume(t *testing.T) {
	err := run([]string{"bind", "--rebind", "--name", "x"})
	if err == nil || !strings.Contains(err.Error(), "--rebind") || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("got %v, want an error naming --rebind and --resume", err)
	}
}

// TestPickRejectsAName pins spec §3: --pick chooses the binding, so naming
// one as well is a usage error. Each case fails before newRuntime, so no
// herdr is reached.
func TestPickRejectsAName(t *testing.T) {
	for _, args := range [][]string{
		{"done", "--pick", "x"},
		{"done", "--pick", "--name", "x"},
		{"unbind", "--pick", "x"},
		{"unbind", "--pick", "--name", "x", "--archive"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "--pick chooses the binding") {
			t.Errorf("%v: got %v, want the --pick usage error", args, err)
		}
	}
}

// TestPickRejectsAnswerFlags: the answer comes from the screen, so --keys,
// --choice and --text have nothing to apply to. Fails before newRuntime.
// TestBindHeadlessConflictsFailBeforeNewRuntime pins headless spec §6: the
// two flag conflicts are usage errors, refused before relay talks to herdr.
// CI runners have no herdr binary, so reaching newRuntime would be a
// different failure with a different message.
func TestBuilderWhere(t *testing.T) {
	if got := builderWhere(store.Endpoint{PaneID: "w2:p4"}); got != "w2:p4" {
		t.Errorf("pane: %q", got)
	}
	if got := builderWhere(store.Endpoint{Mode: store.ModeHeadless, AgentName: "x-builder"}); got != "headless" {
		t.Errorf("headless: %q", got)
	}
}

// TestBindHeadlessFlagIsNoOp is a rule test: --headless is the default and
// only local mode since #303, so bind/add/fork accept it and print one
// stderr note. It tests the pure flag-handling helper, not a subcommand:
// CI runners have no herdr, so no test may run a subcommand that reaches it.
func TestBindHeadlessFlagIsNoOp(t *testing.T) {
	if got := headlessNoOpLines(false); got != nil {
		t.Errorf("headlessNoOpLines(false) = %v, want nil", got)
	}
	got := headlessNoOpLines(true)
	if len(got) != 1 || got[0] != headlessFlagNote {
		t.Fatalf("headlessNoOpLines(true) = %v, want [%q]", got, headlessFlagNote)
	}
	if !strings.Contains(headlessFlagNote, "--headless is the default and only local mode") {
		t.Errorf("note = %q", headlessFlagNote)
	}
}

// TestStatusNotice is §4.6: exactly one line when the cached check proves
// relay is behind, and "" for every row the doctor's release table reports as
// SevOK. It is the pure function only -- no subcommand runs, because CI
// runners have no herdr.
func TestStatusNotice(t *testing.T) {
	cases := []struct {
		name    string
		running string
		latest  string
		ok      bool
		kind    release.Kind
		want    string
	}{
		{
			name:    "behind a plugin release",
			running: "v0.6.0",
			latest:  "v0.7.0",
			ok:      true,
			kind:    release.KindPluginRelease,
			want:    "relay v0.6.0 is behind v0.7.0 -- run relay doctor",
		},
		{
			name:    "behind a go install",
			running: "v0.6.0",
			latest:  "v0.7.0",
			ok:      true,
			kind:    release.KindGoInstall,
			want:    "relay v0.6.0 is behind v0.7.0 -- run relay doctor",
		},
		{
			name:    "no usable cache",
			running: "v0.6.0",
			latest:  "v0.7.0",
			ok:      false,
			kind:    release.KindPluginRelease,
			want:    "",
		},
		{
			name:    "unknown install kind",
			running: "v0.6.0",
			latest:  "v0.7.0",
			ok:      true,
			kind:    release.KindUnknown,
			want:    "",
		},
		{
			name:    "local build has nothing to update to",
			running: "v0.7.0-8-gbd8aed0",
			latest:  "v0.8.0",
			ok:      true,
			kind:    release.KindLocalBuild,
			want:    "",
		},
		{
			name:    "(devel) claims nothing",
			running: "(devel)",
			latest:  "v0.8.0",
			ok:      true,
			kind:    release.KindLocalBuild,
			want:    "",
		},
		{
			name:    "unparseable latest",
			running: "v0.6.0",
			latest:  "nightly",
			ok:      true,
			kind:    release.KindGoInstall,
			want:    "",
		},
		{
			name:    "already current",
			running: "v0.7.0",
			latest:  "v0.7.0",
			ok:      true,
			kind:    release.KindGoInstall,
			want:    "",
		},
		{
			// A describe of the very tag the cache names is not "behind" it.
			name:    "describe is not behind its own tag",
			running: "v0.7.0-8-gbd8aed0",
			latest:  "v0.7.0",
			ok:      true,
			kind:    release.KindPluginSource,
			want:    "",
		},
		{
			name:    "cached latest is older",
			running: "v0.7.0",
			latest:  "v0.6.0",
			ok:      true,
			kind:    release.KindGoInstall,
			want:    "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := statusNotice(c.running, c.latest, c.ok, c.kind); got != c.want {
				t.Errorf("statusNotice(%q, %q, %v, %q) = %q, want %q",
					c.running, c.latest, c.ok, c.kind, got, c.want)
			}
		})
	}
}
