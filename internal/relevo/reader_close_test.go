package relevo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
)

// readerCloseFinal is the final message a fake reader stream carries: a
// summary ending in a relevo block, so a close can tail-parse it.
const readerCloseFinal = "The review is done.\n\n```relevo\nstatus: done\nhalted_at: \"\"\nchanged_paths: [index.html]\ncommands_run: []\nnot_done: []\n```\n"

// bindReader binds a reviewer on repo and sends it a plan, so round 1 is open
// in a scratch worktree. It returns the runtime and the stored binding.
func bindReader(t *testing.T, repo string) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Git = git.NewClient("git", 0, 0)
	rt.Runner = newFakeRunner()
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "reader-bind", Role: "reviewer", Candidate: testClaudeRef,
		PlannerID: testPlannerName, CWD: repo,
	}); err != nil {
		t.Fatalf("Bind(reader): %v", err)
	}
	if _, err := Send(context.Background(), rt, "reader-bind", writePlan(t, "review it"), SendOptions{}); err != nil {
		t.Fatalf("Send(reader): %v", err)
	}
	b, err := rt.Store.Load("reader-bind")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

// writeReaderStream writes a claude stream whose last assistant text is text.
func writeReaderStream(t *testing.T, rt Runtime, name string, round int, text string) {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	if err != nil {
		t.Fatalf("encode stream: %v", err)
	}
	path := rt.Store.BuilderStreamPath(name, round)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
}

// reportEntryFor returns the newest to_planner report entry of one round.
func reportEntryFor(t *testing.T, rt Runtime, name string, round int) store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var found store.LogEntry
	ok := false
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToPlanner && e.Kind == store.KindReport {
			found, ok = e, true
		}
	}
	if !ok {
		t.Fatalf("no report entry for %s round %d: %+v", name, round, entries)
	}
	return found
}

// seedLog appends entries to a binding's log.
func seedLog(t *testing.T, rt Runtime, name string, entries ...store.LogEntry) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, e := range entries {
			if err := tx.AppendLog(name, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append log for %s: %v", name, err)
	}
}

// TestReaderCloseWritesSummaryFromTheFinalMessage closes a reader round on its
// marker and checks that summary.md holds the runner's final message, that the
// report entry points at it, that its tail parses, and that no diff was taken.
func TestReaderCloseWritesSummaryFromTheFinalMessage(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	summary := rt.Store.SummaryPath("reader-bind", 1, "reviewer")
	got, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("summary.md was not written: %v", err)
	}
	if string(got) != readerCloseFinal {
		t.Errorf("summary.md = %q, want the final message %q", got, readerCloseFinal)
	}

	e := reportEntryFor(t, rt, "reader-bind", 1)
	if e.Path != summary {
		t.Errorf("report entry Path = %q, want the summary path %q", e.Path, summary)
	}
	if _, ok, _ := reporttail.ParseWithReason([]byte(got)); !ok {
		t.Errorf("the summary's relevo block did not parse:\n%s", got)
	}

	entries, _ := rt.Store.ReadLog("reader-bind")
	if HasEntry(entries, 1, store.DirToPlanner, store.KindDiff) {
		t.Error("a reader round captured a diff")
	}
}

// TestReaderCloseKeepsARunnerWrittenSummary checks that a summary the runner
// wrote itself is the report, and is never overwritten by the final message.
func TestReaderCloseKeepsARunnerWrittenSummary(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	summary := rt.Store.SummaryPath("reader-bind", 1, "reviewer")
	if err := os.MkdirAll(filepath.Dir(summary), 0o755); err != nil {
		t.Fatal(err)
	}
	const own = "# The runner's own summary\n"
	if err := os.WriteFile(summary, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("read summary.md: %v", err)
	}
	if string(got) != own {
		t.Errorf("summary.md = %q, want the runner's own %q", got, own)
	}
	if e := reportEntryFor(t, rt, "reader-bind", 1); e.Path != summary {
		t.Errorf("report entry Path = %q, want the summary path %q", e.Path, summary)
	}
}

