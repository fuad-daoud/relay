package ui

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/serve"
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
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

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
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

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
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

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
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

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
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

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
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

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
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

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
	if want := updated.viewportHeight(); updated.detail.vp.Height != want {
		t.Errorf("expected vp height %d, got %d", want, updated.detail.vp.Height)
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
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
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

// TestStaleRoundReplyDiscardedForReport pins #183's generalisation: report
// is now round-scoped exactly like diff (fetchReport reads round's own
// entry, not "the newest one logged"), so a reply for a round that is no
// longer on screen must be discarded, not accepted as a legitimate lag.
func TestStaleRoundReplyDiscardedForReport(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.screen = screenDetail
	m.detail.name = "webshop"
	m.detail.round = 4
	m.detail.active = tabReport

	staleMsg := tabMsg{
		name:  "webshop",
		round: 3, // stale round
		t:     tabReport,
		content: tabContent{
			loaded: true,
			body:   "stale report content",
		},
	}

	res, _ := m.Update(staleMsg)
	updated := res.(Model)

	if updated.detail.cache[tabReport].loaded {
		t.Error("stale report tabMsg must be discarded and not update cache")
	}
	if updated.detail.cache[tabReport].body != "" {
		t.Errorf("expected empty cache body, got %q", updated.detail.cache[tabReport].body)
	}
}

func TestMaybeInvalidateBlockedWhenTabInFlight(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
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

	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
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
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

	if !m.statusInFlight {
		t.Fatal("newModel must initialize statusInFlight = true to guard the Init fetch")
	}

	_, cmd := m.Update(tickMsg(time.Now()))
	batch := extractBatch(cmd)
	if hasStatusMsg(batch) {
		t.Fatal("tickMsg arriving before first statusMsg must not issue second status fetch")
	}
}

func TestEmptyIsFalseBeforeLoad(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

	if m.statusLoaded {
		t.Fatal("newModel must start with statusLoaded = false")
	}
	if m.empty() {
		t.Fatal("empty() must report false when statusLoaded is false")
	}

	m.statusInFlight = false
	res, _ := m.Update(statusMsg{report: relay.Report{}})
	loaded := res.(Model)
	if !loaded.statusLoaded {
		t.Fatal("statusMsg must set statusLoaded = true")
	}
	if !loaded.empty() {
		t.Fatal("empty() must report true when statusLoaded is true and rows are empty")
	}
}

func TestEmptyFleetFooter(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	m := newModel(context.Background(), plannerSource{relay.Runtime{Store: st, Herdr: fh}}, Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: relay.Report{}})
	m = res.(Model)

	for _, tc := range []struct {
		name    string
		sort    bool
		compact bool
		wantC   string
	}{
		{"attention cards", true, false, "c compact"},
		{"name cards", false, false, "c compact"},
		{"attention compact", true, true, "c cards"},
		{"name compact", false, true, "c cards"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.sort = tc.sort
			m.compact = tc.compact
			plain := stripANSI(m.footerView())

			for _, want := range []string{"s sort", tc.wantC, "q quit"} {
				if !strings.Contains(plain, want) {
					t.Errorf("footer missing %q: %q", want, plain)
				}
			}
			for _, forbidden := range []string{"↑↓", "⏎", "tab", "1-4", "esc"} {
				if strings.Contains(plain, forbidden) {
					t.Errorf("footer contains forbidden %q: %q", forbidden, plain)
				}
			}
		})
	}
}

// TestEmptyFleetKeysDoNotFocusPane pins the guards in keys.go that keep
// focus out of the pane at zero rows: tab, shift+tab, 1-4 and enter must
// all be no-ops on an empty, loaded fleet.
func TestEmptyFleetKeysDoNotFocusPane(t *testing.T) {
	m := splitModel(t, 140, 40)
	if !m.empty() {
		t.Fatal("fixture must be empty")
	}
	startActive := m.detail.active

	keys := []tea.KeyMsg{
		{Type: tea.KeyTab},
		{Type: tea.KeyShiftTab},
		{Type: tea.KeyRunes, Runes: []rune{'1'}},
		{Type: tea.KeyRunes, Runes: []rune{'4'}},
		{Type: tea.KeyEnter},
	}
	for _, k := range keys {
		res, _ := m.Update(k)
		m = res.(Model)
		if m.screen != screenList {
			t.Errorf("key %q: screen = %v, want screenList", k.String(), m.screen)
		}
		if m.detail.active != startActive {
			t.Errorf("key %q: detail.active changed to %v", k.String(), m.detail.active)
		}
	}
}

