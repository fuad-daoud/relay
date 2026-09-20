package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func ptr[T any](v T) *T { return &v }

func TestUpsertRepoByOrigin(t *testing.T) {
	d := openTestDB(t)
	r := Repo{OriginURL: ptr("https://example.test/a.git"), FirstSeen: time.Now()}

	id1, err := d.UpsertRepo(r)
	if err != nil {
		t.Fatalf("UpsertRepo (1st): %v", err)
	}
	id2, err := d.UpsertRepo(r)
	if err != nil {
		t.Fatalf("UpsertRepo (2nd): %v", err)
	}
	if id1 != id2 {
		t.Errorf("ids differ: %q != %q", id1, id2)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM repo`).Scan(&count); err != nil {
		t.Fatalf("count repo: %v", err)
	}
	if count != 1 {
		t.Errorf("repo has %d rows, want 1", count)
	}
}

func TestUpsertRepoByCommonDirFallback(t *testing.T) {
	d := openTestDB(t)
	r := Repo{CommonDir: ptr("/home/x/repo/.git"), FirstSeen: time.Now()}

	id1, err := d.UpsertRepo(r)
	if err != nil {
		t.Fatalf("UpsertRepo (1st): %v", err)
	}
	id2, err := d.UpsertRepo(r)
	if err != nil {
		t.Fatalf("UpsertRepo (2nd): %v", err)
	}
	if id1 != id2 {
		t.Errorf("ids differ: %q != %q", id1, id2)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM repo`).Scan(&count); err != nil {
		t.Fatalf("count repo: %v", err)
	}
	if count != 1 {
		t.Errorf("repo has %d rows, want 1", count)
	}
}

func TestUpsertRepoFillsMissingKey(t *testing.T) {
	d := openTestDB(t)
	origin := "https://example.test/b.git"
	commonDir := "/home/x/b/.git"

	id1, err := d.UpsertRepo(Repo{OriginURL: ptr(origin), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo (origin only): %v", err)
	}

	id2, err := d.UpsertRepo(Repo{OriginURL: ptr(origin), CommonDir: ptr(commonDir), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo (both): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %q != %q", id1, id2)
	}

	var gotCommonDir string
	if err := d.sqlDB.QueryRow(`SELECT common_dir FROM repo WHERE id = ?`, id1).Scan(&gotCommonDir); err != nil {
		t.Fatalf("select common_dir: %v", err)
	}
	if gotCommonDir != commonDir {
		t.Errorf("common_dir = %q, want %q", gotCommonDir, commonDir)
	}
}

func TestUpsertPlannerUpdatesLastSeen(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)

	id1, err := d.UpsertPlanner(Planner{HarnessKind: "claude", SessionID: "sess-1", FirstSeen: t1, LastSeen: t1})
	if err != nil {
		t.Fatalf("UpsertPlanner (1st): %v", err)
	}
	id2, err := d.UpsertPlanner(Planner{HarnessKind: "claude", SessionID: "sess-1", FirstSeen: t2, LastSeen: t2})
	if err != nil {
		t.Fatalf("UpsertPlanner (2nd): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %q != %q", id1, id2)
	}

	var gotLastSeen string
	if err := d.sqlDB.QueryRow(`SELECT last_seen FROM planner WHERE id = ?`, id1).Scan(&gotLastSeen); err != nil {
		t.Fatalf("select last_seen: %v", err)
	}
	if gotLastSeen != formatTime(t2) {
		t.Errorf("last_seen = %q, want %q", gotLastSeen, formatTime(t2))
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM planner`).Scan(&count); err != nil {
		t.Fatalf("count planner: %v", err)
	}
	if count != 1 {
		t.Errorf("planner has %d rows, want 1", count)
	}
}

func newTestBinding(name string, createdAt time.Time) Binding {
	return Binding{
		Name:         name,
		CWD:          "/home/x/" + name,
		BuilderMode:  "headless",
		CreatedAt:    createdAt,
		IngestSource: IngestLive,
	}
}

func TestUpsertBindingNaturalKey(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)

	id1, err := d.UpsertBinding(newTestBinding("webshop", t1))
	if err != nil {
		t.Fatalf("UpsertBinding (t1): %v", err)
	}
	id1Again, err := d.UpsertBinding(newTestBinding("webshop", t1))
	if err != nil {
		t.Fatalf("UpsertBinding (t1 again): %v", err)
	}
	if id1 != id1Again {
		t.Fatalf("same (name, created_at) minted a new id: %q != %q", id1, id1Again)
	}

	id2, err := d.UpsertBinding(newTestBinding("webshop", t2))
	if err != nil {
		t.Fatalf("UpsertBinding (t2): %v", err)
	}
	if id2 == id1 {
		t.Fatalf("different created_at reused id %q", id1)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM binding WHERE name = 'webshop'`).Scan(&count); err != nil {
		t.Fatalf("count binding: %v", err)
	}
	if count != 2 {
		t.Errorf("binding has %d rows, want 2", count)
	}
}

func newTestRound(bindingID string, number int, outcome string) Round {
	return Round{
		BindingID: bindingID,
		Number:    number,
		StartedAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		Outcome:   outcome,
	}
}

func TestUpsertRoundReplacesColumns(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	id1, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeOpen))
	if err != nil {
		t.Fatalf("UpsertRound (open): %v", err)
	}
	id2, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeReported))
	if err != nil {
		t.Fatalf("UpsertRound (reported): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %q != %q", id1, id2)
	}

	var outcome string
	if err := d.sqlDB.QueryRow(`SELECT outcome FROM round WHERE id = ?`, id1).Scan(&outcome); err != nil {
		t.Fatalf("select outcome: %v", err)
	}
	if outcome != OutcomeReported {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeReported)
	}
}

