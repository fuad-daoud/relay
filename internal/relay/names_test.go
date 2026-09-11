package relay

import (
	"reflect"
	"testing"
)

// TestConsultRolesTooLong pins the boundary the note turns on: a name one
// character under the derived-name limit fits, one over does not.
func TestConsultRolesTooLong(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{"abcdefghijkl", nil},
		{"abcdefghijklm", []string{"researcher"}},
		{"abcdefghijklmn", []string{"researcher"}},
		{"abcdefghijklmno", []string{"researcher", "reviewer"}},
		// 16 + 1 + 7 + 1 + 8 = 33 would make builder "too long" too, but it
		// is not a consult role and must never appear in the note.
		{"abcdefghijklmnop", []string{"researcher", "reviewer"}},
		{"", nil},
	}

	for _, tt := range tests {
		got := ConsultRolesTooLong(tt.name)
		if len(got) == 0 && len(tt.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ConsultRolesTooLong(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
