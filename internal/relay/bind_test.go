package relay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func newRuntime(t *testing.T, f *fakeHerdr) Runtime {
	t.Helper()
	return Runtime{
		Herdr:   f,
		Store:   store.New(t.TempDir()),
		Aliases: alias.DefaultTable(),
		Now:     func() time.Time { return time.Unix(1757000000, 0).UTC() },
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
		Name: "upjo", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if len(f.starts) != 1 {
		t.Fatalf("got %d agent starts, want 1", len(f.starts))
	}
	got := f.starts[0]
	if got.Kind != "opencode" || got.Pane != "w2:p4" || got.Name != "upjo-builder" {
		t.Errorf("start = %+v", got)
	}
	if b.Builder.PaneID != "w2:p4" || b.Planner.SessionID != "planner-sess" {
		t.Errorf("binding = %+v", b)
	}
	if b.Round != 1 || b.State != store.StateActive {
		t.Errorf("new binding must start at round 1 and active, got %d/%s", b.Round, b.State)
	}
}

func TestBindAdoptsExistingBuilderPane(t *testing.T) {
	existing := herdr.Agent{Kind: "claude", Status: herdr.StatusIdle, PaneID: "w2:p8", CWD: "/repo"}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent(), existing}}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "upjo", Alias: "cbuilder", BuilderPane: "w2:p8", PlannerPane: "w2:p3", CWD: "/repo",
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
}

func TestBindRefusesSecondBindingOnSameTree(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	opts := BindOptions{Name: "upjo", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo"}
	if _, err := Bind(context.Background(), rt, opts); err != nil {
		t.Fatalf("first Bind: %v", err)
	}

	opts.Name = "upjo2"
	_, err := Bind(context.Background(), rt, opts)
	if !errors.Is(err, store.ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken", err)
	}
}

func TestBindResumeRepointsPlannerAndKeepsRound(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "upjo", Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
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
		Name: "upjo", Resume: true, PlannerPane: "w7:pB", CWD: "/repo",
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
		"uniqueperfumesjo": "uniqueperfumesjo",
		"money/ai":         "money-ai",
		"My.Repo":          "my-repo",
		"2024-thing":       "b2024-thing",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
