// Package relay: the limit gate (#140). Two rules hold everywhere in this
// file: the text relay reads is scanned only at the five decision points a
// round has already stopped at (never on a tick of a running builder), and
// when more than one line in that text matches a pattern, the last one --
// the most recent -- wins.
package relay

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// limitScanLines is how many trailing lines of a builder's output a
// decision point scans for rate-limit text.
const limitScanLines = 40

// LimitMatch is one rate-limit pattern match against a builder's output.
type LimitMatch struct {
	Line   string    // the matched line, trimmed, capped at 200 runes
	Until  time.Time // gate end, UTC
	Parsed bool      // Until came from Line, not from the fallback
}

// durationRe matches "resets in 2h48m52s", "try again in 5 min", "retry
// after 30s" and captures the duration component run.
var durationRe = regexp.MustCompile(`(?i)(?:resets?|try again|retry)\s+(?:in|after)\s+~?((?:\d+\s*(?:hours?|hr|h|minutes?|min|m|seconds?|sec|s)\s*)+)`)

// durationComponentRe pulls one "<number><unit>" component at a time out of
// the captured run above. Units are checked by first letter (h/m/s), so the
// alternation only needs to avoid a short form swallowing a longer one.
var durationComponentRe = regexp.MustCompile(`(?i)(\d+)\s*(hours?|hr|h|minutes?|min|m|seconds?|sec|s)`)

// clockRe matches "resets 7pm", "resets at 23:30", "resets ~00:26".
var clockRe = regexp.MustCompile(`(?i)resets?\s+(?:at\s+)?~?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?`)

// matchLimit scans text line by line from the last line backwards and
// returns the first (i.e. most recent) line any pattern matches. Until is
// parseReset(line, now) when that succeeds, else now.Add(fallback). ok is
// false when no line matches or patterns is empty. Never errors.
func matchLimit(text string, patterns []*regexp.Regexp, now time.Time, fallback time.Duration) (LimitMatch, bool) {
	if len(patterns) == 0 {
		return LimitMatch{}, false
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		matched := false
		for _, p := range patterns {
			if p.MatchString(line) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		trimmed := strings.TrimSpace(line)
		runes := []rune(trimmed)
		if len(runes) > 200 {
			runes = runes[:200]
		}
		capped := string(runes)
		until, parsed := parseReset(capped, now)
		if !parsed {
			until = now.Add(fallback)
		}
		return LimitMatch{Line: capped, Until: until.UTC(), Parsed: parsed}, true
	}
	return LimitMatch{}, false
}

// parseReset tries two forms, in this order, on the matched line only: a
// duration ("resets in 2h48m52s") and a clock time ("resets 7pm", "resets
// at 23:30"). Either result must lie in (now, now+7d]; anything else is
// ok=false -- garbage in a line that happened to match the limit pattern
// must not gate a provider indefinitely. The returned time is UTC.
func parseReset(line string, now time.Time) (time.Time, bool) {
	if m := durationRe.FindStringSubmatch(line); m != nil {
		var d time.Duration
		for _, c := range durationComponentRe.FindAllStringSubmatch(m[1], -1) {
			n, err := strconv.Atoi(c[1])
			if err != nil {
				continue
			}
			switch unit := strings.ToLower(c[2]); unit[0] {
			case 'h':
				d += time.Duration(n) * time.Hour
			case 'm':
				d += time.Duration(n) * time.Minute
			case 's':
				d += time.Duration(n) * time.Second
			}
		}
		t := now.Add(d)
		if !inLimitWindow(t, now) {
			return time.Time{}, false
		}
		return t.UTC(), true
	}

	if m := clockRe.FindStringSubmatch(line); m != nil {
		hour, err := strconv.Atoi(m[1])
		if err != nil {
			return time.Time{}, false
		}
		minute := 0
		if m[2] != "" {
			minute, err = strconv.Atoi(m[2])
			if err != nil {
				return time.Time{}, false
			}
		}
		if minute < 0 || minute > 59 {
			return time.Time{}, false
		}
		switch ampm := strings.ToLower(m[3]); ampm {
		case "am":
			if hour < 1 || hour > 12 {
				return time.Time{}, false
			}
			if hour == 12 {
				hour = 0
			}
		case "pm":
			if hour < 1 || hour > 12 {
				return time.Time{}, false
			}
			if hour != 12 {
				hour += 12
			}
		default:
			if hour < 0 || hour > 23 {
				return time.Time{}, false
			}
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		if !inLimitWindow(t, now) {
			return time.Time{}, false
		}
		return t.UTC(), true
	}

	return time.Time{}, false
}

// inLimitWindow is parseReset's (now, now+7d] bound.
func inLimitWindow(t, now time.Time) bool {
	return t.After(now) && !t.After(now.Add(7*24*time.Hour))
}
