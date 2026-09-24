package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// dedupeAt is the created_at every seeded mirror binding and record shares, so
// the binding maps to the record by the (name, created_at) identity rule.
var dedupeAt = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

// seedMirrorBinding inserts one mirror binding at dedupeAt and returns its id.
func seedMirrorBinding(t *testing.T, d *db.DB, name string) string {
	t.Helper()
	id, err := d.UpsertBinding(db.Binding{
		Name:         name,
		CWD:          "/tmp/" + name,
		BuilderMode:  "headless",
		CreatedAt:    dedupeAt,
		IngestSource: db.IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding(%s): %v", name, err)
	}
	return id
}

// seedMirrorRound adds round number to bindingID and returns the round id.
func seedMirrorRound(t *testing.T, d *db.DB, bindingID string, number int) string {
	t.Helper()
	id, err := d.UpsertRound(db.Round{BindingID: bindingID, Number: number, StartedAt: dedupeAt, Outcome: db.OutcomeOpen})
	if err != nil {
		t.Fatalf("UpsertRound(%d): %v", number, err)
	}
	return id
}

// recordJSON is the binding_record JSON for a binding named name whose builder
// harness kind is builderKind. transcriptKinds decodes it for the record-side
// kind of the transcript identity rule.
func recordJSON(t *testing.T, name, builderKind string) string {
	t.Helper()
	data, err := json.Marshal(store.Binding{
		Name:    name,
		CWD:     "/tmp/" + name,
		Builder: store.Endpoint{Kind: builderKind},
	})
	if err != nil {
		t.Fatalf("marshal record JSON: %v", err)
	}
	return string(data)
}

// seedRecordAt inserts a live record named name, created at at, whose JSON
// names builderKind, and returns the record id. at == dedupeAt maps the
// seeded mirror binding; any other at leaves it unmapped.
func seedRecordAt(t *testing.T, d *db.DB, name, builderKind string, at time.Time) string {
	t.Helper()
	id, err := d.RecordPut(db.Record{
		Owner:     "",
		Name:      name,
		State:     "active",
		Round:     1,
		CWD:       "/tmp/" + name,
		JSON:      recordJSON(t, name, builderKind),
		CreatedAt: at,
		UpdatedAt: at,
	})
	if err != nil {
		t.Fatalf("RecordPut(%s): %v", name, err)
	}
	return id
}

// putArtifact inserts one mirror artifact of a round with text as both its
// body and its sha256, and returns the stored row.
func putArtifact(t *testing.T, d *db.DB, roundID, kind, text string) db.Artifact {
	t.Helper()
	a := db.Artifact{
		ID:         db.NewID(),
		RoundID:    roundID,
		Kind:       kind,
		Text:       text,
		Bytes:      int64(len(text)),
		SHA256:     sha256Hex([]byte(text)),
		CapturedAt: time.Now(),
	}
	if err := d.UpsertArtifact(a); err != nil {
		t.Fatalf("UpsertArtifact(%s): %v", kind, err)
	}
	return a
}

// putRoundFile seals body under name for recordID, as a real seal would.
func putRoundFile(t *testing.T, d *db.DB, recordID, name string, round int, body string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := d.Tx(func(tx *db.Tx) error {
		return tx.RoundFilePut(recordID, name, round, []byte(body), now, now)
	}); err != nil {
		t.Fatalf("RoundFilePut(%s): %v", name, err)
	}
}

// appendTranscript fails the test when the append does.
func appendTranscript(t *testing.T, d *db.DB, ownerKind, ownerID string, recs []db.TranscriptRecord) {
	t.Helper()
	if _, err := d.AppendTranscript(ownerKind, ownerID, recs); err != nil {
		t.Fatalf("AppendTranscript(%s, %s): %v", ownerKind, ownerID, err)
	}
}

// mustPlan builds the plan and fails the test when DedupeMirror errors.
func mustPlan(t *testing.T, d *db.DB) dedupePlan {
	t.Helper()
	plan, err := DedupeMirror(d)
	if err != nil {
		t.Fatalf("DedupeMirror: %v", err)
	}
	return plan
}

// TestDedupeMirrorDeletesIdenticalReportAndKeepsOneByteDifferent pins case 1:
// a report artifact identical to the record's round file is planned for
// removal, and one differing by a single byte is kept. Mutations that break
// it: drop the sha256 comparison, or drop the length comparison, and the
// one-byte-different report is planned too.
func TestDedupeMirrorDeletesIdenticalReportAndKeepsOneByteDifferent(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)

	// Round 3: report and round file are byte-identical.
	round3 := seedMirrorRound(t, d, bindingID, 3)
	body := "003-report.md: the builder's report\n"
	putRoundFile(t, d, recordID, "003-report.md", 3, body)
	same := putArtifact(t, d, round3, db.ArtifactReport, body)

	// Round 4: the report differs from its round file by one byte.
	round4 := seedMirrorRound(t, d, bindingID, 4)
	putRoundFile(t, d, recordID, "004-report.md", 4, "004-report.md: the builder's report\n")
	putArtifact(t, d, round4, db.ArtifactReport, "004-report.md: the builder's report!")

	plan := mustPlan(t, d)

	if plan.stats.MirrorBindings != 1 {
		t.Errorf("MirrorBindings = %d, want 1", plan.stats.MirrorBindings)
	}
	if plan.stats.Unmapped != 0 {
		t.Errorf("Unmapped = %d, want 0", plan.stats.Unmapped)
	}
	if plan.stats.ArtifactsDeleted != 1 {
		t.Errorf("ArtifactsDeleted = %d, want 1", plan.stats.ArtifactsDeleted)
	}
	if plan.stats.ArtifactsKept != 1 {
		t.Errorf("ArtifactsKept = %d, want 1", plan.stats.ArtifactsKept)
	}
	if len(plan.artifactIDs) != 1 || plan.artifactIDs[0] != same.ID {
		t.Errorf("artifactIDs = %v, want [%s]", plan.artifactIDs, same.ID)
	}

	// DedupeMirror is read-only: both rows are still there.
	if _, ok, err := d.Artifact(round3, db.ArtifactReport); err != nil || !ok {
		t.Errorf("Artifact(round 3 report) after planning = (ok %v, err %v), want (true, nil)", ok, err)
	}
	if _, ok, err := d.Artifact(round4, db.ArtifactReport); err != nil || !ok {
		t.Errorf("Artifact(round 4 report) after planning = (ok %v, err %v), want (true, nil)", ok, err)
	}
}

