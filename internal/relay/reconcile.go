package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/store"
)

// scrapeLines bounds the fallback terminal read.
const scrapeLines = 200

// nudgeNote marks the one reminder relay sends when a builder went idle without
// writing its report file.
const nudgeNote = "nudge"

// nudgeFingerprint is the first line of nudgePrompt, used to detect whether
// a stalled prompt actually landed on screen.
const nudgeFingerprint = "You went idle without finishing."

// startGrace is how long after a plan was handed over relay refuses to nudge,
// no matter what herdr reports. A builder is not "idle" seconds after being
// prompted -- it is starting, and herdr's view of its status lags the prompt.
// Nudging inside this window tells an agent that has not begun to write its
// report now, which abandons the round's real work.
//
// It is measured from Binding.RoundStartedAt, which Send stamps at handoff.
const startGrace = 30 * time.Second

// nudgeGrace is how long the builder gets to answer that reminder before relay
// gives up and scrapes its terminal. It is far longer than a poll interval on
// purpose: scraping abandons the round, so it must never race a report that is
// simply still being written.
const nudgeGrace = 60 * time.Second

// nudgePrompt is the one reminder relay sends when a builder went idle without
// finishing. It names both files: the report and the completion marker.
const nudgePrompt = nudgeFingerprint + `
Write your report to %s if you have not, then create the empty file %s as
your last action, and reply with only the report path.`

