package relay

import (
	"bytes"
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
	if len(report) == 0 {
		return ReportTail{}, false
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
		return ReportTail{}, false
	}

	closeIdx := -1
	for i := openIdx + 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "```" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return ReportTail{}, false
	}

	for i := closeIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return ReportTail{}, false
		}
	}

	var (
		tail        ReportTail
		statusRaw   string
		hasStatus   bool
		haltedAtRaw string
	)

	for i := openIdx + 1; i < closeIdx; i++ {
		line := lines[i]
		line = stripComment(line)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colon := strings.Index(line, ":")
		if colon == -1 {
			return ReportTail{}, false
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		switch key {
		case "status":
			hasStatus = true
			statusRaw = val
		case "halted_at":
			haltedAtRaw = val
		case "changed_paths":
			tail.ChangedPaths = parseListValue(val)
		case "commands_run":
			tail.CommandsRun = parseListValue(val)
		case "not_done":
			tail.NotDone = parseListValue(val)
		default:
			// Unknown keys are ignored
		}
	}

	if !hasStatus {
		return ReportTail{}, false
	}

	status := strings.TrimSpace(unquoteScalar(statusRaw))
	switch status {
	case OutcomeDone, OutcomeHalted, OutcomeBlocked, OutcomeDeferred:
		tail.Status = status
	default:
		return ReportTail{}, false
	}

	tail.HaltedAt = strings.TrimSpace(unquoteScalar(haltedAtRaw))

	return tail, true
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
