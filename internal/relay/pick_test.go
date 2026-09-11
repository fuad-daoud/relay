package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// picks returns the pick entries in a binding's log, in order.
func picks(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog(%q): %v", name, err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindPick {
			out = append(out, e)
		}
	}
	return out
}

// kinds returns the kinds in a binding's log, in order, for position checks.
func kinds(t *testing.T, rt Runtime, name string) []store.Kind {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog(%q): %v", name, err)
	}
	out := make([]store.Kind, len(entries))
	for i, e := range entries {
		out[i] = e.Kind
	}
	return out
}

func TestBindPicksFirstUngatedInOrder(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)

	recordSpawnFailure(rt, testAgyRef, "earlier", errors.New("agent start: exit 1"))
	untilText := GateUntilText(baseTime.Add(SpawnFailedCooldown))

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if len(f.starts) != 1 || f.starts[0].Kind != "claude" {
		t.Fatalf("starts = %+v, want one claude start", f.starts)
	}
	if b.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", b.BuilderCandidate, testClaudeRef)
	}

	got := picks(t, rt, "webshop")
	if len(got) != 1 {
		t.Fatalf("picks = %+v, want 1", got)
	}
	p := got[0]
	if !p.Confirmed || p.Direction != store.DirToPlanner || p.Round != 1 {
		t.Errorf("pick entry = %+v", p)
	}
	wantNote := "picked claude/test/m for builder: order #2; skipped agy/test/m (spawn failed " + untilText + ")"
	if p.Note != wantNote {
		t.Errorf("pick note = %q, want %q", p.Note, wantNote)
	}
}

func TestBindResolvedReturnsTheResolution(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)

	recordSpawnFailure(rt, testAgyRef, "earlier", errors.New("agent start: exit 1"))

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if res.How != HowOrder {
		t.Errorf("How = %q, want HowOrder", res.How)
	}
	if res.Position != 2 {
		t.Errorf("Position = %d, want 2", res.Position)
	}
	if len(res.Skipped) != 1 {
		t.Errorf("Skipped = %+v, want len 1", res.Skipped)
	}
}

func TestBindRefusesWhenEveryCandidateIsGated(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)

	// testClaudeRef's provider ("test") is shared by all three candidates in
	// testCandidatesJSON, so this gates all three.
	if _, err := Unavailable(rt, testClaudeRef, time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrAllGated) {
		t.Fatalf("err = %v, want ErrAllGated", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("starts = %+v, want none", f.starts)
	}
	if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound", err)
	}
}

func TestBindExplicitGatedBypassesAndLogsIt(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	recordSpawnFailure(rt, testAgyRef, "earlier", errors.New("agent start: exit 1"))
	untilText := GateUntilText(baseTime.Add(SpawnFailedCooldown))

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	got := picks(t, rt, "webshop")
	if len(got) != 1 {
		t.Fatalf("picks = %+v, want 1", got)
	}
	wantNote := "picked agy/test/m for builder: explicit, policy bypassed; gated: spawn failed " + untilText
	if got[0].Note != wantNote {
		t.Errorf("pick note = %q, want %q", got[0].Note, wantNote)
	}
}

func TestBindAdoptionWritesNoPick(t *testing.T) {
	existing := herdr.Agent{Kind: "claude", Status: herdr.StatusIdle, PaneID: "w2:p8", CWD: "/repo"}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent(), existing}}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", BuilderPane: "w2:p8", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if got := picks(t, rt, "webshop"); len(got) != 0 {
		t.Errorf("picks = %+v, want none", got)
	}
}