func emitMutations(ctx context.Context, rt Runtime, orig, next store.Binding) {
	if rt.Hooks == nil {
		return
	}
	if next.Round > orig.Round {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventRoundStarted,
			BindingID: next.Name,
			State:     string(next.State),
			OldState:  string(orig.State),
			Round:     next.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
	if next.State != orig.State {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventStateChanged,
			BindingID: next.Name,
			State:     string(next.State),
			OldState:  string(orig.State),
			Round:     next.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
}

// refreshEndpoint updates an endpoint from the live agent it was located by.
//
// Preconditions: SameAgent(a, ep) held -- the caller located this agent.
// Postconditions:
//   - SessionID is set when it was empty and the agent reports one;
//   - PaneID becomes a.PaneID;
//   - Kind is set when it was empty;
//   - AgentName is untouched;
//   - A recorded SessionID is never overwritten: it must not overwrite a recorded
//     session on mismatch, or a sub-agent's session would be recorded and the
//     parent would look foreign.
func refreshEndpoint(ep store.Endpoint, a herdr.Agent) store.Endpoint {
	ep.PaneID = a.PaneID
	if ep.SessionID == "" && a.Session.Value != "" {
		ep.SessionID = a.Session.Value
	}
	if ep.Kind == "" && a.Kind != "" {
		ep.Kind = a.Kind
	}
	return ep
}

// effectiveStatus returns a's status, or StatusWorking when a foreground session
// mismatch indicates that a sub-agent is occupying the pane.
//
// When herdr reports a foreground session that differs from the recorded
// session, herdr is reporting a sub-agent running in the agent's pane. The
// sub-agent's idle or done status reflects only the sub-agent's state, while the
// parent agent remains busy waiting on it. In contrast, a blocked sub-agent has
// an active dialog requiring human input that blocks the parent as well, so
// StatusBlocked passes through.
func effectiveStatus(ep store.Endpoint, a herdr.Agent) string {
	if ep.SessionID != "" && a.Session.Value != "" && a.Session.Value != ep.SessionID {
		if a.Status == herdr.StatusIdle || a.Status == herdr.StatusDone {
			return herdr.StatusWorking
		}
	}
	return a.Status
}

// Reconcile advances one binding against the agent list the daemon already
// fetched, so a tick costs exactly one herdr call no matter how many bindings
// exist.
//
// Reconcile does NOT persist anything: it returns the binding and the caller
// must `tx.Save` it before releasing the lock. Everything it calls takes the
// same tx rather than locking itself, which is what lets the caller hold one
// critical section across the whole read-reconcile-write.
func Reconcile(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (out store.Binding, err error) {
	orig := b
	defer func() {
		if err == nil {
			emitMutations(ctx, rt, orig, out)
		}
	}()
	// Consults reconcile before the builder is located, and before the DONE
	// gate below, because they are orthogonal to both: a reviewer reading a
	// diff has no stake in whether the builder's pane still exists, nor in
	// whether the planner has already called the work done. Reconcile returns
	// early when the binding is done, when the builder is gone (below), and on
	// the round-cap halt, and none of those should stop a consult finishing.
	//
	// The DONE case is the one that bites: Reap skips running consults, so a
	// consult never advanced past ConsultRunning can never be closed, and gc
	// then removes the binding and the reap worklist with it -- leaving a live
	// pane with nothing in relay pointing at it.
	//
	// On those early-return paths deliverAndSettle is skipped, so queued
	// findings wait on disk and `relay pull` retrieves them -- the same
	// behaviour the halt comment below describes for a halted binding.
	b, err = reconcileConsults(ctx, rt, tx, b, agents)
	if err != nil {
		return b, err
	}

	if b.State == store.StateDone {
		return b, nil
	}

	if b.Builder.Remote() {
		return reconcileRemote(ctx, rt, tx, b, agents)
	}

	// A headless builder (#99) is a process, not an agent herdr lists;
	// everything below this line looks for a pane. Spec §5.1 is its own
	// tick.
	if b.Builder.Headless() {
		return reconcileHeadless(ctx, rt, tx, b, agents)
	}

	// Two triggers replace this binding's builder mid-round instead of just
	// marking it broken: the builder cannot be located for switchGrace
	// ("gone"), or the ledger holds a live rate-limit gate on its own
	// candidate token ("gated"). Neither ever fires for an adopted builder
	// (BuilderCandidate == "") or a closed round (RoundStartedAt zero) --
	// relay did not spawn the former, and there is nothing to resend for the
	// latter (spec §4.1).
	now := rt.Now().UTC()
	switchable := b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()

	builder, ok := FindAgent(agents, b.Builder)
	if !ok {
		if b.BuilderMissingSince.IsZero() {
			b.BuilderMissingSince = now
		}
		b.State = store.StateBroken
		if switchable && now.Sub(b.BuilderMissingSince) >= switchGrace {
			return switchBuilder(ctx, rt, tx, b, fmt.Sprintf("gone for %s", now.Sub(b.BuilderMissingSince).Truncate(time.Second)), false, true)
		}
		// Whether it just switched (above) or is still waiting out the grace,
		// a switch tick delivers nothing else to this binding: like a halt,
		// it either replaced the builder or has nothing more to reconcile.
		return b, nil
	}
	b.BuilderMissingSince = time.Time{}

	// Recovery is unconditional here because FindAgent already answered the
	// identity question: an agent was located, so SameAgent held, and re-checking
	// the session would ask the same question twice. Deleting the old gate removes
	// the disagreement with builderAlive that caused #20.
	if b.State == store.StateBroken {
		b.State = store.StateActive
	}

	b.Builder = refreshEndpoint(b.Builder, builder)
	if planner, ok := FindAgent(agents, b.Planner); ok {
		b.Planner = refreshEndpoint(b.Planner, planner)
	}

	// Render what the pane builder's own session record holds since the last
	// tick (#184), the way reconcileHeadless drains its stream, before
	// anything below reads the log.
	b = drainSession(rt, b)

	if switchable {
		if g, gated := gatedBuilder(rt, b); gated {
			reason := "rate-limited"
			if g.Note != "" {
				reason = "rate-limited: " + g.Note
			}
			return switchBuilder(ctx, rt, tx, b, reason, true, false)
		}
	}

	// Halt paths return without calling deliverAndSettle, unlike every branch
	// below. That is deliberate: entering Held or Orphaned would overwrite the
	// NeedsYou state `relay status` reports, and DeliverPending's held-payload
	// notice keys on Held, so a halted binding would resume notifying every
	// tick. A payload queued before the halt is not lost -- `relay pull` still
	// retrieves it. haltBinding's own dedup does not depend on either rule.
	if b.Round > b.RoundCap {
		return haltBinding(ctx, rt, b,
			fmt.Sprintf("%s: hit the round cap of %d", b.Name, b.RoundCap))
	}

	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return b, err
	}

	// The builder's completion marker closes the round on any tick, whatever
	// herdr says the pane is doing: the marker is written last, so what is
	// left of the builder's turn is just its reply (spec §4.2). Idle status
	// matters only on the fallback path below, when there is no marker.
	if HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
		!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport) {
		next, closed, gating, err := closeOnMarker(ctx, rt, tx, b, entries, "")
		if err != nil {
			return b, err
		}
		if gating {
			return next, nil
		}
		if closed {
			return deliverAndSettle(ctx, rt, tx, next, agents)
		}
	}

	var next store.Binding
	switch effectiveStatus(b.Builder, builder) {
	case herdr.StatusIdle, herdr.StatusDone:
		next, err = handleIdleBuilder(ctx, rt, tx, b, entries)
	case herdr.StatusBlocked:
		next, err = handleBlockedBuilder(ctx, rt, tx, b, entries)
	default:
		var halted bool
		next, halted, err = checkRoundTimeout(ctx, rt, tx, b)
		if halted {
			return next, err // a halt does not deliver; see the comment above
		}
	}
	if err != nil {
		return b, err
	}

	return deliverAndSettle(ctx, rt, tx, next, agents)
}

// haltBinding stops relaying and asks for a human, exactly once per round.
//
// The dedup keys on HaltNotifiedRound rather than on State because State is
// rewritten by other steps of the same tick (deliverAndSettle can turn NeedsYou
// into Held or Orphaned), which is what made every earlier State-keyed guard
// notify once per poll instead of once. Per round is also the behaviour a human
// wants: one notification per round that goes wrong.
func haltBinding(ctx context.Context, rt Runtime, b store.Binding, message string) (store.Binding, error) {
	if b.HaltNotifiedRound != b.Round {
		body := fmt.Sprintf("round %d", b.Round)
		if err := rt.Herdr.Notify(ctx, message, body, herdr.SoundRequest); err != nil {
			return b, fmt.Errorf("notify halt: %w", err)
		}

		b.Halt = strings.TrimPrefix(message, b.Name+": ")
		b.HaltAt = rt.Now().UTC()

		// Shares the notify's once-per-round dedup.
		slog.Info("binding halted", "binding", b.Name, "round", b.Round, "reason", message)

		b.HaltNotifiedRound = b.Round
	}

	b.State = store.StateNeedsYou

	return b, nil
}

// handleBlockedBuilder captures the dialog and hands it to the planner. herdr
// refuses agent prompt against a blocked agent, so the planner answers with
// relay answer, which uses send-keys.
func handleBlockedBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry) (store.Binding, error) {
	if HasEntry(entries, b.Round, store.DirToPlanner, store.KindQuestion) {
		return b, nil
	}

	dialog, err := rt.Herdr.ReadAgentSource(ctx, Target(b.Builder), DialogSource, DialogLines)
	if err != nil {
		return b, fmt.Errorf("read blocking dialog: %w", err)
	}

	path := rt.Store.QuestionPath(b.Name, b.Round)
	if err := os.WriteFile(path, []byte(dialog), 0o644); err != nil {
		return b, fmt.Errorf("write question %s: %w", path, err)
	}

	sc := scanForInjection(ctx, rt, "dialog", b, []byte(dialog))

	payload := fmt.Sprintf(
		"Builder is blocked at a dialog in round %d. Question: %s\n"+
			"Read it, then answer with: relay answer --name %s (--keys <key> | --choice <n> | --text <s>)",
		b.Round, path, b.Name)

	if sc.Flagged > 0 {
		pLines := strings.SplitN(payload, "\n", 2)
		pFirst := pLines[0] + flaggedParenthetical(sc.Flagged, sc.Record)
		if len(pLines) > 1 {
			payload = pFirst + "\n" + pLines[1]
		} else {
			payload = pFirst
		}
	}

	entry := store.LogEntry{
		TS: rt.Now().UTC(), Round: b.Round,
		Direction: store.DirToPlanner, Kind: store.KindQuestion,
		Path: path, Payload: payload, Note: sc.Note,
		Flagged: sc.Flagged, FlaggedBy: sc.FlaggedBy, Classify: sc.Record,
	}
	if err := Queue(ctx, rt, tx, b.Name, entry); err != nil {
		return b, err
	}

	dialogLine := capLine(dialog, 100)
	if dialogLine == "" {
		dialogLine = "dialog captured"
	}
	notifyTitle := fmt.Sprintf("%s: builder blocked at a dialog", b.Name)
	notifyBody := fmt.Sprintf("round %d: %s", b.Round, dialogLine)
	if err := rt.Herdr.Notify(ctx, notifyTitle, notifyBody, herdr.SoundRequest); err != nil {
		slog.Warn("blocked dialog notify failed", "binding", b.Name, "err", err)
	}

	// Once per round: HasEntry on the question gates the call.
	slog.Info("builder blocked", "binding", b.Name, "round", b.Round, "question", path)

	b.State = store.StateNeedsYou

	return b, nil
}

