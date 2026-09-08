package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestEnteringDetailFetchesReportTabAndNoOther(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := newModel(context.Background(), rt, Options{Interval: time.Second})
	m.width = 80
	m.height = 24
	m.ready = true

	rep := relay.Report{
		Bindings: []relay.BindingStatus{
			{Name: name, Round: 2, Display: "ACTIVE"},
		},
	}
	res, _ := m.Update(statusMsg{report: rep})
	m = res.(Model)

	// Enter detail screen
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)

	if m.screen != screenDetail {
		t.Fatalf("expected screenDetail, got %v", m.screen)
	}
	if m.detail.name != name {
		t.Fatalf("expected detail.name %s, got %s", name, m.detail.name)
	}
	if m.detail.active != tabReport {
		t.Fatalf("expected active tabReport, got %v", m.detail.active)
	}
	if !m.tabInFlight {
		t.Fatal("expected tabInFlight to be true on enter")
	}

	if cmd == nil {
		t.Fatal("expected non-nil cmd on entering detail")
	}
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg from enter, got %T", msg)
	}
	if tMsg.t != tabReport {
		t.Fatalf("expected tabReport fetch, got tab %v", tMsg.t)
	}
}

func TestSwitchingToUnloadedTabFetchesOnlyThatOne(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Second})
	m.screen = screenDetail
	m.detail.name = "webshop"
	m.detail.active = tabReport
	m.detail.vp = viewport.New(80, 20)

	// Switch to diff tab via key '3'
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	m = res.(Model)

	if m.detail.active != tabDiff {
		t.Fatalf("expected active tabDiff, got %v", m.detail.active)
	}
	if cmd == nil {
		t.Fatal("expected non-nil fetch command for unloaded tab")
	}
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.t != tabDiff {
		t.Fatalf("expected tabDiff fetch, got %v", tMsg.t)
	}
}

func TestScrollParkAndRestore(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Second})
	m.screen = screenDetail
	m.detail.name = "webshop"
	m.detail.active = tabReport
	m.detail.vp = viewport.New(80, 20)

	// Populate caches so switching doesn't trigger unloaded fetch
	m.detail.cache[tabReport] = tabContent{loaded: true, body: strings.Repeat("report line\n", 50)}
	m.detail.cache[tabTerminal] = tabContent{loaded: true, body: strings.Repeat("terminal line\n", 50)}
	m.detail.vp.SetContent(m.detail.cache[tabReport].body)

	// Set scroll offset on report tab
	m.detail.vp.YOffset = 18

	// Switch to terminal tab ('2')
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = res.(Model)
	if m.detail.active != tabTerminal {
		t.Fatalf("expected active tabTerminal, got %v", m.detail.active)
	}
	if m.detail.scroll[tabReport] != 18 {
		t.Fatalf("expected parked scroll for tabReport to be 18, got %d", m.detail.scroll[tabReport])
	}

	// Change offset on terminal tab
	m.detail.vp.YOffset = 7

	// Switch back to report tab ('1')
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = res.(Model)
	if m.detail.active != tabReport {
		t.Fatalf("expected active tabReport, got %v", m.detail.active)
	}
	if m.detail.vp.YOffset != 18 {
		t.Fatalf("expected restored YOffset on tabReport to be 18, got %d", m.detail.vp.YOffset)
	}
	if m.detail.scroll[tabTerminal] != 7 {
		t.Fatalf("expected parked scroll for tabTerminal to be 7, got %d", m.detail.scroll[tabTerminal])
	}
}

func TestEmptyContentNotStyledAsError(t *testing.T) {
	c := tabContent{
		loaded: true,
		empty:  "no diff recorded for round 1 — no baseline captured",
	}

	st := styleFor(c)
	if reflect.DeepEqual(st, errorStyle) {
		t.Fatalf("empty content style must not be errorStyle")
	}
	if st.GetForeground() == errorStyle.GetForeground() {
		t.Fatalf("empty content foreground must not match errorStyle foreground")
	}

	rendered := bodyOf(c)
	if !strings.Contains(rendered, c.empty) {
		t.Fatalf("expected rendered body to contain %q, got %q", c.empty, rendered)
	}
}

func TestTabErrorDoesNotCorruptOtherTabs(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Second})
	m.screen = screenDetail
	m.detail.name = "webshop"
	m.detail.vp = viewport.New(80, 20)

	m.detail.cache[tabDiff] = tabContent{
		loaded: true,
		err:    errors.New("disk read failed"),
	}
	m.detail.cache[tabReport] = tabContent{
		loaded: true,
		body:   "## Successful report content",
	}

	// Active tab diff shows error
	m.detail.active = tabDiff
	m.detail.vp.SetContent(bodyOf(m.detail.cache[tabDiff]))
	if !strings.Contains(m.detail.vp.View(), "error: disk read failed") {
		t.Fatalf("expected error text in diff tab, got %q", m.detail.vp.View())
	}

	// Switch to report tab: it stays readable
	res, _ := m.switchTab(tabReport)
	m = res.(Model)
	if !strings.Contains(m.detail.vp.View(), "Successful report content") {
		t.Fatalf("expected report content in report tab, got %q", m.detail.vp.View())
	}
}

func TestResizeReflowsViewportWithoutLosingActiveTab(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Second})
	m.screen = screenDetail
	m.detail.name = "webshop"
	m.detail.active = tabDiff
	m.detail.vp = viewport.New(80, 20)

	res, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	m = res.(Model)

	if m.detail.active != tabDiff {
		t.Fatalf("expected active tab to remain tabDiff, got %v", m.detail.active)
	}
	if m.detail.vp.Width != 120 {
		t.Fatalf("expected vp.Width 120, got %d", m.detail.vp.Width)
	}
	if m.detail.vp.Height != 60-chromeHeight {
		t.Fatalf("expected vp.Height %d, got %d", 60-chromeHeight, m.detail.vp.Height)
	}
}
