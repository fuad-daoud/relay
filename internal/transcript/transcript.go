// Package transcript renders a headless builder's streamed output -- one
// JSON event per line, in each harness's own shape -- into the lines a
// human reads in NNN-builder.log (#168). It knows harness kinds and
// nothing else: no rounds, no files, no bindings. It is presentation only:
// nothing in relevo decides anything on what it returns.
package transcript

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxArg is how much of a tool's main argument one rendered line carries.
const maxArg = 200

// argKeys is the order in which a tool call's parameters are tried for the
// one worth showing; the first non-empty string wins.
var argKeys = []string{"command", "file_path", "path", "AbsolutePath", "pattern", "description", "prompt", "query", "url"}

// Render turns one raw line of the stream (without its trailing newline)
// into the lines to append to the log, each without a trailing newline.
// Rules, in order: an empty line is nothing; a line that is not a JSON
// object is itself, verbatim (that is how the relevo-exit trailer and a
// plain-text error reach the log); a known event renders per its kind's
// table; noise renders as nothing; anything else renders as "[<type>]" so a
// harness upgrade degrades to noise, not silence. Never errors, never
// panics.
func Render(kind string, line []byte) []string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	var obj map[string]any
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &obj) != nil || obj == nil {
		return []string{string(line)}
	}
	switch kind {
	case "claude":
		return renderClaude(obj)
	case "agy":
		return renderAgy(obj)
	case "opencode":
		return renderOpencode(obj)
	case "codex":
		return renderCodex(obj)
	}
	return []string{unknown(obj)}
}

// unknown is rule 5: the event's type, or event, or "?".
func unknown(obj map[string]any) string {
	if t := str(obj["type"]); t != "" {
		return "[" + t + "]"
	}
	if e := str(obj["event"]); e != "" {
		return "[" + e + "]"
	}
	return "[?]"
}

// toolLine is "● <name> <main argument>", or "● <name>" when no parameter
// is a non-empty string. The marker is what lets a reader tell a call from
// assistant prose (spec §4.3, amended for #180).
func toolLine(name string, params map[string]any) string {
	if arg, ok := mainArg(params); ok {
		return "● " + name + " " + oneLine(arg)
	}
	return "● " + name
}

func mainArg(params map[string]any) (string, bool) {
	for _, k := range argKeys {
		if s := str(params[k]); s != "" {
			return s, true
		}
	}
	var strs []string
	for k, v := range params {
		if s := str(v); s != "" {
			strs = append(strs, k)
		}
	}
	if len(strs) == 0 {
		return "", false
	}
	sort.Strings(strs)
	return str(params[strs[0]]), true
}

// okLine is a successful tool result: "  ⎿ ok: <first line of its output>",
// or "  ⎿ ok" when the harness gave none. One line, so three parallel
// calls' results still tell apart.
func okLine(output string) string {
	if output = oneLine(output); output == "" {
		return "  ⎿ ok"
	}
	return "  ⎿ ok: " + output
}

// errLine is a failed tool result: "  ⎿ error: <first line>", or "  ⎿ error"
// when the harness gave no message.
func errLine(msg string) string {
	if msg = oneLine(msg); msg == "" {
		return "  ⎿ error"
	}
	return "  ⎿ error: " + msg
}

// firstNonEmpty is the first non-empty string among vals, or "" when all are
// empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// oneLine keeps the first line of s and at most maxArg bytes of it, cut on
// a rune boundary and marked with "...".
func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "\r")
	if len(s) > maxArg {
		cut := maxArg
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "..."
	}
	return s
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