// checkRoundTimeout flags a builder that has been working past its budget. It
// never kills anything -- the human decides whether to nudge, reset or switch.
//
// The bool reports whether it halted, so the caller can skip delivery the way
// the round cap does. It is returned rather than inferred from the state,
// because a binding can arrive here already NeedsYou for an unrelated reason
// and must still have its pending payload delivered.
//
// The halting tick scans for a rate limit first (spec §5): a match switches
// the builder instead of halting, and the switch does not count toward
// max_switches.
func checkRoundTimeout(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, bool, error) {
	if b.RoundStartedAt.IsZero() || b.RoundTimeoutMS <= 0 {
		return b, false, nil
	}

	budget := time.Duration(b.RoundTimeoutMS) * time.Millisecond
	if rt.Now().UTC().Sub(b.RoundStartedAt) < budget {
		return b, false, nil
	}

	if b.HaltNotifiedRound != b.Round {
		text := limitText(ctx, rt, b)
		next, _, handled, err := gateOnLimit(ctx, rt, tx, b, text, true)
		if handled {
			return next, true, err
		}
	}

	next, err := haltBinding(ctx, rt, b,
		fmt.Sprintf("%s: round %d has run past %s", b.Name, b.Round, budget))

	return next, true, err
}

// noteScraped marks a report entry whose body is a terminal capture, not
// the builder's own file. A scraped body is never tail-parsed (#221).
const noteScraped = "scraped"

