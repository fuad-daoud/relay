// Package relevo: the limit gate (#140). Two rules hold everywhere in this
// file: the text relevo reads is scanned only at the five decision points a
// round has already stopped at (never on a tick of a running builder), and
// when more than one line in that text matches a pattern, the last one --
// the most recent -- wins.
package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/ledger"
	"github.com/fuad-daoud/relevo/internal/store"
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

// dateRe matches the absolute-date form codex's weekly limit uses: "try
// again at Oct 19th, 2026 7:14 PM", "resets on October 3, 2026". The capture
// groups are, in order: month, day, year (may be empty), hour, minute and
// am/pm (each may be empty when the line names no time).
var dateRe = regexp.MustCompile(`(?i)(?:try again|resets?)\s+(?:at|on)\s+~?(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?\s+(\d{1,2})(?:st|nd|rd|th)?(?:\s*,?\s*(\d{4}))?(?:\s*,?\s*(?:at\s+)?(\d{1,2}):(\d{2})\s*(am|pm)?)?`)

// monthByName maps a month's lower-cased 3-letter prefix to its time.Month,
// so both "sept" and "september" resolve through "sep".
var monthByName = map[string]time.Month{
	"jan": time.January,
	"feb": time.February,
	"mar": time.March,
	"apr": time.April,
	"may": time.May,
	"jun": time.June,
	"jul": time.July,
	"aug": time.August,
	"sep": time.September,
	"oct": time.October,
	"nov": time.November,
	"dec": time.December,
}

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