func TestAppendEventsIgnoresKnownSeq(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	mkEvent := func(seq int) Event {
		return Event{BindingID: bindingID, Seq: seq, TS: time.Now(), Kind: "send", Direction: "planner_to_builder", EntryJSON: "{}"}
	}

	added, err := d.AppendEvents(bindingID, []Event{mkEvent(1), mkEvent(2), mkEvent(3)})
	if err != nil {
		t.Fatalf("AppendEvents (1st): %v", err)
	}
	if added != 3 {
		t.Fatalf("added = %d, want 3", added)
	}

	added, err = d.AppendEvents(bindingID, []Event{mkEvent(1), mkEvent(2), mkEvent(3), mkEvent(4)})
	if err != nil {
		t.Fatalf("AppendEvents (2nd): %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM event WHERE binding_id = ?`, bindingID).Scan(&count); err != nil {
		t.Fatalf("count event: %v", err)
	}
	if count != 4 {
		t.Errorf("event has %d rows, want 4", count)
	}
}

func TestUpsertArtifactByKind(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	roundID, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeOpen))
	if err != nil {
		t.Fatalf("UpsertRound: %v", err)
	}

	a := Artifact{RoundID: roundID, Kind: ArtifactPlan, Text: "plan v1", Bytes: 7, SHA256: "aaa", CapturedAt: time.Now()}
	if err := d.UpsertArtifact(a); err != nil {
		t.Fatalf("UpsertArtifact (1st): %v", err)
	}
	a.Text = "plan v2"
	a.SHA256 = "bbb"
	if err := d.UpsertArtifact(a); err != nil {
		t.Fatalf("UpsertArtifact (2nd): %v", err)
	}

	got, ok, err := d.Artifact(roundID, ArtifactPlan)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if !ok {
		t.Fatal("Artifact not found")
	}
	if got.Text != "plan v2" {
		t.Errorf("Text = %q, want %q", got.Text, "plan v2")
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM artifact WHERE round_id = ?`, roundID).Scan(&count); err != nil {
		t.Fatalf("count artifact: %v", err)
	}
	if count != 1 {
		t.Errorf("artifact has %d rows, want 1", count)
	}
}

func TestAppendTranscriptIgnoresKnownSeq(t *testing.T) {
	d := openTestDB(t)

	mkRecord := func(seq int) TranscriptRecord {
		return TranscriptRecord{Seq: seq, RecordJSON: "{}", Rendered: "line"}
	}

	added, err := d.AppendTranscript(OwnerPlanner, "sess-1", []TranscriptRecord{mkRecord(0), mkRecord(1)})
	if err != nil {
		t.Fatalf("AppendTranscript (1st): %v", err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}

	added, err = d.AppendTranscript(OwnerPlanner, "sess-1", []TranscriptRecord{mkRecord(0), mkRecord(1), mkRecord(2)})
	if err != nil {
		t.Fatalf("AppendTranscript (2nd): %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM transcript WHERE owner_id = ?`, "sess-1").Scan(&count); err != nil {
		t.Fatalf("count transcript: %v", err)
	}
	if count != 3 {
		t.Errorf("transcript has %d rows, want 3", count)
	}
}

func TestSaveCursorReplaces(t *testing.T) {
	d := openTestDB(t)

	if err := d.SaveCursor(Cursor{Source: "/x/log.jsonl", ByteOffset: 10, HeadSHA: "aaa", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveCursor (1st): %v", err)
	}
	if err := d.SaveCursor(Cursor{Source: "/x/log.jsonl", ByteOffset: 20, HeadSHA: "aaa", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveCursor (2nd): %v", err)
	}

	got, ok, err := d.Cursor("/x/log.jsonl")
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}
	if !ok {
		t.Fatal("Cursor not found")
	}
	if got.ByteOffset != 20 {
		t.Errorf("ByteOffset = %d, want 20", got.ByteOffset)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM ingest_cursor`).Scan(&count); err != nil {
		t.Fatalf("count ingest_cursor: %v", err)
	}
	if count != 1 {
		t.Errorf("ingest_cursor has %d rows, want 1", count)
	}
}

func TestWriterRejectsInvalidEnum(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	_, err = d.UpsertRound(Round{BindingID: bindingID, Number: 1, StartedAt: time.Now(), Outcome: "won"})
	if err == nil {
		t.Fatal("UpsertRound with invalid Outcome: got nil error")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("UpsertRound with invalid Outcome: err = %v, want ErrInvalid", err)
	}
}