// joinNotes space-joins the non-empty ones, so a report entry's note can
// carry both an existing reason (e.g. "noreport") and the escape annotation
// (#192) without either overwriting the other.
func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + " " + b
	}
}

// closeOnMarker closes an open round when the builder's completion marker
// (Store.DonePath) exists. It is the one place both the pane and the headless
// path decide "the builder says it is finished", so they cannot disagree.
//
// With a gate configured (#132) it first runs the gate across ticks: gating
// is true while the gate is running and the round must be left alone --
// both callers return early without acting on the builder in any way (no
// nudge, no "exited without a report" switch, no timeout halt). closed is
// true when the round was closed this tick. closed and gating are never
// both true.
//
// extraNote is appended (via joinNotes) to whichever note this close would
// otherwise write -- "" for the pane path, and the escape annotation (#192)
// computed by the headless path from escapeCheck before this call, since
// queueReport clears RoundBaselineTree and the comparison must happen while
// it is still on the binding.
//
// Preconditions: the round is open -- a plan was sent for b.Round and no
// report has been queued for it.
// Postconditions:
//   - marker absent: closed and gating are false, b is returned unchanged, nothing written.
//   - gate running: gating is true, the binding carries the started/updated GateRun.
//   - marker and report present: the round closes normally (note extraNote,
//     plus "gate=<result>" and the gate's payload line when gated).
//   - marker present, report absent: the round closes with note
//     joinNotes("noreport", extraNote) and a payload saying so. The terminal
//     is never read: the builder said it was done, and a scrape would be a
//     worse artefact than an honest gap.
//
// Errors are gateStep's or queueReport's, wrapped; the round stays open and
// the next tick retries, since the marker is still on disk.
func closeOnMarker(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, extraNote string) (store.Binding, bool, bool, error) {
	if _, err := os.Stat(rt.Store.DonePath(b.Name, b.Round)); err != nil {
		return b, false, false, nil
	}

	b, done, rec, err := gateStep(ctx, rt, tx, b)
	if err != nil {
		return b, false, false, fmt.Errorf("close round on marker: gate: %w", err)
	}
	if !done {
		return b, false, true, nil
	}

	note := extraNote
	gateSuffix := ""
	if rec != nil {
		note = joinNotes(note, "gate="+rec.Result)
		var tail []string
		if rec.Result == "fail" {
			tail = tailLines(rec.LogPath, gateTailLines)
		}
		gateSuffix = "\n" + gateLine(*rec, tail)
	}

	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if _, err := os.Stat(reportPath); err == nil {
		slog.Info("round closed by marker", "binding", b.Name, "round", b.Round)
		next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
			fmt.Sprintf("Builder finished round %d. Report: %s", b.Round, reportPath)+gateSuffix, joinNotes("", note), rec)
		if err != nil {
			return b, false, false, fmt.Errorf("close round on marker: %w", err)
		}
		return next, true, false, nil
	}
	slog.Warn("round closed by marker without a report", "binding", b.Name, "round", b.Round, "note", "noreport")
	next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
		fmt.Sprintf("Builder wrote its completion marker for round %d but no report at %s.", b.Round, reportPath)+gateSuffix, joinNotes("noreport", note), rec)
	if err != nil {
		return b, false, false, fmt.Errorf("close round on marker: %w", err)
	}
	return next, true, false, nil
}

