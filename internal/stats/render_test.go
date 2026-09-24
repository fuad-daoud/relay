package stats

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/history"
	"github.com/fuad-daoud/relevo/internal/ledger"
)

var updateGolden = flag.Bool("update", false, "update golden files")

// nameIdentity is the name function until A1 lands: a token prints as itself.
func nameIdentity(s string) string { return s }

// goldenReport is the rich fixture: a candidate over five rounds and a plan
// lane, a candidate under five rounds, unrecorded rounds, a day sparkline, a
// limit table and an active gate, repos and features, and every outcome.
func goldenReport() Report {
	utc := func(day, hour int) time.Time {
		return time.Date(2026, time.September, day, hour, 0, 0, 0, time.UTC)
	}
	repo := stStr("github.com/fuad-daoud/relevo")
	f1, f2 := stStr("cockpit"), stStr("stats")

	row := func(day, hour int, outcome string, dur int64, cost *float64, commits *int) db.RoundRow {
		r := db.RoundRow{
			BindingID: "b1", BindingName: "b1", Repo: repo, Feature: f1,
			StartedAt: utc(day, hour), Outcome: outcome,
			BuilderCandidate: stStr("deepseek-v4.1-flash"), BuilderHarness: stStr("opencode"),
			BuilderProvider: stStr("cline-pass"), BuilderModel: stStr("deepseek-v4.1-flash"),
			DurationMS: stI64(dur), Commits: commits, ReportOutcome: stStr("done"),
			InTokens: stI64(1_000_000), OutTokens: stI64(100_000),
		}
		if cost != nil {
			r.CostUSD = cost
			r.CostBasis = stStr("measured")
		}
		return r
	}

	rows := []db.RoundRow{
		row(2, 9, db.OutcomeReported, 600_000, stF64(0.10), stInt(1)),
		row(5, 9, db.OutcomeReported, 1_200_000, stF64(0.20), stInt(2)),
		row(9, 9, db.OutcomeHalted, 1_800_000, stF64(0.30), stInt(0)),
		row(14, 9, db.OutcomeReported, 2_400_000, stF64(0.15), stInt(1)),
		row(20, 9, db.OutcomeReported, 3_000_000, nil, stInt(1)),
		row(24, 9, db.OutcomeReported, 5_400_000, stF64(0.05), stInt(0)),
		// A plan lane: its cost is never summed.
		{
			BindingID: "b2", BindingName: "b2", Repo: repo, Feature: f2, StartedAt: utc(10, 9),
			Outcome: db.OutcomeReported, BuilderCandidate: stStr("gemini-3.8-flash-high"),
			BuilderProvider: stStr("google"), DurationMS: stI64(1_080_000), Commits: stInt(1),
			ReportOutcome: stStr("done"), CostUSD: stF64(9.99), CostBasis: stStr("measured"),
		},
		{
			BindingID: "b2", BindingName: "b2", Repo: repo, Feature: f2, StartedAt: utc(11, 9),
			Outcome: db.OutcomeReported, BuilderCandidate: stStr("gemini-3.8-flash-high"),
			BuilderProvider: stStr("google"), DurationMS: stI64(1_200_000), Commits: stInt(1),
			ReportOutcome: stStr("done"), CostUSD: stF64(9.99), CostBasis: stStr("measured"),
		},
		// Fewer than five rounds.
		{
			BindingID: "b3", BindingName: "b3", Repo: repo, Feature: f1, StartedAt: utc(23, 9),
			Outcome: db.OutcomeOpen, BuilderCandidate: stStr("glm-5.3-flash"),
			BuilderProvider: stStr("zai"), CostUSD: stF64(0.03), CostBasis: stStr("measured"),
			InTokens: stI64(500_000),
		},
		// Unrecorded rounds: no candidate.
		{
			BindingID: "b4", BindingName: "b4", StartedAt: utc(23, 8),
			Outcome: db.OutcomeReported, ReportOutcome: stStr("unstructured"),
			InTokens: stI64(100_000),
		},
		{
			BindingID: "b4", BindingName: "b4", StartedAt: utc(22, 8),
			Outcome: db.OutcomeReported, InTokens: stI64(100_000),
		},
	}
	// Switches: two rounds switched, four switches between them.
	rows[0].Switches = 1
	rows[2].Switches = 2
	rows[2].ReportOutcome = stStr("halted")
	rows[8].Switches = 1

	hist := history.History{Events: []history.Event{
		{At: utc(24, 1), Kind: ledger.RateLimited, Provider: "google"},
		{At: utc(24, 2), Kind: ledger.RateLimited, Provider: "google"},
		{At: utc(23, 5), Kind: ledger.RateLimited, Provider: "openai"},
		{At: utc(24, 10), Kind: ledger.SpawnFailed, Provider: "google", Token: "gemini-3.8-flash-high"},
	}}
	gates := []ledger.Gate{{
		Token: "gemini-3.8-flash-high", Kind: ledger.RateLimited, Since: utc(24, 1),
		Until: time.Date(2026, time.October, 19, 22, 16, 0, 0, time.UTC),
	}}
	ttft := func(tok string) (int64, bool) {
		switch tok {
		case "deepseek-v4.1-flash":
			return 2500, true
		case "glm-5.3-flash":
			return 2400, true
		}
		return 0, false
	}

	return Build(Inputs{
		Rows:    rows,
		Landed:  map[string]bool{"b1": true},
		History: hist,
		Gates:   gates,
		TTFT:    ttft,
		IsPlan:  func(tok string) bool { return tok == "gemini-3.8-flash-high" },
		Since:   utc(1, 0),
		Until:   utc(24, 12),
		Loc:     time.UTC,
	})
}

// TestRenderGolden pins the whole report's bytes against testdata/report.golden
// (C2a plan §7). Regenerate it with `go test ./internal/stats/ -run Golden
// -update`, then read it and check it against the plan's §4.4.
func TestRenderGolden(t *testing.T) {
	got := Render(goldenReport(), nameIdentity)
	path := filepath.Join("testdata", "report.golden")

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

// TestRenderEmpty pins that an empty report is its first line plus the
// no-rounds line, and nothing else.
func TestRenderEmpty(t *testing.T) {
	rep := Build(Inputs{Until: stNow, Loc: time.UTC})
	got := Render(rep, nameIdentity)
	if !strings.Contains(got, "no rounds in this window") {
		t.Fatalf("empty render = %q, want the no-rounds line", got)
	}
	if lines := strings.Split(strings.TrimRight(got, "\n"), "\n"); len(lines) != 2 {
		t.Errorf("empty render has %d lines, want 2:\n%s", len(lines), got)
	}
}
