package relevo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// showFixtureDir is the shared golden fixture ingest's own tests use,
// reused here read-only for the db-backed Show tests (Task 2's plan: seed a
// temp db via ingest.Ingest over it, copying it into a temp dir first, and
// never writing into testdata).
const showFixtureDir = "../ingest/testdata/binding-three-rounds"

// showFixtureBindFiles are the bind.json stand-ins under showFixtureDir
// that belong to other rounds' tests, never copied alongside bind.json.
var showFixtureBindFiles = map[string]bool{
	"bind-legacy.json":   true,
	"bind-round4.json":   true,
	"bind-cwd-gone.json": true,
}

// copyShowFixture copies showFixtureDir into a fresh temp dir so a live
// ingest.DirSource can read it without ever touching testdata.
func copyShowFixture(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	entries, err := os.ReadDir(showFixtureDir)
	if err != nil {
		t.Fatalf("ReadDir fixture: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || showFixtureBindFiles[e.Name()] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(showFixtureDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	return dst
}

// seedShowDB ingests the golden fixture (live, from a copy) into a fresh
// temp database and returns it.
func seedShowDB(t *testing.T) *db.DB {
	t.Helper()
	dir := copyShowFixture(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := ingest.Ingest(context.Background(), ingest.DirSource(dir), d, ingest.Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	seedRoundMirror(t, d, dir)
	return d
}

// archiveShowFixture copies the golden fixture into a fresh store's
// "fixture" directory -- writing one file per extra basename -> body -- and
// archives it, so the returned store holds exactly one archived record with
// every NNN-* file of the fixture sealed into it.
func archiveShowFixture(t *testing.T, extra map[string]string) *store.Store {
	t.Helper()
	s := store.New(t.TempDir())
	if err := os.MkdirAll(s.Dir("fixture"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries, err := os.ReadDir(showFixtureDir)
	if err != nil {
		t.Fatalf("ReadDir fixture: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || showFixtureBindFiles[e.Name()] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(showFixtureDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(s.Dir("fixture"), e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	for base, body := range extra {
		if err := os.WriteFile(filepath.Join(s.Dir("fixture"), base), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", base, err)
		}
	}
	if _, err := s.Archive("fixture"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}
	return s
}

// seedShowArchiveDB archives the golden fixture into a record and ingests it
// as an archive source, so the binding it produces carries ArchivedAt.
func seedShowArchiveDB(t *testing.T) *db.DB {
	t.Helper()
	s := archiveShowFixture(t, nil)
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
	seedRoundMirror(t, d, showFixtureDir)
	return d
}

// newShowLiveStore builds a live binding "fixture" with three rounds: round
// 1 done (plan, report, diff), round 2 halted (plan, report, builder log,
// no diff), round 3 the open round (plan only). b.Round is 3, matching
// finishRound's Round++ semantics -- round 3 is the current, not-yet-closed
// round, so "newest completed" must resolve to round 2 (the highest round
// with a report entry), not round 3.
func newShowLiveStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.New(t.TempDir())
	b := store.Binding{
		Name:  "fixture",
		CWD:   "/work/show-fixture",
		Round: 3,
		State: store.StateNeedsYou,
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(s.PlanPath("fixture", 1), "# Round 1 plan\n")
	write(s.ReportPath("fixture", 1), "# Round 1 report\n")
	write(s.DiffPath("fixture", 1), "diff --git a/x b/x\n")
	write(s.PlanPath("fixture", 2), "# Round 2 plan\n")
	write(s.ReportPath("fixture", 2), "# Round 2 report\n")
	write(s.BuilderLogPath("fixture", 2), "round 2 builder log line 1\nround 2 builder log line 2\n")
	write(s.PlanPath("fixture", 3), "# Round 3 plan\n")

	entries := []store.LogEntry{
		{TS: time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
		{TS: time.Date(2026, 9, 10, 10, 0, 1, 0, time.UTC), Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"},
		{TS: time.Date(2026, 9, 10, 10, 1, 0, 0, time.UTC), Round: 2, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
		{TS: time.Date(2026, 9, 10, 10, 1, 1, 0, time.UTC), Round: 2, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "halted"},
		{TS: time.Date(2026, 9, 10, 10, 2, 0, 0, time.UTC), Round: 3, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
	}
	for _, e := range entries {
		if err := s.AppendLog("fixture", e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	return s
}

func TestShowLiveDefaultsToNewestCompletedPlan(t *testing.T) {
	rt := Runtime{Store: newShowLiveStore(t)}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Section: ShowPlan})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !res.Live {
		t.Error("Live = false, want true")
	}
	if res.Round != 2 {
		t.Errorf("Round = %d, want 2 (highest round with a report entry)", res.Round)
	}
	if res.Rounds != 3 {
		t.Errorf("Rounds = %d, want 3 (b.Round)", res.Rounds)
	}
	if res.Missing {
		t.Error("Missing = true, want false")
	}
	if res.Text != "# Round 2 plan\n" {
		t.Errorf("Text = %q, want %q", res.Text, "# Round 2 plan\n")
	}
}

func TestShowLiveRoundReport(t *testing.T) {
	rt := Runtime{Store: newShowLiveStore(t)}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowReport})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("Round = %d, want 1", res.Round)
	}
	if res.Text != "# Round 1 report\n" {
		t.Errorf("Text = %q, want %q", res.Text, "# Round 1 report\n")
	}
}

func TestShowLiveMissingDiffIsMissingNotError(t *testing.T) {
	rt := Runtime{Store: newShowLiveStore(t)}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 2, Section: ShowDiff})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !res.Missing {
		t.Error("Missing = false, want true (round 2 has no diff file)")
	}
	if res.Text != "" {
		t.Errorf("Text = %q, want empty", res.Text)
	}
}

func TestShowLiveLogFiltersRound(t *testing.T) {
	rt := Runtime{Store: newShowLiveStore(t)}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowLog})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(res.Events) != 2 {
		t.Fatalf("len(Events) = %d, want 2", len(res.Events))
	}
	for _, e := range res.Events {
		if e.Round != 1 {
			t.Errorf("Events has round %d, want only round 1", e.Round)
		}
	}
}

func TestShowLiveTranscriptReadsBuilderLog(t *testing.T) {
	rt := Runtime{Store: newShowLiveStore(t)}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 2, Section: ShowTranscript})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	want := "round 2 builder log line 1\nround 2 builder log line 2\n"
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

// TestShowLiveRoundsIsHighestPlanned pins Task 0(b): for a live binding,
// Rounds must be the highest round number with a plan log entry, not
// b.Round -- b.Round is the *next* round once a round has closed
// (finishRound does Round++), so a binding sitting idle after round 3
// closed (Round: 4, no round-4 plan sent yet) must still report Rounds ==
// 3, not 4.
func TestShowLiveRoundsIsHighestPlanned(t *testing.T) {
	s := store.New(t.TempDir())
	b := store.Binding{
		Name:  "idle",
		CWD:   "/work/idle",
		Round: 4,
		State: store.StateDone,
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(s.PlanPath("idle", 3), []byte("# Round 3 plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	entries := []store.LogEntry{
		{TS: time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
		{TS: time.Date(2026, 9, 10, 10, 0, 1, 0, time.UTC), Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"},
		{TS: time.Date(2026, 9, 10, 10, 1, 0, 0, time.UTC), Round: 2, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
		{TS: time.Date(2026, 9, 10, 10, 1, 1, 0, time.UTC), Round: 2, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"},
		{TS: time.Date(2026, 9, 10, 10, 2, 0, 0, time.UTC), Round: 3, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
		{TS: time.Date(2026, 9, 10, 10, 2, 1, 0, time.UTC), Round: 3, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"},
	}
	for _, e := range entries {
		if err := s.AppendLog("idle", e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	rt := Runtime{Store: s}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "idle", Section: ShowPlan})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Rounds != 3 {
		t.Errorf("Rounds = %d, want 3 (highest round with a plan entry, not b.Round == 4)", res.Rounds)
	}
	if res.Round != 3 {
		t.Errorf("Round = %d, want 3", res.Round)
	}
}

func TestShowLiveNoCompletedRound(t *testing.T) {
	s := store.New(t.TempDir())
	b := store.Binding{
		Name:  "openonly",
		CWD:   "/work/openonly",
		Round: 1,
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(s.PlanPath("openonly", 1), []byte("# plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	entry := store.LogEntry{TS: time.Now(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true}
	if err := s.AppendLog("openonly", entry); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	rt := Runtime{Store: s}
	_, err := Show(context.Background(), rt, ShowOptions{Name: "openonly", Section: ShowPlan})
	if !errors.Is(err, ErrNoCompletedRound) {
		t.Errorf("err = %v, want ErrNoCompletedRound", err)
	}
}

func TestShowDBFallsBackWhenNotLive(t *testing.T) {
	d := seedShowDB(t)
	rt := Runtime{Store: store.New(t.TempDir()), DB: d}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Section: ShowPlan})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Live {
		t.Error("Live = true, want false")
	}
	if res.Round != 3 {
		t.Errorf("Round = %d, want 3 (highest round with outcome != open)", res.Round)
	}
	if res.Rounds != 3 {
		t.Errorf("Rounds = %d, want 3", res.Rounds)
	}
	want := "# Round 3 plan\n\nFixture plan text for round 3.\n"
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

func TestShowDBTranscriptFromRows(t *testing.T) {
	d := seedShowDB(t)
	rt := Runtime{Store: store.New(t.TempDir()), DB: d}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 2, Section: ShowTranscript})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	want := "round 2 builder log line 1\nround 2 builder log line 2\nround 2 builder log line 3"
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

// TestShowDBSkipsOpenRoundForNewestCompleted pins the db-path completion
// rule -- "the highest round with outcome != open" -- against a binding
// whose highest round is still open: round 2's own plan/log entries would
// make it the numerically newest round, but with no report, no exit and an
// active state it derives to OutcomeOpen (internal/ingest/outcome.go), so
// the newest *completed* round must be round 1.
func TestShowDBSkipsOpenRoundForNewestCompleted(t *testing.T) {
	dir := t.TempDir()
	bind := `{
  "name": "openhead",
  "cwd": "/work/openhead",
  "round": 2,
  "state": "active",
  "created_at": "2026-09-10T10:00:00.000Z"
}`
	log := `{"ts":"2026-09-10T10:00:00.000Z","round":1,"direction":"to_builder","kind":"plan","confirmed":true}
{"ts":"2026-09-10T10:00:01.000Z","round":1,"direction":"to_planner","kind":"report","confirmed":true,"outcome":"done"}
{"ts":"2026-09-10T10:01:00.000Z","round":2,"direction":"to_builder","kind":"plan","confirmed":true}
`
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), []byte(bind), 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatalf("write log.jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "001-plan.md"), []byte("# round 1 plan\n"), 0o644); err != nil {
		t.Fatalf("write 001-plan.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "001-report.md"), []byte("# round 1 report\n"), 0o644); err != nil {
		t.Fatalf("write 001-report.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "002-plan.md"), []byte("# round 2 plan\n"), 0o644); err != nil {
		t.Fatalf("write 002-plan.md: %v", err)
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := ingest.Ingest(context.Background(), ingest.DirSource(dir), d, ingest.Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	seedRoundMirror(t, d, dir)

	rt := Runtime{Store: store.New(t.TempDir()), DB: d}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "openhead", Section: ShowPlan})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("Round = %d, want 1 (round 2 is still open)", res.Round)
	}
	if res.Text != "# round 1 plan\n" {
		t.Errorf("Text = %q, want %q", res.Text, "# round 1 plan\n")
	}
}

func TestShowDBLogFromEvents(t *testing.T) {
	d := seedShowDB(t)
	rt := Runtime{Store: store.New(t.TempDir()), DB: d}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowLog})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(res.Events) != 4 {
		t.Fatalf("len(Events) = %d, want 4 (pick, plan, diff, report)", len(res.Events))
	}
	for _, e := range res.Events {
		if e.Round != 1 {
			t.Errorf("Events has round %d, want only round 1", e.Round)
		}
	}
	if res.Events[3].Kind != store.KindReport || res.Events[3].Outcome != "done" {
		t.Errorf("Events[3] = %+v, want the round-1 report entry (outcome done)", res.Events[3])
	}
}

func TestShowDBArchivedHeaderFacts(t *testing.T) {
	d := seedShowArchiveDB(t)
	rt := Runtime{Store: store.New(t.TempDir()), DB: d}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Section: ShowPlan})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !res.Archived {
		t.Error("Archived = false, want true")
	}
	if res.ArchivedAt.IsZero() {
		t.Error("ArchivedAt is zero, want the tarball's stamp")
	}
}

func TestShowRoundOutOfRange(t *testing.T) {
	d := seedShowDB(t)
	rt := Runtime{Store: store.New(t.TempDir()), DB: d}

	_, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 99, Section: ShowPlan})
	if err == nil {
		t.Fatal("Show: want an error for a round out of range")
	}
	want := "round 99: binding has 3 rounds"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestShowUnknownBinding(t *testing.T) {
	rt := Runtime{Store: store.New(t.TempDir())}

	_, err := Show(context.Background(), rt, ShowOptions{Name: "nope", Section: ShowPlan})
	if err == nil {
		t.Fatal("Show: want an error for an unknown binding")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want it to wrap store.ErrNotFound", err)
	}
}

// TestShowArchivedReadsSealedRoundFiles pins Show's archived step: an archived
// binding is answered from its sealed round files and its record's log, with
// no mirror at all. Mutation: drop Show's archived step and Show returns
// not-found (DB is nil).
func TestShowArchivedReadsSealedRoundFiles(t *testing.T) {
	s := archiveShowFixture(t, map[string]string{"001-gate.log": "gate output\n"})
	rt := Runtime{Store: s}

	// Every expected text is the fixture file's own bytes.
	fixture := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(showFixtureDir, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return string(data)
	}

	cases := []struct {
		name    string
		round   int
		section ShowSection
		want    string
		missing bool
	}{
		{name: "plan round 1", round: 1, section: ShowPlan, want: fixture("001-plan.md")},
		{name: "report round 2", round: 2, section: ShowReport, want: fixture("002-report.md")},
		{name: "diff round 1", round: 1, section: ShowDiff, want: fixture("001-diff.patch")},
		{name: "drift round 2", round: 2, section: ShowDrift, want: fixture("002-drift.patch")},
		{name: "transcript round 2", round: 2, section: ShowTranscript, want: fixture("002-builder.log")},
		{name: "gate round 1", round: 1, section: ShowGate, want: "gate output\n"},
		{name: "diff round 2 is missing", round: 2, section: ShowDiff, want: "", missing: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: tc.round, Section: tc.section})
			if err != nil {
				t.Fatalf("Show: %v", err)
			}
			if res.Text != tc.want {
				t.Errorf("Text = %q, want %q", res.Text, tc.want)
			}
			if res.Missing != tc.missing {
				t.Errorf("Missing = %v, want %v", res.Missing, tc.missing)
			}
			if res.Live {
				t.Error("Live = true, want false")
			}
			if !res.Archived {
				t.Error("Archived = false, want true")
			}
			if res.ArchivedAt.IsZero() {
				t.Error("ArchivedAt is zero, want the record's stamp")
			}
		})
	}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowLog})
	if err != nil {
		t.Fatalf("Show log: %v", err)
	}
	if len(res.Events) == 0 {
		t.Fatal("len(Events) = 0, want at least one round-1 event")
	}
	for _, e := range res.Events {
		if e.Round != 1 {
			t.Errorf("Events has round %d, want only round 1", e.Round)
		}
	}
	if res.Live || !res.Archived || res.ArchivedAt.IsZero() {
		t.Errorf("log result Live = %v, Archived = %v, ArchivedAt = %v, want false, true, non-zero",
			res.Live, res.Archived, res.ArchivedAt)
	}
}

// TestShowArchivedWinsOverTheMirror pins the precedence of Show's archived
// step over the database: the record's sealed gate log answers where showDB
// on its own reports Missing (it has no gate row).
func TestShowArchivedWinsOverTheMirror(t *testing.T) {
	s := archiveShowFixture(t, map[string]string{"001-gate.log": "gate output\n"})
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

	rt := Runtime{Store: s, DB: d}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Round: 1, Section: ShowGate})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Missing {
		t.Error("Missing = true, want the sealed gate log (showDB alone reports Missing)")
	}
	if res.Text != "gate output\n" {
		t.Errorf("Text = %q, want %q", res.Text, "gate output\n")
	}
}

