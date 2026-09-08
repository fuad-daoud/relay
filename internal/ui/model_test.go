package ui

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
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

func TestSingleFlightStatusInFlightBlocksSecondFetch(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	m.statusInFlight = true
	m.tabInFlight = false

	res, cmd := m.Update(tickMsg(time.Now()))
	updated := res.(Model)

	if !updated.statusInFlight {
		t.Error("expected statusInFlight to remain true")
	}

	batch := extractBatch(cmd)
	if hasStatusMsg(batch) {
		t.Error("tickMsg while statusInFlight must not issue fetchStatus")
	}
}

func TestSingleFlightTabInFlightStillIssuesStatus(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	m.statusInFlight = false
	m.tabInFlight = true

	res, cmd := m.Update(tickMsg(time.Now()))
	updated := res.(Model)

	if !updated.statusInFlight {
		t.Error("expected statusInFlight to become true")
	}

	batch := extractBatch(cmd)
	if !hasStatusMsg(batch) {
		t.Fatal("tickMsg while tabInFlight MUST still issue fetchStatus (two-guard requirement)")
	}
}

func TestSingleFlightTabInFlightBlocksSecondTabFetch(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	m.screen = screenDetail
	m.detail.name = "webshop"
	m.detail.active = tabTerminal
	m.statusInFlight = true
	m.tabInFlight = true

	_, cmd := m.Update(tickMsg(time.Now()))
	batch := extractBatch(cmd)

	if hasTabMsg(batch) {
		t.Error("tickMsg while tabInFlight must not issue second tab fetch")
	}
}

func TestStatusMsgErrorPreservesReport(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	initialReport := relay.Report{
		Bindings: []relay.BindingStatus{
			{Name: "webshop", Round: 2, State: "active", Display: "ACTIVE"},
		},
	}
	m.report = initialReport
	m.statusInFlight = true

	testErr := errors.New("transient herdr failure")
	res, _ := m.Update(statusMsg{err: testErr})
	updated := res.(Model)

	if updated.statusInFlight {
		t.Error("statusMsg should clear statusInFlight")
	}
	if updated.err == nil || updated.err.Error() != testErr.Error() {
		t.Errorf("expected err %v, got %v", testErr, updated.err)
	}
	if !reflect.DeepEqual(updated.report, initialReport) {
		t.Errorf("report was modified on error:\ngot:  %+v\nwant: %+v", updated.report, initialReport)
	}
}

func TestStatusMsgSuccessClearsError(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	m.err = errors.New("transient error")
	m.statusInFlight = true

	goodReport := relay.Report{
		Bindings: []relay.BindingStatus{
			{Name: "webshop", Round: 3, State: "active", Display: "ACTIVE"},
		},
	}
	res, _ := m.Update(statusMsg{report: goodReport})
	updated := res.(Model)

	if updated.statusInFlight {
		t.Error("statusMsg should clear statusInFlight")
	}
	if updated.err != nil {
		t.Errorf("expected err to be cleared, got %v", updated.err)
	}
	if len(updated.report.Bindings) != 1 || updated.report.Bindings[0].Name != "webshop" {
		t.Errorf("unexpected report: %+v", updated.report)
	}
}

func TestTabMsgMismatchedBindingDiscarded(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	m.screen = screenDetail
	m.detail.name = "webshop"
	m.detail.active = tabReport
	m.tabInFlight = true

	lateMsg := tabMsg{
		name:  "other-binding",
		round: 2,
		t:     tabReport,
		content: tabContent{
			loaded: true,
			body:   "should be ignored",
		},
	}

	res, _ := m.Update(lateMsg)
	updated := res.(Model)

	if updated.tabInFlight {
		t.Error("tabMsg should clear tabInFlight")
	}
	if updated.detail.cache[tabReport].loaded {
		t.Error("cache should not be populated with mismatched binding response")
	}
	if updated.detail.cache[tabReport].body != "" {
		t.Errorf("cache body should be empty, got %q", updated.detail.cache[tabReport].body)
	}
}

