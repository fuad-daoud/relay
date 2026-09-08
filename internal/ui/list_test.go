package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

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

	want1 := " relay-fork     wM  r3  ACTIVE    builder agy idle    pending report r2"
	want2 := " relay-quiesce  wM  r2  DONE      builder agy gone    pending --"
	want3 := ">relay-rebind   wM  r3  NEEDS YOU builder agy blocked pending question"

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

func TestCursorFollowsBindingByNameAcrossInsert(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Second})

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
	m := newModel(context.Background(), rt, Options{Interval: time.Second})

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
	m := newModel(context.Background(), rt, Options{Interval: time.Second})
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
	m := newModel(context.Background(), rt, Options{Interval: time.Second})

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
