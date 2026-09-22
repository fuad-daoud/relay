package relay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// seedForAsk puts a binding in place with a consult role available and a fixed
// consult id, so filenames and agent names are assertable. seedBound's Bind
// split a pane and started the builder to get there, so the fake's call
// records are cleared here: every ask test counts the calls Ask itself made,
// the same way deliver_test and send_test clear prompts after seeding.
func writeQuestion(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "q.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write question: %v", err)
	}
	return path
}

// TestAskRefusesAConsultNameHerdrWouldRefuse pins #64: a 15-character binding
// name builds a 33-character consult agent name, and Ask must refuse it before
// the reservation is written or the question staged -- nothing on disk, no
// pane split, no consult recorded.
// An agy consult is launched with --agent (#85); its prompt is the consult
// prompt and carries no interactive preamble.
// containsAdjacentPair reports whether args contains a, b as consecutive
// elements, in that order.
func containsAdjacentPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

// TestAskHeadlessStartsAProcessNotAPane pins the one thing `--headless`
// changes: the consult runs through proc.Runner in the harness's print form,
// and no tab, agent start or prompt ever happens. Routing Headless through
// the pane branch fails on fr.specs.
// TestAskIsAlwaysHeadless pins #303: a consult never opens a pane. No
// CreateTab or StartAgent reaches herdr, the endpoint Mode is headless, and the
// process runs through the Runner.
//
// Mutation check: restoring the pane branch behind `if false`, then flipping it
// to `if true`, fails this test on f.tabs/f.starts.
// TestAskHeadlessRefusesUnsupportedTier: a headless consult resolves its tier
// exactly as a pane one does, so opencode on `read` is refused before any
// reservation or process.
// TestAskHeadlessWithoutRunnerIsRefused: a runtime with no Runner cannot run
// a process, so it refuses before anything is reserved.
// ── ask --round: resuming a closed round's builder session ─────────────

// seedRoundReport writes round's report entry -- the one that carries the
// builder session -- and moves the binding on to the next round, so the round
// is closed and `ask --round` has something to resume. A nil session seeds the
// report entry a round built before relay recorded sessions leaves behind.
func seedRoundReport(t *testing.T, rt Runtime, round int, session *store.BuilderSession) {
	t.Helper()
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		if err := tx.AppendLog(b.Name, store.LogEntry{
			TS:             rt.Now(),
			Round:          round,
			Direction:      store.DirToPlanner,
			Kind:           store.KindReport,
			Confirmed:      true,
			BuilderSession: session,
		}); err != nil {
			return err
		}
		b.Round = round + 1
		return tx.Save(b)
	})
	if err != nil {
		t.Fatalf("seed closed round %d: %v", round, err)
	}
}

// TestAskRoundResumesTheSession is the whole feature: a round with a recorded
// builder session is resumed in the harness's own resume form, read-only, with
// the question staged and the ask logged against the current round.
//
// Mutation check: launching the plain headless print form instead of Resume
// leaves out --resume and this fails on the argv.
// TestAskRoundRefusesOpenRound: the open round's builder is live, and resuming
// it would put two writers in one session. The refusal comes before anything
// is reserved or a file staged.
// TestAskRoundRefusesNoSession: a round closed before relay recorded sessions
// has nothing to resume, and guessing a session would resume the wrong one.
// TestAskRoundRefusesCodex: codex resume is not verified, so the round is
// refused with the kind named rather than run with a guessed flag.
// TestAskRoundOpencodeForksOnHarnessTier: opencode has no read-only flag, so
// the round runs at harness tier -- no permission flag, no --auto -- and
// --fork keeps the original session untouched.
// TestAskRoundNeedsExactlyOneQuestion: the round path takes either a file or
// an inline question, never both and never neither.
// TestAskRoundFinalMessageBecomesFindings reuses the headless consult's
// delivery: the resumed process's last message is the findings, written to
// FindingsPath and queued to the planner. No reconcile code is round-aware;
// the headless branch carries it.
