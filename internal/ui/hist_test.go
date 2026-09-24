package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// histFixtureDir is the ingest package's own golden fixture -- three
// rounds (reported, halted, exited) -- reused read-only here exactly as
// internal/relevo's Show tests reuse it.
const histFixtureDir = "../ingest/testdata/binding-three-rounds"

var histFixtureBindFiles = map[string]bool{
	"bind-legacy.json":   true,
	"bind-round4.json":   true,
	"bind-cwd-gone.json": true,
}

// seedArchivedHistBinding archives the golden fixture into a record and
// ingests it as an archive source, so the binding it produces carries
// ArchivedAt and is never in a live report.
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

// TestPointAtArchivedRowLoadsPlanFromDB pins the hist branch (#183, §5.8):
// pointing at a hist row sets live false, round the newest (every round
// closed), and the plan tab's fetch reads the database, not a file.
func TestPointAtArchivedRowLoadsPlanFromDB(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)

	v, cmd := newHistRoundView(testEnv(plannerSource{rt}, relevo.Report{}, 140, 40), h, 0)
	rv := v.(roundView)

	if rv.pane.detail.live {
		t.Error("live = true, want false for a hist row")
	}
	if rv.pane.detail.name != "fixture" {
		t.Errorf("name = %q, want fixture", rv.pane.detail.name)
	}
	if rv.pane.detail.rounds != 3 || rv.pane.detail.round != 3 {
		t.Errorf("round=%d rounds=%d, want round=3 rounds=3", rv.pane.detail.round, rv.pane.detail.rounds)
	}
	if rv.pane.detail.archivedAt.IsZero() {
		t.Error("archivedAt must be set for an archived hist row")
	}
	if cmd == nil {
		t.Fatal("expected a fetch command for the active (plan) tab")
	}
	tMsg, ok := cmd().(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", cmd())
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
// routing and the "never tail-following" rule for a hist row's terminal.
func TestArchivedTerminalTabShowsTranscriptRows(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)

	rv := newTestHistRound(t, rt, h, 0)
	if rv.pane.detail.follow {
		t.Error("follow = true, want false for a hist row (never tail-following)")
	}

	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	rv.pane.tabInFlight = false
	if rv.pane.detail.round != 2 {
		t.Fatalf("round = %d, want 2", rv.pane.detail.round)
	}

	next, cmd := rv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}}, testEnv(plannerSource{rt}, relevo.Report{}, 140, 40))
	rv = next.(roundView)
	if cmd == nil {
		t.Fatal("expected a fetch command for the terminal tab")
	}
	rv = roundMsg(rv, cmd().(tabMsg))

	if rv.pane.detail.follow {
		t.Error("follow must stay false after loading the terminal tab")
	}
	if !rv.pane.detail.cache[tabTerminal].transcript {
		t.Error("a hist row's terminal tab must render as transcript content")
	}
	if rv.pane.detail.cache[tabTerminal].body == "" {
		t.Error("expected rendered transcript rows, got empty body")
	}
}

// TestArchivedMissingDiffIsEmptyProse pins §6: a missing artifact renders
// as tabContent.empty prose, not as an error.
func TestArchivedMissingDiffIsEmptyProse(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)
	rv := newTestHistRound(t, rt, h, 0)
	rv.pane.tabInFlight = false

	next, cmd := rv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}}, testEnv(plannerSource{rt}, relevo.Report{}, 140, 40))
	_ = next
	if cmd == nil {
		t.Fatal("expected a fetch command for the diff tab")
	}
	msg := cmd().(tabMsg)
	if msg.content.err != nil {
		t.Fatalf("unexpected error: %v", msg.content.err)
	}
	if want := "no diff for round 3"; msg.content.empty != want {
		t.Errorf("empty = %q, want %q", msg.content.empty, want)
	}
}

// TestDetailHeaderArchived pins detailHeader's exact rendering for an
// archived binding.
func TestDetailHeaderArchived(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)
	rv := newTestHistRound(t, rt, h, 0)

	got := rv.pane.detailHeader()
	if !strings.HasPrefix(got, "fixture · round 3 of 3 · archived 2026-") {
		t.Errorf("detailHeader() = %q, want a %q prefix", got, "fixture · round 3 of 3 · archived 2026-")
	}
}

// TestArchivedStepRoundRefetches pins round stepping for a hist row (#183).
func TestArchivedStepRoundRefetches(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)
	rv := newTestHistRound(t, rt, h, 0)

	for tb := tab(0); tb < tabCount; tb++ {
		rv.pane.detail.cache[tb] = tabContent{loaded: true, body: "stale"}
	}
	rv.pane.tabInFlight = false

	next, cmd := rv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}}, testEnv(plannerSource{rt}, relevo.Report{}, 140, 40))
	rv = next.(roundView)
	if rv.pane.detail.round != 2 {
		t.Fatalf("round = %d, want 2", rv.pane.detail.round)
	}
	for tb := tab(0); tb < tabCount; tb++ {
		if rv.pane.detail.cache[tb].loaded {
			t.Errorf("tab %v cache still loaded after stepping", tb)
		}
	}
	if cmd == nil {
		t.Fatal("expected a refetch command for the active tab")
	}
	tMsg, ok := cmd().(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", cmd())
	}
	if tMsg.round != 2 {
		t.Errorf("fetched round = %d, want 2", tMsg.round)
	}
}
