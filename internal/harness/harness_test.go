package harness

import (
	"sort"
	"testing"
)

func TestHarnessRules(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() returned no entries")
	}

	// Verify All() is sorted by Kind
	if !sort.SliceIsSorted(all, func(i, j int) bool {
		return all[i].Kind < all[j].Kind
	}) {
		t.Errorf("All() is not sorted by Kind: %+v", all)
	}

	for _, h := range all {
		// An empty RolePath implies an empty RoleDoc
		if h.RolePath == "" && h.RoleDoc != "" {
			t.Errorf("kind %q has empty RolePath but non-empty RoleDoc %q", h.Kind, h.RoleDoc)
		}

		// Every entry with a non-empty RoleDoc resolves to a non-empty embedded doc
		if h.RoleDoc != "" {
			doc, err := AgentDoc(h.RoleDoc)
			if err != nil {
				t.Errorf("kind %q RoleDoc %q failed to resolve: %v", h.Kind, h.RoleDoc, err)
			}
			if len(doc) == 0 {
				t.Errorf("kind %q RoleDoc %q resolved to empty bytes", h.Kind, h.RoleDoc)
			}
		}

		// Lookup resolves to the same entry
		got, ok := Lookup(h.Kind)
		if !ok {
			t.Errorf("Lookup(%q) returned ok=false", h.Kind)
		}
		if got != h {
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
			RolePath:    "",
			RoleDoc:     "",
		},
		"claude": {
			Kind:        "claude",
			Binary:      "claude",
			Integration: "claude",
			RolePath:    ".claude/agents/plan-executor.md",
			RoleDoc:     "claude",
		},
		"opencode": {
			Kind:        "opencode",
			Binary:      "opencode",
			Integration: "opencode",
			RolePath:    ".config/opencode/agents/plan-executor.md",
			RoleDoc:     "opencode",
		},
	}

	for kind, want := range expected {
		got, ok := Lookup(kind)
		if !ok {
			t.Errorf("Lookup(%q) not found", kind)
			continue
		}
		if got != want {
			t.Errorf("Lookup(%q) = %+v, want %+v", kind, got, want)
		}
	}
}
