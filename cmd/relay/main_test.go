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

	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

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

func TestAliasConfigHome(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	relayDir := filepath.Join(configHome, "relay")
	if err := os.MkdirAll(relayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasContent := `[{"name":"custom-builder","kind":"dummy","args":["arg"]}]`
	if err := os.WriteFile(filepath.Join(relayDir, "aliases.json"), []byte(aliasContent), 0o644); err != nil {
		t.Fatal(err)
	}

	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if _, err := rt.Aliases.Lookup("custom-builder"); err != nil {
		t.Fatalf("rt.Aliases.Lookup(%q): %v", "custom-builder", err)
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
func TestBindAcceptsAssumeDeadFlag(t *testing.T) {
	err := run([]string{"bind", "--resume", "--assume-dead"})
	if err == nil {
		t.Fatal("relay bind --resume without --name must still be rejected")
	}
	if strings.Contains(err.Error(), "not defined") {
		t.Fatalf("--assume-dead is not a defined flag: %v", err)
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("expected the --name error, got %q", err)
	}
}

func TestAnswerRefusesToGuessTheBinding(t *testing.T) {
	// A bare `relay answer` used to resolve to whichever binding owns the cwd.
	// With peer builders that is always builder #1, so an unqualified answer
	// pressed a key into a dialog nobody had looked at.
	err := run([]string{"answer", "--keys", "enter"})
	if err == nil {
		t.Fatal("a bare relay answer must be refused")
	}
	if !strings.Contains(err.Error(), "will not guess which one you meant") {
		t.Fatalf("expected a refuse-to-guess error, got %v", err)
	}
	if !strings.Contains(err.Error(), "usage: relay answer") {
		t.Fatalf("expected the usage line, got %v", err)
	}
}

func TestAnswerRefusesBothNameAndPositional(t *testing.T) {
	err := run([]string{"answer", "--name", "webshop", "--keys", "enter", "webshop"})
	if err == nil || !strings.Contains(err.Error(), "will not guess which one you meant") {
		t.Fatalf("naming the binding twice must be refused, got %v", err)
	}
}

func TestAddHelp(t *testing.T) {
	err := run([]string{"add", "-h"})
	if !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestAddValidation(t *testing.T) {
	// Missing --name
	err := run([]string{"add", "--builder", "cbuilder"})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("expected an error about --name, got %v", err)
	}

	// Missing --builder
	err = run([]string{"add", "--name", "frontend"})
	if err == nil || !strings.Contains(err.Error(), "--builder") {
		t.Fatalf("expected an error about --builder, got %v", err)
	}
}

func TestParseFlagsAcceptsFlagsAfterPositionals(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	round := fs.Int("round", 0, "")
	tab := fs.Bool("tab", false, "")

	// This is the README's documented shape: the binding name first, its flags
	// after. Go's flag package stops at the first bare word, so before #48 both
	// flags below were silently dropped.
	if err := parseFlags(fs, []string{"webshop", "--round", "2", "--tab"}); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if *round != 2 {
		t.Errorf("--round after a positional must still parse, got %d", *round)
	}
	if !*tab {
		t.Error("--tab after a positional must still parse")
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
