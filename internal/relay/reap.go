package relay

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relay/internal/store"
)

// ErrUnknownConsult reports a reap naming a consult no binding holds.
var ErrUnknownConsult = errors.New("no such consult")

// ReapOptions selects what to reap.
type ReapOptions struct {
	Name   string // one binding; ignored when All is set
	All    bool
	DryRun bool
}

// ReapResult is what one binding's reap did.
type ReapResult struct {
	Binding string
	Closed  []store.Consult // pane closed, record dropped
	Failed  []store.Consult // close failed, record kept for a retry
}

// Reap closes the panes of terminal consults and drops their records.
//
// This is the only place relay closes a pane, it closes only panes relay
// spawned itself, and it runs because a human or a planner asked -- never
// because relay judged an outcome. docs/design.md's "Relay never kills a pane"
// is amended to name this one command.
//
// Running consults are never touched. A failed close keeps the record so a
// retry is possible and never fails the sweep: one unreachable pane must not
// strand the rest.
func Reap(ctx context.Context, rt Runtime, opts ReapOptions) ([]ReapResult, error) {
	var names []string

	if opts.All {
		bindings, err := rt.Store.List()
		if err != nil {
			return nil, err
		}
		for _, b := range bindings {
			names = append(names, b.Name)
		}
	} else {
		if opts.Name == "" {
			return nil, fmt.Errorf("relay reap needs a binding name or --all")
		}
		names = []string{opts.Name}
	}

	var out []ReapResult
	for _, name := range names {
		res := ReapResult{Binding: name}

		err := rt.Store.WithLock(func(tx *store.Tx) error {
			b, err := tx.Load(name)
			if err != nil {
				return err
			}

			keep := make([]store.Consult, 0, len(b.Consults))
			for _, c := range b.Consults {
				if c.State == store.ConsultRunning {
					keep = append(keep, c)
					continue
				}
				if opts.DryRun {
					res.Closed = append(res.Closed, c)
					keep = append(keep, c)
					continue
				}
				if err := rt.Herdr.ClosePane(ctx, c.Endpoint.PaneID); err != nil {
					res.Failed = append(res.Failed, c)
					keep = append(keep, c)
					continue
				}
				res.Closed = append(res.Closed, c)
			}

			if opts.DryRun {
				return nil
			}

			b.Consults = keep
			return tx.Save(b)
		})
		if err != nil {
			return out, err
		}

		out = append(out, res)
	}

	return out, nil
}
