package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// seedForAsk puts a binding in place with a consult role available and a fixed
// consult id, so filenames and agent names are assertable. seedBound's Bind
// split a pane and started the builder to get there, so the fake's call
// records are cleared here: every ask test counts the calls Ask itself made,
// the same way deliver_test and send_test clear prompts after seeding.
func seedForAsk(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	rt, b := seedBound(t, f)
	rt.NewID = func() string { return "7f2a3c1d" }
	f.newPane = "w2:p9"
	f.starts, f.splitCalls, f.splits = nil, nil, 0
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

// TestAskRefusesAConsultNameHerdrWouldRefuse pins #64: a 15-character binding
// name builds a 33-character consult agent name, and Ask must refuse it before
// the reservation is written or the question staged -- nothing on disk, no
// pane split, no consult recorded.
func TestAskRefusesAConsultNameHerdrWouldRefuse(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)

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
		Role: "reviewer", File: q, Name: name, PlannerPane: "w2:p3",
	})

	// A refused ask leaves nothing behind; these are asserted before the
	// error's identity so the exact artifact left behind is what the failure
	// reports.
	if matches, _ := filepath.Glob(filepath.Join(rt.Store.Dir(name), "*-ask.md")); len(matches) != 0 {
		t.Errorf("a refused ask must stage no question file, found %v", matches)
	}
	if f.splits != 0 {
		t.Errorf("a refused ask must touch no pane: splits = %d", f.splits)
	}

	got, loadErr := rt.Store.Load(name)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if len(got.Consults) != 0 {
		t.Errorf("consults = %+v, want none: a refused ask records nothing", got.Consults)
	}

	if !errors.Is(err, herdr.ErrInvalidAgentName) {
		t.Fatalf("Ask err = %v, want one wrapping herdr.ErrInvalidAgentName", err)
	}
}

func TestAskSpawnsRecordsAndStagesTheQuestion(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "Review 003-diff.patch against the plan.")

	listsBefore := f.listCalls
	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
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

	// The question is staged into state, the way Send stages a plan.
	body, err := os.ReadFile(res.Consult.AskPath)
	if err != nil {
		t.Fatalf("read staged question: %v", err)
	}
	if string(body) != "Review 003-diff.patch against the plan." {
		t.Errorf("staged question = %q", body)
	}

	if len(f.starts) != 1 {
		t.Fatalf("got %d starts, want 1", len(f.starts))
	}
	if f.starts[0].Name != "webshop-reviewer-7f2a3c1d" || f.starts[0].Kind != "claude" {
		t.Errorf("start = %+v", f.starts[0])
	}

	// The prompt names both paths and asks for only the findings path back.
	if len(f.prompts) != 1 {
		t.Fatalf("got %d prompts, want 1", len(f.prompts))
	}
	// Assert the TARGET, not just the text. Typing the consult's prompt into
	// the planner's pane would satisfy every content check below while
	// corrupting the human's conversation -- the exact failure the anti-clobber
	// rule exists to prevent.
	if f.prompts[0].Target != "w2:p9" {
		t.Errorf("prompt went to %q, want the consult's pane w2:p9", f.prompts[0].Target)
	}
	text := f.prompts[0].Text
	for _, want := range []string{res.Consult.AskPath, res.Consult.FindingsPath, "only that path"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt missing %q:\n%s", want, text)
		}
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

	// Backfill is gone: ListAgents was not called during Ask.
	if f.listCalls != listsBefore {
		t.Errorf("listCalls increased %d -> %d during Ask; ListAgents backfill was deleted", listsBefore, f.listCalls)
	}
}

func TestAskOpensTheConsultInTheBindingsTree(t *testing.T) {
	// Tree: "binding" is the role's contract, and nothing else in the suite can
	// observe it: the fake discarded SplitPane's arguments until Step 1b taught
	// it to record them, so passing the planner's cwd -- or an empty one --
	// would have gone unnoticed.
	f := &fakeHerdr{}
	rt, b := seedForAsk(t, f)
	q := writeQuestion(t, "review it")

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if len(f.splitCalls) != 1 {
		t.Fatalf("got %d splits, want 1", len(f.splitCalls))
	}
	if got := f.splitCalls[0].CWD; got != b.CWD {
		t.Errorf("consult pane opened in %q, want the binding's tree %q", got, b.CWD)
	}
	if got := f.splitCalls[0].Target; got != "w2:p3" {
		t.Errorf("split from %q, want the planner's pane w2:p3", got)
	}
}

