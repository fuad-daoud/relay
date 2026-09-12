package relay

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
)

// agentGone is the status shown for a binding endpoint herdr no longer knows.
const agentGone = "gone"

// BindingStatus is one row of relay status: stored binding plus live herdr state.
type BindingStatus struct {
	Name             string `json:"name"`
	CWD              string `json:"cwd"`
	Workspace        string `json:"workspace"`
	Round            int    `json:"round"`
	State            string `json:"state"`
	Display          string `json:"display"`
	BuilderCandidate string `json:"builder_candidate"`
	PlannerPane      string `json:"planner_pane"`
	PlannerKind      string `json:"planner_kind"`
	PlannerStatus    string `json:"planner_status"`
	PlannerFocus     bool   `json:"planner_focused"`
	BuilderPane      string `json:"builder_pane"`
	BuilderKind      string `json:"builder_kind"`
	BuilderStatus    string `json:"builder_status"`
	// Detail explains an overloaded state where the display word cannot.
	// Populated only for store.StateBroken, which covers three situations
	// whose correct recoveries differ -- and in one of which the obvious
	// recovery orphans a builder that is still running.
	Detail  string       `json:"detail,omitempty"`
	Last    *LastEvent   `json:"last,omitempty"`
	Pending *PendingInfo `json:"pending,omitempty"`
	// Nudge is set while the current round has been nudged and no report has
	// arrived: when relay nudged, and how long the builder's terminal has been
	// unchanged against the grace after which relay scrapes it. Nil otherwise.
	Nudge *NudgeInfo `json:"nudge,omitempty"`
	// Foreign lists live agents occupying this binding's working tree that no
	// binding accounts for. It is an observation, never a judgement: relay
	// cannot see writes, so a sanctioned read-only researcher and a rogue
	// implementer both land here and the human reads the title to tell them
	// apart. Deliberately does not affect Display.
	Foreign []ForeignAgent `json:"foreign,omitempty"`
	// SubAgents is the builder harness's sub-agent visibility
	// (harness.Harness.SubAgents): "separate", "foreground", or "hidden".
	// Empty when the builder kind is not in the harness table. It is the
	// fact, not the rendered coverage row, so the TUI or a script can branch
	// on it. Deliberately does not affect Display.
	SubAgents     string `json:"sub_agents,omitempty"`
	ForkedFrom    string `json:"forked_from,omitempty"`
	ForkedAtRound int    `json:"forked_at_round,omitempty"`
	// Consults is how many consults are reserved or running on this binding.
	// Terminal ones are omitted: they are a reap chore, not work in flight.
	Consults int `json:"consults,omitempty"`
	// Switches is builder switches in the current round (#61 step 6); zero is
	// omitted.
	Switches int `json:"switches,omitempty"`
}

// LastEvent is the most recent relayed message, carried as data rather than
// prose. RenderStatus formats it for a human; a statusline consumer reads the
// fields directly instead of parsing a sentence apart.
type LastEvent struct {
	TS        time.Time       `json:"ts"`
	Round     int             `json:"round"`
	Direction store.Direction `json:"direction"`
	Kind      store.Kind      `json:"kind"`
	// Note is the entry's note, when it has one. A nudge is a plan entry to
	// the builder and a scrape is a report entry to the planner, so without
	// it the last line after either reads exactly like the ordinary case.
	Note string `json:"note,omitempty"`
}

// PendingInfo describes a payload waiting on the planner.
type PendingInfo struct {
	Round int        `json:"round"`
	Kind  store.Kind `json:"kind"`
	// Hold is the daemon's quiet clock for a HELD binding: how long the
	// planner's screen has been unchanged, against the grace it will be
	// injected at. Nil when the binding is not held, and nil when it is held
	// but the clock has not started -- a failed screen read, or a hold
	// recorded before the daemon read the screen. The human should know the
	// clock is not running.
	Hold *HoldInfo `json:"hold,omitempty"`
}

