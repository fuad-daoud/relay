package relay

import (
	"context"
	"log/slog"
	"regexp"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
)

// dialogScanLines is how many lines are read from the visible source for the
// unknown guard.
const dialogScanLines = 30

// dialogPatterns returns the compiled pattern list for one builder. Harness
// defaults come from harness.Lookup(kind); if token parses and resolves in
// rt.Candidates, that candidate's DialogPatterns are appended. Non-compiling
// entries are skipped defensively.
func dialogPatterns(rt Runtime, kind, token string) []*regexp.Regexp {
	var raw []string
	if h, ok := harness.Lookup(kind); ok {
		raw = append(raw, h.DialogPatterns...)
	}
	if rt.Candidates != nil && token != "" {
		if ref, err := candidate.ParseRef(token); err == nil {
			if c, err := rt.Candidates.Lookup(ref); err == nil {
				raw = append(raw, c.DialogPatterns...)
			}
		}
	}

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

// dialogTail returns true when any pattern matches anywhere in text.
// Empty text or empty patterns returns false.
func dialogTail(text string, patterns []*regexp.Regexp) bool {
	if text == "" || len(patterns) == 0 {
		return false
	}
	for _, p := range patterns {
		if p.MatchString(text) {
			return true
		}
	}
	return false
}

// dialogGuard reads the visible screen of paneID and checks for dialog patterns.
// On read error, it logs a warning and returns false.
func dialogGuard(ctx context.Context, rt Runtime, paneID string, patterns []*regexp.Regexp) bool {
	text, err := rt.Herdr.ReadAgentSource(ctx, paneID, "visible", dialogScanLines)
	if err != nil {
		slog.Warn("dialog guard: builder screen unreadable", "pane", paneID, "err", err)
		return false
	}
	return dialogTail(text, patterns)
}