// handleIdleBuilder queues the round's report, or nudges once, or falls back to
// a labelled screen scrape.
//
// A round is eligible for a nudge only once startGrace has elapsed since
// RoundStartedAt. A binding whose RoundStartedAt is zero is never nudged:
// the elapsed time is unknowable, and nudging is the destructive choice.
func handleIdleBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry) (store.Binding, error) {
	if !HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) {
		return b, nil // nothing was sent for this round yet
	}
	if HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport) {
		return b, nil // already handled
	}

	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	donePath := rt.Store.DonePath(b.Name, b.Round)

	nudgedAt, ok := nudgeTime(entries, b.Round)
	if !ok {
		if b.RoundStartedAt.IsZero() {
			return b, nil
		}
		if rt.Now().UTC().Sub(b.RoundStartedAt) < startGrace {
			return b, nil
		}
		return nudgeBuilder(ctx, rt, tx, b, reportPath, donePath)
	}

	next, quiescent, err := builderQuiescent(ctx, rt, b, nudgedAt)
	if err != nil {
		// The round stays open (a failed read is not evidence the builder
		// stopped), but a read that keeps failing is a herdr problem the
		// human should see. Per tick on purpose.
		slog.Warn("builder screen unreadable", "binding", b.Name, "round", b.Round, "err", err)
		return b, nil
	}
	if !quiescent {
		return next, nil
	}

	text := limitText(ctx, rt, next)
	gated, m, handled, err := gateOnLimit(ctx, rt, tx, next, text, true)
	if handled {
		return gated, err
	}

	// Once per round: whichever close runs below ends this path. The builder
	// never wrote its marker, so relay cannot know the tree is final; it
	// delivers the best artefact it has and names the omission (spec §4.3).
	quiet := rt.Now().UTC().Sub(next.BuilderScreenAt).Truncate(time.Second)
	if _, err := os.Stat(reportPath); err == nil {
		slog.Warn("builder quiescent with a report but no marker", "binding", next.Name, "round", next.Round, "quiet", quiet, "note", "unmarked")
		payload := fmt.Sprintf(
			"Builder finished round %d but never confirmed completion (no %s). Report: %s. The diff may be premature.",
			next.Round, filepath.Base(donePath), reportPath)
		if m.Line != "" {
			payload += fmt.Sprintf(" Provider rate-limited: %s; gated until %s.", m.Line, m.Until.Local().Format("15:04"))
		}
		return queueReport(ctx, rt, tx, next, entries, reportPath, payload, "unmarked", nil)
	}
	slog.Info("builder quiescent, scraping report", "binding", next.Name, "round", next.Round, "quiet", quiet)
	return scrapeReport(ctx, rt, tx, next, entries, reportPath)
}

