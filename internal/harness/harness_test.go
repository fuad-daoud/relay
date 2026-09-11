package harness

import (
	"reflect"
	"sort"
	"testing"
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
			Kind:                  "agy",
			Binary:                "agy",
			Integration:           "antigravity-cli",
			Roles:                 nil,
			SelectsRoleByPreamble: true,
		},
		"claude": {
			Kind:        "claude",
			Binary:      "claude",
			Integration: "claude",
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
		r, ok := RoleByName(name)
		if !ok {
			t.Fatalf("RoleByName(%q) returned ok=false", name)
		}
		if r.Preamble == "" {
			t.Errorf("RoleByName(%q).Preamble is empty", name)
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

	agy, ok := Lookup("agy")
	if !ok {
		t.Fatal("Lookup(\"agy\") not found")
	}
	if !agy.SelectsRoleByPreamble {
		t.Errorf("Lookup(\"agy\").SelectsRoleByPreamble = false, want true")
	}

	for _, kind := range []string{"claude", "opencode"} {
		h, ok := Lookup(kind)
		if !ok {
			t.Fatalf("Lookup(%q) not found", kind)
		}
		if h.SelectsRoleByPreamble {
			t.Errorf("Lookup(%q).SelectsRoleByPreamble = true, want false", kind)
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

	tests := []struct {
		name         string
		kind         string
		provider     string
		model        string
		extra        []string
		role         RoleSpec
		wantArgs     []string
		wantPreamble string
	}{
		{
			name:         "claude builder",
			kind:         "claude",
			provider:     "prov",
			model:        "m/x",
			extra:        nil,
			role:         builder,
			wantArgs:     []string{"--model", "m/x", "--agent", "plan-executor"},
			wantPreamble: "",
		},
		{
			name:         "opencode builder",
			kind:         "opencode",
			provider:     "prov",
			model:        "m/x",
			extra:        nil,
			role:         builder,
			wantArgs:     []string{"--agent", "plan-executor", "-m", "prov/m/x"},
			wantPreamble: "",
		},
		{
			name:         "agy builder",
			kind:         "agy",
			provider:     "prov",
			model:        "m/x",
			extra:        nil,
			role:         builder,
			wantArgs:     []string{"--model", "m/x"},
			wantPreamble: builder.Preamble,
		},
		{
			name:         "agy builder with extra",
			kind:         "agy",
			provider:     "prov",
			model:        "m/x",
			extra:        []string{"--dangerously-skip-permissions"},
			role:         builder,
			wantArgs:     []string{"--model", "m/x", "--dangerously-skip-permissions"},
			wantPreamble: builder.Preamble,
		},
		{
			name:         "claude builder with extra",
			kind:         "claude",
			provider:     "prov",
			model:        "m/x",
			extra:        []string{"--auto"},
			role:         builder,
			wantArgs:     []string{"--model", "m/x", "--agent", "plan-executor", "--auto"},
			wantPreamble: "",
		},
		{
			name:         "opencode reviewer",
			kind:         "opencode",
			provider:     "prov",
			model:        "m/x",
			extra:        nil,
			role:         reviewer,
			wantArgs:     []string{"--agent", "reviewer", "-m", "prov/m/x"},
			wantPreamble: "",
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
			if got.Preamble != tt.wantPreamble {
				t.Errorf("Launch().Preamble = %q, want %q", got.Preamble, tt.wantPreamble)
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
