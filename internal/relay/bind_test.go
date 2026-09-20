package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

// baseTime is the instant newRuntime's fixed clock reports.
var baseTime = time.Unix(1757000000, 0).UTC()

func newRuntime(t *testing.T, f *fakeHerdr) Runtime {
	t.Helper()
	return Runtime{
		Herdr:       f,
		Store:       store.New(t.TempDir()),
		Candidates:  candidateSet(t, testCandidatesJSON),
		LedgerPath:  filepath.Join(t.TempDir(), "ledger.json"),
		HistoryPath: filepath.Join(t.TempDir(), "history.json"),
		Now:         func() time.Time { return baseTime },
	}
}

func plannerAgent() herdr.Agent {
	return herdr.Agent{
		Kind: "claude", Status: herdr.StatusWorking, CWD: "/repo",
		PaneID: "w2:p3", Session: herdr.Session{Value: "planner-sess"},
	}
}

func TestBindSpawnsBuilderPane(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if len(f.starts) != 1 {
		t.Fatalf("got %d agent starts, want 1", len(f.starts))
	}
	got := f.starts[0]
	if got.Kind != "opencode" || got.Pane != "w2:p4" || got.Name != "webshop-builder" {
		t.Errorf("start = %+v", got)
	}
	if b.Builder.PaneID != "w2:p4" || b.Planner.SessionID != "planner-sess" {
		t.Errorf("binding = %+v", b)
	}
	if b.Round != 1 || b.State != store.StateActive {
		t.Errorf("new binding must start at round 1 and active, got %d/%s", b.Round, b.State)
	}
}

// TestBindRefusesABuilderNameHerdrWouldRefuse pins #64: a 25-character binding
// name passes the store's own limit but builds a 33-character agent name, and
// Bind must refuse it before any pane is split, any agent started or any
// binding saved.
func TestBindRefusesABuilderNameHerdrWouldRefuse(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)

	name := "abcdefghij1234567890abcde" // 25 chars; + "-builder" = 33

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: name, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if len(f.starts) != 0 || len(f.tabs) != 0 {
		t.Errorf("a refused name must touch no pane: tabs = %d, starts = %d", len(f.tabs), len(f.starts))
	}
	if !errors.Is(err, herdr.ErrInvalidAgentName) {
		t.Fatalf("Bind err = %v, want one wrapping herdr.ErrInvalidAgentName", err)
	}
	if _, loadErr := rt.Store.Load(name); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound: a refused name saves no binding", loadErr)
	}
}

func TestBindSpawnRecordsBuilderSessionID(t *testing.T) {
	f := &fakeHerdr{
		agents: []herdr.Agent{
			plannerAgent(),
			// The started agent, as it will appear the moment relay lists
			// agents again right after StartAgent returns.
			{Kind: "opencode", Status: herdr.StatusWorking, PaneID: "w2:p4", Session: herdr.Session{Value: "builder-sess"}},
		},
		newPane: "w2:p4",
	}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if b.Builder.SessionID != "builder-sess" {
		t.Errorf("Builder.SessionID = %q, want the started agent's session id", b.Builder.SessionID)
	}
}

func TestBindSpawnToleratesPostStartListAgentsFailure(t *testing.T) {
	f := &fakeHerdr{
		agents:  []herdr.Agent{plannerAgent()},
		newPane: "w2:p4",
		listErr: errors.New("herdr unavailable"),
	}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind must not fail over a best-effort session lookup: %v", err)
	}
	if b.Builder.SessionID != "" {
		t.Errorf("Builder.SessionID = %q, want empty when the post-start lookup fails", b.Builder.SessionID)
	}

	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("binding must exist despite the lookup failure: %v", err)
	}
	if loaded.Builder.PaneID != "w2:p4" {
		t.Errorf("builder pane = %q, want w2:p4 -- the already-running pane must not be stranded", loaded.Builder.PaneID)
	}
}

func TestBindAdoptsExistingBuilderPane(t *testing.T) {
	existing := herdr.Agent{Kind: "claude", Status: herdr.StatusIdle, PaneID: "w2:p8", CWD: "/repo"}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent(), existing}}
	rt := newRuntime(t, f)

	// No Alias: the CLI's flag handling is mutually exclusive, so an adopt
	// never carries one. Setting both here would test a state main.go cannot
	// produce, and would hide a lookup of the empty alias.
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", BuilderPane: "w2:p8", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if len(f.starts) != 0 {
		t.Errorf("adopting a pane must not start an agent, got %+v", f.starts)
	}
	if b.Builder.PaneID != "w2:p8" {
		t.Errorf("builder pane = %q, want w2:p8", b.Builder.PaneID)
	}
	if b.BuilderCandidate != "" {
		t.Errorf("BuilderAlias = %q, want empty for an adopted pane", b.BuilderCandidate)
	}
}

func TestBindRefusesACandidateThatDoesNotServeBuilder(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"test","model":"m","roles":["reviewer"]}]`)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testClaudeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})

	if !errors.Is(err, ErrRoleNotServed) {
		t.Fatalf("want ErrRoleNotServed, got %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("started %d agents; a refused bind must spawn nothing", len(f.starts))
	}
	// Also assert no pane was SPLIT. Checking only f.starts cannot tell a
	// refusal that happened before anything was created from one that ran after
	// builderPane and left a pane behind with nothing pointing at it -- the
	// ~800 MB leak CLAUDE.md warns about. This is what pins the refusal's
	// placement ahead of builderPane.
	if len(f.tabs) != 0 {
		t.Errorf("created %d tabs; a refused bind must not create a pane it then abandons", len(f.tabs))
	}
}

func TestBindSpawnUnknownAliasFails(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "claude/test/nope", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, candidate.ErrUnknownCandidate) {
		t.Fatalf("got %v, want ErrUnknownCandidate", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("an unknown alias must not start an agent, got %+v", f.starts)
	}
}

func TestBindResolvesTheOnlyBuilderCandidate(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--x"]}]`)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if len(f.starts) != 1 {
		t.Fatalf("starts = %+v, want 1 start", f.starts)
	}
	if f.starts[0].Kind != "agy" {
		t.Errorf("Kind = %q, want agy", f.starts[0].Kind)
	}
	if !reflect.DeepEqual(f.starts[0].Args, []string{"--model", "m", "--agent", "plan-executor", "--x"}) {
		t.Errorf("Args = %v, want [--model m --agent plan-executor --x]", f.starts[0].Args)
	}
	if b.BuilderCandidate != "agy/test/m" {
		t.Errorf("BuilderCandidate = %q, want agy/test/m", b.BuilderCandidate)
	}
}

