package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
	"github.com/muesli/termenv"
)

func splitModel(t *testing.T, width, height int, rows ...relay.BindingStatus) Model {
	t.Helper()
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	m := newModel(context.Background(), plannerSource{relay.Runtime{Store: st, Herdr: fh}}, Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: relay.Report{Bindings: rows}})
	return res.(Model)
}

func threeRows() []relay.BindingStatus {
	return []relay.BindingStatus{
		{Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working", Last: &relay.LastEvent{TS: railNow.Add(-6 * time.Minute)}},
		{Name: "docs", Round: 1, Display: "DONE", BuilderKind: "agy", Last: &relay.LastEvent{TS: railNow.Add(-time.Hour)}},
		{Name: "webshop", Round: 4, Display: "NEEDS YOU", BuilderKind: "agy", BuilderStatus: "blocked", Last: &relay.LastEvent{TS: railNow.Add(-2 * time.Minute)}},
	}
}

// TestStickyFollowsKeyAcrossOwners: the rail's sticky is the row key, so a
// second client's same-named binding appearing above must not steal the
// cursor -- it stays on the key it was on.
func TestStickyFollowsKeyAcrossOwners(t *testing.T) {
	m := splitModel(t, 140, 40, relay.BindingStatus{
		Name: "persist", Owner: "b", OwnerLabel: "b", Round: 1, Display: "ACTIVE", BuilderKind: "agy",
	})
	if m.rows()[m.list.cursor].Key() != "b/persist" {
		t.Fatalf("cursor on %q", m.rows()[m.list.cursor].Key())
	}
	res, _ := m.Update(statusMsg{report: relay.Report{Bindings: []relay.BindingStatus{
		{Name: "persist", Owner: "a", OwnerLabel: "a", Round: 1, Display: "ACTIVE", BuilderKind: "agy"},
		{Name: "persist", Owner: "b", OwnerLabel: "b", Round: 1, Display: "ACTIVE", BuilderKind: "agy"},
	}}})
	m = res.(Model)
	if m.rows()[m.list.cursor].Key() != "b/persist" {
		t.Errorf("cursor moved to %q, want b/persist", m.rows()[m.list.cursor].Key())
	}
}

// TestRailLinesGroupsByOwner: owner-labelled rows get a header per
// maximal owner run -- the label, the ShortOwner fingerprint and the run's
// count -- a blank line before each header, and global card indices; with
// every label empty the output is exactly today's.
func TestRailLinesGroupsByOwner(t *testing.T) {
	id := "SHA256:VLERFMZnvN5HSw/GCBr6FXPEgs4QeAfdU95BUhMMqI0"
	rows := []relay.BindingStatus{
		{Name: "api", Owner: id, OwnerLabel: "a", Round: 1, Display: "ACTIVE", BuilderKind: "agy"},
		{Name: "docs", Owner: id, OwnerLabel: "a", Round: 2, Display: "DONE", BuilderKind: "agy"},
		{Name: "webshop", Owner: "SHA256:abcdefghijklmnop", OwnerLabel: "b", Round: 3, Display: "ACTIVE", BuilderKind: "agy"},
	}
	lines := railLines(rows, -1, false, railNow, true, railDefault, false)
	if plain(lines[0].text) != "a (SHA256:VLERFMZnvN5H…) 2" {
		t.Errorf("first line = %q", plain(lines[0].text))
	}
	if lines[0].binding != -1 {
		t.Errorf("header must be untagged: %+v", lines[0])
	}
	// run a's cards: every card line tagged its global row index
	for i := 1; i <= 3; i++ {
		if lines[i].binding != 0 {
			t.Errorf("api card line %d binding = %d, want 0", i, lines[i].binding)
		}
	}
	for i := 4; i <= 6; i++ {
		if lines[i].binding != 1 {
			t.Errorf("docs card line %d binding = %d, want 1", i, lines[i].binding)
		}
	}
	// the blank line that precedes the b header
	if lines[7].binding != -1 || plain(lines[7].text) != "" {
		t.Errorf("line before the b header must be a blank gap: %+v %q", lines[7], plain(lines[7].text))
	}
	if lines[8].binding != -1 || plain(lines[8].text) != "b (SHA256:abcdefghijkl…) 1" {
		t.Errorf("b header = %q", plain(lines[8].text))
	}
	for i := 9; i <= 11; i++ {
		if lines[i].binding != 2 {
			t.Errorf("webshop card line %d binding = %d, want 2", i, lines[i].binding)
		}
	}
	// trailing blank closes the run
	if lines[12].binding != -1 {
		t.Errorf("trailing gap = %+v", lines[12])
	}
	if len(lines) != 13 {
		t.Fatalf("%d lines, want 13", len(lines))
	}

	// All labels empty: no untagged lines beyond today's -- identical to
	// today's output for the same rows.
	plainRows := []relay.BindingStatus{
		{Name: "api", Round: 1, Display: "ACTIVE", BuilderKind: "agy"},
		{Name: "docs", Round: 2, Display: "DONE", BuilderKind: "agy"},
		{Name: "webshop", Round: 3, Display: "ACTIVE", BuilderKind: "agy"},
	}
	got := railLines(plainRows, -1, false, railNow, true, railDefault, false)
	if len(got) != 9 {
		t.Fatalf("label-less rail has %d lines, want 9", len(got))
	}
	for i, l := range got {
		if l.binding != i/3 {
			t.Errorf("line %d binding = %d, want %d", i, l.binding, i/3)
		}
	}
	for i, first := range []int{0, 3, 6} {
		if p := plain(got[first].text); p != []string{"api r1", "docs r2", "webshop r3"}[i] {
			t.Errorf("card %d first line = %q", i, p)
		}
	}
}

// TestPaneHeadShowsClientLine: an owner-labelled row's planner line is
// replaced by the client line; a planner row keeps the planner line.
func TestPaneHeadShowsClientLine(t *testing.T) {
	id := "SHA256:VLERFMZnvN5HSw/GCBr6FXPEgs4QeAfdU95BUhMMqI0"
	m := splitModel(t, 140, 40, threeRows()...)

	client := relay.BindingStatus{
		Name: "webshop", Owner: id, OwnerLabel: "zen", Round: 4, Display: "NEEDS YOU",
		BuilderKind: "agy", BuilderStatus: "blocked",
	}
	head := stripANSI(strings.Join(m.paneHead(&client), "\n"))
	if !strings.Contains(head, "client") || !strings.Contains(head, "zen") {
		t.Errorf("no client line:\n%s", head)
	}
	if strings.Contains(head, "planner") {
		t.Errorf("the planner line must be replaced:\n%s", head)
	}

	planner := relay.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU",
		BuilderKind: "agy", BuilderStatus: "blocked",
		PlannerPane: "w1:p1", PlannerKind: "claude", PlannerStatus: "working",
	}
	head = stripANSI(strings.Join(m.paneHead(&planner), "\n"))
	if !strings.Contains(head, "planner") {
		t.Errorf("planner row must keep its planner line:\n%s", head)
	}
	if strings.Contains(head, "client") {
		t.Errorf("planner row must not show a client line:\n%s", head)
	}
}

