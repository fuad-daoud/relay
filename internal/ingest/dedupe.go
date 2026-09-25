package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/store"
)

// DedupeStats summarises one DedupeMirrorOnce run. It is also the JSON
// document the run leaves under the kv key `mirror-dedupe.v2`, which is what
// keeps the removal a one-time event.
type DedupeStats struct {
	// DoneAt is when the run finished.
	DoneAt time.Time `json:"done_at"`
	// MirrorBindings is how many mirror bindings were examined.
	MirrorBindings int `json:"mirror_bindings"`
	// Unmapped is how many of them had no unique record, so every row of
	// theirs was kept.
	Unmapped int `json:"unmapped"`
	// ArtifactsDeleted is how many artifact rows were removed.
	ArtifactsDeleted int `json:"artifacts_deleted"`
	// ArtifactsKept is how many examined artifact rows were not proven
	// duplicate, an answer included.
	ArtifactsKept int `json:"artifacts_kept"`
	// TranscriptRoundsDeleted is how many rounds had their transcript rows
	// removed.
	TranscriptRoundsDeleted int `json:"transcript_rounds_deleted"`
	// TranscriptRowsDeleted is how many transcript rows that was.
	TranscriptRowsDeleted int `json:"transcript_rows_deleted"`
	// TranscriptRoundsKept is how many rounds had rows that were not proven
	// duplicate.
	TranscriptRoundsKept int `json:"transcript_rounds_kept"`
	// TranscriptRoundsByStreamLines is how many of TranscriptRoundsDeleted
	// were proven by the stream-lines rule rather than exact re-derivation.
	TranscriptRoundsByStreamLines int `json:"transcript_rounds_by_stream_lines"`
	// TranscriptRowsRenamed is how many rows of those rounds matched a stream
	// line only after the rename rewrite.
	TranscriptRowsRenamed int `json:"transcript_rows_renamed"`
	// BackupPath is where the pre-run backup was written; empty when nothing
	// was deleted, so no backup was taken.
	BackupPath string `json:"backup_path"`
	// VacuumErr is empty on success. A VACUUM failure is not fatal: the rows
	// are gone either way, the file is just not compacted.
	VacuumErr string `json:"vacuum_err"`
}

// dedupePlan is what DedupeMirror decides: the rows to remove, plus the
// counts a DedupeStats reports. It is read-only work -- nothing is deleted
// until DedupeMirrorOnce applies it, and only after a backup.
type dedupePlan struct {
	// artifactIDs are the artifact rows proven duplicate of a round_file.
	artifactIDs []string
	// transcriptOwners are the mirror round ids whose transcript rows were
	// reproduced exactly from the record.
	transcriptOwners []string
	stats            DedupeStats
}

// dedupeArtifactSuffix is the round-file name each artifact kind's content is
// sealed under, after the round's "%03d-" prefix. answer is deliberately
// absent: it comes from a log payload, has no round file, and is always kept.
var dedupeArtifactSuffix = map[string]string{
	db.ArtifactPlan:     "plan.md",
	db.ArtifactReport:   "report.md",
	db.ArtifactDiff:     "diff.patch",
	db.ArtifactDrift:    "drift.patch",
	db.ArtifactQuestion: "question.md",
}

// dedupeArtifactKinds is every artifact kind DedupeMirror examines, in the
// order it examines them: the kinds with a round-file counterpart, plus
// answer. Other kinds (gate_log, ask, findings) are neither deleted nor
// counted; ask and findings carry a consult id, which db.Artifact's
// empty-consult-id filter already excludes.
var dedupeArtifactKinds = []string{
	db.ArtifactPlan,
	db.ArtifactReport,
	db.ArtifactDiff,
	db.ArtifactDrift,
	db.ArtifactQuestion,
	db.ArtifactAnswer,
}

// dedupeRoundFileBase is the round-file basename kind's content is sealed
// under in a round numbered number -- "003-report.md" -- or "" when the kind
// has no round file at all.
func dedupeRoundFileBase(kind string, number int) string {
	suffix, ok := dedupeArtifactSuffix[kind]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%03d-", number) + suffix
}

