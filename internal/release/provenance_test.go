package release

import "testing"

// TestDetectTable is §4.1's six rules, in order. Swap the first two rules and
// the plugin-release row fails.
func TestDetectTable(t *testing.T) {
	tests := []struct {
		name string
		in   Inputs
		want Kind
	}{
		{
			// Both plugin variants leave ./relay beside the manifest; only the
			// release variant's version is exactly the manifest's.
			name: "plugin release: version is exactly v<manifest>",
			in:   Inputs{Version: "v0.7.0", ExeDir: "/plugins/relay", ManifestVersion: "0.7.0"},
			want: KindPluginRelease,
		},
		{
			name: "plugin source: manifest present, version is a describe",
			in:   Inputs{Version: "v0.7.0-8-gbd8aed0", ExeDir: "/plugins/relay", ManifestVersion: "0.7.0"},
			want: KindPluginSource,
		},
		{
			name: "plugin source: manifest present, version is (devel)",
			in:   Inputs{Version: "(devel)", ExeDir: "/plugins/relay", ManifestVersion: "0.7.0"},
			want: KindPluginSource,
		},
		{
			name: "go install: the module supplied the version, no manifest",
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
			name: "otherwise: a clean tag with no other evidence claims nothing",
			in:   Inputs{Version: "v0.7.0", ExeDir: "/usr/local/bin"},
			want: KindUnknown,
		},
		{
			name: "otherwise: empty version claims nothing",
			in:   Inputs{ExeDir: "/usr/local/bin"},
			want: KindUnknown,
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
