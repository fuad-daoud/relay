package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/usage"
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

// TestSpendText pins the SPEND cell (A2): money only. Measured and estimated
// dollars sum, a '~' marks a partly estimated sum, a plan lane reads "plan",
// and anything under a cent reads "<$0.01".
func TestSpendText(t *testing.T) {
	cases := []struct {
		name string
		s    usage.Spend
		want string
	}{
		{"measured only", usage.Spend{Measured: 9.40}, "$9.40"},
		{"measured and estimated", usage.Spend{Measured: 1.23, Estimated: 0.40}, "~$1.63"},
		{"estimated only", usage.Spend{Estimated: 0.02}, "~$0.02"},
		{"plan lane", usage.Spend{Rounds: 2, Plan: 2}, "plan"},
		{"measured beats a plan lane", usage.Spend{Measured: 0.10, Plan: 1}, "$0.10"},
		{"under a cent", usage.Spend{Measured: 0.004}, "<$0.01"},
		{"under a cent, estimated", usage.Spend{Estimated: 0.004}, "~<$0.01"},
		{"nothing measured", usage.Spend{Rounds: 3, Unknown: 2}, ""},
		{"zero", usage.Spend{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := spendText(tc.s); got != tc.want {
				t.Errorf("spendText(%+v) = %q, want %q", tc.s, got, tc.want)
			}
		})
	}
}

// TestFleetBodyHeader pins A1: the table's first body line is the header, and
// neither the loading nor the empty body has one.
func TestFleetBodyHeader(t *testing.T) {
	rows := []relevo.BindingStatus{{Name: "webshop", Round: 4, Display: "ACTIVE"}}
	loaded := Env{Loaded: true, Now: railNow, Report: relevo.Report{Bindings: rows}, Width: 140, Height: 40}
	f := newFleetView(true)

	lines := strings.Split(f.Body(loaded, 140, 40), "\n")
	if !strings.Contains(stripANSI(lines[0]), "NAME") {
		t.Errorf("the first body line must be the header, got %q", stripANSI(lines[0]))
	}
	if !strings.Contains(stripANSI(lines[1]), "webshop") {
		t.Errorf("the header must not scroll the rows away, line 1 = %q", stripANSI(lines[1]))
	}

	loading := loaded
	loading.Loaded = false
	if lines := strings.Split(f.Body(loading, 140, 40), "\n"); strings.Contains(stripANSI(lines[0]), "NAME") {
		t.Errorf("the loading body must have no header: %q", stripANSI(lines[0]))
	}
	empty := loaded
	empty.Report = relevo.Report{}
	if lines := strings.Split(f.Body(empty, 140, 40), "\n"); strings.Contains(stripANSI(lines[0]), "NAME") {
		t.Errorf("the empty body must have no header: %q", stripANSI(lines[0]))
	}
}

// TestFleetHeaderSurvivesNarrowing pins A1: the header uses the rows' own
// widths, gutter and drop rule, so at 80 columns it shows exactly
// NAME ON RND STATE NOW.
func TestFleetHeaderSurvivesNarrowing(t *testing.T) {
	line := stripANSI(fleetHeaderLine(80))
	if w := len([]rune(line)); w != 80 {
		t.Errorf("the header at 80 is %d cells wide, want 80: %q", w, line)
	}
	for _, want := range []string{"NAME", "ON", "RND", "STATE", "NOW"} {
		if !strings.Contains(line, want) {
			t.Errorf("the header at 80 = %q, want %s", line, want)
		}
	}
	for _, not := range []string{"ACTOR", "SPEND", "PLANNER", "REPO"} {
		if strings.Contains(line, not) {
			t.Errorf("the header at 80 = %q, must not name the dropped %s column", line, not)
		}
	}

	wide := stripANSI(fleetHeaderLine(140))
	for _, want := range []string{"NAME", "ACTOR", "ON", "RND", "STATE", "NOW", "SPEND", "PLANNER", "REPO"} {
		if !strings.Contains(wide, want) {
			t.Errorf("the header at 140 = %q, want %s", wide, want)
		}
	}
}

// TestNeedsYouGrammarIsShared pins A3: the header and the fleet's context
// line read "1 needs you" for one and "N need you" for more, from the one
// helper, so they cannot drift.
func TestNeedsYouGrammarIsShared(t *testing.T) {
	one := splitModel(t, 140, 40, relevo.BindingStatus{Name: "webshop", Round: 1, Display: "NEEDS YOU"})
	if h := stripANSI(one.headerView(one.env())); !strings.Contains(h, "● 1 needs you") {
		t.Errorf("header = %q, want the singular", h)
	}
	left, _ := fleet(one).Context(one.env())
	if !strings.Contains(stripANSI(left), "1 needs you") {
		t.Errorf("context = %q, want the singular", stripANSI(left))
	}

	two := splitModel(t, 140, 40,
		relevo.BindingStatus{Name: "webshop", Round: 1, Display: "NEEDS YOU"},
		relevo.BindingStatus{Name: "docs", Round: 1, Display: "NEEDS YOU"},
	)
	if h := stripANSI(two.headerView(two.env())); !strings.Contains(h, "● 2 need you") {
		t.Errorf("header = %q, want the plural", h)
	}
	left, _ = fleet(two).Context(two.env())
	if !strings.Contains(stripANSI(left), "2 need you") {
		t.Errorf("context = %q, want the plural", stripANSI(left))
	}
}