// DedupeMirror builds the one-time removal plan for d's ingest mirror,
// read-only: it changes nothing, and it is DedupeMirrorOnce that applies it.
//
// A mirror binding maps to a record when exactly one of the local owner's
// records -- live or archived -- has the same name and the same created_at.
// Zero matches and more than one match both leave the binding unmapped, and
// an unmapped binding keeps every row. A mapped binding's artifact row is a
// duplicate when the record's round file for it exists, holds the same byte
// count, and hashes to the artifact's own sha256; its transcript is a
// duplicate when either re-deriving the transcript from the record's round
// file reproduces every row exactly, or every row's record JSON is a line of
// the record's sealed builder stream -- after applying renames to the row
// when the exact line is not found. Planner transcripts are never examined.
//
// renames are the substitutions a cutover applied to the sealed stream files,
// so a row whose stored path predates the rewrite still matches its rewritten
// line. nil is allowed.
func DedupeMirror(d *db.DB, renames []legacy.Prefix) (dedupePlan, error) {
	var plan dedupePlan

	// Filter{}'s Archived is nil, which queryBindings reads as "no
	// constraint": this lists every mirror binding, archived ones included.
	bindings, err := d.Bindings(db.Filter{})
	if err != nil {
		return dedupePlan{}, fmt.Errorf("dedupe: bindings: %w", err)
	}

	live, err := d.RecordList("")
	if err != nil {
		return dedupePlan{}, fmt.Errorf("dedupe: records: %w", err)
	}
	archived, err := d.RecordListArchived("")
	if err != nil {
		return dedupePlan{}, fmt.Errorf("dedupe: archived records: %w", err)
	}
	records := append(live, archived...)

	for _, b := range bindings {
		plan.stats.MirrorBindings++
		record, ok := dedupeRecord(b, records)
		if !ok {
			plan.stats.Unmapped++
			continue
		}
		if err := planBinding(&plan, d, b, record, renames); err != nil {
			return dedupePlan{}, err
		}
	}

	return plan, nil
}

// dedupeRecord returns the one record that maps to b, and false when zero
// records or more than one do. The caller's records already hold the local
// owner's live and archived rows.
func dedupeRecord(b db.BindingRow, records []db.Record) (db.Record, bool) {
	var match db.Record
	found := 0
	for _, r := range records {
		if r.Name == b.Name && r.CreatedAt.Equal(b.CreatedAt) {
			match = r
			found++
		}
	}
	if found != 1 {
		return db.Record{}, false
	}
	return match, true
}

// planBinding adds every row of b that is a proven duplicate of record.
func planBinding(plan *dedupePlan, d *db.DB, b db.BindingRow, record db.Record, renames []legacy.Prefix) error {
	rounds, err := d.Rounds(b.ID)
	if err != nil {
		return fmt.Errorf("dedupe: rounds of %s: %w", b.Name, err)
	}
	for _, rd := range rounds {
		if err := planArtifacts(plan, d, record, rd); err != nil {
			return err
		}
		if err := planTranscript(plan, d, b, record, rd, renames); err != nil {
			return err
		}
	}
	return nil
}

// planArtifacts applies the artifact identity rule to every kind DedupeMirror
// examines of one round.
func planArtifacts(plan *dedupePlan, d *db.DB, record db.Record, rd db.Round) error {
	for _, kind := range dedupeArtifactKinds {
		a, ok, err := d.Artifact(rd.ID, kind)
		if err != nil {
			return fmt.Errorf("dedupe: artifact %s/%s: %w", record.Name, kind, err)
		}
		if !ok {
			continue
		}

		duplicate := false
		if base := dedupeRoundFileBase(kind, rd.Number); base != "" {
			body, _, found, ferr := d.RoundFileGet(record.ID, base)
			if ferr != nil {
				return fmt.Errorf("dedupe: round file %s/%s: %w", record.Name, base, ferr)
			}
			duplicate = found && sha256Hex(body) == a.SHA256 && int64(len(body)) == a.Bytes
		}

		if duplicate {
			plan.artifactIDs = append(plan.artifactIDs, a.ID)
			plan.stats.ArtifactsDeleted++
		} else {
			plan.stats.ArtifactsKept++
		}
	}
	return nil
}

