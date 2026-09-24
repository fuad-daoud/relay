package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
)

// TestHistoryUsageFlagsConflict pins the two usage errors `relevo history`
// rejects before ever touching the database: --archived with --live
// together, and an --outcome outside db's enum. validateHistoryFlags is a
// pure function, so this never executes the subcommand -- CI launches no harness.
func TestHistoryUsageFlagsConflict(t *testing.T) {
	if err := validateHistoryFlags(false, false, ""); err != nil {
		t.Errorf("validateHistoryFlags(false, false, \"\") = %v, want nil", err)
	}
	if err := validateHistoryFlags(true, false, "reported"); err != nil {
		t.Errorf("validateHistoryFlags(true, false, \"reported\") = %v, want nil", err)
	}
	if err := validateHistoryFlags(true, true, ""); err == nil {
		t.Error("validateHistoryFlags(true, true, \"\") = nil, want an error (--archived and --live conflict)")
	}
	if err := validateHistoryFlags(false, false, "not-a-real-outcome"); err == nil {
		t.Error(`validateHistoryFlags(false, false, "not-a-real-outcome") = nil, want an error`)
	}
}

// TestValidateHistoryBy pins that --by accepts exactly the ten histq axes
// and that its error lists them all.
func TestValidateHistoryBy(t *testing.T) {
	if err := validateHistoryBy(""); err != nil {
		t.Errorf(`validateHistoryBy("") = %v, want nil`, err)
	}
	for _, a := range histq.Axes() {
		if err := validateHistoryBy(string(a)); err != nil {
			t.Errorf("validateHistoryBy(%q) = %v, want nil", a, err)
		}
	}
	for _, bad := range []string{"nope", "Builders"} {
		err := validateHistoryBy(bad)
		if err == nil {
			t.Errorf("validateHistoryBy(%q) = nil, want an error", bad)
			continue
		}
		for _, a := range histq.Axes() {
			if !strings.Contains(err.Error(), string(a)) {
				t.Errorf("validateHistoryBy(%q) error %q does not list axis %q", bad, err.Error(), a)
			}
		}
	}
}

// TestHistoryAxis pins the defensive axis resolution cmdHistory uses: a
// parsed query that names no axis ("") reads as AxisNone, so no path can turn
// a plain `relevo history` into a regroup and print "no rounds", while a real
// by: query still regroups. Pure, so this never runs the subcommand and never
// reaches a harness.
func TestHistoryAxis(t *testing.T) {
	cases := []struct {
		name   string
		parsed histq.Query
		by     string
		want   histq.Axis
	}{
		{"zero query, no --by", histq.Query{}, "", histq.AxisNone},
		{"AxisNone query, no --by", histq.Query{By: histq.AxisNone}, "", histq.AxisNone},
		{"AxisBuilder query, no --by", histq.Query{By: histq.AxisBuilder}, "", histq.AxisBuilder},
		{"zero query, --by binding", histq.Query{}, "binding", histq.AxisBinding},
		{"AxisBuilder query, --by binding", histq.Query{By: histq.AxisBuilder}, "binding", histq.AxisBinding},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := historyAxis(c.parsed, c.by); got != c.want {
				t.Errorf("historyAxis(%+v, %q) = %q, want %q", c.parsed, c.by, got, c.want)
			}
		})
	}
}

// TestHistoryPlainListsRounds pins the bug this round fixes: `relevo history`
// without -q printed "no rounds" however many rows relevo.db held, because
// Filter left the parsed query's By as the zero value and cmdHistory read ""
// as a regroup axis.
//
// It runs the real subcommand, which is allowed only because cmdHistory
// spawns no harness and makes no network call: it builds a runtime, opens
// relevo.db and prints. XDG_STATE_HOME moves to a temp dir, so TestMain's root
// and the user's state are never touched (#235).
func TestHistoryPlainListsRounds(t *testing.T) {
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

	bindingID, err := d.UpsertBinding(db.Binding{
		Name:         "histcase",
		CWD:          "/work/histcase",
		BuilderMode:  "pane",
		CreatedAt:    time.Now(),
		IngestSource: db.IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	if _, err := d.UpsertRound(db.Round{
		BindingID: bindingID,
		Number:    1,
		StartedAt: time.Now(),
		Outcome:   db.OutcomeReported,
	}); err != nil {
		t.Fatalf("UpsertRound: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, args := range [][]string{
		{"history"},
		{"history", "--since", "3650d"},
		{"history", "--binding", "histcase"},
	} {
		stdout, _, runErr := captureOutput(t, func() error { return run(args) })
		if runErr != nil {
			t.Fatalf("run %v: %v", args, runErr)
		}
		if !strings.Contains(string(stdout), "histcase") {
			t.Errorf("run %v: stdout = %q, want the binding name", args, string(stdout))
		}
		if strings.Contains(string(stdout), "no rounds") {
			t.Errorf("run %v: stdout = %q, must not say no rounds", args, string(stdout))
		}
	}
}

// TestGroupJSONShape pins the --json --by shape: the groups, with each
// group's Rows present only when --rows asked for them, and [] when empty.
func TestGroupJSONShape(t *testing.T) {
	groups := []histq.GroupRow{{
		Key:     "api",
		Rounds:  2,
		Commits: 3,
		Tokens:  1000,
		CostUSD: 1.25,
		Unknown: 1,
		Last:    time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		Rows:    []db.RoundRow{{BindingID: "b1", BindingName: "api"}},
	}}

	with, err := json.Marshal(groupJSON(groups, true))
	if err != nil {
		t.Fatalf("Marshal(with rows): %v", err)
	}
	if !strings.Contains(string(with), `"BindingName":"api"`) {
		t.Errorf("with --rows JSON = %s, want the group's Rows included", with)
	}

	without, err := json.Marshal(groupJSON(groups, false))
	if err != nil {
		t.Fatalf("Marshal(without rows): %v", err)
	}
	if strings.Contains(string(without), `"BindingName":"api"`) {
		t.Errorf("without --rows JSON = %s, want the group's Rows blank", without)
	}
	if !strings.Contains(string(without), `"Key":"api"`) {
		t.Errorf("without --rows JSON = %s, want the group itself kept", without)
	}

	empty, err := json.Marshal(groupJSON(nil, false))
	if err != nil {
		t.Fatalf("Marshal(nil): %v", err)
	}
	if string(empty) != "[]" {
		t.Errorf("Marshal(nil groups) = %s, want []", empty)
	}
}

// TestHistoryJSONHasBuilderName pins A1 §4.4: each `relevo history --json`
// row keeps every token field where it always was and gains the candidate's
// short name beside the token.
func TestHistoryJSONHasBuilderName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.json")
	body := `[{"harness":"agy","provider":"antigravity","model":"opus","roles":["builder"]}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}

	token := "agy/antigravity/opus"
	rows := []db.RoundRow{{BindingName: "api-auth", Number: 3, BuilderCandidate: &token}}

	raw, err := json.Marshal(historyJSONRows(rows, set))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// DeriveNames gives agy/antigravity/opus the name "opus".
	if got := decoded[0]["BuilderName"]; got != "opus" {
		t.Errorf("BuilderName = %v, want opus", got)
	}
	if got := decoded[0]["BindingName"]; got != "api-auth" {
		t.Errorf("BindingName = %v, want the embedded row's field", got)
	}
	if got := decoded[0]["BuilderCandidate"]; got != token {
		t.Errorf("BuilderCandidate = %v, want the token", got)
	}
}