func TestBindRefusesAnAmbiguousCandidate(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrAmbiguousCandidate) {
		t.Fatalf("want ErrAmbiguousCandidate, got %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("started %d agents; a refused bind must spawn nothing", len(f.starts))
	}
	if len(f.tabs) != 0 {
		t.Errorf("created %d tabs; a refused bind must not create a pane it then abandons", len(f.tabs))
	}
}

func TestBindWithNoCandidatesSaysSo(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, "[]")

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("want ErrNoCandidates, got %v", err)
	}
}

func TestResumeWithoutABuilderDoesNotSpawn(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/test/m", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("seed Bind: %v", err)
	}
	f.starts = nil

	_, err = Bind(context.Background(), rt, BindOptions{
		Name: b.Name, Resume: true, Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("resume without candidate must not spawn, got starts = %+v", f.starts)
	}
}

func TestBindRefusesSecondBindingOnSameTree(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	opts := BindOptions{Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo"}
	if _, err := Bind(context.Background(), rt, opts); err != nil {
		t.Fatalf("first Bind: %v", err)
	}

	opts.Name = "webshop2"
	_, err := Bind(context.Background(), rt, opts)
	if !errors.Is(err, store.ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken", err)
	}
}

func TestBindResumeRepointsPlannerAndKeepsRound(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	b.Round = 5
	b.State = store.StateOrphaned
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	f.agents = []herdr.Agent{{
		Kind: "claude", Status: herdr.StatusWorking, CWD: "/repo",
		PaneID: "w7:pB", Session: herdr.Session{Value: "planner-sess-2"},
	}}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w7:pB", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}
	if got.Round != 5 {
		t.Errorf("resume must keep the round, got %d", got.Round)
	}
	if got.Planner.SessionID != "planner-sess-2" || got.Planner.PaneID != "w7:pB" {
		t.Errorf("resume must repoint the planner, got %+v", got.Planner)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"webshop":    "webshop",
		"money/ai":   "money-ai",
		"My.Repo":    "my-repo",
		"2024-thing": "b2024-thing",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBindRefusesExistingName is the regression test for a silently broken
// second session: Save only rewrites bind.json, so the previous session's
// log.jsonl and NNN-*.md files survive and a fresh round 1 collides with the
// old round 1. Reconcile then reads the old report entry as "already handled"
// and the binding stalls with no error and no notification.
func TestBindRefusesExistingName(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 4, State: store.StateDone,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil {
		t.Fatal("binding an existing name must be refused")
	}

	// The refusal has to happen before anything is spawned, or it strands a
	// live builder pane with nothing pointing at it.
	if len(f.starts) != 0 {
		t.Errorf("no agent may be started, got %+v", f.starts)
	}
	if len(f.tabs) != 0 {
		t.Errorf("no tab may be created, got %d", len(f.tabs))
	}

	if !strings.Contains(err.Error(), "relay unbind webshop") {
		t.Errorf("error must name the unbind exit, got %q", err)
	}
	if !strings.Contains(err.Error(), "--resume") {
		t.Errorf("error must name the resume exit, got %q", err)
	}

	// The existing binding must be untouched by the refusal.
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Round != 4 || got.Builder.PaneID != "w1:p2" {
		t.Errorf("refused bind must not rewrite the binding, got %+v", got)
	}
}

// TestBindResumeStillAdoptsAnExistingName guards the exit the refusal offers.
func TestBindResumeStillAdoptsAnExistingName(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 4, State: store.StateOrphaned,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume must still adopt an existing binding: %v", err)
	}
	if got.Round != 4 || got.Planner.PaneID != "w2:p3" || got.State != store.StateActive {
		t.Errorf("resume = %+v", got)
	}
	if len(f.starts) != 0 {
		t.Errorf("resume must not start an agent, got %+v", f.starts)
	}
}

func TestBindRebindWithGoneBuilder(t *testing.T) {
	existing := store.Binding{
		Name:              "webshop",
		CWD:               "/repo",
		Round:             5,
		RoundBaselineTree: "tree-abc",
		State:             store.StateBroken,
		HaltNotifiedRound: 5,
		Halt:              "round 5 has run past 24h0m0s",
		HaltAt:            baseTime,
		BuilderScreen:     "some terminal output",
		BuilderScreenAt:   baseTime,
		Planner:           store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:           store.Endpoint{PaneID: "w2:p4", SessionID: "dead-builder-sess"},
		BuilderCandidate:  testOpencodeRef,
	}

	t.Run("spawn replacement builder", func(t *testing.T) {
		f := &fakeHerdr{
			agents: []herdr.Agent{
				plannerAgent(),
				{Kind: "opencode", Status: herdr.StatusWorking, PaneID: "w2:p9", Session: herdr.Session{Value: "new-builder-sess"}},
			},
			newPane: "w2:p9",
		}
		rt := newRuntime(t, f)
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("rebind: %v", err)
		}

		if got.Builder.PaneID != "w2:p9" || got.Builder.SessionID != "new-builder-sess" {
			t.Errorf("Builder = %+v, want pane w2:p9 session new-builder-sess", got.Builder)
		}
		if got.BuilderCandidate != testOpencodeRef {
			t.Errorf("BuilderCandidate = %q, want %s", got.BuilderCandidate, testOpencodeRef)
		}
		if got.BuilderScreen != "" {
			t.Errorf("BuilderScreen = %q, want empty", got.BuilderScreen)
		}
		if !got.BuilderScreenAt.IsZero() {
			t.Errorf("BuilderScreenAt = %v, want zero", got.BuilderScreenAt)
		}
		if got.HaltNotifiedRound != 0 {
			t.Errorf("HaltNotifiedRound = %d, want 0", got.HaltNotifiedRound)
		}
		if got.Halt != "" {
			t.Errorf("Halt = %q, want empty", got.Halt)
		}
		if !got.HaltAt.IsZero() {
			t.Errorf("HaltAt = %v, want zero", got.HaltAt)
		}
		if got.State != store.StateActive {
			t.Errorf("State = %s, want active", got.State)
		}
		if got.Round != 5 {
			t.Errorf("Round = %d, want 5 (untouched)", got.Round)
		}
		if got.CWD != "/repo" {
			t.Errorf("CWD = %q, want /repo (untouched)", got.CWD)
		}
		if got.RoundBaselineTree != "tree-abc" {
			t.Errorf("RoundBaselineTree = %q, want tree-abc (untouched)", got.RoundBaselineTree)
		}

		saved, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if saved.Builder.PaneID != "w2:p9" || saved.Builder.SessionID != "new-builder-sess" ||
			saved.BuilderScreen != "" || !saved.BuilderScreenAt.IsZero() ||
			saved.HaltNotifiedRound != 0 || saved.Halt != "" || !saved.HaltAt.IsZero() ||
			saved.Round != 5 || saved.RoundBaselineTree != "tree-abc" {
			t.Errorf("saved binding does not reflect rebind updates: %+v", saved)
		}
	})

	t.Run("adopt replacement builder pane", func(t *testing.T) {
		adopted := herdr.Agent{
			Kind:    "claude",
			Status:  herdr.StatusIdle,
			PaneID:  "w2:p8",
			Session: herdr.Session{Value: "adopted-sess"},
			CWD:     "/repo",
		}
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent(), adopted}}
		rt := newRuntime(t, f)
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, BuilderPane: "w2:p8", PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("rebind adopt: %v", err)
		}
		if len(f.starts) != 0 || len(f.tabs) != 0 {
			t.Errorf("adopting must not create tabs or start agents, tabs=%d starts=%+v", len(f.tabs), f.starts)
		}
		if got.Builder.PaneID != "w2:p8" || got.Builder.SessionID != "adopted-sess" {
			t.Errorf("Builder = %+v, want pane w2:p8 session adopted-sess", got.Builder)
		}
		if got.BuilderCandidate != "" {
			t.Errorf("BuilderAlias = %q, want empty for adopted builder", got.BuilderCandidate)
		}
		if got.BuilderScreen != "" || !got.BuilderScreenAt.IsZero() {
			t.Errorf("screen fields not cleared: screen=%q at=%v", got.BuilderScreen, got.BuilderScreenAt)
		}
		if got.HaltNotifiedRound != 0 {
			t.Errorf("HaltNotifiedRound = %d, want 0", got.HaltNotifiedRound)
		}
		if got.Halt != "" || !got.HaltAt.IsZero() {
			t.Errorf("Halt/HaltAt not cleared: halt=%q at=%v", got.Halt, got.HaltAt)
		}
	})
}

