package relay

import "testing"

// TestEscapeOutcome pins escapeOutcome over all eight input combinations
// (#192). It is the mutation target: flip which input the function looks at
// and this must catch it.
func TestEscapeOutcome(t *testing.T) {
	cases := []struct {
		treeUnchanged, repoDirty, hasReport bool
		want                                EscapeOutcome
	}{
		{false, false, false, EscapeNone},
		{false, false, true, EscapeNone},
		{false, true, false, EscapeNone},
		{false, true, true, EscapeNone},
		{true, false, false, EscapeNone},
		{true, false, true, EscapeNone},
		{true, true, false, EscapeHalt},
		{true, true, true, EscapeNote},
	}
	for _, c := range cases {
		got := escapeOutcome(c.treeUnchanged, c.repoDirty, c.hasReport)
		if got != c.want {
			t.Errorf("escapeOutcome(treeUnchanged=%v, repoDirty=%v, hasReport=%v) = %v, want %v",
				c.treeUnchanged, c.repoDirty, c.hasReport, got, c.want)
		}
	}
}
