package relay

import (
	"bytes"
	"fmt"
	"strings"
)

// Outcome constants for ReportTail and LogEntry.
const (
	OutcomeDone         = "done"
	OutcomeHalted       = "halted"
	OutcomeBlocked      = "blocked"
	OutcomeDeferred     = "deferred"
	OutcomeUnstructured = "unstructured"
)

// ReportTail is the structured metadata decoded from the builder's trailing relay block (#133).
type ReportTail struct {
	Status       string
	HaltedAt     string
	ChangedPaths []string
	CommandsRun  []string
	NotDone      []string
}

// ParseReportTail finds and decodes the builder's trailing relay block.
// Pure; no I/O; every failure returns ok == false.
func ParseReportTail(report []byte) (ReportTail, bool) {
	tail, ok, _ := parseReportTail(report)
	return tail, ok
}

// parseReportTail is ParseReportTail plus a reason when a ```relay fence was
// found but the block was rejected. The reason is empty when there was no
// block (the usual unstructured case) so callers can tell "builder omitted
// the block" from "builder wrote one we could not read".
func parseReportTail(report []byte) (ReportTail, bool, string) {
	if len(report) == 0 {
		return ReportTail{}, false, ""
	}

	rawLines := bytes.Split(report, []byte("\n"))
	lines := make([]string, len(rawLines))
	for i, l := range rawLines {
		lines[i] = string(bytes.TrimRight(l, "\r"))
	}

	openIdx := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == "```relay" {
			openIdx = i
		}
	}
	if openIdx == -1 {
		return ReportTail{}, false, ""
	}

	closeIdx := -1
	for i := openIdx + 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "```" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return ReportTail{}, false, "tail: unclosed fence"
	}

	for i := closeIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return ReportTail{}, false, "tail: prose after closing fence"
		}
	}

	var (
		tail        ReportTail
		statusRaw   string
		hasStatus   bool
		haltedAtRaw string
		openList    string
	)

	for i := openIdx + 1; i < closeIdx; i++ {
		line := lines[i]
		line = stripComment(line)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "-") && (len(line) == 1 || line[1] == ' ' || line[1] == '\t') {
			if openList == "" {
				return ReportTail{}, false, fmt.Sprintf("tail: line %d has no ':'", i+1)
			}
			item := strings.TrimSpace(unquoteScalar(strings.TrimSpace(line[1:])))
			if item != "" {
				setList(&tail, openList, append(listOf(tail, openList), item))
			}
			continue
		}
		colon := strings.Index(line, ":")
		if colon == -1 {
			return ReportTail{}, false, fmt.Sprintf("tail: line %d has no ':'", i+1)
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		openList = ""
		switch key {
		case "status":
			hasStatus = true
			statusRaw = val
		case "halted_at":
			haltedAtRaw = val
		case "changed_paths", "commands_run", "not_done":
			if val == "" {
				setList(&tail, key, nil)
				openList = key
			} else {
				setList(&tail, key, parseListValue(val))
			}
		default:
			// Unknown keys are ignored
		}
	}

	if !hasStatus {
		return ReportTail{}, false, "tail: missing status"
	}

	status := strings.TrimSpace(unquoteScalar(statusRaw))
	switch status {
	case OutcomeDone, OutcomeHalted, OutcomeBlocked, OutcomeDeferred:
		tail.Status = status
	default:
		return ReportTail{}, false, "tail: unknown status"
	}

	tail.HaltedAt = strings.TrimSpace(unquoteScalar(haltedAtRaw))

	return tail, true, ""
}

func listOf(tail ReportTail, key string) []string {
	switch key {
	case "changed_paths":
		return tail.ChangedPaths
	case "commands_run":
		return tail.CommandsRun
	case "not_done":
		return tail.NotDone
	default:
		return nil
	}
}

func setList(tail *ReportTail, key string, vals []string) {
	switch key {
	case "changed_paths":
		tail.ChangedPaths = vals
	case "commands_run":
		tail.CommandsRun = vals
	case "not_done":
		tail.NotDone = vals
	}
}

func stripComment(line string) string {
	var inQuote rune
	inBracket := false
	for i, r := range line {
		if inQuote != 0 {
			if r == inQuote {
				inQuote = 0
			}
		} else if inBracket {
			if r == '"' || r == '\'' {
				inQuote = r
			} else if r == ']' {
				inBracket = false
			}
		} else {
			if r == '"' || r == '\'' {
				inQuote = r
			} else if r == '[' {
				inBracket = true
			} else if r == '#' {
				return line[:i]
			}
		}
	}
	return line
}

func unquoteScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func parseListValue(s string) []string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := s[1 : len(s)-1]
		rawElements := splitListElements(inner)
		var res []string
		for _, elem := range rawElements {
			elem = strings.TrimSpace(unquoteScalar(elem))
			if elem != "" {
				res = append(res, elem)
			}
		}
		if len(res) == 0 {
			return nil
		}
		return res
	}

	elem := strings.TrimSpace(unquoteScalar(s))
	if elem == "" {
		return nil
	}
	return []string{elem}
}

func splitListElements(s string) []string {
	var elements []string
	var current strings.Builder
	var inQuote rune

	for _, r := range s {
		if inQuote != 0 {
			if r == inQuote {
				inQuote = 0
			}
			current.WriteRune(r)
		} else {
			if r == '"' || r == '\'' {
				inQuote = r
				current.WriteRune(r)
			} else if r == ',' {
				elements = append(elements, current.String())
				current.Reset()
			} else {
				current.WriteRune(r)
			}
		}
	}
	elements = append(elements, current.String())
	return elements
}