// TestDedupeMirrorKeepsAnswerArtifacts pins case 2: an answer artifact is
// always kept, because it comes from a log payload and has no round file to be
// a duplicate of. Mutation that breaks it: give answer a round-file suffix,
// and this row is planned for removal.
func TestDedupeMirrorKeepsAnswerArtifacts(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	seedRecordAt(t, d, name, "claude", dedupeAt)
	round3 := seedMirrorRound(t, d, bindingID, 3)

	putArtifact(t, d, round3, db.ArtifactAnswer, "the answer\n")

	plan := mustPlan(t, d)

	if plan.stats.ArtifactsDeleted != 0 {
		t.Errorf("ArtifactsDeleted = %d, want 0", plan.stats.ArtifactsDeleted)
	}
	if plan.stats.ArtifactsKept != 1 {
		t.Errorf("ArtifactsKept = %d, want 1", plan.stats.ArtifactsKept)
	}
	if len(plan.artifactIDs) != 0 {
		t.Errorf("artifactIDs = %v, want none", plan.artifactIDs)
	}
	if _, ok, err := d.Artifact(round3, db.ArtifactAnswer); err != nil || !ok {
		t.Errorf("answer artifact after planning = (ok %v, err %v), want (true, nil)", ok, err)
	}
}

// TestDedupeMirrorKeepsEveryRowOfAnUnmappedBinding pins case 3: a binding that
// maps to no record keeps every artifact and every transcript row, and is
// counted as unmapped. Mutation that breaks it: drop the created_at
// comparison in dedupeRecord, and this binding maps to the record after all,
// so its identical report is planned.
func TestDedupeMirrorKeepsEveryRowOfAnUnmappedBinding(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	round3 := seedMirrorRound(t, d, bindingID, 3)

	// The record's created_at is an hour off, so nothing maps.
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt.Add(time.Hour))

	body := "003-report.md: the builder's report\n"
	putRoundFile(t, d, recordID, "003-report.md", 3, body)
	putArtifact(t, d, round3, db.ArtifactReport, body)
	appendTranscript(t, d, db.OwnerRound, round3, []db.TranscriptRecord{{Seq: 0, Rendered: "a line"}})

	plan := mustPlan(t, d)

	if plan.stats.MirrorBindings != 1 {
		t.Errorf("MirrorBindings = %d, want 1", plan.stats.MirrorBindings)
	}
	if plan.stats.Unmapped != 1 {
		t.Errorf("Unmapped = %d, want 1", plan.stats.Unmapped)
	}
	if plan.stats.ArtifactsDeleted != 0 || len(plan.artifactIDs) != 0 {
		t.Errorf("artifactIDs = %v, want none (ArtifactsDeleted %d)", plan.artifactIDs, plan.stats.ArtifactsDeleted)
	}
	if plan.stats.ArtifactsKept != 0 {
		t.Errorf("ArtifactsKept = %d, want 0: an unmapped binding is not examined", plan.stats.ArtifactsKept)
	}
	if plan.stats.TranscriptRoundsDeleted != 0 || len(plan.transcriptOwners) != 0 {
		t.Errorf("transcriptOwners = %v, want none", plan.transcriptOwners)
	}
	if plan.stats.TranscriptRoundsKept != 0 {
		t.Errorf("TranscriptRoundsKept = %d, want 0: an unmapped binding is not examined", plan.stats.TranscriptRoundsKept)
	}

	if _, ok, err := d.Artifact(round3, db.ArtifactReport); err != nil || !ok {
		t.Errorf("artifact of an unmapped binding = (ok %v, err %v), want (true, nil)", ok, err)
	}
}

