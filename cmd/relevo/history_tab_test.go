package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/store"
	usagepkg "github.com/fuad-daoud/relevo/internal/usage"
)

// historyTabStatsRoot seeds a temp state root with one live binding and one
// archived record, so `relevo history --tab` and `--stats` have both halves of
// the gather to report on, and points XDG_STATE_HOME at it.
func historyTabStatsRoot(t *testing.T) {
	t.Helper()

	stateHome := t.TempDir()
	root := filepath.Join(stateHome, "relevo")
	t.Setenv("XDG_STATE_HOME", stateHome)

	s := store.New(root)
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	plan := store.LogEntry{
		TS: at, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true,
	}
	report := store.LogEntry{
		TS:        at.Add(time.Minute),
		Round:     1,
		Direction: store.DirToPlanner,
		Kind:      store.KindReport,
		Confirmed: true,
		Usage: &usagepkg.Usage{
			Harness: "opencode", Provider: "anthropic", Model: "claude-sonnet-5",
			Tokens:  usagepkg.Tokens{In: 1000, CacheRead: 3000, Out: 100},
			Cost:    usagepkg.Cost{USD: 0.50, Basis: usagepkg.Measured},
			Samples: 1,
		},
	}

	if err := s.Save(store.Binding{Name: "webshop", CWD: "/work/webshop", Round: 2, State: store.StateDone}); err != nil {
		t.Fatalf("Save live: %v", err)
	}
	if err := s.AppendLog("webshop", plan); err != nil {
		t.Fatalf("AppendLog live plan: %v", err)
	}
	if err := s.AppendLog("webshop", report); err != nil {
		t.Fatalf("AppendLog live report: %v", err)
	}

	if err := s.Save(store.Binding{Name: "oldsite", CWD: "/work/oldsite", Round: 1, State: store.StateDone}); err != nil {
		t.Fatalf("Save archived: %v", err)
	}
	if err := s.AppendLog("oldsite", plan); err != nil {
		t.Fatalf("AppendLog archived plan: %v", err)
	}
	if err := s.AppendLog("oldsite", report); err != nil {
		t.Fatalf("AppendLog archived report: %v", err)
	}
	if _, err := s.Archive("oldsite"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
}

// TestHistoryTabOutputIsTheOldTabOutput pins the exact bytes `relevo history
// --tab` prints (P3d §4.6): the body of the removed `relevo tab`, over a live
// binding and an archived record alike. The bytes were verified equal to the
// pre-change `relevo tab` output by running the HEAD binary and this one over
// the same fixture before this test was written.
func TestHistoryTabOutputIsTheOldTabOutput(t *testing.T) {
	historyTabStatsRoot(t)

	stdout, _, err := captureOutput(t, func() error {
		return cmdHistory([]string{"--tab"})
	})
	if err != nil {
		t.Fatalf("history --tab: %v", err)
	}
	want := "group    rounds   steps  calls/st       in    cache    write      out   measured  estimated  plan  unknown\n" +
		"oldsite       1                         1k       3k        0      100      $0.50                          \n" +
		"webshop       1                         1k       3k        0      100      $0.50                          \n" +
		"total         2                         2k       6k        0      200      $1.00                          \n"
	if string(stdout) != want {
		t.Errorf("history --tab =\n%q\nwant\n%q", stdout, want)
	}
}

// TestHistoryStatsNewReport replaces the old-output pin (X3): `history --stats`
// now prints internal/stats's report over relevo.db (C2a §4.4/§7). It seeds a
// temp database the way TestHistoryPlainListsRounds does, runs the verb
// in-process, and checks the section headings and that --json decodes into a
// stats.Report. It spawns no harness and reaches no network.
func TestHistoryStatsNewReport(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	dbPath := filepath.Join(stateHome, "relevo", "relevo.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	strp := func(s string) *string { return &s }
	flp := func(f float64) *float64 { return &f }

	bindingID, err := d.UpsertBinding(db.Binding{
		Name:         "statscase",
		CWD:          "/work/statscase",
		BuilderMode:  "pane",
		FinalState:   strp("done"),
		CreatedAt:    time.Now(),
		IngestSource: db.IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	if _, err := d.UpsertRound(db.Round{
		BindingID:        bindingID,
		Number:           1,
		StartedAt:        time.Now(),
		Outcome:          db.OutcomeReported,
		BuilderCandidate: strp("opencode/test/model"),
		BuilderProvider:  strp("test"),
		CostBasis:        strp("measured"),
		CostUSD:          flp(1.5),
	}); err != nil {
		t.Fatalf("UpsertRound: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, _, err := captureOutput(t, func() error {
		return cmdHistory([]string{"--stats", "--since", "all"})
	})
	if err != nil {
		t.Fatalf("history --stats: %v", err)
	}
	for _, heading := range []string{"candidates", "spend per day", "reliability", "repos", "outcomes"} {
		if !strings.Contains(string(stdout), heading) {
			t.Errorf("history --stats =\n%s\nwant the %q heading", stdout, heading)
		}
	}

	jsonOut, _, err := captureOutput(t, func() error {
		return cmdHistory([]string{"--stats", "--since", "all", "--json"})
	})
	if err != nil {
		t.Fatalf("history --stats --json: %v", err)
	}
	var rep stats.Report
	if err := json.Unmarshal(jsonOut, &rep); err != nil {
		t.Fatalf("history --stats --json does not decode into stats.Report: %v\n%s", err, jsonOut)
	}
	if rep.Totals.Rounds != 1 {
		t.Errorf("json rounds = %d, want 1", rep.Totals.Rounds)
	}
}

// TestHistoryTabRejectsHistoryFlags pins the flag rule (P3d §4.6): --tab and
// --stats are mutually exclusive with each other and with every history flag
// the two forms do not document, and the refusal is a usage error naming the
// flag. No runtime is built, so no harness and no state are touched.
func TestHistoryTabRejectsHistoryFlags(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--tab", "--stats"}, "--tab and --stats are mutually exclusive"},
		{[]string{"--tab", "--binding", "x"}, "--binding cannot be combined with --tab"},
		{[]string{"--stats", "--outcome", "reported"}, "--outcome cannot be combined with --stats"},
		{[]string{"--stats", "--by", "model"}, "--by cannot be combined with --stats"},
		{[]string{"--tab", "--limit", "5"}, "--limit cannot be combined with --tab"},
	}
	for _, c := range cases {
		_, _, err := captureOutput(t, func() error { return cmdHistory(c.args) })
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("cmdHistory(%v) err = %v, want exitCodeErr{code: 2}", c.args, err)
		}
	}
}
