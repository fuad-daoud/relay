package harness

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestParseTier(t *testing.T) {
	good := []struct {
		in   string
		want Tier
	}{
		{"harness", TierHarness},
		{"read", TierRead},
		{"edit", TierEdit},
		{"yolo", TierYolo},
	}
	for _, tt := range good {
		got, err := ParseTier(tt.in)
		if err != nil {
			t.Errorf("ParseTier(%q) unexpected error: %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("ParseTier(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	bad := []string{"", "YOLO", "bypass", "random"}
	for _, in := range bad {
		_, err := ParseTier(in)
		if err == nil {
			t.Errorf("ParseTier(%q) expected error, got nil", in)
			continue
		}
		wantMsg := `unknown tier `
		if !strings.Contains(err.Error(), wantMsg) || !strings.Contains(err.Error(), "known: harness, read, edit, yolo") {
			t.Errorf("ParseTier(%q) error = %q, want it to name known set", in, err.Error())
		}
	}
}

func TestTierOrder(t *testing.T) {
	if TierRead.Rank() != 1 {
		t.Errorf("TierRead.Rank() = %d, want 1", TierRead.Rank())
	}
	if TierEdit.Rank() != 2 {
		t.Errorf("TierEdit.Rank() = %d, want 2", TierEdit.Rank())
	}
	if TierYolo.Rank() != 3 {
		t.Errorf("TierYolo.Rank() = %d, want 3", TierYolo.Rank())
	}
	if TierHarness.Rank() != 0 {
		t.Errorf("TierHarness.Rank() = %d, want 0", TierHarness.Rank())
	}

	if !TierEdit.Above(TierRead) {
		t.Errorf("expected TierEdit.Above(TierRead) to be true")
	}
	if !TierYolo.Above(TierEdit) {
		t.Errorf("expected TierYolo.Above(TierEdit) to be true")
	}
	if !TierYolo.Above(TierRead) {
		t.Errorf("expected TierYolo.Above(TierRead) to be true")
	}
	if TierRead.Above(TierEdit) {
		t.Errorf("TierRead.Above(TierEdit) = true, want false")
	}
	if TierRead.Above(TierRead) {
		t.Errorf("TierRead.Above(TierRead) = true, want false")
	}

	// Above returns false when either side is harness.
	harnessCases := [][2]Tier{
		{TierHarness, TierHarness},
		{TierHarness, TierRead},
		{TierHarness, TierEdit},
		{TierHarness, TierYolo},
		{TierRead, TierHarness},
		{TierEdit, TierHarness},
		{TierYolo, TierHarness},
	}
	for _, tc := range harnessCases {
		if tc[0].Above(tc[1]) {
			t.Errorf("%s.Above(%s) = true, want false", tc[0], tc[1])
		}
	}
}

func TestPermissionArgsTable(t *testing.T) {
	tests := []struct {
		kind       string
		tier       Tier
		wantArgs   []string
		wantErr    error
		wantSubstr string
	}{
		// claude
		{"claude", TierHarness, nil, nil, ""},
		{"claude", TierRead, []string{"--permission-mode", "plan"}, nil, ""},
		{"claude", TierEdit, []string{"--permission-mode", "acceptEdits"}, nil, ""},
		{"claude", TierYolo, []string{"--dangerously-skip-permissions"}, nil, ""},

		// agy
		{"agy", TierHarness, nil, nil, ""},
		{"agy", TierRead, []string{"--mode", "plan"}, nil, ""},
		{"agy", TierEdit, []string{"--mode", "accept-edits"}, nil, ""},
		{"agy", TierYolo, []string{"--dangerously-skip-permissions"}, nil, ""},

		// opencode
		{"opencode", TierHarness, nil, nil, ""},
		{"opencode", TierRead, nil, ErrTierUnsupported, ""},
		{"opencode", TierEdit, nil, ErrTierUnsupported, ""},
		{"opencode", TierYolo, []string{"--auto"}, nil, ""},

		// codex
		{"codex", TierHarness, nil, nil, ""},
		{"codex", TierRead, nil, ErrTierUnsupported, "writable_roots"},
		{"codex", TierEdit, []string{"-s", "workspace-write", "-c", StatePlaceholder}, nil, ""},
		{"codex", TierYolo, []string{"--dangerously-bypass-approvals-and-sandbox"}, nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.kind+"_"+string(tt.tier), func(t *testing.T) {
			h, ok := Lookup(tt.kind)
			if !ok {
				t.Fatalf("Lookup(%q) not found", tt.kind)
			}
			args, err := h.PermissionArgs(tt.tier)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("PermissionArgs(%s) error = %v, want %v", tt.tier, err, tt.wantErr)
				}
				if tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
					t.Errorf("PermissionArgs(%s) error = %q, want containing %q", tt.tier, err.Error(), tt.wantSubstr)
				}
			} else {
				if err != nil {
					t.Fatalf("PermissionArgs(%s) unexpected error: %v", tt.tier, err)
				}
				if !reflect.DeepEqual(args, tt.wantArgs) {
					t.Errorf("PermissionArgs(%s) = %v, want %v", tt.tier, args, tt.wantArgs)
				}
			}
		})
	}
}

func TestLaunchAppliesTier(t *testing.T) {
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}
	reviewer, ok := RoleByName("reviewer")
	if !ok {
		t.Fatal("RoleByName(\"reviewer\") not found")
	}

	// claude builder at edit -> Args and Print both contain --permission-mode acceptEdits after --agent plan-executor and before extra
	c, _ := Lookup("claude")
	extra := []string{"--some-flag"}
	cl, err := c.Launch("prov", "m/x", extra, builder, TierEdit)
	if err != nil {
		t.Fatalf("claude Launch edit error: %v", err)
	}
	wantClaudeArgs := []string{"--model", "m/x", "--agent", "plan-executor", "--permission-mode", "acceptEdits", "--some-flag"}
	if !reflect.DeepEqual(cl.Args, wantClaudeArgs) {
		t.Errorf("claude Args = %v, want %v", cl.Args, wantClaudeArgs)
	}
	wantClaudePrint := []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor",
		"--output-format", "stream-json", "--verbose", "--permission-mode", "acceptEdits", "--some-flag"}
	if !reflect.DeepEqual(cl.Print, wantClaudePrint) {
		t.Errorf("claude Print = %v, want %v", cl.Print, wantClaudePrint)
	}

	// agy reviewer at read -> --mode plan
	a, _ := Lookup("agy")
	al, err := a.Launch("prov", "m/x", nil, reviewer, TierRead)
	if err != nil {
		t.Fatalf("agy Launch read error: %v", err)
	}
	wantAgyArgs := []string{"--model", "m/x", "--agent", "reviewer", "--mode", "plan"}
	if !reflect.DeepEqual(al.Args, wantAgyArgs) {
		t.Errorf("agy Args = %v, want %v", al.Args, wantAgyArgs)
	}

	// opencode at yolo -> --auto
	o, _ := Lookup("opencode")
	ol, err := o.Launch("prov", "m/x", nil, builder, TierYolo)
	if err != nil {
		t.Fatalf("opencode Launch yolo error: %v", err)
	}
	wantOpencodeArgs := []string{"--agent", "plan-executor", "-m", "prov/m/x", "--auto"}
	if !reflect.DeepEqual(ol.Args, wantOpencodeArgs) {
		t.Errorf("opencode Args = %v, want %v", ol.Args, wantOpencodeArgs)
	}
}

