package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// paneHead is the pane's first rows: title with the state pill and the
// last event, planner, builder, tree, blank -- plus a usage and a spend row
// when the binding has them, which is why the caller measures it rather than
// assuming paneHeadRows. Each row is unpadded; paneView fits them.
func (m Model) paneHead(b *relevo.BindingStatus) []string {
	label := func(s string) string { return dimStyle.Render(fmt.Sprintf("%-9s", s)) }
	if b == nil {
		return []string{"", "", "", "", ""}
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render(b.Name) +
		dimStyle.Render(fmt.Sprintf("  round %d  ", b.Round)) + pillStyle(b.Display).Render(b.Display)
	if b.Last != nil {
		right := dimStyle.Render(fmt.Sprintf("%s r%d · %s ago", b.Last.Kind, b.Last.Round, ago(b.Last.TS, m.now())))
		title = spread(title, right, m.paneWidth())
	}

	plannerName := b.PlannerName
	if plannerName == "" {
		plannerName = b.PlannerID
	}
	planner := label("planner") + fmt.Sprintf("%-4s %-9s route %s", plannerName, b.PlannerKind, b.PlannerRoute)
	// On a serve box the pane belongs to a client, not to this planner: the
	// client line replaces the planner line (empty OwnerLabel is a planner
	// row, which renders today's line above).
	if b.OwnerLabel != "" {
		planner = label("client") + b.OwnerLabel + "  (" + dimStyle.Render(relevo.ShortOwner(b.Owner)) + ")"
	}

	var bparts []string
	if b.Headless != nil {
		bparts = append(bparts, stateStyle(b.Display).Render(b.BuilderStatus))
		if b.Headless.PID != 0 {
			bparts = append(bparts, dimStyle.Render(fmt.Sprintf("pid %d since %s", b.Headless.PID, b.Headless.StartedAt.Local().Format("15:04"))))
		}
		bparts = append(bparts, fgStyle.Render("`"+b.BuilderCandidate+"`"))
	} else {
		bparts = append(bparts, builderStatusStyle(b.BuilderStatus).Render(b.BuilderStatus))
	}
	if b.Consults > 0 {
		bparts = append(bparts, dimStyle.Render(fmt.Sprintf("%d consults", b.Consults)))
	}
	if b.Switches > 0 {
		bparts = append(bparts, dimStyle.Render(fmt.Sprintf("switched %dx", b.Switches)))
	}
	builder := label("builder") + fmt.Sprintf("%-9s ", b.BuilderKind) + strings.Join(bparts, sep)

	rows := []string{title, planner, builder}

	var tparts []string
	if b.Branch != "" {
		tparts = append(tparts, fgStyle.Render(b.Branch))
	} else {
		tparts = append(tparts, fgStyle.Render(b.CWD))
	}
	if b.Dirty {
		tparts = append(tparts, stateNeedsYouStyle.Render("dirty"))
	}
	if lc := b.LastClose; lc != nil {
		unit := "commits"
		if lc.Commits == 1 {
			unit = "commit"
		}
		s := fmt.Sprintf("last close r%d: %d %s", lc.Round, lc.Commits, unit)
		if lc.Tree != "" {
			s += ", " + lc.Tree
		}
		tparts = append(tparts, dimStyle.Render(s))
	}
	rows = append(rows, label("tree")+strings.Join(tparts, sep))
	// usage and spend mirror `relevo status`'s rows (#142): the newest
	// round's line, then the binding's total. A running round shows its
	// live figure instead of the last closed one's (#234); spend is
	// closed rounds only and never shares a cell with the live figure.
	if b.LiveUsage != nil {
		parts := usage.LiveParts(*b.LiveUsage)
		styled := make([]string, len(parts))
		for i, p := range parts {
			switch {
			case i == 0:
				styled[i] = accentStyle.Render(p) // the word "live" is the point
			case i == len(parts)-1:
				styled[i] = fgStyle.Render(p) // the cost word is the point
			default:
				styled[i] = dimStyle.Render(p)
			}
		}
		rows = append(rows, label("usage")+strings.Join(styled, sep))
	} else if b.LastUsage != nil {
		parts := usage.Parts(*b.LastUsage)
		styled := make([]string, len(parts))
		for i, p := range parts {
			styled[i] = dimStyle.Render(p)
		}
		styled[len(styled)-1] = fgStyle.Render(parts[len(parts)-1]) // the cost word is the point
		rows = append(rows, label("usage")+strings.Join(styled, sep))
	}
	if b.Spend != nil {
		rows = append(rows, label("spend")+usage.SpendLine(*b.Spend))
	}
	rows = append(rows, "")
	return rows
}

// histPaneHead is paneHead for a hist (archived) detail: the identity line
// (detailHeader) alone, padded to paneHead's row budget -- an archived
// binding carries no live planner/builder/tree facts to show.
func (m Model) histPaneHead() []string {
	return []string{fgStyle.Render(m.detailHeader()), "", "", "", ""}
}

// builderStatusStyle: blocked is the one status a human must notice.
func builderStatusStyle(status string) lipgloss.Style {
	if status == "blocked" {
		return stateNeedsYouStyle
	}
	return fgStyle
}

// spread puts right at the right edge of a width-wide line, after left,
// dropping the gap when they would overlap (left wins; the caller decides
// what is important enough for the right).
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// tabBar is the five tab words and, under them, a rule whose heavy accent
// segment sits under the active word (spec §3.4). No numbers: 1-5 still
// switch, the footer says so.
func (m Model) tabBar() []string {
	var words, rule []string
	for i, t := range tabTitles {
		label := " " + t + " "
		if tab(i) == m.detail.active {
			words = append(words, activeTabStyle.Render(label))
			rule = append(rule, accentStyle.Render(strings.Repeat("━", lipgloss.Width(label))))
		} else {
			words = append(words, inactiveTabStyle.Render(label))
			rule = append(rule, ruleStyle.Render(strings.Repeat("─", lipgloss.Width(label))))
		}
	}
	gap := ruleStyle.Render("──")
	line := strings.Join(rule, gap)
	if pad := m.paneWidth() - lipgloss.Width(line); pad > 0 {
		line += ruleStyle.Render(strings.Repeat("─", pad))
	}
	// tabSpans (mouse.go) assumes this two-space join to compute each
	// word's column span; change both together.
	return []string{strings.Join(words, "  "), line}
}

// diffStat counts a patch the way `git diff --stat` would summarise it:
// files by `diff --git` headers, added and removed by leading +/- that
// are not the ---/+++ file markers.
func diffStat(patch string) (files, added, removed int) {
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			files++
		case strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "--- "):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return
}

