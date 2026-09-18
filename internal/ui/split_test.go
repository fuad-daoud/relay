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
	"github.com/muesli/termenv"
)

func splitModel(t *testing.T, width, height int, rows ...relay.BindingStatus) Model {
	t.Helper()
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	m := newModel(context.Background(), relay.Runtime{Store: st, Herdr: fh}, Options{Interval: time.Second})
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

func TestTerminalFollowsTailUntilScrolledUp(t *testing.T) {
	rows := threeRows()
	rows[0].Headless = &relay.HeadlessInfo{PID: 1, LogPath: "/x/002-builder.log"}
	m := splitModel(t, 140, 40, rows...)
	// Attention order puts webshop (NEEDS YOU) first; make api the one under
	// test by moving to it, then to the terminal tab.
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	m.tabInFlight = false
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
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
	res, _ = m.Update(tabMsg{name: "api", t: tabTerminal, content: tabContent{loaded: true, body: body(100)}})
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
	res, _ = m.Update(tabMsg{name: "api", t: tabTerminal, content: tabContent{loaded: true, body: body(120)}})
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