// TestShowArchivedDefaultsToNewestCompletedRound pins Show's archived step to
// showLive's default-round rule: with no --round, the newest completed round
// is the highest KindReport entry round -- round 2 in the fixture -- not the
// highest planned round (3).
func TestShowArchivedDefaultsToNewestCompletedRound(t *testing.T) {
	s := archiveShowFixture(t, nil)
	rt := Runtime{Store: s}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "fixture", Section: ShowPlan})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Round != 2 {
		t.Errorf("Round = %d, want 2 (highest round with a report entry)", res.Round)
	}
	want, err := os.ReadFile(filepath.Join(showFixtureDir, "002-plan.md"))
	if err != nil {
		t.Fatalf("read fixture 002-plan.md: %v", err)
	}
	if res.Text != string(want) {
		t.Errorf("Text = %q, want %q", res.Text, string(want))
	}
}

// mirrorLines returns body's complete lines the way the retired round
// transcript read yielded them (internal/ingest readAppendOnly): everything
// up to the final newline, split on '\n'. A file with no trailing newline
// has no complete line and yields none.
func mirrorLines(body []byte) [][]byte {
	end := bytes.LastIndexByte(body, '\n')
	if end < 0 {
		return nil
	}
	return bytes.Split(body[:end], []byte{'\n'})
}

