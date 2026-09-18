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
