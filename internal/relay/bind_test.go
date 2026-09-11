package relay

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// baseTime is the instant newRuntime's fixed clock reports.
var baseTime = time.Unix(1757000000, 0).UTC()

func newRuntime(t *testing.T, f *fakeHerdr) Runtime {
	t.Helper()
	return Runtime{
		Herdr:   f,
		Store:   store.New(t.TempDir()),
		Aliases: alias.DefaultTable(),
		Now:     func() time.Time { return baseTime },
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
		Name: "webshop", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
		Name: "webshop", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
		Name: "webshop", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
	if b.BuilderAlias != "" {
		t.Errorf("BuilderAlias = %q, want empty for an adopted pane", b.BuilderAlias)
	}
}

func TestBindRefusesAConsultAlias(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Aliases = consultTable(t)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Alias: "reviewer", PlannerPane: "w2:p3", CWD: "/repo",
	})

	if !errors.Is(err, ErrConsultAlias) {
		t.Fatalf("want ErrConsultAlias, got %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("started %d agents; a refused bind must spawn nothing", len(f.starts))
	}
	// Also assert no pane was SPLIT. Checking only f.starts cannot tell a
	// refusal that happened before anything was created from one that ran after
	// builderPane and left a pane behind with nothing pointing at it -- the
	// ~800 MB leak CLAUDE.md warns about. This is what pins the refusal's
	// placement ahead of builderPane.
	if f.splits != 0 {
		t.Errorf("split %d panes; a refused bind must not create a pane it then abandons", f.splits)
	}
}

func TestBindSpawnUnknownAliasFails(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Alias: "nope", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, alias.ErrUnknownAlias) {
		t.Fatalf("got %v, want ErrUnknownAlias", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("an unknown alias must not start an agent, got %+v", f.starts)
	}
}

// There is no default builder. A bare `relay bind` must say so rather than
// pick one, and must not have split a pane before finding that out.
func TestBindWithoutBuilderFails(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil {
		t.Fatal("bind with no --builder and no pane to adopt must fail")
	}
	if !strings.Contains(err.Error(), "--builder") {
		t.Errorf("error should name the missing flag, got: %v", err)
	}
	// The known aliases belong in the message: it is the only place a new user
	// finds out what they can pass.
	if !strings.Contains(err.Error(), "cbuilder") {
		t.Errorf("error should list the known aliases, got: %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("nothing may be started without a builder, got %+v", f.starts)
	}
}

func TestBindRefusesSecondBindingOnSameTree(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	opts := BindOptions{Name: "webshop", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo"}
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
		Name: "webshop", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
		Name: "webshop", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil {
		t.Fatal("binding an existing name must be refused")
	}

	// The refusal has to happen before anything is spawned, or it strands a
	// live builder pane with nothing pointing at it.
	if len(f.starts) != 0 {
		t.Errorf("no agent may be started, got %+v", f.starts)
	}
	if f.splits != 0 {
		t.Errorf("no pane may be split, got %d", f.splits)
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
		BuilderScreen:     "some terminal output",
		BuilderScreenAt:   baseTime,
		Planner:           store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:           store.Endpoint{PaneID: "w2:p4", SessionID: "dead-builder-sess"},
		BuilderAlias:      "builder",
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
			Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("rebind: %v", err)
		}

		if got.Builder.PaneID != "w2:p9" || got.Builder.SessionID != "new-builder-sess" {
			t.Errorf("Builder = %+v, want pane w2:p9 session new-builder-sess", got.Builder)
		}
		if got.BuilderAlias != "builder" {
			t.Errorf("BuilderAlias = %q, want builder", got.BuilderAlias)
		}
		if !got.PreamblePending {
			t.Errorf("PreamblePending = false, want true")
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
			!saved.PreamblePending || saved.BuilderScreen != "" || !saved.BuilderScreenAt.IsZero() ||
			saved.HaltNotifiedRound != 0 || saved.Round != 5 || saved.RoundBaselineTree != "tree-abc" {
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
		if len(f.starts) != 0 || f.splits != 0 {
			t.Errorf("adopting must not split or start agents, splits=%d starts=%+v", f.splits, f.starts)
		}
		if got.Builder.PaneID != "w2:p8" || got.Builder.SessionID != "adopted-sess" {
			t.Errorf("Builder = %+v, want pane w2:p8 session adopted-sess", got.Builder)
		}
		if got.BuilderAlias != "" {
			t.Errorf("BuilderAlias = %q, want empty for adopted builder", got.BuilderAlias)
		}
		if !got.PreamblePending {
			t.Errorf("PreamblePending = false, want true")
		}
		if got.BuilderScreen != "" || !got.BuilderScreenAt.IsZero() {
			t.Errorf("screen fields not cleared: screen=%q at=%v", got.BuilderScreen, got.BuilderScreenAt)
		}
		if got.HaltNotifiedRound != 0 {
			t.Errorf("HaltNotifiedRound = %d, want 0", got.HaltNotifiedRound)
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
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("got err = %v, want ErrBuilderAlive", err)
	}

	if f.splits != 0 {
		t.Errorf("no pane may be split when builder is alive, got %d splits", f.splits)
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
			Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("rebind must succeed when builder session is gone: %v", err)
		}
		if got.Builder.PaneID != "w2:p9" {
			t.Errorf("Builder.PaneID = %q, want w2:p9", got.Builder.PaneID)
		}
		if !got.PreamblePending {
			t.Errorf("PreamblePending = false, want true")
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
			Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
		})
		if !errors.Is(err, ErrBuilderAlive) {
			t.Fatalf("got err = %v, want ErrBuilderAlive", err)
		}
		if f.splits != 0 || len(f.starts) != 0 {
			t.Errorf("no pane may be split or agent started, splits=%d starts=%+v", f.splits, f.starts)
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
		if f.splits != 0 || len(f.starts) != 0 {
			t.Errorf("no pane may be split or agent started, splits=%d starts=%+v", f.splits, f.starts)
		}
	})

	t.Run("rebind of DONE binding is refused", func(t *testing.T) {
		// Reset state to Done for this subtest
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("reset existing binding: %v", err)
		}
		_, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err == nil {
			t.Fatal("rebind on done binding must be refused")
		}
		wantMsg := `binding "webshop" is done: ` + "`relay bind` to start fresh"
		if !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("error = %q, want containing %q", err.Error(), wantMsg)
		}
		if f.splits != 0 || len(f.starts) != 0 {
			t.Errorf("no pane may be split or agent started, splits=%d starts=%+v", f.splits, f.starts)
		}
	})
}

