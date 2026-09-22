// Package release answers the two questions relay asks about its own
// distribution without ever touching the network: how this binary was
// installed (#293), and whether a newer release exists in an answer the
// daemon cached. The fetch itself sits behind an interface in fetch.go.
package release

import (
	"strconv"
	"strings"
)

// Version is a parsed vMAJOR.MINOR.PATCH. Pre-release and build metadata
// are kept verbatim in Suffix.
type Version struct {
	Major, Minor, Patch int
	Suffix              string // "" for a clean release; "-2-gddf3d4f", "-dirty", "-rc1"
}

// ParseVersion accepts "v1.2.3", "1.2.3" and "v1.2.3-2-gabc1234".
// ok is false for "(devel)" and anything unparseable.
func ParseVersion(s string) (v Version, ok bool) {
	rest := strings.TrimPrefix(s, "v")

	// Everything from the first "-" or "+" on is suffix: a git describe tail,
	// "-dirty", or a pre-release. The numbers themselves are never signed, so
	// splitting there cannot eat a digit.
	num, suffix := rest, ""
	if i := strings.IndexAny(rest, "-+"); i >= 0 {
		num, suffix = rest[:i], rest[i:]
	}

	parts := strings.Split(num, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, false
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Suffix: suffix}, true
}

// Newer reports whether latest is strictly newer than running. It is
// false whenever either side is unparseable -- relay never claims
// staleness it cannot prove.
//
// The numbers decide first. On equal numbers nothing is newer: a suffix on
// running (a git describe, -dirty) means the running binary is commits past
// that tag -- newer than the bare numbers, never older -- so the naive
// semver reading that would call v0.6.0-2-g... older than v0.6.0 is exactly
// the false "behind v0.6.0" relay must not print.
func Newer(running, latest Version) bool {
	switch {
	case running.Major != latest.Major:
		return running.Major < latest.Major
	case running.Minor != latest.Minor:
		return running.Minor < latest.Minor
	case running.Patch != latest.Patch:
		return running.Patch < latest.Patch
	}
	return false
}

// NewerStrings is Newer for the string form both callers hold: a side that
// does not parse makes the answer false, which is how "relay never claims
// staleness it cannot prove" is enforced when the running version is
// "(devel)" or a release tag is malformed.
func NewerStrings(running, latest string) bool {
	rv, rok := ParseVersion(running)
	lv, lok := ParseVersion(latest)
	return rok && lok && Newer(rv, lv)
}
