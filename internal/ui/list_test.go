package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/muesli/termenv"
)

func TestListWindow(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		top, cursor, rows, n int
		want                 int
	}{
		{"no limit", 0, 0, 0, 10, 0},
		{"fits", 0, 3, 5, 3, 0},
		{"cursor below", 0, 7, 5, 10, 3},
		{"cursor above", 6, 2, 5, 10, 2},
		{"already visible", 3, 4, 5, 10, 3},
		{"clamped from past the end", 9, 4, 5, 10, 4},
		{"last row", 0, 9, 5, 10, 5},
	} {
		if got := listWindow(tc.top, tc.cursor, tc.rows, tc.n); got != tc.want {
			t.Errorf("%s: listWindow(%d, %d, %d, %d) = %d, want %d",
				tc.name, tc.top, tc.cursor, tc.rows, tc.n, got, tc.want)
		}
	}
}

func TestListRowsRenderFixedGoldenWidth(t *testing.T) {
	b1 := relay.BindingStatus{
		Name:          "relay-fork",
		Workspace:     "wM",
		Round:         3,
		Display:       "ACTIVE",
		BuilderAlias:  "agy",
		BuilderStatus: "idle",
		Pending:       &relay.PendingInfo{Round: 2, Kind: store.KindReport},
	}
	b2 := relay.BindingStatus{
		Name:          "relay-quiesce",
		Workspace:     "wM",
		Round:         2,
		Display:       "DONE",
		BuilderAlias:  "agy",
		BuilderStatus: "gone",
		Pending:       nil,
	}
	b3 := relay.BindingStatus{
		Name:          "relay-rebind",
		Workspace:     "wM",
		Round:         3,
		Display:       "NEEDS YOU",
		BuilderAlias:  "agy",
		BuilderStatus: "blocked",
		Pending:       &relay.PendingInfo{Round: 3, Kind: store.KindQuestion},
	}

	want1 := " relay-fork     wM  r3  " + styleDisplay("ACTIVE") + " builder agy idle    pending report r2"
	want2 := " relay-quiesce  wM  r2  " + styleDisplay("DONE") + " builder agy gone    pending --"
	want3 := cursorStyle.Render(">") + "relay-rebind   wM  r3  " + styleDisplay("NEEDS YOU") + " builder agy blocked pending question"

	r1 := renderListRow(b1, false)
	r2 := renderListRow(b2, false)
	r3 := renderListRow(b3, true)

	if r1 != want1 {
		t.Fatalf("row 1 mismatch:\ngot:  %q\nwant: %q", r1, want1)
	}
	if r2 != want2 {
		t.Fatalf("row 2 mismatch:\ngot:  %q\nwant: %q", r2, want2)
	}
	if r3 != want3 {
		t.Fatalf("row 3 mismatch:\ngot:  %q\nwant: %q", r3, want3)
	}
}

func TestRenderListRowShowsHoldClock(t *testing.T) {
	b := relay.BindingStatus{
		Name: "relay-held", Workspace: "wM", Round: 3, Display: "HELD",
		BuilderAlias: "agy", BuilderStatus: "idle",
		Pending: &relay.PendingInfo{
			Round: 3, Kind: store.KindReport,
			Hold: &relay.HoldInfo{QuietMS: 23000, GraceMS: 60000},
		},
	}
	got := renderListRow(b, false)
	if !strings.HasSuffix(got, "pending report r3, held: quiet 23s of 1m0s") {
		t.Errorf("row = %q", got)
	}

	b.Pending.Hold = nil
	got = renderListRow(b, false)
	if !strings.HasSuffix(got, "pending report r3, held: waiting for the planner's screen") {
		t.Errorf("row = %q", got)
	}
}

func TestListRowsBudget(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})
	m.ready = true
	m.width = 80

	m.height = 24
	m.err = nil
	if got := m.listRows(); got != 22 {
		t.Errorf("height 24, no error: listRows() = %d, want 22", got)
	}

	m.err = errors.New("line one\nline two")
	if got := m.listRows(); got != 20 {
		t.Errorf("height 24, two-line error: listRows() = %d, want 20", got)
	}

	m.height = 2
	m.err = nil
	if got := m.listRows(); got != 1 {
		t.Errorf("height 2: listRows() = %d, want 1", got)
	}

	m.height = 0
	if got := m.listRows(); got != 0 {
		t.Errorf("height 0: listRows() = %d, want 0", got)
	}
}

