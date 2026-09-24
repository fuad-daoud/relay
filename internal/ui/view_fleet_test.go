package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// TestWhatAge ports the rail's whatAge assertions to view_fleet.go (R2.10).
func TestWhatAge(t *testing.T) {
	cases := []struct {
		name     string
		b        relevo.BindingStatus
		wantWhat string
		wantAge  string
	}{
		{
			name: "needs you blocked",
			b: relevo.BindingStatus{Display: "NEEDS YOU",
				Waiting: &relevo.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute)}},
			wantWhat: "question", wantAge: "2m",
		},
		{
			name:     "needs you detail",
			b:        relevo.BindingStatus{Display: "NEEDS YOU", Detail: "broken: no space"},
			wantWhat: "broken: no space",
		},
		{
			name:     "needs you bare",
			b:        relevo.BindingStatus{Display: "NEEDS YOU"},
			wantWhat: "needs you",
		},
		{
			name:     "active working",
			b:        relevo.BindingStatus{Display: "ACTIVE", BuilderStatus: "working"},
			wantWhat: "working",
		},
		{
			name:     "done",
			b:        relevo.BindingStatus{Display: "DONE", Last: &relevo.LastEvent{TS: railNow.Add(-3 * time.Hour)}},
			wantWhat: "done", wantAge: "3h",
		},
		{
			name:     "paused",
			b:        relevo.BindingStatus{Display: "PAUSED", Last: &relevo.LastEvent{TS: railNow.Add(-time.Hour), Kind: "pause"}},
			wantWhat: "paused", wantAge: "1h",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotWhat, gotAge := whatAge(tc.b, railNow)
			if gotWhat != tc.wantWhat || gotAge != tc.wantAge {
				t.Errorf("whatAge = (%q, %q), want (%q, %q)", gotWhat, gotAge, tc.wantWhat, tc.wantAge)
			}
		})
	}
}

// TestNowCellCarriesTheStaleLabel is the stale-label port (#135): a NEEDS
// YOU row carries its stale age in the NOW cell.
func TestNowCellCarriesTheStaleLabel(t *testing.T) {
	b := relevo.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU", BuilderKind: "agy",
		Stale:   "stale 4h 0m",
		Waiting: &relevo.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute)},
	}
	if got := nowCell(b, railNow); !strings.Contains(got, "· stale 4h 0m") {
		t.Errorf("nowCell = %q, want it to carry %q", got, "· stale 4h 0m")
	}
}

// TestCandidateText pins the ON cell's naming rule in the one place it
// lives: the model part is everything after the second '/'.
func TestCandidateText(t *testing.T) {
	cases := map[string]string{
		"cline-pass/deepseek-v4.1-flash#high": "deepseek-v4.1-flash#high",
		"agy":                                 "agy",
		"":                                    "-",
	}
	for in, want := range cases {
		if got := candidateText(relevo.BindingStatus{BuilderCandidate: in}); got != want {
			t.Errorf("candidateText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFleetColumnsAndNarrowDropOrder pins §4.3's columns and the drop order
// REPO, PLANNER, SPEND, ACTOR at widths 140, 110, 95 and 80 (R2.11).
func TestFleetColumnsAndNarrowDropOrder(t *testing.T) {
	b := relevo.BindingStatus{
		Name: "webshop", Round: 4, Display: "ACTIVE", Role: "reviewer", CWD: "/home/x/very/long/repo/path",
		BuilderCandidate: "cline-pass/deepseek-v4.1-flash#high",
		PlannerName:      "architect-1",
	}
	cases := []struct {
		width     int
		wantRepo  bool
		wantPlan  bool
		wantActor bool
		wantSpend bool
	}{
		// 140: everything fits, REPO takes the rest.
		{140, true, true, true, true},
		// 110: REPO then PLANNER drop.
		{110, false, false, true, true},
		// 95: REPO, PLANNER, SPEND then ACTOR drop.
		{95, false, false, false, false},
		// 80: REPO, PLANNER, SPEND and ACTOR all drop.
		{80, false, false, false, false},
	}
	for _, tc := range cases {
		cells := strings.Join(fleetCells(b, railNow, tc.width), "|")
		if got := strings.Contains(cells, "repo/path"); got != tc.wantRepo {
			t.Errorf("width %d: repo present = %v, want %v: %s", tc.width, got, tc.wantRepo, cells)
		}
		if got := strings.Contains(cells, "architect-1"); got != tc.wantPlan {
			t.Errorf("width %d: planner present = %v, want %v: %s", tc.width, got, tc.wantPlan, cells)
		}
		if got := strings.Contains(cells, "reviewer"); got != tc.wantActor {
			t.Errorf("width %d: actor present = %v, want %v: %s", tc.width, got, tc.wantActor, cells)
		}
		if got := strings.Contains(cells, "│"); got {
			t.Errorf("width %d: separators leak into the cells: %s", tc.width, cells)
		}
		if got := strings.Contains(cells, "deepseek-v4.1-flash"); !got {
			t.Errorf("width %d: the ON cell must survive: %s", tc.width, cells)
		}
	}
}

// TestFleetNeedsYouSecondLine pins §4.3: a NEEDS YOU row with a Waiting
// line gets a second line, five spaces, "└ ", then the question in dim.
func TestFleetNeedsYouSecondLine(t *testing.T) {
	b := relevo.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU",
		Waiting: &relevo.Waiting{Cause: "blocked", Line: "which database should r4 use?"},
	}
	f := fleetView{}
	env := Env{Loaded: true, Now: railNow, Report: relevo.Report{Bindings: []relevo.BindingStatus{b}}, Width: 140}
	lines := f.fleetLines(env, 140)
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2 (row + question)", len(lines))
	}
	if !strings.Contains(stripANSI(lines[1].text), "└ which database should r4 use?") {
		t.Errorf("second line = %q", stripANSI(lines[1].text))
	}
	if lines[1].row != 0 {
		t.Errorf("the question line belongs to row 0, got %d", lines[1].row)
	}

	// No Waiting line: no second line.
	b.Waiting = &relevo.Waiting{Cause: "blocked"}
	env.Report = relevo.Report{Bindings: []relevo.BindingStatus{b}}
	if got := len(f.fleetLines(env, 140)); got != 1 {
		t.Errorf("without a Waiting line: %d lines, want 1", got)
	}
}
