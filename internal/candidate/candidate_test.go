package candidate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRefRoundTrip(t *testing.T) {
	tests := []struct {
		input   string
		wantRef Ref
		wantErr error
	}{
		{"claude/anthropic/sonnet", Ref{"claude", "anthropic", "sonnet"}, nil},
		{"opencode/openrouter/z-ai/glm-5.3-flash", Ref{"opencode", "openrouter", "z-ai/glm-5.3-flash"}, nil},
		{"agy/google/gemini-3.8-flash-high", Ref{"agy", "google", "gemini-3.8-flash-high"}, nil},
		{"claude/anthropic", Ref{}, ErrBadRef},
		{"claude//sonnet", Ref{}, ErrBadRef},
		{"/anthropic/sonnet", Ref{}, ErrBadRef},
		{"claude/anthropic/", Ref{}, ErrBadRef},
		{"", Ref{}, ErrBadRef},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseRef(tt.input)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ParseRef(%q) err = %v, want %v", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRef(%q) unexpected err: %v", tt.input, err)
			}
			if got != tt.wantRef {
				t.Errorf("ParseRef(%q) = %+v, want %+v", tt.input, got, tt.wantRef)
			}
			if got.String() != tt.input {
				t.Errorf("Ref.String() = %q, want %q", got.String(), tt.input)
			}
		})
	}
}

func TestSetQueries(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "candidates.json")
	data := `[
		{
			"harness": "claude",
			"provider": "anthropic",
			"model": "sonnet",
			"roles": ["builder", "reviewer"]
		},
		{
			"harness": "opencode",
			"provider": "openrouter",
			"model": "z-ai/glm-5.3-flash",
			"roles": ["builder"]
		},
		{
			"harness": "agy",
			"provider": "google",
			"model": "gemini-3.8-flash-high",
			"roles": ["builder"]
		}
	]`
	if err := os.WriteFile(confPath, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	set, err := Load(confPath)
	if err != nil {
		t.Fatalf("Load() unexpected err: %v", err)
	}

	if set.Len() != 3 {
		t.Errorf("Len() = %d, want 3", set.Len())
	}

	wantRefs := []string{
		"agy/google/gemini-3.8-flash-high",
		"claude/anthropic/sonnet",
		"opencode/openrouter/z-ai/glm-5.3-flash",
	}
	if gotRefs := set.Refs(); !reflect.DeepEqual(gotRefs, wantRefs) {
		t.Errorf("Refs() = %v, want %v", gotRefs, wantRefs)
	}

	builders := set.ForRole("builder")
	if len(builders) != 3 {
		t.Fatalf("ForRole(\"builder\") returned %d entries, want 3", len(builders))
	}
	for i, want := range wantRefs {
		if builders[i].Ref().String() != want {
			t.Errorf("ForRole(\"builder\")[%d] = %q, want %q", i, builders[i].Ref().String(), want)
		}
	}

	reviewers := set.ForRole("reviewer")
	if len(reviewers) != 1 {
		t.Fatalf("ForRole(\"reviewer\") returned %d entries, want 1", len(reviewers))
	}
	if reviewers[0].Ref().String() != "claude/anthropic/sonnet" {
		t.Errorf("ForRole(\"reviewer\")[0] = %q, want %q", reviewers[0].Ref().String(), "claude/anthropic/sonnet")
	}

	researchers := set.ForRole("researcher")
	if len(researchers) != 0 {
		t.Errorf("ForRole(\"researcher\") returned %d entries, want 0", len(researchers))
	}

	claudeSonnet, err := set.Lookup(Ref{"claude", "anthropic", "sonnet"})
	if err != nil {
		t.Errorf("Lookup(claude/anthropic/sonnet) err = %v, want nil", err)
	}
	if claudeSonnet.Model != "sonnet" {
		t.Errorf("Lookup returned model %q, want sonnet", claudeSonnet.Model)
	}

	_, err = set.Lookup(Ref{"claude", "anthropic", "opus"})
	if !errors.Is(err, ErrUnknownCandidate) {
		t.Errorf("Lookup(claude/anthropic/opus) err = %v, want ErrUnknownCandidate", err)
	}
	if !strings.Contains(err.Error(), "configured:") {
		t.Errorf("Lookup error %q does not contain \"configured:\"", err.Error())
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "none.json")
	set, err := Load(missingPath)
	if err != nil {
		t.Fatalf("Load(%q) unexpected err: %v", missingPath, err)
	}
	if set == nil {
		t.Fatal("Load() returned nil Set")
	}
	if set.Len() != 0 {
		t.Errorf("Len() = %d, want 0", set.Len())
	}
}