// TestReaderCloseRemovesTheScratch checks that a reader round's throwaway
// worktree is gone once the round has closed on its marker.
func TestReaderCloseRemovesTheScratch(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	scratch := rt.Store.ScratchWorktreePath("reader-bind", 1)
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("the scratch was not created: %v", err)
	}
	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("the scratch is still present after close: %v", err)
	}
}

// TestReaderStopRemovesTheScratch checks that stopping a reader round takes
// its throwaway worktree with it.
func TestReaderStopRemovesTheScratch(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, _ := bindReader(t, repo)
	scratch := rt.Store.ScratchWorktreePath("reader-bind", 1)
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("the scratch was not created: %v", err)
	}

	if _, err := Stop(context.Background(), rt, "reader-bind", StopOptions{}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("the scratch is still present after stop: %v", err)
	}
}

// TestReaderDoneRemovesTheScratch checks that done takes a reader binding's
// throwaway worktree with it.
func TestReaderDoneRemovesTheScratch(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, _ := bindReader(t, repo)
	scratch := rt.Store.ScratchWorktreePath("reader-bind", 1)
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("the scratch was not created: %v", err)
	}

	if _, err := Done(context.Background(), rt, "reader-bind"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("the scratch is still present after done: %v", err)
	}
}

// TestSweepScratchKeepsOpenReaderRounds checks that the daemon's sweep removes
// every scratch except the one an open reader round still needs.
func TestSweepScratchKeepsOpenReaderRounds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := readerRepo(t)
	rt := newRuntime(t)
	rt.Git = git.NewClient("git", 0, 0)

	for _, name := range []string{"open-reader", "closed-reader"} {
		if err := rt.Store.Save(store.Binding{
			Name: name, CWD: repo, Shape: store.ShapeReader,
			Round: 1, State: store.StateActive,
		}); err != nil {
			t.Fatalf("Save(%s): %v", name, err)
		}
	}
	seedLog(t, rt, "open-reader",
		store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan})
	seedLog(t, rt, "closed-reader",
		store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan},
		store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport})

	for _, name := range []string{"open-reader", "closed-reader", "ghost"} {
		if _, err := CreateScratch(ctx, rt, store.Binding{Name: name, CWD: repo}, 1); err != nil {
			t.Fatalf("CreateScratch(%s): %v", name, err)
		}
	}

	sweepReaderScratch(ctx, rt)

	if _, err := os.Stat(rt.Store.ScratchWorktreePath("open-reader", 1)); err != nil {
		t.Errorf("the open reader's scratch was removed: %v", err)
	}
	for _, name := range []string{"closed-reader", "ghost"} {
		if _, err := os.Stat(rt.Store.ScratchWorktreePath(name, 1)); !os.IsNotExist(err) {
			t.Errorf("%s's scratch is still present after the sweep: %v", name, err)
		}
	}
}

// TestReaderRoundIsNotAnEscape checks that a reader round never runs the
// worktree-escape check: a dirty binding tree beside a reader's scratch is not
// an escape, and no git read is even made.
func TestReaderRoundIsNotAnEscape(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{snapshotTreeID: "tree1", dirtyResult: true}
	rt := Runtime{Git: fg}
	reader := store.Binding{
		Name: "reader-bind", CWD: "/tree", Repo: "/repo",
		Shape: store.ShapeReader, RoundBaselineTree: "tree1",
		Builder: store.Endpoint{Mode: store.ModeHeadless},
	}
	if got := escapeCheck(context.Background(), rt, reader, true); got != EscapeNone {
		t.Errorf("escapeCheck(reader) = %v, want EscapeNone", got)
	}
	if fg.snapshotCalls != 0 {
		t.Errorf("the reader escape check made %d git reads, want 0", fg.snapshotCalls)
	}

	// The same inputs on a writer are exactly the escape the check catches, so
	// the reader arm is what makes the difference.
	writer := reader
	writer.Shape = store.ShapeWriter
	if got := escapeCheck(context.Background(), rt, writer, true); got != EscapeNote {
		t.Errorf("escapeCheck(writer) = %v, want EscapeNote", got)
	}
}
