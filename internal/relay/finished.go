package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// FinishedResult is what one planner's bindings say about the "all rounds
// finished" toast.
type FinishedResult struct {
	Fire  bool     // the toast should be raised now
	Names []string // the bindings whose FinishPending it clears, sorted
}

// roundInFlight reports whether something is still running on the
// planner's behalf for this binding (#182): an open round on an active
// builder, a held or undelivered payload, or a broken builder the daemon
// is about to switch. A NEEDS YOU, orphaned, non-switchable broken or DONE
// binding is not in flight: the human already has its notification.
func roundInFlight(b store.Binding, pending bool) bool {
	switch {
	case b.State == store.StateHeld:
		return true
	case pending:
		return true
	case b.State == store.StateActive && !b.RoundStartedAt.IsZero():
		return true
	case b.State == store.StateBroken && switchable(b):
		return true
	default:
		return false
	}
}

// FinishedGroup decides the toast for one planner's bindings. Pure.
// inFlight is roundInFlight with the pending lookup bound by the caller.
func FinishedGroup(bs []store.Binding, plannerIdle bool, inFlight func(store.Binding) bool) FinishedResult {
	if !plannerIdle {
		return FinishedResult{}
	}

	var names []string
	for _, b := range bs {
		if inFlight(b) {
			return FinishedResult{}
		}
		if b.FinishPending {
			names = append(names, b.Name)
		}
	}
	if len(names) == 0 {
		return FinishedResult{}
	}

	sort.Strings(names)
	return FinishedResult{Fire: true, Names: names}
}

// finishedTitle and finishedBody render the "all rounds finished" toast
// (#182): the title names every binding that finished, the body gives one
// line per binding naming the round it closed and the state it landed in.
func finishedTitle(names []string) string {
	return strings.Join(names, ", ") + ": all rounds finished"
}

func finishedBody(bs []store.Binding, names []string) string {
	byName := make(map[string]store.Binding, len(bs))
	for _, b := range bs {
		byName[b.Name] = b
	}

	lines := make([]string, 0, len(names))
	for _, name := range names {
		b := byName[name]
		lines = append(lines, fmt.Sprintf("%s round %d %s", name, b.Round-1, strings.ToLower(displayState(b.State))))
	}
	return strings.Join(lines, "\n")
}

// notifyFinished groups bindings by Planner.PaneID, evaluates FinishedGroup
// per group (plannerIdle: FindAgent(agents, b.Planner) found with Status
// idle or done; a planner not found is never idle), and for each firing
// group raises ONE toast then clears FinishPending on its Names under one
// store lock, re-loading each binding first (a concurrent unbind is
// ErrNotFound and skipped). A Notify error is slog.Warn and the flags are
// NOT cleared (retry next tick).
func notifyFinished(ctx context.Context, rt Runtime, bindings []store.Binding, agents []herdr.Agent) {
	groups := make(map[string][]store.Binding)
	for _, b := range bindings {
		if b.Planner.PaneID == "" {
			continue
		}
		groups[b.Planner.PaneID] = append(groups[b.Planner.PaneID], b)
	}

	for pane, bs := range groups {
		planner, found := FindAgent(agents, bs[0].Planner)
		idle := found && (planner.Status == herdr.StatusIdle || planner.Status == herdr.StatusDone)

		res := FinishedGroup(bs, idle, func(b store.Binding) bool {
			_, pending, _ := rt.Store.PendingForPlanner(b.Name)
			return roundInFlight(b, pending)
		})
		if !res.Fire {
			continue
		}

		if err := rt.Herdr.Notify(ctx, finishedTitle(res.Names), finishedBody(bs, res.Names), herdr.SoundDone); err != nil {
			slog.Warn("all rounds finished notify failed", "planner", pane, "bindings", res.Names, "err", err)
			continue
		}

		err := rt.Store.WithLock(func(tx *store.Tx) error {
			for _, name := range res.Names {
				b, err := tx.Load(name)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				b.FinishPending = false
				if err := tx.Save(b); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			slog.Warn("clear finish pending failed", "planner", pane, "bindings", res.Names, "err", err)
			continue
		}

		slog.Info("all rounds finished", "planner", pane, "bindings", res.Names)
	}
}