// Note: Rule 6 (CanServe false) is unreachable with the shipped table --
// every kind serves every role -- so it is not included in the validation table.
func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantSubstring string
	}{
		{
			name:          "missing model",
			body:          `[{"harness":"claude","provider":"anthropic","roles":["builder"]}]`,
			wantSubstring: "candidate 0: harness, provider and model are required",
		},
		{
			name:          "multi-segment provider",
			body:          `[{"harness":"claude","provider":"a/b","model":"m","roles":["builder"]}]`,
			wantSubstring: "provider must be a single segment",
		},
		{
			name:          "unknown harness",
			body:          `[{"harness":"nope","provider":"p","model":"m","roles":["builder"]}]`,
			wantSubstring: `unknown harness "nope"`,
		},
		{
			name:          "empty roles array",
			body:          `[{"harness":"claude","provider":"p","model":"m","roles":[]}]`,
			wantSubstring: "roles must not be empty",
		},
		{
			name:          "omitted roles",
			body:          `[{"harness":"claude","provider":"p","model":"m"}]`,
			wantSubstring: "roles must not be empty",
		},
		{
			name:          "unknown role",
			body:          `[{"harness":"claude","provider":"p","model":"m","roles":["reviwer"]}]`,
			wantSubstring: `unknown role "reviwer"`,
		},
		{
			name:          "invalid tree value",
			body:          `[{"harness":"claude","provider":"p","model":"m","roles":["builder"],"tree":"sideways"}]`,
			wantSubstring: `tree must be "binding" or "none"`,
		},
		{
			name:          "duplicate candidate",
			body:          `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]},{"harness":"claude","provider":"p","model":"m","roles":["reviewer"]}]`,
			wantSubstring: "duplicate candidate claude/p/m at index 0 and 1",
		},
		{
			name:          "not json",
			body:          `not json`,
			wantSubstring: "decode candidates",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "candidates.json")
			if err := os.WriteFile(path, []byte(tt.body), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load() expected error containing %q, got nil", tt.wantSubstring)
			}
			if !strings.Contains(err.Error(), tt.wantSubstring) {
				t.Errorf("Load() error = %q, want substring %q", err.Error(), tt.wantSubstring)
			}
		})
	}
}

func TestLoadAcceptsExtraArgsAndTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.json")
	body := `[
		{
			"harness": "agy",
			"provider": "google",
			"model": "gemini-3.8-flash-high",
			"roles": ["builder"],
			"tree": "binding",
			"extra_args": ["--dangerously-skip-permissions"]
		}
	]`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	set, err := Load(path)
	if err != nil {
		t.Fatalf("Load() unexpected err: %v", err)
	}

	c, err := set.Lookup(Ref{"agy", "google", "gemini-3.8-flash-high"})
	if err != nil {
		t.Fatalf("Lookup() err: %v", err)
	}

	wantExtra := []string{"--dangerously-skip-permissions"}
	if !reflect.DeepEqual(c.ExtraArgs, wantExtra) {
		t.Errorf("ExtraArgs = %v, want %v", c.ExtraArgs, wantExtra)
	}
	if c.Tree != "binding" {
		t.Errorf("Tree = %q, want \"binding\"", c.Tree)
	}
}
