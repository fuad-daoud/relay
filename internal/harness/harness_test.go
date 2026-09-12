package harness

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestHarnessRules(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() returned no entries")
	}

	if !sort.SliceIsSorted(all, func(i, j int) bool {
		return all[i].Kind < all[j].Kind
	}) {
		t.Errorf("All() is not sorted by Kind: %+v", all)
	}

	for _, h := range all {
		got, ok := Lookup(h.Kind)
		if !ok {
			t.Errorf("Lookup(%q) returned ok=false", h.Kind)
			continue
		}
		// Harness carries a Roles slice, so it is not comparable with != .
		if !reflect.DeepEqual(got, h) {
			t.Errorf("Lookup(%q) = %+v, want %+v", h.Kind, got, h)
		}
	}
}

func TestLookupUnknown(t *testing.T) {
	_, ok := Lookup("unknown-kind")
	if ok {
		t.Errorf("Lookup(unknown-kind) returned ok=true, want false")
	}
}

func TestTableExactValues(t *testing.T) {
	expected := map[string]Harness{
		"agy": {
			Kind:        "agy",
			Binary:      "agy",
			Integration: "antigravity-cli",
			MinVersion:  "1.1.6",
			SubAgents:   SubAgentsForeground,
			Roles: []Role{
				{Name: "plan-executor", Path: ".gemini/config/agents/plan-executor.md", Doc: "plan-executor.agy", ExpectModel: "inherit"},
				{Name: "researcher", Path: ".gemini/config/agents/researcher.md", Doc: "researcher.agy", ExpectModel: "inherit"},
				{Name: "reviewer", Path: ".gemini/config/agents/reviewer.md", Doc: "reviewer.agy", ExpectModel: "inherit"},
			},
		},
		"claude": {
			Kind:        "claude",
			Binary:      "claude",
			Integration: "claude",
			SubAgents:   SubAgentsSeparate,
			Roles: []Role{
				{Name: "plan-executor", Path: ".claude/agents/plan-executor.md", Doc: "plan-executor.claude"},
				{Name: "researcher", Path: ".claude/agents/researcher.md", Doc: "researcher.claude"},
				{Name: "reviewer", Path: ".claude/agents/reviewer.md", Doc: "reviewer.claude"},
			},
		},
		"opencode": {
			Kind:        "opencode",
			Binary:      "opencode",
			Integration: "opencode",
			SubAgents:   SubAgentsHidden,
			Roles: []Role{
				{Name: "plan-executor", Path: ".config/opencode/agents/plan-executor.md", Doc: "plan-executor.opencode"},
				{Name: "researcher", Path: ".config/opencode/agents/researcher.md", Doc: "researcher.opencode"},
				{Name: "reviewer", Path: ".config/opencode/agents/reviewer.md", Doc: "reviewer.opencode"},
			},
		},
	}

	for kind, want := range expected {
		got, ok := Lookup(kind)
		if !ok {
			t.Errorf("Lookup(%q) not found", kind)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Lookup(%q) = %+v, want %+v", kind, got, want)
		}
	}
}

func TestRoleTable(t *testing.T) {
	wantNames := []string{"builder", "reviewer", "researcher"}
	if got := RoleNames(); !reflect.DeepEqual(got, wantNames) {
		t.Errorf("RoleNames() = %v, want %v", got, wantNames)
	}

	b, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") returned ok=false")
	}
	if b.Shape != ShapeBuilder {
		t.Errorf("RoleByName(\"builder\").Shape = %v, want %v", b.Shape, ShapeBuilder)
	}
	if b.Definition != "plan-executor" {
		t.Errorf("RoleByName(\"builder\").Definition = %q, want %q", b.Definition, "plan-executor")
	}

	for _, name := range []string{"reviewer", "researcher"} {
		r, ok := RoleByName(name)
		if !ok {
			t.Fatalf("RoleByName(%q) returned ok=false", name)
		}
		if r.Shape != ShapeConsult {
			t.Errorf("RoleByName(%q).Shape = %v, want %v", name, r.Shape, ShapeConsult)
		}
	}

	for _, name := range RoleNames() {
		if _, ok := RoleByName(name); !ok {
			t.Fatalf("RoleByName(%q) returned ok=false", name)
		}
	}

	if _, ok := RoleByName("nope"); ok {
		t.Errorf("RoleByName(\"nope\") returned ok=true, want false")
	}
}