// mirrorTranscriptRecords builds round n's builder transcript rows exactly as
// ingest built them before D3b: the builder stream (NNN-builder.jsonl) when
// the fixture has one, else the builder log (NNN-builder.log) (D3b plan §3
// fence item 3).
func mirrorTranscriptRecords(t *testing.T, dir string, n int, builderKind string) []db.TranscriptRecord {
	t.Helper()

	streamName := fmt.Sprintf("%03d-builder.jsonl", n)
	stream, err := os.ReadFile(filepath.Join(dir, streamName))
	if err == nil {
		lines := mirrorLines(stream)
		recs := make([]db.TranscriptRecord, 0, len(lines))
		for i, line := range lines {
			rec := db.TranscriptRecord{Seq: i, RecordJSON: string(line)}
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 && trimmed[0] == '{' {
				rec.Rendered = strings.Join(transcript.Render(builderKind, line), "\n")
			}
			recs = append(recs, rec)
		}
		return recs
	}
	if !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", streamName, err)
	}

	logName := fmt.Sprintf("%03d-builder.log", n)
	logBody, err := os.ReadFile(filepath.Join(dir, logName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", logName, err)
	}
	lines := mirrorLines(logBody)
	recs := make([]db.TranscriptRecord, 0, len(lines))
	for i, line := range lines {
		recs = append(recs, db.TranscriptRecord{Seq: i, Rendered: string(line)})
	}
	return recs
}

