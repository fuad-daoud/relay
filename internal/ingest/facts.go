package ingest

import (
	"time"

	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/store"
)

// roundFacts derives round n's plain facts from its events: when it
// started and closed, its tier, its commit/tree facts, its gate result,
// its token and cost usage, and the report's own outcome block. It fills
// every db.Round field except ID, BindingID, Number, Outcome,
// BuilderCandidate/Harness/Provider/Model/Mode and Switches, which the
// caller (Ingest) sets from deriveOutcome, builderForRound and b.
func roundFacts(events []store.LogEntry, n int) db.Round {
	var r db.Round

	var earliest time.Time
	var haveEarliest bool
	var havePlanTS bool
	var planTS time.Time

	for _, e := range events {
		if e.Round != n {
			continue
		}
		if !haveEarliest || e.TS.Before(earliest) {
			earliest, haveEarliest = e.TS, true
		}
		switch e.Kind {
		case store.KindPlan:
			if !havePlanTS || e.TS.Before(planTS) {
				planTS, havePlanTS = e.TS, true
			}
			if e.Tier != "" {
				tier := e.Tier
				r.Tier = &tier
			}
		case store.KindDiff:
			if e.Tree != "" {
				commits := e.Commits
				tree := e.Tree
				r.Commits = &commits
				r.Tree = &tree
			}
		case store.KindReport:
			ts := e.TS
			r.ClosedAt = &ts
			if e.Gate != nil {
				result := e.Gate.Result
				exitCode := e.Gate.ExitCode
				durationMS := e.Gate.DurationMS
				r.GateResult = &result
				r.GateExit = &exitCode
				r.GateDurationMS = &durationMS
			}
			if e.Usage != nil {
				in := e.Usage.Tokens.In
				cache := e.Usage.Tokens.CacheRead
				write := e.Usage.Tokens.CacheWrite
				out := e.Usage.Tokens.Out
				cost := e.Usage.Cost.USD
				basis := string(e.Usage.Cost.Basis)
				r.InTokens = &in
				r.CacheTokens = &cache
				r.WriteTokens = &write
				r.OutTokens = &out
				r.CostUSD = &cost
				r.CostBasis = &basis
			}
			outcome := e.Outcome
			if outcome == "" {
				outcome = "unstructured"
			}
			r.ReportOutcome = &outcome
		case store.KindExit, store.KindSwitch:
			if r.ClosedAt == nil {
				ts := e.TS
				r.ClosedAt = &ts
			}
		}
	}

	if havePlanTS {
		r.StartedAt = planTS
	} else if haveEarliest {
		r.StartedAt = earliest
	}

	return r
}