func TestCanServe(t *testing.T) {
	matrix := []struct {
		kind string
		role string
		want bool
	}{
		{"agy", "builder", true},
		{"agy", "reviewer", true},
		{"agy", "researcher", true},
		{"agy", "nope", false},
		{"claude", "builder", true},
		{"claude", "reviewer", true},
		{"claude", "researcher", true},
		{"claude", "nope", false},
		{"opencode", "builder", true},
		{"opencode", "reviewer", true},
		{"opencode", "researcher", true},
		{"opencode", "nope", false},
	}

	for _, tt := range matrix {
		h, ok := Lookup(tt.kind)
		if !ok {
			t.Fatalf("Lookup(%q) not found", tt.kind)
		}
		if got := h.CanServe(tt.role); got != tt.want {
			t.Errorf("Harness(%q).CanServe(%q) = %v, want %v", tt.kind, tt.role, got, tt.want)
		}
	}
}

func TestLaunch(t *testing.T) {
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}
	reviewer, ok := RoleByName("reviewer")
	if !ok {
		t.Fatal("RoleByName(\"reviewer\") not found")
	}
	researcher, ok := RoleByName("researcher")
	if !ok {
		t.Fatal("RoleByName(\"researcher\") not found")
	}

	tests := []struct {
		name     string
		kind     string
		provider string
		model    string
		extra    []string
		role     RoleSpec
		wantArgs []string
	}{
		{
			name:     "claude builder",
			kind:     "claude",
			provider: "prov",
			model:    "m/x",
			extra:    nil,
			role:     builder,
			wantArgs: []string{"--model", "m/x", "--agent", "plan-executor"},
		},
		{
			name:     "opencode builder",
			kind:     "opencode",
			provider: "prov",
			model:    "m/x",
			extra:    nil,
			role:     builder,
			wantArgs: []string{"--agent", "plan-executor", "-m", "prov/m/x"},
		},
		{
			name: "agy builder", kind: "agy", provider: "prov", model: "m/x", extra: nil, role: builder,
			wantArgs: []string{"--model", "m/x", "--agent", "plan-executor"},
		},
		{
			name: "agy builder with extra", kind: "agy", provider: "prov", model: "m/x",
			extra: []string{"--dangerously-skip-permissions"}, role: builder,
			wantArgs: []string{"--model", "m/x", "--agent", "plan-executor", "--dangerously-skip-permissions"},
		},
		{
			name: "agy researcher", kind: "agy", provider: "prov", model: "m/x", extra: nil, role: researcher,
			wantArgs: []string{"--model", "m/x", "--agent", "researcher"},
		},
		{
			name:     "claude builder with extra",
			kind:     "claude",
			provider: "prov",
			model:    "m/x",
			extra:    []string{"--auto"},
			role:     builder,
			wantArgs: []string{"--model", "m/x", "--agent", "plan-executor", "--auto"},
		},
		{
			name:     "opencode reviewer",
			kind:     "opencode",
			provider: "prov",
			model:    "m/x",
			extra:    nil,
			role:     reviewer,
			wantArgs: []string{"--agent", "reviewer", "-m", "prov/m/x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ok := Lookup(tt.kind)
			if !ok {
				t.Fatalf("Lookup(%q) not found", tt.kind)
			}
			got := h.Launch(tt.provider, tt.model, tt.extra, tt.role)
			if got.Kind != tt.kind {
				t.Errorf("Launch().Kind = %q, want %q", got.Kind, tt.kind)
			}
			if !reflect.DeepEqual(got.Args, tt.wantArgs) {
				t.Errorf("Launch().Args = %v, want %v", got.Args, tt.wantArgs)
			}
		})
	}

	// Final assertion: appending to returned Args must not mutate extra.
	extra := []string{"--a"}
	h, ok := Lookup("agy")
	if !ok {
		t.Fatal("Lookup(\"agy\") not found")
	}
	launch := h.Launch("prov", "m/x", extra, builder)
	launch.Args = append(launch.Args, "--b")
	if !reflect.DeepEqual(extra, []string{"--a"}) {
		t.Errorf("extra was modified: got %v, want [--a]", extra)
	}

	unknownHarness := Harness{Kind: "unknown"}
	launchUnknown := unknownHarness.Launch("prov", "m/x", extra, builder)
	launchUnknown.Args = append(launchUnknown.Args, "--c")
	if !reflect.DeepEqual(extra, []string{"--a"}) {
		t.Errorf("extra was modified on unknown harness: got %v, want [--a]", extra)
	}
}