// colourDiff styles a patch line by line: file headers bold, hunks blue,
// additions green, removals red, everything else untouched. It never
// parses further than the first characters of a line.
func colourDiff(patch string) string {
	lines := strings.Split(patch, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "diff --git "), strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "--- "):
			lines[i] = diffFileStyle.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = diffHunkStyle.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = diffAddStyle.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = diffDelStyle.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// colourTranscript styles a headless builder's log for the terminal tab,
// by the transcript's own markers and nothing else: a call is a green
// bullet, a bold tool name and its argument in parentheses, dim; an ok
// result is dim; an error result is red; every other line -- assistant
// prose, [unknown] events, the relevo-exit trailer -- is left alone.
func colourTranscript(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "● "):
			rest := strings.TrimPrefix(l, "● ")
			name, arg, _ := strings.Cut(rest, " ")
			out := stateActiveStyle.Render("●") + " " + lipgloss.NewStyle().Bold(true).Render(name)
			if arg != "" {
				out += dimStyle.Render("(" + arg + ")")
			}
			lines[i] = out
		case strings.HasPrefix(l, "  ⎿ error"):
			lines[i] = errorStyle.Render(l)
		case strings.HasPrefix(l, "  ⎿ "):
			lines[i] = dimStyle.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// sourceLine says, in one faint line, what the viewport is showing.
