package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

func newTestBinding(name string) store.Binding {
	return store.Binding{
		Name:             name,
		CWD:              "/tmp/test",
		Planner:          store.Endpoint{PaneID: "w1:p1", SessionID: "planner-session", Kind: "claude"},
		Builder:          store.Endpoint{AgentName: name + "-builder", PaneID: "w1:p2", Kind: "opencode"},
		BuilderCandidate: "agy",
		Round:            2,
		State:            store.StateActive,
	}
}

func TestTabOrderStartsWithPlan(t *testing.T) {
	if tabPlan != 0 {
		t.Fatalf("tabPlan = %d, want 0 (first in the tab order)", tabPlan)
	}
	wantOrder := [tabCount]string{"plan", "report", "terminal", "diff", "log"}
	if tabTitles != wantOrder {
		t.Fatalf("tabTitles = %v, want %v", tabTitles, wantOrder)
	}
}

func TestFetchPlanLive(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	planPath := st.PlanPath(name, 1)
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte("# Round 1 plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := fetchPlan(context.Background(), plannerSource{rt}, name, 1)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body != "# Round 1 plan\n" {
		t.Fatalf("body = %q, want the plan file's content", tMsg.content.body)
	}
	if tMsg.t != tabPlan {
		t.Errorf("t = %v, want tabPlan", tMsg.t)
	}
	if tMsg.round != 1 {
		t.Errorf("round = %d, want 1", tMsg.round)
	}
}