// TestSubAgentsSetOnEveryKind pins the rule that "" is not a visibility
// state: an unknown kind yields "" downstream, and a known kind never may.
func TestSubAgentsSetOnEveryKind(t *testing.T) {
	valid := map[SubAgentVisibility]bool{
		SubAgentsSeparate:   true,
		SubAgentsForeground: true,
		SubAgentsHidden:     true,
	}
	for _, h := range All() {
		if !valid[h.SubAgents] {
			t.Errorf("harness %q: SubAgents = %q, want one of separate/foreground/hidden", h.Kind, h.SubAgents)
		}
	}
}

func TestLaunchPrintPerKind(t *testing.T) {
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}
	tests := []struct {
		kind       string
		extra      []string
		wantPrint  []string
		wantPrompt int
	}{
		{
			kind: "agy",
			wantPrint: []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor",
				"--output-format", "text", "--print-timeout", BudgetPlaceholder},
			wantPrompt: 1,
		},
		{
			kind: "agy", extra: []string{"--dangerously-skip-permissions"},
			wantPrint: []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor",
				"--output-format", "text", "--print-timeout", BudgetPlaceholder, "--dangerously-skip-permissions"},
			wantPrompt: 1,
		},
		{
			kind:       "claude",
			wantPrint:  []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor", "--output-format", "text"},
			wantPrompt: 1,
		},
		{
			kind:       "opencode",
			wantPrint:  []string{"run", PromptPlaceholder, "-m", "prov/m/x", "--agent", "plan-executor"},
			wantPrompt: 1,
		},
		{
			kind: "opencode", extra: []string{"--auto"},
			wantPrint:  []string{"run", PromptPlaceholder, "-m", "prov/m/x", "--agent", "plan-executor", "--auto"},
			wantPrompt: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.kind+" "+strings.Join(tt.extra, " "), func(t *testing.T) {
			h, ok := Lookup(tt.kind)
			if !ok {
				t.Fatalf("Lookup(%q) not found", tt.kind)
			}
			got := h.Launch("prov", "m/x", tt.extra, builder)
			if !reflect.DeepEqual(got.Print, tt.wantPrint) {
				t.Errorf("Print = %v, want %v", got.Print, tt.wantPrint)
			}
			if got.PromptAt != tt.wantPrompt {
				t.Errorf("PromptAt = %d, want %d", got.PromptAt, tt.wantPrompt)
			}
			if got.Print[got.PromptAt] != PromptPlaceholder {
				t.Errorf("Print[PromptAt] = %q, want the placeholder", got.Print[got.PromptAt])
			}
		})
	}

	unknown := Harness{Kind: "unknown"}
	got := unknown.Launch("prov", "m/x", []string{"--z"}, builder)
	if len(got.Print) != 0 || got.PromptAt != -1 {
		t.Errorf("unknown kind: Print = %v PromptAt = %d; want empty and -1", got.Print, got.PromptAt)
	}
}

func TestPrintArgsSubstitutesPromptAndBudgetWithoutMutating(t *testing.T) {
	builder, _ := RoleByName("builder")
	h, _ := Lookup("agy")
	extra := []string{"--dangerously-skip-permissions"}
	l := h.Launch("prov", "m/x", extra, builder)
	before := append([]string(nil), l.Print...)

	prompt := "Read /state/x/003-plan.md and write /state/x/003-report.md"
	got := l.PrintArgs(prompt, 90*time.Minute)
	want := []string{"-p", prompt, "--model", "m/x", "--agent", "plan-executor",
		"--output-format", "text", "--print-timeout", "1h30m0s", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PrintArgs = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(l.Print, before) {
		t.Errorf("PrintArgs mutated Print: %v", l.Print)
	}
	if !reflect.DeepEqual(extra, []string{"--dangerously-skip-permissions"}) {
		t.Errorf("extra was modified: %v", extra)
	}
	got[0] = "changed"
	if l.Print[0] != "-p" {
		t.Error("PrintArgs must return a fresh slice, not alias Print")
	}

	// A kind with no budget flag ignores the budget; the prompt still lands.
	c, _ := Lookup("claude")
	cl := c.Launch("prov", "m/x", nil, builder)
	got = cl.PrintArgs("hello", time.Hour)
	if !reflect.DeepEqual(got, []string{"-p", "hello", "--model", "m/x", "--agent", "plan-executor", "--output-format", "text"}) {
		t.Errorf("claude PrintArgs = %v", got)
	}
	for _, a := range got {
		if a == BudgetPlaceholder || a == PromptPlaceholder {
			t.Errorf("placeholder survived substitution: %v", got)
		}
	}

	// Unknown kind: empty in, empty out, no panic.
	if got := (Launch{Kind: "unknown", PromptAt: -1}).PrintArgs("x", time.Minute); len(got) != 0 {
		t.Errorf("unknown kind PrintArgs = %v, want empty", got)
	}
}
