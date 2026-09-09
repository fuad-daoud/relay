package harness

import (
	"errors"
	"testing"
)

func TestAgentDoc(t *testing.T) {
	tests := []struct {
		key     string
		wantErr error
	}{
		{key: "claude", wantErr: nil},
		{key: "opencode", wantErr: nil},
		{key: "agy", wantErr: ErrNoAgentDoc},
		{key: "", wantErr: ErrNoAgentDoc},
		{key: "unknown", wantErr: ErrNoAgentDoc},
	}

	for _, tc := range tests {
		doc, err := AgentDoc(tc.key)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("AgentDoc(%q) error = %v, want %v", tc.key, err, tc.wantErr)
			}
		} else {
			if err != nil {
				t.Errorf("AgentDoc(%q) unexpected error: %v", tc.key, err)
			}
			if len(doc) == 0 {
				t.Errorf("AgentDoc(%q) returned empty bytes", tc.key)
			}
		}
	}
}