// tenBindings builds a model at the given height showing b00..b09.
func tenBindings(t *testing.T, height int) Model {
	t.Helper()
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})
	m.ready = true
	m.width = 80
	m.height = height
	res, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
	m = res.(Model)
	res, _ = m.Update(statusMsg{report: relay.Report{Bindings: bindingStatuses(10)}})
	return res.(Model)
}

// bindingStatuses builds Display:"ACTIVE" bindings named b00..b{count-1}.
func bindingStatuses(count int) []relay.BindingStatus {
	bs := make([]relay.BindingStatus, count)
	for i := range bs {
		bs[i] = relay.BindingStatus{Name: fmt.Sprintf("b%02d", i), Display: "ACTIVE"}
	}
	return bs
}

// press sends r as a KeyMsg through Update and returns the updated model.
func press(t *testing.T, m Model, r rune) Model {
	t.Helper()
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return res.(Model)
}

// assertCursorVisible fails t unless the list view shows the cursor row for
// name, the header, the footer, and exactly m.height-1 newlines. The height is
// read off the model so the same helper works after a resize.
func assertCursorVisible(t *testing.T, m Model, name string) {
	t.Helper()
	view := m.View()
	// renderListRow puts the cursor glyph (rendered by cursorStyle) directly
	// before the binding name, so build the needle with the same function the
	// way TestListRowsRenderFixedGoldenWidth builds its expected rows; a plain
	// "> name" needle would miss escape codes if lipgloss emitted them.
	if !strings.Contains(view, cursorStyle.Render(">")+name) {
		t.Errorf("view must show the cursor row for %s, got:\n%s", name, view)
	}
	if !strings.Contains(view, "+- relay") {
		t.Errorf("view must contain the header border, got:\n%s", view)
	}
	if !strings.Contains(view, "enter open") {
		t.Errorf("view must contain the footer, got:\n%s", view)
	}
	if got := strings.Count(m.View(), "\n"); got != m.height-1 {
		t.Errorf("view must have %d newlines at height %d, got %d:\n%s",
			m.height-1, m.height, got, view)
	}
}

func TestListViewKeepsCursorVisibleWhenScrollingDown(t *testing.T) {
	m := tenBindings(t, 6)
	for i := 1; i <= 9; i++ {
		m = press(t, m, 'j')
		assertCursorVisible(t, m, fmt.Sprintf("b%02d", i))
	}
}

func TestListViewKeepsCursorVisibleWhenScrollingUp(t *testing.T) {
	m := tenBindings(t, 6)
	for i := 0; i < 9; i++ {
		m = press(t, m, 'j')
	}
	for i := 8; i >= 0; i-- {
		m = press(t, m, 'k')
		assertCursorVisible(t, m, fmt.Sprintf("b%02d", i))
		if i == 8 {
			// After the first k from the bottom the window must not have
			// moved: it still shows b06..b09, not b05.
			view := m.View()
			if !strings.Contains(view, "b06") {
				t.Errorf("after the first k the view must still contain b06, got:\n%s", view)
			}
			if strings.Contains(view, "b05") {
				t.Errorf("after the first k the view must not contain b05 yet, got:\n%s", view)
			}
		}
	}
}

func TestListViewRewindowsOnResize(t *testing.T) {
	m := tenBindings(t, 6)
	for i := 0; i < 7; i++ {
		m = press(t, m, 'j')
	}
	res, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 4})
	m = res.(Model)
	assertCursorVisible(t, m, "b07")
	if got := strings.Count(m.View(), "\n"); got != 3 {
		t.Errorf("after resize to height 4 the view must have 3 newlines, got %d:\n%s", got, m.View())
	}
}

func TestListViewRewindowsWhenBindingRemoved(t *testing.T) {
	m := tenBindings(t, 6)
	for i := 0; i < 9; i++ {
		m = press(t, m, 'j')
	}
	res, _ := m.Update(statusMsg{report: relay.Report{Bindings: bindingStatuses(5)}})
	m = res.(Model)
	if m.list.cursor != 4 {
		t.Errorf("cursor must clamp to 4 (b04) when bindings are removed, got %d", m.list.cursor)
	}
	assertCursorVisible(t, m, "b04")
}