func TestBindRebindRefusesLiveBuilder(t *testing.T) {
	liveBuilder := herdr.Agent{
		Kind:    "opencode",
		Status:  herdr.StatusWorking,
		PaneID:  "w2:p4",
		Session: herdr.Session{Value: "live-builder-sess"},
		CWD:     "/repo",
	}
	f := &fakeHerdr{
		agents:  []herdr.Agent{plannerAgent(), liveBuilder},
		newPane: "w2:p5",
	}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", SessionID: "live-builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("got err = %v, want ErrBuilderAlive", err)
	}

	if len(f.tabs) != 0 {
		t.Errorf("no tab may be created when builder is alive, got %d", len(f.tabs))
	}
	if len(f.starts) != 0 {
		t.Errorf("no agent may be started when builder is alive, got %+v", f.starts)
	}

	// Existing binding must be untouched.
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Builder.PaneID != "w2:p4" || loaded.Builder.SessionID != "live-builder-sess" {
		t.Errorf("existing binding was modified: %+v", loaded)
	}
}

func TestBindRebindIdentityRule(t *testing.T) {
	t.Run("rebind succeeds when old pane is reused by unrelated agent", func(t *testing.T) {
		unrelatedAgent := herdr.Agent{
			Kind:    "opencode",
			Status:  herdr.StatusWorking,
			PaneID:  "w2:p4",
			Session: herdr.Session{Value: "unrelated-agent-sess"},
			CWD:     "/other",
		}
		f := &fakeHerdr{
			agents:  []herdr.Agent{plannerAgent(), unrelatedAgent},
			newPane: "w2:p9",
		}
		rt := newRuntime(t, f)

		existing := store.Binding{
			Name:    "webshop",
			CWD:     "/repo",
			Round:   5,
			State:   store.StateBroken,
			Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
			Builder: store.Endpoint{PaneID: "w2:p4", SessionID: "dead-builder-sess"},
		}
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("rebind must succeed when builder session is gone: %v", err)
		}
		if got.Builder.PaneID != "w2:p9" {
			t.Errorf("Builder.PaneID = %q, want w2:p9", got.Builder.PaneID)
		}
	})

	t.Run("rebind refused when builder session is alive", func(t *testing.T) {
		liveBuilder := herdr.Agent{
			Kind:    "opencode",
			Status:  herdr.StatusWorking,
			PaneID:  "w2:p4",
			Session: herdr.Session{Value: "live-builder-sess"},
			CWD:     "/repo",
		}
		f := &fakeHerdr{
			agents:  []herdr.Agent{plannerAgent(), liveBuilder},
			newPane: "w2:p9",
		}
		rt := newRuntime(t, f)

		existing := store.Binding{
			Name:    "webshop",
			CWD:     "/repo",
			Round:   3,
			State:   store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
			Builder: store.Endpoint{PaneID: "w2:p4", SessionID: "live-builder-sess"},
		}
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		_, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if !errors.Is(err, ErrBuilderAlive) {
			t.Fatalf("got err = %v, want ErrBuilderAlive", err)
		}
		if len(f.tabs) != 0 || len(f.starts) != 0 {
			t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts)
		}
	})
}

func TestBindResumeDoneBindingScope(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateDone,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2", SessionID: "builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	t.Run("planner-only resume of DONE binding succeeds", func(t *testing.T) {
		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("planner-only resume on done binding must succeed: %v", err)
		}
		if got.State != store.StateActive {
			t.Errorf("state = %s, want active", got.State)
		}
		if got.Planner.PaneID != "w2:p3" {
			t.Errorf("Planner.PaneID = %q, want w2:p3", got.Planner.PaneID)
		}
		if got.Builder != existing.Builder {
			t.Errorf("Builder = %+v, want %+v (untouched)", got.Builder, existing.Builder)
		}
		if len(f.tabs) != 0 || len(f.starts) != 0 {
			t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts)
		}
	})

	t.Run("rebind of DONE binding is refused", func(t *testing.T) {
		// Reset state to Done for this subtest
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("reset existing binding: %v", err)
		}
		_, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err == nil {
			t.Fatal("rebind on done binding must be refused")
		}
		wantMsg := `binding "webshop" is done: ` + "`relay bind` to start fresh"
		if !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("error = %q, want containing %q", err.Error(), wantMsg)
		}
		if len(f.tabs) != 0 || len(f.starts) != 0 {
			t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts)
		}
	})
}

