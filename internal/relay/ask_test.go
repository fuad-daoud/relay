package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/store"
)

// seedForAsk puts a binding in place with a consult role available and a fixed
// consult id, so filenames and agent names are assertable. seedBound's Bind
// split a pane and started the builder to get there, so the fake's call
// records are cleared here: every ask test counts the calls Ask itself made,
// the same way deliver_test and send_test clear prompts after seeding.
func seedForAsk(t *testing.T, f *fakePanes) (Runtime, store.Binding) {
	t.Helper()
	rt, b := seedBound(t, f)
	rt.NewID = func() string { return "7f2a3c1d" }
	f.newPane = "w2:p9"
	f.starts, f.tabs = nil, nil
	return rt, b
}

func writeQuestion(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "q.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write question: %v", err)
	}
	return path
}

func TestAskDoesNotAdvanceTheRound(t *testing.T) {
	f := &fakePanes{}
	rt, before := seedForAsk(t, f)
	q := writeQuestion(t, "look at this")

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	after, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Round != before.Round {
		t.Errorf("round moved %d -> %d; a consult is orthogonal to the builder's round", before.Round, after.Round)
	}
	if !after.RoundStartedAt.Equal(before.RoundStartedAt) {
		t.Error("RoundStartedAt was restamped by a consult")
	}
}

func TestAskRefusesAnUnknownRole(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviwer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})

	if !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("want ErrUnknownRole, got %v", err)
	}
	if !strings.Contains(err.Error(), "known:") {
		t.Errorf("expected error message to contain 'known:', got %q", err.Error())
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %d, want 0", len(f.starts))
	}
	if len(f.tabs) != 0 {
		t.Errorf("tabs = %d, want 0", len(f.tabs))
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 0 {
		t.Errorf("consults = %+v, want none", b.Consults)
	}
}

func TestAskRefusesTheBuilderRole(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "builder", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})

	if !errors.Is(err, ErrNotAConsultRole) {
		t.Fatalf("want ErrNotAConsultRole, got %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %d, want 0", len(f.starts))
	}
	if len(f.tabs) != 0 {
		t.Errorf("tabs = %d, want 0", len(f.tabs))
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 0 {
		t.Errorf("consults = %+v, want none", b.Consults)
	}
}

func TestAskRefusesACandidateThatDoesNotServeTheRole(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role:        "reviewer",
		Candidate:   testAgyRef,
		File:        q,
		Name:        "webshop",
		PlannerPane: "w2:p3",
	})

	if !errors.Is(err, ErrRoleNotServed) {
		t.Fatalf("want ErrRoleNotServed, got %v", err)
	}
}