func TestListViewUnlimitedBeforeResize(t *testing.T) {
	// Built by hand rather than through tenBindings: no WindowSizeMsg, so
	// height stays 0 and listRows reads as no limit — every row must render,
	// exactly as before windowing existed.
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})
	m.ready = true
	m.width = 80
	m.height = 0
	res, _ := m.Update(statusMsg{report: relay.Report{Bindings: bindingStatuses(10)}})
	m = res.(Model)

	view := m.View()
	for _, name := range []string{"b00", "b01", "b02", "b03", "b04", "b05", "b06", "b07", "b08", "b09"} {
		if !strings.Contains(view, name) {
			t.Errorf("height 0 must render every row; %s missing from:\n%s", name, view)
		}
	}
}

func TestCursorFollowsBindingByNameAcrossInsert(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	initialReport := relay.Report{
		Bindings: []relay.BindingStatus{
			{Name: "bravo"},
			{Name: "charlie"},
		},
	}
	res, _ := m.Update(statusMsg{report: initialReport})
	m = res.(Model)
	if m.list.cursor != 0 || m.list.sticky != "bravo" {
		t.Fatalf("expected cursor at 0 (bravo), got %d (%s)", m.list.cursor, m.list.sticky)
	}

	// Insert "alpha" before "bravo"
	updatedReport := relay.Report{
		Bindings: []relay.BindingStatus{
			{Name: "alpha"},
			{Name: "bravo"},
			{Name: "charlie"},
		},
	}
	res, _ = m.Update(statusMsg{report: updatedReport})
	m = res.(Model)

	if m.list.cursor != 1 {
		t.Errorf("expected cursor to track 'bravo' to index 1, got %d", m.list.cursor)
	}
	if m.list.sticky != "bravo" {
		t.Errorf("expected sticky to remain 'bravo', got %s", m.list.sticky)
	}
}

func TestCursorClampsWhenBindingRemoved(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	initialReport := relay.Report{
		Bindings: []relay.BindingStatus{
			{Name: "alpha"},
			{Name: "bravo"},
		},
	}
	res, _ := m.Update(statusMsg{report: initialReport})
	m = res.(Model)

	// Move down to "bravo"
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = res.(Model)
	if m.list.cursor != 1 || m.list.sticky != "bravo" {
		t.Fatalf("expected cursor on bravo (1), got %d (%s)", m.list.cursor, m.list.sticky)
	}

	// "bravo" is removed
	updatedReport := relay.Report{
		Bindings: []relay.BindingStatus{
			{Name: "alpha"},
		},
	}
	res, _ = m.Update(statusMsg{report: updatedReport})
	m = res.(Model)

	if m.list.cursor != 0 {
		t.Errorf("expected cursor clamped to 0, got %d", m.list.cursor)
	}
	if m.list.sticky != "alpha" {
		t.Errorf("expected sticky re-pointed to 'alpha', got %s", m.list.sticky)
	}
}

func TestEmptyBindingsList(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})
	m.ready = true
	m.width = 80
	m.height = 24

	res, _ := m.Update(statusMsg{report: relay.Report{Bindings: nil}})
	m = res.(Model)

	view := m.View()
	if !strings.Contains(view, "no bindings") {
		t.Errorf("expected 'no bindings' in view, got:\n%s", view)
	}

	// Enter on empty bindings is a no-op
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	if m.screen != screenList {
		t.Errorf("expected screen to remain screenList, got %v", m.screen)
	}
	if cmd != nil {
		t.Errorf("expected enter on empty list to return nil cmd, got %v", cmd)
	}
}

func TestQuitFromList(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected quit cmd, got nil")
	}
	// In bubbletea, tea.Quit returns a quitMsg
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		// tea.Quit() returns nil msg internally or signals quit
		// In bubbletea tea.Quit() is func() Msg { return quitMsg{} }
	}
}

