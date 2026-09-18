package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
)

func TestDialogTailMatchesEveryDefault(t *testing.T) {
	h, ok := harness.Lookup("agy")
	if !ok {
		t.Fatal("Lookup(\"agy\") not found")
	}
	patterns := make([]*regexp.Regexp, 0, len(h.DialogPatterns))
	for _, pat := range h.DialogPatterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			t.Fatalf("Compile(%q): %v", pat, err)
		}
		patterns = append(patterns, re)
	}

	fixtures := []string{
		"[y/N]",
		"(Y/n)",
		"Do you want to proceed?",
		"❯ 1. Yes",
		"Press Enter to confirm",
		"Esc to cancel",
	}

	for i, f := range fixtures {
		if !dialogTail(f, patterns) {
			t.Errorf("dialogTail(%q, patterns) = false, want true", f)
		}
		if i < len(patterns) && !patterns[i].MatchString(f) {
			t.Errorf("pattern %q did not match fixture %q", patterns[i].String(), f)
		}
	}
}

func TestDialogTailIgnoresOrdinaryOutput(t *testing.T) {
	h, ok := harness.Lookup("agy")
	if !ok {
		t.Fatal("Lookup(\"agy\") not found")
	}
	patterns := make([]*regexp.Regexp, 0, len(h.DialogPatterns))
	for _, pat := range h.DialogPatterns {
		patterns = append(patterns, regexp.MustCompile(pat))
	}

	fixtures := []string{
		"compiling main.go",
		"error: expected ] at line 3",
		"ok  github.com/x 0.4s",
		"",
	}

	for _, f := range fixtures {
		if dialogTail(f, patterns) {
			t.Errorf("dialogTail(%q, patterns) = true, want false", f)
		}
	}
}

func TestDialogPatternsAdoptedBuilderGetsDefaults(t *testing.T) {
	h, ok := harness.Lookup("agy")
	if !ok {
		t.Fatal("Lookup(\"agy\") not found")
	}
	defaultCount := len(h.DialogPatterns)

	rt := Runtime{}
	pats := dialogPatterns(rt, "agy", "")
	if len(pats) != defaultCount {
		t.Fatalf("dialogPatterns(agy, \"\") count = %d, want %d", len(pats), defaultCount)
	}

	dir := t.TempDir()
	confPath := filepath.Join(dir, "candidates.json")
	data := `[
		{
			"harness": "agy",
			"provider": "google",
			"model": "gemini-3.8-flash-high",
			"roles": ["builder"],
			"dialog_patterns": ["custom"]
		}
	]`
	if err := os.WriteFile(confPath, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	cands, err := candidate.Load(confPath)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	rt.Candidates = cands

	patsCustom := dialogPatterns(rt, "agy", "agy/google/gemini-3.8-flash-high")
	if len(patsCustom) != defaultCount+1 {
		t.Fatalf("dialogPatterns with custom count = %d, want %d", len(patsCustom), defaultCount+1)
	}
	if patsCustom[len(patsCustom)-1].String() != "custom" {
		t.Errorf("last pattern = %q, want %q", patsCustom[len(patsCustom)-1].String(), "custom")
	}
}

func TestDialogGuardReadErrorIsNotADialog(t *testing.T) {
	fake := &fakeHerdr{readErr: errors.New("cannot read screen")}
	rt := Runtime{Herdr: fake}
	patterns := []*regexp.Regexp{regexp.MustCompile("(?i)confirm")}

	got := dialogGuard(context.Background(), rt, "%42", patterns)
	if got {
		t.Error("dialogGuard on read error = true, want false")
	}
	if len(fake.reads) != 1 {
		t.Fatalf("reads = %d, want 1", len(fake.reads))
	}
	r := fake.reads[0]
	if r.Target != "%42" {
		t.Errorf("read Target = %q, want %%42", r.Target)
	}
	if r.Source != "visible" {
		t.Errorf("read Source = %q, want \"visible\"", r.Source)
	}
	if r.Lines != dialogScanLines {
		t.Errorf("read Lines = %d, want %d", r.Lines, dialogScanLines)
	}
}
