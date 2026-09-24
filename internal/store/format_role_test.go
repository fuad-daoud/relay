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

// TestRemoteLiveDoesNotRaiseRecordFormat pins the format-3 rule (see
// BindingFormat): RemoteLive is a poll cache -- observeRemote re-fetches it
// every tick and clears it in every non-running state -- so a binding that
// carries it is still written at the lowest format that can hold the rest of
// the record. With an empty Role that is format 1, stored as an absent
// "format" key, never 3: stamping 3 would lock an older relevo out of loading a
// running remote binding (store.go Load refuses a newer format).
func TestRemoteLiveDoesNotRaiseRecordFormat(t *testing.T) {
	s := New(t.TempDir())

	b := newBinding("remote-bind", "/home/dev/projects/remote")
	b.Builder.Mode = ModeRemote
	b.Builder.RemoteLive = &LiveFacts{PID: 4242}

	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw := bindingRecordJSON(t, s, b.Name)
	if bytes.Contains(raw, []byte(`"format"`)) {
		t.Errorf("RemoteLive (format 3) must not raise the record's format; an empty Role stays format 1 with no format key:\n%s", raw)
	}

	got, err := s.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Format != 0 {
		t.Errorf("loaded Format = %d, want 0 (the format-1 encoding), not 3", got.Format)
	}
	if got.Builder.RemoteLive == nil || got.Builder.RemoteLive.PID != 4242 {
		t.Errorf("RemoteLive did not round-trip: %+v", got.Builder.RemoteLive)
	}
}