func TestResumeRestoresMissingWorktree(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Fatalf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
	wantCall := checkoutWorktreeCall{Dir: "/repo", Path: wt, Branch: "relay/webshop"}
	if fg.checkoutWorktreeCalls[0] != wantCall {
		t.Errorf("checkoutWorktreeCall = %+v, want %+v", fg.checkoutWorktreeCalls[0], wantCall)
	}
	if res.RestoredWorktree != wt {
		t.Errorf("RestoredWorktree = %q, want %q", res.RestoredWorktree, wt)
	}
	if res.RestoredBranch != "relay/webshop" {
		t.Errorf("RestoredBranch = %q, want relay/webshop", res.RestoredBranch)
	}
	if res.OrphanedPane != "w2:p4" {
		t.Errorf("OrphanedPane = %q, want w2:p4", res.OrphanedPane)
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateActive {
		t.Errorf("loaded.State = %s, want active", loaded.State)
	}
}

func TestResumeRestoreHeadlessHasNoOrphan(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo
	fr := newFakeRunner()
	rt.Runner = fr

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if res.OrphanedPane != "" {
		t.Errorf("OrphanedPane = %q, want empty", res.OrphanedPane)
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Errorf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
}

func TestResumeRefusesRestoreWithoutBranch(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "no branch is recorded") {
		t.Fatalf("err = %v, want containing 'no branch is recorded'", err)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateDone {
		t.Errorf("loaded.State = %s, want done (unchanged)", loaded.State)
	}
}

func TestResumeRefusesRestoreFromWrongRepo(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{} // branchExists stays false: the caller's cwd has no relay/webshop
	rt.Git = fg

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt,
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/elsewhere",
	})
	if err == nil || !strings.Contains(err.Error(), "run resume from the repository") {
		t.Fatalf("err = %v, want 'run resume from the repository'", err)
	}
	if fg.lastBranchDir != "/elsewhere" || fg.lastBranchName != "relay/webshop" {
		t.Errorf("BranchExists asked (%q, %q), want (/elsewhere, relay/webshop)", fg.lastBranchDir, fg.lastBranchName)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateDone {
		t.Errorf("state = %s, want done (unchanged)", loaded.State)
	}
}

func TestResumeSurfacesBranchCheckedOut(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{checkoutWorktreeErr: git.ErrBranchCheckedOut}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "git worktree list") {
		t.Fatalf("err = %v, want containing 'git worktree list'", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("tabs=%d starts=%+v, want none", len(f.tabs), f.starts)
	}
}

func TestResumePresentWorktreeIsNotRestored(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := t.TempDir()
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateActive,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	if res.RestoredWorktree != "" {
		t.Errorf("RestoredWorktree = %q, want empty", res.RestoredWorktree)
	}
}

func TestRebindOnDoneWithRestoredWorktree(t *testing.T) {
	oldBuilder := herdr.Agent{
		Name:   "webshop-builder",
		Kind:   "opencode",
		Status: herdr.StatusIdle,
		PaneID: "w2:p4",
		CWD:    "/repo",
	}
	f := &fakeHerdr{
		agents:  []herdr.Agent{plannerAgent(), oldBuilder},
		newPane: "w2:p5",
	}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		Round:    4,
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Fatalf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
	if res.OrphanedPane != "w2:p4" {
		t.Errorf("OrphanedPane = %q, want w2:p4", res.OrphanedPane)
	}
	if got.Builder.PaneID != "w2:p5" {
		t.Errorf("Builder.PaneID = %q, want w2:p5", got.Builder.PaneID)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %s, want active", got.State)
	}
	if got.Builder.Headless() {
		t.Errorf("Builder is headless, want pane binding")
	}
}

func TestBindRebindNotFound(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got err = %v, want store.ErrNotFound", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts)
	}
}

func TestBindOpensBuilderInItsOwnTab(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4", newTab: "w2:pT"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		WorkspaceID: "w2",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if len(f.tabs) != 1 {
		t.Fatalf("got %d tab creations, want 1: placement is tab-only (#79)", len(f.tabs))
	}
	if got := f.tabs[0]; got.WorkspaceID != "w2" || got.CWD != "/repo" || got.Label != "webshop-builder" {
		t.Errorf("tab call = %+v", got)
	}
	if b.Builder.PaneID != "w2:pT" {
		t.Errorf("builder pane = %q, want the tab's root pane", b.Builder.PaneID)
	}
	if len(f.starts) != 1 || f.starts[0].Pane != "w2:pT" {
		t.Errorf("agent must start in the tab's root pane, got %+v", f.starts)
	}
}

func TestBindTimeoutOverrideAndDefault(t *testing.T) {
	t.Run("override is stored", func(t *testing.T) {
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)

		b, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
			RoundTimeout: 90 * time.Minute,
		})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if got, want := b.RoundTimeoutMS, int((90 * time.Minute).Milliseconds()); got != want {
			t.Errorf("RoundTimeoutMS = %d, want %d", got, want)
		}
	})

	t.Run("default is a day, not half an hour", func(t *testing.T) {
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)

		b, err := Bind(context.Background(), rt, BindOptions{
			Name: "kobe", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo2",
		})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		// A builder working a real stage runs for hours; 30m flagged healthy
		// work as needing a human on the first live run.
		if got, want := b.RoundTimeoutMS, int((24 * time.Hour).Milliseconds()); got != want {
			t.Errorf("default RoundTimeoutMS = %d, want %d (24h)", got, want)
		}
	})
}

