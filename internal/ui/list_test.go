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
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
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

func TestListRowsBudget(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.ready = true
	m.width = 80

	m.height = 24
	m.err = nil
	if got := m.bodyRows(); got != 21 {
		t.Errorf("height 24, no error: bodyRows() = %d, want 21", got)
	}

	m.err = errors.New("line one\nline two")
	if got := m.bodyRows(); got != 19 {
		t.Errorf("height 24, two-line error: bodyRows() = %d, want 19", got)
	}

	m.height = 2
	m.err = nil
	if got := m.bodyRows(); got != 0 {
		t.Errorf("height 2: bodyRows() = %d, want 0", got)
	}

	m.height = 0
	if got := m.bodyRows(); got != 0 {
		t.Errorf("height 0: bodyRows() = %d, want 0", got)
	}
}

// tenBindings builds a model at the given height showing b00..b09.
func tenBindings(t *testing.T, height int) Model {
	t.Helper()
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.ready = true
	m.width = 80
	m.height = height
	res, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
	m = res.(Model)
	res, _ = m.Update(statusMsg{report: relevo.Report{Bindings: bindingStatuses(10)}})
	return res.(Model)
}

// bindingStatuses builds Display:"ACTIVE" bindings named b00..b{count-1}.
func bindingStatuses(count int) []relevo.BindingStatus {
	bs := make([]relevo.BindingStatus, count)
	for i := range bs {
		bs[i] = relevo.BindingStatus{Name: fmt.Sprintf("b%02d", i), Display: "ACTIVE"}
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
	if !strings.Contains(plain(view), "▎ "+name) {
		t.Errorf("view must show the cursor card for %s, got:\n%s", name, view)
	}
	if !strings.Contains(plain(view), "relevo") {
		t.Errorf("view must contain the header, got:\n%s", view)
	}
	if !strings.Contains(view, "open") {
		t.Errorf("view must contain the footer, got:\n%s", view)
	}
	if got := strings.Count(m.View(), "\n"); got != m.height-1 {
		t.Errorf("view must have %d newlines at height %d, got %d:\n%s",
			m.height-1, m.height, got, view)
	}
}

func TestListViewKeepsCursorVisibleWhenScrollingDown(t *testing.T) {
	m := tenBindings(t, 15)
	for i := 1; i <= 9; i++ {
		m = press(t, m, 'j')
		assertCursorVisible(t, m, fmt.Sprintf("b%02d", i))
	}
}

func TestListViewKeepsCursorVisibleWhenScrollingUp(t *testing.T) {
	m := tenBindings(t, 15)
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
	m := tenBindings(t, 15)
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
	m := tenBindings(t, 15)
	for i := 0; i < 9; i++ {
		m = press(t, m, 'j')
	}
	res, _ := m.Update(statusMsg{report: relevo.Report{Bindings: bindingStatuses(5)}})
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
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.ready = true
	m.width = 80
	m.height = 0
	res, _ := m.Update(statusMsg{report: relevo.Report{Bindings: bindingStatuses(10)}})
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
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

	initialReport := relevo.Report{
		Bindings: []relevo.BindingStatus{
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
	updatedReport := relevo.Report{
		Bindings: []relevo.BindingStatus{
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
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

	initialReport := relevo.Report{
		Bindings: []relevo.BindingStatus{
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
	updatedReport := relevo.Report{
		Bindings: []relevo.BindingStatus{
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
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.ready = true
	m.width = 80
	m.height = 24

	res, _ := m.Update(statusMsg{report: relevo.Report{Bindings: nil}})
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
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

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

func TestStateStylesDistinguishable(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	states := []string{"NEEDS YOU", "HELD", "ACTIVE", "DONE"}
	rendered := make(map[string]string, len(states))
	for _, s := range states {
		rendered[s] = stateStyle(s).Render(s)
	}
	for i, a := range states {
		for _, b := range states[i+1:] {
			if rendered[a] == rendered[b] {
				t.Fatalf("state display styles must be distinguishable:\n%s: %q\n%s: %q", a, rendered[a], b, rendered[b])
			}
		}
	}
	if stateStyle("HELD").Render("HELD") == normalStyle.Render("HELD") {
		t.Fatalf("HELD must be styled, not left as normalStyle (that was the old regression)")
	}
	// The selected-card gutter styling is pinned by TestCardLinesShapes'
	// "blocked" case ("▎ webshop"); renderListRow/cursorStyle are gone
	// (Task 3).
}

func TestListScreenThreeStates(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	// 1. Fresh model with no message renders "loading…"
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
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
	res, _ := m.Update(statusMsg{err: errors.New("harness connection refused")})
	m = res.(Model)
	view = m.View()
	if !strings.Contains(view, "status unavailable — see the error above") {
		t.Errorf("failing statusMsg before any success must render status-unavailable line, got:\n%s", view)
	}
	if strings.Contains(view, "no bindings") {
		t.Errorf("failing statusMsg before any success must NOT render 'no bindings', got:\n%s", view)
	}

	// 3. Model given a successful empty report renders "no bindings"
	res, _ = m.Update(statusMsg{report: relevo.Report{Bindings: nil}})
	m = res.(Model)
	view = m.View()
	if !strings.Contains(view, "no bindings") {
		t.Errorf("successful empty report must render 'no bindings', got:\n%s", view)
	}

	// 4. A later failing poll after a successful one keeps showing the last good list rather than reverting
	b := relevo.BindingStatus{Name: "webshop", Round: 1, Display: "ACTIVE"}
	res, _ = m.Update(statusMsg{report: relevo.Report{Bindings: []relevo.BindingStatus{b}}})
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
	if strings.Contains(view, "cannot reach the daemon") {
		t.Errorf("failing poll after successful one must NOT revert to cannot-reach, got:\n%s", view)
	}
	if strings.Contains(view, "no bindings") {
		t.Errorf("failing poll after successful one must NOT revert to 'no bindings', got:\n%s", view)
	}
}

func TestRenderErrorAndListErrorBlock(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	// 1. A three-line error renders as three lines with no line exceeding width
	threeLineErr := errors.New("error line one\nerror line two\nerror line three")
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
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

	// 2. A multi-line error renders in full, its last line included
	multiLineMsg := "client protocol 22 is newer than server protocol 20; restart the daemon\n" +
		"before using this command. Stop the old process to use the new version.\n" +
		"Stopping exits running processes.\n" +
		"Run `relevo daemon --stop`, then restart relevo with the\n" +
		"same socket override."
	m.err = errors.New(multiLineMsg)
	view = m.View()
	if !strings.Contains(view, "same socket override") {
		t.Errorf("expected the last line of a multi-line error in listView output, got:\n%s", view)
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
	footer := stripANSI(m.footerView())
	if !strings.Contains(footer, "! refresh failed (retrying)") {
		t.Errorf("footer must contain '! refresh failed (retrying)', got %q", footer)
	}
	if strings.Contains(footer, "client protocol") {
		t.Errorf("footer must NOT contain error text, got %q", footer)
	}

	// 5. Detail screen has footer marker and NO error block
	m.screen = screenDetail
	m.width = 80
	m.pane.detail.name = "webshop"
	m.pane.detail.active = tabReport
	m.pane.detail.vp = viewport.New(80, 20)
	m.pane.detail.cache[tabReport] = tabContent{loaded: true, body: "report content"}
	m.pane.detail.vp.SetContent(bodyOf(tabReport, m.pane.detail.cache[tabReport], false))
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
