// Package relay statusline rendering per docs/specs/2026-09-13-statusline-design.md.
package relay

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fuad-daoud/relay/internal/store"
)

var (
	ansiDim      = "\x1b[38;5;245m"
	ansiActive   = "\x1b[38;5;42m"
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
// for Claude Code's statusLine setting per spec §4.3 and §5.
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
			mid += " · " + harnessSegment(b.BuilderCandidate)
		}
		mid += " · " + waiting(b)

		displayWord := b.Display
		switch b.Display {
		case "ACTIVE":
			displayWord = ansiActive + "ACTIVE" + ansiReset
		case "NEEDS YOU":
			displayWord = ansiNeedsYou + "NEEDS YOU" + ansiReset
		}

		rawRight := age(b, now) + " · " + b.Display
		colouredRight := age(b, now) + " · " + displayWord

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

func waiting(b BindingStatus) string {
	if b.Detail != "" {
		return b.Detail
	}
	if hold := HoldText(b); hold != "" {
		return "report → planner · " + hold
	}
	if b.Nudge != nil {
		return "nudged · " + NudgeText(*b.Nudge)
	}
	if b.LastPayload == nil {
		return "no plan yet"
	}
	switch b.LastPayload.Kind {
	case store.KindPlan:
		return "plan sent"
	case store.KindReport:
		if b.LastPayload.Note != "" {
			return fmt.Sprintf("report in (%s)", b.LastPayload.Note)
		}
		return "report in"
	case store.KindQuestion:
		return "question in"
	case store.KindAnswer:
		return "answered"
	}
	return ""
}

func age(b BindingStatus, now time.Time) string {
	if b.LastPayload == nil {
		return "--"
	}
	return AgeText(now.Sub(b.LastPayload.TS))
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

// PlannerStatus filters stored bindings to one planner pane and builds rows
// through buildReport from the store alone per spec §4.1. It never probes herdr.
func PlannerStatus(ctx context.Context, rt Runtime, pane string) (Report, error) {
	if pane == "" {
		return Report{}, nil
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return Report{}, err
	}
	var kept []store.Binding
	for _, b := range bindings {
		if b.Planner.PaneID == pane && b.State != store.StateDone {
			kept = append(kept, b)
		}
	}
	return buildReport(ctx, rt, kept, nil)
}