func TestUnbindTeardown(t *testing.T) {
	ctx := context.Background()

	t.Run("ordinary binding unbinds with all-zero result", func(t *testing.T) {
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		b := store.Binding{
			Name: "webshop", CWD: "/repo", State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "webshop", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.ArchivedTo != "" || res.WorktreeRemoved != "" || res.WorktreeKept != "" || res.KeptReason != "" {
			t.Errorf("expected all-zero result for ordinary binding unbind, got %+v", res)
		}
		if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("binding state still exists after Unbind: %v", err)
		}
	})

	t.Run("clean worktree is removed", func(t *testing.T) {
		fg := &fakeGit{}
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-clean", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-clean", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeRemoved != wt {
			t.Errorf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, wt)
		}
		if res.WorktreeKept != "" {
			t.Errorf("WorktreeKept = %q, want empty", res.WorktreeKept)
		}
		if len(fg.removeWorktreeCalls) != 1 {
			t.Fatalf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
		}
		if fg.removeWorktreeCalls[0].Force {
			t.Error("teardown must pass force: false")
		}
		if _, err := rt.Store.Load("fork-clean"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should be removed")
		}
	})

	t.Run("dirty worktree is kept and says why", func(t *testing.T) {
		fg := &fakeGit{dirtyResult: true}
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-dirty", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-dirty", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeRemoved != "" {
			t.Errorf("WorktreeRemoved = %q, want empty", res.WorktreeRemoved)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "uncommitted changes" {
			t.Errorf("KeptReason = %q, want 'uncommitted changes'", res.KeptReason)
		}
		if len(fg.removeWorktreeCalls) != 0 {
			t.Error("RemoveWorktree must NOT be called for dirty tree")
		}
		if _, err := rt.Store.Load("fork-dirty"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})

	t.Run("dirty check error keeps worktree with honest reason", func(t *testing.T) {
		fg := &fakeGit{dirtyErr: errors.New("git lock busy\ndetails")}
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-dirty-err", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-dirty-err", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "dirty check failed: git lock busy" {
			t.Errorf("KeptReason = %q, want 'dirty check failed: git lock busy'", res.KeptReason)
		}
		if len(fg.removeWorktreeCalls) != 0 {
			t.Errorf("RemoveWorktree should not be called on dirty check error, got %d calls", len(fg.removeWorktreeCalls))
		}
	})

	t.Run("git unavailable keeps worktree", func(t *testing.T) {
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		rt.Git = nil

		b := store.Binding{
			Name: "fork-nogit", CWD: "/state/.worktrees/fork-nogit",
			Worktree: "/state/.worktrees/fork-nogit", State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-nogit", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != "/state/.worktrees/fork-nogit" || res.KeptReason != "git unavailable" {
			t.Errorf("kept mismatch: %+v", res)
		}
		if _, err := rt.Store.Load("fork-nogit"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})

	t.Run("git remove failure keeps worktree and completes unbind", func(t *testing.T) {
		fg := &fakeGit{removeWorktreeErr: errors.New("git lock locked\ndetails")}
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-fail", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-fail", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "git lock locked" {
			t.Errorf("KeptReason = %q, want 'git lock locked' (brief)", res.KeptReason)
		}
		if _, err := rt.Store.Load("fork-fail"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})
}

func TestUnbindReportsAnAlreadyGoneWorktree(t *testing.T) {
	ctx := context.Background()
	fg := &fakeGit{}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Git = fg

	missingWT := filepath.Join(t.TempDir(), "already-gone-worktree")
	b := store.Binding{
		Name: "fork-gone", CWD: "/repo",
		Worktree: missingWT, State: store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Unbind(ctx, rt, "fork-gone", false)
	if err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if res.WorktreeGone != missingWT {
		t.Errorf("WorktreeGone = %q, want %q", res.WorktreeGone, missingWT)
	}
	if res.WorktreeKept != "" || res.WorktreeRemoved != "" {
		t.Errorf("kept=%q removed=%q, want both empty", res.WorktreeKept, res.WorktreeRemoved)
	}
	if fg.dirtyCalls != 0 {
		t.Errorf("dirtyCalls = %d, want 0", fg.dirtyCalls)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("removeWorktreeCalls = %d, want 0", len(fg.removeWorktreeCalls))
	}
	if _, err := rt.Store.Load("fork-gone"); !errors.Is(err, store.ErrNotFound) {
		t.Error("binding state should still be deleted")
	}
}

func TestResumeRefusesRebindWhenSessionlessBuilderStillLives(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	// The builder was spawned with no session recorded -- the #20 condition --
	// but its pane still holds an agy agent, so it is alive.
	f.agents = []herdr.Agent{plannerAgent(), builderAgent(herdr.StatusWorking)}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo", Resume: true,
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("err = %v, want ErrBuilderAlive", err)
	}
	if len(f.starts) != 1 {
		t.Fatalf("started %d agents, want 1 (no builder spawned by the refused rebind)", len(f.starts))
	}
}

func TestResumeAllowsRebindWhenSessionlessBuilderPaneIsGone(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	// Pane w2:p4 no longer holds anything.
	f.agents = []herdr.Agent{plannerAgent()}
	f.newPane = "w2:p7"

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo", Resume: true,
	})
	if err != nil {
		t.Fatalf("Bind resume: %v", err)
	}
	if b.Builder.PaneID != "w2:p7" {
		t.Fatalf("builder pane = %q, want w2:p7", b.Builder.PaneID)
	}
}

// A session-less builder that cannot be located may be dead or may be alive in
// a pane that moved workspaces. relay cannot tell, so it must not spawn a
// replacement on the guess -- that is how a live builder gets orphaned.
func TestBindResumeRefusesUnverifiableBuilder(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:           "webshop",
		CWD:            "/repo",
		Round:          3,
		State:          store.StateBroken,
		Planner:        store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:        store.Endpoint{PaneID: "w2:p4", Kind: "agy"}, // never session-identified
		RoundStartedAt: time.Now().UTC(),
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderUnverified) {
		t.Fatalf("got err = %v, want ErrBuilderUnverified", err)
	}
	if !strings.Contains(err.Error(), "--assume-dead") {
		t.Errorf("error must name the flag that releases it, got %q", err)
	}
	if !strings.Contains(err.Error(), "w2:p4") {
		t.Errorf("error must name the pane to check, got %q", err)
	}

	// The refusal must land before anything irreversible.
	if len(f.tabs) != 0 {
		t.Errorf("no tab may be created, got %d", len(f.tabs))
	}
	if len(f.starts) != 0 {
		t.Errorf("no agent may be started, got %+v", f.starts)
	}

	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Builder.PaneID != "w2:p4" || loaded.Round != 3 {
		t.Errorf("binding must be untouched, got %+v", loaded)
	}
}

