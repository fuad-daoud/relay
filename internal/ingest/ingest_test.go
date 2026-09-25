package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

const fixtureDir = "testdata/binding-three-rounds"

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// copyFixture copies the golden fixture (minus bind-legacy.json, which is
// only ever used in place of bind.json) into a fresh temp dir so a test can
// mutate it freely.
func copyFixture(t *testing.T) string {
	t.Helper()
	return copyFixtureAs(t, "bind.json")
}

// copyLegacyFixture is copyFixture with bind-legacy.json standing in for
// bind.json, for the legacy-bind.json tests.
func copyLegacyFixture(t *testing.T) string {
	t.Helper()
	return copyFixtureAs(t, "bind-legacy.json")
}

func copyFixtureAs(t *testing.T, bindFile string) string {
	t.Helper()
	dst := t.TempDir()
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatalf("ReadDir fixture: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || isBindVariant(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixtureDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	{
		data, err := os.ReadFile(filepath.Join(fixtureDir, bindFile))
		if err != nil {
			t.Fatalf("read %s: %v", bindFile, err)
		}
		if err := os.WriteFile(filepath.Join(dst, "bind.json"), data, 0o644); err != nil {
			t.Fatalf("write bind.json: %v", err)
		}
	}
	return dst
}

// isBindVariant reports whether name is one of the fixture's bind.json
// stand-ins (bind.json itself, or a bind-*.json variant used by a specific
// test), so copyFixtureAs never lets an unrelated variant leak into a
// destination dir as a stray member.
func isBindVariant(name string) bool {
	if name == "bind.json" {
		return true
	}
	return strings.HasPrefix(name, "bind-") && strings.HasSuffix(name, ".json")
}

func mustBinding(t *testing.T, d *db.DB, name string) db.BindingRow {
	t.Helper()
	b, found, err := d.Binding(name)
	if err != nil {
		t.Fatalf("Binding(%s): %v", name, err)
	}
	if !found {
		t.Fatalf("Binding(%s): not found", name)
	}
	return b
}

