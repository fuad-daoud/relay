package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/serve"
)

func extractBatch(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return []tea.Cmd(batch)
	}
	return []tea.Cmd{cmd}
}

func hasStatusMsg(batch []tea.Cmd) bool {
	// batch[0] is tick. Subsequent commands are fetches.
	for i := 1; i < len(batch); i++ {
		msg := batch[i]()
		if _, ok := msg.(statusMsg); ok {
			return true
		}
	}
	return false
}

func hasTabMsg(batch []tea.Cmd) bool {
	for i := 1; i < len(batch); i++ {
		msg := batch[i]()
		if _, ok := msg.(tabMsg); ok {
			return true
		}
	}
	return false
}

func TestRowHelper(t *testing.T) {
	rep := relay.Report{
		Bindings: []relay.BindingStatus{
			{Name: "a", Round: 1},
			{Name: "b", Round: 2},
		},
	}

	ra := row(rep, "a")
	if ra == nil || ra.Name != "a" || ra.Round != 1 {
		t.Errorf("expected row 'a', got %+v", ra)
	}

	rc := row(rep, "c")
	if rc != nil {
		t.Errorf("expected nil for 'c', got %+v", rc)
	}
}

// TestStaleRoundReplyDiscardedForReport pins #183's generalisation: report
// is now round-scoped exactly like diff (fetchReport reads round's own
// entry, not "the newest one logged"), so a reply for a round that is no
// longer on screen must be discarded, not accepted as a legitimate lag.
// TestEmptyFleetKeysDoNotFocusPane pins the guards in keys.go that keep
// focus out of the pane at zero rows: tab, shift+tab, 1-4 and enter must
// all be no-ops on an empty, loaded fleet.
// TestEmptyFleetSnapsBackToList pins the statusMsg arm's snap: rows
// dropping to zero while the pane is focused must land back on the rail,
// with exactly the existing "is gone" notice and none added on top of it.
// TestKeyAToggleWithoutDBNotices pins §6's error handling: pressing "a"
// with no database leaves scope on live, sets a sticky notice, and never
// exits or panics.
// TestScopeAllRefusedOnServer pins §2's seam end to end: a model built
// over a serverSource presses "a", and because the server box carries no
// database (serverSource.Base().DB == nil), the toggle refuses -- scope
// stays live and the sticky notice names the missing database.
func TestScopeAllRefusedOnServer(t *testing.T) {
	srv, err := serve.New(serve.Config{Root: t.TempDir(), Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m := newModel(context.Background(), ServerSource(srv), Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	m.statusInFlight = false
	m.opts.PrefsPath = filepath.Join(t.TempDir(), "ui.json")

	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = res.(Model)

	if m.scope != scopeLive {
		t.Errorf("scope = %v, want scopeLive (the server has no database)", m.scope)
	}
	if m.notice == "" {
		t.Error("notice must be set when the server refuses scope all")
	}
	if !strings.Contains(m.notice, "no database") {
		t.Errorf("notice = %q, want it to name the database", m.notice)
	}
}

// TestStepRoundBackRefetchesEveryTab pins #183's round stepping end to end:
// "[" and "]" move detail.round within [1, detail.rounds], clamping at
// either edge, and invalidate every tab's cache -- not just the active
// one -- so a later switch to any tab re-fetches instead of showing a
// different round's stale content.
// TestStepRoundEdgesNoop pins #183's clamp: stepping past either edge of
// [1, detail.rounds] changes nothing -- not the round, not the cache.
// TestPointDetailAtMarksViewed pins #143: pointing the pane at a binding
// stamps its .viewed sidecar through the Source, exercised here against the
// real plannerSource (the ui package has no separate fake Source double;
// plannerSource's own MarkViewed writes through rt.Store, which is exactly
// the write pointDetailAt is supposed to trigger).
