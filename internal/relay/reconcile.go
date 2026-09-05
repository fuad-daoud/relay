package relay

import (
	"context"
	"fmt"
	"os"

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
// exist. It returns the binding to persist.
//
// The caller holds the state lock across the whole call and passes tx in:
// Queue and DeliverPending must run in the same critical section as the round
// advance that follows them, or a concurrent `relay send` could lose
// RoundStartedAt and silently break timeout detection for that round.
func Reconcile(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
	if b.State == store.StateDone {
		return b, nil
	}

	builder, ok := FindAgent(agents, b.Builder)
	if !ok {
		b.State = store.StateBroken
		return b, nil
	}

	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return b, err
	}

	switch builder.Status {
	case herdr.StatusIdle, herdr.StatusDone:
		b, err = handleIdleBuilder(ctx, rt, tx, b, entries)
		if err != nil {
			return b, err
		}
	}

	return deliverAndSettle(ctx, rt, tx, b, agents)
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