// planTranscript applies the transcript identity rule to one mirror round: if
// the round has transcript rows at all, it plans their removal when a
// re-derivation from the record reproduces every row exactly, and otherwise
// when every row's record JSON is a line of the record's sealed builder
// stream, with renames applied to a row whose stored path predates a cutover.
func planTranscript(plan *dedupePlan, d *db.DB, b db.BindingRow, record db.Record, rd db.Round, renames []legacy.Prefix) error {
	rows, err := d.Transcript(db.OwnerRound, rd.ID, 0, 0)
	if err != nil {
		return fmt.Errorf("dedupe: transcript of %s round %d: %w", b.Name, rd.Number, err)
	}
	if len(rows) == 0 {
		return nil
	}

	candidates, err := deriveTranscripts(d, record, rd)
	if err != nil {
		return err
	}
	for _, derived := range candidates {
		if transcriptRowsEqual(rows, derived) {
			plan.transcriptOwners = append(plan.transcriptOwners, rd.ID)
			plan.stats.TranscriptRoundsDeleted++
			plan.stats.TranscriptRowsDeleted += len(rows)
			return nil
		}
	}

	covered, renamed, err := streamLinesCover(d, record, rd, rows, renames)
	if err != nil {
		return err
	}
	if covered {
		plan.transcriptOwners = append(plan.transcriptOwners, rd.ID)
		plan.stats.TranscriptRoundsDeleted++
		plan.stats.TranscriptRowsDeleted += len(rows)
		plan.stats.TranscriptRoundsByStreamLines++
		plan.stats.TranscriptRowsRenamed += renamed
		return nil
	}

	plan.stats.TranscriptRoundsKept++
	return nil
}

// streamLinesCover reports whether every row's record JSON is a line of the
// record's sealed builder stream, and how many rows matched only after the
// rename rewrite. It is the looser second proof planTranscript falls back to
// when no exact re-derivation fits: rendered and ts are derived from the
// record JSON, and seq from the line's order, so such a row holds nothing the
// stream does not. An empty RecordJSON is never vouched for -- a log-derived
// row has no record behind it. rows must be non-empty.
func streamLinesCover(d *db.DB, record db.Record, rd db.Round, rows []db.TranscriptRecord, renames []legacy.Prefix) (covered bool, renamed int, err error) {
	base := builderStreamPathBase(rd.Number)
	body, _, found, err := d.RoundFileGet(record.ID, base)
	if err != nil {
		return false, 0, fmt.Errorf("dedupe: round file %s/%s: %w", record.Name, base, err)
	}
	if !found {
		return false, 0, nil
	}
	lines, lerr := roundFileLines(base, body)
	if lerr != nil {
		return false, 0, fmt.Errorf("dedupe: split %s/%s: %w", record.Name, base, lerr)
	}
	set := make(map[string]bool, len(lines))
	for _, line := range lines {
		set[string(line)] = true
	}

	for _, row := range rows {
		if row.RecordJSON == "" {
			return false, 0, nil
		}
		if set[row.RecordJSON] {
			continue
		}
		if len(renames) == 0 {
			return false, 0, nil
		}
		rewritten := string(legacy.RewriteJSON([]byte(row.RecordJSON), renames))
		if rewritten == row.RecordJSON || !set[rewritten] {
			return false, 0, nil
		}
		renamed++
	}
	return true, renamed, nil
}

// Kept here since D3b removed ingest.go's round-file mirror, their former caller.
func builderLogPathBase(round int) string {
	return filepath.Base(memberStore.BuilderLogPath("x", round))
}
func builderStreamPathBase(round int) string {
	return filepath.Base(memberStore.BuilderStreamPath("x", round))
}