func TestSplitViewShape(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 40 {
		t.Fatalf("%d lines at height 40", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 140 {
			t.Errorf("line %d is %d wide: %q", i, w, stripANSI(l))
		}
	}
	p := plain(view)
	if !strings.Contains(p, "relay 3 bindings · 1 needs you") {
		t.Errorf("header missing counts:\n%s", p)
	}
	// Attention order: webshop first and selected, pane shows it.
	if idx := strings.Index(p, "▎ webshop"); idx < 0 || idx > strings.Index(p, " api ") {
		t.Errorf("webshop must be the selected, first card:\n%s", p)
	}
	if !strings.Contains(p, "webshop round 4") {
		t.Errorf("pane must show the cursor's binding:\n%s", p)
	}
	if !strings.Contains(p, "⏎ focus pane") || !strings.Contains(p, "s sort: attention") {
		t.Errorf("split footer keys missing:\n%s", p)
	}
	if m.detail.name != "webshop" || m.detail.round != 3 {
		t.Errorf("detail not pointed at the cursor: %+v", m.detail)
	}
}

func TestPaneFollowsCursor(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	// The statusMsg pointed the pane at webshop and issued its fetch.
	if !m.tabInFlight {
		t.Fatal("initial point must fetch")
	}
	m.tabInFlight = false
	m.detail.active = tabDiff
	m.detail.scroll[tabDiff] = 7
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	if m.detail.name != "api" {
		t.Errorf("after j the pane shows %q, want api", m.detail.name)
	}
	if cmd == nil || !m.tabInFlight {
		t.Error("moving the cursor must issue exactly one fetch for the new binding")
	}
	if m.detail.active != tabDiff {
		t.Error("the active tab must survive the move")
	}
	if m.detail.scroll[tabDiff] != 0 || m.detail.cache[tabDiff].loaded {
		t.Error("parked scrolls and caches must be cleared")
	}
	// With a fetch in flight, a second move issues none.
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	if cmd != nil {
		t.Error("a move while tabInFlight must not issue a second fetch")
	}
	if m.detail.name != "docs" {
		t.Errorf("pane must still re-point: %q", m.detail.name)
	}
	// The reply for the binding the cursor left arrives: discarded, and
	// the guard is released so the next tick can fetch for docs.
	res, _ = m.Update(tabMsg{name: "webshop", round: 3, t: tabDiff, content: tabContent{loaded: true, body: "old"}})
	m = res.(Model)
	if m.detail.cache[tabDiff].loaded {
		t.Error("stale tabMsg for a previous binding must be discarded")
	}
	if m.tabInFlight {
		t.Error("a stale reply still completes the fetch; the guard must be released")
	}
}

func TestResizeAcrossThreshold(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	if m.screen != screenDetail {
		t.Fatal("enter in split focuses the pane")
	}
	res, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = res.(Model)
	if m.layout() != layoutStack || m.screen != screenDetail || m.detail.name != "webshop" {
		t.Errorf("narrowing with the pane focused must land on the full-width detail of the same binding: layout=%v screen=%v name=%q", m.layout(), m.screen, m.detail.name)
	}
	if m.detail.vp.Width != 100 {
		t.Errorf("viewport width after narrowing = %d", m.detail.vp.Width)
	}
	// Back to the list, then widen: the pane must point at the cursor.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = res.(Model)
	m.tabInFlight = false
	m.detail = detailModel{}
	res, cmd := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	if m.detail.name != "webshop" || cmd == nil {
		t.Errorf("widening from the list must point the pane at the cursor and fetch: %+v", m.detail)
	}
}

func TestSortToggleKeepsSelection(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	if m.rows()[m.list.cursor].Name != "api" {
		t.Fatalf("cursor on %q", m.rows()[m.list.cursor].Name)
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = res.(Model)
	if m.sort {
		t.Error("s must switch to name order")
	}
	if got := m.rows()[m.list.cursor].Name; got != "api" {
		t.Errorf("cursor moved to %q on re-sort", got)
	}
	if m.rows()[0].Name != "api" || m.rows()[2].Name != "webshop" {
		t.Errorf("name order = %v", m.rows())
	}
	if !strings.Contains(stripANSI(m.View()), "s sort: name") {
		t.Error("footer must show the current order")
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !res.(Model).sort {
		t.Error("s twice is identity")
	}
}

func TestFooterNoticesAndRefreshAge(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}) // pane on api
	m = res.(Model)
	m.now = func() time.Time { return railNow.Add(2 * time.Second) }
	f := stripANSI(m.footerView())
	if !strings.Contains(f, "webshop NEEDS YOU") {
		t.Errorf("another binding at NEEDS YOU must be a footer notice: %q", f)
	}
	if !strings.Contains(f, "[ ] round") {
		t.Errorf("footer must show the [ ] round hint next to the tab hint: %q", f)
	}
	if !strings.HasSuffix(strings.TrimRight(f, " "), "refreshed 2s ago") {
		t.Errorf("footer must end with the refresh age: %q", f)
	}
	// The right side wins when they would overlap: at the narrowest split
	// width the six keys plus the notice do not fit on one line.
	m.width = splitMinWidth
	f = stripANSI(m.footerView())
	if lipgloss.Width(f) > splitMinWidth {
		t.Errorf("footer wider than the terminal: %q", f)
	}
	if !strings.Contains(f, "webshop NEEDS YOU") || !strings.Contains(f, "refreshed 2s ago") {
		t.Errorf("the notice and the age must survive the squeeze: %q", f)
	}
	if strings.Contains(f, "q quit") {
		t.Errorf("the key list must be the side that gives way: %q", f)
	}
}

// TestFooterMarksHerdrUnreachable: a report that degraded because herdr did
// not answer still renders its rows and marks the footer once -- without the
// error text itself, the way the refresh marker works (list_test.go) -- and
// a report whose HerdrError is empty adds no marker.
func TestFooterMarksHerdrUnreachable(t *testing.T) {
	rows := []relay.BindingStatus{
		{Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "unknown"},
	}

	// A report with an empty HerdrError adds no footer marker.
	m := splitModel(t, 140, 40, rows...)
	if strings.Contains(stripANSI(m.footerView()), "herdr unreachable") {
		t.Errorf("empty HerdrError must not mark the footer: %q", m.footerView())
	}

	// The same model receiving a report whose HerdrError is set marks the
	// footer without the error text, and keeps rendering the rows.
	res, _ := m.Update(statusMsg{report: relay.Report{
		HerdrError: "no herdr server",
		Bindings:   rows,
	}})
	m = res.(Model)
	f := stripANSI(m.footerView())
	if !strings.Contains(f, "! herdr unreachable") {
		t.Errorf("footer must mark herdr unreachable: %q", f)
	}
	if strings.Contains(f, "no herdr server") {
		t.Errorf("the error text itself must not be shown in the footer: %q", f)
	}
	if strings.Contains(f, "refresh failed") {
		t.Errorf("a degraded report is not a refresh failure: %q", f)
	}
	if !strings.Contains(stripANSI(m.View()), "api") {
		t.Errorf("rows must still render, got:\n%s", m.View())
	}
}

func TestTerminalFollowsTailUntilScrolledUp(t *testing.T) {
	rows := threeRows()
	rows[0].Headless = &relay.HeadlessInfo{PID: 1, LogPath: "/x/002-builder.log"}
	m := splitModel(t, 140, 40, rows...)
	// Attention order puts webshop (NEEDS YOU) first; make api the one under
	// test by moving to it, then to the terminal tab.
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	m.tabInFlight = false
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	m = res.(Model)
	m.tabInFlight = false
	if !m.detail.follow {
		t.Fatal("a fresh terminal tab must follow")
	}
	body := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "line %d\n", i)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	res, _ = m.Update(tabMsg{name: "api", round: m.detail.round, t: tabTerminal, content: tabContent{loaded: true, body: body(100)}})
	m = res.(Model)
	if !m.detail.vp.AtBottom() {
		t.Error("following: a refresh must land at the bottom")
	}
	// Scroll up: follow clears.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // focus the pane
	m = res.(Model)
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = res.(Model)
	if m.detail.follow {
		t.Error("scrolling up must stop following")
	}
	y := m.detail.vp.YOffset
	res, _ = m.Update(tabMsg{name: "api", round: m.detail.round, t: tabTerminal, content: tabContent{loaded: true, body: body(120)}})
	m = res.(Model)
	if m.detail.vp.YOffset != y {
		t.Errorf("not following: a refresh must hold the offset (%d -> %d)", y, m.detail.vp.YOffset)
	}
	// Back to the bottom: follow resumes. The viewport's default keymap has
	// no Home/End binding, so page down repeatedly until AtBottom().
	for i := 0; i < 200 && !m.detail.vp.AtBottom(); i++ {
		res, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		m = res.(Model)
	}
	if !m.detail.follow || !m.detail.vp.AtBottom() {
		t.Error("scrolling to the bottom must resume following")
	}
}