func TestBindRebindNotFound(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got err = %v, want store.ErrNotFound", err)
	}
	if f.splits != 0 || len(f.starts) != 0 {
		t.Errorf("no pane may be split or agent started, splits=%d starts=%+v", f.splits, f.starts)
	}
}

func TestBindOpensBuilderInItsOwnTabWhenAsked(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newTab: "w2:pT"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
		NewTab: true, WorkspaceID: "w2",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if len(f.tabs) != 1 {
		t.Fatalf("got %d tab creations, want 1", len(f.tabs))
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

func TestBindSplitsThePlannerPaneByDefault(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4", newTab: "w2:pT"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if len(f.tabs) != 0 {
		t.Errorf("no tab may be created without --tab, got %+v", f.tabs)
	}
	if b.Builder.PaneID != "w2:p4" {
		t.Errorf("builder pane = %q, want the split pane", b.Builder.PaneID)
	}
}

func TestBindTimeoutOverrideAndDefault(t *testing.T) {
	t.Run("override is stored", func(t *testing.T) {
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)

		b, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
			Name: "kobe", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo2",
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
		Name: "webshop", Alias: "abuilder", PlannerPane: "w2:p3", CWD: "/repo", Resume: true,
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
		Name: "webshop", Alias: "abuilder", PlannerPane: "w2:p3", CWD: "/repo", Resume: true,
	})
	if err != nil {
		t.Fatalf("Bind resume: %v", err)
	}
	if b.Builder.PaneID != "w2:p7" {
		t.Fatalf("builder pane = %q, want w2:p7", b.Builder.PaneID)
	}
	if !b.PreamblePending {
		t.Fatal("a replacement builder must get the preamble")
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
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
	if f.splits != 0 {
		t.Errorf("no pane may be split, got %d splits", f.splits)
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
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3",
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
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3",
		CWD: "/repo", AssumeDead: true,
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("got err = %v, want ErrBuilderAlive even with AssumeDead", err)
	}
	if f.splits != 0 || len(f.starts) != 0 {
		t.Errorf("nothing may be spawned, splits=%d starts=%+v", f.splits, f.starts)
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
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
		Name:            "webshop",
		CWD:             "/repo",
		Round:           3,
		RoundClosedTree: "tree-closed-123",
		State:           store.StateActive,
		Planner:         store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:         store.Endpoint{PaneID: "w2:p4", SessionID: "dead-builder-sess"},
		BuilderAlias:    "builder",
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
			Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