// deriveTranscripts re-derives a mirror round's transcript rows from the
// record's own sealed round files, returning every candidate the identity
// rule allows: the transcript is a duplicate when either source reproduces
// the rows exactly.
//
// The stream (NNN-builder.jsonl) is tried first, split exactly as Ingest
// splits it and rendered under each candidate kind (transcriptKinds). The log
// (NNN-builder.log) is tried as well whenever it is present -- already-rendered
// text with no record behind it -- so a round whose stored rows came from the
// log is recognised even when the record also holds the stream. Neither file
// present means no candidate at all.
func deriveTranscripts(d *db.DB, record db.Record, rd db.Round) ([][]db.TranscriptRecord, error) {
	var out [][]db.TranscriptRecord

	streamBase := builderStreamPathBase(rd.Number)
	body, _, found, err := d.RoundFileGet(record.ID, streamBase)
	if err != nil {
		return nil, fmt.Errorf("dedupe: round file %s/%s: %w", record.Name, streamBase, err)
	}
	if found {
		lines, lerr := roundFileLines(streamBase, body)
		if lerr != nil {
			return nil, fmt.Errorf("dedupe: split %s/%s: %w", record.Name, streamBase, lerr)
		}
		kinds := transcriptKinds(record, rd)
		out = make([][]db.TranscriptRecord, 0, len(kinds)+1)
		for _, kind := range kinds {
			recs, _ := streamTranscriptRecords(kind, lines, 0)
			out = append(out, recs)
		}
	}

	logBase := builderLogPathBase(rd.Number)
	body, _, found, err = d.RoundFileGet(record.ID, logBase)
	if err != nil {
		return nil, fmt.Errorf("dedupe: round file %s/%s: %w", record.Name, logBase, err)
	}
	if found {
		lines, lerr := roundFileLines(logBase, body)
		if lerr != nil {
			return nil, fmt.Errorf("dedupe: split %s/%s: %w", record.Name, logBase, lerr)
		}
		out = append(out, logOnlyTranscriptRecords(lines, 0))
	}

	return out, nil
}

// transcriptKinds is the harness kind each stream candidate is rendered
// under, in order: the mirror round's own BuilderHarness when it has one,
// then the record's decoded store.Binding.Builder.Kind. A repeated kind is
// dropped, so the same derivation is never tried twice. A record whose JSON
// does not decode to a Binding contributes no kind of its own -- the harness
// candidate still stands -- rather than failing a read-only plan for one
// unreadable record.
func transcriptKinds(record db.Record, rd db.Round) []string {
	kinds := make([]string, 0, 2)
	seen := make(map[string]bool, 2)
	add := func(kind string) {
		if seen[kind] {
			return
		}
		seen[kind] = true
		kinds = append(kinds, kind)
	}

	if rd.BuilderHarness != nil {
		add(*rd.BuilderHarness)
	}
	var b store.Binding
	if err := json.Unmarshal([]byte(record.JSON), &b); err == nil {
		add(b.Builder.Kind)
	}
	return kinds
}

// roundFileLines splits a sealed round file's body into complete lines exactly
// as Ingest's readAppendOnly splits a live member: a trailing partial line is
// left for a next read, and every returned line has no newline. It calls
// readAppendOnly itself, over an in-memory opener, rather than shipping a
// second splitter that could drift from the reader it must agree with.
func roundFileLines(name string, body []byte) ([][]byte, error) {
	opener := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	lines, _, _, _, err := readAppendOnly(opener, name, db.Cursor{}, false)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// transcriptRowsEqual reports whether two transcript row lists are the same
// rows: the same count, and per index the same seq, record JSON, rendered
// text and timestamp. Ids are ignored -- ingest mints a fresh ULID per row.
func transcriptRowsEqual(want, got []db.TranscriptRecord) bool {
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i].Seq != got[i].Seq ||
			want[i].RecordJSON != got[i].RecordJSON ||
			want[i].Rendered != got[i].Rendered ||
			!timePtrEqual(want[i].TS, got[i].TS) {
			return false
		}
	}
	return true
}

