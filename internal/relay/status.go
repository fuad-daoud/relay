package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/remote/client"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// agentUnknown is the status word for a builder relay could not determine: a
// remote server that has not reported yet, or a headless process whose state
// could not be read. It means "relay did not ask and must not claim absence",
// the same word headlessStatus and the remote path use for "could not
// determine".
const agentUnknown = "unknown"

// plannerRoute decides how a pending report reaches a binding planner
// (#303 §3.6, §4.6): the live channel claim first, then the configured
// deliverer for the planner kind, else "pull".
//
// live reports whether that route can push right now. A pull route is never
// live: the daemon cannot see whether the background wait's `relay pull` is
// running, which is exactly why pull is a route and not a fault (D6).
func plannerRoute(rt Runtime, b store.Binding) (route string, live bool) {
	if rt.Channels != nil && b.PlannerID != "" {
		now := time.Now()
		if rt.Now != nil {
			now = rt.Now()
		}
		if c, err := rt.Channels.Live(b.PlannerID, now); err == nil && c != nil {
			return "channel", true
		}
	}
	if b.Planner.Kind != "" {
		if _, ok := rt.Deliverers[b.Planner.Kind]; ok {
			return "deliverer", true
		}
	}
	return "pull", false
}

// plannerNameOrID is the planner a status row names: the record name when it
// has one, else the id, else "".
func plannerNameOrID(b BindingStatus) string {
	if b.PlannerName != "" {
		return b.PlannerName
	}
	return b.PlannerID
}

// LiveDiff is the live "+N/-M in F" a status row shows while a round is
// open (#143): dir's working tree against the round's baseline tree, no
// patch body, just the numstat. Shared marks a --cwd binding, whose
// worktree is the planner's own tree rather than one relay created.
type LiveDiff struct {
	Files   int  `json:"files"`
	Added   int  `json:"added"`
	Removed int  `json:"removed"`
	Shared  bool `json:"shared,omitempty"`
}

