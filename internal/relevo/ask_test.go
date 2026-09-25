package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedForAsk puts a binding in place with a consult role available and a fixed
// consult id, so filenames and agent names are assertable. Bind starts the
// headless builder; every ask test counts what Ask itself did.
func seedForAsk(t *testing.T) (Runtime, store.Binding) {
	t.Helper()
	rt, b := seedBound(t)
	rt.NewID = func() string { return "7f2a3c1d" }
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

// seedRoundReport writes round's report entry -- the one that carries the
// builder session -- and moves the binding on to the next round, so the round
// is closed and `ask --round` has something to resume. A nil session seeds the
// report entry a round built before relevo recorded sessions leaves behind.
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

// TestAskRefusesAnOverlongConsultName pins #64: a 15-character binding
// name builds a 33-character consult agent name, and Ask must refuse it before
// the reservation is written or the question staged -- nothing on disk, no
// pane split, no consult recorded.
func TestAskRefusesAnOverlongConsultName(t *testing.T) {
	rt, _ := seedForAsk(t)

	// seedBound fixes the binding name at "webshop", so seed a second binding
	// by hand with the long name. It gets its own CWD: the store refuses a
	// second active binding on the same working tree as webshop's.
	name := "abcdefghij12345" // 15 chars; + "-reviewer-" + 8 hex = 33
	b := store.Binding{
		Name:    name,
		CWD:     "/other-tree",
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{AgentName: name + "-builder", PaneID: "w2:p4", Kind: "agy"},
		Round:   1,
		State:   store.StateActive,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("seed %s binding: %v", name, err)
	}
	q := writeQuestion(t, "Review 003-diff.patch against the plan.")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: name, PlannerID: testPlannerName,
	})

	// A refused ask leaves nothing behind; these are asserted before the
	// error's identity so the exact artifact left behind is what the failure
	// reports.
	if matches, _ := filepath.Glob(filepath.Join(rt.Store.Dir(name), "*-ask.md")); len(matches) != 0 {
		t.Errorf("a refused ask must stage no question file, found %v", matches)
	}

	got, loadErr := rt.Store.Load(name)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if len(got.Consults) != 0 {
		t.Errorf("consults = %+v, want none: a refused ask records nothing", got.Consults)
	}

	if err == nil || !strings.Contains(err.Error(), "needs a name of at most") {
		t.Fatalf("Ask err = %v, want the consult agent-name cap refusal", err)
	}
}

func TestAskSpawnsRecordsAndStagesTheQuestion(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	q := writeQuestion(t, "Review 003-diff.patch against the plan.")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if res.Consult.ID != "7f2a3c1d" || res.Consult.Role != "reviewer" {
		t.Errorf("consult = %+v", res.Consult)
	}
	if res.Consult.State != store.ConsultRunning {
		t.Errorf("state = %q, want running", res.Consult.State)
	}

	// The question is recorded into state, the way Send stages a plan -- and,
	// since it fits inline, it is not a file on disk.
	if _, err := os.Stat(res.Consult.AskPath); !os.IsNotExist(err) {
		t.Errorf("ask file exists on disk at %s (err %v), want no file", res.Consult.AskPath, err)
	}
	body, err := rt.Store.ReadFile(res.Consult.AskPath)
	if err != nil {
		t.Fatalf("read recorded question: %v", err)
	}
	if string(body) != "Review 003-diff.patch against the plan." {
		t.Errorf("recorded question = %q", body)
	}

	// The consult is a process, not a pane.
	if len(fr.specs) != 1 {
		t.Fatalf("got %d processes, want 1", len(fr.specs))
	}
	// The prompt carries the question and asks for the findings as the
	// process's final message.
	argv := fr.specs[0].Argv
	var prompt string
	for _, a := range argv {
		if strings.Contains(a, "Answer as your final message") {
			prompt = a
		}
	}
	if prompt == "" {
		t.Fatalf("argv carries no consult prompt: %v", argv)
	}
	if !strings.Contains(prompt, "Review 003-diff.patch against the plan.") {
		t.Errorf("prompt does not carry the question:\n%s", prompt)
	}

	// The record is durable, so the daemon finds it after a restart.
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Consults) != 1 || got.Consults[0].ID != "7f2a3c1d" {
		t.Fatalf("consults = %+v", got.Consults)
	}

	// The KindAsk entry was logged with Confirmed: true.
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var askEntry *store.LogEntry
	for i := range entries {
		if entries[i].Kind == store.KindAsk {
			askEntry = &entries[i]
		}
	}
	if askEntry == nil {
		t.Fatal("KindAsk log entry not found")
	}
	if !askEntry.Confirmed {
		t.Error("KindAsk log entry Confirmed = false, want true")
	}

}