// TestDedupeMirrorPlansIdenticalTranscriptAndKeepsAlteredRendered pins case 4:
// a transcript re-derived from the round-file stream is planned, and the same
// transcript with one row's rendered text altered is kept. Mutation that
// breaks it: drop the Rendered comparison in transcriptRowsEqual, and the
// altered transcript is planned too.
func TestDedupeMirrorPlansIdenticalTranscriptAndKeepsAlteredRendered(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)

	line1 := `{"ts":"2026-09-01T10:00:00Z","type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`
	line2 := `{"ts":"2026-09-01T10:00:01Z","type":"user","message":{"content":"go on"}}`
	raw := line1 + "\n" + line2 + "\n"
	// The lines readAppendOnly returns for raw: both complete, no newline.
	lines := [][]byte{[]byte(line1), []byte(line2)}

	// Round 3: the seeded rows are exactly what re-derivation produces.
	round3 := seedMirrorRound(t, d, bindingID, 3)
	putRoundFile(t, d, recordID, "003-builder.jsonl", 3, raw)
	same, skipped := streamTranscriptRecords("claude", lines, 0)
	if skipped != 0 {
		t.Fatalf("streamTranscriptRecords skipped %d lines, want 0", skipped)
	}
	appendTranscript(t, d, db.OwnerRound, round3, same)

	// Round 4: one row's rendered text was altered.
	round4 := seedMirrorRound(t, d, bindingID, 4)
	putRoundFile(t, d, recordID, "004-builder.jsonl", 4, raw)
	altered, _ := streamTranscriptRecords("claude", lines, 0)
	altered[1].Rendered = "tampered"
	appendTranscript(t, d, db.OwnerRound, round4, altered)

	plan := mustPlan(t, d)

	if plan.stats.TranscriptRoundsDeleted != 1 {
		t.Errorf("TranscriptRoundsDeleted = %d, want 1", plan.stats.TranscriptRoundsDeleted)
	}
	if plan.stats.TranscriptRowsDeleted != len(same) {
		t.Errorf("TranscriptRowsDeleted = %d, want %d", plan.stats.TranscriptRowsDeleted, len(same))
	}
	if plan.stats.TranscriptRoundsKept != 1 {
		t.Errorf("TranscriptRoundsKept = %d, want 1", plan.stats.TranscriptRoundsKept)
	}
	if len(plan.transcriptOwners) != 1 || plan.transcriptOwners[0] != round3 {
		t.Errorf("transcriptOwners = %v, want [%s]", plan.transcriptOwners, round3)
	}
}

