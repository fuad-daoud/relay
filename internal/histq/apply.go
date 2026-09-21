package histq

import (
	"strings"

	"github.com/fuad-daoud/relay/internal/db"
)

// Apply is the in-Go half of a query: the conditions db.Query cannot
// express, applied to the rows it returned. Order is preserved and every
// condition must hold (a query with none keeps every row).
//
// A bare Word is a case-insensitive substring of the row's binding name,
// repo or feature -- any of the three. A NumCond compares cost (*CostUSD),
// tokens (sum of the four token columns, nil counting 0), commits
// (*Commits), duration (minutes, from *DurationMS) or the round number; a
// nil column fails every comparison. Report, Gate, Basis, Server and Mode
// compare against ReportOutcome, GateResult, CostBasis, Server and
// BuilderMode, and a nil column fails too
// (docs/specs/2026-09-21-dashboard-design.md §3-§4).
func (q Query) Apply(rows []db.RoundRow) []db.RoundRow {
	out := make([]db.RoundRow, 0, len(rows))
	for _, r := range rows {
		if q.keep(r) {
			out = append(out, r)
		}
	}
	return out
}

// keep reports whether every condition in q holds for r.
func (q Query) keep(r db.RoundRow) bool {
	for _, w := range q.Words {
		if !rowHasWord(r, w) {
			return false
		}
	}
	for _, n := range q.Nums {
		if !numHolds(r, n) {
			return false
		}
	}
	if q.Report != "" && !strEquals(r.ReportOutcome, q.Report) {
		return false
	}
	if q.Gate != "" && !strEquals(r.GateResult, q.Gate) {
		return false
	}
	if q.Basis != "" && !strEquals(r.CostBasis, q.Basis) {
		return false
	}
	if q.Server != "" && !strEquals(r.Server, q.Server) {
		return false
	}
	if q.Mode != "" && !strEquals(r.BuilderMode, q.Mode) {
		return false
	}
	return true
}

// rowHasWord reports whether word is a case-insensitive substring of the
// row's binding name, repo or feature.
func rowHasWord(r db.RoundRow, word string) bool {
	w := strings.ToLower(word)
	if strings.Contains(strings.ToLower(r.BindingName), w) {
		return true
	}
	if r.Repo != nil && strings.Contains(strings.ToLower(*r.Repo), w) {
		return true
	}
	if r.Feature != nil && strings.Contains(strings.ToLower(*r.Feature), w) {
		return true
	}
	return false
}

// numHolds reports whether the row satisfies one numeric condition.
func numHolds(r db.RoundRow, n NumCond) bool {
	switch n.Key {
	case "cost":
		if r.CostUSD == nil {
			return false
		}
		return compareNum(n.Op, *r.CostUSD, n.Value)
	case "tokens":
		return compareNum(n.Op, float64(rowTokens(r)), n.Value)
	case "commits":
		if r.Commits == nil {
			return false
		}
		return compareNum(n.Op, float64(*r.Commits), n.Value)
	case "duration":
		if r.DurationMS == nil {
			return false
		}
		return compareNum(n.Op, float64(*r.DurationMS)/60000, n.Value)
	case "round":
		return compareNum(n.Op, float64(r.Number), n.Value)
	}
	return true
}

// compareNum applies op, one of > < >= <= or =, to have and want.
func compareNum(op string, have, want float64) bool {
	switch op {
	case ">":
		return have > want
	case "<":
		return have < want
	case ">=":
		return have >= want
	case "<=":
		return have <= want
	case "=":
		return have == want
	}
	return false
}

// rowTokens sums the four token columns, treating nil as zero.
func rowTokens(r db.RoundRow) int64 {
	var n int64
	for _, p := range []*int64{r.InTokens, r.CacheTokens, r.WriteTokens, r.OutTokens} {
		if p != nil {
			n += *p
		}
	}
	return n
}

// strEquals reports whether a nullable column equals want; nil never does.
func strEquals(have *string, want string) bool {
	return have != nil && *have == want
}