// seedRoundMirror inserts, directly, the round mirror rows ingest wrote
// before D3b and no longer writes: the plain round-file artifacts (plan,
// report, diff, drift) and each round's builder transcript, read from the
// fixture's own files exactly as ingest built them (D3b plan §3 fence items
// 1 and 3). Show's database fallback (showDB, show.go) still reads those rows
// for a binding with no live files and no archived record, so the db-backed
// show tests seed them by hand after ingest.Ingest.
func seedRoundMirror(t *testing.T, d *db.DB, dir string) {
	t.Helper()

	var bind struct {
		Name    string `json:"name"`
		Builder struct {
			Kind string `json:"kind"`
		} `json:"builder"`
	}
	bindJSON, err := os.ReadFile(filepath.Join(dir, "bind.json"))
	if err != nil {
		t.Fatalf("read bind.json: %v", err)
	}
	if err := json.Unmarshal(bindJSON, &bind); err != nil {
		t.Fatalf("parse bind.json: %v", err)
	}

	row, found, err := d.Binding(bind.Name)
	if err != nil || !found {
		t.Fatalf("Binding(%s): found=%v err=%v", bind.Name, found, err)
	}
	rounds, err := d.Rounds(row.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}

	type artifactSeed struct {
		roundID, kind, text, sha string
		size                     int64
	}
	type transcriptSeed struct {
		roundID string
		recs    []db.TranscriptRecord
	}

	roundFiles := []struct{ base, kind string }{
		{"%03d-plan.md", db.ArtifactPlan},
		{"%03d-report.md", db.ArtifactReport},
		{"%03d-diff.patch", db.ArtifactDiff},
		{"%03d-drift.patch", db.ArtifactDrift},
	}

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var artifacts []artifactSeed
	var transcripts []transcriptSeed
	for _, r := range rounds {
		for _, a := range roundFiles {
			body, rerr := os.ReadFile(filepath.Join(dir, fmt.Sprintf(a.base, r.Number)))
			if rerr != nil {
				if os.IsNotExist(rerr) {
					continue
				}
				t.Fatalf("read %s: %v", a.base, rerr)
			}
			sum := sha256.Sum256(body)
			artifacts = append(artifacts, artifactSeed{
				roundID: r.ID,
				kind:    a.kind,
				text:    string(body),
				size:    int64(len(body)),
				sha:     fmt.Sprintf("%x", sum),
			})
		}
		transcripts = append(transcripts, transcriptSeed{
			roundID: r.ID,
			recs:    mirrorTranscriptRecords(t, dir, r.Number, bind.Builder.Kind),
		})
	}

	if err := d.Tx(func(tx *db.Tx) error {
		for _, a := range artifacts {
			if err := tx.UpsertArtifact(db.Artifact{
				RoundID:    a.roundID,
				Kind:       a.kind,
				Text:       a.text,
				Bytes:      a.size,
				SHA256:     a.sha,
				CapturedAt: now,
			}); err != nil {
				return err
			}
		}
		for _, ts := range transcripts {
			if len(ts.recs) == 0 {
				continue
			}
			if _, err := tx.AppendTranscript(db.OwnerRound, ts.roundID, ts.recs); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed round mirror: %v", err)
	}
}
