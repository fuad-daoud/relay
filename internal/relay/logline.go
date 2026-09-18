package relay

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// logLineIndent is the width of the timestamp column plus its two-space
// gap, so the usage line sits under the "round" column.
const logLineIndent = 21

// LogLine is the one text form of a log entry, shared by `relay log` and
// the ui's log tab. Line one is the format both have always printed; a
// second line, indented and marked ⎿ like the status log tail, carries the
// round's usage when the entry has one (#142).
func LogLine(e store.LogEntry) string {
	first := fmt.Sprintf("%s  round %-3d %-10s %-9s %s %s",
		e.TS.Local().Format("2006-01-02 15:04:05"), e.Round, e.Direction, e.Kind, e.Path, e.Note)
	if e.Late {
		first += " late"
	}
	if e.Usage == nil {
		return first
	}
	return first + "\n" + strings.Repeat(" ", logLineIndent) + "⎿ " + usage.Line(*e.Usage)
}