func mustRounds(t *testing.T, d *db.DB, bindingID string) []db.Round {
	t.Helper()
	rounds, err := d.Rounds(bindingID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	sort.Slice(rounds, func(i, j int) bool { return rounds[i].Number < rounds[j].Number })
	return rounds
}

func strEq(a *string, want string) bool { return a != nil && *a == want }

func TestIngestFixtureLive(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	stats, err := Ingest(context.Background(), DirSource(dir), d, Deps{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// D3b fence items 1 and 3: the fixture's log carries no answer entry, so
	// no artifact row is written at all (Artifacts == 0); this test locates
	// no planner transcript, so TranscriptRecords == 0, and the builder
	// stream's one unparseable line no longer contributes to Skipped.
	wantStats := Stats{Bindings: 1, Rounds: 3, Events: 9}
	if stats != wantStats {
		t.Errorf("Stats = %+v, want %+v", stats, wantStats)
	}

	dbStats, err := d.Stats()
	if err != nil {
		t.Fatalf("db.Stats: %v", err)
	}
	wantRows := map[string]int{"repo": 1, "planner": 1, "binding": 1, "round": 3, "event": 9, "artifact": 0, "transcript": 0}
	for tbl, want := range wantRows {
		if dbStats.Rows[tbl] != want {
			t.Errorf("Rows[%s] = %d, want %d", tbl, dbStats.Rows[tbl], want)
		}
	}

	b := mustBinding(t, d, "fixture")
	if !strEq(b.RepoOrigin, "https://github.com/o/r") {
		t.Errorf("RepoOrigin = %v, want https://github.com/o/r", b.RepoOrigin)
	}
	if !strEq(b.Feature, "auth") {
		t.Errorf("Feature = %v, want auth", b.Feature)
	}
	if !strEq(b.FinalState, "needs_you") {
		t.Errorf("FinalState = %v, want needs_you", b.FinalState)
	}
	if b.IngestSource != "live" {
		t.Errorf("IngestSource = %q, want live", b.IngestSource)
	}
	if b.PlannerID == nil {
		t.Error("PlannerID is nil, want a planner row")
	}

	rounds := mustRounds(t, d, b.ID)
	if len(rounds) != 3 {
		t.Fatalf("len(rounds) = %d, want 3", len(rounds))
	}
	if rounds[0].Outcome != db.OutcomeReported {
		t.Errorf("round 1 outcome = %q, want reported", rounds[0].Outcome)
	}
	if rounds[1].Outcome != db.OutcomeReported {
		t.Errorf("round 2 outcome = %q, want reported", rounds[1].Outcome)
	}
	if !strEq(rounds[1].ReportOutcome, "halted") {
		t.Errorf("round 2 report_outcome = %v, want halted", rounds[1].ReportOutcome)
	}
	if !strEq(rounds[1].BuilderHarness, "agy") || !strEq(rounds[1].BuilderProvider, "google") {
		t.Errorf("round 2 builder = %v/%v, want agy/google", rounds[1].BuilderHarness, rounds[1].BuilderProvider)
	}
	if rounds[2].Outcome != db.OutcomeExited {
		t.Errorf("round 3 outcome = %q, want exited", rounds[2].Outcome)
	}

	events, err := d.Events(b.ID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 9 {
		t.Errorf("len(events) = %d, want 9", len(events))
	}

	// D3b fence item 1: the plain round-file artifacts are no longer mirrored,
	// so plan/report/diff/drift are absent for every round.
	// r1: plan/report/diff and gate_log/question absent.
	assertArtifact(t, d, rounds[0].ID, db.ArtifactPlan, false)
	assertArtifact(t, d, rounds[0].ID, db.ArtifactReport, false)
	assertArtifact(t, d, rounds[0].ID, db.ArtifactDiff, false)
	assertArtifact(t, d, rounds[0].ID, db.ArtifactGateLog, false)
	// r2: plan/report/drift absent.
	assertArtifact(t, d, rounds[1].ID, db.ArtifactPlan, false)
	assertArtifact(t, d, rounds[1].ID, db.ArtifactReport, false)
	assertArtifact(t, d, rounds[1].ID, db.ArtifactDrift, false)
	// r3: plan and report absent.
	assertArtifact(t, d, rounds[2].ID, db.ArtifactPlan, false)
	assertArtifact(t, d, rounds[2].ID, db.ArtifactReport, false)

	// D3b fence item 3: no round transcript row is mirrored any more.
	r1t, err := d.Transcript(db.OwnerRound, rounds[0].ID, 0, 0)
	if err != nil {
		t.Fatalf("Transcript r1: %v", err)
	}
	if len(r1t) != 0 {
		t.Errorf("len(r1 transcript) = %d, want 0", len(r1t))
	}

	r2t, err := d.Transcript(db.OwnerRound, rounds[1].ID, 0, 0)
	if err != nil {
		t.Fatalf("Transcript r2: %v", err)
	}
	if len(r2t) != 0 {
		t.Errorf("len(r2 transcript) = %d, want 0", len(r2t))
	}

	r3t, err := d.Transcript(db.OwnerRound, rounds[2].ID, 0, 0)
	if err != nil {
		t.Fatalf("Transcript r3: %v", err)
	}
	if len(r3t) != 0 {
		t.Errorf("len(r3 transcript) = %d, want 0", len(r3t))
	}
}

// TestIngestLinksEventsToRounds pins the Task 0(a) fix: events are appended
// before any round row exists, so every event.round_id starts out null;
// after ingest, db.Events(bindingID, round) must return exactly that
// round's entries by following round_id, not by re-decoding entry_json
// client-side.
//
// Mutation check: skip the link step (comment out the
// linkEventsToRounds call in Ingest) and this must fail -- every
// db.Events(bindingID, N) call returns zero rows instead of the round's
// entries, since round_id stays null forever.
func TestIngestLinksEventsToRounds(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")

	round2, err := d.Events(b.ID, 2)
	if err != nil {
		t.Fatalf("Events(round 2): %v", err)
	}
	if len(round2) != 3 {
		t.Fatalf("Events(round 2) = %d rows, want 3", len(round2))
	}
	for _, e := range round2 {
		var entry store.LogEntry
		if err := json.Unmarshal([]byte(e.EntryJSON), &entry); err != nil {
			t.Fatalf("unmarshal entry_json: %v", err)
		}
		if entry.Round != 2 {
			t.Errorf("Events(round 2) returned a round %d entry (seq %d)", entry.Round, e.Seq)
		}
		if e.RoundID == nil {
			t.Errorf("seq %d: RoundID is nil, want round 2's id", e.Seq)
		}
	}

	round1, err := d.Events(b.ID, 1)
	if err != nil {
		t.Fatalf("Events(round 1): %v", err)
	}
	if len(round1) != 4 {
		t.Fatalf("Events(round 1) = %d rows, want 4", len(round1))
	}

	round3, err := d.Events(b.ID, 3)
	if err != nil {
		t.Fatalf("Events(round 3): %v", err)
	}
	if len(round3) != 2 {
		t.Fatalf("Events(round 3) = %d rows, want 2", len(round3))
	}
}

func assertArtifact(t *testing.T, d *db.DB, roundID, kind string, wantFound bool) {
	t.Helper()
	_, found, err := d.Artifact(roundID, kind)
	if err != nil {
		t.Fatalf("Artifact(%s): %v", kind, err)
	}
	if found != wantFound {
		t.Errorf("Artifact(%s) found = %v, want %v", kind, found, wantFound)
	}
}

// archiveFixtureAs packs the golden fixture, with bindFile standing in for
// bind.json, into an archived record under a fresh temp state root and returns
// the store and that record's id -- the archive-source counterpart of
// copyFixtureAs (P3d §4.1: an archived binding is a record, not a tarball).
func archiveFixtureAs(t *testing.T, bindFile string) (*store.Store, string) {
	t.Helper()
	root := t.TempDir()
	s := store.New(root)
	if err := os.MkdirAll(s.Dir("fixture"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatalf("ReadDir fixture: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || isBindVariant(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixtureDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(s.Dir("fixture"), e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	bindData, err := os.ReadFile(filepath.Join(fixtureDir, bindFile))
	if err != nil {
		t.Fatalf("read %s: %v", bindFile, err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir("fixture"), "bind.json"), bindData, 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}
	if _, err := s.Archive("fixture"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}
	return s, archived[0].RecordID
}

func TestIngestFixtureArchiveEqualsLive(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	deps := Deps{Now: func() time.Time { return now }}

	liveDir := copyFixture(t)
	liveDB := openTestDB(t)
	if _, err := Ingest(context.Background(), DirSource(liveDir), liveDB, deps); err != nil {
		t.Fatalf("live Ingest: %v", err)
	}

	archiveStore, recordID := archiveFixtureAs(t, "bind.json")

	archiveDB := openTestDB(t)
	if _, err := Ingest(context.Background(), ArchivedSource(archiveStore, recordID), archiveDB, deps); err != nil {
		t.Fatalf("archive Ingest: %v", err)
	}

	liveB := mustBinding(t, liveDB, "fixture")
	archB := mustBinding(t, archiveDB, "fixture")

	if !strPtrEq(liveB.RepoOrigin, archB.RepoOrigin) {
		t.Errorf("RepoOrigin live=%v archive=%v", liveB.RepoOrigin, archB.RepoOrigin)
	}
	if !strPtrEq(liveB.Feature, archB.Feature) {
		t.Errorf("Feature live=%v archive=%v", liveB.Feature, archB.Feature)
	}
	if !strPtrEq(liveB.FinalState, archB.FinalState) {
		t.Errorf("FinalState live=%v archive=%v", liveB.FinalState, archB.FinalState)
	}
	if liveB.CWD != archB.CWD || liveB.BuilderMode != archB.BuilderMode {
		t.Errorf("CWD/BuilderMode mismatch: live=%q/%q archive=%q/%q", liveB.CWD, liveB.BuilderMode, archB.CWD, archB.BuilderMode)
	}
	if !liveB.CreatedAt.Equal(archB.CreatedAt) {
		t.Errorf("CreatedAt live=%v archive=%v", liveB.CreatedAt, archB.CreatedAt)
	}
	if archB.IngestSource != "archive" {
		t.Errorf("archive IngestSource = %q, want archive", archB.IngestSource)
	}
	if archB.ArchivedAt == nil {
		t.Error("archive ArchivedAt is nil, want the archive stamp")
	}
	// An archived record has no tarball behind it any more (P3d §4.5), so
	// ArchivePath carries no path.

	liveRounds := normalizeRounds(mustRounds(t, liveDB, liveB.ID))
	archRounds := normalizeRounds(mustRounds(t, archiveDB, archB.ID))
	if len(liveRounds) != len(archRounds) {
		t.Fatalf("round count live=%d archive=%d", len(liveRounds), len(archRounds))
	}
	for i := range liveRounds {
		lr, ar := liveRounds[i], archRounds[i]
		if lr.Number != ar.Number || lr.Outcome != ar.Outcome || !strPtrEq(lr.ReportOutcome, ar.ReportOutcome) ||
			!strPtrEq(lr.BuilderHarness, ar.BuilderHarness) || !strPtrEq(lr.BuilderProvider, ar.BuilderProvider) ||
			!strPtrEq(lr.BuilderModel, ar.BuilderModel) || !intPtrEq(lr.Commits, ar.Commits) ||
			!strPtrEq(lr.Tree, ar.Tree) || !strPtrEq(lr.GateResult, ar.GateResult) ||
			lr.Switches != ar.Switches {
			t.Errorf("round %d mismatch: live=%+v archive=%+v", lr.Number, lr, ar)
		}
	}

	liveEvents, err := liveDB.Events(liveB.ID, 0)
	if err != nil {
		t.Fatalf("live Events: %v", err)
	}
	archEvents, err := archiveDB.Events(archB.ID, 0)
	if err != nil {
		t.Fatalf("archive Events: %v", err)
	}
	if len(liveEvents) != len(archEvents) {
		t.Fatalf("event count live=%d archive=%d", len(liveEvents), len(archEvents))
	}
	// Compared as decoded entries, not as raw entry_json: the live source
	// here is a directory, whose log.jsonl lines are kept verbatim, while an
	// archived record's log.jsonl is re-encoded from its event rows exactly
	// as StoreSource re-encodes a live one's. The mirror rows are what must
	// agree.
	for i := range liveEvents {
		var le, ae store.LogEntry
		if err := json.Unmarshal([]byte(liveEvents[i].EntryJSON), &le); err != nil {
			t.Fatalf("decode live event %d: %v", i, err)
		}
		if err := json.Unmarshal([]byte(archEvents[i].EntryJSON), &ae); err != nil {
			t.Fatalf("decode archive event %d: %v", i, err)
		}
		if le.Kind != ae.Kind || le.Round != ae.Round || le.Direction != ae.Direction ||
			le.Confirmed != ae.Confirmed || !le.TS.Equal(ae.TS) || le.Path != ae.Path ||
			le.Payload != ae.Payload || le.Outcome != ae.Outcome || le.Tier != ae.Tier ||
			le.Note != ae.Note {
			t.Errorf("event %d mismatch: live=%+v archive=%+v", i, le, ae)
		}
		if liveEvents[i].Seq != archEvents[i].Seq {
			t.Errorf("event %d seq live=%d archive=%d", i, liveEvents[i].Seq, archEvents[i].Seq)
		}
	}

	for i := range liveRounds {
		for _, kind := range []string{db.ArtifactPlan, db.ArtifactReport, db.ArtifactDiff, db.ArtifactDrift} {
			la, lfound, _ := liveDB.Artifact(mustRounds(t, liveDB, liveB.ID)[i].ID, kind)
			aa, afound, _ := archiveDB.Artifact(mustRounds(t, archiveDB, archB.ID)[i].ID, kind)
			if lfound != afound {
				t.Errorf("round %d artifact %s found live=%v archive=%v", i+1, kind, lfound, afound)
				continue
			}
			if lfound && (la.Text != aa.Text || la.SHA256 != aa.SHA256 || la.Bytes != aa.Bytes) {
				t.Errorf("round %d artifact %s content mismatch", i+1, kind)
			}
		}

		lRoundID := mustRounds(t, liveDB, liveB.ID)[i].ID
		aRoundID := mustRounds(t, archiveDB, archB.ID)[i].ID
		lTranscript, _ := liveDB.Transcript(db.OwnerRound, lRoundID, 0, 0)
		aTranscript, _ := archiveDB.Transcript(db.OwnerRound, aRoundID, 0, 0)
		if len(lTranscript) != len(aTranscript) {
			t.Errorf("round %d transcript count live=%d archive=%d", i+1, len(lTranscript), len(aTranscript))
			continue
		}
		for j := range lTranscript {
			if lTranscript[j].RecordJSON != aTranscript[j].RecordJSON || lTranscript[j].Rendered != aTranscript[j].Rendered {
				t.Errorf("round %d transcript row %d mismatch", i+1, j)
			}
		}
	}
}

func normalizeRounds(rounds []db.Round) []db.Round {
	out := make([]db.Round, len(rounds))
	copy(out, rounds)
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

func strPtrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func intPtrEq(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestIngestTwiceIsNoop(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	before, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	stats2, err := Ingest(context.Background(), DirSource(dir), d, Deps{})
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if stats2 != (Stats{}) {
		t.Errorf("second Ingest Stats = %+v, want zero", stats2)
	}

	after, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for tbl, n := range before.Rows {
		if after.Rows[tbl] != n {
			t.Errorf("Rows[%s] changed: before %d, after %d", tbl, n, after.Rows[tbl])
		}
	}
}

func TestIngestAppendsAfterNewRound(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	f, err := os.OpenFile(filepath.Join(dir, "log.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open log.jsonl: %v", err)
	}
	if _, err := f.WriteString(`{"ts":"2026-09-10T10:30:00.000Z","round":4,"direction":"to_builder","kind":"plan","path":"/work/fixture/004-plan.md","confirmed":true,"tier":"high"}` + "\n"); err != nil {
		t.Fatalf("append log entry: %v", err)
	}
	f.Close()
	if err := os.WriteFile(filepath.Join(dir, "004-plan.md"), []byte("round 4 plan"), 0o644); err != nil {
		t.Fatalf("write 004-plan.md: %v", err)
	}

	stats2, err := Ingest(context.Background(), DirSource(dir), d, Deps{})
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	// 004-plan.md is no longer mirrored into an artifact row (D3b fence item 1).
	want := Stats{Bindings: 1, Rounds: 1, Events: 1}
	if stats2 != want {
		t.Errorf("second Ingest Stats = %+v, want %+v", stats2, want)
	}

	b := mustBinding(t, d, "fixture")
	rounds := mustRounds(t, d, b.ID)
	if len(rounds) != 4 {
		t.Fatalf("len(rounds) = %d, want 4", len(rounds))
	}
	if rounds[3].Number != 4 {
		t.Errorf("rounds[3].Number = %d, want 4", rounds[3].Number)
	}
}

// TestIngestNoPhantomRoundFromBindRound pins the round-3, backfill-found
// rule: bind.json's "round" field is the *next* round number after
// finishRound's Round++, so it names a round that has no files and no
// events on a fully finished binding. bind-round4.json is exactly
// binding-three-rounds with "round": 4 while every file and event still
// only names rounds 1-3; Ingest must not manufacture an empty round 4 from
// that field alone.
//
// Mutation check: re-adding `if b.Round > 0 { roundSet[b.Round] = true }`
// to the round union in ingest.go must make this fail with 4 rounds.
func TestIngestNoPhantomRoundFromBindRound(t *testing.T) {
	dir := copyFixtureAs(t, "bind-round4.json")
	d := openTestDB(t)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	rounds := mustRounds(t, d, b.ID)
	if len(rounds) != 3 {
		t.Fatalf("len(rounds) = %d, want 3 (bind.json's round=4 must not create a phantom round)", len(rounds))
	}
	for _, r := range rounds {
		if r.Number == 4 {
			t.Errorf("round 4 exists, want no round from bind.json's round field alone")
		}
	}
}

// mapGitFacts answers RepoFacts per directory, erroring for any dir it was
// not told about -- standing in for a gc'd worktree (RepoFacts fails
// because the dir no longer exists) alongside a source checkout that still
// resolves.
type mapGitFacts map[string]struct{ originURL, commonDir string }

func (m mapGitFacts) RepoFacts(ctx context.Context, dir string) (string, string, error) {
	f, ok := m[dir]
	if !ok {
		return "", "", fmt.Errorf("no such repo: %s", dir)
	}
	return f.originURL, f.commonDir, nil
}

// TestIngestRepoFromSourceCheckoutWhenCWDGone pins the repo-fallback rule:
// when bind.json carries no RepoRef and b.CWD's worktree is gone (the
// archived-binding case -- RepoFacts(b.CWD) fails), Ingest falls back to
// RepoFacts(b.Repo), the pre-existing source-checkout field. Exercised for
// both a live and an archive source, since the old code gated the git
// lookup on kind == "live" and skipped it entirely for archives.
func TestIngestRepoFromSourceCheckoutWhenCWDGone(t *testing.T) {
	deps := Deps{Git: mapGitFacts{
		"/work/source-checkout": {originURL: "git@github.com:o/r.git", commonDir: "/work/source-checkout/.git"},
	}}

	dir := copyFixtureAs(t, "bind-cwd-gone.json")
	d := openTestDB(t)
	if _, err := Ingest(context.Background(), DirSource(dir), d, deps); err != nil {
		t.Fatalf("live Ingest: %v", err)
	}
	b := mustBinding(t, d, "fixture")
	if b.RepoID == nil {
		t.Fatal("live: RepoID is nil, want a repo row resolved from b.Repo (the source checkout) when b.CWD is gone")
	}
	if !strEq(b.RepoOrigin, "https://github.com/o/r") {
		t.Errorf("live: RepoOrigin = %v, want https://github.com/o/r", b.RepoOrigin)
	}

	archiveStore, recordID := archiveFixtureAs(t, "bind-cwd-gone.json")
	archiveDB := openTestDB(t)
	if _, err := Ingest(context.Background(), ArchivedSource(archiveStore, recordID), archiveDB, deps); err != nil {
		t.Fatalf("archive Ingest: %v", err)
	}
	archB := mustBinding(t, archiveDB, "fixture")
	if archB.RepoID == nil {
		t.Fatal("archive: RepoID is nil, want a repo row resolved from b.Repo even for an archive source")
	}
	if !strEq(archB.RepoOrigin, "https://github.com/o/r") {
		t.Errorf("archive: RepoOrigin = %v, want https://github.com/o/r", archB.RepoOrigin)
	}
}

func TestIngestLegacyBindJSON(t *testing.T) {
	dir := copyLegacyFixture(t)
	d := openTestDB(t)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	if b.RepoID != nil {
		t.Errorf("RepoID = %v, want nil (no Git dep, no repo_ref)", b.RepoID)
	}
	if b.Feature != nil {
		t.Errorf("Feature = %v, want nil", b.Feature)
	}

	rounds := mustRounds(t, d, b.ID)
	if len(rounds) != 3 {
		t.Errorf("len(rounds) = %d, want 3", len(rounds))
	}
}

type fakeGitFacts struct {
	originURL, commonDir string
}

func (f fakeGitFacts) RepoFacts(ctx context.Context, dir string) (string, string, error) {
	return f.originURL, f.commonDir, nil
}

func TestIngestResolvesRepoWhenMissing(t *testing.T) {
	dir := copyLegacyFixture(t)
	d := openTestDB(t)

	deps := Deps{Git: fakeGitFacts{originURL: "git@github.com:o/r.git", commonDir: "/work/fixture/.git"}}
	if _, err := Ingest(context.Background(), DirSource(dir), d, deps); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	if b.RepoID == nil {
		t.Fatal("RepoID is nil, want a repo row resolved from GitFacts")
	}
	if !strEq(b.RepoOrigin, "https://github.com/o/r") {
		t.Errorf("RepoOrigin = %v, want the normalised origin", b.RepoOrigin)
	}
}

func TestIngestPlannerTranscript(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)

	sessionPath := filepath.Join(t.TempDir(), "S1.jsonl")
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`
	if err := os.WriteFile(sessionPath, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	fakeSessions := func(kind, sessionID string) (string, bool) {
		if kind == "claude" && sessionID == "S1" {
			return sessionPath, true
		}
		return "", false
	}

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{Sessions: fakeSessions}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	if b.PlannerID == nil {
		t.Fatal("PlannerID is nil")
	}

	recs, err := d.Transcript(db.OwnerPlanner, *b.PlannerID, 0, 0)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len(recs) = %d, want 1", len(recs))
	}
	if recs[0].RecordJSON != line {
		t.Errorf("RecordJSON = %q, want %q", recs[0].RecordJSON, line)
	}
	if recs[0].Rendered == "" {
		t.Error("Rendered is empty, want transcript.RenderRecord's output")
	}
}

// txProbingGitFacts records the RepoFacts call and, from inside it, opens a
// transaction on the same *db.DB Ingest is about to write with -- exactly the
// lock contention #436 is about.
type txProbingGitFacts struct {
	d      *db.DB
	called bool
	txErr  error
}

func (f *txProbingGitFacts) RepoFacts(ctx context.Context, dir string) (string, string, error) {
	f.called = true
	f.txErr = f.d.Tx(func(*db.Tx) error { return nil })
	return "git@github.com:o/r.git", "/work/fixture/.git", nil
}

// TestIngestResolvesGitAndSessionsOutsideTheTransaction pins #436: the git
// facts and the planner-session lookup run before Ingest opens its write
// transaction, so neither holds the db's write lock while it shells out to
// git or searches the disk. Before the fix, each fake's own Tx on the same
// *db.DB hits the busy lock the outer transaction already holds.
func TestIngestResolvesGitAndSessionsOutsideTheTransaction(t *testing.T) {
	dir := copyLegacyFixture(t)
	d := openTestDB(t)

	git := &txProbingGitFacts{d: d}
	sessCalled := false
	var sessTxErr error
	sessions := func(kind, sessionID string) (string, bool) {
		sessCalled = true
		sessTxErr = d.Tx(func(*db.Tx) error { return nil })
		return "", false
	}

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{Git: git, Sessions: sessions}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !git.called {
		t.Error("GitFacts.RepoFacts was not called")
	}
	if !sessCalled {
		t.Error("Sessions was not called")
	}
	if git.txErr != nil {
		t.Errorf("Tx inside GitFacts.RepoFacts: %v, want nil (no write lock held)", git.txErr)
	}
	if sessTxErr != nil {
		t.Errorf("Tx inside Sessions: %v, want nil (no write lock held)", sessTxErr)
	}
}

// TestIngestWritesNoRoundFileMirror pins D3b's new behaviour: ingest no
// longer mirrors round files into `artifact` rows or into round transcript
// rows. Every artifact row that exists must be an answer, and no
// `transcript` row may carry owner_kind='round'.
//
// Mutation: restore the plain round-file artifact loop and this fails.
func TestIngestWritesNoRoundFileMirror(t *testing.T) {
	dir := copyFixture(t)
	d := openTestDB(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	dbStats, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	rounds := mustRounds(t, d, b.ID)
	if len(rounds) == 0 {
		t.Fatal("no rounds ingested, cannot check the mirror")
	}

	// Every artifact kind except answer is a round-file mirror that D3b
	// (fence items 1 and 2) must not write any more.
	nonAnswerKinds := []string{
		db.ArtifactPlan, db.ArtifactReport, db.ArtifactDiff, db.ArtifactDrift,
		db.ArtifactGateLog, db.ArtifactQuestion, db.ArtifactAsk, db.ArtifactFindings,
	}

	answers := 0
	for _, r := range rounds {
		for _, kind := range nonAnswerKinds {
			_, found, err := d.Artifact(r.ID, kind)
			if err != nil {
				t.Fatalf("Artifact(round %d, %s): %v", r.Number, kind, err)
			}
			if found {
				t.Errorf("round %d has a %q artifact row, want only %q rows", r.Number, kind, db.ArtifactAnswer)
			}
		}
		_, found, err := d.Artifact(r.ID, db.ArtifactAnswer)
		if err != nil {
			t.Fatalf("Artifact(round %d, %s): %v", r.Number, db.ArtifactAnswer, err)
		}
		if found {
			answers++
		}

		recs, err := d.Transcript(db.OwnerRound, r.ID, 0, 0)
		if err != nil {
			t.Fatalf("Transcript(round %d): %v", r.Number, err)
		}
		if len(recs) != 0 {
			t.Errorf("round %d has %d owner_kind=%q transcript rows, want 0", r.Number, len(recs), db.OwnerRound)
		}
	}

	// The round-scoped checks miss nothing only if these totals agree: every
	// artifact row is one of the answers just counted, and every transcript
	// row belongs to a planner, not to a round.
	if dbStats.Rows["artifact"] != answers {
		t.Errorf("artifact rows = %d, want %d (one per answer artifact)", dbStats.Rows["artifact"], answers)
	}
	plannerRows := 0
	if b.PlannerID != nil {
		recs, err := d.Transcript(db.OwnerPlanner, *b.PlannerID, 0, 0)
		if err != nil {
			t.Fatalf("Transcript(planner): %v", err)
		}
		plannerRows = len(recs)
	}
	if dbStats.Rows["transcript"] != plannerRows {
		t.Errorf("transcript rows = %d, want %d (planner-owned only)", dbStats.Rows["transcript"], plannerRows)
	}
}

func TestIngestBadBindIsErrSourceAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	d := openTestDB(t)

	before, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	_, err = Ingest(context.Background(), DirSource(dir), d, Deps{})
	if err == nil {
		t.Fatal("Ingest over a directory with no bind.json must fail")
	}

	after, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for tbl, n := range before.Rows {
		if after.Rows[tbl] != n {
			t.Errorf("Rows[%s] changed on a failed Ingest: before %d, after %d", tbl, n, after.Rows[tbl])
		}
	}
}
