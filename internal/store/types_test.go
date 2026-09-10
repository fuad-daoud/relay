package store

import (
	"encoding/json"
	"testing"
)

func TestBindingRoundClosedTreeJSON(t *testing.T) {
	t.Run("empty omits round_closed_tree key", func(t *testing.T) {
		b := Binding{}
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if _, ok := decoded["round_closed_tree"]; ok {
			t.Errorf("expected round_closed_tree key to be omitted when empty, got JSON: %s", string(data))
		}
	})

	t.Run("non-empty includes round_closed_tree key", func(t *testing.T) {
		const treeID = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
		b := Binding{
			RoundClosedTree: treeID,
		}
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		got, ok := decoded["round_closed_tree"]
		if !ok {
			t.Fatalf("expected round_closed_tree key to be present when non-empty, got JSON: %s", string(data))
		}
		if got != treeID {
			t.Errorf("round_closed_tree = %v, want %v", got, treeID)
		}
	})
}
