package relay

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/remote/client"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// agentGone is the status shown for a binding endpoint herdr no longer knows.
const agentGone = "gone"

// agentUnknown is the status for a pane endpoint relay could not look up
// because herdr did not answer. It means "relay did not ask and must not
// claim absence", the same word headlessStatus and the remote path use for
// "could not determine".
const agentUnknown = "unknown"

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
	// Headless is set for a headless builder (#99): its process state and
	// log. BuilderPane reads "headless" and BuilderStatus is one of idle,
	// working, exited N, exited, unknown. Nil for a pane builder.
	Headless *HeadlessInfo `json:"headless,omitempty"`
	// Detail explains an overloaded state where the display word cannot.
	// Populated only for store.StateBroken, which covers three situations
	// whose correct recoveries differ -- and in one of which the obvious
	// recovery orphans a builder that is still running.
	Detail string     `json:"detail,omitempty"`
	Last   *LastEvent `json:"last,omitempty"`
	// LastSeq is the Seq of the newest log entry; 0 when the log is empty.
	// No omitempty: a consumer reads 0 as "nothing yet".
	LastSeq int `json:"last_seq"`
	// LastPayload is the most recent plan/report/question/answer entry --
	// the four kinds that cross between planner and builder -- as opposed to
	// Last, which is the most recent entry of any kind including relay's own
	// bookkeeping (drift, pick, switch, exit, diff). Nil when the log has
	// none.
	LastPayload *LastEvent `json:"last_payload,omitempty"`
	// LastClose is the newest diff entry's commit facts; nil when the log
	// has no diff entry.
	LastClose *CloseInfo `json:"last_close,omitempty"`
	// LastUsage is the newest report entry's usage (#142); nil when no
	// report entry carries one -- every round closed before #185, and
	// every round not yet closed. Spend sums every report and findings
	// entry that carries usage; nil when none does. Both omitted from JSON
	// when nil so a consumer that never learned them sees the document it
	// always did.
	LastUsage *usage.Usage `json:"last_usage,omitempty"`
	Spend     *usage.Spend `json:"spend,omitempty"`
	// LiveUsage is what the open round has consumed so far, read from the
	// harness's record on this call (#234). nil when no round is open or
	// nothing is readable yet. Never recorded, never summed into Spend.
	LiveUsage *usage.Usage `json:"live_usage,omitempty"`
	// Dirty is the rendered rule: the newest close left the tree dirty and
	// no newer round has been sent, so the uncommitted work is still what
	// the tree holds. False once a round is running -- a dirty tree is then
	// the expected state.
	Dirty   bool         `json:"dirty"`
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
	// Branch is the binding's worktree branch; "" for a --cwd binding,
	// which has no worktree of its own.
	Branch string `json:"branch,omitempty"`
	// Waiting is set when the binding is stalled on a human (WaitingOn):
	// the cause, the one-line reason, since when, and the verb that
	// resolves it. Nil otherwise, including for a switchable broken
	// binding the daemon is about to fix itself.
	Waiting *Waiting `json:"waiting,omitempty"`
	// Owner is the client this row belongs to on a serve box: the client's
	// "SHA256:<base64>" fingerprint. Empty on a planner, where every row
	// belongs to the one runtime the UI is welded to.
	Owner string `json:"owner,omitempty"`
	// OwnerLabel is Owner rendered for a human: Clients.LabelOf, or
	// ShortOwner(Owner) when that client has no label. Empty on a planner
	// row. Renderers key new behaviour on OwnerLabel != "" only.
	OwnerLabel string `json:"owner_label,omitempty"`
}

// Key is the UI's row identity. A planner row keys by Name; a server row
// keys by owner/name, because two clients may share a binding name.
func (b BindingStatus) Key() string {
	if b.Owner == "" {
		return b.Name
	}
	return b.Owner + "/" + b.Name
}

// HeadlessInfo is the process half of a headless builder's status row
// (#99, spec §4.8). Nil on a pane row.
type HeadlessInfo struct {
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at"` // zero when idle
	LogPath   string    `json:"log_path,omitempty"`
	// ExitCode is "3", or "unknown" when the process is gone without a
	// trailer; "" while running or idle.
	ExitCode string `json:"exit_code,omitempty"`
	// Tail is the log's last few lines, for the human. Never parsed.
	Tail []string `json:"tail,omitempty"`
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
	Note    string `json:"note,omitempty"`
	Outcome string `json:"outcome,omitempty"`
}