// TestEmptyFleetSnapsBackToList pins the statusMsg arm's snap: rows
// dropping to zero while the pane is focused must land back on the rail,
// with exactly the existing "is gone" notice and none added on top of it.
func TestEmptyFleetSnapsBackToList(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()[:1]...)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	if m.screen != screenDetail {
		t.Fatal("fixture must start with the pane focused")
	}
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: relay.Report{}})
	m = res.(Model)

	if m.screen != screenList {
		t.Errorf("screen = %v, want screenList", m.screen)
	}
	if m.notice == "" {
		t.Error("the existing \"is gone\" notice must still fire")
	}
	if n := strings.Count(m.notice, "is gone"); n != 1 {
		t.Errorf("notice must carry exactly one \"is gone\", got %d: %q", n, m.notice)
	}
}

// TestKeyAToggleWithoutDBNotices pins §6's error handling: pressing "a"
// with no database leaves scope on live, sets a sticky notice, and never
// exits or panics.
func TestKeyAToggleWithoutDBNotices(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...) // rt.DB is nil: newFakeHerdr's runtime carries no *db.DB
	m.opts.PrefsPath = filepath.Join(t.TempDir(), "ui.json")

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = res.(Model)

	if m.scope != scopeLive {
		t.Errorf("scope = %v, want scopeLive (no database: toggle must not stick)", m.scope)
	}
	if m.notice == "" {
		t.Error("notice must be set when toggling to all with no database")
	}
	if !strings.Contains(m.notice, "no database") {
		t.Errorf("notice = %q, want it to name the database", m.notice)
	}
	if cmd == nil {
		t.Fatal("expected a save command even when the toggle reverts")
	}
}

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
func TestStepRoundBackRefetchesEveryTab(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	b.Round = 3 // rounds 1 and 2 closed; round 3 is open
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.width, m.height, m.ready = 140, 40, true
	rep := relay.Report{Bindings: []relay.BindingStatus{{Name: name, Round: 3, Display: "ACTIVE"}}}
	res, _ := m.Update(statusMsg{report: rep})
	m = res.(Model)

	if m.detail.round != 2 || m.detail.rounds != 3 {
		t.Fatalf("after pointing: round=%d rounds=%d, want round=2 rounds=3", m.detail.round, m.detail.rounds)
	}

	// Populate every tab's cache so the invalidation is visible.
	for tb := tab(0); tb < tabCount; tb++ {
		m.detail.cache[tb] = tabContent{loaded: true, body: "stale"}
	}
	m.tabInFlight = false

	for i := 0; i < 2; i++ {
		res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
		m = res.(Model)
		m.tabInFlight = false
	}
	if m.detail.round != 1 {
		t.Fatalf("after \"[\" twice: round = %d, want 1", m.detail.round)
	}
	for tb := tab(0); tb < tabCount; tb++ {
		if m.detail.cache[tb].loaded {
			t.Errorf("tab %v cache still loaded after stepping back", tb)
		}
	}

	for i := 0; i < 3; i++ {
		res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
		m = res.(Model)
		m.tabInFlight = false
	}
	if m.detail.round != 3 {
		t.Fatalf("after \"]\" three times: round = %d, want 3 (the open round)", m.detail.round)
	}

	reportMsg := fetchReport(context.Background(), plannerSource{rt}, name, m.detail.round)().(tabMsg)
	wantReport := "round 3 is open; report arrives when it closes"
	if reportMsg.content.empty != wantReport {
		t.Errorf("report empty = %q, want %q", reportMsg.content.empty, wantReport)
	}
	diffMsg := fetchDiff(context.Background(), plannerSource{rt}, name, m.detail.round)().(tabMsg)
	wantDiff := "diff is captured when round 3 closes"
	if diffMsg.content.empty != wantDiff {
		t.Errorf("diff empty = %q, want %q", diffMsg.content.empty, wantDiff)
	}
}

// TestStepRoundEdgesNoop pins #183's clamp: stepping past either edge of
// [1, detail.rounds] changes nothing -- not the round, not the cache.
func TestStepRoundEdgesNoop(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.width, m.height, m.ready = 140, 40, true
	m.detail.name = "webshop"
	m.detail.round = 1
	m.detail.rounds = 3
	m.detail.active = tabReport
	m.detail.cache[tabReport] = tabContent{loaded: true, body: "round 1"}

	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	low := res.(Model)
	if low.detail.round != 1 {
		t.Errorf("\"[\" at round 1: round = %d, want 1 (no-op)", low.detail.round)
	}
	if !low.detail.cache[tabReport].loaded {
		t.Error("\"[\" at round 1: cache must stay untouched (no-op)")
	}

	m.detail.round = 3
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	high := res.(Model)
	if high.detail.round != 3 {
		t.Errorf("\"]\" at round 3 (rounds=3): round = %d, want 3 (no-op)", high.detail.round)
	}
	if !high.detail.cache[tabReport].loaded {
		t.Error("\"]\" at round 3: cache must stay untouched (no-op)")
	}
}
