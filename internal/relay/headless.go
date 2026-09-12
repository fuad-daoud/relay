package relay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrBuilderBusy reports a send against a headless binding whose previous
// round's process is still running (headless spec §5.2). One process per
// round is the model; two at once in one tree would race each other's
// edits.
var ErrBuilderBusy = errors.New("builder's previous process is still running; wait for its report, or relay done")

// handleOf is the endpoint's stored process fields as the Runner's handle.
// StartedAt is Unix seconds on the endpoint (store spec §3.1, amended).
func handleOf(e store.Endpoint) ProcHandle {
	return ProcHandle{PID: e.PID, StartedAt: time.Unix(e.StartedAt, 0)}
}

// defaultRoundBudget mirrors the store's default round timeout, for a
// binding that was never saved. Store.Save fills RoundTimeoutMS on every
// real binding, so this is a guard, not a policy.
const defaultRoundBudget = 24 * time.Hour

// roundBudget is the binding's round budget as a duration: what agy's
// --print-timeout gets, so the harness's own default (5m) never cuts a
// round short (headless spec §3.5).
func roundBudget(b store.Binding) time.Duration {
	if b.RoundTimeoutMS <= 0 {
		return defaultRoundBudget
	}
	return time.Duration(b.RoundTimeoutMS) * time.Millisecond
}

// headlessLaunch renders the argv for one headless round: the candidate's
// binary, then its print form with the prompt and the budget filled in
// (headless spec §4.2). Pure. An unknown kind is an error, not a panic:
// Load validated the set, but a binding written by a future relay could
// name a kind this one does not know.
func headlessLaunch(c candidate.Candidate, role harness.RoleSpec, budget time.Duration, prompt string) ([]string, error) {
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return nil, fmt.Errorf("unknown harness kind %q", c.Harness)
	}
	l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)
	if l.PromptAt < 0 {
		return nil, fmt.Errorf("harness %q has no print form", c.Harness)
	}
	return append([]string{h.Binary}, l.PrintArgs(prompt, budget)...), nil
}

// startRound starts the round's process for a headless binding and records
// its handle on the endpoint (headless spec §4.3). The caller holds the
// state lock, has staged the plan, and saves what comes back.
//
// Preconditions:  b.Builder.Headless(); no live process on the endpoint
// (Send checks with Runner.Alive first); rt.Runner non-nil.
// Postconditions: on success PID, StartedAt and LogPath describe the new
// process. On failure the endpoint is returned as it was, PID 0, and the
// candidate's spawn_failed is in the ledger -- the same record a pane spawn
// failure leaves, because it is the same failure: the candidate could not
// be launched. The caller decides the binding's state.
func startRound(ctx context.Context, rt Runtime, b store.Binding, prompt string) (store.Binding, error) {
	if rt.Runner == nil {
		return b, ErrRunnerUnavailable
	}
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	role, _ := harness.RoleByName("builder")
	argv, err := headlessLaunch(c, role, roundBudget(b), prompt)
	if err != nil {
		return b, err
	}
	logPath := rt.Store.BuilderLogPath(b.Name, b.Round)
	h, err := rt.Runner.Start(ctx, ProcSpec{Dir: b.CWD, Argv: argv, LogPath: logPath})
	if err != nil {
		recordSpawnFailureLocked(rt, c.Ref().String(), b.Name, err)
		return b, fmt.Errorf("start headless builder for %q (%s): %w", b.Name, c.Ref().String(), err)
	}
	b.Builder.PID = h.PID
	b.Builder.StartedAt = h.StartedAt.Unix()
	b.Builder.LogPath = logPath
	return b, nil
}
