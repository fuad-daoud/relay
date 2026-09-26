// Package release answers the two questions relevo asks about its own
// distribution without ever touching the network: how this binary was
// installed, and whether a newer release exists in an answer the
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

// IsReleaseTag reports whether s is exactly a release tag: "v", then one or
// more ASCII digits, ".", one or more ASCII digits, ".", one or more ASCII
// digits, with nothing before or after. Leading zeros are allowed; a suffix,
// a path separator or surrounding whitespace is not. It is checked byte by
// byte, because this string is about to become a path segment in a download
// URL, and it is the precondition AssetURLs and FetchBinary name.
func IsReleaseTag(s string) bool {
	if len(s) < 5 || s[0] != 'v' {
		return false
	}
	dots, digits := 0, 0
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.':
			if digits == 0 {
				return false
			}
			dots++
			digits = 0
		default:
			return false
		}
	}
	return dots == 2 && digits > 0
}

// Newer reports whether latest is strictly newer than running. It is
// false whenever either side is unparseable -- relevo never claims
// staleness it cannot prove.
//
// The numbers decide first. On equal numbers nothing is newer: a suffix on
// running (a git describe, -dirty) means the running binary is commits past
// that tag -- newer than the bare numbers, never older -- so the naive
// semver reading that would call v0.6.0-2-g... older than v0.6.0 is exactly
// the false "behind v0.6.0" relevo must not print.
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
// does not parse makes the answer false, which is how "relevo never claims
// staleness it cannot prove" is enforced when the running version is
// "(devel)" or a release tag is malformed.
func NewerStrings(running, latest string) bool {
	rv, rok := ParseVersion(running)
	lv, lok := ParseVersion(latest)
	return rok && lok && Newer(rv, lv)
}