// screenFingerprint hashes the builder's current terminal, read from exactly
// the source scrapeReport would read, so the liveness check and the scrape
// cannot disagree about what the builder's output is.
//
// Errors: a wrapped herdr failure. A failed read is NOT evidence the builder
// stopped, and callers must not treat it as such.
func screenFingerprint(ctx context.Context, rt Runtime, b store.Binding) (string, error) {
	text, err := rt.Herdr.ReadAgent(ctx, Target(b.Builder), scrapeLines)
	if err != nil {
		return "", fmt.Errorf("fingerprint builder terminal: %w", err)
	}
	return fingerprint(text), nil
}

// builderQuiescent reports whether the builder's terminal has been unchanged
// for the whole nudge grace, which is relay's evidence that it has genuinely
// stopped rather than gone quiet.
//
// It returns the binding to persist: when the screen HAS moved, the returned
// binding carries the new fingerprint and a refreshed BuilderScreenAt, which
// is what resets the grace.
//
// Preconditions:  the round has been nudged.
// Postconditions: quiescent is true only when a fingerprint was taken at least
//
//	nudgeGrace ago and the current fingerprint equals it.
//	On any read failure, quiescent is false and the binding is
//	returned unchanged.
func builderQuiescent(ctx context.Context, rt Runtime, b store.Binding, nudgedAt time.Time) (store.Binding, bool, error) {
	since := b.BuilderScreenAt
	if since.IsZero() {
		since = nudgedAt // nudged under the old code: fall back to the log
	}

	current, err := screenFingerprint(ctx, rt, b)
	if err != nil {
		return b, false, err
	}

	// No fingerprint yet: take one and start the clock from now. A binding
	// nudged before this feature therefore waits one extra grace period, which
	// is the safe direction to be wrong in.
	if b.BuilderScreen == "" {
		b.BuilderScreen, b.BuilderScreenAt = current, rt.Now().UTC()
		return b, false, nil
	}

	if current != b.BuilderScreen {
		// The screen moved: the builder is alive. Reset the grace.
		b.BuilderScreen, b.BuilderScreenAt = current, rt.Now().UTC()
		return b, false, nil
	}

	if rt.Now().UTC().Sub(since) < nudgeGrace {
		return b, false, nil // unchanged, but not for long enough yet
	}

	return b, true, nil
}

func nudgeBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, reportPath, donePath string) (store.Binding, error) {
	late := false
	if err := promptWithRetry(ctx, rt, Target(b.Builder), fmt.Sprintf(nudgePrompt, reportPath, donePath), nudgeFingerprint); err != nil {
		if errors.Is(err, ErrPromptLate) {
			late = true
		} else {
			return b, fmt.Errorf("nudge builder: %w", err)
		}
	}

	entry := store.LogEntry{
		TS: rt.Now().UTC(), Round: b.Round,
		Direction: store.DirToBuilder, Kind: store.KindPlan,
		Path: reportPath, Note: nudgeNote, Confirmed: true, Late: late,
	}
	if err := tx.AppendLog(b.Name, entry); err != nil {
		return b, err
	}

	// Once per round: nudgeTime gates the call.
	slog.Info("builder nudged", "binding", b.Name, "round", b.Round)

	if fp, err := screenFingerprint(ctx, rt, b); err == nil {
		b.BuilderScreen, b.BuilderScreenAt = fp, rt.Now().UTC()
	}

	return b, nil
}

