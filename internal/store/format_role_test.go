package store

import (
	"bytes"
	"testing"
)

type formatCase struct {
	name       string
	mutate     func(*Binding)
	wantFormat string // "" means the "format" key must be absent
	wantRole   string // "" means the "role" key must be absent
	verify     func(*testing.T, Binding)
}

func formatCases() []formatCase {
	return []formatCase{
		{
			name:   "a builder binding stays format 1",
			mutate: func(*Binding) {},
			verify: func(t *testing.T, got Binding) {
				if got.Role != "" {
					t.Errorf("builder Role = %q, want empty", got.Role)
				}
			},
		},
		{
			name:       "a custom role is format 2",
			mutate:     func(b *Binding) { b.Role = "ui-builder" },
			wantFormat: `"format":2`,
			wantRole:   `"role":"ui-builder"`,
			verify: func(t *testing.T, got Binding) {
				if got.Role != "ui-builder" {
					t.Errorf("Role = %q, want ui-builder", got.Role)
				}
			},
		},
		{
			name: "RemoteLive is a poll cache and does not raise the format",
			mutate: func(b *Binding) {
				b.Builder.Mode = ModeRemote
				b.Builder.RemoteLive = &LiveFacts{PID: 4242}
			},
			verify: func(t *testing.T, got Binding) {
				if got.Format != 0 {
					t.Errorf("loaded Format = %d, want 0, not 3", got.Format)
				}
				if got.Builder.RemoteLive == nil || got.Builder.RemoteLive.PID != 4242 {
					t.Errorf("RemoteLive did not round-trip: %+v", got.Builder.RemoteLive)
				}
			},
		},
		{
			name: "StreamStart is a byte offset and does not raise the format",
			mutate: func(b *Binding) {
				b.Builder.Mode = ModeHeadless
				b.Builder.StreamStart = 1024
			},
			verify: func(t *testing.T, got Binding) {
				if got.Format != 0 {
					t.Errorf("loaded Format = %d, want 0, not 4", got.Format)
				}
				if got.Builder.StreamStart != 1024 {
					t.Errorf("StreamStart did not round-trip: %d", got.Builder.StreamStart)
				}
			},
		},
	}
}

// TestSaveWritesTheLowestFormatThatHoldsTheRecord pins the format rule: a
// binding is written at the lowest format that can hold it. A builder binding
// (Role "") and one carrying only poll caches (RemoteLive, StreamStart) stay
// format 1, stored as an absent "format" key, so an older relevo can still
// load them; a custom role is format 2.
func TestSaveWritesTheLowestFormatThatHoldsTheRecord(t *testing.T) {
	for _, tc := range formatCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := New(t.TempDir())
			b := newBinding("webshop", "/home/dev/projects/webshop")
			tc.mutate(&b)
			if err := s.Save(b); err != nil {
				t.Fatalf("Save: %v", err)
			}
			raw := bindingRecordJSON(t, s, b.Name)
			checkBindingKey(t, raw, "format", tc.wantFormat)
			checkBindingKey(t, raw, "role", tc.wantRole)

			got, err := s.Load(b.Name)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tc.verify(t, got)
		})
	}
}

// checkBindingKey asserts raw carries want -- a fragment, or "" for a key that
// must be absent.
func checkBindingKey(t *testing.T, raw []byte, key, want string) {
	t.Helper()
	if want == "" {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Errorf("record must carry no %s key:\n%s", key, raw)
		}
		return
	}
	if !bytes.Contains(raw, []byte(want)) {
		t.Errorf("record lacks %s:\n%s", want, raw)
	}
}
