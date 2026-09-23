package release

import "testing"

// TestAssetURLs pins the exact shape of both URLs: the published v0.9.0 asset
// names are relevo_v0.9.0_linux_amd64.tar.gz and so on, plus checksums.txt.
func TestAssetURLs(t *testing.T) {
	tests := []struct {
		name          string
		tag           string
		goos          string
		goarch        string
		wantArchive   string
		wantChecksums string
	}{
		{
			name:          "published linux amd64",
			tag:           "v0.9.0",
			goos:          "linux",
			goarch:        "amd64",
			wantArchive:   "https://github.com/fuad-daoud/relevo/releases/download/v0.9.0/relevo_v0.9.0_linux_amd64.tar.gz",
			wantChecksums: "https://github.com/fuad-daoud/relevo/releases/download/v0.9.0/checksums.txt",
		},
		{
			name:          "darwin arm64",
			tag:           "v1.2.3",
			goos:          "darwin",
			goarch:        "arm64",
			wantArchive:   "https://github.com/fuad-daoud/relevo/releases/download/v1.2.3/relevo_v1.2.3_darwin_arm64.tar.gz",
			wantChecksums: "https://github.com/fuad-daoud/relevo/releases/download/v1.2.3/checksums.txt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			archive, checksums := AssetURLs(tc.tag, tc.goos, tc.goarch)
			if archive != tc.wantArchive {
				t.Errorf("AssetURLs(%q, %q, %q) archive = %q, want %q", tc.tag, tc.goos, tc.goarch, archive, tc.wantArchive)
			}
			if checksums != tc.wantChecksums {
				t.Errorf("AssetURLs(%q, %q, %q) checksums = %q, want %q", tc.tag, tc.goos, tc.goarch, checksums, tc.wantChecksums)
			}
		})
	}
}
