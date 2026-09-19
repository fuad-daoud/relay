package classify

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestKeyFileUsable(t *testing.T) {
	cases := []struct {
		mode os.FileMode
		ok   bool
	}{
		{0o600, true},
		{0o400, true},
		{0o640, false},
		{0o644, false},
		{0o666, false},
		{0o700, true},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%04o", tc.mode), func(t *testing.T) {
			ok, reason := KeyFileUsable(tc.mode)
			if ok != tc.ok {
				t.Errorf("KeyFileUsable(%04o) ok = %v, want %v", tc.mode, ok, tc.ok)
			}
			if tc.ok {
				if reason != "" {
					t.Errorf("KeyFileUsable(%04o) unexpected reason = %q", tc.mode, reason)
				}
			} else {
				octal := fmt.Sprintf("0%o", tc.mode.Perm())
				if !strings.Contains(reason, octal) {
					t.Errorf("reason %q does not contain octal mode %q", reason, octal)
				}
				if !strings.Contains(reason, "chmod 600") {
					t.Errorf("reason %q does not contain 'chmod 600'", reason)
				}
			}
		})
	}
}