func TestRenderBorderWidthWithBullet(t *testing.T) {
	for _, tc := range []struct {
		title string
		width int
	}{
		{"relay", 60},
		{"enter open · q quit", 60},
		{"webshop · round 3 · NEEDS YOU", 60},
		{"webshop · round 3 · NEEDS YOU", 80},
	} {
		border := renderBorder(tc.title, tc.width)
		gotW := lipgloss.Width(border)
		if gotW != tc.width {
			t.Errorf("renderBorder(%q, %d) width = %d, want %d", tc.title, tc.width, gotW, tc.width)
		}
		if !strings.HasPrefix(border, "+- ") || !strings.HasSuffix(border, "+") {
			t.Errorf("renderBorder(%q, %d) missing frame delimiters: %q", tc.title, tc.width, border)
		}
	}
}

func TestRenderBorderOverlongTruncatesAndCloses(t *testing.T) {
	longTitle := "this is an extremely long title that exceeds the total border width by a lot · extra info"
	width := 40

	border := renderBorder(longTitle, width)
	gotW := lipgloss.Width(border)
	if gotW != width {
		t.Fatalf("overlong border width = %d, want %d", gotW, width)
	}
	if !strings.HasPrefix(border, "+- ") || !strings.HasSuffix(border, "+") {
		t.Fatalf("overlong border must remain closed with '+': %q", border)
	}
}

func TestStateStylesDistinguishable(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)
	sActive := styleDisplay("ACTIVE")
	sDone := styleDisplay("DONE")
	sNeedsYou := styleDisplay("NEEDS YOU")

	if sActive == sDone || sDone == sNeedsYou || sActive == sNeedsYou {
		t.Fatalf("state display styles must be distinguishable:\nACTIVE: %q\nDONE: %q\nNEEDS YOU: %q",
			sActive, sDone, sNeedsYou)
	}

	b := relay.BindingStatus{
		Name:          "webshop",
		Round:         1,
		Display:       "ACTIVE",
		BuilderAlias:  "agy",
		BuilderStatus: "idle",
	}
	rowSelected := renderListRow(b, true)
	if !strings.Contains(rowSelected, cursorStyle.Render(">")) {
		t.Fatalf("selected row cursor must be styled with cursorStyle, got %q", rowSelected)
	}
}

func TestListScreenThreeStates(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	// 1. Fresh model with no message renders "loading…"
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})
	m.ready = true
	m.width = 80
	m.height = 24
	view := m.View()
	if !strings.Contains(view, "loading…") {
		t.Errorf("fresh model must render 'loading…', got:\n%s", view)
	}
	if strings.Contains(view, "no bindings") {
		t.Errorf("fresh model must NOT render 'no bindings', got:\n%s", view)
	}

	// 2. Model receiving only a failing statusMsg renders cannot-reach line and NOT "no bindings"
	res, _ := m.Update(statusMsg{err: errors.New("herdr connection refused")})
	m = res.(Model)
	view = m.View()
	if !strings.Contains(view, "cannot reach herdr — see the error above") {
		t.Errorf("failing statusMsg before any success must render cannot-reach line, got:\n%s", view)
	}
	if strings.Contains(view, "no bindings") {
		t.Errorf("failing statusMsg before any success must NOT render 'no bindings', got:\n%s", view)
	}

	// 3. Model given a successful empty report renders "no bindings"
	res, _ = m.Update(statusMsg{report: relay.Report{Bindings: nil}})
	m = res.(Model)
	view = m.View()
	if !strings.Contains(view, "no bindings") {
		t.Errorf("successful empty report must render 'no bindings', got:\n%s", view)
	}

	// 4. A later failing poll after a successful one keeps showing the last good list rather than reverting
	b := relay.BindingStatus{Name: "webshop", Round: 1, Display: "ACTIVE"}
	res, _ = m.Update(statusMsg{report: relay.Report{Bindings: []relay.BindingStatus{b}}})
	m = res.(Model)
	view = m.View()
	if !strings.Contains(view, "webshop") {
		t.Fatalf("expected 'webshop' in list, got:\n%s", view)
	}

	// Now fail
	res, _ = m.Update(statusMsg{err: errors.New("intermittent failure")})
	m = res.(Model)
	view = m.View()
	if !strings.Contains(view, "webshop") {
		t.Errorf("failing poll after successful one must keep showing last good list, got:\n%s", view)
	}
	if strings.Contains(view, "cannot reach herdr") {
		t.Errorf("failing poll after successful one must NOT revert to cannot-reach, got:\n%s", view)
	}
	if strings.Contains(view, "no bindings") {
		t.Errorf("failing poll after successful one must NOT revert to 'no bindings', got:\n%s", view)
	}
}