// timePtrEqual compares two optional timestamps without dereferencing a nil.
func timePtrEqual(a, b *time.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Equal(*b)
	}
}

// dedupeKVKey is the kv key a finished removal is recorded under. Its presence
// is what makes the removal one-time: the key appears only once a run has
// either deleted everything it planned or found nothing to delete, so a run
// that fails on the backup leaves no key and the next daemon start retries.
//
// v1 was the exact-derivation pass (#422); v2 adds the stream-lines proof
// (#476). A v1 row is left in kv as history and does not stop v2.
const dedupeKVKey = "mirror-dedupe.v2"

// DedupeMirrorOnce removes the mirror rows DedupeMirror proves duplicate of a
// round_file, once per database and only after a full backup.
//
// It is the daemon's start-up call, so it never half-does the work:
//
//  1. an existing mirror-dedupe.v2 kv row means an earlier run finished:
//     nothing happens and ran is false.
//  2. the plan comes from DedupeMirror, which changes nothing.
//  3. a plan that deletes nothing writes the stats and returns ran true, with
//     no backup: there is nothing to be able to undo.
//  4. otherwise the whole database is backed up under backupDir first. A
//     backup failure returns the error with no deletes and no kv row.
//  5. one transaction deletes every planned artifact and every planned
//     round's transcript rows, then writes the stats. A failure rolls the
//     transaction back, so the deletes and the kv row go together.
//  6. VACUUM compacts the file; its failure is recorded in stats.VacuumErr and
//     is not returned, because the rows are gone either way.
//
// renames is passed through to DedupeMirror.
func DedupeMirrorOnce(d *db.DB, backupDir string, renames []legacy.Prefix, now time.Time) (stats DedupeStats, ran bool, err error) {
	if _, ok, kerr := d.KVGet(dedupeKVKey); kerr != nil {
		return DedupeStats{}, false, fmt.Errorf("dedupe: kv get %s: %w", dedupeKVKey, kerr)
	} else if ok {
		return DedupeStats{}, false, nil
	}

	plan, err := DedupeMirror(d, renames)
	if err != nil {
		return DedupeStats{}, false, err
	}
	stats = plan.stats
	stats.DoneAt = now

	if len(plan.artifactIDs) == 0 && len(plan.transcriptOwners) == 0 {
		if err := putDedupeStats(d, stats); err != nil {
			return DedupeStats{}, false, err
		}
		return stats, true, nil
	}

	backupPath := filepath.Join(backupDir, "relevo.db.pre-dedupe-"+now.UTC().Format("20060102-150405"))
	if berr := d.BackupTo(backupPath); berr != nil {
		return DedupeStats{}, false, berr
	}
	stats.BackupPath = backupPath

	if err := d.Tx(func(tx *db.Tx) error {
		for _, id := range plan.artifactIDs {
			if derr := tx.DeleteArtifact(id); derr != nil {
				return derr
			}
		}
		for _, owner := range plan.transcriptOwners {
			if _, derr := tx.DeleteRoundTranscript(owner); derr != nil {
				return derr
			}
		}
		value, merr := json.Marshal(stats)
		if merr != nil {
			return fmt.Errorf("dedupe: marshal stats: %w", merr)
		}
		return tx.KVPut(dedupeKVKey, value)
	}); err != nil {
		return DedupeStats{}, false, err
	}

	if verr := d.Vacuum(); verr != nil {
		stats.VacuumErr = verr.Error()
	}
	return stats, true, nil
}

// putDedupeStats writes a finished run's document under the kv key, the one
// write that records the removal as done.
func putDedupeStats(d *db.DB, stats DedupeStats) error {
	value, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("dedupe: marshal stats: %w", err)
	}
	if err := d.KVPut(dedupeKVKey, value); err != nil {
		return fmt.Errorf("dedupe: kv put %s: %w", dedupeKVKey, err)
	}
	return nil
}