// TestDedupeMirrorNeverPlansPlannerTranscript pins case 5: a planner-owned
// transcript sharing an owner id with a planned round is never touched. The
// round rows of that owner go, the planner rows stay. Mutation that breaks
// it: let DeleteRoundTranscript take the owner kind from its caller, and the
// planner rows disappear with the round's.
func TestDedupeMirrorNeverPlansPlannerTranscript(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)

	line := `{"ts":"2026-09-01T10:00:00Z","type":"assistant","message":{"content":"hi"}}`
	round3 := seedMirrorRound(t, d, bindingID, 3)
	putRoundFile(t, d, recordID, "003-builder.jsonl", 3, line+"\n")
	roundRecs, _ := streamTranscriptRecords("claude", [][]byte{[]byte(line)}, 0)
	appendTranscript(t, d, db.OwnerRound, round3, roundRecs)

	// A planner transcript under the very same owner id.
	appendTranscript(t, d, db.OwnerPlanner, round3, []db.TranscriptRecord{
		{Seq: 0, RecordJSON: line, Rendered: "the planner's own line"},
	})

	plan := mustPlan(t, d)

	if plan.stats.TranscriptRoundsDeleted != 1 || len(plan.transcriptOwners) != 1 || plan.transcriptOwners[0] != round3 {
		t.Fatalf("transcriptOwners = %v (rounds deleted %d), want [%s]", plan.transcriptOwners, plan.stats.TranscriptRoundsDeleted, round3)
	}

	// Applying the plan's deletion for that owner leaves the planner rows.
	if err := d.Tx(func(tx *db.Tx) error {
		_, derr := tx.DeleteRoundTranscript(round3)
		return derr
	}); err != nil {
		t.Fatalf("DeleteRoundTranscript: %v", err)
	}
	plannerRows, err := d.Transcript(db.OwnerPlanner, round3, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(planner): %v", err)
	}
	if len(plannerRows) != 1 {
		t.Errorf("planner transcript has %d rows after the round's delete, want 1", len(plannerRows))
	}
}

// TestDedupeMirrorLeavesABindingMatchingTwoRecordsUnmapped pins case 6: one
// name and created_at held by both a live and an archived record is two
// matches, so the binding is unmapped and keeps every row. Mutation that
// breaks it: take the first match instead of requiring exactly one, and the
// identical report below is planned.
func TestDedupeMirrorLeavesABindingMatchingTwoRecordsUnmapped(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	round3 := seedMirrorRound(t, d, bindingID, 3)

	liveID := seedRecordAt(t, d, name, "claude", dedupeAt)
	archivedJSON := recordJSON(t, name, "claude")
	if err := d.Tx(func(tx *db.Tx) error {
		_, aerr := tx.RecordPutArchived(db.Record{
			Owner:     "",
			Name:      name,
			State:     "archived",
			Round:     1,
			CWD:       "/tmp/" + name,
			JSON:      archivedJSON,
			CreatedAt: dedupeAt,
			UpdatedAt: dedupeAt,
		}, dedupeAt.Add(time.Hour))
		return aerr
	}); err != nil {
		t.Fatalf("RecordPutArchived: %v", err)
	}

	body := "003-report.md: the builder's report\n"
	putRoundFile(t, d, liveID, "003-report.md", 3, body)
	putArtifact(t, d, round3, db.ArtifactReport, body)

	plan := mustPlan(t, d)

	if plan.stats.MirrorBindings != 1 {
		t.Errorf("MirrorBindings = %d, want 1", plan.stats.MirrorBindings)
	}
	if plan.stats.Unmapped != 1 {
		t.Errorf("Unmapped = %d, want 1", plan.stats.Unmapped)
	}
	if len(plan.artifactIDs) != 0 {
		t.Errorf("artifactIDs = %v, want none", plan.artifactIDs)
	}
}