func TestFocusIsVisible(t *testing.T) {
	// ruleStyle and accentStyle render identically (plain text) under the
	// Ascii profile go test's non-tty output gets by default; force real
	// colour so the two are actually distinguishable, as detail_test.go and
	// list_test.go already do for the same reason.
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	m := splitModel(t, 140, 40, threeRows()...)
	railFocused := m.View()
	if !strings.Contains(railFocused, ruleStyle.Render("│")) || strings.Contains(railFocused, accentStyle.Render("│")) {
		t.Error("rail focused: separator must be in the rule colour")
	}
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	paneFocused := res.(Model).View()
	if !strings.Contains(paneFocused, accentStyle.Render("│")) {
		t.Error("pane focused: separator must be in accent")
	}
	if !strings.Contains(paneFocused, dimStyle.Render("▎")) {
		t.Error("pane focused: the selected card's gutter must dim")
	}
}

func TestRailResizeKeys(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'>'}})
	m = res.(Model)
	if m.railCols != railDefault+railStep {
		t.Errorf("> widens by railStep: %d", m.railCols)
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'<'}})
	res, _ = res.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'<'}})
	m = res.(Model)
	if m.railCols != railDefault-railStep {
		t.Errorf("< narrows by railStep: %d", m.railCols)
	}
	for i := 0; i < 50; i++ {
		res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'<'}})
		m = res.(Model)
	}
	if m.railCols != railMin {
		t.Errorf("< stops at railMin: %d", m.railCols)
	}
	view := m.View()
	for i, l := range strings.Split(view, "\n") {
		if w := lipgloss.Width(l); w > 140 {
			t.Errorf("line %d is %d wide after resizing", i, w)
		}
	}
	// The pane's viewport follows the divider.
	if m.detail.vp.Width != m.paneWidth() {
		t.Errorf("viewport width %d, pane %d", m.detail.vp.Width, m.paneWidth())
	}
}

