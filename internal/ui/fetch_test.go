package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

func newTestBinding(name string) store.Binding {
	return store.Binding{
		Name:         name,
		CWD:          "/tmp/test",
		Planner:      store.Endpoint{PaneID: "w1:p1", SessionID: "planner-session", Kind: "claude"},
		Builder:      store.Endpoint{AgentName: name + "-builder", PaneID: "w1:p2", Kind: "opencode"},
		BuilderAlias: "agy",
		Round:        2,
		State:        store.StateActive,
	}
}

func TestFetchStatusReturnsExactlyOneMessage(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{
		Store: st,
		Herdr: fh,
	}

	cmd := fetchStatus(context.Background(), rt)
	if cmd == nil {
		t.Fatal("fetchStatus returned nil command")
	}
	msg := cmd()
	sMsg, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", msg)
	}
	if sMsg.err != nil {
		t.Fatalf("unexpected error: %v", sMsg.err)
	}
}

func TestFetchReportScrapedPayloadDoesNotTouchPath(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entry := store.LogEntry{
		TS:        time.Now(),
		Round:     2,
		Direction: store.DirToPlanner,
		Kind:      store.KindReport,
		Path:      "/nonexistent/directory/that/does/not/exist/report.md",
		Payload:   "scraped report payload content",
		Note:      "scraped",
	}
	if err := st.AppendLog(name, entry); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	cmd := fetchReport(context.Background(), rt, name)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body != "scraped report payload content" {
		t.Fatalf("expected payload, got %q", tMsg.content.body)
	}
	if tMsg.round != 2 {
		t.Fatalf("expected round 2, got %d", tMsg.round)
	}
	if tMsg.t != tabReport {
		t.Fatalf("expected tabReport, got %v", tMsg.t)
	}
}

func TestFetchReportEmptyLogReturnsRoundOneInFlight(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "emptybinding"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cmd := fetchReport(context.Background(), rt, name)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	wantEmpty := "round 1 in flight; no report yet"
	if tMsg.content.empty != wantEmpty {
		t.Fatalf("expected empty %q, got %q", wantEmpty, tMsg.content.empty)
	}
}

func TestFetchTerminalBuilderAbsent(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	b.BuilderAlias = "agy"
	b.Builder.PaneID = "w2:p4"
	b.Builder.AgentName = "webshop-builder"
	b.Builder.SessionID = "builder-sess"
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cmd := fetchTerminal(context.Background(), rt, name, 24)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	wantEmpty := "builder gone (`agy`); pane w2:p4 no longer exists"
	if tMsg.content.empty != wantEmpty {
		t.Fatalf("expected empty %q, got %q", wantEmpty, tMsg.content.empty)
	}
	if fh.readCalls != 0 {
		t.Fatalf("expected 0 ReadAgent calls, got %d", fh.readCalls)
	}
}

func TestFetchTerminalBuilderPresent(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	fh.agents = []herdr.Agent{
		{PaneID: "w2:p4"},
	}
	fh.readOut = "terminal output line 1\nline 2"
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	b.Builder.AgentName = ""
	b.Builder.PaneID = "w2:p4"
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cmd := fetchTerminal(context.Background(), rt, name, 24)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body != "terminal output line 1\nline 2" {
		t.Fatalf("unexpected body %q", tMsg.content.body)
	}
	if fh.readCalls != 1 {
		t.Fatalf("expected 1 ReadAgent call, got %d", fh.readCalls)
	}
}

func TestFetchDiffRoundZero(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	cmd := fetchDiff(context.Background(), rt, "webshop", 0)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	wantEmpty := "no completed round yet"
	if tMsg.content.empty != wantEmpty {
		t.Fatalf("expected empty %q, got %q", wantEmpty, tMsg.content.empty)
	}
}

func TestFetchDiffRoundNoStoredPatch(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cmd := fetchDiff(context.Background(), rt, name, 1)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	wantEmpty := "no diff recorded for round 1 — no baseline captured"
	if tMsg.content.empty != wantEmpty {
		t.Fatalf("expected empty %q, got %q", wantEmpty, tMsg.content.empty)
	}
}

func TestFetchLogTwoEntriesByteIdentical(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	e1 := store.LogEntry{
		TS:        now,
		Round:     1,
		Direction: store.DirToBuilder,
		Kind:      store.KindPlan,
		Path:      "/path/plan1.md",
		Note:      "started",
	}
	e2 := store.LogEntry{
		TS:        now.Add(2 * time.Minute),
		Round:     1,
		Direction: store.DirToPlanner,
		Kind:      store.KindReport,
		Path:      "/path/report1.md",
		Note:      "finished",
	}

	if err := st.AppendLog(name, e1); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if err := st.AppendLog(name, e2); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	var want strings.Builder
	for _, e := range []store.LogEntry{e1, e2} {
		fmt.Fprintf(&want, "%s  round %-3d %-10s %-9s %s %s\n",
			e.TS.Local().Format("2006-01-02 15:04:05"),
			e.Round, e.Direction, e.Kind, e.Path, e.Note)
	}

	cmd := fetchLog(context.Background(), rt, name)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body != want.String() {
		t.Fatalf("log mismatch:\ngot:\n%q\nwant:\n%q", tMsg.content.body, want.String())
	}
}

func TestFetchForRouting(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, tab := range []tab{tabReport, tabTerminal, tabDiff, tabLog} {
		cmd := fetchFor(context.Background(), rt, tab, name, 1, 24)
		if cmd == nil {
			t.Fatalf("fetchFor returned nil for tab %v", tab)
		}
		msg := cmd()
		tMsg, ok := msg.(tabMsg)
		if !ok {
			t.Fatalf("expected tabMsg for tab %v, got %T", tab, msg)
		}
		if tMsg.t != tab {
			t.Fatalf("expected tab %v, got %v", tab, tMsg.t)
		}
	}
}
