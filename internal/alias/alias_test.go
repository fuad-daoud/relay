package alias

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestDefaultAliasArgv pins the exact argv of each built-in alias, not just
// its kind. A wrong model string would start the wrong (or a nonexistent)
// model with no error relay could see, so the argv is the thing worth
// asserting even though the aliases themselves are only examples.
func TestDefaultAliasArgv(t *testing.T) {
	tbl := DefaultTable()

	b, err := tbl.Lookup("builder")
	if err != nil {
		t.Fatalf("Lookup builder: %v", err)
	}
	if b.Kind != "opencode" {
		t.Errorf("builder kind = %q, want opencode", b.Kind)
	}
	wantBuilder := []string{"--agent", "plan-executor", "-m", "openrouter/z-ai/glm-5.3-flash"}
	if !slices.Equal(b.Args, wantBuilder) {
		t.Errorf("builder args = %v, want %v", b.Args, wantBuilder)
	}
	if b.Preamble != "" {
		t.Errorf("builder needs no preamble, got %q", b.Preamble)
	}

	c, err := tbl.Lookup("cbuilder")
	if err != nil {
		t.Fatalf("Lookup cbuilder: %v", err)
	}
	if c.Kind != "claude" {
		t.Errorf("cbuilder kind = %q, want claude", c.Kind)
	}
	wantCbuilder := []string{"--agent", "plan-executor", "--model", "sonnet"}
	if !slices.Equal(c.Args, wantCbuilder) {
		t.Errorf("cbuilder args = %v, want %v", c.Args, wantCbuilder)
	}

	a, err := tbl.Lookup("abuilder")
	if err != nil {
		t.Fatalf("Lookup abuilder: %v", err)
	}
	if a.Kind != "agy" {
		t.Errorf("abuilder kind = %q, want agy", a.Kind)
	}
	wantAbuilder := []string{"--model", "gemini-3.8-flash-high", "--dangerously-skip-permissions"}
	if !slices.Equal(a.Args, wantAbuilder) {
		t.Errorf("abuilder args = %v, want %v", a.Args, wantAbuilder)
	}
	if a.Preamble == "" {
		t.Error("abuilder must carry a plan-executor preamble; agy has no --agent flag")
	}
}

func TestSpecDefaultsToBuilderRoleAndBindingTree(t *testing.T) {
	// The three shipped aliases predate roles. They must keep parsing as
	// builders with no edit to aliases.json.
	tbl := DefaultTable()
	for _, name := range tbl.Names() {
		spec, err := tbl.Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		if spec.IsConsult() {
			t.Errorf("%q reports as a consult; shipped aliases are builders", name)
		}
	}
}

func TestLoadTableReadsRoleAndTree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliases.json")
	body := `[{"name":"reviewer","kind":"claude","args":["--agent","reviewer"],"role":"consult","tree":"binding"}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write aliases: %v", err)
	}

	tbl, err := LoadTable(path)
	if err != nil {
		t.Fatalf("LoadTable: %v", err)
	}
	spec, err := tbl.Lookup("reviewer")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !spec.IsConsult() {
		t.Errorf("role = %q, want a consult", spec.Role)
	}
	if spec.Tree != "binding" {
		t.Errorf("tree = %q, want %q", spec.Tree, "binding")
	}
}

func TestLookupUnknownAlias(t *testing.T) {
	if _, err := DefaultTable().Lookup("nope"); !errors.Is(err, ErrUnknownAlias) {
		t.Fatal("want ErrUnknownAlias")
	}
}

func TestLoadTableOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.json")
	body := `[{"name":"builder","kind":"opencode","args":["--agent","plan-executor","-m","other/model"]}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	tbl, err := LoadTable(path)
	if err != nil {
		t.Fatalf("LoadTable: %v", err)
	}

	b, err := tbl.Lookup("builder")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if b.Args[3] != "other/model" {
		t.Errorf("override not applied: %v", b.Args)
	}
	if _, err := tbl.Lookup("cbuilder"); err != nil {
		t.Errorf("untouched defaults must survive an override: %v", err)
	}
}

func TestLoadTableMissingFileIsDefaults(t *testing.T) {
	tbl, err := LoadTable(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing override file is not an error: %v", err)
	}
	if len(tbl.Names()) != 3 {
		t.Errorf("got %d aliases, want the 3 defaults", len(tbl.Names()))
	}
}

func TestLoadTableUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipping test when running as root")
	}

	path := filepath.Join(t.TempDir(), "unreadable.json")
	if err := os.WriteFile(path, []byte("[]"), 0o000); err != nil {
		t.Fatalf("write unreadable file: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(path, 0o600)
	})

	if _, err := LoadTable(path); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("LoadTable on unreadable file: got %v, want os.ErrPermission", err)
	}
}