func TestAskRefusesAnAmbiguousCandidate(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"a","model":"m","roles":["reviewer"]},{"harness":"claude","provider":"b","model":"m","roles":["reviewer"]}]`)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})

	if !errors.Is(err, ErrAmbiguousCandidate) {
		t.Fatalf("want ErrAmbiguousCandidate, got %v", err)
	}
}

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

func TestAskRefusesAtTheConsultCap(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.ConsultCap = 1
	b.Consults = []store.Consult{{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultRunning}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if !errors.Is(err, ErrConsultCap) {
		t.Fatalf("want ErrConsultCap, got %v", err)
	}
}

func TestAskCountsAReservationAgainstTheCap(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.ConsultCap = 1
	b.Consults = []store.Consult{{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultSpawning}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if !errors.Is(err, ErrConsultCap) {
		t.Fatalf("want ErrConsultCap, got %v", err)
	}
	if !(len(f.tabs) == 0 && len(f.starts) == 0) {
		t.Errorf("expected no tabs and no starts, got tabs=%d starts=%d", len(f.tabs), len(f.starts))
	}
}

func TestAskCountsOnlyRunningConsultsAgainstTheCap(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.ConsultCap = 1
	// A terminal consult is waiting to be reaped, not occupying a slot.
	b.Consults = []store.Consult{{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultDone}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask refused over a reapable consult: %v", err)
	}
}

func TestAskLogsTheQuestionOutbound(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if last.Direction != store.DirToConsult || last.Kind != store.KindAsk {
		t.Errorf("entry = %s/%s, want to_consult/ask", last.Direction, last.Kind)
	}
	if !last.Confirmed {
		t.Error("an outbound entry must be confirmed, or the pending scan will try to deliver it to the planner")
	}
	// The pending scan must be untouched by an outbound consult entry.
	if _, found, err := rt.Store.PendingForPlanner("webshop"); err != nil || found {
		t.Errorf("ask entry showed up as pending for the planner: found=%v err=%v", found, err)
	}
}

func TestAskHoldsNoLockWhileSpawning(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	lockFree := make(chan struct{})
	f.onSpawn = func() {
		go func() {
			// Mutexes are not reentrant (spec §7.1): if Ask holds the store
			// lock across CreateTab, WithLock blocks and never returns. A
			// timeout is therefore proof the lock was held.
			_ = rt.Store.WithLock(func(*store.Tx) error {
				return nil
			})
			close(lockFree)
		}()
		select {
		case <-lockFree:
		case <-time.After(2 * time.Second):
			t.Fatal("state lock is held during CreateTab")
		}
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
}

func TestAskReservesBeforeSpawning(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	rt.NewID = func() string { return "7f2a3c1d" }
	q := writeQuestion(t, "x")

	done := make(chan struct{})
	f.onSpawn = func() {
		go func() {
			defer close(done)
			b, err := rt.Store.Load("webshop")
			if err != nil {
				t.Errorf("Load: %v", err)
				return
			}
			if len(b.Consults) != 1 {
				t.Errorf("got %d consults, want 1 reservation", len(b.Consults))
				return
			}
			c := b.Consults[0]
			if c.ID != "7f2a3c1d" {
				t.Errorf("id = %q, want 7f2a3c1d", c.ID)
			}
			if c.State != store.ConsultSpawning {
				t.Errorf("state = %q, want spawning", c.State)
			}
			if c.Endpoint.PaneID != "" {
				t.Errorf("pane = %q, want empty before split", c.Endpoint.PaneID)
			}
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for Load inside onSpawn")
		}
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
}

func TestAskReappendsAReapedReservation(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	rt.NewID = func() string { return "7f2a3c1d" }
	q := writeQuestion(t, "x")

	reaped := make(chan struct{})
	f.onSpawn = func() {
		go func() {
			defer close(reaped)
			b, err := rt.Store.Load("webshop")
			if err != nil {
				t.Errorf("Load: %v", err)
				return
			}
			b.Consults = nil
			if err := rt.Store.Save(b); err != nil {
				t.Errorf("Save: %v", err)
			}
		}()
		select {
		case <-reaped:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for reap simulation in onSpawn")
		}
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 1 {
		t.Fatalf("got %d consults, want 1 re-appended consult", len(b.Consults))
	}
	if b.Consults[0].State != store.ConsultRunning {
		t.Errorf("state = %q, want running", b.Consults[0].State)
	}
}

// TestAskHeadlessStartsAProcessNotAPane pins the one thing `--headless`
// changes: the consult runs through proc.Runner in the harness's print form,
// and no tab, agent start or prompt ever happens. Routing Headless through
// the pane branch fails on fr.specs.
func TestAskHeadlessStartsAProcessNotAPane(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, b := seedForAsk(t, f)
	rt.Runner = fr
	q := writeQuestion(t, "review it")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if len(fr.specs) != 1 {
		t.Fatalf("got %d processes, want 1", len(fr.specs))
	}
	spec := fr.specs[0]
	if spec.Dir != b.CWD {
		t.Errorf("process dir = %q, want the binding's tree %q", spec.Dir, b.CWD)
	}
	if len(spec.Argv) == 0 || spec.Argv[0] != "claude" {
		t.Errorf("argv = %v, want it to start with the harness binary claude", spec.Argv)
	}
	var prompt string
	for _, a := range spec.Argv {
		if strings.Contains(a, "Answer as your final message") {
			prompt = a
		}
	}
	if prompt == "" {
		t.Errorf("argv carries no consult-headless prompt: %v", spec.Argv)
	} else if !strings.Contains(prompt, res.Consult.AskPath) {
		t.Errorf("prompt does not name the staged question %s:\n%s", res.Consult.AskPath, prompt)
	}

	if len(f.tabs) != 0 || len(f.starts) != 0 || len(f.prompts) != 0 {
		t.Errorf("a headless consult must touch no pane: tabs=%d starts=%d prompts=%d",
			len(f.tabs), len(f.starts), len(f.prompts))
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Consults) != 1 {
		t.Fatalf("consults = %+v, want 1", got.Consults)
	}
	c := got.Consults[0]
	if !c.Endpoint.Headless() {
		t.Errorf("endpoint mode = %q, want headless", c.Endpoint.Mode)
	}
	if c.Endpoint.PID != fr.handles[0].PID {
		t.Errorf("PID = %d, want %d", c.Endpoint.PID, fr.handles[0].PID)
	}
	if want := rt.Store.ConsultStreamPath("webshop", c.Round, c.ID); c.Endpoint.LogPath != want {
		t.Errorf("LogPath = %q, want the stream path %q", c.Endpoint.LogPath, want)
	}
	if c.State != store.ConsultRunning {
		t.Errorf("state = %q, want running", c.State)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Kind == store.KindAsk && e.Confirmed {
			found = true
		}
	}
	if !found {
		t.Error("no confirmed KindAsk entry logged for the headless consult")
	}
}

// TestAskHeadlessRefusesUnsupportedTier: a headless consult resolves its tier
// exactly as a pane one does, so opencode on `read` is refused before any
// reservation or process.
func TestAskHeadlessRefusesUnsupportedTier(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, _ := seedForAsk(t, f)
	rt.Runner = fr
	rt.Policy.Tier = map[string]string{"reviewer": "read"}
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role:        "reviewer",
		Candidate:   testOpencodeRef,
		File:        q,
		Name:        "webshop",
		PlannerPane: "w2:p3",
	})
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("err = %v, want harness.ErrTierUnsupported", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want 0: the tier is resolved before any process", len(fr.specs))
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 0 {
		t.Errorf("consults = %d, want 0 (no reservation written)", len(b.Consults))
	}
}

// TestAskHeadlessWithoutRunnerIsRefused: a runtime with no Runner cannot run
// a process, so it refuses before anything is reserved.
func TestAskHeadlessWithoutRunnerIsRefused(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	rt.Runner = nil
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if !errors.Is(err, ErrRunnerUnavailable) {
		t.Fatalf("err = %v, want ErrRunnerUnavailable", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 0 {
		t.Errorf("consults = %+v, want none", b.Consults)
	}
}

func TestAskReviewerOnOpencodeTierReadRefused(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedForAsk(t, f)
	rt.Policy.Tier = map[string]string{"reviewer": "read"}
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role:        "reviewer",
		Candidate:   testOpencodeRef,
		File:        q,
		Name:        "webshop",
		PlannerPane: "w2:p3",
	})
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("err = %v, want harness.ErrTierUnsupported", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("tabs = %d, starts = %d; want 0", len(f.tabs), len(f.starts))
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 0 {
		t.Errorf("consults = %d, want 0 (no reservation written)", len(b.Consults))
	}
}

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
func TestAskRoundResumesTheSession(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, _ := seedForAsk(t, f)
	rt.Runner = fr
	seedRoundReport(t, rt, 1, &store.BuilderSession{Kind: "claude", ID: "sess-1"})

	res, err := Ask(context.Background(), rt, AskOptions{Round: 1, Question: "why X?", Name: "webshop"})
	if err != nil {
		t.Fatalf("Ask --round: %v", err)
	}

	if len(fr.specs) != 1 {
		t.Fatalf("got %d processes, want 1", len(fr.specs))
	}
	argv := fr.specs[0].Argv
	if argv[0] != "claude" {
		t.Errorf("argv[0] = %q, want the harness binary claude", argv[0])
	}
	if !containsAdjacentPair(argv, "--resume", "sess-1") {
		t.Errorf("argv = %v, want --resume sess-1", argv)
	}
	if !containsAdjacentPair(argv, "--permission-mode", "plan") {
		t.Errorf("argv = %v, want the read tier's --permission-mode plan", argv)
	}

	var prompt string
	for _, a := range argv {
		if strings.Contains(a, "You built round 1") {
			prompt = a
		}
	}
	if prompt == "" {
		t.Fatalf("argv carries no round prompt: %v", argv)
	}
	if !strings.Contains(prompt, res.Consult.AskPath) {
		t.Errorf("prompt does not name the staged question %s:\n%s", res.Consult.AskPath, prompt)
	}

	body, err := os.ReadFile(res.Consult.AskPath)
	if err != nil {
		t.Fatalf("read ask file: %v", err)
	}
	if !strings.Contains(string(body), "why X?") {
		t.Errorf("ask file = %q, want the inline question", body)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Consults) != 1 {
		t.Fatalf("consults = %+v, want 1", got.Consults)
	}
	c := got.Consults[0]
	if c.Role != "round" {
		t.Errorf("role = %q, want the round label", c.Role)
	}
	if c.Endpoint.Kind != "claude" {
		t.Errorf("endpoint kind = %q, want the session's kind claude", c.Endpoint.Kind)
	}
	if !c.Endpoint.Headless() {
		t.Errorf("endpoint mode = %q, want headless", c.Endpoint.Mode)
	}
	if c.Round != 2 {
		t.Errorf("consult round = %d, want the asking round 2", c.Round)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var note string
	for _, e := range entries {
		if e.Kind == store.KindPick && e.Round == c.Round {
			t.Errorf("round consult logged a candidate pick: %+v", e)
		}
		if e.Kind == store.KindAsk && e.Direction == store.DirToConsult {
			note = e.Note
		}
	}
	if !strings.Contains(note, "round 1 session claude:sess-1") {
		t.Errorf("ask note = %q, want it to name the resumed session", note)
	}
}

// TestAskRoundRefusesOpenRound: the open round's builder is live, and resuming
// it would put two writers in one session. The refusal comes before anything
// is reserved or a file staged.
func TestAskRoundRefusesOpenRound(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, _ := seedForAsk(t, f)
	rt.Runner = fr
	seedRoundReport(t, rt, 1, &store.BuilderSession{Kind: "claude", ID: "sess-1"})

	_, err := Ask(context.Background(), rt, AskOptions{Round: 2, Question: "x", Name: "webshop"})
	if err == nil || !strings.Contains(err.Error(), "open round") {
		t.Fatalf("err = %v, want an open-round refusal", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want 0: the refusal is before any process", len(fr.specs))
	}
	if matches, _ := filepath.Glob(filepath.Join(rt.Store.Dir("webshop"), "*-ask.md")); len(matches) != 0 {
		t.Errorf("a refused round ask must stage no question file, found %v", matches)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 0 {
		t.Errorf("consults = %+v, want none reserved", b.Consults)
	}
}

// TestAskRoundRefusesNoSession: a round closed before relay recorded sessions
// has nothing to resume, and guessing a session would resume the wrong one.
func TestAskRoundRefusesNoSession(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, _ := seedForAsk(t, f)
	rt.Runner = fr
	seedRoundReport(t, rt, 1, nil)

	_, err := Ask(context.Background(), rt, AskOptions{Round: 1, Question: "x", Name: "webshop"})
	if err == nil || !strings.Contains(err.Error(), "recorded no builder session") {
		t.Fatalf("err = %v, want a no-session refusal", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want 0: the refusal is before any process", len(fr.specs))
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 0 {
		t.Errorf("consults = %+v, want none reserved", b.Consults)
	}
}

// TestAskRoundRefusesCodex: codex resume is not verified, so the round is
// refused with the kind named rather than run with a guessed flag.
func TestAskRoundRefusesCodex(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, _ := seedForAsk(t, f)
	rt.Runner = fr
	seedRoundReport(t, rt, 1, &store.BuilderSession{Kind: "codex", ID: "thread-1"})

	_, err := Ask(context.Background(), rt, AskOptions{Round: 1, Question: "x", Name: "webshop"})
	if !errors.Is(err, harness.ErrResumeUnsupported) {
		t.Fatalf("err = %v, want harness.ErrResumeUnsupported", err)
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("err = %v, want it to name codex", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want 0: the refusal is before any process", len(fr.specs))
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 0 {
		t.Errorf("consults = %+v, want none reserved", b.Consults)
	}
}

// TestAskRoundOpencodeForksOnHarnessTier: opencode has no read-only flag, so
// the round runs at harness tier -- no permission flag, no --auto -- and
// --fork keeps the original session untouched.
func TestAskRoundOpencodeForksOnHarnessTier(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, _ := seedForAsk(t, f)
	rt.Runner = fr
	seedRoundReport(t, rt, 1, &store.BuilderSession{Kind: "opencode", ID: "ses-1"})

	if _, err := Ask(context.Background(), rt, AskOptions{Round: 1, Question: "x", Name: "webshop"}); err != nil {
		t.Fatalf("Ask --round: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("got %d processes, want 1", len(fr.specs))
	}
	argv := fr.specs[0].Argv
	if !containsAdjacentPair(argv, "--session", "ses-1") {
		t.Errorf("argv = %v, want --session ses-1", argv)
	}
	if !containsAdjacentPair(argv, "--fork", "--format") {
		t.Errorf("argv = %v, want --fork", argv)
	}
	for _, bad := range []string{"--auto", "--permission-mode", "--mode"} {
		for _, a := range argv {
			if a == bad {
				t.Errorf("argv = %v, want no %s at harness tier", argv, bad)
			}
		}
	}
}

// TestAskRoundNeedsExactlyOneQuestion: the round path takes either a file or
// an inline question, never both and never neither.
func TestAskRoundNeedsExactlyOneQuestion(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, _ := seedForAsk(t, f)
	rt.Runner = fr
	seedRoundReport(t, rt, 1, &store.BuilderSession{Kind: "claude", ID: "sess-1"})

	for _, opts := range []AskOptions{
		{Round: 1, Name: "webshop"},
		{Round: 1, File: writeQuestion(t, "x"), Question: "x", Name: "webshop"},
	} {
		_, err := Ask(context.Background(), rt, opts)
		if err == nil || !strings.Contains(err.Error(), "--file or -q") {
			t.Errorf("Ask(%+v) err = %v, want a --file or -q refusal", opts, err)
		}
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want 0", len(fr.specs))
	}
}

// TestAskRoundFinalMessageBecomesFindings reuses the headless consult's
// delivery: the resumed process's last message is the findings, written to
// FindingsPath and queued to the planner. No reconcile code is round-aware;
// the headless branch carries it.
func TestAskRoundFinalMessageBecomesFindings(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, _ := seedForAsk(t, f)
	rt.Runner = fr
	seedRoundReport(t, rt, 1, &store.BuilderSession{Kind: "claude", ID: "sess-1"})

	res, err := Ask(context.Background(), rt, AskOptions{Round: 1, Question: "why X?", Name: "webshop"})
	if err != nil {
		t.Fatalf("Ask --round: %v", err)
	}
	c := res.Consult

	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"FINDINGS BODY"}]}}` + "\n" +
		"relay-exit:0\n"
	if err := os.WriteFile(c.Endpoint.LogPath, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	fr.script(c.Endpoint.PID, false)
	fr.exit(c.Endpoint.PID, 0)

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultDone {
		t.Fatalf("state = %q, want done", b.Consults[0].State)
	}
	body, err := os.ReadFile(c.FindingsPath)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	if !strings.Contains(string(body), "FINDINGS BODY") {
		t.Errorf("findings = %q, want the final message", body)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("findings were not queued: found=%v err=%v", found, err)
	}
	if pending.Kind != store.KindFindings || pending.Path != c.FindingsPath {
		t.Errorf("entry = %s path=%q, want findings at %q", pending.Kind, pending.Path, c.FindingsPath)
	}
}
