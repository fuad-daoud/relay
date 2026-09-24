package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// histFixtureDir is the ingest package's own golden fixture -- three
// rounds (reported, halted, exited) -- reused read-only here exactly as
// internal/relevo's Show tests reuse it (Task 4's plan: seed a db by
// ingest.Ingest over a copy packed as a tarball, so the binding is
// archived and not live).
const histFixtureDir = "../ingest/testdata/binding-three-rounds"

var histFixtureBindFiles = map[string]bool{
	"bind-legacy.json":   true,
	"bind-round4.json":   true,
	"bind-cwd-gone.json": true,
}

// seedArchivedHistBinding archives the golden fixture into a record and
// ingests it as an archive source, so the binding it produces carries
// ArchivedAt and is never in a live report -- fetchShow and relevo.Show
// fall through to the db for it by construction. It returns a runtime
// carrying that db and the one HistoryBinding row relevo.Bindings gives
// back for it.
func seedArchivedHistBinding(t *testing.T) (relevo.Runtime, relevo.HistoryBinding) {
	t.Helper()
	root := t.TempDir()
	s := store.New(root)
	if err := os.MkdirAll(s.Dir("fixture"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries, err := os.ReadDir(histFixtureDir)
	if err != nil {
		t.Fatalf("ReadDir fixture: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || histFixtureBindFiles[e.Name()] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(histFixtureDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(s.Dir("fixture"), e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	if _, err := s.Archive("fixture"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	if _, err := ingest.Ingest(context.Background(), ingest.ArchivedSource(s, archived[0].RecordID), d, ingest.Deps{}); err != nil {
		t.Fatalf("archive Ingest: %v", err)
	}

	rt := relevo.Runtime{Store: store.New(t.TempDir()), DB: d}

	rows, err := relevo.Bindings(context.Background(), rt, "")
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(Bindings) = %d, want 1", len(rows))
	}
	return rt, rows[0]
}

func histModel(t *testing.T, rt relevo.Runtime, h relevo.HistoryBinding) Model {
	t.Helper()
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.width, m.height, m.ready = 140, 40, true
	m.scope = scopeAll
	m.statusLoaded = true
	m, cmd := m.pointDetailAtHist(h)
	if cmd != nil {
		res, _ := m.Update(cmd())
		m = res.(Model)
	}
	return m
}

// TestPointAtArchivedRowLoadsPlanFromDB pins the hist branch of "enter /
// re-point on a row" (#183, §5.8): pointing at a hist row sets live false,
// round the newest (every round closed), and the plan tab's fetch reads
// the database, not a file.
func TestPointAtArchivedRowLoadsPlanFromDB(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)

	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.width, m.height, m.ready = 140, 40, true
	m.scope = scopeAll

	m, cmd := m.pointDetailAtHist(h)
	if m.detail.live {
		t.Error("live = true, want false for a hist row")
	}
	if m.detail.name != "fixture" {
		t.Errorf("name = %q, want fixture", m.detail.name)
	}
	if m.detail.rounds != 3 || m.detail.round != 3 {
		t.Errorf("round=%d rounds=%d, want round=3 rounds=3 (every round closed, newest default)", m.detail.round, m.detail.rounds)
	}
	if m.detail.archivedAt.IsZero() {
		t.Error("archivedAt must be set for an archived hist row")
	}
	if cmd == nil {
		t.Fatal("expected a fetch command for the active (plan) tab")
	}
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.t != tabPlan {
		t.Errorf("t = %v, want tabPlan (the default active tab)", tMsg.t)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body == "" {
		t.Error("expected the round 3 plan's body, got empty")
	}
}

// TestArchivedTerminalTabShowsTranscriptRows pins fetchShow's terminal
// routing (terminal -> ShowTranscript) and the "never tail-following" rule
// for a hist row's terminal (#183, §5.8): round 2 of the fixture has a
// builder.log (transcript rows via logOnlyTranscriptRecords, per
// internal/ingest), so stepping to it and opening the terminal tab renders
// transcript content, not a live capture. (Round 3, the default newest
// round, has neither a stream nor a log and would read Missing instead.)
func TestArchivedTerminalTabShowsTranscriptRows(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)

	m := histModel(t, rt, h)
	if m.detail.follow {
		t.Error("follow = true, want false for a hist row (never tail-following)")
	}

	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	m = res.(Model)
	m.tabInFlight = false
	if m.detail.round != 2 {
		t.Fatalf("round = %d, want 2", m.detail.round)
	}

	res, cmd := m.switchTab(tabTerminal)
	m = res.(Model)
	if cmd == nil {
		t.Fatal("expected a fetch command for the terminal tab")
	}
	msg := cmd().(tabMsg)
	res, _ = m.Update(msg)
	m = res.(Model)

	if m.detail.follow {
		t.Error("follow must stay false after loading the terminal tab")
	}
	if !m.detail.cache[tabTerminal].transcript {
		t.Error("a hist row's terminal tab must render as transcript content")
	}
	if m.detail.cache[tabTerminal].body == "" {
		t.Error("expected rendered transcript rows, got empty body")
	}
}

// TestArchivedMissingDiffIsEmptyProse pins §6: a missing artifact (round 3
// of the fixture has no diff.patch) renders as tabContent.empty prose, not
// as an error.
func TestArchivedMissingDiffIsEmptyProse(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)
	m := histModel(t, rt, h)

	res, cmd := m.switchTab(tabDiff)
	m = res.(Model)
	if cmd == nil {
		t.Fatal("expected a fetch command for the diff tab")
	}
	msg := cmd().(tabMsg)
	if msg.content.err != nil {
		t.Fatalf("unexpected error: %v", msg.content.err)
	}
	want := "no diff for round 3"
	if msg.content.empty != want {
		t.Errorf("empty = %q, want %q", msg.content.empty, want)
	}
}

// TestDetailHeaderArchived pins detailHeader's exact rendering for an
// archived binding.
func TestDetailHeaderArchived(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)
	m := histModel(t, rt, h)

	got := m.detailHeader()
	if !strings.HasPrefix(got, "fixture · round 3 of 3 · archived 2026-") {
		t.Errorf("detailHeader() = %q, want a %q prefix", got, "fixture · round 3 of 3 · archived 2026-")
	}
}

// TestArchivedStepRoundRefetches pins round stepping for a hist row
// (#183): "[" moves detail.round within a closed binding's rounds exactly
// as it does for a live one, invalidating every tab's cache.
func TestArchivedStepRoundRefetches(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)
	m := histModel(t, rt, h)

	for tb := tab(0); tb < tabCount; tb++ {
		m.detail.cache[tb] = tabContent{loaded: true, body: "stale"}
	}
	m.tabInFlight = false

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	m = res.(Model)
	if m.detail.round != 2 {
		t.Fatalf("round = %d, want 2", m.detail.round)
	}
	for tb := tab(0); tb < tabCount; tb++ {
		if m.detail.cache[tb].loaded {
			t.Errorf("tab %v cache still loaded after stepping", tb)
		}
	}
	if cmd == nil {
		t.Fatal("expected a refetch command for the active tab")
	}
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.round != 2 {
		t.Errorf("fetched round = %d, want 2", tMsg.round)
	}
}
