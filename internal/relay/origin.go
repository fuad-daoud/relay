package relay

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relay/internal/store"
)

// OriginLine produces the one fixed first line for a typed payload (#139).
// Pure; no trailing newline.
func OriginLine(name string, round int, dir store.Direction, kind store.Kind) string {
	if dir == store.DirToBuilder {
		return fmt.Sprintf("relay: round %d · to builder %q · from the planner (not the human)", round, name)
	}
	if kind == store.KindFindings {
		return fmt.Sprintf("relay: consult · to planner · about builder %q (not the human)", name)
	}
	return fmt.Sprintf("relay: round %d · to planner · about builder %q (not the human)", round, name)
}

// WithOrigin prepends the origin line separated by a blank line, unless payload
// already begins with "relay: " (after trimming leading whitespace), in which
// case payload is returned unchanged.
func WithOrigin(payload, origin string) string {
	trimmed := strings.TrimLeft(payload, " \t\r\n")
	if strings.HasPrefix(trimmed, "relay: ") {
		return payload
	}
	return origin + "\n\n" + payload
}