// TestResumeRebindLogsPickAtCurrentRound mirrors
// TestResumeAllowsRebindWhenSessionlessBuilderPaneIsGone. seedBound's own
// initial Bind (Candidate: testAgyRef, explicit) already writes a leading
// pick entry, so this checks the entry the resume itself adds, not the
// total count.
func TestResumeRebindLogsPickAtCurrentRound(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	before := picks(t, rt, "webshop")

	// Pane w2:p4 no longer holds anything.
	f.agents = []herdr.Agent{plannerAgent()}
	f.newPane = "w2:p7"

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo", Resume: true,
	})
	if err != nil {
		t.Fatalf("Bind resume: %v", err)
	}

	after := picks(t, rt, "webshop")
	if len(after) != len(before)+1 {
		t.Fatalf("picks after resume = %+v, want %d entries (one more than before)", after, len(before)+1)
	}
	got := after[len(after)-1]
	if got.Round != b.Round {
		t.Errorf("pick round = %d, want %d", got.Round, b.Round)
	}
	if !strings.HasPrefix(got.Note, "picked agy/test/m for builder: explicit") {
		t.Errorf("pick note = %q", got.Note)
	}
}

func TestAddLogsPick(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	rt.Policy = orderOf("builder", testAgyRef)

	res, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: "", PlannerPane: "w2:p3", Repo: addRepo(t),
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Resolution.How != HowOrder {
		t.Errorf("Resolution.How = %q, want HowOrder", res.Resolution.How)
	}

	got := picks(t, rt, "frontend")
	if len(got) != 1 {
		t.Fatalf("picks = %+v, want 1", got)
	}
	if got[0].Round != 1 {
		t.Errorf("pick round = %d, want 1", got[0].Round)
	}
}

func TestForkInheritedLogsSource(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)

	res, err := Fork(context.Background(), rt, ForkOptions{
		Source: "source", Round: 2, NewName: "alt", Candidate: "", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if res.Resolution.InheritedFrom != "source" {
		t.Errorf("InheritedFrom = %q, want source", res.Resolution.InheritedFrom)
	}

	ks := kinds(t, rt, "alt")
	forkIdx := -1
	for i, k := range ks {
		if k == store.KindFork {
			forkIdx = i
			break
		}
	}
	if forkIdx == -1 || forkIdx+1 >= len(ks) || ks[forkIdx+1] != store.KindPick {
		t.Fatalf("kinds = %v, want KindFork immediately followed by KindPick", ks)
	}

	entries, err := rt.Store.ReadLog("alt")
	if err != nil {
		t.Fatal(err)
	}
	pick := entries[forkIdx+1]
	wantNote := "picked opencode/test/m for builder: explicit, inherited from source, policy bypassed"
	if pick.Note != wantNote {
		t.Errorf("pick note = %q, want %q", pick.Note, wantNote)
	}
	if pick.Round != 3 {
		t.Errorf("pick round = %d, want 3", pick.Round)
	}
}

func TestAskLogsPickBeforeAsk(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedForAsk(t, f)

	qPath := writeQuestion(t, "what do you think?")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: qPath, Name: b.Name, PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	ks := kinds(t, rt, "webshop")
	if len(ks) < 2 || ks[len(ks)-2] != store.KindPick || ks[len(ks)-1] != store.KindAsk {
		t.Fatalf("kinds tail = %v, want [..., pick, ask]", ks)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	pick := entries[len(entries)-2]
	if pick.Round != res.Consult.Round {
		t.Errorf("pick round = %d, want %d", pick.Round, res.Consult.Round)
	}
	wantNote := "picked claude/test/m for reviewer: sole candidate"
	if pick.Note != wantNote {
		t.Errorf("pick note = %q, want %q", pick.Note, wantNote)
	}
}

// TestStrandedAskLogsNoPick checks the delta a stranded ask adds, not the
// total: seedForAsk's own seedBound already writes a leading pick entry for
// the builder bind.
func TestStrandedAskLogsNoPick(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedForAsk(t, f)
	before := picks(t, rt, "webshop")
	f.startErr = errors.New("agent start: exit 1")

	qPath := writeQuestion(t, "what do you think?")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: qPath, Name: b.Name, PlannerPane: "w2:p3",
	})
	if err == nil {
		t.Fatal("expected an error from a stranded ask")
	}
	if res.Resolution.How != HowSole {
		t.Errorf("Resolution.How = %q, want HowSole", res.Resolution.How)
	}

	after := picks(t, rt, "webshop")
	if len(after) != len(before) {
		t.Errorf("a stranded ask must log no pick, before=%+v after=%+v", before, after)
	}
}
