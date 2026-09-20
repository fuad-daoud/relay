package relay

import (
	"context"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// usageDeadline bounds one usage read at round close. The round closes
// whatever the reader does; a slow read is a note, not a stall.
const usageDeadline = 5 * time.Second

// liveDeadline bounds one live usage read per binding (#234). Status runs
// on every ui tick and statusline call across every binding, so a slow or
// absent record shows nothing live rather than stalling the refresh.
const liveDeadline = 500 * time.Millisecond

// peekUsage reads what the binding's open round has consumed so far
// (#234): the same reader and source recordUsage uses, asked without
// waiting for the record to close. nil when no round is open, no reader
// is wired, or nothing is readable yet. Never recorded, never summed.
func peekUsage(ctx context.Context, rt Runtime, b store.Binding, now time.Time) *usage.Usage {
	if rt.Usage == nil || b.RoundStartedAt.IsZero() || b.Builder.Kind == "" {
		return nil
	}
	src := roundSource(rt, b, b.RoundStartedAt, now)
	pctx, cancel := context.WithTimeout(ctx, liveDeadline)
	defer cancel()
	samples, note := rt.Usage.Peek(pctx, src)
	if len(samples) == 0 {
		return nil
	}
	u := usage.Fold(samples, rt.Prices, src.Plan, note)
	u.Harness = src.Harness
	if u.Provider == "" {
		u.Provider = src.Provider
	}
	if !src.Start.IsZero() && src.End.After(src.Start) {
		u.DurationMS = src.End.Sub(src.Start).Milliseconds()
	}
	return &u
}

// roundSource is everything the usage reader needs for the binding's
// current round: the builder that closed it, its candidate's provider and
// model when it was spawned from one, the round's stream file when
// headless, the worktree when pane, and the window.
func roundSource(rt Runtime, b store.Binding, start, end time.Time) usage.Source {
	src := usage.Source{
		Harness:  b.Builder.Kind,
		Mode:     usage.ModePane,
		Worktree: b.Worktree,
		Start:    start,
		End:      end,
	}
	if b.Builder.Headless() {
		src.Mode = usage.ModeHeadless
		src.StreamPath = rt.Store.BuilderStreamPath(b.Name, b.Round)
	}
	fillCandidate(&src, rt, b.BuilderCandidate)
	return src
}

// consultSource is roundSource for one consult: its own pane, the
// binding's worktree, its spawn as the window's start.
func consultSource(rt Runtime, b store.Binding, c store.Consult, end time.Time) usage.Source {
	src := usage.Source{
		Harness:  c.Endpoint.Kind,
		Mode:     usage.ModePane,
		Worktree: b.Worktree,
		Start:    c.SpawnedAt,
		End:      end,
	}
	if c.Endpoint.Headless() {
		src.Mode = usage.ModeHeadless
		src.StreamPath = c.Endpoint.LogPath // consults have no stream file today; the reader notes "no stream"
	}
	return src
}

func fillCandidate(src *usage.Source, rt Runtime, token string) {
	if token == "" || rt.Candidates == nil {
		return
	}
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		src.Provider, src.Model = ref.Provider, ref.Model
		return
	}
	src.Provider, src.Model, src.Plan = c.Provider, c.Model, c.Plan
}

// recordUsage reads src and folds it with the runtime's prices. It never
// returns nil and never errors: no reader, a reader that finds nothing,
// and a reader that overruns usageDeadline are all Basis unknown with a
// note.
func recordUsage(ctx context.Context, rt Runtime, src usage.Source) *usage.Usage {
	var u usage.Usage
	if rt.Usage == nil {
		u = usage.Fold(nil, rt.Prices, src.Plan, "no reader")
	} else {
		rctx, cancel := context.WithTimeout(ctx, usageDeadline)
		samples, note := rt.Usage.Read(rctx, src)
		timedOut := rctx.Err() != nil
		cancel()
		u = usage.Fold(samples, rt.Prices, src.Plan, note)
		if len(samples) > 0 && note != "" && !strings.Contains(u.Note, note) {
			if u.Note != "" {
				u.Note += "; "
			}
			u.Note += note
		}
		if timedOut {
			if u.Note != "" {
				u.Note += "; "
			}
			u.Note += "timed out"
		}
	}
	u.Harness = src.Harness
	if u.Provider == "" {
		u.Provider = src.Provider
	}
	if !src.Start.IsZero() && src.End.After(src.Start) {
		u.DurationMS = src.End.Sub(src.Start).Milliseconds()
	}
	return &u
}
