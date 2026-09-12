package relay

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/harness"
)

func TestSubAgentCoverage(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		vis     harness.SubAgentVisibility
		printed bool
		want    string
	}{
		{
			name: "separate prints nothing", kind: "claude", vis: harness.SubAgentsSeparate,
			printed: false, want: "",
		},
		{
			name: "foreground", kind: "agy", vis: harness.SubAgentsForeground,
			printed: true,
			want:    "sub-agents hidden: agy runs them in the builder pane; no foreign rows above does not mean the tree is clear",
		},
		{
			name: "hidden", kind: "opencode", vis: harness.SubAgentsHidden,
			printed: true,
			want:    "sub-agents hidden: opencode runs them in-process, herdr lists only the pane; no foreign rows above does not mean the tree is clear",
		},
		{
			name: "unknown kind is unverified", kind: "gemini", vis: "",
			printed: true,
			want:    `sub-agents unverified for kind "gemini"; no foreign rows above does not mean the tree is clear`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SubAgentCoverage(tc.kind, tc.vis)
			if ok != tc.printed {
				t.Fatalf("printed = %v, want %v (text %q)", ok, tc.printed, got)
			}
			if got != tc.want {
				t.Errorf("text =\n  %q\nwant\n  %q", got, tc.want)
			}
			if tc.printed && !strings.Contains(got, tc.kind) {
				t.Errorf("text must name the kind %q: %q", tc.kind, got)
			}
		})
	}
}
