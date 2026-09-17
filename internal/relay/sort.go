package relay

import "sort"

// attentionRank orders display states for a human: what needs a decision
// first, what is waiting on the planner next, then what is working, then
// what is finished. Unknown states (none today) sort after DONE.
var attentionRank = map[string]int{
	"NEEDS YOU": 0,
	"HELD":      1,
	"ACTIVE":    2,
	"DONE":      3,
}

func rankOf(display string) int {
	if r, ok := attentionRank[display]; ok {
		return r
	}
	return len(attentionRank)
}

// SortRows orders status rows for a human. attention groups by display
// state -- NEEDS YOU, HELD, ACTIVE, DONE -- and within a group puts the
// most recent Last.TS first (a nil Last last), name as the tiebreak; name
// is the order Status has always returned. Stable; never mutates its
// input. Only `relay ui` calls it today; #143 moves `status` onto it.
func SortRows(rows []BindingStatus, attention bool) []BindingStatus {
	out := make([]BindingStatus, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if attention {
			if ra, rb := rankOf(a.Display), rankOf(b.Display); ra != rb {
				return ra < rb
			}
			switch {
			case a.Last != nil && b.Last != nil && !a.Last.TS.Equal(b.Last.TS):
				return a.Last.TS.After(b.Last.TS)
			case a.Last != nil && b.Last == nil:
				return true
			case a.Last == nil && b.Last != nil:
				return false
			}
		}
		return a.Name < b.Name
	})
	return out
}
