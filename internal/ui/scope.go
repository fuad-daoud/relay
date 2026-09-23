package ui

import "github.com/fuad-daoud/relevo/internal/relevo"

// scope is the rail's breadth: live (today's bindings only) or all (every
// binding relevo's database has ever recorded, docs/specs/2026-09-20-persistence-design.md
// §5.8).
type scope int

const (
	scopeLive scope = iota
	scopeAll
)

// railRow is one rail row in either scope: exactly one of live and hist is
// non-nil. A name present in both the live report and the database is
// always the live one -- the running binding is the newer, truer picture.
type railRow struct {
	live *relevo.BindingStatus
	hist *relevo.HistoryBinding
}

// name is the row's key: Key() for a live row, the bare binding name for
// a hist row (its key everywhere in scope all -- planner-only, so no
// owner prefix exists to add).
func (r railRow) name() string {
	switch {
	case r.live != nil:
		return r.live.Key()
	case r.hist != nil:
		return r.hist.Name
	}
	return ""
}

// scopeRows merges live's rows, in the order given (the caller has already
// sorted them, attention or name), with hist's rows not named in live.
// hist is expected newest LastActivity first -- relevo.Bindings's own
// order -- and scopeRows preserves that order rather than re-sorting it: a
// hist row is never attention-sorted, and the union never re-orders what
// each half already decided.
func scopeRows(live []relevo.BindingStatus, hist []relevo.HistoryBinding) []railRow {
	rows := make([]railRow, 0, len(live)+len(hist))
	liveNames := make(map[string]bool, len(live))
	for i := range live {
		liveNames[live[i].Name] = true
		rows = append(rows, railRow{live: &live[i]})
	}
	for i := range hist {
		if liveNames[hist[i].Name] {
			continue
		}
		rows = append(rows, railRow{hist: &hist[i]})
	}
	return rows
}