// BindingStatus is one row of relay status: stored binding plus what relay can
// determine about its builder and its delivery route.
type BindingStatus struct {
	Name             string `json:"name"`
	CWD              string `json:"cwd"`
	Round            int    `json:"round"`
	State            string `json:"state"`
	Display          string `json:"display"`
	BuilderCandidate string `json:"builder_candidate"`
	// PlannerID and PlannerName name the relay planner record this binding
	// belongs to (#303 §3.2).
	PlannerID   string `json:"planner_id,omitempty"`
	PlannerName string `json:"planner_name,omitempty"`
	PlannerKind string `json:"planner_kind"`
	// PlannerRoute is how a pending report reaches this binding planner
	// (#303 §3.6): "channel", "deliverer" or "pull". "pull" is a route, not
	// a fault: it is the background wait's `relay pull`, which is how a
	// Claude Code planner in tools mode gets its report (D6).
	PlannerRoute string `json:"planner_route"`
	// PlannerRouteLive reports whether that route can push right now: a live
	// channel claim, or a configured deliverer. A pull route is never live,
	// because the daemon cannot see whether a background wait is running.
	PlannerRouteLive bool   `json:"planner_route_live"`
	BuilderKind      string `json:"builder_kind"`
	// BuilderDefinition is the builder's resolved agent definition on this
	// binding's builder kind, set only when it is custom (#374): a shipped
	// definition leaves the field empty and omitted, so today's JSON is
	// unchanged.
	BuilderDefinition string `json:"builder_definition,omitempty"`
	// BuilderDefinitionCustom is true exactly when BuilderDefinition is set,
	// so a consumer can tell "custom" from "absent" without the string.
	BuilderDefinitionCustom bool   `json:"builder_definition_custom,omitempty"`
	BuilderStatus           string `json:"builder_status"`
	// Headless is set for a headless builder (#99): its process state and
	// log. BuilderStatus is one of idle, working, exited N, exited, unknown.
	// Nil for a remote builder.
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
	Dirty         bool         `json:"dirty"`
	Pending       *PendingInfo `json:"pending,omitempty"`
	ForkedFrom    string       `json:"forked_from,omitempty"`
	ForkedAtRound int          `json:"forked_at_round,omitempty"`
	// Consults is how many consults are reserved or running on this binding.
	// Terminal ones are omitted: they are a reap chore, not work in flight.
	Consults int `json:"consults,omitempty"`
	// Switches is builder switches in the current round (#61 step 6); zero is
	// omitted.
	Switches int `json:"switches,omitempty"`
	// Branch is the binding's worktree branch; "" for a --cwd binding,
	// which has no worktree of its own.
	Branch string `json:"branch,omitempty"`
	// Landed is #136's land rule: "landed", or "landed pr <url>" when the
	// last land created a PR. Empty until the binding is first landed, and
	// cleared by the next Send.
	Landed string `json:"landed,omitempty"`
	// LandedAt and LandedPR are the same fact as data, for a statusline
	// consumer that should not parse the rendered word. LandedAt is the zero
	// time and LandedPR "" until the first land.
	LandedAt time.Time `json:"landed_at,omitempty"`
	LandedPR string    `json:"landed_pr,omitempty"`
	// LastProgressAt is when one of the binding's progress signals last
	// changed (#135): the later of the tree's and the output's last change.
	// The zero time when the open round has not been sampled.
	LastProgressAt time.Time `json:"last_progress_at,omitempty"`
	// Stall is #135's stalled label ("stalled 17m") while the binding is
	// stalled; "" otherwise. Relay never acts on it.
	Stall string `json:"stall,omitempty"`
	// Exploring is #135's exploring label ("exploring 22m") while the
	// builder's output is moving and its tree is not; "" otherwise.
	Exploring string `json:"exploring,omitempty"`
	// Stale is #135's stale label ("stale 4h 0m") for a NEEDS YOU or HELD
	// binding that has sat unacted past stale_after_ms; "" otherwise.
	Stale string `json:"stale,omitempty"`
	// Waiting is set when the binding is stalled on a human (WaitingOn):
	// the cause, the one-line reason, since when, and the verb that
	// resolves it. Nil otherwise, including for a switchable broken
	// binding the daemon is about to fix itself.
	Waiting *Waiting `json:"waiting,omitempty"`
	// Verdict is the newest reviewer verdict (#144), shown while it belongs
	// to the round just closed: "verdict: rejected (2 reasons)". Empty when
	// the binding never had a verify round and when the verdict is stale --
	// a consumer cannot tell those apart, and does not need to.
	Verdict string `json:"last_verdict,omitempty"`
	// Live is the round's live diff stat against its baseline tree (#143):
	// nil when no round is open, the baseline was never recorded, or the
	// git read failed -- a status row never fails because of it.
	Live *LiveDiff `json:"live,omitempty"`
	// QuietFor is how long since LastProgressAt for an ACTIVE row with an
	// open round (#143), AgeText-formatted; "" before the first progress
	// sample and for every other display state.
	QuietFor string `json:"quiet_for,omitempty"`
	// Unread is true when the binding's newest report entry is newer than
	// its .viewed stamp -- or there is no stamp at all and a report exists
	// (#143). No omitempty: a consumer reads false as "seen".
	Unread bool `json:"unread"`
	// Owner is the client this row belongs to on a serve box: the client's
	// "SHA256:<base64>" fingerprint. Empty on a planner, where every row
	// belongs to the one runtime the UI is welded to.
	Owner string `json:"owner,omitempty"`
	// OwnerLabel is Owner rendered for a human: Clients.LabelOf, or
	// ShortOwner(Owner) when that client has no label. Empty on a planner
	// row. Renderers key new behaviour on OwnerLabel != "" only.
	OwnerLabel string `json:"owner_label,omitempty"`
	// Queued is a served row's place in the server's builder queue (#285):
	// nil unless the round is queued. Set only by internal/serve's
	// AdminStatus/FlatStatus; always nil from a planner's own Status.
	Queued *remote.QueueView `json:"queued,omitempty"`
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

// Status builds every row from the store and what relay can determine
// locally: the planner record, a live channel claim and the configured
// deliverers. Only store failures fail the call.
func Status(ctx context.Context, rt Runtime) (Report, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return Report{}, err
	}

	return buildReport(ctx, rt, bindings)
}

func buildReport(ctx context.Context, rt Runtime, bindings []store.Binding) (Report, error) {
	rows := make([]BindingStatus, 0, len(bindings))
	for _, b := range bindings {
		row, err := statusRow(ctx, rt, b)
		if err != nil {
			return Report{}, err
		}
		rows = append(rows, row)
	}

	// #143: status now uses the same attention-first order ui already did --
	// NEEDS YOU, HELD, ACTIVE, PAUSED, DONE, stale first, newest Last.TS
	// first -- so `status`, `status --json` and the statusline agree with
	// `ui` instead of the plain name order this used to be.
	rows = SortRows(rows, true)

	rep := Report{Bindings: rows}
	rep.Gated = Gates(rt)
	return rep, nil
}

