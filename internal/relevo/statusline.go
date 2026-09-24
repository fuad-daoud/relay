// Package relevo renders the status line per docs/specs/2026-09-13-statusline-design.md
// and docs/specs/2026-09-24-statusline-redesign-design.md.
package relevo

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// claudeCodeMargin is the number of cells Claude Code's chrome takes from
// COLUMNS: measured 2026-09-14, 141 of 146 rendered before its own ellipsis
// (#155).
const claudeCodeMargin = 4

var (
	ansiDim      = "\x1b[38;5;245m"
	ansiNeedsYou = "\x1b[1;38;5;214m"
	ansiReset    = "\x1b[0m"
)

// AgeText humanises a duration into coarse units per spec §3.4:
// truncating; under a minute Ns; under an hour Nm; otherwise Nh Mm with M
// the whole minutes past the hour, always present (e.g. 1h 0m). Negative renders 0s.
func AgeText(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	hours := int(d / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// RenderStatusLine formats a Report into one row per live binding
// for Claude Code's statusLine setting per spec §4.3 and §5, amended
// by docs/specs/2026-09-24-statusline-redesign-design.md.
func RenderStatusLine(r Report, now time.Time, columns int) string {
	if len(r.Bindings) == 0 {
		return ""
	}
	if columns <= 0 {
		columns = 80
	}

	nameW := 0
	for _, b := range r.Bindings {
		if w := utf8.RuneCountInString(b.Name); w > nameW {
			nameW = w
		}
	}

	var sb strings.Builder
	for _, b := range r.Bindings {
		dotColoured := ansiDim + "○" + ansiReset
		if b.Display == "NEEDS YOU" {
			dotColoured = ansiNeedsYou + "●" + ansiReset
		}

		mid := "r" + strconv.Itoa(b.Round)
		if b.BuilderCandidate != "" {
			harness := harnessSegment(b.BuilderCandidate)
			if b.Server != "" {
				harness += "@" + b.Server
			}
			mid += " · " + harness
		}
		mid += " · " + waiting(b)
		if t := roundTokens(b); t != "" {
			mid += " · " + t
		}

		clock := roundClock(b, now)
		var rawRight, colouredRight string
		if b.Display == "ACTIVE" || b.Display == "" {
			rawRight = clock
			colouredRight = clock
		} else {
			word := b.Display
			if b.Display == "NEEDS YOU" {
				word = ansiNeedsYou + "NEEDS YOU" + ansiReset
			}
			rawRight = clock + " · " + b.Display
			colouredRight = clock + " · " + word
		}

		leftW := 2 + nameW + 2
		midW := columns - leftW - 1 - utf8.RuneCountInString(rawRight)

		var line string
		if midW < 8 {
			line = dotColoured + " " + b.Name + "  " + mid + " · " + colouredRight
		} else {
			line = dotColoured + " " + pad(b.Name, nameW) + "  " + pad(truncate(mid, midW), midW) + " " + colouredRight
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	return sb.String()
}

// RenderPlannerLine is the statusline's first line: the planner's own name,
// dim, so each terminal shows which planner it is (#386). An empty name renders
// nothing -- a session relevo did not identify keeps the statusline it had. When
// columns > 0 and the line would overflow it, the visible text is cut with the
// same truncate the binding rows use, before the colour codes wrap it.
func RenderPlannerLine(name string, columns int) string {
	if name == "" {
		return ""
	}
	text := "planner " + name
	if columns > 0 && utf8.RuneCountInString(text) > columns {
		text = truncate(text, columns)
	}
	return ansiDim + text + ansiReset + "\n"
}

func waiting(b BindingStatus) string {
	if b.Detail != "" {
		return b.Detail
	}
	if b.LastPayload == nil {
		return "no plan yet"
	}
	switch b.LastPayload.Kind {
	case store.KindPlan:
		return "plan sent"
	case store.KindReport:
		base := "report in"
		if b.LastPayload.Note != "" {
			base = fmt.Sprintf("report in (%s)", b.LastPayload.Note)
		}
		if b.LastPayload.Outcome != "" && b.LastPayload.Outcome != OutcomeDone {
			base += " · " + b.LastPayload.Outcome
		}
		return base
	case store.KindQuestion:
		return "question in"
	case store.KindAnswer:
		return "answered"
	}
	return ""
}

func roundClock(b BindingStatus, now time.Time) string {
	if b.RoundStart.IsZero() {
		return "--"
	}
	if !b.RoundEnd.IsZero() {
		return AgeText(b.RoundEnd.Sub(b.RoundStart))
	}
	return AgeText(now.Sub(b.RoundStart))
}

func roundTokens(b BindingStatus) string {
	var base int64
	if b.RoundEnd.IsZero() {
		if b.LiveUsage != nil && b.LiveUsage.Samples > 0 {
			base = b.LiveUsage.Tokens.Total()
		}
	} else if b.RoundUsage != nil {
		base = b.RoundUsage.Tokens.Total()
	}
	total := base + b.RoundPriorTokens.Total()
	if total > 0 {
		return usage.ShortTokens(total) + " tok"
	}
	return ""
}

// harnessSegment is the harness segment of a candidate token: the part
// before its first "/" (agy, claude, opencode). A token with no "/" is
// returned whole.
//
// Named harnessSegment rather than the plan's harness: this package already
// imports internal/harness (see status.go et al.), and a package-level func
// harness collides with that import identifier across every file in this
// package (a Go package-block conflict, confirmed by the compiler) --
// renaming the import instead would have touched files well outside this
// task's declared scope. Deviation flagged in the report per plan
// instructions.
func harnessSegment(token string) string {
	if i := strings.IndexByte(token, '/'); i >= 0 {
		return token[:i]
	}
	return token
}

func truncate(s string, w int) string {
	runes := []rune(s)
	if len(runes) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	return string(runes[:w-1]) + "…"
}

func pad(s string, w int) string {
	n := utf8.RuneCountInString(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

// StatusLineWidth is the row width the verb lays out to: columns, Claude
// Code's own COLUMNS, minus its chrome. columns <= 0 means "not under Claude
// Code" and returns 0, the renderer's own default-to-80 case. The margin is
// override parsed as a non-negative integer when it parses as one, else
// claudeCodeMargin. The result is never less than 1.
func StatusLineWidth(columns int, override string) int {
	if columns <= 0 {
		return 0
	}
	margin := claudeCodeMargin
	if n, err := strconv.Atoi(override); err == nil && n >= 0 {
		margin = n
	}
	if w := columns - margin; w > 1 {
		return w
	}
	return 1
}

// ShouldDrainStdin reports whether the verb should drain stdin before
// rendering: true for anything that is not a character device (a pipe,
// Claude Code's common case, or a regular file), false for a tty, so a
// human running the verb by hand gets it back at once instead of blocking
// on EOF that will never come.
func ShouldDrainStdin(mode os.FileMode) bool {
	return mode&os.ModeCharDevice == 0
}

// PlannerStatus filters stored bindings to one planner id and builds rows
// through buildReport from the store alone per spec §4.1.
func PlannerStatus(ctx context.Context, rt Runtime, plannerID string) (Report, error) {
	if plannerID == "" {
		return Report{}, nil
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return Report{}, err
	}
	var kept []store.Binding
	for _, b := range bindings {
		if b.PlannerID == plannerID && b.State != store.StateDone {
			kept = append(kept, b)
		}
	}
	return buildReport(ctx, rt, kept)
}

// StatusLinePlanner is the planner identification in StatusLineDoc (§3).
type StatusLinePlanner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// StatusLineRow is one binding row in StatusLineDoc (§3).
type StatusLineRow struct {
	Name      string `json:"name"`
	Round     int    `json:"round"`
	Display   string `json:"display"`
	Harness   string `json:"harness"`
	Candidate string `json:"candidate"`
	Role      string `json:"role,omitempty"`
	Waiting   string `json:"waiting"`
	Clock     string `json:"clock"`
	Tokens    string `json:"tokens"`
	LastKind  string `json:"last_kind"`
	LastTS    string `json:"last_ts"`
	Route     string `json:"route"`
}

// StatusLineDoc is the top-level document emitted by relevo status --line --json (§3).
type StatusLineDoc struct {
	Planner *StatusLinePlanner `json:"planner"`
	Now     time.Time          `json:"now"`
	Rows    []StatusLineRow    `json:"rows"`
}

// StatusLineRows produces one StatusLineRow per r.Bindings entry, in order (§4).
func StatusLineRows(r Report, now time.Time) []StatusLineRow {
	rows := make([]StatusLineRow, 0, len(r.Bindings))
	for _, b := range r.Bindings {
		harness := harnessSegment(b.BuilderCandidate)
		if b.Server != "" {
			harness += "@" + b.Server
		}
		var lastKind, lastTS string
		if b.LastPayload != nil {
			lastKind = string(b.LastPayload.Kind)
			if !b.LastPayload.TS.IsZero() {
				lastTS = b.LastPayload.TS.UTC().Format(time.RFC3339)
			}
		}
		rows = append(rows, StatusLineRow{
			Name:      b.Name,
			Round:     b.Round,
			Display:   b.Display,
			Harness:   harness,
			Candidate: b.BuilderCandidate,
			Role:      b.Role,
			Waiting:   waiting(b),
			Clock:     roundClock(b, now),
			Tokens:    roundTokens(b),
			LastKind:  lastKind,
			LastTS:    lastTS,
			Route:     b.PlannerRoute,
		})
	}
	return rows
}
