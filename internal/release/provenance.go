package release

import (
	"runtime/debug"
	"strings"
)

// Kind is how the running binary was installed.
type Kind string

const (
	KindGoInstall  Kind = "go-install"
	KindLocalBuild Kind = "local-build"
	// KindRelease is a binary built by release.yml and unpacked from a
	// published archive. It is the only kind that carries the distribution
	// stamp with a clean tag.
	KindRelease Kind = "release"
	KindUnknown Kind = "unknown"
)

// DistributionRelease is the only value of main.distribution Detect
// recognises. Any other non-empty value, such as a future "homebrew", falls
// through to the existing rules.
const DistributionRelease = "release"

// Inputs is every fact Detect reads. The caller gathers them; Detect
// touches no disk and no environment, so the table in the test is the
// whole truth about this decision.
type Inputs struct {
	// Version is buildVersion(): a git describe, a module version, or "(devel)".
	Version string
	// ExeDir is filepath.Dir of the resolved executable path. Detect does not
	// read it -- it records where the executable lives, so a caller can see
	// at a glance which directory the classification is about.
	ExeDir string
	// FromModule is true when debug.ReadBuildInfo gave the version, i.e.
	// there was no ldflags stamp.
	FromModule bool
	// VCS is true when the build info carries a vcs.revision setting: the
	// binary was built from a VCS checkout, not from the module cache. Only
	// meaningful when FromModule is true; the zero value means "no VCS stamp".
	//
	// `go install ...@v0.13.0` from the module cache has no vcs.revision,
	// while `go build ./cmd/relevo` in a checkout gets a pseudo-version and
	// the setting. A `-buildvcs=false` build has no setting and reaches
	// rule 1 only as "(devel)".
	VCS bool
	// Distribution is the main.distribution ldflags stamp. Only release.yml
	// sets it, and it is empty in every other build. It is optional, and the
	// zero value means "no claim".
	Distribution string
}

// Detect classifies the install. Pure. A binary that sits next to an old
// plugin manifest is no longer special: with no manifest input it reads as
// the kind it otherwise is.
func Detect(in Inputs) Kind {
	// Rule 1: the module supplied the version. A VCS stamp or a bare
	// "(devel)" means the build came from a checkout, not the module cache
	// (#497; see the table in §1 of docs/plans/2026-09-25-provenance-vcs.md).
	if in.FromModule {
		if in.VCS || in.Version == "(devel)" {
			return KindLocalBuild
		}
		return KindGoInstall
	}
	// Rule 2: the release stamp together with a clean tag. A stamped
	// non-clean version -- a describe suffix, -dirty or (devel) -- is not a
	// release, and falls through to rule 3, which classifies it as a local
	// build.
	if in.Distribution == DistributionRelease {
		if v, ok := ParseVersion(in.Version); ok && v.Suffix == "" {
			return KindRelease
		}
	}
	if in.Version == "(devel)" {
		return KindLocalBuild
	}
	if hasDescribeSuffix(in.Version) {
		return KindLocalBuild
	}
	return KindUnknown
}

// HasVCSRevision reports whether settings carries the stamp a build made
// inside a VCS checkout gets: a non-empty vcs.revision. It is the signal that
// separates `go build` in a checkout, which has it, from `go install
// ...@v0.13.0` from the module cache, which has no settings at all. Pure: it
// takes the settings as an argument and never calls debug.ReadBuildInfo.
func HasVCSRevision(settings []debug.BuildSetting) bool {
	for _, s := range settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return true
		}
	}
	return false
}

// hasDescribeSuffix reports whether v ends in the tail a build made inside a
// git checkout gets: `git describe --tags --dirty` writes "-<N>-g<sha>", and
// a dirty checkout appends "-dirty" to that.
func hasDescribeSuffix(v string) bool {
	if strings.HasSuffix(v, "-dirty") {
		return true
	}
	sha := strings.LastIndex(v, "-")
	if sha < 0 || !strings.HasPrefix(v[sha+1:], "g") || len(v[sha+1:]) < 2 {
		return false
	}
	count := strings.LastIndex(v[:sha], "-")
	if count < 0 {
		return false
	}
	n := v[count+1 : sha]
	if n == "" {
		return false
	}
	for _, r := range n {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