// scrapeReport is the last resort. It reads the scrollback (ReadAgent's
// recent-unwrapped) on purpose: a report is prose the builder printed, not a
// dialog. herdr documents that alternate-screen rows never reach that
// scrollback, so this may be truncated -- the payload says so explicitly
// rather than letting the planner trust it.
func scrapeReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, reportPath string) (store.Binding, error) {
	text, err := rt.Herdr.ReadAgent(ctx, Target(b.Builder), scrapeLines)
	if err != nil {
		return b, fmt.Errorf("scrape builder terminal: %w", err)
	}

	body := "<!-- SCRAPED from the terminal; may be truncated -->\n\n" + text
	if err := os.WriteFile(reportPath, []byte(body), 0o644); err != nil {
		return b, fmt.Errorf("write scraped report: %w", err)
	}

	payload := fmt.Sprintf(
		"Builder finished round %d but never wrote its report file. SCRAPED from its terminal (may be truncated): %s",
		b.Round, reportPath)

	return queueReport(ctx, rt, tx, b, entries, reportPath, payload, noteScraped, nil)
}

func queueReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, path, payload, note string, gate *store.GateRecord) (store.Binding, error) {
	now := rt.Now().UTC()
	roundStart := b.RoundStartedAt

	body, _ := os.ReadFile(path)
	var (
		tail   ReportTail
		ok     bool
		reject string
	)
	outcome := OutcomeUnstructured
	if note != noteScraped {
		// A scraped body is the terminal, which holds the prompt's own
		// ```relay skeleton, truncated by the capture. The tail contract is
		// for the file the builder writes, not for what herdr had on screen.
		tail, ok, reject = parseReportTail(body)
	}
	if ok {
		outcome = tail.Status
	} else if reject != "" {
		// A fence was present but unreadable: keep unstructured, but say why
		// so the planner does not treat this as "the builder omitted the block".
		note = joinNotes(note, reject)
	}
	sc := scanForInjection(ctx, rt, "report", b, body)
	note = joinNotes(note, sc.Note)

	pLines := strings.SplitN(payload, "\n", 2)
	pFirst := pLines[0]
	pRest := ""
	if len(pLines) > 1 {
		pRest = "\n" + pLines[1]
	}

	if outcome != OutcomeDone && outcome != OutcomeUnstructured {
		prefix := fmt.Sprintf("Builder finished round %d", b.Round)
		if strings.HasPrefix(pFirst, prefix) {
			replacement := prefix + " -- " + outcome
			if tail.HaltedAt != "" {
				replacement += fmt.Sprintf(" at %q", tail.HaltedAt)
			}
			pFirst = replacement + pFirst[len(prefix):]
		} else {
			pFirst = pFirst + fmt.Sprintf(" Outcome: %s.", outcome)
		}
	}
	if sc.Flagged > 0 {
		pFirst += flaggedParenthetical(sc.Flagged, sc.Record)
	}
	payload = pFirst + pRest

	closed := ""
	if !HasEntry(entries, b.Round, store.DirToPlanner, store.KindDiff) {
		result := CaptureRoundDiff(ctx, rt, b)
		facts := CommitFacts(ctx, rt, b)
		closed = result.EndTree
		diffNote := DiffSummary(result, facts)
		if result.Available && ok && tail.ChangedPaths != nil && len(tail.ChangedPaths) != result.Stat.FilesChanged {
			diffNote = joinNotes(diffNote, fmt.Sprintf("paths: report %d, diff %d", len(tail.ChangedPaths), result.Stat.FilesChanged))
		}
		diffEntry := store.LogEntry{
			TS:        rt.Now().UTC(),
			Round:     b.Round,
			Direction: store.DirToPlanner,
			Kind:      store.KindDiff,
			Path:      result.Path,
			Note:      diffNote,
			Confirmed: true,
		}
		if facts.Known {
			diffEntry.Commits = facts.Commits
			diffEntry.Tree = "clean"
			if facts.Dirty {
				diffEntry.Tree = "dirty"
			}
		}
		if err := tx.AppendLog(b.Name, diffEntry); err != nil {
			return b, err
		}
		if line := DiffLine(result, facts, b.Branch); line != "" {
			payload = payload + "\n" + line
		}
	}

	entry := store.LogEntry{
		TS: now, Round: b.Round,
		Direction: store.DirToPlanner, Kind: store.KindReport,
		Path: path, Payload: payload, Note: note,
		Usage:        recordUsage(ctx, rt, roundSource(rt, b, roundStart, now)),
		Outcome:      outcome,
		HaltedAt:     tail.HaltedAt,
		ChangedPaths: tail.ChangedPaths,
		CommandsRun:  tail.CommandsRun,
		NotDone:      tail.NotDone,
		Flagged:      sc.Flagged,
		FlaggedBy:    sc.FlaggedBy,
		Classify:     sc.Record,
		Gate:         gate,
	}
	if err := Queue(ctx, rt, tx, b.Name, entry); err != nil {
		return b, err
	}

	b.Round++
	b.State = store.StateActive

	// The new round has not been sent yet, so it has no deadline: leaving the
	// old round's start in place would time the next round out against a clock
	// that started before the planner had even seen this report. Send stamps a
	// fresh RoundStartedAt when it hands the round over.
	b.RoundStartedAt = time.Time{}

	// A halt notified for the old round says nothing about the new one, so the
	// next round that goes wrong gets its own single notification.
	b.HaltNotifiedRound = 0
	b.Halt = ""
	b.HaltAt = time.Time{}
	// A switch counted against the old round says nothing about the new one,
	// and neither does an exclusion recorded against it (#191).
	b.RoundSwitches = 0
	b.RoundExcluded = nil
	b.RoundTier = ""
	b.RoundBaselineTree = ""
	b.RoundBaselineHead = ""
	b.RoundClosedTree = closed
	b.BuilderScreen = ""
	b.BuilderScreenAt = time.Time{}
	b.GateRun = nil

	return b, nil
}

