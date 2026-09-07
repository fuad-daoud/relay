package relay

import (
	"context"
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

	builder, ok := FindAgent(agents, b.Builder)
	if !ok {
		b.State = store.StateBroken
		return b, nil
	}

	// A broken binding recovers only when the SAME agent session is back.
	// Matching on pane alone would resume relaying into whatever now occupies
	// that pane, which for a closed builder could be an unrelated agent.
	if b.State == store.StateBroken {
		if b.Builder.SessionID == "" || builder.Session.Value != b.Builder.SessionID {
			return b, nil
		}

		b.State = store.StateActive
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
		return queueReport(ctx, rt, tx, b, reportPath, payload, "")
	}

	nudgedAt, ok := nudgeTime(entries, b.Round)
	if !ok {
		return nudgeBuilder(ctx, rt, tx, b, reportPath)
	}

	// Give the builder a chance to answer the nudge. Without this the next
	// poll -- two seconds later, before herdr's status has necessarily even
	// moved -- would scrape the terminal, label the round done and advance
	// past it, so the real report lands on an abandoned round's path.
	if rt.Now().UTC().Sub(nudgedAt) < nudgeGrace {
		return b, nil
	}

	return scrapeReport(ctx, rt, tx, b, reportPath)
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

	return b, tx.AppendLog(b.Name, entry)
}

// scrapeReport is the last resort. It reads the scrollback (ReadAgent's
// recent-unwrapped) on purpose: a report is prose the builder printed, not a
// dialog. herdr documents that alternate-screen rows never reach that
// scrollback, so this may be truncated -- the payload says so explicitly
// rather than letting the planner trust it.
func scrapeReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, reportPath string) (store.Binding, error) {
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

	return queueReport(ctx, rt, tx, b, reportPath, payload, "scraped")
}

func queueReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, path, payload, note string) (store.Binding, error) {
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