// TestDedupeMirrorRehearsal rehearses a removal against a copy of a real
// database, so what a run would do is known before one is applied. It skips
// unless RELEVO_DEDUPE_REHEARSAL names a database file; when set, it copies
// that file (and its -wal when present) into a temp dir, opens the copy, and
// logs the plan. It never calls DedupeMirrorOnce and never writes to the named
// file, so the real database is untouched.
func TestDedupeMirrorRehearsal(t *testing.T) {
	src := os.Getenv("RELEVO_DEDUPE_REHEARSAL")
	if src == "" {
		t.Skip("RELEVO_DEDUPE_REHEARSAL is not set")
	}

	dir := t.TempDir()
	dst := filepath.Join(dir, filepath.Base(src))
	copyRehearsalFile(t, src, dst)
	if _, err := os.Stat(src + "-wal"); err == nil {
		copyRehearsalFile(t, src+"-wal", dst+"-wal")
	}

	d, err := db.Open(dst)
	if err != nil {
		t.Fatalf("db.Open(%s): %v", dst, err)
	}
	defer d.Close()

	plan, err := DedupeMirror(d)
	if err != nil {
		t.Fatalf("DedupeMirror: %v", err)
	}

	t.Logf("rehearsal stats: %+v", plan.stats)
	t.Logf("rehearsal: %d artifact rows, %d transcript rounds, %d transcript rows planned for deletion",
		len(plan.artifactIDs), len(plan.transcriptOwners), plan.stats.TranscriptRowsDeleted)
}

// copyRehearsalFile copies src to dst byte for byte.
func copyRehearsalFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// seedPlannedScene seeds one mapped binding with: a report artifact identical
// to its round file (planned for removal), an answer artifact (always kept), a
// two-row round transcript re-derivable from the round's stream (planned), and
// a planner transcript under the same owner id (never touched). It returns the
// mirror round id.
func seedPlannedScene(t *testing.T, d *db.DB) string {
	t.Helper()
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)
	round3 := seedMirrorRound(t, d, bindingID, 3)

	body := "003-report.md: the builder's report\n"
	putRoundFile(t, d, recordID, "003-report.md", 3, body)
	putArtifact(t, d, round3, db.ArtifactReport, body)
	putArtifact(t, d, round3, db.ArtifactAnswer, "the answer\n")

	line1 := `{"ts":"2026-09-01T10:00:00Z","type":"assistant","message":{"content":"hello"}}`
	line2 := `{"ts":"2026-09-01T10:00:01Z","type":"user","message":{"content":"go on"}}`
	putRoundFile(t, d, recordID, "003-builder.jsonl", 3, line1+"\n"+line2+"\n")
	recs, _ := streamTranscriptRecords("claude", [][]byte{[]byte(line1), []byte(line2)}, 0)
	appendTranscript(t, d, db.OwnerRound, round3, recs)
	appendTranscript(t, d, db.OwnerPlanner, round3, []db.TranscriptRecord{
		{Seq: 0, RecordJSON: line1, Rendered: "the planner's own line"},
	})

	return round3
}

// dedupeBackupPath is where DedupeMirrorOnce backs up to for a given now and
// backupDir, so a test can pre-create the path or assert on the file.
func dedupeBackupPath(backupDir string, now time.Time) string {
	return filepath.Join(backupDir, "relevo.db.pre-dedupe-"+now.UTC().Format("20060102-150405"))
}

// seedPlannedSceneStats is the stats a run over seedPlannedScene's scene
// reports, with DoneAt and BackupPath filled in by the caller.
func seedPlannedSceneCounts() DedupeStats {
	return DedupeStats{
		MirrorBindings:          1,
		Unmapped:                0,
		ArtifactsDeleted:        1,
		ArtifactsKept:           1,
		TranscriptRoundsDeleted: 1,
		TranscriptRowsDeleted:   2,
		TranscriptRoundsKept:    0,
	}
}