func TestCompactToggleKeepsSelection(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	name := m.rows()[m.list.cursor].Name
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = res.(Model)
	if !m.compact || m.rows()[m.list.cursor].Name != name || m.detail.name != name {
		t.Errorf("c: compact %v cursor %q pane %q", m.compact, m.rows()[m.list.cursor].Name, m.detail.name)
	}
	if !strings.Contains(stripANSI(m.View()), "c cards") {
		t.Error("footer names the toggle's other state")
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if res.(Model).compact {
		t.Error("c twice is identity")
	}
}

func TestCompactIgnoresResize(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = res.(Model)
	before := m.railCols
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'>'}})
	m = res.(Model)
	if m.railCols != before || m.railWidth() != railCompact {
		t.Errorf("> while compact: cols %d width %d", m.railCols, m.railWidth())
	}
	view := m.View()
	for i, l := range strings.Split(view, "\n") {
		if w := lipgloss.Width(l); w > 140 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	if m.detail.vp.Width != 140-railCompact-railGap {
		t.Errorf("viewport must widen with the pane: %d", m.detail.vp.Width)
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = res.(Model)
	if m.detail.vp.Width != m.paneWidth() || m.railWidth() != before {
		t.Errorf("back to cards: viewport %d pane %d rail %d", m.detail.vp.Width, m.paneWidth(), m.railWidth())
	}
}

func TestHeaderGatesAndClock(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.report.Gated = []ledger.Gate{{Token: "codex", Kind: ledger.RateLimited, Since: railNow, Until: railNow.Add(88 * time.Minute)}}
	h := stripANSI(m.headerView())
	if !strings.Contains(h, "codex gated until 15:30") || !strings.Contains(h, "14:02") {
		t.Errorf("header = %q", h)
	}
	if strings.Count(h, "\n") != headerRows-1 {
		t.Errorf("header must be %d rows: %q", headerRows, h)
	}
}

func TestPaneHeadUsageAndSpendRows(t *testing.T) {
	rows := threeRows()
	m := splitModel(t, 140, 40, rows...)
	var b relay.BindingStatus
	for _, r := range rows {
		if r.Name == m.detail.name {
			b = r
		}
	}
	b.LastUsage = &usage.Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 9 * 60_000,
		Tokens: usage.Tokens{In: 100, CacheRead: 15_000_000, Out: 55_000}, Cost: usage.Cost{USD: 4.71, Basis: usage.Measured}, Samples: 1}
	b.Spend = &usage.Spend{Rounds: 2, Measured: 4.71, Unknown: 1}
	head := m.paneHead(&b)
	joined := stripANSI(strings.Join(head, "\n"))
	if !strings.Contains(joined, "usage    claude-sonnet-5 · 9m · in 100 · cache 15.0M (100%) · write 0 · out 55k · $4.71") {
		t.Errorf("no usage row in the block's own idiom:\n%s", joined)
	}
	if !strings.Contains(joined, "spend    2 rounds · $4.71 · 1 unknown") {
		t.Errorf("no spend row:\n%s", joined)
	}
	if head[len(head)-1] != "" {
		t.Error("the block still ends with its blank row")
	}
	b.LastUsage, b.Spend = nil, nil
	if n := len(m.paneHead(&b)); n != 5 {
		t.Errorf("without usage the block is 5 rows, got %d", n)
	}
	// The header no longer carries the spend: it belongs in the block.
	if h := stripANSI(m.headerView()); strings.Contains(h, "spend") {
		t.Errorf("header must not show spend: %q", h)
	}
}

