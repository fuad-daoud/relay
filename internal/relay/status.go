package relay

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/store"
)

// agentGone is the status shown for a binding endpoint herdr no longer knows.
const agentGone = "gone"

// BindingStatus is one row of relay status: stored binding plus live herdr state.
type BindingStatus struct {
	Name          string `json:"name"`
	CWD           string `json:"cwd"`
	Workspace     string `json:"workspace"`
	Round         int    `json:"round"`
	State         string `json:"state"`
	Display       string `json:"display"`
	BuilderAlias  string `json:"builder_alias"`
	PlannerPane   string `json:"planner_pane"`
	PlannerKind   string `json:"planner_kind"`
	PlannerStatus string `json:"planner_status"`
	PlannerFocus  bool   `json:"planner_focused"`
	BuilderPane   string `json:"builder_pane"`
	BuilderKind   string `json:"builder_kind"`
	BuilderStatus string `json:"builder_status"`
	// Detail explains an overloaded state where the display word cannot.
	// Populated only for store.StateBroken, which covers three situations
	// whose correct recoveries differ -- and in one of which the obvious
	// recovery orphans a builder that is still running.
	Detail        string       `json:"detail,omitempty"`
	Last          *LastEvent   `json:"last,omitempty"`
	Pending       *PendingInfo `json:"pending,omitempty"`
	ForkedFrom    string       `json:"forked_from,omitempty"`
	ForkedAtRound int          `json:"forked_at_round,omitempty"`
}

// LastEvent is the most recent relayed message, carried as data rather than
// prose. RenderStatus formats it for a human; a statusline consumer reads the
// fields directly instead of parsing a sentence apart.
type LastEvent struct {
	TS        time.Time       `json:"ts"`
	Round     int             `json:"round"`
	Direction store.Direction `json:"direction"`
	Kind      store.Kind      `json:"kind"`
}

// PendingInfo describes a payload waiting on the planner.
type PendingInfo struct {
	Round int        `json:"round"`
	Kind  store.Kind `json:"kind"`
}

// Report is the whole status surface.
type Report struct {
	Bindings []BindingStatus `json:"bindings"`
}

// Status derives every row live from herdr, so it cannot disagree with reality.
func Status(ctx context.Context, rt Runtime) (Report, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return Report{}, err
	}

	agents, err := rt.Herdr.ListAgents(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("list agents: %w", err)
	}

	rows := make([]BindingStatus, 0, len(bindings))
	for _, b := range bindings {
		row, err := statusRow(rt, b, agents)
		if err != nil {
			return Report{}, err
		}
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	return Report{Bindings: rows}, nil
}

// statusRow is read-only, so it reaches the store through the self-locking
// *store.Store methods directly rather than a *store.Tx: there is no
// load-modify-save here for WithLock to protect.
func statusRow(rt Runtime, b store.Binding, agents []herdr.Agent) (BindingStatus, error) {
	row := BindingStatus{
		Name: b.Name, CWD: b.CWD, Round: b.Round,
		State: string(b.State), Display: displayState(b.State),
		BuilderAlias:  b.BuilderAlias,
		ForkedFrom:    b.ForkedFrom,
		ForkedAtRound: b.ForkedAtRound,
		PlannerPane:   b.Planner.PaneID, PlannerKind: b.Planner.Kind, PlannerStatus: agentGone,
		BuilderPane: b.Builder.PaneID, BuilderKind: b.Builder.Kind, BuilderStatus: agentGone,
	}

	if a, ok := FindAgent(agents, b.Planner); ok {
		row.PlannerStatus = a.Status
		row.PlannerFocus = a.Focused
		row.PlannerPane = a.PaneID
		row.Workspace = a.WorkspaceID
	}
	if a, ok := FindAgent(agents, b.Builder); ok {
		row.BuilderStatus = a.Status
		row.BuilderPane = a.PaneID
	}

	// Only broken is overloaded: it means "builder pane is gone", which covers
	// a clean exit, a mid-round exit, and a pane that merely moved workspaces
	// while the agent kept running. orphaned and needs_you are unambiguous.
	if b.State == store.StateBroken {
		row.Detail = DiagnoseBuilder(b).Detail(b.Round)
	}

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		return BindingStatus{}, err
	}
	if n := len(entries); n > 0 {
		last := entries[n-1]
		row.Last = &LastEvent{
			TS: last.TS, Round: last.Round,
			Direction: last.Direction, Kind: last.Kind,
		}
	}

	pending, found, err := rt.Store.PendingForPlanner(b.Name)
	if err != nil {
		return BindingStatus{}, err
	}
	if found {
		row.Pending = &PendingInfo{Round: pending.Round, Kind: pending.Kind}
	}

	return row, nil
}

// displayState collapses six stored states into the three the human cares
// about. broken and orphaned both mean "a human must act".
func displayState(s store.State) string {
	switch s {
	case store.StateHeld:
		return "HELD"
	case store.StateNeedsYou, store.StateBroken, store.StateOrphaned:
		return "NEEDS YOU"
	case store.StateDone:
		return "DONE"
	default:
		return "ACTIVE"
	}
}

// RenderStatus formats a Report for a terminal.
func RenderStatus(r Report) string {
	if len(r.Bindings) == 0 {
		return "no bindings\n"
	}

	var sb strings.Builder
	for _, b := range r.Bindings {
		fmt.Fprintf(&sb, "%-8s %-40s %-4s round %-3d %s\n",
			b.Name, b.CWD, b.Workspace, b.Round, b.Display)
		focus := ""
		if b.PlannerFocus {
			focus = "  (focused)"
		}
		fmt.Fprintf(&sb, "  planner  %-14s %-8s %s%s\n",
			b.PlannerPane, b.PlannerKind, b.PlannerStatus, focus)
		fmt.Fprintf(&sb, "  builder  %-14s %-8s %-9s `%s`\n",
			b.BuilderPane, b.BuilderKind, b.BuilderStatus, b.BuilderAlias)
		if b.Last != nil {
			fmt.Fprintf(&sb, "  last     %s %s %s round %d\n",
				b.Last.TS.Local().Format("15:04:05"), b.Last.Kind, b.Last.Direction, b.Last.Round)
		}
		if b.Pending != nil {
			fmt.Fprintf(&sb, "  pending  %s round %d -> planner\n\n", b.Pending.Kind, b.Pending.Round)
		} else {
			fmt.Fprint(&sb, "  pending  --\n\n")
		}
	}

	return sb.String()
}

// Done stops relaying for a binding once the planner has verified the work.
func Done(ctx context.Context, rt Runtime, name string) error {
	// Load-modify-save, so it runs inside the state lock: the daemon rewrites
	// this binding on every tick and would otherwise resurrect it by saving a
	// pre-Done snapshot back over the top. Reach state only through tx here --
	// rt.Store.Load/Save would try to take the lock a second time and Go
	// mutexes are not reentrant.
	return rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}

		oldState := b.State
		b.State = store.StateDone

		if err := tx.Save(b); err != nil {
			return err
		}

		if rt.Hooks != nil && oldState != store.StateDone {
			rt.Hooks.Dispatch(ctx, hooks.Event{
				Type:      hooks.EventStateChanged,
				BindingID: b.Name,
				State:     string(store.StateDone),
				OldState:  string(oldState),
				Round:     b.Round,
				Timestamp: rt.Now().UTC(),
			})
		}

		return nil
	})
}
