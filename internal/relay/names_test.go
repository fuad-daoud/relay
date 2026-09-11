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
