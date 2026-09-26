package candidate

import (
	"regexp"
	"strconv"
	"strings"
)

// namePattern is the shape of a candidate name: lowercase, at most 24
// characters, and never containing "/", so a name can never be mistaken for a
// harness/provider/model token.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,23}$`)

// maxNameLen is the longest name namePattern admits.
const maxNameLen = 24

// IsName reports whether s is shaped like a candidate name (never contains "/").
func IsName(s string) bool {
	return namePattern.MatchString(s)
}

// DeriveNames returns one name per entry, in entry order. An entry with a
// non-empty Name keeps it verbatim; Parse validates it, not here.
//
// Every explicit name and every provider name is reserved first. An entry
// without a name takes the first free candidate from this order: the model's
// last "/"-segment slugged and stripped of its "#..."/":..." effort suffix,
// then that base with the effort appended, then the harness prefixed on either
// form, then the base with a numeric suffix.
func DeriveNames(entries []Candidate) []string {
	reserved := make(map[string]bool, len(entries)*2)
	for _, e := range entries {
		if e.Name != "" {
			reserved[e.Name] = true
		}
	}
	for _, e := range entries {
		if e.Provider != "" {
			reserved[e.Provider] = true
		}
	}

	names := make([]string, len(entries))
	for i, e := range entries {
		if e.Name != "" {
			names[i] = e.Name
			continue
		}
		base, effort := deriveBase(e.Model)
		name := pickName(e.Harness, base, effort, reserved)
		reserved[name] = true
		names[i] = name
	}
	return names
}

// pickName returns the first name candidate that is neither reserved nor
// rejected by namePattern, falling back to numbered suffixes on base. It does
// not reserve its result: DeriveNames reserves it.
func pickName(harnessName, base, effort string, reserved map[string]bool) string {
	tries := []string{base}
	if effort != "" {
		tries = append(tries, base+"-"+effort)
	}
	tries = append(tries, harnessName+"-"+base)
	if effort != "" {
		tries = append(tries, harnessName+"-"+base+"-"+effort)
	}
	for _, t := range tries {
		if n := truncateName(t); !reserved[n] && IsName(n) {
			return n
		}
	}

	for i := 2; ; i++ {
		suffix := "-" + strconv.Itoa(i)
		n := truncate(base, maxNameLen-len(suffix)) + suffix
		if !reserved[n] && IsName(n) {
			return n
		}
	}
}

// deriveBase returns a model's name base and the effort suffix it carried. The
// base is the model's last "/"-segment with an effort suffix ("#..." or ":...")
// removed, slugged into name characters.
func deriveBase(model string) (base, effort string) {
	seg := model
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	if i := strings.IndexAny(seg, "#:"); i >= 0 {
		effort = seg[i+1:]
		seg = seg[:i]
	}
	return slug(seg), effort
}

// slug reduces s to name characters: lowercase [a-z0-9.-], every other run of
// characters replaced by one "-", leading "-" and "." dropped, truncated to
// maxNameLen. An empty result becomes "c", the shortest valid name.
func slug(s string) string {
	s = strings.ToLower(s)

	var b strings.Builder
	inRun := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			b.WriteRune(r)
			inRun = false
			continue
		}
		if !inRun {
			b.WriteByte('-')
			inRun = true
		}
	}

	out := strings.TrimLeft(b.String(), "-.")
	out = truncate(out, maxNameLen)
	if out == "" {
		return "c"
	}
	return out
}

// truncateName cuts s to at most maxNameLen characters, on a rune boundary.
func truncateName(s string) string {
	return truncate(s, maxNameLen)
}

// truncate cuts s to at most n characters, on a rune boundary.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// isRuneStart reports whether b starts a UTF-8 rune.
func isRuneStart(b byte) bool {
	return b&0xC0 != 0x80
}
