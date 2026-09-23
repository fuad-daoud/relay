package release

import "testing"

// TestDetectTable is the classification, in order. A binary that sits next to
// an old plugin manifest is no longer special: with no manifest input it
// reads as the kind it otherwise is.
func TestDetectTable(t *testing.T) {
	tests := []struct {
		name string
		in   Inputs
		want Kind
	}{
		{
			name: "go install: the module supplied the version",
			in:   Inputs{Version: "v0.7.0", ExeDir: "/home/fuad/go/bin", FromModule: true},
			want: KindGoInstall,
		},
		{
			name: "local build: (devel) from a checkout",
			in:   Inputs{Version: "(devel)", ExeDir: "/worktrees/relay-update"},
			want: KindLocalBuild,
		},
		{
			name: "local build: describe suffix",
			in:   Inputs{Version: "v0.6.0-2-gddf3d4f", ExeDir: "/worktrees/relay-update"},
			want: KindLocalBuild,
		},
		{
			// This worktree's own `git describe --tags --dirty`.
			name: "local build: this worktree",
			in:   Inputs{Version: "v0.7.0-8-gbd8aed0", ExeDir: "/worktrees/relay-update"},
			want: KindLocalBuild,
		},
		{
			name: "local build: dirty describe",
			in:   Inputs{Version: "v0.6.0-2-gddf3d4f-dirty", ExeDir: "/worktrees/relay-update"},
			want: KindLocalBuild,
		},
		{
			// An old plugin install left ./relay beside a manifest; with the
			// manifest input gone nothing distinguishes it, so it claims nothing.
			name: "old plugin install: a clean tag claims nothing",
			in:   Inputs{Version: "v0.7.0", ExeDir: "/plugins/relay"},
			want: KindUnknown,
		},
		{
			name: "otherwise: a clean tag with no other evidence claims nothing",
			in:   Inputs{Version: "v0.7.0", ExeDir: "/usr/local/bin"},
			want: KindUnknown,
		},
		{
			name: "otherwise: empty version claims nothing",
			in:   Inputs{ExeDir: "/usr/local/bin"},
			want: KindUnknown,
		},
		{
			// The release stamp plus a clean tag: what release.yml builds.
			name: "release: stamped clean tag",
			in:   Inputs{Version: "v0.9.0", ExeDir: "/usr/local/bin", Distribution: "release"},
			want: KindRelease,
		},
		{
			// A stamp on a build inside a checkout is not a release; it is a
			// local build by the unchanged rule 3.
			name: "release stamp with a describe suffix is a local build",
			in:   Inputs{Version: "v0.9.0-3-gabc1234", ExeDir: "/worktrees/relay", Distribution: "release"},
			want: KindLocalBuild,
		},
		{
			name: "release stamp on (devel) is a local build",
			in:   Inputs{Version: "(devel)", ExeDir: "/worktrees/relay", Distribution: "release"},
			want: KindLocalBuild,
		},
		{
			// An unrecognised distribution value falls through to the
			// existing rules, so a clean tag claims nothing.
			name: "unknown distribution value claims nothing",
			in:   Inputs{Version: "v0.9.0", ExeDir: "/usr/local/bin", Distribution: "homebrew"},
			want: KindUnknown,
		},
		{
			// Rule 1 comes first: the module supplied the version, so the
			// distribution stamp does not make it a release.
			name: "go install wins over the release stamp",
			in:   Inputs{Version: "v0.9.0", ExeDir: "/home/fuad/go/bin", FromModule: true, Distribution: "release"},
			want: KindGoInstall,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(tc.in); got != tc.want {
				t.Errorf("Detect(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