func TestAskDoesNotAdvanceTheRound(t *testing.T) {
	rt, before := seedForAsk(t)
	q := writeQuestion(t, "look at this")

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
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
	rt, _ := seedForAsk(t)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviwer", File: q, Name: "webshop", PlannerID: testPlannerName,
	})

	if !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("want ErrUnknownRole, got %v", err)
	}
	if !strings.Contains(err.Error(), "known:") {
		t.Errorf("expected error message to contain 'known:', got %q", err.Error())
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
	rt, _ := seedForAsk(t)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "builder", File: q, Name: "webshop", PlannerID: testPlannerName,
	})

	if !errors.Is(err, ErrNotAConsultRole) {
		t.Fatalf("want ErrNotAConsultRole, got %v", err)
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
	rt, _ := seedForAsk(t)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role:      "reviewer",
		Candidate: testAgyRef,
		File:      q,
		Name:      "webshop",
		PlannerID: testPlannerName,
	})

	if !errors.Is(err, ErrRoleNotServed) {
		t.Fatalf("want ErrRoleNotServed, got %v", err)
	}
}

func TestAskRefusesAnAmbiguousCandidate(t *testing.T) {
	rt, _ := seedForAsk(t)
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"a","model":"m","roles":["reviewer"]},{"harness":"claude","provider":"b","model":"m","roles":["reviewer"]}]`)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	})

	if !errors.Is(err, ErrAmbiguousCandidate) {
		t.Fatalf("want ErrAmbiguousCandidate, got %v", err)
	}
}

func TestAskLaunchesTheCandidateWithTheRoleDefinition(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	q := writeQuestion(t, "review this")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("got %d processes, want 1", len(fr.specs))
	}
	argv := fr.specs[0].Argv
	if argv[0] != "claude" {
		t.Errorf("argv[0] = %q, want the harness binary claude", argv[0])
	}
	if !containsAdjacentPair(argv, "--agent", "reviewer") {
		t.Errorf("argv = %v, want the adjacent pair --agent reviewer", argv)
	}
	if res.Consult.Role != "reviewer" {
		t.Errorf("res.Consult.Role = %q, want reviewer", res.Consult.Role)
	}
}

// An agy consult is launched with --agent (#85); its prompt is the consult
// prompt and carries no interactive preamble.

// An agy consult is launched with --agent (#85); its prompt is the consult
// prompt and carries no interactive preamble.
func TestAskSendsTheConsultPromptAlone(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"t","model":"m","roles":["reviewer"]}]`)
	q := writeQuestion(t, "review this")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("got %d processes, want 1", len(fr.specs))
	}
	argv := fr.specs[0].Argv
	var prompt string
	for _, a := range argv {
		if strings.Contains(a, "Answer as your final message") {
			prompt = a
		}
	}
	if prompt == "" {
		t.Fatalf("argv carries no consult prompt: %v", argv)
	}
	if strings.Contains(prompt, "Activate your") {
		t.Errorf("consult prompt must not carry a preamble:\n%s", prompt)
	}
	if !containsAdjacentPair(argv, "--agent", "reviewer") {
		t.Errorf("argv = %v, want it to contain the adjacent pair --agent reviewer", argv)
	}
}

// containsAdjacentPair reports whether args contains a, b as consecutive
// elements, in that order.

func TestAskRefusesAtTheConsultCap(t *testing.T) {
	rt, _ := seedForAsk(t)
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
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	})
	if !errors.Is(err, ErrConsultCap) {
		t.Fatalf("want ErrConsultCap, got %v", err)
	}
}

func TestAskCountsAReservationAgainstTheCap(t *testing.T) {
	rt, _ := seedForAsk(t)
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
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	})
	if !errors.Is(err, ErrConsultCap) {
		t.Fatalf("want ErrConsultCap, got %v", err)
	}
}

func TestAskCountsOnlyRunningConsultsAgainstTheCap(t *testing.T) {
	rt, _ := seedForAsk(t)
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
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	}); err != nil {
		t.Fatalf("Ask refused over a reapable consult: %v", err)
	}
}

