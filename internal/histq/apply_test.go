package histq

import "testing"

// TestApplyWordMatchesAnyOfThree pins that a bare word matches the binding
// name, the repo or the feature -- any of the three. "web" reaches five rows
// only because the repo branch adds the two rows whose binding is not named
// "web"; dropping that branch leaves three and fails this test. "agy" lives
// only in the builder column, which is deliberately not one of the three.
func TestApplyWordMatchesAnyOfThree(t *testing.T) {
	rows := fixtureRows()

	cases := []struct {
		word string
		want int
	}{
		{"api", 6},      // b1's name (4 rows) ∪ the api repo (5 rows) = 6 distinct
		{"web", 5},      // the web binding (3 rows) ∪ the web repo (5 rows)
		{"checkout", 5}, // only the feature column
		{"search", 5},   // the other feature
		{"SEARCH", 5},   // case-insensitive
		{"agy", 0},      // the builder column is not matched by a word
	}
	for _, c := range cases {
		q := Query{Words: []string{c.word}}
		if got := len(q.Apply(rows)); got != c.want {
			t.Errorf("Apply(word %q) kept %d rows, want %d", c.word, got, c.want)
		}
	}
}

// TestApplyNumericConds exercises every numeric key and operator, including
// the nil cases: a nil cost, commits or duration fails every comparison, and
// a nil token column counts as zero.
func TestApplyNumericConds(t *testing.T) {
	rows := fixtureRows()

	cases := []struct {
		query string
		want  int
	}{
		{"cost>0", 9},             // row 5 has no cost at all
		{"cost>=2", 4},            // rows 2 (2.00), 3 (3.00), 7 (4.00), 9 (3.50)
		{"tokens>=2000", 4},       // rows 4, 6, 7, 9
		{"tokens=1000", 2},        // rows 1, 2
		{"commits>=1", 7},         // row 5 is nil, rows 3 and 10 are 0
		{"commits<3", 6},          // rows 1, 2, 3, 6, 8, 10
		{"duration<=60", 6},       // rows 1-4, 6, 7; row 5 is nil
		{"duration>60", 3},        // rows 8, 9, 10
		{"round>2", 4},            // rows 3, 6, 9, 10
		{"round=3", 3},            // rows 3, 6, 9
		{"cost>=1 commits>=2", 4}, // rows 1, 7, 8, 9: every condition holds
	}
	for _, c := range cases {
		q, err := ParseAt(c.query, parseNow)
		if err != nil {
			t.Fatalf("ParseAt(%q): %v", c.query, err)
		}
		if got := len(q.Apply(rows)); got != c.want {
			t.Errorf("Apply(%q) kept %d rows, want %d", c.query, got, c.want)
		}
	}
}

// TestApplyEnumConds exercises the five columns with no db.Filter of their
// own, alone and combined; a row whose column is nil fails.
func TestApplyEnumConds(t *testing.T) {
	rows := fixtureRows()

	cases := []struct {
		query string
		want  int
	}{
		{"report:done", 4},           // rows 1, 2, 7, 9
		{"report:deferred", 1},       // row 4
		{"gate:pass", 3},             // rows 1, 4, 9
		{"gate:timeout", 1},          // row 7
		{"basis:unknown", 2},         // rows 3, 8
		{"basis:measured", 5},        // rows 1, 2, 4, 7, 9
		{"server:contabo", 5},        // rows 1-5
		{"server:local", 5},          // rows 6-10
		{"mode:remote", 3},           // rows 5, 6, 8
		{"mode:pane", 4},             // rows 1, 2, 7, 10
		{"mode:headless", 3},         // rows 3, 4, 9
		{"report:done mode:pane", 3}, // rows 1, 2, 7
		{"gate:pass mode:remote", 0}, // no remote row passed the gate
	}
	for _, c := range cases {
		q, err := ParseAt(c.query, parseNow)
		if err != nil {
			t.Fatalf("ParseAt(%q): %v", c.query, err)
		}
		if got := len(q.Apply(rows)); got != c.want {
			t.Errorf("Apply(%q) kept %d rows, want %d", c.query, got, c.want)
		}
	}
}
