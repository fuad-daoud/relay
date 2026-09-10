package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
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

// dialogSource is the herdr read source for a blocking dialog. A TUI approval
// prompt is drawn on the alternate screen, which never reaches the scrollback
// recent-unwrapped reads, so the dialog has to come from detection instead.
const dialogSource = "detection"

const nudgePrompt = `You went idle without writing your report.
Write it to %s now, then reply with only that path.`

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
//   - A recorded SessionID is never overwritten.
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
	if b.State == store.StateDone {
		return b, nil
	}

	// Consults reconcile before the builder is located, because they are
	// orthogonal to it: a reviewer reading a diff has no stake in whether the
	// builder's pane still exists. Reconcile returns early both when the
	// builder is gone (below) and on the round-cap halt, and neither should
	// stop a consult from finishing.
	//
	// On those early-return paths deliverAndSettle is skipped, so queued
	// findings wait on disk and `relay pull` retrieves them -- the same
	// behaviour the halt comment below describes for a halted binding.
	b, err = reconcileConsults(ctx, rt, tx, b, agents)
	if err != nil {
		return b, err
	}

	builder, ok := FindAgent(agents, b.Builder)
	if !ok {
		b.State = store.StateBroken
		return b, nil
	}

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

	var next store.Binding
	switch builder.Status {
	case herdr.StatusIdle, herdr.StatusDone:
		next, err = handleIdleBuilder(ctx, rt, tx, b, entries)
	case herdr.StatusBlocked:
		next, err = handleBlockedBuilder(ctx, rt, tx, b, entries)
	default:
		var halted bool
		next, halted, err = checkRoundTimeout(ctx, rt, b)
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
		if err := rt.Herdr.Notify(ctx, message); err != nil {
			return b, fmt.Errorf("notify halt: %w", err)
		}

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

	dialog, err := rt.Herdr.ReadAgentSource(ctx, Target(b.Builder), dialogSource, scrapeLines)
	if err != nil {
		return b, fmt.Errorf("read blocking dialog: %w", err)
	}

	path := rt.Store.QuestionPath(b.Name, b.Round)
	if err := os.WriteFile(path, []byte(dialog), 0o644); err != nil {
		return b, fmt.Errorf("write question %s: %w", path, err)
	}

	payload := fmt.Sprintf(
		"Builder is blocked at a dialog in round %d. Question: %s\n"+
			"Read it, then answer with: relay answer --name %s (--keys <key> | --choice <n> | --text <s>)",
		b.Round, path, b.Name)

	entry := store.LogEntry{
		TS: rt.Now().UTC(), Round: b.Round,
		Direction: store.DirToPlanner, Kind: store.KindQuestion,
		Path: path, Payload: payload,
	}
	if err := Queue(ctx, rt, tx, b.Name, entry); err != nil {
		return b, err
	}

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
func checkRoundTimeout(ctx context.Context, rt Runtime, b store.Binding) (store.Binding, bool, error) {
	if b.RoundStartedAt.IsZero() || b.RoundTimeoutMS <= 0 {
		return b, false, nil
	}

	budget := time.Duration(b.RoundTimeoutMS) * time.Millisecond
	if rt.Now().UTC().Sub(b.RoundStartedAt) < budget {
		return b, false, nil
	}

	next, err := haltBinding(ctx, rt, b,
		fmt.Sprintf("%s: round %d has run past %s", b.Name, b.Round, budget))

	return next, true, err
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
	if _, err := os.Stat(reportPath); err == nil {
		payload := fmt.Sprintf("Builder finished round %d. Report: %s", b.Round, reportPath)
		return queueReport(ctx, rt, tx, b, entries, reportPath, payload, "")
	}

	nudgedAt, ok := nudgeTime(entries, b.Round)
	if !ok {
		if b.RoundStartedAt.IsZero() {
			return b, nil
		}
		if rt.Now().UTC().Sub(b.RoundStartedAt) < startGrace {
			return b, nil
		}
		return nudgeBuilder(ctx, rt, tx, b, reportPath)
	}

	next, quiescent, err := builderQuiescent(ctx, rt, b, nudgedAt)
	if err != nil {
		return b, nil
	}
	if !quiescent {
		return next, nil
	}

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
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:]), nil
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

func nudgeBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, reportPath string) (store.Binding, error) {
	if err := promptWithRetry(ctx, rt, Target(b.Builder), fmt.Sprintf(nudgePrompt, reportPath)); err != nil {
		return b, fmt.Errorf("nudge builder: %w", err)
	}

	entry := store.LogEntry{
		TS: rt.Now().UTC(), Round: b.Round,
		Direction: store.DirToBuilder, Kind: store.KindPlan,
		Path: reportPath, Note: nudgeNote, Confirmed: true,
	}
	if err := tx.AppendLog(b.Name, entry); err != nil {
		return b, err
	}

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

	return queueReport(ctx, rt, tx, b, entries, reportPath, payload, "scraped")
}

func queueReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, path, payload, note string) (store.Binding, error) {
	closed := ""
	if !HasEntry(entries, b.Round, store.DirToPlanner, store.KindDiff) {
		result := CaptureRoundDiff(ctx, rt, b)
		closed = result.EndTree
		diffEntry := store.LogEntry{
			TS:        rt.Now().UTC(),
			Round:     b.Round,
			Direction: store.DirToPlanner,
			Kind:      store.KindDiff,
			Path:      result.Path,
			Note:      DiffSummary(result),
			Confirmed: true,
		}
		if err := tx.AppendLog(b.Name, diffEntry); err != nil {
			return b, err
		}
		if line := DiffLine(result); line != "" {
			payload = payload + "\n" + line
		}
	}

	entry := store.LogEntry{
		TS: rt.Now().UTC(), Round: b.Round,
		Direction: store.DirToPlanner, Kind: store.KindReport,
		Path: path, Payload: payload, Note: note,
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
	b.RoundBaselineTree = ""
	b.RoundClosedTree = closed
	b.BuilderScreen = ""
	b.BuilderScreenAt = time.Time{}

	return b, nil
}

// deliverAndSettle attempts any pending delivery and folds the result into the
// binding's state.
func deliverAndSettle(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
	got, err := DeliverPending(ctx, rt, tx, b, agents)
	if err != nil {
		return b, err
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