// deliverAndSettle attempts any pending delivery and folds the result into the
// binding's state. It is also where a held-path decision becomes visible:
// DeliverPending records why it held or injected, and nothing else in
// production reads that reason (#75).
func deliverAndSettle(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
	if b.Owner != "" {
		// Owned by a remote client: there is no planner pane. Payloads stay
		// queued; the owner reads them over the wire (remote-builders spec §6.2).
		return b, nil
	}
	prev := b.State
	next, got, err := DeliverPending(ctx, rt, tx, b, agents)
	if err != nil {
		return b, err
	}
	b = next

	switch {
	case got.Held && prev != store.StateHeld:
		// Log the transition only. A held tick's reason carries the quiet
		// clock ("quiet 23s of 1m0s") and would change every tick.
		slog.Info("payload held", "binding", b.Name, "round", got.Round, "reason", got.Reason)
	case got.Delivered && got.Reason != "":
		// The two focused-path injects. The unfocused delivery has an empty
		// reason and stays silent: it is the ordinary path.
		slog.Info("payload delivered", "binding", b.Name, "round", got.Round, "reason", got.Reason)
	}

	switch {
	case got.PlannerGone:
		b.State = store.StateOrphaned
	case got.Held:
		b.State = store.StateHeld
	case got.Delivered && b.State == store.StateHeld:
		b.State = store.StateActive
	case got.Empty && b.State == store.StateHeld:
		// Nothing is waiting any more, so the hold is over. This is the path a
		// `relay pull` leaves behind: it claims the payload without delivering
		// it, so nothing else ever clears Held.
		b.State = store.StateActive
	}

	return b, nil
}

// HasEntry reports whether the log already contains a message of that shape.
func HasEntry(entries []store.LogEntry, round int, dir store.Direction, kind store.Kind) bool {
	for _, e := range entries {
		if e.Round == round && e.Direction == dir && e.Kind == kind && e.Note != nudgeNote {
			return true
		}
	}
	return false
}

// nudgeTime reports when this round was nudged, if it was.
func nudgeTime(entries []store.LogEntry, round int) (time.Time, bool) {
	for _, e := range entries {
		if e.Round == round && e.Note == nudgeNote {
			return e.TS, true
		}
	}
	return time.Time{}, false
}