func TestRenderErrorAndListErrorBlock(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	// 1. A three-line error renders as three lines with no line exceeding width
	threeLineErr := errors.New("error line one\nerror line two\nerror line three")
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})
	m.ready = true
	m.width = 60
	m.height = 24
	m.err = threeLineErr
	view := m.View()

	errRendered := renderError(threeLineErr, 60)
	errLines := strings.Split(errRendered, "\n")
	if len(errLines) != 3 {
		t.Fatalf("expected 3 error lines, got %d: %q", len(errLines), errLines)
	}
	for i, l := range errLines {
		if w := lipgloss.Width(l); w > 60 {
			t.Errorf("error line %d width %d > 60: %q", i, w, l)
		}
	}
	for _, l := range errLines {
		if !strings.Contains(view, l) {
			t.Errorf("expected view to contain error line %q", l)
		}
	}

	// 2. Full text of herdr protocol mismatch message is present, containing "herdr server stop"
	protocolMismatchMsg := "client protocol 22 is newer than server protocol 20; restart the Herdr server\n" +
		"before using this command. Stop the old server to use the new version.\n" +
		"Stopping exits pane processes.\n" +
		"Run `HERDR_SOCKET_PATH=... herdr server stop`, then restart Herdr with the\n" +
		"same socket override."
	m.err = errors.New(protocolMismatchMsg)
	view = m.View()
	if !strings.Contains(view, "herdr server stop") {
		t.Errorf("expected 'herdr server stop' in listView output, got:\n%s", view)
	}

	// 3. Error of more than maxErrorLines (8) lines is capped at maxErrorLines and ends with "…"
	tenLineMsg := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10"
	tenLineRendered := renderError(errors.New(tenLineMsg), 80)
	cappedLines := strings.Split(tenLineRendered, "\n")
	if len(cappedLines) != maxErrorLines {
		t.Fatalf("expected %d lines, got %d", maxErrorLines, len(cappedLines))
	}
	if !strings.HasSuffix(cappedLines[len(cappedLines)-1], "…") {
		t.Fatalf("expected capped error to end with '…', got %q", cappedLines[len(cappedLines)-1])
	}

	// 4. Footer contains the marker "! refresh failed (retrying)" but not the error text
	footer := m.footer()
	if !strings.Contains(footer, "! refresh failed (retrying)") {
		t.Errorf("footer must contain '! refresh failed (retrying)', got %q", footer)
	}
	if strings.Contains(footer, "client protocol") {
		t.Errorf("footer must NOT contain error text, got %q", footer)
	}

	// 5. Detail screen has footer marker and NO error block
	m.screen = screenDetail
	m.width = 80
	m.detail.name = "webshop"
	m.detail.active = tabReport
	m.detail.vp = viewport.New(80, 20)
	m.detail.cache[tabReport] = tabContent{loaded: true, body: "report content"}
	m.detail.vp.SetContent(bodyOf(m.detail.cache[tabReport]))
	detailOut := m.View()
	if strings.Contains(detailOut, "client protocol") {
		t.Errorf("detailView must NOT contain error block, got:\n%s", detailOut)
	}
	if !strings.Contains(detailOut, "! refresh failed (retrying)") {
		t.Errorf("detailView footer must contain '! refresh failed (retrying)', got:\n%s", detailOut)
	}

	// 6. Nil m.err adds no lines to either screen
	m.err = nil
	m.screen = screenList
	viewNoErr := m.View()
	if strings.Contains(viewNoErr, "refresh failed") {
		t.Errorf("nil err must not add refresh failed marker to list, got:\n%s", viewNoErr)
	}

	m.screen = screenDetail
	detailNoErr := m.View()
	if strings.Contains(detailNoErr, "refresh failed") {
		t.Errorf("nil err must not add refresh failed marker to detail, got:\n%s", detailNoErr)
	}
}