// TestFleetFilter pins A4: '/' opens a capturing input, the typed text keeps
// only matching rows, the selection survives, enter keeps the filter, and
// esc clears it.
func TestFleetFilter(t *testing.T) {
	rows := []relevo.BindingStatus{
		{Name: "webshop", Round: 4, Display: "ACTIVE", CWD: "/home/x/web"},
		{Name: "docs", Round: 1, Display: "DONE"},
		{Name: "api", Round: 2, Display: "ACTIVE"},
	}
	env := Env{Loaded: true, Now: railNow, Report: relevo.Report{Bindings: rows}, Width: 140, Height: 40, StatusAt: railNow}
	f := newFleetView(true)
	key := func(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

	// Put the selection on "docs" first: it must survive the filter.
	all := f.rows(env)
	for i, b := range all {
		if b.Name == "docs" {
			f.cursor, f.sticky = i, b.Key()
		}
	}
	if got := f.rows(env)[f.cursor].Name; got != "docs" {
		t.Fatalf("cursor on %q, want docs", got)
	}

	// '/' opens the input; it captures every key while open.
	next, _ := f.Update(key('/'), env)
	f = next.(fleetView)
	if !f.Capturing() {
		t.Fatal("the filter input must capture keys while open")
	}

	// Type "do": only docs matches, and it stays selected.
	for _, r := range "do" {
		next, _ = f.Update(key(r), env)
		f = next.(fleetView)
	}
	if got := f.rows(env); len(got) != 1 || got[0].Name != "docs" {
		t.Fatalf("filtered rows = %v, want [docs]", got)
	}
	if got := f.rows(env)[f.cursor].Name; got != "docs" {
		t.Errorf("the selection must survive the filter, cursor on %q", got)
	}
	if _, right := f.Context(env); !strings.Contains(stripANSI(right), `filter "do"`) {
		t.Errorf("the context line must name the filter, right = %q", stripANSI(right))
	}

	// enter keeps the filter and closes the input.
	next, _ = f.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	f = next.(fleetView)
	if f.Capturing() {
		t.Error("enter must close the filter input")
	}
	if got := f.rows(env); len(got) != 1 || got[0].Name != "docs" {
		t.Errorf("enter must keep the filter, rows = %v", got)
	}

	// esc at the fleet, not capturing, clears it.
	next, _ = f.Update(tea.KeyMsg{Type: tea.KeyEsc}, env)
	f = next.(fleetView)
	if got := f.rows(env); len(got) != len(rows) {
		t.Errorf("esc must clear the filter, rows = %v", got)
	}
	if _, right := f.Context(env); strings.Contains(stripANSI(right), "filter") {
		t.Errorf("the cleared filter must leave the context line, right = %q", stripANSI(right))
	}

	// esc while the input is open clears it and closes.
	next, _ = f.Update(key('/'), env)
	f = next.(fleetView)
	next, _ = f.Update(key('w'), env)
	f = next.(fleetView)
	if len(f.rows(env)) != 1 {
		t.Fatalf("typing must filter live, rows = %v", f.rows(env))
	}
	next, _ = f.Update(tea.KeyMsg{Type: tea.KeyEsc}, env)
	f = next.(fleetView)
	if f.Capturing() {
		t.Error("esc must close the filter input")
	}
	if got := f.rows(env); len(got) != len(rows) {
		t.Errorf("esc must clear the filter, rows = %v", got)
	}
}

// TestFleetFilterMatchesEveryShownField pins A4's field list: key, actor,
// candidate, planner, repo and state all match, case-insensitively.
func TestFleetFilterMatchesEveryShownField(t *testing.T) {
	b := relevo.BindingStatus{
		Name: "webshop", Round: 4, Display: "PAUSED", Role: "reviewer",
		BuilderCandidate: "cline-pass/deepseek-v4.1-flash#high",
		PlannerName:      "architect-1", CWD: "/home/x/relevo",
	}
	for _, q := range []string{"WEBSHOP", "reviewer", "deepseek", "architect-1", "relevo", "paused"} {
		if !fleetRowMatches(b, q) {
			t.Errorf("fleetRowMatches(%q) = false, want true", q)
		}
	}
	if fleetRowMatches(b, "zzz") {
		t.Error("an unmatched text must keep nothing")
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
