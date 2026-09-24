package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEditor writes an executable sh script (body after the shebang) into a
// fresh temp dir and returns its path. The plan sanctions this: it is `sh`,
// not a harness.
func writeEditor(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write editor script: %v", err)
	}
	return path
}

// TestRemovedVerbsNameReplacement is step 3's table test: each of the seven
// verbs P2b folded into `relevo config` exits 2 and names its replacement.
func TestRemovedVerbsNameReplacement(t *testing.T) {
	initRoot(t)

	cases := map[string]string{
		"init":       "relevo config init",
		"candidates": "relevo config",
		"policy":     "relevo config",
		"roles":      "relevo config",
		"agent":      "relevo config agents",
		"client":     "relevo config server",
		"servers":    "relevo config server list",
	}
	for verb, want := range cases {
		_, stderr, err := captureOutput(t, func() error {
			return run([]string{verb})
		})
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("%s: exit = %v, want exit code 2", verb, err)
			continue
		}
		if !strings.Contains(string(stderr), "was removed") || !strings.Contains(string(stderr), want) {
			t.Errorf("%s: stderr = %q, want it to name the replacement %q", verb, stderr, want)
		}
	}
}

// TestConfigBareShowsThreeHeadings pins the bare form: one line per block.
func TestConfigBareShowsThreeHeadings(t *testing.T) {
	initRoot(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config"})
	})
	if err != nil {
		t.Fatalf("run config: %v (stderr: %s)", err, stderr)
	}
	for _, heading := range []string{"roles", "pick", "candidates"} {
		if !strings.Contains(string(stdout), heading) {
			t.Errorf("bare config output does not name the %q block:\n%s", heading, stdout)
		}
	}
}

