package relay

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

func TestLogLineWithoutUsageIsTodaysFormat(t *testing.T) {
	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	e := store.LogEntry{TS: ts, Round: 4, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/p/004-report.md", Note: "scraped"}
	want := ts.Local().Format("2006-01-02 15:04:05") + "  round 4   to_planner report    /p/004-report.md scraped"
	if got := LogLine(e); got != want {
		t.Errorf("\n got  %q\n want %q", got, want)
	}
	if strings.Contains(LogLine(e), "\n") {
		t.Error("no second line without usage")
	}
}

func TestLogLineWithUsageAddsSecondLine(t *testing.T) {
	e := store.LogEntry{TS: time.Now(), Round: 4, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/p/004-report.md",
		Usage: &usage.Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 60_000,
			Tokens: usage.Tokens{In: 100, Out: 10}, Cost: usage.Cost{USD: 0.5, Basis: usage.Measured}, Samples: 1}}
	got := LogLine(e)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("want two lines, got %q", got)
	}
	if !strings.HasPrefix(lines[1], strings.Repeat(" ", 21)+"⎿ ") {
		t.Errorf("second line must be indented under the round column: %q", lines[1])
	}
	if !strings.HasSuffix(lines[1], usage.Line(*e.Usage)) {
		t.Errorf("second line must end with usage.Line: %q", lines[1])
	}
}

func TestLogLineLateSuffix(t *testing.T) {
	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	base := store.LogEntry{TS: ts, Round: 4, Direction: store.DirToBuilder, Kind: store.KindPlan, Path: "/p/004-report.md", Note: "nudge"}
	notLate := base
	notLate.Late = false
	late := base
	late.Late = true

	wantNotLate := ts.Local().Format("2006-01-02 15:04:05") + "  round 4   to_builder plan      /p/004-report.md nudge"
	if got := LogLine(notLate); got != wantNotLate {
		t.Errorf("\n got  %q\n want %q", got, wantNotLate)
	}

	gotLate := LogLine(late)
	wantLate := wantNotLate + " late"
	if gotLate != wantLate {
		t.Errorf("\n got  %q\n want %q", gotLate, wantLate)
	}
	if !strings.HasSuffix(gotLate, "nudge late") {
		t.Errorf("expected suffix %q, got %q", "nudge late", gotLate)
	}
}

func TestLogLineOutcomeAndFlagged(t *testing.T) {
	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	e := store.LogEntry{
		TS:        ts,
		Round:     1,
		Direction: store.DirToPlanner,
		Kind:      store.KindReport,
		Path:      "/p/001-report.md",
		Note:      "noreport",
		Outcome:   "halted",
		Flagged:   2,
		Late:      true,
	}
	got := LogLine(e)
	want := ts.Local().Format("2006-01-02 15:04:05") + "  round 1   to_planner report    /p/001-report.md noreport outcome=halted flagged=2 late"
	if got != want {
		t.Errorf("\n got  %q\n want %q", got, want)
	}
}

func TestLogLineClassify(t *testing.T) {
	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	e := store.LogEntry{
		TS:        ts,
		Round:     1,
		Direction: store.DirToPlanner,
		Kind:      store.KindReport,
		Path:      "/p/001-report.md",
		Flagged:   3,
		FlaggedBy: "both",
		Classify:  &store.ClassifyRecord{Max: 0.94, Partial: true},
		Late:      true,
	}
	got := LogLine(e)
	wantSuffix := "flagged=3 by=both p=0.94 partial late"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("got %q, want suffix %q", got, wantSuffix)
	}

	eTimeout := store.LogEntry{
		TS:        ts,
		Round:     1,
		Direction: store.DirToPlanner,
		Kind:      store.KindReport,
		Path:      "/p/001-report.md",
		Flagged:   3,
		FlaggedBy: "regex",
		Classify:  &store.ClassifyRecord{Note: "classify: timeout"},
	}
	gotTimeout := LogLine(eTimeout)
	if strings.Contains(gotTimeout, "p=") {
		t.Errorf("got %q, want no p=", gotTimeout)
	}
}