func TestBindResumeProceedsWithAssumeDead(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:           "webshop",
		CWD:            "/repo",
		Round:          3,
		State:          store.StateBroken,
		Planner:        store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:        store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
		RoundStartedAt: time.Now().UTC(),
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3",
		CWD: "/repo", AssumeDead: true,
	})
	if err != nil {
		t.Fatalf("Bind with AssumeDead: %v", err)
	}
	if got.Builder.PaneID != "w2:p5" {
		t.Errorf("builder pane = %q, want the newly spawned w2:p5", got.Builder.PaneID)
	}
}

// A builder with a recorded session is unambiguous: if no live agent carries
// that session it really is gone, so the gate must not fire.
func TestBindResumeUnaffectedWhenSessionRecorded(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy", SessionID: "dead-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
}

// A builder with an agent name is unambiguous: it can be identified by name, so
// the gate must not fire even when SessionID is empty and a round was open.
func TestBindResumeUnaffectedWhenNamedWithoutSession(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:           "webshop",
		CWD:            "/repo",
		Round:          3,
		State:          store.StateBroken,
		Planner:        store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:        store.Endpoint{PaneID: "w2:p4", Kind: "agy", AgentName: "webshop-builder"},
		RoundStartedAt: time.Now().UTC(),
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
}

// --assume-dead releases only the unverifiable case. A builder relay can
// positively see is alive is still refused: that is #20's guarantee.
func TestAssumeDeadNeverOverridesBuilderAlive(t *testing.T) {
	f := &fakeHerdr{
		agents: []herdr.Agent{
			plannerAgent(),
			{Kind: "agy", Status: herdr.StatusWorking, PaneID: "w2:p4",
				Session: herdr.Session{Value: "live-builder-sess"}},
		},
		newPane: "w2:p5",
	}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", SessionID: "live-builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3",
		CWD: "/repo", AssumeDead: true,
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("got err = %v, want ErrBuilderAlive even with AssumeDead", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("nothing may be spawned, tabs=%d starts=%+v", len(f.tabs), f.starts)
	}
}

// #20's recovery (PR #22): a session-less builder with no round in flight is
// unambiguous enough to rebind without ceremony. The gate must not broaden to
// catch this case -- if it ever does, this test fails loudly.
func TestBindResumeAllowsSessionlessRebindWhenRoundClosed(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy"}, // never session-identified
		// RoundStartedAt left zero: no round in flight.
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind resume: %v", err)
	}
	if got.Builder.PaneID != "w2:p5" {
		t.Errorf("builder pane = %q, want the newly spawned w2:p5", got.Builder.PaneID)
	}
}

func TestResumeRebindClearsRoundClosedTree(t *testing.T) {
	existing := store.Binding{
		Name:             "webshop",
		CWD:              "/repo",
		Round:            3,
		RoundClosedTree:  "tree-closed-123",
		State:            store.StateActive,
		Planner:          store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:          store.Endpoint{PaneID: "w2:p4", SessionID: "dead-builder-sess"},
		BuilderCandidate: testOpencodeRef,
	}

	t.Run("with alias", func(t *testing.T) {
		f := &fakeHerdr{
			agents: []herdr.Agent{
				plannerAgent(),
				{Kind: "opencode", Status: herdr.StatusWorking, PaneID: "w2:p9", Session: herdr.Session{Value: "new-builder-sess"}},
			},
			newPane: "w2:p9",
		}
		rt := newRuntime(t, f)
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("Bind resume with alias: %v", err)
		}
		if got.RoundClosedTree != "" {
			t.Errorf("returned RoundClosedTree = %q, want empty", got.RoundClosedTree)
		}

		saved, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if saved.RoundClosedTree != "" {
			t.Errorf("loaded RoundClosedTree = %q, want empty", saved.RoundClosedTree)
		}
	})

	t.Run("with builder pane", func(t *testing.T) {
		adopted := herdr.Agent{
			Kind:    "claude",
			Status:  herdr.StatusIdle,
			PaneID:  "w2:p8",
			Session: herdr.Session{Value: "adopted-sess"},
			CWD:     "/repo",
		}
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent(), adopted}}
		rt := newRuntime(t, f)
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, BuilderPane: "w2:p8", PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("Bind resume with builder pane: %v", err)
		}
		if got.RoundClosedTree != "" {
			t.Errorf("returned RoundClosedTree = %q, want empty", got.RoundClosedTree)
		}

		saved, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if saved.RoundClosedTree != "" {
			t.Errorf("loaded RoundClosedTree = %q, want empty", saved.RoundClosedTree)
		}
	})
}

func TestResumePlannerOnlyPreservesRoundClosedTree(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusWorking, CWD: "/repo",
			PaneID: "w7:pB", Session: herdr.Session{Value: "planner-sess-2"}},
	}}
	rt := newRuntime(t, f)

	const closedTree = "tree-closed-123"
	existing := store.Binding{
		Name:            "webshop",
		CWD:             "/repo",
		Round:           3,
		RoundClosedTree: closedTree,
		State:           store.StateOrphaned,
		Planner:         store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:         store.Endpoint{PaneID: "w2:p4", SessionID: "builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w7:pB", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind resume planner only: %v", err)
	}
	if got.RoundClosedTree != closedTree {
		t.Errorf("returned RoundClosedTree = %q, want %q", got.RoundClosedTree, closedTree)
	}

	saved, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.RoundClosedTree != closedTree {
		t.Errorf("loaded RoundClosedTree = %q, want %q", saved.RoundClosedTree, closedTree)
	}
}