func TestAskLogsTheQuestionOutbound(t *testing.T) {
	rt, _ := seedForAsk(t)
	q := writeQuestion(t, "x")

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
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
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	q := writeQuestion(t, "x")

	lockFree := make(chan struct{})
	fr.onStart = func() {
		go func() {
			// Mutexes are not reentrant (spec §7.1): if Ask holds the store
			// lock across Runner.Start, WithLock blocks and never returns. A
			// timeout is therefore proof the lock was held.
			_ = rt.Store.WithLock(func(*store.Tx) error {
				return nil
			})
			close(lockFree)
		}()
		select {
		case <-lockFree:
		case <-time.After(2 * time.Second):
			t.Fatal("state lock is held during the consult spawn")
		}
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
}

func TestAskReservesBeforeSpawning(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	rt.NewID = func() string { return "7f2a3c1d" }
	q := writeQuestion(t, "x")

	done := make(chan struct{})
	fr.onStart = func() {
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
			if c.Endpoint.PID != 0 {
				t.Errorf("pid = %d, want none before the process starts", c.Endpoint.PID)
			}
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for Load inside onStart")
		}
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
}

func TestAskRecordsSilentWhenTheProcessFailsToStart(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	fr.startErr = errors.New("cannot start")
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	})
	if err == nil {
		t.Fatal("Ask succeeded when the process failed to start")
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 1 {
		t.Fatalf("got %d consults, want 1", len(b.Consults))
	}
	c := b.Consults[0]
	if c.State != store.ConsultSilent {
		t.Errorf("state = %q, want silent", c.State)
	}
	if c.Endpoint.PID != 0 {
		t.Errorf("pid = %d, want none", c.Endpoint.PID)
	}
	if !strings.HasPrefix(c.Note, "spawn failed") {
		t.Errorf("note = %q, want prefix 'spawn failed'", c.Note)
	}
}