// TestDedupeMirrorOnceDeletesBacksUpAndRecordsKV pins the first run: it deletes
// exactly the planned rows and no others, writes the backup file before
// deleting (the copy still holds the deleted artifact and transcript rows),
// writes the kv row, and reports the stats it applied. Mutation that breaks it:
// delete before backing up, and the backup no longer holds the deleted rows.
func TestDedupeMirrorOnceDeletesBacksUpAndRecordsKV(t *testing.T) {
	d := openTestDB(t)
	round3 := seedPlannedScene(t, d)
	backupDir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	stats, ran, err := DedupeMirrorOnce(d, backupDir, now)
	if err != nil {
		t.Fatalf("DedupeMirrorOnce: %v", err)
	}
	if !ran {
		t.Fatal("ran = false, want true")
	}

	want := seedPlannedSceneCounts()
	want.DoneAt = now
	want.BackupPath = dedupeBackupPath(backupDir, now)
	if stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}

	// The planned rows are gone; the kept ones are not.
	if _, ok, err := d.Artifact(round3, db.ArtifactReport); err != nil || ok {
		t.Errorf("report artifact after the run = (ok %v, err %v), want (false, nil)", ok, err)
	}
	if _, ok, err := d.Artifact(round3, db.ArtifactAnswer); err != nil || !ok {
		t.Errorf("answer artifact after the run = (ok %v, err %v), want (true, nil)", ok, err)
	}
	roundRows, err := d.Transcript(db.OwnerRound, round3, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(round): %v", err)
	}
	if len(roundRows) != 0 {
		t.Errorf("round transcript has %d rows after the run, want 0", len(roundRows))
	}
	plannerRows, err := d.Transcript(db.OwnerPlanner, round3, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(planner): %v", err)
	}
	if len(plannerRows) != 1 {
		t.Errorf("planner transcript has %d rows after the run, want 1", len(plannerRows))
	}

	// The kv row is the run's own document.
	stored, ok, err := d.KVGet(dedupeKVKey)
	if err != nil || !ok {
		t.Fatalf("KVGet(%s) = (ok %v, err %v), want (true, nil)", dedupeKVKey, ok, err)
	}
	var recorded DedupeStats
	if err := json.Unmarshal(stored, &recorded); err != nil {
		t.Fatalf("kv value is not a DedupeStats: %v", err)
	}
	if !reflect.DeepEqual(recorded, stats) {
		t.Errorf("kv stats = %+v, want %+v", recorded, stats)
	}

	// The backup still holds what the run deleted, so it can be undone.
	copyDB, err := db.Open(stats.BackupPath)
	if err != nil {
		t.Fatalf("Open(backup): %v", err)
	}
	defer copyDB.Close()
	if _, ok, err := copyDB.Artifact(round3, db.ArtifactReport); err != nil || !ok {
		t.Errorf("backup report artifact = (ok %v, err %v), want (true, nil)", ok, err)
	}
	copiedRows, err := copyDB.Transcript(db.OwnerRound, round3, 0, 0)
	if err != nil {
		t.Fatalf("backup Transcript(round): %v", err)
	}
	if len(copiedRows) != 2 {
		t.Errorf("backup round transcript has %d rows, want 2", len(copiedRows))
	}
}

