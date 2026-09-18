package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
)

// paneHead is the pane's first rows: title with the state pill and the
// last event, planner, builder, tree, blank -- plus one `foreign` row per
// foreign agent, which is why the caller measures it rather than
// assuming paneHeadRows. Each row is unpadded; paneView fits them.
func (m Model) paneHead(b *relay.BindingStatus) []string {
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

	planner := label("planner") + fmt.Sprintf("%-4s %-9s %s", b.PlannerPane, b.PlannerKind, b.PlannerStatus)
	if b.PlannerFocus {
		planner += sep + dimStyle.Render("focused")
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
	builder := label("builder") + fmt.Sprintf("%-4s %-9s ", b.BuilderPane, b.BuilderKind) + strings.Join(bparts, sep)

	rows := []string{title, planner, builder}
	for _, fa := range b.Foreign {
		rows = append(rows, label("foreign")+fmt.Sprintf("%-4s %-9s %s"+sep+"%s", fa.PaneID, fa.Kind, fa.Status, fa.Title))
	}

	var tparts []string
	if b.Branch != "" {
		tparts = append(tparts, fgStyle.Render(b.Branch))
	} else {
		tparts = append(tparts, fgStyle.Render(b.CWD))
	}
	if b.Dirty {
		tparts = append(tparts, stateNeedsYouStyle.Render("dirty"))
	}
	if b.LastClose != nil {
		tree := b.LastClose.Tree
		if len(tree) > 7 {
			tree = tree[:7]
		}
		tparts = append(tparts, dimStyle.Render(fmt.Sprintf("last close %s (%d commits)", tree, b.LastClose.Commits)))
	}
	rows = append(rows, label("tree")+strings.Join(tparts, sep), "")
	return rows
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

// tabBar is the numbered tab row and the rule under it.
func (m Model) tabBar() []string {
	var parts []string
	for i, t := range tabTitles {
		label := fmt.Sprintf(" %d %s ", i+1, t)
		if tab(i) == m.detail.active {
			parts = append(parts, activeTabStyle.Render(label))
		} else {
			parts = append(parts, inactiveTabStyle.Render(label))
		}
	}
	return []string{strings.Join(parts, " "), ruleStyle.Render(strings.Repeat("─", m.paneWidth()))}
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
	case tabReport:
		s = fmt.Sprintf("report r%d · %s", c.round, c.at.Local().Format("15:04"))
	case tabTerminal:
		n := strings.Count(strings.TrimRight(c.body, "\n"), "\n") + 1
		r := row(m.report, m.detail.name)
		if m.detail.headless {
			src := "headless"
			if r != nil && r.Headless != nil && r.Headless.LogPath != "" {
				src += " · " + filepath.Base(r.Headless.LogPath)
			}
			mode := "following"
			if !m.detail.follow {
				mode = "scrolled"
			}
			s = fmt.Sprintf("%s · %d lines · %s", src, n, mode)
		} else {
			pane := ""
			if r != nil {
				pane = r.BuilderPane
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
func (m Model) hintLine(b *relay.BindingStatus) (string, bool) {
	if b == nil || b.Waiting == nil || b.Waiting.Cause != "blocked" || m.detail.active != tabTerminal {
		return "", false
	}
	return accentStyle.Render("relay: ") + fgStyle.Render(b.Waiting.Hint), true
}

// paneView draws exactly bodyRows() rows at width: head, tabs, source,
// blank, viewport, with the hint replacing the last viewport row when it
// applies. The viewport is resized to what is left after a head that
// grew by foreign rows.
func (m Model) paneView(width int) string {
	b := row(m.report, m.detail.name)
	rows := []string{}
	rows = append(rows, m.paneHead(b)...)
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