func TestConfigSetGetUnsetRoundTrip(t *testing.T) {
	initRoot(t)

	// A whole section replaced.
	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`})
	}); err != nil {
		t.Fatalf("set candidates: %v (stderr: %s)", err, stderr)
	}
	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err != nil {
		t.Fatalf("get candidates: %v", err)
	}
	if !strings.Contains(string(stdout), `"harness": "claude"`) {
		t.Errorf("get candidates = %q, want the stored body", stdout)
	}

	// A nested key, with the intermediate object created.
	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "policy.order.builder", `["claude/p/m"]`})
	}); err != nil {
		t.Fatalf("set policy.order.builder: %v", err)
	}
	stdout, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "policy.order.builder"})
	})
	if err != nil {
		t.Fatalf("get policy.order.builder: %v", err)
	}
	if !strings.Contains(string(stdout), "claude/p/m") {
		t.Errorf("get policy.order.builder = %q, want the stored token", stdout)
	}

	// Unset the key, then the section.
	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "unset", "policy.order.builder"})
	}); err != nil {
		t.Fatalf("unset policy.order.builder: %v", err)
	}
	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "policy.order.builder"})
	})
	if err == nil || !strings.Contains(err.Error(), "policy.order.builder: not set") {
		t.Errorf("get after unset = %v, want policy.order.builder: not set", err)
	}

	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "unset", "candidates"})
	}); err != nil {
		t.Fatalf("unset candidates: %v", err)
	}
	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err == nil || !strings.Contains(err.Error(), "candidates: not set") {
		t.Errorf("get after unset = %v, want candidates: not set", err)
	}
}

func TestConfigSetRefusesInvalidPolicy(t *testing.T) {
	initRoot(t)

	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "policy", `{"order":{"builder":["claude/p/m"]}}`})
	}); err != nil {
		t.Fatalf("set valid policy: %v", err)
	}

	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "policy", `{"max_switches":-1}`})
	}); err == nil {
		t.Fatal("set of an invalid policy: want error, got nil")
	}

	// Nothing changed: the old policy stands, and the refused key is absent.
	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "policy"})
	})
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if !strings.Contains(string(stdout), "order") {
		t.Errorf("get policy = %q, want the previous body", stdout)
	}
	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "policy.max_switches"})
	})
	if err == nil {
		t.Error("get policy.max_switches after a refused set: want not set, got nil")
	}
}

func TestConfigExportImportRoundTrip(t *testing.T) {
	initRoot(t)

	for _, args := range [][]string{
		{"config", "set", "candidates", `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`},
		{"config", "set", "policy.order.builder", `["claude/p/m"]`},
	} {
		if _, _, err := captureOutput(t, func() error { return run(args) }); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	first, _, err := captureOutput(t, func() error {
		return run([]string{"config", "export"})
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, stderr, err := runWithStdin(t, string(first), "config", "import", "-"); err != nil {
		t.Fatalf("import: %v (stderr: %s)", err, stderr)
	}
	second, _, err := captureOutput(t, func() error {
		return run([]string{"config", "export"})
	})
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("export/import is not identity:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestConfigEditSavesChanges(t *testing.T) {
	initRoot(t)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", writeEditor(t, `printf '%s' '{"candidates":[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]}' > "$1"`))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "edit"})
	})
	if err != nil {
		t.Fatalf("edit: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "saved (config version") {
		t.Errorf("edit stdout = %q, want the saved line", stdout)
	}

	out, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err != nil {
		t.Fatalf("get after edit: %v", err)
	}
	if !strings.Contains(string(out), `"harness": "claude"`) {
		t.Errorf("get after edit = %q, want the edited value", out)
	}
}

func TestConfigEditEmptyFileAborts(t *testing.T) {
	initRoot(t)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", writeEditor(t, `: > "$1"`))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "edit"})
	})
	if err != nil {
		t.Fatalf("edit: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "aborted; nothing changed") {
		t.Errorf("edit stdout = %q, want the abort line", stdout)
	}

	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err == nil {
		t.Error("get candidates after an aborted edit: want not set, got nil")
	}
}

func TestConfigEditRetriesInvalidJSON(t *testing.T) {
	initRoot(t)
	t.Setenv("VISUAL", "")

	count := filepath.Join(t.TempDir(), "runs")
	body := "n=$(cat '" + count + "' 2>/dev/null || echo 0)\n" +
		"n=$((n + 1))\n" +
		"echo \"$n\" > '" + count + "'\n" +
		"if [ \"$n\" -eq 1 ]; then\n" +
		"  printf '%s' '{not json' > \"$1\"\n" +
		"else\n" +
		"  printf '%s' '{\"candidates\":[{\"harness\":\"claude\",\"provider\":\"p\",\"model\":\"m\",\"roles\":[\"builder\"]}]}' > \"$1\"\n" +
		"fi\n"
	t.Setenv("EDITOR", writeEditor(t, body))

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "edit"})
	})
	if err != nil {
		t.Fatalf("edit: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stderr), "relevo config edit:") {
		t.Errorf("edit stderr = %q, want the validation error", stderr)
	}
	if !strings.Contains(string(stdout), "saved (config version") {
		t.Errorf("edit stdout = %q, want the saved line", stdout)
	}

	out, _, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "candidates"})
	})
	if err != nil {
		t.Fatalf("get after edit: %v", err)
	}
	if !strings.Contains(string(out), `"harness": "claude"`) {
		t.Errorf("get after edit = %q, want the second-run value", out)
	}
}

func TestConfigServerKeyIsStable(t *testing.T) {
	initRoot(t)

	first, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "server", "key"})
	})
	if err != nil {
		t.Fatalf("server key: %v (stderr: %s)", err, stderr)
	}
	second, _, err := captureOutput(t, func() error {
		return run([]string{"config", "server", "key"})
	})
	if err != nil {
		t.Fatalf("second server key: %v", err)
	}

	id1 := strings.SplitN(string(first), "\n", 2)[0]
	id2 := strings.SplitN(string(second), "\n", 2)[0]
	if !strings.HasPrefix(id1, "client id ") {
		t.Fatalf("server key first line = %q, want a client id", id1)
	}
	if id1 != id2 {
		t.Errorf("server key id changed between runs: %q then %q", id1, id2)
	}
}

func TestConfigSecretSetListHidesValue(t *testing.T) {
	initRoot(t)

	if _, stderr, err := runWithStdin(t, "  ts-key-123  \n", "config", "secret", "set", "typesafe"); err != nil {
		t.Fatalf("secret set: %v (stderr: %s)", err, stderr)
	}

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"config", "secret", "list"})
	})
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	if !strings.Contains(string(stdout), "typesafe") {
		t.Errorf("secret list = %q, want the name", stdout)
	}
	if strings.Contains(string(stdout), "ts-key-123") {
		t.Errorf("secret list = %q, must never print the value", stdout)
	}
}

func TestConfigSecretUnknownNameExits2(t *testing.T) {
	initRoot(t)

	_, stderr, err := runWithStdin(t, "x", "config", "secret", "set", "bogus")
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("secret set bogus: exit = %v, want exit code 2", err)
	}
	if !strings.Contains(string(stderr), "typesafe") || !strings.Contains(string(stderr), "client.key") {
		t.Errorf("stderr = %q, want both allowed names", stderr)
	}
}
