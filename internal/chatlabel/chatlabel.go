// Package chatlabel turns what a planner record already stores -- its harness
// kind, session id and transcript locator -- into the harness's own
// human-facing name for that session: a chat title or the last prompt, plus a
// claude.ai link when the session is bridged.
//
// A label is computed when a command runs and only printed. It is never
// written to relevo state, never logged and never committed, so fixtures here
// are synthetic. Nothing in this package errors: anything unreadable gives the
// empty Label, which renders as "-".
package chatlabel

import (
	"strings"
	"time"
)

// MaxTextRunes is the longest Text a Label carries, excluding the two quote
// characters around a prompt. A longer value is cut to MaxTextRunes-1 runes
// plus an ellipsis, so the result is at most MaxTextRunes runes.
const MaxTextRunes = 50

// DefaultTailBytes is how much of a transcript's end ReadTail reads when the
// caller names no budget. Across the 60 largest transcripts on the planner's
// machine the latest entry of every type used here sat within 35 KB of the end,
// and Claude Code re-appends these entries throughout a session.
const DefaultTailBytes int64 = 256 << 10

// OpencodeTimeout bounds the sqlite3 read of an opencode session title: a
// label must never make a listing hang.
const OpencodeTimeout = 2 * time.Second

// Label is the harness's own human-facing name for one session. Both fields are
// best-effort and may be empty: Text is a chat title (bare) or the last prompt
// (wrapped in `"`), Link is a claude.ai session URL.
type Label struct {
	Text string
	Link string
}

// String renders a Label for a table cell: "-" when there is nothing to show,
// otherwise the text and the link joined by " · ".
func (l Label) String() string {
	switch {
	case l.Text == "" && l.Link == "":
		return "-"
	case l.Link == "":
		return l.Text
	case l.Text == "":
		return l.Link
	default:
		return l.Text + " · " + l.Link
	}
}

// clean turns harness text into something a person can read in one table cell:
// "" stays "", otherwise the first non-empty line, with every run of whitespace
// collapsed to one space, trimmed, and cut to MaxTextRunes runes.
func clean(s string) string {
	s = strings.Join(strings.Fields(firstNonEmptyLine(s)), " ")
	if s == "" {
		return ""
	}
	return truncate(s, MaxTextRunes)
}

// firstNonEmptyLine is clean's first step: a prompt may open with blank lines,
// and only the first line that carries text is worth showing.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// truncate cuts s to at most max runes. When it cuts, it keeps max-1 runes and
// appends an ellipsis, so a cut is visible and the result stays within max.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// quote wraps a cleaned prompt in the two quote characters that mark Text as a
// prompt rather than a title. An empty prompt stays empty.
func quote(s string) string {
	if s == "" {
		return ""
	}
	return "\"" + s + "\""
}
