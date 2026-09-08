package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	cmd = exec.Command("git", "-c", "user.name=T", "-c", "user.email=t@e", "commit", "-m", "init")
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

func TestForkHelp(t *testing.T) {
	err := run([]string{"fork", "-h"})
	if !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestForkValidation(t *testing.T) {
	// Missing source
	err := run([]string{"fork", "--round", "1", "--new-name", "fork-1"})
	if err == nil || !strings.Contains(err.Error(), "needs the source binding name") {
		t.Fatalf("expected error about source binding, got %v", err)
	}

	// Missing --round
	err = run([]string{"fork", "src", "--new-name", "fork-1"})
	if err == nil || !strings.Contains(err.Error(), "--round") {
		t.Fatalf("expected error about --round, got %v", err)
	}

	// Missing --new-name
	err = run([]string{"fork", "src", "--round", "1"})
	if err == nil || !strings.Contains(err.Error(), "--new-name") {
		t.Fatalf("expected error about --new-name, got %v", err)
	}
}
