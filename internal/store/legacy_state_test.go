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

			// A bind.json written before those states were deleted, holding
			// the legacy word as an older relevo wrote it. The import is what
			// reads it now (P3a plan §4.3).
			b := newBinding("webshop", "/repo/webshop")
			b.Round = 2
			b.State = State(legacy)
			raw, err := json.MarshalIndent(b, "", "  ")
			if err != nil {
				t.Fatalf("encode bind.json: %v", err)
			}
			if err := os.MkdirAll(s.Dir("webshop"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "webshop", "bind.json"), raw, 0o644); err != nil {
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
