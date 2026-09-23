package release

import "strings"

// Kind is how the running binary was installed.
type Kind string

const (
	KindGoInstall  Kind = "go-install"
	KindLocalBuild Kind = "local-build"
	KindUnknown    Kind = "unknown"
)

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
}

// Detect classifies the install. Pure. A binary that sits next to an old
// plugin manifest is no longer special: with no manifest input it reads as
// the kind it otherwise is.
func Detect(in Inputs) Kind {
	if in.FromModule {
		return KindGoInstall
	}
	if in.Version == "(devel)" {
		return KindLocalBuild
	}
	if hasDescribeSuffix(in.Version) {
		return KindLocalBuild
	}
	return KindUnknown
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
