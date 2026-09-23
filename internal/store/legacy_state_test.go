package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadMapsHeldAndOrphanedToActive is the plan's required case for #303
// §1: the states this version deleted still load. A bind.json written with
// state "held" (a pane payload in flight) or "orphaned" (the planner's
// session gone) reads back as active, so an old binding keeps working instead
// of being refused.
func TestLoadMapsHeldAndOrphanedToActive(t *testing.T) {
	for _, legacy := range []string{"held", "orphaned"} {
		t.Run(legacy, func(t *testing.T) {
			root := t.TempDir()
			s := New(root)

			// Write the binding through the store so every other field is
			// what a real bind.json holds, then rewrite its state to the
			// legacy word exactly as an older relay wrote it.
			b := Binding{Name: "webshop", CWD: "/repo/webshop", Round: 2}
			if err := s.Save(b); err != nil {
				t.Fatalf("Save: %v", err)
			}
			path := filepath.Join(root, "webshop", "bind.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read bind.json: %v", err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("decode bind.json: %v", err)
			}
			doc["state"] = legacy
			patched, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("encode bind.json: %v", err)
			}
			if err := os.WriteFile(path, patched, 0o644); err != nil {
				t.Fatalf("write bind.json: %v", err)
			}

			got, err := s.Load("webshop")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got.State != StateActive {
				t.Errorf("state %q loaded as %q, want active", legacy, got.State)
			}
		})
	}
}

// TestLoadKeepsKnownStates is the other side of that mapping: a state this
// version does know is not rewritten on load.
func TestLoadKeepsKnownStates(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(Binding{Name: "webshop", CWD: "/repo/webshop", State: StateNeedsYou}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != StateNeedsYou {
		t.Errorf("State = %q, want the stored needs_you", got.State)
	}
}