// TestDedupeMirrorOnceRunsOnlyOnce pins the second run: the kv row makes it a
// no-op that reports ran false and a zero stats, and the database does not
// change. Mutation that breaks it: drop the kv check, and a second run deletes
// nothing (there is nothing left) but takes a second backup and rewrites the
// kv row, so the file count and BackupPath below still move.
func TestDedupeMirrorOnceRunsOnlyOnce(t *testing.T) {
	d := openTestDB(t)
	seedPlannedScene(t, d)
	backupDir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	if _, _, err := DedupeMirrorOnce(d, backupDir, now); err != nil {
		t.Fatalf("first DedupeMirrorOnce: %v", err)
	}

	before, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	stats, ran, err := DedupeMirrorOnce(d, backupDir, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second DedupeMirrorOnce: %v", err)
	}
	if ran {
		t.Error("second run ran = true, want false")
	}
	if stats != (DedupeStats{}) {
		t.Errorf("second run stats = %+v, want the zero value", stats)
	}

	after, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if !reflect.DeepEqual(before.Rows, after.Rows) {
		t.Errorf("rows after the second run = %v, want %v", after.Rows, before.Rows)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("ReadDir(backupDir): %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("backupDir holds %d files, want 1", len(entries))
	}
}

// TestDedupeMirrorOnceBackupFailureDeletesNothing pins the ordering: when the
// backup cannot be written -- here because the path already exists -- the run
// returns the error with no deletes and no kv row, so the next daemon start
// retries. Mutation that breaks it: move the backup after the delete
// transaction, and the rows are gone with no backup to restore from.
func TestDedupeMirrorOnceBackupFailureDeletesNothing(t *testing.T) {
	d := openTestDB(t)
	round3 := seedPlannedScene(t, d)
	backupDir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	backupPath := dedupeBackupPath(backupDir, now)
	if err := os.WriteFile(backupPath, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("pre-create the backup path: %v", err)
	}

	_, ran, err := DedupeMirrorOnce(d, backupDir, now)
	if err == nil {
		t.Fatal("DedupeMirrorOnce with an unwritable backup path = nil, want an error")
	}
	if ran {
		t.Error("ran = true, want false")
	}

	if _, ok, err := d.Artifact(round3, db.ArtifactReport); err != nil || !ok {
		t.Errorf("report artifact after the failed backup = (ok %v, err %v), want (true, nil)", ok, err)
	}
	roundRows, err := d.Transcript(db.OwnerRound, round3, 0, 0)
	if err != nil {
		t.Fatalf("Transcript(round): %v", err)
	}
	if len(roundRows) != 2 {
		t.Errorf("round transcript has %d rows after the failed backup, want 2", len(roundRows))
	}
	if _, ok, err := d.KVGet(dedupeKVKey); err != nil || ok {
		t.Errorf("KVGet(%s) after the failed backup = (ok %v, err %v), want (false, nil)", dedupeKVKey, ok, err)
	}
}

// TestDedupeMirrorOnceWithNothingToDelete pins the empty plan: the kv row is
// written, no backup file is taken (there is nothing to undo), and ran is true.
// Mutation that breaks it: back up unconditionally, and the empty backupDir
// assertion below fails.
func TestDedupeMirrorOnceWithNothingToDelete(t *testing.T) {
	d := openTestDB(t)
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	seedRecordAt(t, d, name, "claude", dedupeAt)
	seedMirrorRound(t, d, bindingID, 3)

	backupDir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	stats, ran, err := DedupeMirrorOnce(d, backupDir, now)
	if err != nil {
		t.Fatalf("DedupeMirrorOnce: %v", err)
	}
	if !ran {
		t.Fatal("ran = false, want true")
	}
	if stats.MirrorBindings != 1 {
		t.Errorf("MirrorBindings = %d, want 1", stats.MirrorBindings)
	}
	if stats.ArtifactsDeleted != 0 || stats.TranscriptRoundsDeleted != 0 {
		t.Errorf("stats = %+v, want nothing deleted", stats)
	}
	if stats.BackupPath != "" {
		t.Errorf("BackupPath = %q, want empty: nothing was deleted", stats.BackupPath)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("ReadDir(backupDir): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("backupDir holds %v, want no file", entries)
	}

	stored, ok, err := d.KVGet(dedupeKVKey)
	if err != nil || !ok {
		t.Fatalf("KVGet(%s) = (ok %v, err %v), want (true, nil)", dedupeKVKey, ok, err)
	}
	var recorded DedupeStats
	if err := json.Unmarshal(stored, &recorded); err != nil {
		t.Fatalf("kv value is not a DedupeStats: %v", err)
	}
	if !reflect.DeepEqual(recorded, stats) {
		t.Errorf("kv stats = %+v, want %+v", recorded, stats)
	}
}