func TestLaunchHarnessTierUnchanged(t *testing.T) {
	builder, _ := RoleByName("builder")

	tests := []struct {
		kind      string
		provider  string
		model     string
		extra     []string
		wantArgs  []string
		wantPrint []string
	}{
		{
			kind:      "agy",
			provider:  "prov",
			model:     "m/x",
			extra:     nil,
			wantArgs:  []string{"--model", "m/x", "--agent", "plan-executor"},
			wantPrint: []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor", "--output-format", "stream-json", "--print-timeout", BudgetPlaceholder, "--add-dir", DirPlaceholder},
		},
		{
			kind:      "claude",
			provider:  "prov",
			model:     "m/x",
			extra:     nil,
			wantArgs:  []string{"--model", "m/x", "--agent", "plan-executor"},
			wantPrint: []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor", "--output-format", "stream-json", "--verbose"},
		},
		{
			kind:      "opencode",
			provider:  "prov",
			model:     "m/x",
			extra:     nil,
			wantArgs:  []string{"--agent", "plan-executor", "-m", "prov/m/x"},
			wantPrint: []string{"run", PromptPlaceholder, "-m", "prov/m/x", "--agent", "plan-executor", "--format", "json"},
		},
	}

	for _, tt := range tests {
		h, ok := Lookup(tt.kind)
		if !ok {
			t.Fatalf("Lookup(%q) not found", tt.kind)
		}
		got, err := h.Launch(tt.provider, tt.model, tt.extra, builder, TierHarness)
		if err != nil {
			t.Fatalf("Launch(%s, TierHarness) error: %v", tt.kind, err)
		}
		if !reflect.DeepEqual(got.Args, tt.wantArgs) {
			t.Errorf("%s Args = %v, want %v", tt.kind, got.Args, tt.wantArgs)
		}
		if !reflect.DeepEqual(got.Print, tt.wantPrint) {
			t.Errorf("%s Print = %v, want %v", tt.kind, got.Print, tt.wantPrint)
		}
	}
}

