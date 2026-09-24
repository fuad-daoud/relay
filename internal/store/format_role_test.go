package store

import (
	"bytes"
	"testing"
)

// TestSaveFormatFollowsRole pins #382 §5.2's "lowest format that can hold the
// record": a builder binding (Role "") is written at format 1 with no "role"
// key, and no "format" key either, so it stays byte-identical to a binding
// written before the field existed; a custom-role binding is written at format
// 2 with its role. Both read back with the role they were saved with.
func TestSaveFormatFollowsRole(t *testing.T) {
	s := New(t.TempDir())

	builder := newBinding("builder-bind", "/home/dev/projects/one")
	if err := s.Save(builder); err != nil {
		t.Fatalf("Save(builder): %v", err)
	}
	raw := bindingRecordJSON(t, s, builder.Name)
	if bytes.Contains(raw, []byte(`"format"`)) {
		t.Errorf("a builder binding must carry no format key:\n%s", raw)
	}
	if bytes.Contains(raw, []byte(`"role"`)) {
		t.Errorf("a builder binding must carry no role key:\n%s", raw)
	}
	gotBuilder, err := s.Load(builder.Name)
	if err != nil {
		t.Fatalf("Load(builder): %v", err)
	}
	if gotBuilder.Role != "" {
		t.Errorf("builder Role = %q, want empty", gotBuilder.Role)
	}

	custom := newBinding("ui-bind", "/home/dev/projects/two")
	custom.Role = "ui-builder"
	if err := s.Save(custom); err != nil {
		t.Fatalf("Save(ui-builder): %v", err)
	}
	raw = bindingRecordJSON(t, s, custom.Name)
	if !bytes.Contains(raw, []byte(`"format":2`)) {
		t.Errorf("a custom-role binding must be format 2:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte(`"role":"ui-builder"`)) {
		t.Errorf("a custom-role binding must carry its role:\n%s", raw)
	}
	gotCustom, err := s.Load(custom.Name)
	if err != nil {
		t.Fatalf("Load(ui-builder): %v", err)
	}
	if gotCustom.Role != "ui-builder" {
		t.Errorf("custom Role = %q, want ui-builder", gotCustom.Role)
	}
}