func TestResumeRebindResolvesThroughTheOrder(t *testing.T) {
	// #92: a builder that halted between rounds is gone, no round is open,
	// and the planner wants a replacement without naming a token. Rebind
	// must walk policy.json order and the ledger exactly as create does,
	// and record the pick.
	existing := store.Binding{
		Name:             "webshop",
		CWD:              "/repo",
		Round:            3,
		State:            store.StateBroken,
		Planner:          store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:          store.Endpoint{PaneID: "w2:p4", SessionID: "dead-builder-sess", Kind: "opencode"},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{
		agents: []herdr.Agent{
			plannerAgent(),
			{Kind: "agy", Status: herdr.StatusWorking, PaneID: "w2:p9", Session: herdr.Session{Value: "new-builder-sess"}},
		},
		newPane: "w2:p9",
	}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}

	if res.How != HowOrder || res.Position != 1 {
		t.Errorf("resolution = %+v, want order #1", res)
	}
	if got.BuilderCandidate != testAgyRef {
		t.Errorf("BuilderCandidate = %q, want the order's first, %s", got.BuilderCandidate, testAgyRef)
	}
	if got.Builder.PaneID != "w2:p9" || got.State != store.StateActive || got.Round != 3 {
		t.Errorf("binding = %+v, want new builder in w2:p9, active, still round 3", got)
	}
	if len(f.starts) != 1 || f.starts[0].Kind != "agy" {
		t.Errorf("starts = %+v, want exactly one agy start", f.starts)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var picks int
	for _, e := range entries {
		if e.Kind == store.KindPick && e.Round == 3 && e.Note == ExplainResolution("builder", res) {
			picks++
		}
	}
	if picks != 1 {
		t.Errorf("want exactly one pick entry for round 3 reading %q, got entries %+v", ExplainResolution("builder", res), entries)
	}
}

func TestResumeRebindRefusesALiveBuilder(t *testing.T) {
	// --rebind is a rebind: the ErrBuilderAlive guard applies to it exactly
	// as it does to --builder.
	liveBuilder := herdr.Agent{
		Kind: "opencode", Status: herdr.StatusWorking, PaneID: "w2:p4",
		Session: herdr.Session{Value: "live-builder-sess"}, CWD: "/repo",
	}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent(), liveBuilder}, newPane: "w2:p5"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	existing := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 3, State: store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", SessionID: "live-builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("got %v, want ErrBuilderAlive", err)
	}
	if len(f.starts) != 0 || len(f.tabs) != 0 {
		t.Errorf("a refused rebind must spawn nothing: starts=%+v tabs=%+v", f.starts, f.tabs)
	}
}

func TestBindHeadlessRecordsAnEndpointAndSpawnsNothing(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Fatalf("headless must open no tab and start no agent: tabs=%d starts=%d", len(f.tabs), len(f.starts))
	}
	ep := b.Builder
	if !ep.Headless() || ep.Mode != store.ModeHeadless {
		t.Errorf("Mode = %q, want headless", ep.Mode)
	}
	if ep.AgentName != "webshop-builder" || ep.Kind != "opencode" {
		t.Errorf("AgentName/Kind = %q/%q, want webshop-builder/opencode", ep.AgentName, ep.Kind)
	}
	if ep.PaneID != "" || ep.SessionID != "" || ep.PID != 0 || ep.LogPath != "" || ep.StartedAt != 0 {
		t.Errorf("spec §3.1 invariants broken: %+v", ep)
	}
	if b.BuilderCandidate != testOpencodeRef || res.Token() != testOpencodeRef {
		t.Errorf("candidate = %q / %q, want %q", b.BuilderCandidate, res.Token(), testOpencodeRef)
	}
	if b.Round != 1 || b.State != store.StateActive {
		t.Errorf("round/state = %d/%s, want 1/active", b.Round, b.State)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil || len(entries) != 1 || entries[0].Kind != store.KindPick {
		t.Errorf("a fresh headless binding logs its pick and nothing else: %+v (%v)", entries, err)
	}
	// The stored binding reads back headless too.
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() {
		t.Errorf("stored builder: %+v (%v)", stored.Builder, err)
	}
}

func TestBindHeadlessRefusesAdoptAndResumeBeforeListingAgents(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", BuilderPane: "w2:p4", PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if !errors.Is(err, ErrHeadlessAdopt) {
		t.Errorf("headless + adopt: err = %v, want ErrHeadlessAdopt", err)
	}
	_, err = Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if !errors.Is(err, ErrHeadlessResume) {
		t.Errorf("headless + resume: err = %v, want ErrHeadlessResume", err)
	}
	if f.listCalls != 0 {
		t.Errorf("refusals must happen before herdr is asked anything: listCalls = %d", f.listCalls)
	}
	if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused bind must save nothing: %v", err)
	}
}

// A binding's mode is fixed at creation. Rebinding a headless binding whose
// process is gone must produce another headless endpoint, not a pane (#119).
func TestBindResumeRebindKeepsAHeadlessBindingHeadless(t *testing.T) {
	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{
			AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless,
			PID: 4321, StartedAt: 1_700_000_000, LogPath: "/state/webshop/004-builder.log",
		},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	fr := newFakeRunner()
	fr.script(4321, false) // the old process is gone
	rt.Runner = fr
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Fatalf("a headless rebind must open no tab and start no agent: tabs=%+v starts=%+v", f.tabs, f.starts)
	}
	ep := got.Builder
	if !ep.Headless() || ep.Mode != store.ModeHeadless {
		t.Errorf("Mode = %q, want headless", ep.Mode)
	}
	if ep.PaneID != "" || ep.SessionID != "" || ep.PID != 0 || ep.LogPath != "" || ep.StartedAt != 0 {
		t.Errorf("rebound endpoint must be a fresh headless endpoint with nothing running: %+v", ep)
	}
	if got.BuilderCandidate != testAgyRef || res.How != HowOrder {
		t.Errorf("candidate = %q (%+v), want the order's first, %s", got.BuilderCandidate, res, testAgyRef)
	}
	if got.State != store.StateActive || got.Round != 4 {
		t.Errorf("state/round = %s/%d, want active/4", got.State, got.Round)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() {
		t.Errorf("stored builder: %+v (%v)", stored.Builder, err)
	}
}

