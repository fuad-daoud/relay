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
		{
			name:          "invalid limit pattern regex",
			body:          `[{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"],"limit_patterns":["(unclosed"]}]`,
			wantSubstring: "limit_patterns[0]",
		},
		{
			name:          "invalid dialog pattern regex",
			body:          `[{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"],"dialog_patterns":["("]}]`,
			wantSubstring: "dialog_patterns[0]",
		},
		{
			name:          "invalid tier",
			body:          `[{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"],"tier":"god"}]`,
			wantSubstring: "candidate 0: tier:",
		},
		{
			name:          "invalid denial pattern regex",
			body:          `[{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"],"denial_patterns":["["]}]`,
			wantSubstring: "denial_patterns[0]",
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

// TestLoadAcceptsEmptyRoles is the port of the two "roles must not be empty"
// cases TestLoadValidation used to carry (#374 §4.4): a candidate with an empty
// roles array, or none at all, is valid. It serves nothing in legacy mode, and
// in roles.json mode its roles are ignored anyway.
func TestLoadAcceptsEmptyRoles(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty roles array", body: `[{"harness":"claude","provider":"p","model":"m","roles":[]}]`},
		{name: "omitted roles", body: `[{"harness":"claude","provider":"p","model":"m"}]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "candidates.json")
			if err := os.WriteFile(path, []byte(tt.body), 0644); err != nil {
				t.Fatal(err)
			}
			set, err := Load(path)
			if err != nil {
				t.Fatalf("Load() unexpected err: %v", err)
			}
			if set.Len() != 1 {
				t.Fatalf("Len() = %d, want 1", set.Len())
			}
			ref, err := ParseRef("claude/p/m")
			if err != nil {
				t.Fatal(err)
			}
			c, err := set.Lookup(ref)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if c.Serves("builder") {
				t.Error(`Serves("builder") = true, want false: the candidate lists no roles`)
			}
		})
	}
}

// loadWarnings writes body to a temp candidates.json and loads it with
// LoadWithWarnings, failing on any error.
func loadWarnings(t *testing.T, body string) (*Set, []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	set, warnings, err := LoadWithWarnings(path)
	if err != nil {
		t.Fatalf("LoadWithWarnings(%s): %v", body, err)
	}
	return set, warnings
}

// TestLoadSkipsUnknownHarnessAndRole pins #372 §4.4: a candidate whose harness
// or one of whose roles is unknown is skipped with a warning, and the others
// still load. A duplicate still fails.
func TestLoadSkipsUnknownHarnessAndRole(t *testing.T) {
	t.Run("unknown harness", func(t *testing.T) {
		set, warnings := loadWarnings(t, `[
			{"harness":"nope","provider":"p","model":"m","roles":["builder"]},
			{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"]}
		]`)
		if set.Len() != 1 || len(set.Refs()) != 1 || set.Refs()[0] != "claude/anthropic/m" {
			t.Fatalf("set = %v, want only claude/anthropic/m", set.Refs())
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], `unknown harness "nope" (skipped)`) {
			t.Fatalf("warnings = %v, want one naming the unknown harness", warnings)
		}
	})

	t.Run("unknown role", func(t *testing.T) {
		set, warnings := loadWarnings(t, `[
			{"harness":"claude","provider":"p","model":"m","roles":["reviwer"]},
			{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"]}
		]`)
		if set.Len() != 1 || len(set.Refs()) != 1 || set.Refs()[0] != "claude/anthropic/m" {
			t.Fatalf("set = %v, want only claude/anthropic/m", set.Refs())
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], `unknown role "reviwer" (skipped)`) {
			t.Fatalf("warnings = %v, want one naming the unknown role", warnings)
		}
	})

	t.Run("a duplicate still fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "candidates.json")
		body := `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]},{"harness":"claude","provider":"p","model":"m","roles":["reviewer"]}]`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadWithWarnings(path); err == nil || !strings.Contains(err.Error(), "duplicate candidate") {
			t.Fatalf("LoadWithWarnings err = %v, want a duplicate error", err)
		}
	})
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

func TestLoadAcceptsLimitPatterns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.json")
	body := `[
		{
			"harness": "agy",
			"provider": "google",
			"model": "gemini-3.8-flash-high",
			"roles": ["builder"],
			"limit_patterns": ["(?i)quota"]
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

	wantPatterns := []string{"(?i)quota"}
	if !reflect.DeepEqual(c.LimitPatterns, wantPatterns) {
		t.Errorf("LimitPatterns = %v, want %v", c.LimitPatterns, wantPatterns)
	}
}

func TestLoadAcceptsDialogPatterns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.json")
	body := `[
		{
			"harness": "agy",
			"provider": "google",
			"model": "gemini-3.8-flash-high",
			"roles": ["builder"],
			"dialog_patterns": ["(?i)confirm"]
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

	wantPatterns := []string{"(?i)confirm"}
	if !reflect.DeepEqual(c.DialogPatterns, wantPatterns) {
		t.Errorf("DialogPatterns = %v, want %v", c.DialogPatterns, wantPatterns)
	}
}

func TestCandidatePlanFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.json")
	body := `[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"],"plan":true},
	          {"harness":"agy","provider":"google","model":"g","roles":["builder"]}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := set.Lookup(Ref{Harness: "claude", Provider: "anthropic", Model: "sonnet"})
	if err != nil || !c.Plan {
		t.Errorf("plan flag not loaded: %+v, %v", c, err)
	}
	c, _ = set.Lookup(Ref{Harness: "agy", Provider: "google", Model: "g"})
	if c.Plan {
		t.Error("plan defaults to false")
	}
}

func TestLoadAcceptsTierAndDenialPatterns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.json")
	body := `[
		{
			"harness": "claude",
			"provider": "anthropic",
			"model": "sonnet",
			"roles": ["builder"],
			"tier": "yolo",
			"denial_patterns": ["(?i)permission denied"]
		}
	]`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	set, err := Load(path)
	if err != nil {
		t.Fatalf("Load() unexpected err: %v", err)
	}

	c, err := set.Lookup(Ref{"claude", "anthropic", "sonnet"})
	if err != nil {
		t.Fatalf("Lookup() err: %v", err)
	}

	if c.Tier != "yolo" {
		t.Errorf("Tier = %q, want \"yolo\"", c.Tier)
	}
	wantPatterns := []string{"(?i)permission denied"}
	if !reflect.DeepEqual(c.DenialPatterns, wantPatterns) {
		t.Errorf("DenialPatterns = %v, want %v", c.DenialPatterns, wantPatterns)
	}
}

// TestSetProviders: two candidates on one provider name it once, the result
// is sorted whatever order the file listed them in, and a nil set -- a server
// with no candidates.json -- answers nil instead of panicking.
func TestSetProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.json")
	body := `[
	  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
	  {"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]},
	  {"harness":"agy","provider":"zeta","model":"g","roles":["builder"]},
	  {"harness":"claude","provider":"test","model":"sonnet","roles":["reviewer"]}
	]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := set.Providers()
	want := []string{"anthropic", "test", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Providers() = %v, want %v", got, want)
	}

	var nilSet *Set
	if got := nilSet.Providers(); got != nil {
		t.Errorf("(*Set)(nil).Providers() = %v, want nil", got)
	}
}

// TestParseNames pins §4.1's name rules: derived names are filled, an explicit
// name is kept, the three Parse errors fire, and a skipped entry still takes
// part in DeriveNames so a later name does not shift when it is fixed.
func TestParseNames(t *testing.T) {
	t.Run("derived names are filled", func(t *testing.T) {
		body := `[
			{"harness":"claude","provider":"anthropic","model":"sonnet"},
			{"harness":"agy","provider":"google","model":"gemini-3.8-flash-high"}
		]`
		set, _, err := Parse("candidates.json", []byte(body))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got, want := set.Names(), []string{"gemini-3.8-flash-high", "sonnet"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Names() = %v, want %v", got, want)
		}
		c, err := set.Lookup(Ref{"claude", "anthropic", "sonnet"})
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if c.Name != "sonnet" {
			t.Errorf("candidate Name = %q, want %q", c.Name, "sonnet")
		}
	})

	t.Run("an explicit name is kept", func(t *testing.T) {
		body := `[{"name":"haiku","harness":"claude","provider":"anthropic","model":"claude-3-5-haiku"}]`
		set, _, err := Parse("candidates.json", []byte(body))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got, want := set.Names(), []string{"haiku"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Names() = %v, want %v", got, want)
		}
	})

	t.Run("bad shape", func(t *testing.T) {
		body := `[{"name":"Bad","harness":"claude","provider":"anthropic","model":"sonnet"}]`
		_, _, err := Parse("candidates.json", []byte(body))
		if err == nil {
			t.Fatal("Parse succeeded for a bad name, want an error")
		}
		want := `name "Bad": want ^[a-z0-9][a-z0-9.-]{0,23}$`
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Parse err = %q, want it containing %q", err.Error(), want)
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		body := `[
			{"name":"x","harness":"claude","provider":"anthropic","model":"a"},
			{"name":"x","harness":"claude","provider":"other","model":"b"}
		]`
		_, _, err := Parse("candidates.json", []byte(body))
		if err == nil {
			t.Fatal("Parse succeeded for a duplicate name, want an error")
		}
		want := `candidate 1: duplicate name "x" at index 0 and 1`
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Parse err = %q, want it containing %q", err.Error(), want)
		}
	})

	t.Run("provider clash", func(t *testing.T) {
		body := `[
			{"harness":"claude","provider":"anthropic","model":"a"},
			{"name":"anthropic","harness":"claude","provider":"other","model":"b"}
		]`
		_, _, err := Parse("candidates.json", []byte(body))
		if err == nil {
			t.Fatal("Parse succeeded for a name equal to a provider, want an error")
		}
		want := `candidate 1: name "anthropic" is also a provider name`
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Parse err = %q, want it containing %q", err.Error(), want)
		}
	})

	t.Run("a skipped entry still takes part in DeriveNames", func(t *testing.T) {
		body := `[
			{"harness":"nope","provider":"p","model":"m"},
			{"harness":"claude","provider":"anthropic","model":"m"}
		]`
		set, warnings, err := Parse("candidates.json", []byte(body))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("warnings = %v, want one for the unknown harness", warnings)
		}
		// The skipped entry still reserved "m", so the surviving candidate
		// keeps the name DeriveNames gave it in the full list.
		if got, want := set.Names(), []string{"claude-m"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Names() = %v, want %v", got, want)
		}
	})
}

// TestResolve pins §4.1's Resolve: a name, a token, a bad token, an unknown
// string that lists the known names, and a nil set.
func TestResolve(t *testing.T) {
	body := `[
		{"harness":"claude","provider":"anthropic","model":"sonnet"},
		{"harness":"agy","provider":"google","model":"gemini-3.8-flash-high"}
	]`
	set, _, err := Parse("candidates.json", []byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	t.Run("by name", func(t *testing.T) {
		c, err := set.Resolve("sonnet")
		if err != nil {
			t.Fatalf("Resolve(sonnet): %v", err)
		}
		if got := c.Ref().String(); got != "claude/anthropic/sonnet" {
			t.Errorf("Resolve(sonnet) = %q, want claude/anthropic/sonnet", got)
		}
	})

	t.Run("by token", func(t *testing.T) {
		c, err := set.Resolve("claude/anthropic/sonnet")
		if err != nil {
			t.Fatalf("Resolve(token): %v", err)
		}
		if got := c.Ref().String(); got != "claude/anthropic/sonnet" {
			t.Errorf("Resolve(token) = %q, want claude/anthropic/sonnet", got)
		}
	})

	t.Run("bad token", func(t *testing.T) {
		if _, err := set.Resolve("claude/anthropic"); !errors.Is(err, ErrBadRef) {
			t.Errorf("Resolve(claude/anthropic) err = %v, want ErrBadRef", err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		_, err := set.Resolve("nope")
		if !errors.Is(err, ErrUnknownCandidate) {
			t.Fatalf("Resolve(nope) err = %v, want ErrUnknownCandidate", err)
		}
		want := `unknown candidate "nope" (known: gemini-3.8-flash-high, sonnet)`
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(nope) err = %q, want it containing %q", err.Error(), want)
		}
	})

	t.Run("nil set", func(t *testing.T) {
		var nilSet *Set
		_, err := nilSet.Resolve("sonnet")
		if !errors.Is(err, ErrUnknownCandidate) {
			t.Fatalf("(*Set)(nil).Resolve err = %v, want ErrUnknownCandidate", err)
		}
		if !strings.Contains(err.Error(), "(no candidates configured)") {
			t.Errorf("(*Set)(nil).Resolve err = %q, want it to say no candidates are configured", err.Error())
		}
	})
}

// TestNameOf pins §4.1's NameOf: a known token resolves, an unknown one is
// returned unchanged, and a nil set returns its argument.
func TestNameOf(t *testing.T) {
	body := `[{"harness":"claude","provider":"anthropic","model":"sonnet"}]`
	set, _, err := Parse("candidates.json", []byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := set.NameOf("claude/anthropic/sonnet"); got != "sonnet" {
		t.Errorf("NameOf(known) = %q, want sonnet", got)
	}
	if got := set.NameOf("claude/anthropic/opus"); got != "claude/anthropic/opus" {
		t.Errorf("NameOf(unknown) = %q, want the token back", got)
	}

	var nilSet *Set
	if got := nilSet.NameOf("claude/anthropic/sonnet"); got != "claude/anthropic/sonnet" {
		t.Errorf("(*Set)(nil).NameOf = %q, want the token back", got)
	}
}

// TestNameFor pins round 3 F3's NameFor: ok is true only when the set holds
// the token. A known token gives its name; an unknown one and a nil set give
// "", false, so a caller can leave a name field empty.
func TestNameFor(t *testing.T) {
	body := `[{"harness":"claude","provider":"anthropic","model":"sonnet"}]`
	set, _, err := Parse("candidates.json", []byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	name, ok := set.NameFor("claude/anthropic/sonnet")
	if !ok || name != "sonnet" {
		t.Errorf("NameFor(known) = %q, %v, want sonnet, true", name, ok)
	}

	if name, ok := set.NameFor("claude/anthropic/opus"); ok || name != "" {
		t.Errorf("NameFor(unknown) = %q, %v, want \"\", false", name, ok)
	}

	var nilSet *Set
	if name, ok := nilSet.NameFor("claude/anthropic/sonnet"); ok || name != "" {
		t.Errorf("(*Set)(nil).NameFor = %q, %v, want \"\", false", name, ok)
	}
}