func TestWindowSizeMsgSetsReady(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	m.ready = false
	res, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	updated := res.(Model)

	if !updated.ready {
		t.Error("WindowSizeMsg should set ready to true")
	}
	if updated.width != 100 || updated.height != 40 {
		t.Errorf("expected 100x40, got %dx%d", updated.width, updated.height)
	}
	if updated.detail.vp.Width != 100 {
		t.Errorf("expected vp width 100, got %d", updated.detail.vp.Width)
	}
	if updated.detail.vp.Height != 40-chromeHeight {
		t.Errorf("expected vp height %d, got %d", 40-chromeHeight, updated.detail.vp.Height)
	}
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

func TestStaleRoundReplyDiscardedForDiff(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})
	m.screen = screenDetail
	m.detail.name = "webshop"
	m.detail.round = 4
	m.detail.active = tabDiff

	staleMsg := tabMsg{
		name:  "webshop",
		round: 3, // stale round
		t:     tabDiff,
		content: tabContent{
			loaded: true,
			body:   "old diff content",
		},
	}

	res, _ := m.Update(staleMsg)
	updated := res.(Model)

	if updated.detail.cache[tabDiff].loaded {
		t.Error("stale diff tabMsg must be discarded and not update cache")
	}
	if updated.detail.cache[tabDiff].body != "" {
		t.Errorf("expected empty cache body, got %q", updated.detail.cache[tabDiff].body)
	}
}

func TestLaggingRoundAcceptedForReport(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})
	m.screen = screenDetail
	m.detail.name = "webshop"
	m.detail.round = 4
	m.detail.active = tabReport

	laggingMsg := tabMsg{
		name:  "webshop",
		round: 3, // legitimately lagging round
		t:     tabReport,
		content: tabContent{
			loaded: true,
			body:   "lagging report content",
		},
	}

	res, _ := m.Update(laggingMsg)
	updated := res.(Model)

	if !updated.detail.cache[tabReport].loaded {
		t.Error("lagging report tabMsg must be accepted into cache")
	}
	if updated.detail.cache[tabReport].body != "lagging report content" {
		t.Errorf("expected lagging report content, got %q", updated.detail.cache[tabReport].body)
	}
}

func TestMaybeInvalidateBlockedWhenTabInFlight(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})
	name := "webshop"
	ts := time.Now()

	m.screen = screenDetail
	m.detail.name = name
	m.detail.active = tabReport
	m.detail.lastLogTS = ts
	m.tabInFlight = true

	rep := relay.Report{
		Bindings: []relay.BindingStatus{
			{
				Name:  name,
				Round: 3,
				Last: &relay.LastEvent{
					TS:    ts.Add(5 * time.Second),
					Round: 3,
				},
			},
		},
	}

	res, cmd := m.Update(statusMsg{report: rep})
	updated := res.(Model)

	if cmd != nil {
		t.Error("maybeInvalidate must issue no fetch while tabInFlight is set")
	}
	if !updated.tabInFlight {
		t.Error("tabInFlight must remain true")
	}
}

func TestEnterPressedTwiceIssuesOneFetch(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})
	m.statusInFlight = false
	rep := relay.Report{
		Bindings: []relay.BindingStatus{
			{Name: name, Round: 2, Display: "ACTIVE"},
		},
	}
	res, _ := m.Update(statusMsg{report: rep})
	m = res.(Model)

	// First enter
	res1, cmd1 := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd1 == nil {
		t.Fatal("first enter must return non-nil cmd")
	}

	// Second enter while tabInFlight is true
	_, cmd2 := res1.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 != nil {
		t.Fatal("second enter while tabInFlight is set must return nil cmd")
	}
}

func TestTickBeforeFirstStatusIssuesNoSecondFetch(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Millisecond})

	if !m.statusInFlight {
		t.Fatal("newModel must initialize statusInFlight = true to guard the Init fetch")
	}

	_, cmd := m.Update(tickMsg(time.Now()))
	batch := extractBatch(cmd)
	if hasStatusMsg(batch) {
		t.Fatal("tickMsg arriving before first statusMsg must not issue second status fetch")
	}
}
