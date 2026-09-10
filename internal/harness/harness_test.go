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
			Kind:        "agy",
			Binary:      "agy",
			Integration: "antigravity-cli",
			Roles:       nil,
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
