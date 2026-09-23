package main

import (
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/release"
)

// TestReleaseWorkflowStampsDistribution guards the three links between
// release.yml and the code that tells a release binary it is one: the
// distribution stamp, the archive name AssetURLs formats, and the checksums
// file the doctor's fix points at. It reads the workflow from the repo and
// nothing else -- no network, no subcommand, no harness.
func TestReleaseWorkflowStampsDistribution(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatalf("read release.yml: %v", err)
	}
	workflow := string(raw)

	links := []struct {
		what string
		sub  string
	}{
		{
			what: "the stamp",
			sub:  "-X main.distribution=" + release.DistributionRelease,
		},
		{
			what: "the archive name",
			sub:  "relevo_${version}_${goos}_${goarch}.tar.gz",
		},
		{
			what: "the checksum file",
			sub:  "checksums.txt",
		},
	}

	for _, link := range links {
		if !strings.Contains(workflow, link.sub) {
			t.Errorf("release.yml lost %s: %q is not in the workflow", link.what, link.sub)
		}
	}
}
