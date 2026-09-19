package relay

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/fuad-daoud/relay/internal/policy"
)

// Built-in instruction-shaped scan patterns (#139, §3.6).
var builtInPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<system-reminder`),
	regexp.MustCompile(`(?i)</?(human|assistant)>`),
	regexp.MustCompile(`^\s*(Human|Assistant|User):`),
	regexp.MustCompile(`(?i)ignore (all )?(previous|prior) instructions`),
	regexp.MustCompile(`(?i)^\s*IMPORTANT:.*you must`),
}

// isFence returns true if line is exactly three or more backticks optionally followed by an info string.
func isFence(line string) bool {
	trimmed := strings.TrimRight(line, " \t")
	n := 0
	for n < len(trimmed) && trimmed[n] == '`' {
		n++
	}
	if n < 3 {
		return false
	}
	rest := trimmed[n:]
	if strings.Contains(rest, "`") {
		return false
	}
	return true
}

// ScanInstructionShaped counts lines in text that match any built-in pattern or extra patterns outside of fences.
// Pure; no I/O; returns 0 for empty input.
func ScanInstructionShaped(text []byte, extra []*regexp.Regexp) int {
	if len(text) == 0 {
		return 0
	}

	rawLines := bytes.Split(text, []byte("\n"))
	count := 0
	insideFence := false

	for _, rawLine := range rawLines {
		line := string(bytes.TrimRight(rawLine, "\r"))
		if isFence(line) {
			insideFence = !insideFence
			continue
		}
		if insideFence {
			continue
		}

		matched := false
		for _, re := range builtInPatterns {
			if re.MatchString(line) {
				matched = true
				break
			}
		}
		if !matched {
			for _, re := range extra {
				if re.MatchString(line) {
					matched = true
					break
				}
			}
		}
		if matched {
			count++
		}
	}

	return count
}

// compileScanPatterns compiles policy extra scan patterns.
func compileScanPatterns(p policy.Policy) []*regexp.Regexp {
	if len(p.ScanPatterns) == 0 {
		return nil
	}
	var res []*regexp.Regexp
	for _, pat := range p.ScanPatterns {
		re, err := regexp.Compile(pat)
		if err == nil {
			res = append(res, re)
		}
	}
	return res
}
