package relay

import (
	"context"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// signals is one tick's read of a binding's progress sources (#135). A source
// that cannot be read is absent for that sample: sampleSignals leaves it at its
// zero value rather than inventing one, because a failed read is not evidence
// that a builder stopped.
type signals struct {
	// tree is the working tree's fingerprint; "" when unavailable (no git, or
	// no cwd to run it in).
	tree string
	// output is the pane builder's screen fingerprint; "" for a headless
	// builder, whose output is the stream file's mtime instead.
	output string
	// outputAt is a headless builder's streamLastActivity. A pane's output
	// time is derived from output changes, so it is left zero here.
	outputAt time.Time
	// blocked is true when the pane builder's status is blocked: it is waiting
	// on a human, so it is never stalled.
	blocked bool
}

// sampleSignals reads a binding's progress sources for one tick (#135). It is
// read-only and best-effort: every failure is simply an absent signal.
func sampleSignals(ctx context.Context, rt Runtime, b store.Binding, agents []herdr.Agent) signals {
	var s signals

	if rt.Git != nil && b.CWD != "" {
		if fp, err := rt.Git.TreeFingerprint(ctx, b.CWD); err == nil {
			s.tree = fp
		}
	}

	if b.Builder.Headless() {
		// A headless builder is a process, not a pane: its liveness signal is
		// the stream file's mtime, exactly as #252 read it.
		s.outputAt = streamLastActivity(rt, b)
		return s
	}

	if rt.Herdr != nil {
		if fp, err := screenFingerprint(ctx, rt, b); err == nil {
			s.output = fp
		}
	}
	if a, ok := FindAgent(agents, b.Builder); ok && a.Status == herdr.StatusBlocked {
		s.blocked = true
	}

	return s
}

// progressStep advances a binding's progress clock by one tick (#135).
//
// Precondition: the round is open (RoundStartedAt non-zero). The first call
// records the current signals and starts the clock from the round's start; a
// call inside policy.json's progress_interval_ms returns the binding unchanged,
// so callers may call it every tick and sample at most once per interval.
//
// It sets and clears two labels and never acts:
//   - StalledSince when no signal has moved for stall_after_ms and the builder
//     is not blocked;
//   - ExploringSince when the output or screen has moved while the tree has
//     not for explore_after_ms, and the builder is not stalled.
func progressStep(rt Runtime, b store.Binding, now time.Time, s signals) store.Binding {
	if b.RoundStartedAt.IsZero() {
		return b
	}

	if b.Progress == nil {
		b.Progress = &store.Progress{
			SampledAt: now,
			Tree:      s.tree,
			TreeAt:    b.RoundStartedAt,
			Output:    s.output,
			OutputAt:  b.RoundStartedAt,
		}
	} else if now.Sub(b.Progress.SampledAt) < rt.Policy.ProgressInterval() {
		return b // not yet
	} else {
		b.Progress.SampledAt = now
	}

	p := b.Progress
	if s.tree != "" && s.tree != p.Tree {
		p.Tree, p.TreeAt = s.tree, now
	}
	if b.Builder.Headless() {
		if s.outputAt.After(p.OutputAt) {
			p.OutputAt = s.outputAt
		}
	} else if s.output != "" && s.output != p.Output {
		p.Output, p.OutputAt = s.output, now
	}

	// No signal has ever been readable: record the sample and set no label,
	// because the round's start is not evidence of a hung builder on its own.
	if !anySignal(b, p) {
		return b
	}

	last := p.TreeAt
	if p.OutputAt.After(last) {
		last = p.OutputAt
	}

	if !s.blocked && now.Sub(last) >= rt.Policy.StallAfter() {
		if b.StalledSince.IsZero() {
			b.StalledSince = last
			slog.Warn("builder stalled", "binding", b.Name, "round", b.Round, "quiet", now.Sub(last).Truncate(time.Second))
		}
	} else {
		b.StalledSince = time.Time{}
	}

	if b.StalledSince.IsZero() && p.OutputAt.After(p.TreeAt) && now.Sub(p.TreeAt) >= rt.Policy.ExploreAfter() {
		if b.ExploringSince.IsZero() {
			b.ExploringSince = p.TreeAt
		}
	} else {
		b.ExploringSince = time.Time{}
	}

	return b
}

// anySignal reports whether at least one progress source was ever sampled
// non-empty. A headless stream's mtime counts, which is why it is compared
// against the round's start rather than tested for zero.
func anySignal(b store.Binding, p *store.Progress) bool {
	if p.Tree != "" || p.Output != "" {
		return true
	}
	return p.OutputAt.After(b.RoundStartedAt)
}

// labelsOf renders a binding's progress labels for `relay status` and
// `relay ui` (#135): the working word that replaces "working", and the stale
// word that sits after the state word. Either may be "".
func labelsOf(b store.Binding, now time.Time) (working string, stale string) {
	switch {
	case !b.StalledSince.IsZero():
		working = "stalled " + AgeText(now.Sub(b.StalledSince))
	case !b.ExploringSince.IsZero():
		working = "exploring " + AgeText(now.Sub(b.ExploringSince))
	}
	if !b.StaleSince.IsZero() {
		stale = "stale " + AgeText(now.Sub(b.StaleSince))
	}
	return working, stale
}