// parseReset tries three forms, in this order, on the matched line only: a
// duration ("resets in 2h48m52s"), an absolute date ("try again at Oct 19th,
// 2026 7:14 PM"), and a clock time ("resets 7pm", "resets at 23:30"). A
// duration and a clock time must lie in (now, now+7d]; a date that names a
// year may lie in (now, now+31d], because a full date is hard to misread.
// Anything else is ok=false -- garbage in a line that happened to match the
// limit pattern must not gate a provider indefinitely. The returned time is
// UTC.
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
		if !inLimitWindow(t, now, limitWindowShort) {
			return time.Time{}, false
		}
		return t.UTC(), true
	}

	if m := dateRe.FindStringSubmatch(line); m != nil {
		key := strings.ToLower(m[1])
		if len(key) > 3 {
			key = key[:3]
		}
		month, known := monthByName[key]
		if !known {
			return time.Time{}, false
		}
		day, err := strconv.Atoi(m[2])
		if err != nil || day < 1 || day > 31 {
			return time.Time{}, false
		}

		hour, minute := 0, 0
		if m[4] != "" {
			hour, err = strconv.Atoi(m[4])
			if err != nil {
				return time.Time{}, false
			}
			minute, err = strconv.Atoi(m[5])
			if err != nil {
				return time.Time{}, false
			}
			if minute < 0 || minute > 59 {
				return time.Time{}, false
			}
			switch ampm := strings.ToLower(m[6]); ampm {
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
		}

		loc := now.Location() // codex prints the machine's local time
		var t time.Time
		var window time.Duration
		if m[3] != "" {
			year, err := strconv.Atoi(m[3])
			if err != nil {
				return time.Time{}, false
			}
			t = time.Date(year, month, day, hour, minute, 0, 0, loc)
			if t.Day() != day {
				return time.Time{}, false
			}
			window = limitWindowDated
		} else {
			t = time.Date(now.Year(), month, day, hour, minute, 0, 0, loc)
			if t.Day() != day {
				return time.Time{}, false
			}
			if !t.After(now) {
				t = time.Date(now.Year()+1, month, day, hour, minute, 0, 0, loc)
				if t.Day() != day {
					return time.Time{}, false
				}
			}
			window = limitWindowShort
		}
		if !inLimitWindow(t, now, window) {
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
		if !inLimitWindow(t, now, limitWindowShort) {
			return time.Time{}, false
		}
		return t.UTC(), true
	}

	return time.Time{}, false
}

// limitWindowShort bounds the vaguer reset forms -- a duration and a clock
// time, both easy to misread -- to (now, now+7d].
const limitWindowShort = 7 * 24 * time.Hour

// limitWindowDated bounds a reset that names a full date with a year: a
// full calendar date is hard to misread, so it earns the longer window.
const limitWindowDated = 31 * 24 * time.Hour

// inLimitWindow is parseReset's (now, now+max] bound.
func inLimitWindow(t, now time.Time, max time.Duration) bool {
	return t.After(now) && !t.After(now.Add(max))
}

// limitPatterns is the harness defaults for token's kind followed by the
// candidate's own limit_patterns, each compiled. Empty when token does not
// resolve to a configured candidate (an adopted builder) -- and then no
// decision point matches anything. Compiled on each call; decision points
// fire at most once per round, so caching buys nothing.
//
// A candidate pattern that fails to compile here cannot happen -- Load
// already refused the file -- so a pattern that somehow doesn't compile is
// skipped defensively rather than panicking.
func limitPatterns(rt Runtime, token string) []*regexp.Regexp {
	if rt.Candidates == nil {
		return nil
	}
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return nil
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		return nil
	}

	var raw []string
	if h, ok := harness.Lookup(c.Harness); ok {
		raw = append(raw, h.LimitPatterns...)
	}
	raw = append(raw, c.LimitPatterns...)

	var compiled []*regexp.Regexp
	for _, p := range raw {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}

// limitText is the text a decision point scans for rate-limit patterns: the
// tail of the log. A local builder is always headless since #303.
func limitText(ctx context.Context, rt Runtime, b store.Binding) string {
	return logTail(b.Builder.LogPath, limitScanLines)
}

// gateOnLimit is the one helper every decision point calls (spec §4.4).
// Preconditions: the round is open and the caller holds the store lock.
//
// It applies the switchable guard itself -- the same one the existing
// gatedBuilder triggers use -- and returns handled=false without reading the
// ledger when it fails: an adopted builder is never gated by relevo, and no
// call site has to repeat the check.
//
// On a match it records one rate_limited ledger entry (source relevo), warns,
// marks a headless builder's log, then checks whether this round already has
// a report on disk: if so the gate is recorded but the round is left for the
// caller to close as it would have (handled=false, m.Line set); otherwise it
// switches the builder uncounted (handled=true) and returns the replacement.
func gateOnLimit(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, text string, closeOld bool) (next store.Binding, m LimitMatch, handled bool, err error) {
	switchable := b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()
	if !switchable {
		return b, LimitMatch{}, false, nil
	}

	now := rt.Now()
	patterns := limitPatterns(rt, b.BuilderCandidate)
	m, ok := matchLimit(text, patterns, now, rt.Policy.LimitGateDefault())
	if !ok {
		return b, LimitMatch{}, false, nil
	}

	entry := ledger.Entry{
		Kind:    ledger.RateLimited,
		Subject: providerOf(b.BuilderCandidate),
		At:      now,
		Until:   m.Until,
		Note:    m.Line,
		Source:  "relevo",
		Binding: b.Name,
	}
	if err := appendEntryLocked(rt, entry); err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not record rate limit gate: %v\n", err)
	}

	slog.Warn("provider rate-limited",
		"binding", b.Name, "round", b.Round, "provider", entry.Subject,
		"until", m.Until, "parsed", m.Parsed, "line", m.Line)

	if b.Builder.Headless() {
		appendLogMarker(b.Builder.LogPath, now, "rate-limited: "+m.Line)
	}

	if _, _, ok, _ := rt.Store.StatFile(rt.Store.ReportPath(b.Name, b.Round)); ok {
		return b, m, false, nil
	}

	next, err = switchBuilder(ctx, rt, tx, b, "rate-limited: "+m.Line, closeOld, false)
	return next, m, true, err
}
