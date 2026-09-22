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
	Dropped []store.Consult // terminal record dropped
}

// Reap drops the records of terminal consults. Running consults are never
// touched, and reap never kills a process.
func Reap(_ context.Context, rt Runtime, opts ReapOptions) ([]ReapResult, error) {
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
			if errors.Is(err, store.ErrNotFound) && opts.All {
				// A `relay unbind` landed between List and here. The lock is
				// taken per binding, not per sweep, so this race is real.
				// Skipping matches the daemon's tick, which resolves the same
				// List-then-Load race the same way (daemon.go:82). A sweep must
				// not abandon every binding after the one that vanished.
				//
				// Deliberately NOT extended to a named binding: there,
				// ErrNotFound is the human's typo and must surface.
				return nil
			}
			if err != nil {
				return err
			}

			keep := make([]store.Consult, 0, len(b.Consults))
			for _, c := range b.Consults {
				if c.State == store.ConsultRunning {
					keep = append(keep, c)
					continue
				}
				if c.State == store.ConsultSpawning {
					keep = append(keep, c)
					continue
				}
				res.Dropped = append(res.Dropped, c)
				if opts.DryRun {
					keep = append(keep, c)
				}
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
