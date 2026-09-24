package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestStatusMsgUnchangedTSNoFetch(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

	name := "webshop"
	ts := time.Now().Truncate(time.Second)

	m.screen = screenDetail
	m.pane.detail.name = name
	m.pane.detail.live = true
	m.pane.detail.active = tabReport
	m.pane.detail.lastLogTS = ts
	m.pane.detail.cache[tabReport] = tabContent{loaded: true, body: "initial report"}

	rep := relevo.Report{
		Bindings: []relevo.BindingStatus{
			{
				Name:  name,
				Round: 2,
				Last: &relevo.LastEvent{
					TS:    ts,
					Round: 2,
				},
			},
		},
	}

	res, cmd := m.Update(statusMsg{report: rep})
	m = res.(Model)

	if cmd != nil {
		t.Fatalf("expected nil cmd for unchanged TS, got %v", cmd)
	}
	if !m.pane.detail.cache[tabReport].loaded {
		t.Fatal("expected report cache to remain loaded when TS unchanged")
	}
}

func TestStatusMsgNewerTSClearsFileCachesPreservesTerminal(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

	ts := time.Now().Truncate(time.Second)

	m.screen = screenDetail
	m.pane.detail.name = name
	m.pane.detail.live = true
	m.pane.detail.active = tabReport
	m.pane.detail.lastLogTS = ts
	m.pane.detail.vp = viewport.New(80, 20)

	m.pane.detail.cache[tabReport] = tabContent{loaded: true, body: "cached report"}
	m.pane.detail.cache[tabDiff] = tabContent{loaded: true, body: "cached diff"}
	m.pane.detail.cache[tabLog] = tabContent{loaded: true, body: "cached log"}
	m.pane.detail.cache[tabTerminal] = tabContent{loaded: true, body: "live terminal"}

	newTS := ts.Add(10 * time.Second)
	rep := relevo.Report{
		Bindings: []relevo.BindingStatus{
			{
				Name:  name,
				Round: 4,
				Last: &relevo.LastEvent{
					TS:    newTS,
					Round: 4,
				},
			},
		},
	}

	res, cmd := m.Update(statusMsg{report: rep})
	m = res.(Model)

	if m.pane.detail.cache[tabReport].loaded {
		t.Error("tabReport cache should be cleared on new TS")
	}
	if m.pane.detail.cache[tabDiff].loaded {
		t.Error("tabDiff cache should be cleared on new TS")
	}
	if m.pane.detail.cache[tabLog].loaded {
		t.Error("tabLog cache should be cleared on new TS")
	}
	if !m.pane.detail.cache[tabTerminal].loaded || m.pane.detail.cache[tabTerminal].body != "live terminal" {
		t.Error("tabTerminal cache must be untouched by log invalidation")
	}

	if m.pane.detail.round != 3 {
		t.Errorf("expected detail.round to update to 3 (row.Round-1), got %d", m.pane.detail.round)
	}
	if !m.pane.detail.lastLogTS.Equal(newTS) {
		t.Errorf("expected lastLogTS to update to %v, got %v", newTS, m.pane.detail.lastLogTS)
	}

	if cmd == nil {
		t.Fatal("expected refetch cmd for active tab after invalidation")
	}
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg from refetch cmd, got %T", msg)
	}
	if tMsg.t != tabReport {
		t.Fatalf("expected refetch of active tab (tabReport), got %v", tMsg.t)
	}
}

func TestScrollPreservedAcrossStatusMsgWithoutInvalidation(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})

	name := "webshop"
	ts := time.Now().Truncate(time.Second)

	m.screen = screenDetail
	m.pane.detail.name = name
	m.pane.detail.live = true
	m.pane.detail.active = tabReport
	m.pane.detail.lastLogTS = ts
	m.pane.detail.vp = viewport.New(80, 20)
	m.pane.detail.vp.SetContent(strings.Repeat("line\n", 100))
	m.pane.detail.vp.YOffset = 33

	rep := relevo.Report{
		Bindings: []relevo.BindingStatus{
			{
				Name:  name,
				Round: 2,
				Last: &relevo.LastEvent{
					TS:    ts,
					Round: 2,
				},
			},
		},
	}

	res, _ := m.Update(statusMsg{report: rep})
	m = res.(Model)

	if m.pane.detail.vp.YOffset != 33 {
		t.Errorf("expected scroll offset 33 preserved, got %d", m.pane.detail.vp.YOffset)
	}
}

func TestTerminalTabPollsOnEveryTickWhenVisible(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.screen = screenDetail
	m.pane.detail.name = name
	m.pane.detail.live = true
	m.pane.detail.active = tabTerminal
	m.pane.detail.vp = viewport.New(80, 20)
	m.statusInFlight = false
	m.pane.tabInFlight = false

	// Tick while terminal is visible -> issues tab fetch
	res, cmd := m.Update(tickMsg(time.Now()))
	m = res.(Model)

	batch := extractBatch(cmd)
	if !hasTabMsg(batch) {
		t.Error("terminal tab must issue fetch on every tick while visible")
	}

	// Switch to report tab with cached content
	m.pane.detail.active = tabReport
	m.pane.detail.cache[tabReport] = tabContent{loaded: true, body: "report"}
	m.statusInFlight = false
	m.pane.tabInFlight = false

	// Tick while report tab is cached -> does not issue tab fetch
	_, cmd2 := m.Update(tickMsg(time.Now()))
	batch2 := extractBatch(cmd2)
	if hasTabMsg(batch2) {
		t.Error("file-backed cached tab must NOT issue fetch on tick")
	}
}

func TestStatusMsgBindingVanishesPopsToListWithNote(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	// width 80 (stack layout): this test predates footerView's width-aware
	// left/right layout and was built at width 0, which the new footerView
	// treats as "no room" and drops the right side entirely.
	m.width = 80

	m.screen = screenDetail
	m.pane.detail.name = "webshop"
	m.pane.detail.live = true

	// Report without "webshop"
	emptyRep := relevo.Report{
		Bindings: []relevo.BindingStatus{
			{Name: "other-binding"},
		},
	}

	res, _ := m.Update(statusMsg{report: emptyRep})
	m = res.(Model)

	if m.screen != screenList {
		t.Fatalf("expected screenList when binding vanishes, got %v", m.screen)
	}
	if !strings.Contains(m.notice, "webshop is gone") {
		t.Fatalf("expected 'webshop is gone' notice, got %q", m.notice)
	}
	if !strings.Contains(stripANSI(m.footerView()), "webshop is gone") {
		t.Fatalf("expected footer to contain 'webshop is gone', got %q", stripANSI(m.footerView()))
	}

	// Second good statusMsg must NOT clear the notice
	res, _ = m.Update(statusMsg{report: emptyRep})
	m = res.(Model)
	if !strings.Contains(m.notice, "webshop is gone") {
		t.Fatalf("notice must survive subsequent statusMsg, got %q", m.notice)
	}
	if !strings.Contains(stripANSI(m.footerView()), "webshop is gone") {
		t.Fatalf("footer must still show notice after subsequent statusMsg, got %q", stripANSI(m.footerView()))
	}

	// Keypress clears the notice
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = res.(Model)
	if m.notice != "" {
		t.Fatalf("notice must be cleared on keypress, got %q", m.notice)
	}
}