// CloseInfo is the newest round close's commit facts, from its diff log
// entry (#130). Tree is "clean", "dirty", or "" when the entry predates the
// facts or git could not answer.
type CloseInfo struct {
	Round   int    `json:"round"`
	Commits int    `json:"commits"`
	Tree    string `json:"tree"`
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
	// HerdrError is the error text herdr returned when Status asked for its
	// agents. Empty exactly when the lookup succeeded -- including an
	// answered empty agent list. When set, the report's rows were built
	// from the store alone and every pane endpoint relay could not look up
	// reads unknown, not gone (relay did not ask, so it must not claim
	// absence). Absent from JSON when empty so a consumer that never
	// learned the field sees the document it always did.
	HerdrError string `json:"herdr_error,omitempty"`
}

// Status derives every row live from herdr, so it cannot disagree with
// reality. A herdr that fails to answer is reported, not fatal: the report
// is still built from the store alone, the error rides along as
// Report.HerdrError, and every pane endpoint it could not look up reads
// unknown. Only store failures fail the call.
func Status(ctx context.Context, rt Runtime) (Report, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return Report{}, err
	}

	agents, herdrErr := rt.Herdr.ListAgents(ctx)
	if herdrErr != nil {
		agents = nil
	}

	return buildReport(ctx, rt, bindings, agents, herdrErr)
}

