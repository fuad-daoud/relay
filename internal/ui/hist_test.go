package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	"github.com/fuad-daoud/relevo/internal/transcript"
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
	seedRoundMirror(t, d, histFixtureDir)

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
// 1 and 3). relevo.Show's database fallback (showDB) still reads those rows
// for a binding with no live files and no archived record in the runtime's
// store, so seedArchivedHistBinding seeds them by hand after ingest.Ingest.
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
