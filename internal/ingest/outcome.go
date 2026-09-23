package ingest

import (
	"strings"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/store"
)

// deriveOutcome derives round n's outcome from its events, the binding's
// present state, and its members (the set of file basenames the source
// holds), first match wins (docs/specs/2026-09-20-persistence-design.md
// §5.3):
//
//  1. a report entry exists for n                                -> reported
//  2. members has round n's done marker and no report            -> done_no_report
//  3. an exit entry exists for n, n is the binding's current      -> exited
//     round, and the binding's state is not active
//  4. the binding's state is needs_you on round n, or its Halt    -> halted
//     names round n
//  5. a switch entry exists for n                                 -> switched
//  6. otherwise                                                   -> open
func deriveOutcome(events []store.LogEntry, n int, b store.Binding, members map[string]bool) string {
	var hasReport, hasExit, hasSwitch bool
	for _, e := range events {
		if e.Round != n {
			continue
		}
		switch e.Kind {
		case store.KindReport:
			hasReport = true
		case store.KindExit:
			hasExit = true
		case store.KindSwitch:
			hasSwitch = true
		}
	}

	if hasReport {
		return db.OutcomeReported
	}
	if members[donePathBase(n)] {
		return db.OutcomeDoneNoReport
	}
	if hasExit && n == b.Round && b.State != store.StateActive {
		return db.OutcomeExited
	}
	if (b.State == store.StateNeedsYou && n == b.Round) || (b.Halt != "" && n == b.Round) {
		return db.OutcomeHalted
	}
	if hasSwitch {
		return db.OutcomeSwitched
	}
	return db.OutcomeOpen
}

// switchesForRound counts switch entries for round n, regardless of outcome.
func switchesForRound(events []store.LogEntry, n int) int {
	count := 0
	for _, e := range events {
		if e.Round == n && e.Kind == store.KindSwitch {
			count++
		}
	}
	return count
}

// builderForRound resolves round n's builder: the token parsed from the
// last pick or switch entry's note for the round, falling back to
// b.BuilderCandidate. A consult's pick is not the builder: a pick whose note
// names a role other than "builder" (isRolePick) is skipped, so a consult
// asked after the builder pick cannot overwrite the round's builder.
// candidateTok is the token verbatim (with any effort suffix kept); ref is
// parsed from it with a trailing "#..." suffix stripped (a ":effort" suffix
// belongs to the model and stays). ok is false when no token was found at
// all -- from a note or from the binding.
func builderForRound(events []store.LogEntry, n int, b store.Binding) (candidateTok string, ref candidate.Ref, ok bool) {
	var lastKind store.Kind
	var lastNote string
	var found bool
	for _, e := range events {
		if e.Round != n {
			continue
		}
		if e.Kind == store.KindPick && isRolePick(e.Note) {
			continue
		}
		if e.Kind == store.KindPick || e.Kind == store.KindSwitch {
			lastKind, lastNote, found = e.Kind, e.Note, true
		}
	}

	tok := ""
	if found {
		tok = parseBuilderNote(lastKind, lastNote)
	}
	if tok == "" {
		tok = b.BuilderCandidate
	}
	if tok == "" {
		return "", candidate.Ref{}, false
	}

	if r, err := candidate.ParseRef(stripEffortHash(tok)); err == nil {
		ref = r
	}
	return tok, ref, true
}

// isRolePick reports whether note is the pick `ask` writes for a consult:
// "picked <tok> for <role>: ..." with a role other than "builder". It
// recognises that consult pick and deliberately nothing else, so a builder
// pick, a remote " on <server>:" pick, and any older or unrecognised shape
// all return false.
func isRolePick(note string) bool {
	const prefix = "picked "
	if !strings.HasPrefix(note, prefix) {
		return false
	}
	rest := note[len(prefix):]
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		return false
	}
	const sep = " for "
	if !strings.HasPrefix(rest[sp:], sep) {
		return false
	}
	rolePart := rest[sp+len(sep):]
	ci := strings.IndexByte(rolePart, ':')
	if ci < 0 {
		return false
	}
	role := rolePart[:ci]
	if role == "" || role == "builder" || strings.IndexByte(role, ' ') >= 0 {
		return false
	}
	return true
}

// parseBuilderNote extracts the candidate token from a pick or switch log
// entry's note.
func parseBuilderNote(kind store.Kind, note string) string {
	switch kind {
	case store.KindPick:
		return parsePickNote(note)
	case store.KindSwitch:
		return parseSwitchNote(note)
	}
	return ""
}

// parsePickNote parses "picked <token> ...": a candidate token never
// contains a space, so the token is the first space-delimited word after
// "picked ", with one trailing ":" stripped. This keeps " for builder"
// (the local pick note) and a model's ":effort" suffix out of the cut.
func parsePickNote(note string) string {
	const prefix = "picked "
	if !strings.HasPrefix(note, prefix) {
		return ""
	}
	rest := note[len(prefix):]
	tok := rest
	if i := strings.IndexByte(rest, ' '); i >= 0 {
		tok = rest[:i]
	}
	return strings.TrimSuffix(tok, ":")
}

// parseSwitchNote parses the token from a switch entry's note. When the
// note carries the "picked <tok> ..." clause relay actually writes
// ("switched builder (<reason>): picked <tok> for builder: ...") the pick
// rule applies; otherwise it is the token after the last "->", trimmed of
// a trailing ")" and whitespace.
func parseSwitchNote(note string) string {
	if i := strings.LastIndex(note, "picked "); i >= 0 {
		return parsePickNote(note[i:])
	}
	i := strings.LastIndex(note, "->")
	if i < 0 {
		return ""
	}
	tok := strings.TrimSpace(note[i+2:])
	tok = strings.TrimSuffix(tok, ")")
	return strings.TrimSpace(tok)
}

// stripEffortHash removes a trailing "#..." suffix from a candidate token.
// A ":effort" suffix is not touched -- it belongs to the model part.
func stripEffortHash(tok string) string {
	if i := strings.IndexByte(tok, '#'); i >= 0 {
		return tok[:i]
	}
	return tok
}
