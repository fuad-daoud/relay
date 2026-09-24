package chatlabel

import (
	"regexp"
	"strings"
)

// opencodeSessionID matches an opencode session id. It duplicates
// internal/relevo/deliver_opencode.go's opencodeSessionIDPattern instead of
// importing it: internal/relevo imports this package in round 2, so the import
// back would be a cycle.
var opencodeSessionID = regexp.MustCompile(`^ses_[A-Za-z0-9]+$`)

// OpencodeQuery is the sqlite3 read of an opencode session's title. The id's
// single quotes are doubled, the SQL string-literal escape.
func OpencodeQuery(sessionID string) string {
	return "select title from session where id = '" + strings.ReplaceAll(sessionID, "'", "''") + "'"
}

// Opencode builds the Label for the stdout of OpencodeQuery: its first line,
// trimmed and cleaned, is the title. Empty output, or a blank first line, gives
// the empty Label; an opencode title carries no link.
func Opencode(out []byte) Label {
	first, _, _ := strings.Cut(string(out), "\n")
	text := clean(strings.TrimSpace(first))
	if text == "" {
		return Label{}
	}
	return Label{Text: text}
}
