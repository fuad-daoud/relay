package planner

import "time"

// Prune forgets every record that is gone and that no binding still names
// (§4.7): the pile-up the plugin's SessionStart hook leaves behind on a machine
// that runs many Claude Code sessions, minus anything still in use.
//
// procStart is RecordState's injected process read. bindings reports how many
// non-DONE bindings name a planner id -- the same store walk Forget's in-use
// guard uses (cmd/relevo's plannerBindingCounts); nil counts nothing. A record
// that is live, an explicit registration, and a record any non-DONE binding
// names are all left alone.
//
// It returns the records it forgot, in List's name order; with dryRun it
// returns what it would forget and forgets nothing. An error from List or
// Forget stops the walk and returns what was forgotten before it.
func Prune(reg Registry, procStart func(pid int) (int64, error), bindings func(id string) int, dryRun bool) ([]Record, error) {
	records, err := reg.List()
	if err != nil {
		return nil, err
	}

	inUse := func(id string) bool { return bindings != nil && bindings(id) > 0 }

	var forgotten []Record
	for _, rec := range records {
		if RecordState(rec, procStart) != StateGone || inUse(rec.ID) {
			continue
		}
		forgotten = append(forgotten, rec)
		if dryRun {
			continue
		}
		// The count above already proved no binding names this record, so
		// Forget needs no guard of its own.
		if err := reg.Forget(rec.ID, nil); err != nil {
			return forgotten, err
		}
	}
	return forgotten, nil
}

// OpencodeIdleTTL is how long an explicit opencode planner record may go
// unseen before the daemon forgets it (no binding may name it).
const OpencodeIdleTTL = 7 * 24 * time.Hour

func PruneIdle(reg Registry, bindings func(id string) int, now time.Time, dryRun bool) ([]Record, error) {
	records, err := reg.List()
	if err != nil {
		return nil, err
	}

	inUse := func(id string) bool { return bindings != nil && bindings(id) > 0 }

	var forgotten []Record
	for _, rec := range records {
		if rec.HarnessKind != "opencode" || rec.HostPID > 0 || now.Sub(rec.SeenAt) <= OpencodeIdleTTL || inUse(rec.ID) {
			continue
		}
		forgotten = append(forgotten, rec)
		if dryRun {
			continue
		}
		if err := reg.Forget(rec.ID, nil); err != nil {
			return forgotten, err
		}
	}
	return forgotten, nil
}