func TestAskDoesNotAdvanceTheRound(t *testing.T) {
	f := &fakeHerdr{}
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
	f := &fakeHerdr{}
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
	if f.splits != 0 {
		t.Errorf("splits = %d, want 0", f.splits)
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
	f := &fakeHerdr{}
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
	if f.splits != 0 {
		t.Errorf("splits = %d, want 0", f.splits)
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
	f := &fakeHerdr{}
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
	f := &fakeHerdr{}
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

func TestAskLaunchesTheCandidateWithTheRoleDefinition(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "review this")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(f.starts) != 1 {
		t.Fatalf("got %d starts, want 1", len(f.starts))
	}
	if f.starts[0].Kind != "claude" {
		t.Errorf("starts[0].Kind = %q, want claude", f.starts[0].Kind)
	}
	wantArgs := []string{"--model", "m", "--agent", "reviewer"}
	if !reflect.DeepEqual(f.starts[0].Args, wantArgs) {
		t.Errorf("starts[0].Args = %v, want %v", f.starts[0].Args, wantArgs)
	}
	if res.Consult.Role != "reviewer" {
		t.Errorf("res.Consult.Role = %q, want reviewer", res.Consult.Role)
	}
}

func TestAskPrependsThePreambleForAPreambleHarness(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"t","model":"m","roles":["reviewer"]}]`)
	q := writeQuestion(t, "review this")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("got %d prompts, want 1", len(f.prompts))
	}
	roleSpec, ok := harness.RoleByName("reviewer")
	if !ok {
		t.Fatal("RoleByName(reviewer) failed")
	}
	if !strings.HasPrefix(f.prompts[0].Text, roleSpec.Preamble) {
		t.Errorf("prompt text does not start with preamble %q:\n%s", roleSpec.Preamble, f.prompts[0].Text)
	}
}

func TestAskRefusesAtTheConsultCap(t *testing.T) {
	f := &fakeHerdr{}
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
	f := &fakeHerdr{}
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
	if !(f.splits == 0 && len(f.starts) == 0) {
		t.Errorf("expected f.splits == 0 && len(f.starts) == 0, got splits=%d starts=%d", f.splits, len(f.starts))
	}
}

func TestAskCountsOnlyRunningConsultsAgainstTheCap(t *testing.T) {
	f := &fakeHerdr{}
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

func TestAskRecordsAReapableConsultWhenTheSpawnFails(t *testing.T) {
	// The pane exists by the time Prompt fails. Returning the error and walking
	// away would strand it, which is what resolveBuilder does today and what
	// CLAUDE.md warns costs ~800 MB indefinitely.
	f := &fakeHerdr{promptErr: errors.New("herdr exploded")}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err == nil {
		t.Fatal("Ask returned nil after a prompt failure")
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Consults) != 1 {
		t.Fatalf("got %d consults, want 1 reapable record", len(got.Consults))
	}
	if got.Consults[0].State != store.ConsultSilent {
		t.Errorf("state = %q, want silent so the pane is reapable", got.Consults[0].State)
	}
	if got.Consults[0].Endpoint.PaneID != "w2:p9" {
		t.Errorf("pane = %q; the record must name the pane so reap can close it", got.Consults[0].Endpoint.PaneID)
	}
	if !strings.HasPrefix(got.Consults[0].Note, "prompt failed") {
		t.Errorf("note = %q, want prefix 'prompt failed'", got.Consults[0].Note)
	}
}

func TestAskLogsTheQuestionOutbound(t *testing.T) {
	f := &fakeHerdr{}
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
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	lockFree := make(chan struct{})
	f.onSplit = func() {
		go func() {
			// Mutexes are not reentrant (spec §7.1): if Ask holds the store
			// lock across SplitPane, WithLock blocks and never returns. A
			// timeout is therefore proof the lock was held.
			_ = rt.Store.WithLock(func(*store.Tx) error {
				return nil
			})
			close(lockFree)
		}()
		select {
		case <-lockFree:
		case <-time.After(2 * time.Second):
			t.Fatal("state lock is held during SplitPane")
		}
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
}

func TestAskReservesBeforeSpawning(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	rt.NewID = func() string { return "7f2a3c1d" }
	q := writeQuestion(t, "x")

	done := make(chan struct{})
	f.onSplit = func() {
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
			t.Fatal("timed out waiting for Load inside onSplit")
		}
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
}

func TestAskRecordsSilentWithNoPaneWhenTheSplitFails(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	f.newPane = ""
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err == nil {
		t.Fatal("Ask succeeded when split returned empty pane")
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
	if c.Endpoint.PaneID != "" {
		t.Errorf("pane = %q, want empty", c.Endpoint.PaneID)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %d, want 0", len(f.starts))
	}
}

func TestAskRecordsAReapableConsultWhenTheStartFails(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	f.startErr = errors.New("cannot start")
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err == nil {
		t.Fatal("Ask succeeded when StartAgent failed")
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
	if c.Endpoint.PaneID != "w2:p9" {
		t.Errorf("pane = %q, want w2:p9", c.Endpoint.PaneID)
	}
	if !strings.HasPrefix(c.Note, "start failed") {
		t.Errorf("note = %q, want prefix 'start failed'", c.Note)
	}
}

func TestAskUpsertsAnExpiredReservation(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	rt.NewID = func() string { return "7f2a3c1d" }
	q := writeQuestion(t, "x")

	modified := make(chan struct{})
	f.onSplit = func() {
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
			t.Fatal("timed out waiting for rewrite inside onSplit")
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
		t.Fatalf("got %d consults, want 1", len(b.Consults))
	}
	c := b.Consults[0]
	if c.State != store.ConsultRunning {
		t.Errorf("state = %q, want running", c.State)
	}
	if c.Endpoint.PaneID != "w2:p9" {
		t.Errorf("pane = %q, want w2:p9", c.Endpoint.PaneID)
	}
}

func TestAskReappendsAReapedReservation(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	rt.NewID = func() string { return "7f2a3c1d" }
	q := writeQuestion(t, "x")

	reaped := make(chan struct{})
	f.onSplit = func() {
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
			t.Fatal("timed out waiting for reap simulation in onSplit")
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