func buildReport(ctx context.Context, rt Runtime, bindings []store.Binding, agents []herdr.Agent, herdrErr error) (Report, error) {
	// The absent word for a pane endpoint relay cannot look up: gone when
	// herdr answered (it knows the endpoint is not there), unknown when it
	// did not (relay did not ask, so it must not claim absence).
	absent := agentGone
	if herdrErr != nil {
		absent = agentUnknown
	}

	// Computed once, not per binding: it spans every binding, so it does not
	// vary across rows.
	known := knownEndpoints(bindings)

	rows := make([]BindingStatus, 0, len(bindings))
	for _, b := range bindings {
		row, err := statusRow(ctx, rt, b, agents, known, absent)
		if err != nil {
			return Report{}, err
		}
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	rep := Report{Bindings: rows}
	if herdrErr != nil {
		rep.HerdrError = herdrErr.Error()
	}
	rep.Gated = Gates(rt)
	return rep, nil
}

// statusRow is read-only, so it reaches the store through the self-locking
// *store.Store methods directly rather than a *store.Tx: there is no
// load-modify-save here for WithLock to protect.
func statusRow(ctx context.Context, rt Runtime, b store.Binding, agents []herdr.Agent, known []store.Endpoint, absent string) (BindingStatus, error) {
	row := BindingStatus{
		Name: b.Name, CWD: b.CWD, Round: b.Round,
		State: string(b.State), Display: displayState(b.State),
		BuilderCandidate: b.BuilderCandidate,
		ForkedFrom:       b.ForkedFrom,
		ForkedAtRound:    b.ForkedAtRound,
		Consults:         runningConsults(b),
		Switches:         b.RoundSwitches,
		Branch:           b.Branch,
		PlannerPane:      b.Planner.PaneID, PlannerKind: b.Planner.Kind, PlannerStatus: absent,
		BuilderPane: b.Builder.PaneID, BuilderKind: b.Builder.Kind, BuilderStatus: absent,
	}

	// What relay acts on is what it shows (spec §7.4).
	if a, ok := FindAgent(agents, b.Planner); ok {
		row.PlannerStatus = effectiveStatus(b.Planner, a)
		row.PlannerFocus = a.Focused
		row.PlannerPane = a.PaneID
		row.Workspace = a.WorkspaceID
	}
	if b.Builder.Headless() {
		row.BuilderPane = "headless"
		row.BuilderStatus, row.Headless = headlessStatus(ctx, rt, b)
	} else if b.Builder.Remote() {
		row.BuilderPane = b.Builder.Server
		row.BuilderStatus = b.Builder.RemoteStatus
		if row.BuilderStatus == "" {
			row.BuilderStatus = "unknown"
		}
		if !b.StalledSince.IsZero() && row.BuilderStatus == "running" {
			row.BuilderStatus = "stalled " + AgeText(rt.Now().Sub(b.StalledSince))
		}
	} else if a, ok := FindAgent(agents, b.Builder); ok {
		row.BuilderStatus = effectiveStatus(b.Builder, a)
		row.BuilderPane = a.PaneID
	}

	// A gate in flight overrides whatever the builder itself reports (#132):
	// the round is held on the gate, not on the builder, which the marker
	// already confirmed finished.
	if b.GateRun != nil {
		age := rt.Now().Sub(time.Unix(b.GateRun.StartedAt, 0)).Truncate(time.Second)
		row.BuilderStatus = fmt.Sprintf("gating %s", age)
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
	if w, ok := WaitingOn(b, entries, questionFirstLine(rt)); ok {
		row.Waiting = &w
	}
	if n := len(entries); n > 0 {
		last := entries[n-1]
		row.LastSeq = last.Seq
		row.Last = &LastEvent{
			TS: last.TS, Round: last.Round,
			Direction: last.Direction, Kind: last.Kind,
			Note: last.Note, Outcome: last.Outcome,
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; isPayloadKind(e.Kind) {
			row.LastPayload = &LastEvent{
				TS: e.TS, Round: e.Round,
				Direction: e.Direction, Kind: e.Kind,
				Note: e.Note, Outcome: e.Outcome,
			}
			break
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Kind == store.KindDiff {
			row.LastClose = &CloseInfo{Round: e.Round, Commits: e.Commits, Tree: e.Tree}
			break
		}
	}
	var usages []usage.Usage
	var consults []bool
	for _, e := range entries {
		if e.Usage == nil || (e.Kind != store.KindReport && e.Kind != store.KindFindings) {
			continue
		}
		usages = append(usages, *e.Usage)
		consults = append(consults, e.Kind == store.KindFindings)
		if e.Kind == store.KindReport {
			u := *e.Usage
			row.LastUsage = &u
		}
	}
	if len(usages) > 0 {
		s := usage.Sum(usages, consults)
		row.Spend = &s
	}
	// A round that is still running gets its figure read live (#234):
	// after Spend is set, so the recorded sums stay exactly what the log
	// entries give. rt.Now is nil in some test runtimes; the clock falls
	// back to the wall.
	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}
	row.LiveUsage = peekUsage(ctx, rt, b, now)
	row.Dirty = row.LastClose != nil && row.LastClose.Tree == "dirty" && b.RoundStartedAt.IsZero()

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

// isPayloadKind reports whether k is one of the four kinds that cross
// between planner and builder (plan, report, question, answer) -- the ones
// LastPayload tracks, as opposed to relay's own bookkeeping kinds.
func isPayloadKind(k store.Kind) bool {
	switch k {
	case store.KindPlan, store.KindReport, store.KindQuestion, store.KindAnswer:
		return true
	default:
		return false
	}
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

// ShortOwner truncates a client id for a human: "SHA256:" plus the first
// twelve characters after the prefix, then an ellipsis. An id without the
// prefix, or one too short to truncate, is returned unchanged.
func ShortOwner(id string) string {
	const prefix = "SHA256:"
	if !strings.HasPrefix(id, prefix) {
		return id
	}
	// Ids are base64 ASCII, so runes and bytes agree here: unchanged unless
	// the id is longer than the 19-rune "SHA256:" + 12 fingerprint prefix.
	if len([]rune(id)) <= len([]rune(prefix))+12 {
		return id
	}
	return id[:len(prefix)+12] + "…"
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
		// HerdrError is likewise report-wide, not per binding, so it must
		// survive the same rebuild.
		Gated: r.Gated,

		HerdrError: r.HerdrError,
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

	if r.HerdrError != "" {
		fmt.Fprintf(&sb, "herdr unreachable: %s; pane statuses unknown\n\n", r.HerdrError)
	}

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
		// Written to sb rather than returned as a literal so a herdr
		// header, when present, precedes it.
		sb.WriteString("no bindings\n")
		return sb.String()

	case len(r.Bindings) == 0:
		sb.WriteString("no bindings\n")
		writeGatedBlock(&sb, r.Gated, false)
		return sb.String()
	}

	for _, b := range r.Bindings {
		fmt.Fprintf(&sb, "%-8s %-40s %-4s round %-3d %s",
			b.Name, b.CWD, b.Workspace, b.Round, b.Display)
		if b.Dirty {
			fmt.Fprint(&sb, " dirty")
		}
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
		if b.Headless != nil {
			// spec §4.8: builder  headless  <kind>  <status>  [pid P since HH:MM]  `<token>`
			fmt.Fprintf(&sb, "  builder  %-14s %-8s %-9s", "headless", b.BuilderKind, b.BuilderStatus)
			if b.Headless.PID != 0 {
				fmt.Fprintf(&sb, " pid %d since %s ", b.Headless.PID, b.Headless.StartedAt.Local().Format("15:04"))
			} else {
				fmt.Fprint(&sb, " ")
			}
			fmt.Fprintf(&sb, "`%s`", b.BuilderCandidate)
		} else {
			fmt.Fprintf(&sb, "  builder  %-14s %-8s %-9s `%s`",
				b.BuilderPane, b.BuilderKind, b.BuilderStatus, b.BuilderCandidate)
		}
		if b.Switches > 0 {
			fmt.Fprintf(&sb, "   switched %dx", b.Switches)
		}
		fmt.Fprint(&sb, "\n")
		if b.Headless != nil {
			for _, line := range b.Headless.Tail {
				fmt.Fprintf(&sb, "  log      %s\n", line)
			}
		}
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
			if b.Last.Outcome != "" && b.Last.Outcome != OutcomeDone {
				fmt.Fprintf(&sb, " %s", b.Last.Outcome)
			}
			fmt.Fprint(&sb, "\n")
		}
		if b.LiveUsage != nil {
			fmt.Fprintf(&sb, "  usage    %s\n", strings.Join(usage.LiveParts(*b.LiveUsage), "  "))
		} else if b.LastUsage != nil {
			fmt.Fprintf(&sb, "  usage    %s\n", usage.Line(*b.LastUsage))
		}
		if b.Spend != nil {
			fmt.Fprintf(&sb, "  spend    %s\n", usage.SpendLine(*b.Spend))
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

// DoneResult is what Done actually did with the binding's worktree. Exactly
// one of the three path fields is set, or none when the binding has no
// Worktree (a --cwd or adopted binding). Same shape and meaning as the
// worktree fields of UnbindResult (§3.1).
type DoneResult struct {
	WorktreeRemoved string // path relay removed; the branch survives.
	WorktreeKept    string // path relay left in place.
	KeptReason      string // why; "" unless WorktreeKept is set.
	WorktreeGone    string // recorded path that no longer exists.
	Branch          string // b.Branch, for the message; may be "".
}

// Done stops relaying for a binding once the planner has verified the work,
// and gives a clean worktree back (§4.2).
func Done(ctx context.Context, rt Runtime, name string) (DoneResult, error) {
	// Load-modify-save, so it runs inside the state lock: the daemon rewrites
	// this binding on every tick and would otherwise resurrect it by saving a
	// pre-Done snapshot back over the top. Reach state only through tx here --
	// rt.Store.Load/Save would try to take the lock a second time and Go
	// mutexes are not reentrant.
	var out DoneResult
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}

		// A remote binding's server is told first (§4.6): the server is the
		// one place that knows whether the round is still open, and it must
		// agree before this binding stops relaying locally.
		if b.Builder.Remote() {
			if rt.Remote == nil {
				return ErrRemoteUnavailable
			}
			if derr := rt.Remote.Done(ctx, b.Builder.Server, b.Name); derr != nil {
				var httpErr *client.HTTPError
				if errors.As(derr, &httpErr) && httpErr.Status == 409 {
					return fmt.Errorf("round %d is running on %s; wait or relay unbind --force", b.Round, b.Builder.Server)
				}
				if errors.Is(derr, client.ErrUnreachable) {
					return fmt.Errorf("%s unreachable: %w", b.Builder.Server, derr)
				}
				return fmt.Errorf("%s: %w", b.Builder.Server, derr)
			}
		}

		oldState := b.State
		b.State = store.StateDone

		// A headless round's process is stopped here (#99, spec §4.6): the
		// planner has declared the work finished, so a builder still
		// editing the tree is now the wrong thing. Failure is reported
		// after DONE is saved -- the state change stands either way -- and
		// the pid stays on the endpoint so the human can find it.
		pid, stopErr := stopProcess(ctx, rt, b.Builder, "done")
		if stopErr == nil {
			b.Builder = clearProcess(b.Builder)
		}

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

		out.Branch = b.Branch
		switch {
		case b.Worktree == "":
			// nothing
		case b.Builder.Headless() && stopErr != nil:
			out.WorktreeKept = b.Worktree
			out.KeptReason = "builder process still running"
		case !b.Builder.Headless() && !b.RoundStartedAt.IsZero():
			out.WorktreeKept = b.Worktree
			out.KeptReason = fmt.Sprintf("round %d open; the builder may still write", b.Round)
		default:
			outcome := worktreeTeardown(ctx, rt, b, false)
			out.WorktreeRemoved = outcome.Removed
			out.WorktreeKept = outcome.Kept
			out.KeptReason = outcome.Reason
			out.WorktreeGone = outcome.Gone
		}

		if stopErr != nil {
			return fmt.Errorf("%s marked done, but its builder process %d is still running: %v: %w", b.Name, pid, stopErr, ErrStopFailed)
		}
		return nil
	})
	return out, err
}
