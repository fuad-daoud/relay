package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestStatusMsgUnchangedTSNoFetch(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Second})

	name := "webshop"
	ts := time.Now().Truncate(time.Second)

	m.screen = screenDetail
	m.detail.name = name
	m.detail.active = tabReport
	m.detail.lastLogTS = ts
	m.detail.cache[tabReport] = tabContent{loaded: true, body: "initial report"}

	rep := relay.Report{
		Bindings: []relay.BindingStatus{
			{
				Name:  name,
				Round: 2,
				Last: &relay.LastEvent{
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
	if !m.detail.cache[tabReport].loaded {
		t.Fatal("expected report cache to remain loaded when TS unchanged")
	}
}

func TestStatusMsgNewerTSClearsFileCachesPreservesTerminal(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := newModel(context.Background(), rt, Options{Interval: time.Second})

	ts := time.Now().Truncate(time.Second)

	m.screen = screenDetail
	m.detail.name = name
	m.detail.active = tabReport
	m.detail.lastLogTS = ts
	m.detail.vp = viewport.New(80, 20)

	m.detail.cache[tabReport] = tabContent{loaded: true, body: "cached report"}
	m.detail.cache[tabDiff] = tabContent{loaded: true, body: "cached diff"}
	m.detail.cache[tabLog] = tabContent{loaded: true, body: "cached log"}
	m.detail.cache[tabTerminal] = tabContent{loaded: true, body: "live terminal"}

	newTS := ts.Add(10 * time.Second)
	rep := relay.Report{
		Bindings: []relay.BindingStatus{
			{
				Name:  name,
				Round: 4,
				Last: &relay.LastEvent{
					TS:    newTS,
					Round: 4,
				},
			},
		},
	}

	res, cmd := m.Update(statusMsg{report: rep})
	m = res.(Model)

	if m.detail.cache[tabReport].loaded {
		t.Error("tabReport cache should be cleared on new TS")
	}
	if m.detail.cache[tabDiff].loaded {
		t.Error("tabDiff cache should be cleared on new TS")
	}
	if m.detail.cache[tabLog].loaded {
		t.Error("tabLog cache should be cleared on new TS")
	}
	if !m.detail.cache[tabTerminal].loaded || m.detail.cache[tabTerminal].body != "live terminal" {
		t.Error("tabTerminal cache must be untouched by log invalidation")
	}

	if m.detail.round != 3 {
		t.Errorf("expected detail.round to update to 3 (row.Round-1), got %d", m.detail.round)
	}
	if !m.detail.lastLogTS.Equal(newTS) {
		t.Errorf("expected lastLogTS to update to %v, got %v", newTS, m.detail.lastLogTS)
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
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Second})

	name := "webshop"
	ts := time.Now().Truncate(time.Second)

	m.screen = screenDetail
	m.detail.name = name
	m.detail.active = tabReport
	m.detail.lastLogTS = ts
	m.detail.vp = viewport.New(80, 20)
	m.detail.vp.SetContent(strings.Repeat("line\n", 100))
	m.detail.vp.YOffset = 33

	rep := relay.Report{
		Bindings: []relay.BindingStatus{
			{
				Name:  name,
				Round: 2,
				Last: &relay.LastEvent{
					TS:    ts,
					Round: 2,
				},
			},
		},
	}

	res, _ := m.Update(statusMsg{report: rep})
	m = res.(Model)

	if m.detail.vp.YOffset != 33 {
		t.Errorf("expected scroll offset 33 preserved, got %d", m.detail.vp.YOffset)
	}
}

func TestTerminalTabPollsOnEveryTickWhenVisible(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := newModel(context.Background(), rt, Options{Interval: time.Second})
	m.screen = screenDetail
	m.detail.name = name
	m.detail.active = tabTerminal
	m.detail.vp = viewport.New(80, 20)
	m.statusInFlight = false
	m.tabInFlight = false

	// Tick while terminal is visible -> issues tab fetch
	res, cmd := m.Update(tickMsg(time.Now()))
	m = res.(Model)

	batch := extractBatch(cmd)
	if !hasTabMsg(batch) {
		t.Error("terminal tab must issue fetch on every tick while visible")
	}

	// Switch to report tab with cached content
	m.detail.active = tabReport
	m.detail.cache[tabReport] = tabContent{loaded: true, body: "report"}
	m.statusInFlight = false
	m.tabInFlight = false

	// Tick while report tab is cached -> does not issue tab fetch
	_, cmd2 := m.Update(tickMsg(time.Now()))
	batch2 := extractBatch(cmd2)
	if hasTabMsg(batch2) {
		t.Error("file-backed cached tab must NOT issue fetch on tick")
	}
}

func TestStatusMsgBindingVanishesPopsToListWithNote(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	m := newModel(context.Background(), rt, Options{Interval: time.Second})

	m.screen = screenDetail
	m.detail.name = "webshop"

	// Report without "webshop"
	emptyRep := relay.Report{
		Bindings: []relay.BindingStatus{
			{Name: "other-binding"},
		},
	}

	res, _ := m.Update(statusMsg{report: emptyRep})
	m = res.(Model)

	if m.screen != screenList {
		t.Fatalf("expected screenList when binding vanishes, got %v", m.screen)
	}
	if m.err == nil || !strings.Contains(m.err.Error(), "webshop is gone") {
		t.Fatalf("expected 'webshop is gone' error note, got %v", m.err)
	}
}
