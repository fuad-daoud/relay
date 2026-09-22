package mcp

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relay/internal/relay"
)

// Verbs is what a tools/call dispatches to: the four verbs, each returning
// what the CLI's --json would (or an error, turned into an isError result
// by the caller).
type Verbs interface {
	Status(ctx context.Context, a StatusArgs) (any, error)
	Send(ctx context.Context, a SendArgs) (any, error)
	Answer(ctx context.Context, a AnswerArgs) (any, error)
	Done(ctx context.Context, a DoneArgs) (any, error)
}

// RelayVerbs adapts internal/relay's functions to Verbs, resolved against
// one planner pane (spec docs/specs/2026-09-21-planner-channel-design.md §4,
// §5).
type RelayVerbs struct {
	RT   relay.Runtime
	Pane string
}

// Status returns relay.Status filtered to this pane's bindings (unless
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
			if b.PlannerPane == v.Pane {
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
	return res, nil
}

// Answer calls relay.Answer, then reports relay.AnswerText the way the CLI
// prints it.
func (v *RelayVerbs) Answer(ctx context.Context, a AnswerArgs) (any, error) {
	in := relay.AnswerInput{Keys: a.Keys, Text: a.Text, Choice: a.Choice}
	if err := relay.Answer(ctx, v.RT, a.Name, in); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "text": relay.AnswerText(a.Name)}, nil
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