// statusRow is read-only, so it reaches the store through the self-locking
// *store.Store methods directly rather than a *store.Tx: there is no
// load-modify-save here for WithLock to protect.
func statusRow(ctx context.Context, rt Runtime, b store.Binding) (BindingStatus, error) {
	row := BindingStatus{
		Name: b.Name, CWD: b.CWD, Round: b.Round,
		State: string(b.State), Display: displayState(b.State),
		BuilderCandidate: b.BuilderCandidate,
		ForkedFrom:       b.ForkedFrom,
		ForkedAtRound:    b.ForkedAtRound,
		Consults:         runningConsults(b),
		Switches:         b.RoundSwitches,
		Branch:           b.Branch,
		PlannerKind:      b.Planner.Kind,
		PlannerID:        b.PlannerID,
		BuilderKind:      b.Builder.Kind, BuilderStatus: agentUnknown,
	}

	// #374: a custom builder definition is named on the row -- and so in
	// `status --json` -- while a shipped one leaves today's document alone.
	if kind := b.Builder.Kind; kind != "" {
		if role, ok := rt.RoleRegistry().Role("builder"); ok {
			if d, ok := role.Definitions[kind]; ok && d.Custom {
				row.BuilderDefinition = d.Agent
				row.BuilderDefinitionCustom = true
			}
		}
	}

	row.PlannerRoute, row.PlannerRouteLive = plannerRoute(rt, b)

	// PlannerName is the record's name, so `status --json` and a status row
	// can say "planner architect-1" without a second lookup by the reader.
	// A Runtime with no registry (tests) or a forgotten record leaves it "".
	if b.PlannerID != "" && rt.Planners != nil {
		if rec, err := rt.Planners.Get(b.PlannerID); err == nil {
			row.PlannerName = rec.Name
		}
	}

	// #136: a binding landed since its last send says so until the branch
	// moves again.
	if !b.LandedAt.IsZero() {
		row.Landed = "landed"
		if b.LandedPR != "" {
			row.Landed = "landed pr " + b.LandedPR
		}
		row.LandedAt = b.LandedAt
		row.LandedPR = b.LandedPR
	}

	if b.Builder.Headless() {
		row.BuilderStatus, row.Headless = headlessStatus(ctx, rt, b)
	} else if b.Builder.Remote() {
		row.BuilderStatus = b.Builder.RemoteStatus
		if row.BuilderStatus == "" {
			row.BuilderStatus = "unknown"
		}
		if row.BuilderStatus == string(remote.RoundQueued) {
			row.BuilderStatus = queueText(b.Builder.RemoteQueue, b.Builder.Server, rt.Now())
		}
		if !b.StalledSince.IsZero() && row.BuilderStatus == "running" {
			row.BuilderStatus = "stalled " + AgeText(rt.Now().Sub(b.StalledSince))
		}
	}

	// #135's progress labels. The stale label is the row's own; the working
	// label is a headless row's, already carried out of headlessStatus (a
	// remote row's status comes from the server). This runs before the gate
	// override so a gated row still reads "gating ...".
	labelsNow := time.Now()
	if rt.Now != nil {
		labelsNow = rt.Now()
	}
	working, staleLabel := labelsOf(b, labelsNow)
	row.Stale = staleLabel
	if b.Progress != nil {
		row.LastProgressAt = b.Progress.TreeAt
		if b.Progress.OutputAt.After(row.LastProgressAt) {
			row.LastProgressAt = b.Progress.OutputAt
		}
	}
	switch {
	case strings.HasPrefix(working, "stalled "):
		row.Stall = working
	case strings.HasPrefix(working, "exploring "):
		row.Exploring = working
	}

	// A gate in flight overrides whatever the builder itself reports (#132):
	// the round is held on the gate, not on the builder, which the marker
	// already confirmed finished.
	if b.GateRun != nil {
		age := rt.Now().Sub(time.Unix(b.GateRun.StartedAt, 0)).Truncate(time.Second)
		row.BuilderStatus = fmt.Sprintf("gating %s", age)
	}

	// broken is overloaded: it means the builder process is gone, which
	// covers both a clean exit and one mid-round. needs_you is unambiguous.
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

	// #143's live diff: the round's working tree against its baseline,
	// while a round is open. liveStat degrades to nil on its own -- no Git
	// wired, no baseline recorded, or the git read failed -- so this never
	// fails the row.
	row.Live = liveStat(ctx, rt, b)

	// #143's quiet age: only for an ACTIVE row with an open round that has
	// been sampled at least once. Reuses the same now the live usage figure
	// just used, so the two clocks in one row never disagree.
	if row.Display == "ACTIVE" && !b.RoundStartedAt.IsZero() && !row.LastProgressAt.IsZero() {
		row.QuietFor = AgeText(now.Sub(row.LastProgressAt))
	}

	// #143's unread marker: the newest report entry is newer than the
	// binding's .viewed stamp, or there is no stamp at all and a report
	// exists.
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != store.KindReport {
			continue
		}
		viewedAt, ok := rt.Store.ViewedAt(b.Name)
		if !ok || entries[i].TS.After(viewedAt) {
			row.Unread = true
		}
		break
	}

	pending, found, err := rt.Store.PendingForPlanner(b.Name)
	if err != nil {
		return BindingStatus{}, err
	}
	if found {
		row.Pending = &PendingInfo{Round: pending.Round, Kind: pending.Kind}
	}

	// The newest reviewer verdict, while it judged the round just closed
	// (#144): LastVerdict.Round == b.Round-1 means no later round has closed
	// since. A verdict for an older round is history, and `relay log` has it.
	if b.LastVerdict != nil && b.LastVerdict.Round == b.Round-1 {
		row.Verdict = fmt.Sprintf("verdict: %s (%d reasons)", b.LastVerdict.Verdict, len(b.LastVerdict.Reasons))
	}

	return row, nil
}