func TestLaunchRefusesExtraArgsPermissionFlag(t *testing.T) {
	builder, _ := RoleByName("builder")

	// agy extra --dangerously-skip-permissions at edit -> ErrExtraArgsPermission naming it
	agy, _ := Lookup("agy")
	_, err := agy.Launch("prov", "m/x", []string{"--dangerously-skip-permissions"}, builder, TierEdit)
	if !errors.Is(err, ErrExtraArgsPermission) {
		t.Fatalf("agy Launch edit with extra perm flag error = %v, want ErrExtraArgsPermission", err)
	}
	if !strings.Contains(err.Error(), "--dangerously-skip-permissions") {
		t.Errorf("error %q should name --dangerously-skip-permissions", err.Error())
	}

	// same extra at harness -> ok and the flag present once
	l, err := agy.Launch("prov", "m/x", []string{"--dangerously-skip-permissions"}, builder, TierHarness)
	if err != nil {
		t.Fatalf("agy Launch harness with extra perm flag unexpected error: %v", err)
	}
	count := 0
	for _, a := range l.Args {
		if a == "--dangerously-skip-permissions" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("flag --dangerously-skip-permissions present %d times, want 1", count)
	}

	// claude extra --permission-mode=plan (equals form) at read -> refused
	claude, _ := Lookup("claude")
	_, err = claude.Launch("prov", "m/x", []string{"--permission-mode=plan"}, builder, TierRead)
	if !errors.Is(err, ErrExtraArgsPermission) {
		t.Fatalf("claude Launch read with extra perm flag error = %v, want ErrExtraArgsPermission", err)
	}
	if !strings.Contains(err.Error(), "--permission-mode=plan") {
		t.Errorf("error %q should name --permission-mode=plan", err.Error())
	}

	// codex extra -s read-only at edit -> refused
	codex, _ := Lookup("codex")
	_, err = codex.Launch("prov", "m/x", []string{"-s", "read-only"}, builder, TierEdit)
	if !errors.Is(err, ErrExtraArgsPermission) {
		t.Fatalf("codex Launch edit with extra perm flag error = %v, want ErrExtraArgsPermission", err)
	}
	if !strings.Contains(err.Error(), "-s") {
		t.Errorf("error %q should name -s", err.Error())
	}

	// same extra at harness -> ok, extra passed through
	cl, err := codex.Launch("prov", "m/x", []string{"-s", "read-only"}, builder, TierHarness)
	if err != nil {
		t.Fatalf("codex Launch harness with extra perm flag unexpected error: %v", err)
	}
	if len(cl.Args) < 2 || cl.Args[len(cl.Args)-2] != "-s" || cl.Args[len(cl.Args)-1] != "read-only" {
		t.Errorf("codex Args should end with -s read-only: %v", cl.Args)
	}
}

func TestDenialPatternsSetOnEveryKind(t *testing.T) {
	for _, h := range All() {
		if len(h.DenialPatterns) < 1 {
			t.Errorf("harness %q: len(DenialPatterns) = %d, want >= 1", h.Kind, len(h.DenialPatterns))
		}
	}
}

func TestDenialPatternsCompile(t *testing.T) {
	for _, h := range All() {
		for _, pat := range h.DenialPatterns {
			if _, err := regexp.Compile(pat); err != nil {
				t.Errorf("harness %q: pattern %q failed to compile: %v", h.Kind, pat, err)
			}
		}
	}
}
