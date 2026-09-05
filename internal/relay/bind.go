package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// splitDirection matches herdr's own guidance for a sibling agent pane.
const splitDirection = "right"

// BindOptions describes one bind request. BuilderPane adopts an existing pane;
// leaving it empty spawns a new one from Alias.
type BindOptions struct {
	Name        string
	Alias       string
	BuilderPane string
	PlannerPane string
	CWD         string
	Resume      bool
}

// Bind ties the calling planner pane to a builder over one working tree.
func Bind(ctx context.Context, rt Runtime, opts BindOptions) (store.Binding, error) {
	if opts.PlannerPane == "" {
		return store.Binding{}, errors.New("no planner pane; is HERDR_PANE_ID set")
	}
	if opts.CWD == "" {
		return store.Binding{}, errors.New("no working directory")
	}

	agents, err := rt.Herdr.ListAgents(ctx)
	if err != nil {
		return store.Binding{}, fmt.Errorf("list agents: %w", err)
	}

	planner, ok := FindAgent(agents, store.Endpoint{PaneID: opts.PlannerPane})
	if !ok {
		return store.Binding{}, fmt.Errorf("no agent in planner pane %s", opts.PlannerPane)
	}

	if opts.Resume {
		return resume(rt, opts, planner)
	}

	return create(ctx, rt, opts, planner)
}

func resume(rt Runtime, opts BindOptions, planner herdr.Agent) (store.Binding, error) {
	var out store.Binding

	// Load-modify-save, so it runs inside the state lock: the daemon writes the
	// same binding on every tick and would otherwise clobber the new planner.
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}

		b.Planner = endpointOf(planner)
		b.State = store.StateActive

		if err := tx.Save(b); err != nil {
			return err
		}

		out = b

		return nil
	})
	if err != nil {
		return store.Binding{}, err
	}

	return out, nil
}

func create(ctx context.Context, rt Runtime, opts BindOptions, planner herdr.Agent) (store.Binding, error) {
	name := opts.Name
	if name == "" {
		name = SanitizeName(baseName(opts.CWD))
	}
	if err := store.ValidName(name); err != nil {
		return store.Binding{}, err
	}

	// Check the working tree before spawning anything. Save re-checks under the
	// lock and stays authoritative, but without this a refused bind would leave
	// a started builder pane stranded with nothing pointing at it.
	other, found, err := rt.Store.FindByCWD(opts.CWD)
	if err != nil {
		return store.Binding{}, err
	}
	if found && other.Name != name && other.State != store.StateDone {
		return store.Binding{}, fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
			opts.CWD, other.Name, other.Builder.PaneID, other.Round, store.ErrCWDTaken)
	}

	builder, err := resolveBuilder(ctx, rt, opts, name, planner.PaneID)
	if err != nil {
		return store.Binding{}, err
	}

	b := store.Binding{
		Name:         name,
		CWD:          opts.CWD,
		Planner:      endpointOf(planner),
		Builder:      builder,
		BuilderAlias: opts.Alias,
		Round:        1,
		State:        store.StateActive,
	}

	if err := rt.Store.Save(b); err != nil {
		// The pre-check passed but the lock disagreed, so a builder pane is now
		// running with no binding. Name it: relay never closes a pane itself.
		return store.Binding{}, fmt.Errorf("bind failed after starting builder in pane %s (close it yourself): %w",
			builder.PaneID, err)
	}

	return b, nil
}

// resolveBuilder adopts an existing builder pane, or splits a sibling pane and
// starts the aliased agent in it. Focus stays with the planner either way.
//
// The alias is looked up only on the spawn path. Adopting a pane needs no
// alias: the human launched that agent themselves, so relay has no kind or
// args to supply -- and for agy, their fish function already activated the
// plan-executor role in that session.
func resolveBuilder(ctx context.Context, rt Runtime, opts BindOptions, name, plannerPane string) (store.Endpoint, error) {
	if opts.BuilderPane != "" {
		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return store.Endpoint{}, fmt.Errorf("list agents: %w", err)
		}
		found, ok := FindAgent(agents, store.Endpoint{PaneID: opts.BuilderPane})
		if !ok {
			return store.Endpoint{}, fmt.Errorf("no agent in builder pane %s", opts.BuilderPane)
		}
		return endpointOf(found), nil
	}

	spec, err := rt.Aliases.Lookup(opts.Alias)
	if err != nil {
		return store.Endpoint{}, err
	}

	paneID, err := rt.Herdr.SplitPane(ctx, plannerPane, splitDirection, opts.CWD)
	if err != nil {
		return store.Endpoint{}, fmt.Errorf("split pane for builder: %w", err)
	}

	agentName := name + "-builder"
	if err := rt.Herdr.StartAgent(ctx, agentName, spec.Kind, paneID, spec.Args); err != nil {
		return store.Endpoint{}, fmt.Errorf("start builder %q: %w", agentName, err)
	}

	return store.Endpoint{AgentName: agentName, PaneID: paneID, Kind: spec.Kind}, nil
}

// endpointOf projects a live herdr agent onto the store's durable endpoint
// shape, used for both a binding's planner and an adopted builder.
func endpointOf(a herdr.Agent) store.Endpoint {
	return store.Endpoint{
		PaneID:    a.PaneID,
		SessionID: a.Session.Value,
		Kind:      a.Kind,
	}
}

// Unbind forgets a binding. It never touches the panes, so the builder's output
// stays on screen for the human to read.
func Unbind(_ context.Context, rt Runtime, name string) error {
	if _, err := rt.Store.Load(name); err != nil {
		return err
	}
	return rt.Store.Delete(name)
}

// SanitizeName coerces a directory name into herdr's agent-name rule.
func SanitizeName(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}

	out := strings.Trim(sb.String(), "-")
	if out == "" {
		return "relay"
	}
	if out[0] < 'a' || out[0] > 'z' {
		out = "b" + out
	}
	if len(out) > 32 {
		out = out[:32]
	}

	return out
}

func baseName(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}
