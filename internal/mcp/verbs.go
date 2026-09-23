package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

// Verbs is what a tools/call dispatches to: the three verbs, each returning
// what the CLI's --json would (or an error, turned into an isError result
// by the caller).
type Verbs interface {
	Status(ctx context.Context, a StatusArgs) (any, error)
	Send(ctx context.Context, a SendArgs) (any, error)
	Done(ctx context.Context, a DoneArgs) (any, error)
}

// RelayVerbs adapts internal/relay's functions to Verbs, resolved against
// one planner (spec docs/specs/2026-09-21-planner-channel-design.md §4, §5;
// keyed by planner id in #303 §4.5).
type RelayVerbs struct {
	RT      relay.Runtime
	Planner string
}

// Status returns relay.Status filtered to this planner's bindings (unless
// a.All), narrowed to a.Name when given, with the same DONE-hiding the CLI
// applies by default.
func (v *RelayVerbs) Status(ctx context.Context, a StatusArgs) (any, error) {
	rep, err := relay.Status(ctx, v.RT)
	if err != nil {
		return nil, err
	}

	if !a.All {
		kept := rep.Bindings[:0:0]
		for _, b := range rep.Bindings {
			if b.PlannerID == v.Planner {
				kept = append(kept, b)
			}
		}
		rep.Bindings = kept
	}

	if a.Name != "" {
		var found *relay.BindingStatus
		for i := range rep.Bindings {
			if rep.Bindings[i].Name == a.Name {
				found = &rep.Bindings[i]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("no binding named %s", a.Name)
		}
		rep.Bindings = []relay.BindingStatus{*found}
	}

	if a.Name == "" && !a.All {
		rep = relay.HideDone(rep)
	}

	return rep, nil
}

// sendResult is relay.SendResult plus the tools-mode background-wait budget:
// the binding's round budget, rendered the way `relay wait --timeout`
// accepts it (#303 §4.5). Empty on a dry run, where no round was opened.
type sendResult struct {
	relay.SendResult
	WaitBudget string `json:"wait_budget,omitempty"`
}

// waitBudget renders a binding's round budget (ms) as a duration string for
// `relay wait --timeout`. A non-positive value reads as "", which leaves the
// wait command out of the send result.
func waitBudget(roundTimeoutMS int) string {
	if roundTimeoutMS <= 0 {
		return ""
	}
	return (time.Duration(roundTimeoutMS) * time.Millisecond).String()
}

// budgetOf pulls the wait budget out of a Send result. A result that is not
// a sendResult (a dry run, or a fake in a test) has none.
func budgetOf(res any) string {
	if sr, ok := res.(sendResult); ok {
		return sr.WaitBudget
	}
	return ""
}

// Send calls relay.Send, or relay.SendDryRun when a.DryRun. AllowYolo is
// always false: escalation to yolo stays on the CLI (spec §2 non-goals).
func (v *RelayVerbs) Send(ctx context.Context, a SendArgs) (any, error) {
	opts := relay.SendOptions{
		Tier:      a.Tier,
		AllowYolo: false,
		Regate:    a.Regate,
		Verify:    a.Verify,
	}

	if a.DryRun {
		d, err := relay.SendDryRun(ctx, v.RT, a.Name, a.File, opts)
		if err != nil {
			return nil, err
		}
		return d, nil
	}

	res, err := relay.Send(ctx, v.RT, a.Name, a.File, opts)
	if err != nil {
		return nil, err
	}
	out := sendResult{SendResult: res}
	// The budget is read back from the binding Send just saved: store fills
	// the default in, so a binding with no --timeout still reads 24h.
	if v.RT.Store != nil {
		if b, lerr := v.RT.Store.Load(a.Name); lerr == nil {
			out.WaitBudget = waitBudget(b.RoundTimeoutMS)
		}
	}
	return out, nil
}

// doneResult is relay.DoneResult plus the CLI's rendered text, so a model
// reading the tool result gets both the structured fields and the sentence
// a human would see.
type doneResult struct {
	relay.DoneResult
	Text string `json:"text"`
}

// Done calls relay.Done and reports relay.DoneText alongside its result.
func (v *RelayVerbs) Done(ctx context.Context, a DoneArgs) (any, error) {
	res, err := relay.Done(ctx, v.RT, a.Name)
	if err != nil {
		return nil, err
	}
	return doneResult{DoneResult: res, Text: relay.DoneText(a.Name, res)}, nil
}