// TestPaneHeadLiveUsageRow pins the live figure's place in the header
// (#234): a running round's `usage` row is the live one, exactly one, and
// the closed round's row does not appear beside it; spend keeps its row.
func TestPaneHeadLiveUsageRow(t *testing.T) {
	rows := threeRows()
	m := splitModel(t, 140, 40, rows...)
	var b relay.BindingStatus
	for _, r := range rows {
		if r.Name == m.detail.name {
			b = r
		}
	}
	b.LastUsage = &usage.Usage{Harness: "agy", Provider: "google", Model: "gemini-3-pro", DurationMS: 6 * 60_000,
		Cost: usage.Cost{Basis: usage.Unknown}, Note: "agy keeps no usage record"}
	b.LiveUsage = &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash", DurationMS: 4 * 60_000,
		Tokens: usage.Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3_100, Out: 8_200},
		Cost:   usage.Cost{USD: 0.04, Basis: usage.Measured}, Samples: 3}
	b.Spend = &usage.Spend{Rounds: 2, Measured: 0.16}
	head := m.paneHead(&b)
	joined := stripANSI(strings.Join(head, "\n"))
	if n := strings.Count(joined, "usage    "); n != 1 {
		t.Errorf("%d usage rows, want exactly one:\n%s", n, joined)
	}
	if !strings.Contains(joined, "usage    live · glm-5.3-flash") {
		t.Errorf("the usage row must be the live one:\n%s", joined)
	}
	if strings.Contains(joined, "gemini-3-pro") || strings.Contains(joined, "6m") {
		t.Errorf("the closed round's row must yield to the live one:\n%s", joined)
	}
	if !strings.Contains(joined, "spend    2 rounds · $0.16") {
		t.Errorf("no spend row:\n%s", joined)
	}
}