func TestFetchStatusReturnsExactlyOneMessage(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{
		Store: st,
		Herdr: fh,
	}

	cmd := fetchStatus(context.Background(), plannerSource{rt}, scopeLive, "")
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

	cmd := fetchReport(context.Background(), plannerSource{rt}, name, 2)
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

	cmd := fetchReport(context.Background(), plannerSource{rt}, name, 1)
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

// TestFetchReportTakesRound pins #183: fetchReport now reads round's own
// report entry, not the newest one logged -- stepping back to an earlier
// round must show that round's report, not a later round's.
func TestFetchReportTakesRound(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries := []store.LogEntry{
		{TS: time.Now(), Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Payload: "round 1 report"},
		{TS: time.Now(), Round: 2, Direction: store.DirToPlanner, Kind: store.KindReport, Payload: "round 2 report"},
	}
	for _, e := range entries {
		if err := st.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	msg := fetchReport(context.Background(), plannerSource{rt}, name, 1)().(tabMsg)
	if msg.content.body != "round 1 report" {
		t.Errorf("round 1: body = %q, want %q", msg.content.body, "round 1 report")
	}
	if msg.round != 1 {
		t.Errorf("round 1: tabMsg.round = %d, want 1", msg.round)
	}

	msg = fetchReport(context.Background(), plannerSource{rt}, name, 2)().(tabMsg)
	if msg.content.body != "round 2 report" {
		t.Errorf("round 2: body = %q, want %q (not round 1's, even though it is the newest logged)", msg.content.body, "round 2 report")
	}
}

func TestFetchTerminalBuilderAbsent(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	b.BuilderCandidate = "agy"
	b.Builder.PaneID = "w2:p4"
	b.Builder.AgentName = "webshop-builder"
	b.Builder.SessionID = "builder-sess"
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cmd := fetchTerminal(context.Background(), plannerSource{rt}, name, 2, 24)
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
		{PaneID: "w2:p4", Kind: "opencode"},
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

	cmd := fetchTerminal(context.Background(), plannerSource{rt}, name, 2, 24)
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

func TestFetchTerminalPaneReadsRoundLog(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	logPath := st.BuilderLogPath(name, 2)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newTestBinding(name)
	b.Builder.StreamRound = 2
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	msg := fetchTerminal(context.Background(), plannerSource{rt}, name, 2, 24)()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil || tMsg.content.empty != "" {
		t.Fatalf("content = %+v, want a body", tMsg.content)
	}
	if tMsg.content.body != "a\nb\nc" {
		t.Errorf("body = %q, want the round log", tMsg.content.body)
	}
	if !tMsg.content.transcript {
		t.Error("transcript = false, want true for a rendered round log")
	}
	if tMsg.content.logName != "002-builder.log" {
		t.Errorf("logName = %q, want 002-builder.log", tMsg.content.logName)
	}
	if fh.readCalls != 0 {
		t.Errorf("the round log satisfies the tab; ReadAgent must not be called: readCalls = %d", fh.readCalls)
	}
}

func TestFetchTerminalPaneFallsBackToCapture(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	fh.agents = []herdr.Agent{
		{PaneID: "w1:p2", Kind: "opencode"},
	}
	fh.readOut = "captured screen"
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	// StreamRound is set (as Send would arm it) but the round log was never
	// written -- e.g. the session record was never located -- so the tab
	// falls back to today's capture path.
	b := newTestBinding(name)
	b.Builder.AgentName = ""
	b.Builder.StreamRound = 2
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	msg := fetchTerminal(context.Background(), plannerSource{rt}, name, 2, 24)()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body != "captured screen" {
		t.Errorf("body = %q, want the capture", tMsg.content.body)
	}
	if tMsg.content.transcript {
		t.Error("transcript = true, want false for a capture")
	}
	if fh.readCalls != 1 {
		t.Errorf("expected 1 ReadAgent call (the fallback), got %d", fh.readCalls)
	}
}

// TestFetchTerminalNonCurrentPaneRoundIsEmptyProse pins #183: a plain pane
// builder's terminal only ever shows the binding's live screen, which
// only ever belongs to its current round; a past round it never captured
// a log for reads as prose, not as an error, and must never reach herdr.
func TestFetchTerminalNonCurrentPaneRoundIsEmptyProse(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name) // Round: 2, plain pane builder
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	msg := fetchTerminal(context.Background(), plannerSource{rt}, name, 1, 24)().(tabMsg)
	if msg.content.err != nil {
		t.Fatalf("unexpected error: %v", msg.content.err)
	}
	want := "terminal is live; round 1 left no log"
	if msg.content.empty != want {
		t.Errorf("empty = %q, want %q", msg.content.empty, want)
	}
	if fh.readCalls != 0 {
		t.Errorf("a non-current round must never touch herdr: readCalls = %d", fh.readCalls)
	}
}

func TestFetchDiffRoundZero(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	cmd := fetchDiff(context.Background(), plannerSource{rt}, "webshop", 0)
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

	cmd := fetchDiff(context.Background(), plannerSource{rt}, name, 1)
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

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.Local)
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

	want := "2026-09-08 12:00:00  round 1   to_builder plan      /path/plan1.md started\n" +
		"2026-09-08 12:02:00  round 1   to_planner report    /path/report1.md finished\n"

	cmd := fetchLog(context.Background(), plannerSource{rt}, name, 1)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body != want {
		t.Fatalf("log mismatch:\ngot:\n%q\nwant:\n%q", tMsg.content.body, want)
	}
}

// TestFetchLogFiltersRound pins #183: fetchLog now scopes to round rather
// than dumping the whole binding log.
func TestFetchLogFiltersRound(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.Local)
	entries := []store.LogEntry{
		{TS: now, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Path: "/p1.md"},
		{TS: now.Add(time.Minute), Round: 2, Direction: store.DirToBuilder, Kind: store.KindPlan, Path: "/p2.md"},
	}
	for _, e := range entries {
		if err := st.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	msg := fetchLog(context.Background(), plannerSource{rt}, name, 1)().(tabMsg)
	if msg.content.err != nil {
		t.Fatalf("unexpected error: %v", msg.content.err)
	}
	if strings.Count(strings.TrimRight(msg.content.body, "\n"), "\n")+1 != 1 {
		t.Fatalf("body = %q, want exactly one line (round 1 only)", msg.content.body)
	}
	if !strings.Contains(msg.content.body, "round 1") || strings.Contains(msg.content.body, "round 2") {
		t.Errorf("body = %q, want only round 1's entry", msg.content.body)
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

	for _, tab := range []tab{tabPlan, tabReport, tabTerminal, tabDiff, tabLog} {
		cmd := fetchFor(context.Background(), plannerSource{rt}, tab, name, 1, 24, true)
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

// A spawned builder records an AgentName that herdr can forget across a server
// restart. fetchTerminal has already located the live agent, so it must address
// that agent, not replay a name that may no longer resolve.
func TestFetchTerminalAddressesLocatedAgent(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(store.Binding{
		Name: "relay-ui", CWD: t.TempDir(),
		Planner:          store.Endpoint{PaneID: "wM:p1", Kind: "claude"},
		Builder:          store.Endpoint{AgentName: "relay-ui-builder", PaneID: "wM:p7", Kind: "agy"},
		BuilderCandidate: "abuilder", Round: 1, State: store.StateActive,
	}); err != nil {
		t.Fatal(err)
	}

	fh := newFakeHerdr(t)
	fh.agents = []herdr.Agent{{Kind: "agy", PaneID: "wM:p7", Status: "idle"}}
	fh.readOut = "builder screen"
	rt := relay.Runtime{Store: st, Herdr: fh, Now: time.Now}

	msg := fetchTerminal(context.Background(), plannerSource{rt}, "relay-ui", 1, 40)()
	tm, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tm.content.err != nil {
		t.Fatalf("unexpected err: %v", tm.content.err)
	}
	if len(fh.readTargets) != 1 {
		t.Fatalf("expected 1 ReadAgent call, got %d", len(fh.readTargets))
	}
	if got := fh.readTargets[0]; got != "wM:p7" {
		t.Fatalf("ReadAgent addressed %q; want the located agent's pane %q "+
			"(a recorded agent name herdr has forgotten is unusable)", got, "wM:p7")
	}
}

func TestFetchTerminalHeadlessReadsTheLogNotHerdr(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	logPath := filepath.Join(t.TempDir(), "002-builder.log")
	if err := os.WriteFile(logPath, []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newTestBinding("webshop")
	b.Builder = store.Endpoint{AgentName: "webshop-builder", Kind: "agy", Mode: store.ModeHeadless, PID: 4242, StartedAt: 1_700_000_000, LogPath: logPath}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The lines argument is a pane-builder concern (#180's Task 2): the
	// headless branch now always returns the whole log, capped only by
	// headlessLogLines, so a request for 3 lines still gets all of it.
	msg := fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 3)()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil || tMsg.content.empty != "" {
		t.Fatalf("content = %+v, want a body", tMsg.content)
	}
	if tMsg.content.body != "a\nb\nc\nd\ne" {
		t.Errorf("body = %q, want the whole log regardless of the lines argument", tMsg.content.body)
	}
	if fh.readCalls != 0 {
		t.Errorf("a headless builder has no pane to read: readCalls = %d", fh.readCalls)
	}
}

func TestFetchTerminalHeadlessReturnsWholeLog(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}
	name := "webshop"

	logPath := filepath.Join(t.TempDir(), "002-builder.log")
	var lines40 []string
	for i := 1; i <= 40; i++ {
		lines40 = append(lines40, fmt.Sprintf("line %d", i))
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines40, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newTestBinding(name)
	b.Builder = store.Endpoint{AgentName: name + "-builder", Kind: "agy", Mode: store.ModeHeadless, LogPath: logPath}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	msg := fetchTerminal(context.Background(), plannerSource{rt}, name, 2, 5)().(tabMsg)
	if got := strings.Count(msg.content.body, "\n") + 1; got != 40 {
		t.Errorf("headless terminal body has %d lines, want all 40 regardless of the lines argument", got)
	}

	// A log longer than the cap keeps only its tail.
	var linesOver []string
	for i := 1; i <= headlessLogLines+10; i++ {
		linesOver = append(linesOver, fmt.Sprintf("line %d", i))
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(linesOver, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg = fetchTerminal(context.Background(), plannerSource{rt}, name, 2, 5)().(tabMsg)
	lines := strings.Split(msg.content.body, "\n")
	if len(lines) != headlessLogLines || !strings.HasSuffix(lines[len(lines)-1], fmt.Sprint(headlessLogLines+10)) {
		t.Errorf("capped body: %d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestFetchTerminalHeadlessIdleAndMissingLog(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	b := newTestBinding("webshop")
	b.Builder = store.Endpoint{AgentName: "webshop-builder", Kind: "agy", Mode: store.ModeHeadless}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	tMsg := fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if tMsg.content.empty != "headless builder; no round has run yet, so there is no log" {
		t.Errorf("idle: empty = %q", tMsg.content.empty)
	}

	b.Builder.PID = 4242
	b.Builder.LogPath = filepath.Join(t.TempDir(), "absent.log")
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	tMsg = fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if !strings.HasPrefix(tMsg.content.empty, "log not written yet: ") || !strings.Contains(tMsg.content.empty, b.Builder.LogPath) {
		t.Errorf("missing log: empty = %q", tMsg.content.empty)
	}
	if fh.readCalls != 0 {
		t.Errorf("readCalls = %d, want 0", fh.readCalls)
	}
}

func TestFetchTerminalHeadlessBetweenRoundsShowsTheLastRoundsLog(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	b := newTestBinding("webshop")
	b.Round = 3 // round 2 closed; nothing sent yet
	// Between rounds: no process, LogPath cleared, but the cursor still
	// names round 2 (transcript spec §3.4).
	b.Builder = store.Endpoint{AgentName: "webshop-builder", Kind: "agy", Mode: store.ModeHeadless, StreamRound: 2, StreamOffset: 100}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	logPath := st.BuilderLogPath("webshop", 2)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("Bash go test ./...\n  -> ok: ok\nrelay-exit:0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tMsg := fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if tMsg.content.empty != "" || tMsg.content.err != nil {
		t.Fatalf("between rounds the last log must show: %+v", tMsg.content)
	}
	if tMsg.content.body != "Bash go test ./...\n  -> ok: ok\nrelay-exit:0" {
		t.Errorf("body = %q", tMsg.content.body)
	}
	if fh.readCalls != 0 {
		t.Errorf("readCalls = %d, want 0", fh.readCalls)
	}
}
