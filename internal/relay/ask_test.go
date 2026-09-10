package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	rt.Aliases = consultTable(t)
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

func TestAskSpawnsRecordsAndStagesTheQuestion(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "Review 003-diff.patch against the plan.")

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

func TestAskRefusesABuilderAlias(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "abuilder", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})

	if !errors.Is(err, ErrNotAConsultRole) {
		t.Fatalf("want ErrNotAConsultRole, got %v", err)
	}
	if f.splits != 0 {
		t.Error("a refused ask split a pane; validation must precede spawning")
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