func (m Model) sourceLine() string {
	if m.detail.name == "" {
		return ""
	}
	c := m.detail.cache[m.detail.active]
	if !c.loaded {
		return faintStyle.Render("loading…")
	}
	var s string
	switch m.detail.active {
	case tabPlan:
		s = fmt.Sprintf("plan r%d · %s", c.round, c.at.Local().Format("15:04"))
	case tabReport:
		s = fmt.Sprintf("report r%d · %s", c.round, c.at.Local().Format("15:04"))
	case tabTerminal:
		n := strings.Count(strings.TrimRight(c.body, "\n"), "\n") + 1
		r := row(m.report, m.detail.name)
		if c.transcript {
			src := "pane"
			if m.detail.headless {
				src = "headless"
			}
			if c.logName != "" {
				src += " · " + c.logName
			}
			mode := "following"
			if !m.detail.follow {
				mode = "scrolled"
			}
			s = fmt.Sprintf("%s · %d lines · %s", src, n, mode)
		} else {
			pane := ""
			if r != nil {
				pane = "remote"
				if r.Headless != nil {
					pane = "headless"
				}
			}
			s = fmt.Sprintf("%s · captured %s ago · %d lines", pane, ago(c.at, m.now()), n)
		}
	case tabDiff:
		files, add, del := diffStat(c.body)
		unit := "files"
		if files == 1 {
			unit = "file"
		}
		s = fmt.Sprintf("round %d · %d %s · +%d −%d", m.detail.round, files, unit, add, del)
	case tabLog:
		n := strings.Count(strings.TrimRight(c.body, "\n"), "\n") + 1
		s = fmt.Sprintf("%d entries", n)
	}
	if c.err != nil || c.empty != "" {
		// The viewport carries the prose; the source line says only where
		// it looked.
		switch m.detail.active {
		case tabPlan:
			s = fmt.Sprintf("round %d", m.detail.round)
		case tabReport:
			s = "report"
		case tabDiff:
			s = fmt.Sprintf("round %d", m.detail.round)
		case tabLog:
			s = "log"
		}
	}
	return faintStyle.Render(s)
}

// hintLine is the one rendered line under a blocked builder's dialog on
// the terminal tab: the verb that resolves it (spec §3.4). The ui runs
// nothing; it names the command.
func (m Model) hintLine(b *relevo.BindingStatus) (string, bool) {
	if b == nil || b.Waiting == nil || b.Waiting.Cause != "blocked" || m.detail.active != tabTerminal {
		return "", false
	}
	return accentStyle.Render("relevo: ") + fgStyle.Render(b.Waiting.Hint), true
}

// emptyPaneBlock is the prose the pane shows at zero rows, already styled
// and fitted to width and height (padded with blank rows to height).
func emptyPaneBlock(width, height int) []string {
	if height <= 0 {
		return nil
	}
	raw := []string{
		"no bindings",
		"",
		"  relevo bind                   put a builder on this tree",
		"  relevo add --name <name>      put a builder on its own worktree",
		"  relevo candidates             list what can be bound",
	}
	lines := make([]string, 0, height)
	for _, l := range raw {
		if l == "" {
			lines = append(lines, fit("", width))
		} else {
			lines = append(lines, fit(emptyStyle.Render(l), width))
		}
	}
	for len(lines) < height {
		lines = append(lines, fit("", width))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// paneView draws exactly bodyRows() rows at width: head, tabs, source,
// blank, viewport, with the hint replacing the last viewport row when it
// applies. The viewport is resized to what is left after a head that
// grew by foreign rows.
func (m Model) paneView(width int) string {
	if m.empty() {
		return strings.Join(emptyPaneBlock(width, m.bodyRows()), "\n")
	}
	b := row(m.report, m.detail.name)
	rows := []string{}
	if m.detail.name != "" && !m.detail.live {
		rows = append(rows, m.histPaneHead()...)
	} else {
		rows = append(rows, m.paneHead(b)...)
	}
	rows = append(rows, m.tabBar()...)
	rows = append(rows, m.sourceLine(), "")
	budget := m.bodyRows() - len(rows)
	if budget < 0 {
		budget = 0
	}
	hint, hasHint := m.hintLine(b)
	vpRows := budget
	if hasHint && vpRows > 0 {
		vpRows--
	}
	vp := m.detail.vp
	vp.Width = width
	vp.Height = vpRows
	if vpRows > 0 {
		rows = append(rows, strings.Split(vp.View(), "\n")...)
	}
	if hasHint && budget > 0 {
		rows = append(rows, hint)
	}
	for len(rows) < m.bodyRows() {
		rows = append(rows, "")
	}
	rows = rows[:m.bodyRows()]
	for i := range rows {
		rows[i] = fit(rows[i], width)
	}
	return strings.Join(rows, "\n")
}