// HoldInfo is the quiet clock carried as data, like LastEvent: a statusline
// consumer reads the two numbers, and RenderStatus formats them. Milliseconds
// as ints, the way RoundTimeoutMS is, not time.Duration's nanoseconds.
type HoldInfo struct {
	QuietMS int `json:"quiet_ms"`
	// GraceMS is zero when the binding was held by a daemon that did not
	// record its grace (state written before HeldGrace existed). status then
	// shows the quiet time alone rather than guess a fraction.
	GraceMS int `json:"grace_ms,omitempty"`
}

// NudgeInfo is the quiescence clock carried as data, like HoldInfo. The
// grace needs no state field: nudgeGrace is a constant in this package, so
// status knows it without the daemon writing it down.
type NudgeInfo struct {
	At      time.Time `json:"at"`
	QuietMS int       `json:"quiet_ms"`
	GraceMS int       `json:"grace_ms"`
}

// NudgeText is the human form of the quiescence clock: "quiet 23s of 1m0s".
func NudgeText(n NudgeInfo) string {
	quiet := (time.Duration(n.QuietMS) * time.Millisecond).Truncate(time.Second)
	return fmt.Sprintf("quiet %s of %s", quiet, time.Duration(n.GraceMS)*time.Millisecond)
}

// Report is the whole status surface.
type Report struct {
	Bindings []BindingStatus `json:"bindings"`
	// DoneHidden is the number of DONE rows HideDone removed; zero and absent
	// whenever nothing was filtered, so a consumer that never learned the
	// field sees the document it always did.
	DoneHidden int `json:"done_hidden,omitempty"`
	// Gated lists every live ledger gate against the configured candidates,
	// independent of any binding: a rate limit or spawn failure exists
	// whether or not a builder is currently running it. Absent from JSON
	// when nothing is gated, so a consumer that never learned the field
	// sees the document it always did.
	Gated []ledger.Gate `json:"gated,omitempty"`
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

	// Computed once, not per binding: it spans every binding, so it does not
	// vary across rows.
	known := knownEndpoints(bindings)

	rows := make([]BindingStatus, 0, len(bindings))
	for _, b := range bindings {
		row, err := statusRow(rt, b, agents, known)
		if err != nil {
			return Report{}, err
		}
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	rep := Report{Bindings: rows}
	rep.Gated = Gates(rt)
	return rep, nil
}

// statusRow is read-only, so it reaches the store through the self-locking
// *store.Store methods directly rather than a *store.Tx: there is no
// load-modify-save here for WithLock to protect.
func statusRow(rt Runtime, b store.Binding, agents []herdr.Agent, known []store.Endpoint) (BindingStatus, error) {
	row := BindingStatus{
		Name: b.Name, CWD: b.CWD, Round: b.Round,
		State: string(b.State), Display: displayState(b.State),
		BuilderCandidate: b.BuilderCandidate,
		ForkedFrom:       b.ForkedFrom,
		ForkedAtRound:    b.ForkedAtRound,
		Consults:         runningConsults(b),
		Switches:         b.RoundSwitches,
		PlannerPane:      b.Planner.PaneID, PlannerKind: b.Planner.Kind, PlannerStatus: agentGone,
		BuilderPane: b.Builder.PaneID, BuilderKind: b.Builder.Kind, BuilderStatus: agentGone,
	}

	// What relay acts on is what it shows (spec §7.4).
	if a, ok := FindAgent(agents, b.Planner); ok {
		row.PlannerStatus = effectiveStatus(b.Planner, a)
		row.PlannerFocus = a.Focused
		row.PlannerPane = a.PaneID
		row.Workspace = a.WorkspaceID
	}
	if a, ok := FindAgent(agents, b.Builder); ok {
		row.BuilderStatus = effectiveStatus(b.Builder, a)
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
			Note: last.Note,
		}
	}

	// What relay acts on is what it shows: the clock starts where
	// builderQuiescent starts it -- at the last fingerprint, falling back to
	// the nudge itself when none has been taken yet.
	if nudgedAt, ok := nudgeTime(entries, b.Round); ok {
		since := b.BuilderScreenAt
		if since.IsZero() {
			since = nudgedAt
		}
		quiet := rt.Now().UTC().Sub(since)
		if quiet < 0 {
			quiet = 0
		}
		row.Nudge = &NudgeInfo{
			At:      nudgedAt,
			QuietMS: int(quiet / time.Millisecond),
			GraceMS: int(nudgeGrace / time.Millisecond),
		}
	}

	pending, found, err := rt.Store.PendingForPlanner(b.Name)
	if err != nil {
		return BindingStatus{}, err
	}
	if found {
		row.Pending = &PendingInfo{Round: pending.Round, Kind: pending.Kind}
		if b.State == store.StateHeld && b.PlannerScreen != "" {
			quiet := rt.Now().UTC().Sub(b.PlannerScreenAt)
			if quiet < 0 {
				quiet = 0
			}
			row.Pending.Hold = &HoldInfo{
				QuietMS: int(quiet / time.Millisecond),
				GraceMS: int(b.HeldGrace / time.Millisecond),
			}
		}
	}

	row.Foreign = ForeignAgents(agents, known, b.CWD)

	if h, ok := harness.Lookup(b.Builder.Kind); ok {
		row.SubAgents = string(h.SubAgents)
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

// HoldText is the human form of a held binding's clock, shared by
// RenderStatus and the TUI so the two never drift: "quiet 23s of 1m0s",
// "quiet 23s" when the grace is unknown, or "waiting for the planner's
// screen" when the clock has not started. Empty for any binding that is
// not HELD with a pending payload.
func HoldText(b BindingStatus) string {
	if b.Display != "HELD" || b.Pending == nil {
		return ""
	}
	h := b.Pending.Hold
	if h == nil {
		return "waiting for the planner's screen"
	}
	quiet := (time.Duration(h.QuietMS) * time.Millisecond).Truncate(time.Second)
	if h.GraceMS == 0 {
		return fmt.Sprintf("quiet %s", quiet)
	}
	return fmt.Sprintf("quiet %s of %s", quiet, time.Duration(h.GraceMS)*time.Millisecond)
}

// HideDone returns a copy of r excluding every binding whose state is DONE.
// The order of the remaining bindings is preserved. DoneHidden is set to
// the count of removed bindings. The input report is not modified.
func HideDone(r Report) Report {
	out := Report{
		Bindings:   make([]BindingStatus, 0, len(r.Bindings)),
		DoneHidden: 0,
		// Gated is machine-wide, not per binding: hiding DONE rows must not
		// hide a rate limit (#61). Found by rendering a hand-written ledger
		// through the real binary; the renderer tests could not see it.
		Gated: r.Gated,
	}
	for _, b := range r.Bindings {
		if b.State == string(store.StateDone) {
			out.DoneHidden++
		} else {
			out.Bindings = append(out.Bindings, b)
		}
	}
	return out
}

// writeGatedBlock renders the "candidates" block RenderStatus, `relay
// candidates` and `relay doctor` all draw from the same ledger.Gate slice
// for: one row per live gate, sharing GateKindText/GateUntilText so the
// wording never drifts between renderers. trailingBlank adds one blank
// line after the block, only when a footer follows it in the output.
func writeGatedBlock(sb *strings.Builder, gates []ledger.Gate, trailingBlank bool) {
	sb.WriteString("candidates\n")

	width := 0
	for _, g := range gates {
		if len(g.Token) > width {
			width = len(g.Token)
		}
	}

	for _, g := range gates {
		fmt.Fprintf(sb, "  %-*s  %-12s  %s  %s",
			width, g.Token, GateKindText(g.Kind), g.Since.Local().Format("15:04"), GateUntilText(g.Until))
		if g.Note != "" {
			fmt.Fprintf(sb, "  %s", g.Note)
		}
		if g.Binding != "" {
			fmt.Fprintf(sb, "  (%s)", g.Binding)
		}
		sb.WriteString("\n")
	}

	if trailingBlank {
		sb.WriteString("\n")
	}
}

// RenderStatus formats a Report for a terminal.
func RenderStatus(r Report) string {
	var sb strings.Builder

	switch {
	case len(r.Bindings) == 0 && r.DoneHidden > 0:
		// The footer says "clear" and not "free": gc frees disk only for
		// relay-created worktrees, and the footer must not overpromise.
		fmt.Fprintf(&sb, "%d done · relay gc to clear\n", r.DoneHidden)
		if len(r.Gated) > 0 {
			writeGatedBlock(&sb, r.Gated, false)
		}
		return sb.String()

	case len(r.Bindings) == 0 && len(r.Gated) == 0:
		return "no bindings\n"

	case len(r.Bindings) == 0:
		sb.WriteString("no bindings\n")
		writeGatedBlock(&sb, r.Gated, false)
		return sb.String()
	}

	for _, b := range r.Bindings {
		fmt.Fprintf(&sb, "%-8s %-40s %-4s round %-3d %s",
			b.Name, b.CWD, b.Workspace, b.Round, b.Display)
		// Zero stays out of the row entirely: +0c on every healthy binding
		// would be noise, not information.
		if b.Consults > 0 {
			fmt.Fprintf(&sb, " +%dc", b.Consults)
		}
		fmt.Fprint(&sb, "\n")
		focus := ""
		if b.PlannerFocus {
			focus = "  (focused)"
		}
		fmt.Fprintf(&sb, "  planner  %-14s %-8s %s%s\n",
			b.PlannerPane, b.PlannerKind, b.PlannerStatus, focus)
		fmt.Fprintf(&sb, "  builder  %-14s %-8s %-9s `%s`",
			b.BuilderPane, b.BuilderKind, b.BuilderStatus, b.BuilderCandidate)
		if b.Switches > 0 {
			fmt.Fprintf(&sb, "   switched %dx", b.Switches)
		}
		fmt.Fprint(&sb, "\n")
		for _, fa := range b.Foreign {
			loc := ""
			if fa.CWD != b.CWD {
				loc = fa.CWD
				if rel, err := filepath.Rel(b.CWD, fa.CWD); err == nil {
					loc = rel
				}
			}
			fmt.Fprintf(&sb, "  foreign  %-14s %-8s %-9s %s",
				fa.PaneID, fa.Kind, fa.Status, fa.Title)
			if loc != "" {
				fmt.Fprintf(&sb, "  %s", loc)
			}
			fmt.Fprint(&sb, "\n")
		}
		if note, ok := SubAgentCoverage(b.BuilderKind, harness.SubAgentVisibility(b.SubAgents)); ok {
			fmt.Fprintf(&sb, "  %-8s %s\n", "coverage", note)
		}
		if b.Detail != "" {
			fmt.Fprintf(&sb, "  detail   %s\n", b.Detail)
		}
		if b.Nudge != nil {
			fmt.Fprintf(&sb, "  nudge    %s  %s\n",
				b.Nudge.At.Local().Format("15:04:05"), NudgeText(*b.Nudge))
		}
		if b.Last != nil {
			fmt.Fprintf(&sb, "  last     %s %s %s round %d",
				b.Last.TS.Local().Format("15:04:05"), b.Last.Kind, b.Last.Direction, b.Last.Round)
			if b.Last.Note != "" {
				fmt.Fprintf(&sb, " (%s)", b.Last.Note)
			}
			fmt.Fprint(&sb, "\n")
		}
		if b.Pending != nil {
			fmt.Fprintf(&sb, "  pending  %s round %d -> planner", b.Pending.Kind, b.Pending.Round)
			if hold := HoldText(b); hold != "" {
				fmt.Fprintf(&sb, ", held: %s", hold)
			}
			fmt.Fprint(&sb, "\n\n")
		} else {
			fmt.Fprint(&sb, "  pending  --\n\n")
		}
	}

	if len(r.Gated) > 0 {
		writeGatedBlock(&sb, r.Gated, r.DoneHidden > 0)
	}

	if r.DoneHidden > 0 {
		// The footer says "clear" and not "free": gc frees disk only for
		// relay-created worktrees, and the footer must not overpromise.
		fmt.Fprintf(&sb, "%d done · relay gc to clear\n", r.DoneHidden)
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
