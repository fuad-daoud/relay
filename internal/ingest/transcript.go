package ingest

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/transcript"
)

// tsKeys is the order in which a stream record's timestamp field is tried;
// the first one present and RFC3339-parseable wins.
var tsKeys = []string{"ts", "timestamp", "time"}

// streamTranscriptRecords turns raw builder-stream lines (NNN-builder.jsonl)
// into TranscriptRecord rows starting at startSeq, one per line
// (docs/specs/2026-09-20-persistence-design.md §5.2).
//
// A line that is not a JSON object is garbage: transcript.Render never
// fails and would render it as itself verbatim, but that is not a useful
// transcript row, so it is kept verbatim in RecordJSON with Rendered left
// empty and counted in skipped instead. A JSON object line is rendered
// with transcript.Render and its lines joined with "\n".
func streamTranscriptRecords(kind string, lines [][]byte, startSeq int) (recs []db.TranscriptRecord, skipped int) {
	recs = make([]db.TranscriptRecord, 0, len(lines))
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)

		var obj map[string]any
		isJSONObject := len(trimmed) > 0 && trimmed[0] == '{' && json.Unmarshal(trimmed, &obj) == nil && obj != nil

		var rendered string
		var ts *time.Time
		if isJSONObject {
			rendered = strings.Join(transcript.Render(kind, line), "\n")
			ts = tsFromRecord(obj)
		} else {
			skipped++
		}

		recs = append(recs, db.TranscriptRecord{
			Seq:        startSeq + i,
			TS:         ts,
			RecordJSON: string(line),
			Rendered:   rendered,
		})
	}
	return recs, skipped
}

// tsFromRecord tries obj's known timestamp fields, RFC3339, in tsKeys
// order; nil when none is present or parseable.
func tsFromRecord(obj map[string]any) *time.Time {
	for _, k := range tsKeys {
		s, ok := obj[k].(string)
		if !ok || s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return &t
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return &t
		}
	}
	return nil
}

// logOnlyTranscriptRecords turns NNN-builder.log lines (no NNN-builder.jsonl
// present -- a pane round, or an old archive, #228) into TranscriptRecord
// rows starting at startSeq: already-rendered text, no record behind it.
func logOnlyTranscriptRecords(lines [][]byte, startSeq int) []db.TranscriptRecord {
	recs := make([]db.TranscriptRecord, 0, len(lines))
	for i, line := range lines {
		recs = append(recs, db.TranscriptRecord{
			Seq:      startSeq + i,
			Rendered: string(line),
		})
	}
	return recs
}

// plannerTranscriptRecords turns a planner harness's own session-record
// lines into TranscriptRecord rows starting at startSeq, rendered with
// transcript.RenderRecord (#184). Rendered is often "" for a housekeeping
// record RenderRecord recognises but has nothing to show for -- that is
// not a parse failure, so it is not counted anywhere.
func plannerTranscriptRecords(kind string, lines [][]byte, startSeq int) []db.TranscriptRecord {
	recs := make([]db.TranscriptRecord, 0, len(lines))
	for i, line := range lines {
		recs = append(recs, db.TranscriptRecord{
			Seq:        startSeq + i,
			RecordJSON: string(line),
			Rendered:   strings.Join(transcript.RenderRecord(kind, line), "\n"),
		})
	}
	return recs
}