// herdr's agent list cannot see a process, so a headless binding's liveness
// is the Runner's answer. A live process refuses the rebind (§4.3).
func TestBindResumeRebindRefusesALiveHeadlessProcess(t *testing.T) {
	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{
			AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless,
			PID: 4321, StartedAt: 1_700_000_000, LogPath: "/state/webshop/004-builder.log",
		},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	fr := newFakeRunner()
	fr.script(4321, true) // still running
	rt.Runner = fr
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("err = %v, want ErrBuilderAlive", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("a refused rebind must spawn nothing: tabs=%+v starts=%+v", f.tabs, f.starts)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || stored.Builder.PID != 4321 || !stored.Builder.Headless() {
		t.Errorf("a refused rebind must leave the binding untouched: %+v (%v)", stored.Builder, err)
	}
}

// A pane cannot replace a process builder; the mode is fixed at creation.
func TestBindResumeRefusesAPaneForAHeadlessBinding(t *testing.T) {
	existing := store.Binding{
		Name:             "webshop",
		CWD:              "/repo",
		Round:            4,
		State:            store.StateBroken,
		Planner:          store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:          store.Endpoint{AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{agents: []herdr.Agent{
		plannerAgent(),
		{Kind: "agy", Status: herdr.StatusIdle, PaneID: "w2:p9", CWD: "/repo", Session: herdr.Session{Value: "stray-sess"}},
	}}
	rt := newRuntime(t, f)
	rt.Runner = newFakeRunner()
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, BuilderPane: "w2:p9", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrHeadlessAdopt) {
		t.Fatalf("err = %v, want ErrHeadlessAdopt", err)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() || stored.Builder.PaneID != "" {
		t.Errorf("a refused rebind must leave the binding untouched: %+v (%v)", stored.Builder, err)
	}
}

func TestBindHeadlessStillRefusesANameHerdrWouldRefuse(t *testing.T) {
	// The agent name is validated even though no herdr agent is started:
	// the name is what status, log and a later pane-mode rebind identify
	// the builder by, and the limit must not depend on the mode.
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	name := "abcdefghij1234567890abcde" // 25 chars; + "-builder" = 33
	_, err := Bind(context.Background(), rt, BindOptions{
		Name: name, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if err == nil || !strings.Contains(err.Error(), "builder agent name") {
		t.Fatalf("err = %v, want the agent-name refusal", err)
	}
}

func TestBindWithTierEditOnClaude(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testClaudeRef,
		PlannerPane: "w2:p3",
		CWD:         "/repo",
		Tier:        "edit",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Tier != "edit" {
		t.Errorf("b.Tier = %q, want %q", b.Tier, "edit")
	}
	if len(f.starts) != 1 {
		t.Fatalf("got %d agent starts, want 1", len(f.starts))
	}
	wantClaudeArgs := []string{"--model", "m", "--agent", "plan-executor", "--permission-mode", "acceptEdits"}
	if !reflect.DeepEqual(f.starts[0].Args, wantClaudeArgs) {
		t.Errorf("expected %v in starts[0].Args, got %v", wantClaudeArgs, f.starts[0].Args)
	}
}

func TestBindPaneCodexTierEditFillsStateDir(t *testing.T) {
	const codexCandidatesJSON = `[
	  {"harness":"codex","provider":"test","model":"gpt-5.6-terra","roles":["builder"]}
	]`
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, codexCandidatesJSON)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   "codex/test/gpt-5.6-terra",
		PlannerPane: "w2:p3",
		CWD:         "/repo",
		Tier:        "edit",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if len(f.starts) != 1 {
		t.Fatalf("got %d agent starts, want 1", len(f.starts))
	}
	wantRoot := fmt.Sprintf(`sandbox_workspace_write.writable_roots=[%q]`, rt.Store.Dir(b.Name))
	found := false
	for i, arg := range f.starts[0].Args {
		if arg == "-c" && i+1 < len(f.starts[0].Args) && f.starts[0].Args[i+1] == wantRoot {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected -c followed by %s in starts[0].Args, got %v", wantRoot, f.starts[0].Args)
	}
	for _, arg := range f.starts[0].Args {
		if arg == harness.StatePlaceholder {
			t.Errorf("StatePlaceholder survived in starts[0].Args: %v", f.starts[0].Args)
		}
	}
}

func TestBindWithTierYoloWithoutAllowYoloRefused(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testClaudeRef,
		PlannerPane: "w2:p3",
		CWD:         "/repo",
		Tier:        "yolo",
	})
	if !errors.Is(err, ErrTierAboveMax) {
		t.Fatalf("err = %v, want ErrTierAboveMax", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("tabs = %d, starts = %d; want 0", len(f.tabs), len(f.starts))
	}
	if _, loadErr := rt.Store.Load("webshop"); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound", loadErr)
	}
}

func TestBindOpencodeCandidateTierReadRefused(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testOpencodeRef,
		PlannerPane: "w2:p3",
		CWD:         "/repo",
		Tier:        "read",
	})
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("err = %v, want harness.ErrTierUnsupported", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("tabs = %d, starts = %d; want 0", len(f.tabs), len(f.starts))
	}
	if _, loadErr := rt.Store.Load("webshop"); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound", loadErr)
	}
}

func TestBindPolicyTierBuilderRead(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Tier = map[string]string{"builder": "read"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testClaudeRef,
		PlannerPane: "w2:p3",
		CWD:         "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Tier != "read" {
		t.Errorf("b.Tier = %q, want %q", b.Tier, "read")
	}
	if len(f.starts) != 1 {
		t.Fatalf("got %d agent starts, want 1", len(f.starts))
	}
	hasFlag := false
	for i, arg := range f.starts[0].Args {
		if arg == "--permission-mode" && i+1 < len(f.starts[0].Args) && f.starts[0].Args[i+1] == "plan" {
			hasFlag = true
			break
		}
	}
	if !hasFlag {
		t.Errorf("expected --permission-mode plan in starts[0].Args, got %v", f.starts[0].Args)
	}
}

// TestBindGateFlagStored pins #132: an explicit --gate is stored on the
// binding as given.
func TestBindGateFlagStored(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
		Gate: "make check",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "make check" {
		t.Errorf("b.Gate = %q, want %q", b.Gate, "make check")
	}
}

// TestBindGatePolicyDefaultApplied pins #132: with no --gate, policy.json's
// gate.default is used.
func TestBindGatePolicyDefaultApplied(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Gate = &policy.GatePolicy{Default: "make check"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "make check" {
		t.Errorf("b.Gate = %q, want the policy default %q", b.Gate, "make check")
	}
}

// TestBindNoGateOverridesPolicyDefault pins #132: --no-gate opts a binding
// out of policy.json's gate.default.
func TestBindNoGateOverridesPolicyDefault(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Gate = &policy.GatePolicy{Default: "make check"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
		NoGate: true,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "" {
		t.Errorf("b.Gate = %q, want empty despite the policy default", b.Gate)
	}
}

// TestForkInheritsSourceGate pins #132: a fork with no --gate/--no-gate
// inherits the source binding's Gate.
func TestForkInheritsSourceGate(t *testing.T) {
	ctx := context.Background()
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)
	src, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Gate = "make check"
	if err := rt.Store.Save(src); err != nil {
		t.Fatal(err)
	}

	res, err := Fork(ctx, rt, ForkOptions{
		Source:      "source",
		Round:       2,
		NewName:     "alt",
		PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if res.Binding.Gate != "make check" {
		t.Errorf("Binding.Gate = %q, want inherited from source", res.Binding.Gate)
	}

	stored, err := rt.Store.Load("alt")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Gate != "make check" {
		t.Errorf("stored Gate = %q, want inherited from source", stored.Gate)
	}
}
