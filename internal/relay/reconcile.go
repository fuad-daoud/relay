package relay

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// scrapeLines bounds the fallback terminal read.
const scrapeLines = 200

// nudgeNote marks the one reminder relay sends when a builder went idle without
// writing its report file.
const nudgeNote = "nudge"

const nudgePrompt = `You went idle without writing your report.
Write it to %s now, then reply with only that path.`

// Reconcile advances one binding against the agent list the daemon already
// fetched, so a tick costs exactly one herdr call no matter how many bindings
// exist.
//
// Reconcile does NOT persist anything: it returns the binding and the caller
// must `tx.Save` it before releasing the lock. Everything it calls takes the
// same tx rather than locking itself, which is what lets the caller hold one
// critical section across the whole read-reconcile-write.
func Reconcile(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
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
		next, err = checkRoundTimeout(ctx, rt, b)
	}
	if err != nil {
		return b, err
	}

	return deliverAndSettle(ctx, rt, tx, next, agents)
}

// haltBinding stops relaying and asks for a human, exactly once per transition.
func haltBinding(ctx context.Context, rt Runtime, b store.Binding, message string) (store.Binding, error) {
	if b.State == store.StateNeedsYou {
		return b, nil
	}
	if err := rt.Herdr.Notify(ctx, message); err != nil {
		return b, fmt.Errorf("notify halt: %w", err)
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

	dialog, err := rt.Herdr.ReadAgent(ctx, Target(b.Builder), scrapeLines)
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
func checkRoundTimeout(ctx context.Context, rt Runtime, b store.Binding) (store.Binding, error) {
	if b.RoundStartedAt.IsZero() || b.RoundTimeoutMS <= 0 {
		return b, nil
	}

	budget := time.Duration(b.RoundTimeoutMS) * time.Millisecond
	if rt.Now().UTC().Sub(b.RoundStartedAt) < budget {
		return b, nil
	}

	return haltBinding(ctx, rt, b,
		fmt.Sprintf("%s: round %d has run past %s", b.Name, b.Round, budget))
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

	if !nudged(entries, b.Round) {
		return nudgeBuilder(ctx, rt, tx, b, reportPath)
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

// scrapeReport is the last resort. herdr documents that alternate-screen rows
// never reach its scrollback, so this may be truncated -- the payload says so
// explicitly rather than letting the planner trust it.
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

func nudged(entries []store.LogEntry, round int) bool {
	for _, e := range entries {
		if e.Round == round && e.Note == nudgeNote {
			return true
		}
	}
	return false
}