// queueText renders a served, queued round's status word (#285): the bare
// "queued" when the server has not yet reported facts (part 1's tolerance),
// else the full picture -- how many builders are busy on that server, how
// far back in line, and how long it has waited.
func queueText(q *store.QueueFacts, server string, now time.Time) string {
	if q == nil {
		return "queued"
	}
	return fmt.Sprintf("queued (%d/%d busy on %s, %d ahead, %s)",
		q.Running, q.Cap, server, q.Ahead, AgeText(now.Sub(q.Since)))
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

// displayState collapses the stored states into the words the human cares
// about. needs_you and broken both mean "a human must act"; paused means the
// worktree was released deliberately and bind --resume brings it back.
func displayState(s store.State) string {
	switch s {
	case store.StateNeedsYou, store.StateBroken:
		return "NEEDS YOU"
	case store.StatePaused:
		return "PAUSED"
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
			width, g.Token, GateKindText(g.Kind), GateTimeText(g.Since), GateUntilText(g.Until))
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
		sb.WriteString("no bindings\n")
		return sb.String()

	case len(r.Bindings) == 0:
		sb.WriteString("no bindings\n")
		writeGatedBlock(&sb, r.Gated, false)
		return sb.String()
	}

	for _, b := range r.Bindings {
		fmt.Fprintf(&sb, "%-8s %-40s round %-3d %s",
			b.Name, b.CWD, b.Round, b.Display)
		// #136's land state sits on the round line, where the human reads
		// what happened to the branch.
		if b.Landed != "" {
			fmt.Fprintf(&sb, "  %s", b.Landed)
		}
		// #135's stale age sits right after the state word, where a human
		// scanning for what has been waiting the longest looks.
		if b.Stale != "" {
			fmt.Fprintf(&sb, "  %s", b.Stale)
		}
		if b.Dirty {
			fmt.Fprint(&sb, " dirty")
		}
		// Zero stays out of the row entirely: +0c on every healthy binding
		// would be noise, not information.
		if b.Consults > 0 {
			fmt.Fprintf(&sb, " +%dc", b.Consults)
		}
		// The newest reviewer verdict, while it judged the round just closed
		// (#144): "verdict: rejected (2 reasons)".
		if b.Verdict != "" {
			fmt.Fprintf(&sb, " %s", b.Verdict)
		}
		// #143: the round's live diff against its baseline, how long an
		// ACTIVE row has been quiet, and whether its newest report is
		// unread.
		if b.Live != nil {
			fmt.Fprintf(&sb, "  +%d/-%d in %d", b.Live.Added, b.Live.Removed, b.Live.Files)
			if b.Live.Shared {
				fmt.Fprint(&sb, " (shared tree)")
			}
		}
		if b.QuietFor != "" {
			fmt.Fprintf(&sb, "  quiet %s", b.QuietFor)
		}
		if b.Unread {
			fmt.Fprint(&sb, "  ●new")
		}
		fmt.Fprint(&sb, "\n")
		fmt.Fprintf(&sb, "  planner  %-14s %-8s route %s\n",
			plannerNameOrID(b), b.PlannerKind, b.PlannerRoute)
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
				"remote", b.BuilderKind, b.BuilderStatus, b.BuilderCandidate)
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
		if b.Detail != "" {
			fmt.Fprintf(&sb, "  detail   %s\n", b.Detail)
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
			fmt.Fprintf(&sb, "  pending  %s round %d -> planner\n\n", b.Pending.Kind, b.Pending.Round)
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

		// A served binding still queued (#285) has no process to stop and no
		// completed round to hand back; refuse the same way the wire does, so
		// the server-local `relay serve` admin verbs agree with it.
		if b.Owner != "" && !b.QueuedAt.IsZero() {
			return fmt.Errorf("round %d is queued; relay stop to drop it from the queue, or unbind", b.Round)
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
					return fmt.Errorf("round %d is running on %s; relay stop %s to stop it and keep the binding, or relay unbind %s to drop it", b.Round, b.Builder.Server, b.Name, b.Name)
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