func TestAskUpsertsAnExpiredReservation(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	rt.NewID = func() string { return "7f2a3c1d" }
	q := writeQuestion(t, "x")

	modified := make(chan struct{})
	fr.onStart = func() {
		go func() {
			defer close(modified)
			b, err := rt.Store.Load("webshop")
			if err != nil {
				t.Errorf("Load: %v", err)
				return
			}
			for i := range b.Consults {
				if b.Consults[i].ID == "7f2a3c1d" {
					b.Consults[i].State = store.ConsultSilent
					b.Consults[i].Note = "spawn did not complete"
				}
			}
			if err := rt.Store.Save(b); err != nil {
				t.Errorf("Save: %v", err)
			}
		}()
		select {
		case <-modified:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for rewrite inside onStart")
		}
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 1 {
		t.Fatalf("got %d consults, want 1", len(b.Consults))
	}
	c := b.Consults[0]
	if c.State != store.ConsultRunning {
		t.Errorf("state = %q, want running", c.State)
	}
	if !c.Endpoint.Headless() || c.Endpoint.PID == 0 {
		t.Errorf("endpoint = %+v, want a running headless process", c.Endpoint)
	}
}

func TestAskReappendsAReapedReservation(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	rt.NewID = func() string { return "7f2a3c1d" }
	q := writeQuestion(t, "x")

	reaped := make(chan struct{})
	fr.onStart = func() {
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
			t.Fatal("timed out waiting for reap simulation in onStart")
		}
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
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

func TestAskReviewerOnClaudeTierRead(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	rt.Policy.Tier = map[string]string{"reviewer": "read"}
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role:      "reviewer",
		Candidate: testClaudeRef,
		File:      q,
		Name:      "webshop",
		PlannerID: testPlannerName,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("got %d processes, want 1", len(fr.specs))
	}
	if !containsAdjacentPair(fr.specs[0].Argv, "--permission-mode", "plan") {
		t.Errorf("expected --permission-mode plan in argv, got %v", fr.specs[0].Argv)
	}
}

// TestAskHeadlessStartsAProcessNotAPane pins the one thing `--headless`
// changes: the consult runs through proc.Runner in the harness's print form,
// and no tab, agent start or prompt ever happens. Routing Headless through
// the pane branch fails on fr.specs.

// TestAskHeadlessStartsAProcessNotAPane pins the one thing `--headless`
// changes: the consult runs through proc.Runner in the harness's print form,
// and no tab, agent start or prompt ever happens. Routing Headless through
// the pane branch fails on fr.specs.
func TestAskHeadlessStartsAProcessNotAPane(t *testing.T) {
	fr := newFakeRunner()
	rt, b := seedForAsk(t)
	rt.Runner = fr
	q := writeQuestion(t, "review it")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
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
	} else if !strings.Contains(prompt, "review it") {
		t.Errorf("prompt does not carry the question:\n%s", prompt)
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

// TestAskIsAlwaysHeadless pins #303: a consult never opens a pane. No pane is
// created or started, the endpoint Mode is headless, and the process runs
// through the Runner.
//
// Mutation check: restoring the pane branch behind `if false`, then flipping
// it to `if true`, fails this test: no pane call is reachable at all.

// TestAskIsAlwaysHeadless pins #303: a consult never opens a pane. No pane is
// created or started, the endpoint Mode is headless, and the process runs
// through the Runner.
//
// Mutation check: restoring the pane branch behind `if false`, then flipping
// it to `if true`, fails this test: no pane call is reachable at all.
func TestAskIsAlwaysHeadless(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	q := writeQuestion(t, "review it")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if !res.Consult.Endpoint.Headless() {
		t.Errorf("endpoint mode = %q, want headless", res.Consult.Endpoint.Mode)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("got %d processes, want 1", len(fr.specs))
	}
}

// TestAskHeadlessRefusesUnsupportedTier: a headless consult resolves its tier
// exactly as a pane one does, so opencode on `read` is refused before any
// reservation or process.

// TestAskHeadlessRefusesUnsupportedTier: a headless consult resolves its tier
// exactly as a pane one does, so opencode on `read` is refused before any
// reservation or process.
func TestAskHeadlessRefusesUnsupportedTier(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	rt.Policy.Tier = map[string]string{"reviewer": "read"}
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role:      "reviewer",
		Candidate: testOpencodeRef,
		File:      q,
		Name:      "webshop",
		PlannerID: testPlannerName,
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

// TestAskHeadlessWithoutRunnerIsRefused: a runtime with no Runner cannot run
// a process, so it refuses before anything is reserved.
func TestAskHeadlessWithoutRunnerIsRefused(t *testing.T) {
	rt, _ := seedForAsk(t)
	rt.Runner = nil
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerID: testPlannerName,
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
	rt, _ := seedForAsk(t)
	rt.Policy.Tier = map[string]string{"reviewer": "read"}
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role:      "reviewer",
		Candidate: testOpencodeRef,
		File:      q,
		Name:      "webshop",
		PlannerID: testPlannerName,
	})
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("err = %v, want harness.ErrTierUnsupported", err)
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
// report entry a round built before relevo recorded sessions leaves behind.

// TestAskRoundResumesTheSession is the whole feature: a round with a recorded
// builder session is resumed in the harness's own resume form, read-only, with
// the question staged and the ask logged against the current round.
//
// Mutation check: launching the plain headless print form instead of Resume
// leaves out --resume and this fails on the argv.
func TestAskRoundResumesTheSession(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
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
	if !strings.Contains(prompt, "why X?") {
		t.Errorf("prompt does not carry the question:\n%s", prompt)
	}

	if _, err := os.Stat(res.Consult.AskPath); !os.IsNotExist(err) {
		t.Errorf("ask file exists on disk at %s (err %v), want no file", res.Consult.AskPath, err)
	}
	body, err := rt.Store.ReadFile(res.Consult.AskPath)
	if err != nil {
		t.Fatalf("read recorded question: %v", err)
	}
	if !strings.Contains(string(body), "why X?") {
		t.Errorf("recorded question = %q, want the inline question", body)
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

// TestAskRoundRefusesOpenRound: the open round's builder is live, and resuming
// it would put two writers in one session. The refusal comes before anything
// is reserved or a file staged.
func TestAskRoundRefusesOpenRound(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
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
	if _, err := rt.Store.ReadFile(rt.Store.AskPath("webshop", 2, "7f2a3c1d")); err == nil {
		t.Error("ReadFile found a question for a refused round ask, want a miss")
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 0 {
		t.Errorf("consults = %+v, want none reserved", b.Consults)
	}
}

// TestAskRoundRefusesNoSession: a round closed before relevo recorded sessions
// has nothing to resume, and guessing a session would resume the wrong one.

// TestAskRoundRefusesNoSession: a round closed before relevo recorded sessions
// has nothing to resume, and guessing a session would resume the wrong one.
func TestAskRoundRefusesNoSession(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
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

// TestAskRoundRefusesCodex: codex resume is not verified, so the round is
// refused with the kind named rather than run with a guessed flag.
func TestAskRoundRefusesCodex(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
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

// TestAskRoundOpencodeForksOnHarnessTier: opencode has no read-only flag, so
// the round runs at harness tier -- no permission flag, no --auto -- and
// --fork keeps the original session untouched.
func TestAskRoundOpencodeForksOnHarnessTier(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
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

// TestAskRoundNeedsExactlyOneQuestion: the round path takes either a file or
// an inline question, never both and never neither.
func TestAskRoundNeedsExactlyOneQuestion(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
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

// TestAskRoundFinalMessageBecomesFindings reuses the headless consult's
// delivery: the resumed process's last message is the findings, written to
// FindingsPath and queued to the planner. No reconcile code is round-aware;
// the headless branch carries it.
func TestAskRoundFinalMessageBecomesFindings(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	seedRoundReport(t, rt, 1, &store.BuilderSession{Kind: "claude", ID: "sess-1"})

	res, err := Ask(context.Background(), rt, AskOptions{Round: 1, Question: "why X?", Name: "webshop"})
	if err != nil {
		t.Fatalf("Ask --round: %v", err)
	}
	c := res.Consult

	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"FINDINGS BODY"}]}}` + "\n" +
		"relevo-exit:0\n"
	if err := os.WriteFile(c.Endpoint.LogPath, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	fr.script(c.Endpoint.PID, false)
	fr.exit(c.Endpoint.PID, 0)

	b := tickConsults(t, rt)

	if b.Consults[0].State != store.ConsultDone {
		t.Fatalf("state = %q, want done", b.Consults[0].State)
	}
	if _, err := os.Stat(c.FindingsPath); !os.IsNotExist(err) {
		t.Fatalf("expected no findings file on disk, got err: %v", err)
	}
	body, err := rt.Store.ReadFile(c.FindingsPath)
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

// TestAskScopesBothConsultPaths pins #313: both consult paths run in a
// relevo-consult-* scope drawn from the same template as a round, with the
// template's CPUQuota -- never its GateCPUQuota.
func TestAskScopesBothConsultPaths(t *testing.T) {
	template := func() *ScopeSpec {
		return &ScopeSpec{CPUWeight: 100, CPUQuota: "150%", GateCPUQuota: "300%", AllowedCPUs: "0-3"}
	}

	assertConsultScope := func(t *testing.T, spec ProcSpec, c store.Consult) {
		t.Helper()
		if spec.Scope == nil {
			t.Fatal("consult spec.Scope = nil, want a scope from the template")
		}
		wantUnit := "relevo-consult-local-webshop-" + strconv.Itoa(c.Round) + "-" + c.ID
		if spec.Scope.Unit != wantUnit {
			t.Errorf("Scope.Unit = %q, want %q", spec.Scope.Unit, wantUnit)
		}
		if spec.Scope.CPUQuota != "150%" {
			t.Errorf("Scope.CPUQuota = %q, want the template's 150%%, not the gate quota", spec.Scope.CPUQuota)
		}
		if spec.Scope.AllowedCPUs != "0-3" {
			t.Errorf("Scope.AllowedCPUs = %q, want the whole pool 0-3: a consult runs alongside the builder", spec.Scope.AllowedCPUs)
		}
		if spec.Scope.GateCPUQuota != "" {
			t.Errorf("Scope.GateCPUQuota = %q, want it zeroed", spec.Scope.GateCPUQuota)
		}
	}

	t.Run("headless consult", func(t *testing.T) {
		fr := newFakeRunner()
		rt, _ := seedForAsk(t)
		rt.Runner = fr
		rt.Scope = template()

		res, err := Ask(context.Background(), rt, AskOptions{
			Role: "reviewer", File: writeQuestion(t, "review it"), Name: "webshop", PlannerID: testPlannerName,
		})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if len(fr.specs) != 1 {
			t.Fatalf("specs = %+v, want one Start", fr.specs)
		}
		assertConsultScope(t, fr.specs[0], res.Consult)
	})

	t.Run("session consult", func(t *testing.T) {
		fr := newFakeRunner()
		rt, _ := seedForAsk(t)
		rt.Runner = fr
		rt.Scope = template()
		seedRoundReport(t, rt, 1, &store.BuilderSession{Kind: "claude", ID: "sess-1"})

		res, err := Ask(context.Background(), rt, AskOptions{Round: 1, Question: "why X?", Name: "webshop"})
		if err != nil {
			t.Fatalf("Ask --round: %v", err)
		}
		if len(fr.specs) != 1 {
			t.Fatalf("specs = %+v, want one Start", fr.specs)
		}
		assertConsultScope(t, fr.specs[0], res.Consult)
	})
}

// TestConsultStderrSharesTheStream pins R2: a consult's stderr goes into its
// stream file, and NNN-<id>-consult.log is no longer written. FinalText and
// the usage readers skip every non-JSON line, so a harness's stderr on the
// stream costs a reader nothing.
//
// Mutation: set LogPath back to a separate path and this fails.
func TestConsultStderrSharesTheStream(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: writeQuestion(t, "review it"), Name: "webshop", PlannerID: testPlannerName,
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	spec := fr.specs[0]
	if spec.LogPath != spec.StreamPath {
		t.Errorf("LogPath = %q, StreamPath = %q; want the stderr on the stream file", spec.LogPath, spec.StreamPath)
	}
	if !strings.HasSuffix(spec.LogPath, "-consult.jsonl") {
		t.Errorf("LogPath = %q, want it to end in -consult.jsonl", spec.LogPath)
	}
}

// ── the question goes in the prompt (N1-N4) ────────────────────────────

// TestInlinePrompt pins the pure decision (§4.3): a question that fits is
// delimited and inline, one byte too large is not, and askInlineBlock trims
// exactly one trailing newline.
//
// Mutation check (run and report): `<` for `<=` fails on the exact-size case;
// an unconditional true fails the over-the-limit case.
func TestInlinePrompt(t *testing.T) {
	render := func(ref string) string { return fmt.Sprintf(consultHeadlessPrompt, ref) }
	const question = "Why did round 1 change the schema?"

	prompt, ok := inlinePrompt(render, []byte(question))
	if !ok {
		t.Fatalf("inlinePrompt(%q) = not ok, want ok", question)
	}
	if !strings.Contains(prompt, question) {
		t.Errorf("prompt does not contain the question:\n%s", prompt)
	}
	for _, want := range []string{"-----BEGIN QUESTION-----", "-----END QUESTION-----"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not contain %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Read:") {
		t.Errorf("prompt contains a Read: reference:\n%s", prompt)
	}

	// A question sized so the whole prompt is exactly inlineAskMax bytes
	// inlines; one byte more does not.
	fits := []byte(strings.Repeat("x", inlineAskMax-len(prompt)+len(question)))
	p, ok := inlinePrompt(render, fits)
	if !ok || len(p) != inlineAskMax {
		t.Errorf("prompt of %d bytes = (%d, %v), want (%d, true)", inlineAskMax, len(p), ok, inlineAskMax)
	}
	over := append(append([]byte(nil), fits...), 'x')
	if p, ok := inlinePrompt(render, over); ok {
		t.Errorf("prompt of %d bytes = (%d, true), want not ok", len(p), len(p))
	}

	// Exactly one trailing newline is trimmed.
	if a, b := askInlineBlock([]byte("hi\n")), askInlineBlock([]byte("hi")); a != b {
		t.Errorf("askInlineBlock kept or dropped more than one newline:\n%q\n%q", a, b)
	}
	if got := askInlineBlock([]byte("hi\n\n")); !strings.HasSuffix(got, "\n\n-----END QUESTION-----") {
		t.Errorf("askInlineBlock(hi\\n\\n) = %q, want the extra newline kept", got)
	}
}

// TestAskInlinesASmallQuestion (N2): a small question is carried by the started
// argv, is not a file on disk, comes back from round_file, and the ask entry
// still names the canonical AskPath.
//
// Mutation check (run and report): always os.WriteFile fails on the on-disk
// check; keeping the Read: prompt fails on the argv.
func TestAskInlinesASmallQuestion(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	const question = "Review 003-diff.patch against the plan."

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: writeQuestion(t, question), Name: "webshop", PlannerID: testPlannerName,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr.specs))
	}
	argv := strings.Join(fr.specs[0].Argv, "\x00")
	if !strings.Contains(argv, question) {
		t.Errorf("argv does not carry the question %q:\n%s", question, argv)
	}

	if _, err := os.Stat(res.Consult.AskPath); !os.IsNotExist(err) {
		t.Errorf("ask file exists on disk at %s (err %v), want no file", res.Consult.AskPath, err)
	}
	body, err := rt.Store.ReadFile(res.Consult.AskPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", res.Consult.AskPath, err)
	}
	if string(body) != question {
		t.Errorf("ReadFile = %q, want the question %q", body, question)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var askEntry *store.LogEntry
	for i := range entries {
		if entries[i].Kind == store.KindAsk {
			askEntry = &entries[i]
		}
	}
	if askEntry == nil {
		t.Fatal("KindAsk log entry not found")
	}
	if askEntry.Path != res.Consult.AskPath {
		t.Errorf("ask entry Path = %q, want %q", askEntry.Path, res.Consult.AskPath)
	}
}

// TestAskFallsBackToAFileOverTheLimit (N3): a question over inlineAskMax falls
// back to today's staged file and Read: prompt.
//
// Mutation check (run and report): an inlinePrompt that always returns true
// fails on the Read: assertion and the on-disk read.
func TestAskFallsBackToAFileOverTheLimit(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	question := strings.Repeat("x", inlineAskMax+1)

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: writeQuestion(t, question), Name: "webshop", PlannerID: testPlannerName,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr.specs))
	}
	argv := strings.Join(fr.specs[0].Argv, " ")
	if !strings.Contains(argv, "Read: "+res.Consult.AskPath) {
		t.Errorf("argv does not read the staged question %s:\n%s", res.Consult.AskPath, argv)
	}
	if strings.Contains(argv, question) {
		t.Error("argv carries the oversized question inline, want the staged file")
	}
	body, err := os.ReadFile(res.Consult.AskPath)
	if err != nil {
		t.Fatalf("read staged question: %v", err)
	}
	if string(body) != question {
		t.Errorf("staged question length = %d, want %d", len(body), len(question))
	}
}

// TestAskRoundInlinesTheQuestion (N4): the round template inlines too, and a
// question over the limit falls back to the staged file.
//
// Mutation check (run and report): asking for the file form regardless of
// inline fails the argv.
func TestAskRoundInlinesTheQuestion(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedForAsk(t)
	rt.Runner = fr
	seedRoundReport(t, rt, 1, &store.BuilderSession{Kind: "claude", ID: "sess-1"})

	const question = "why X?"
	res, err := Ask(context.Background(), rt, AskOptions{Round: 1, Question: question, Name: "webshop"})
	if err != nil {
		t.Fatalf("Ask --round: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr.specs))
	}
	argv := strings.Join(fr.specs[0].Argv, "\x00")
	if !strings.Contains(argv, question) {
		t.Errorf("resumed argv does not carry the question %q:\n%s", question, argv)
	}
	if _, err := os.Stat(res.Consult.AskPath); !os.IsNotExist(err) {
		t.Errorf("ask file exists on disk at %s (err %v), want no file", res.Consult.AskPath, err)
	}
	body, err := rt.Store.ReadFile(res.Consult.AskPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != question {
		t.Errorf("ReadFile = %q, want %q", body, question)
	}

	// The fallback case: a question over the limit is staged as a file.
	fr2 := newFakeRunner()
	rt2, _ := seedForAsk(t)
	rt2.Runner = fr2
	seedRoundReport(t, rt2, 1, &store.BuilderSession{Kind: "claude", ID: "sess-1"})
	big := strings.Repeat("x", inlineAskMax+1)
	res2, err := Ask(context.Background(), rt2, AskOptions{Round: 1, Question: big, Name: "webshop"})
	if err != nil {
		t.Fatalf("Ask --round over the limit: %v", err)
	}
	if len(fr2.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr2.specs))
	}
	argv2 := strings.Join(fr2.specs[0].Argv, " ")
	if !strings.Contains(argv2, "Read: "+res2.Consult.AskPath) {
		t.Errorf("fallback argv does not read the staged question %s", res2.Consult.AskPath)
	}
	if _, err := os.Stat(res2.Consult.AskPath); err != nil {
		t.Errorf("staged question file: %v", err)
	}
}
