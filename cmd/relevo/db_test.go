package main

import (
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
)

// TestFormatStats pins the exact lines `relevo db stats` prints over a
// literal db.Stats, never opening a database: CI launches no harness and this must
// not execute a subcommand that reaches it (it doesn't -- formatStats is a
// pure function).
func TestFormatStats(t *testing.T) {
	newest := time.Date(2026, 9, 15, 14, 2, 0, 0, time.UTC)
	s := db.Stats{
		Version:   1,
		SizeBytes: 1536,
		Rows: map[string]int{
			"repo":          2,
			"planner":       1,
			"binding":       3,
			"round":         6,
			"event":         12,
			"artifact":      4,
			"transcript":    0,
			"ingest_cursor": 1,
		},
		NewestRound: &newest,
	}

	want := "artifact  4\n" +
		"binding  3\n" +
		"event  12\n" +
		"ingest_cursor  1\n" +
		"planner  1\n" +
		"repo  2\n" +
		"round  6\n" +
		"transcript  0\n" +
		"size 1.5 KiB  version 1  newest round 2026-09-15T14:02:00Z\n"

	got := formatStats(s)
	if got != want {
		t.Errorf("formatStats() =\n%q\nwant\n%q", got, want)
	}
}

// TestFormatStatsEmptyNewest pins the "-" placeholder when no round exists
// yet, e.g. right after `relevo db migrate` on a fresh database.
func TestFormatStatsEmptyNewest(t *testing.T) {
	s := db.Stats{
		Version: 1,
		Rows: map[string]int{
			"repo": 0, "planner": 0, "binding": 0, "round": 0,
			"event": 0, "artifact": 0, "transcript": 0, "ingest_cursor": 0,
		},
	}

	want := "artifact  0\n" +
		"binding  0\n" +
		"event  0\n" +
		"ingest_cursor  0\n" +
		"planner  0\n" +
		"repo  0\n" +
		"round  0\n" +
		"transcript  0\n" +
		"size 0 B  version 1  newest round -\n"

	got := formatStats(s)
	if got != want {
		t.Errorf("formatStats() =\n%q\nwant\n%q", got, want)
	}
}

// TestFormatBackfillLine pins `relevo db backfill`'s per-source lines as
// pure functions of a literal ingest.Stats, never running the subcommand:
// CI launches no harness, and backfill's own loop reaches store.Store and
// internal/relevo's runtime, so only the formatters are tested here.
func TestFormatBackfillLine(t *testing.T) {
	stats := ingest.Stats{Rounds: 3, Events: 9, Artifacts: 7, TranscriptRecords: 9, Skipped: 1}

	got := formatBackfillLine("", "fixture", "2026-09-15T14:02:00Z", stats)
	want := "fixture  2026-09-15T14:02:00Z  rounds 3 events 9 artifacts 7 transcript 9\n"
	if got != want {
		t.Errorf("formatBackfillLine() = %q, want %q", got, want)
	}

	gotLive := formatBackfillLine("", "fixture", "-", stats)
	wantLive := "fixture  -  rounds 3 events 9 artifacts 7 transcript 9\n"
	if gotLive != wantLive {
		t.Errorf("formatBackfillLine() (live) = %q, want %q", gotLive, wantLive)
	}

	gotDry := formatBackfillLine("would ", "fixture", "-", stats)
	wantDry := "would fixture  -  rounds 3 events 9 artifacts 7 transcript 9\n"
	if gotDry != wantDry {
		t.Errorf("formatBackfillLine() (dry-run) = %q, want %q", gotDry, wantDry)
	}
}

// TestFormatBackfillFailure pins the "<name>  FAILED: <err>" line, prefixed
// under --dry-run the same way formatBackfillLine is.
func TestFormatBackfillFailure(t *testing.T) {
	err := errors.New("tarball unreadable")

	got := formatBackfillFailure("", "fixture", err)
	want := "fixture  FAILED: tarball unreadable\n"
	if got != want {
		t.Errorf("formatBackfillFailure() = %q, want %q", got, want)
	}

	gotDry := formatBackfillFailure("would ", "fixture", err)
	wantDry := "would fixture  FAILED: tarball unreadable\n"
	if gotDry != wantDry {
		t.Errorf("formatBackfillFailure() (dry-run) = %q, want %q", gotDry, wantDry)
	}
}
