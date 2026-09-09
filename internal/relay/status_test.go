package relay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestStatusReportsLiveAgentState(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(rep.Bindings))
	}

	got := rep.Bindings[0]
	if got.PlannerStatus != herdr.StatusWorking || got.BuilderStatus != herdr.StatusWorking {
		t.Errorf("status must come from the live agent list, got %+v", got)
	}
	if got.Display != "ACTIVE" {
		t.Errorf("display = %q, want ACTIVE", got.Display)
	}
	if got.BuilderAlias != "abuilder" {
		t.Errorf("alias = %q", got.BuilderAlias)
	}
}

func TestStatusMarksMissingAgentsAsGone(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = nil

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].BuilderStatus != "gone" {
		t.Errorf("builder status = %q, want gone", rep.Bindings[0].BuilderStatus)
	}
}

func TestStatusSurfacesHeldPending(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	b.State = store.StateHeld
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true), builderAgent(herdr.StatusIdle)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.Display != "HELD" || got.Pending == nil {
		t.Fatalf("held binding must show its pending payload, got %+v", got)
	}

	text := RenderStatus(rep)
	if !strings.Contains(text, "HELD") || !strings.Contains(text, "webshop") {
		t.Errorf("rendered status = %q", text)
	}
	if !strings.Contains(text, "pending") {
		t.Errorf("rendered status must still show a pending line, got %q", text)
	}
}

// TestStatusJSONCarriesStructuredFields guards the statusline interface: Last
// and Pending must serialise as JSON objects with typed fields, not as
// rendered prose the consumer would have to regex apart.
func TestStatusJSONCarriesStructuredFields(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	b.State = store.StateHeld
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true), builderAgent(herdr.StatusIdle)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	bindings, ok := decoded["bindings"].([]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("bindings = %#v", decoded["bindings"])
	}
	row, ok := bindings[0].(map[string]any)
	if !ok {
		t.Fatalf("row is not a JSON object: %#v", bindings[0])
	}

	pending, ok := row["pending"].(map[string]any)
	if !ok {
		t.Fatalf("pending must be a JSON object, not prose, got %#v", row["pending"])
	}
	if _, ok := pending["round"].(float64); !ok {
		t.Errorf("pending.round missing or not numeric: %#v", pending)
	}
	if _, ok := pending["kind"].(string); !ok {
		t.Errorf("pending.kind missing or not a string: %#v", pending)
	}

	last, ok := row["last"].(map[string]any)
	if !ok {
		t.Fatalf("last must be a JSON object, not prose, got %#v", row["last"])
	}
	if _, ok := last["round"].(float64); !ok {
		t.Errorf("last.round missing or not numeric: %#v", last)
	}
	if _, ok := last["direction"].(string); !ok {
		t.Errorf("last.direction missing or not a string: %#v", last)
	}
}

func TestDoneStopsRelaying(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)

	if err := Done(context.Background(), rt, b.Name); err != nil {
		t.Fatalf("Done: %v", err)
	}

	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != store.StateDone {
		t.Errorf("state = %s, want done", got.State)
	}
}

func TestStatusJSONForkProvenance(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	// 1. Ordinary binding: forked_from and forked_at_round must be omitted from JSON
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	humanBefore := RenderStatus(rep)

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	row := decoded["bindings"].([]any)[0].(map[string]any)
	if _, ok := row["forked_from"]; ok {
		t.Errorf("ordinary binding must omit forked_from: %+v", row)
	}
	if _, ok := row["forked_at_round"]; ok {
		t.Errorf("ordinary binding must omit forked_at_round: %+v", row)
	}

	// 2. Forked binding: forked_from and forked_at_round must be present in JSON
	b.ForkedFrom = "source"
	b.ForkedAtRound = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	repFork, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status fork: %v", err)
	}
	humanAfter := RenderStatus(repFork)

	// RenderStatus human output must be byte-identical
	if humanBefore != humanAfter {
		t.Errorf("RenderStatus human output changed for fork:\nbefore:\n%s\nafter:\n%s", humanBefore, humanAfter)
	}

	rawFork, err := json.Marshal(repFork)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decodedFork map[string]any
	if err := json.Unmarshal(rawFork, &decodedFork); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	rowFork := decodedFork["bindings"].([]any)[0].(map[string]any)
	if got, ok := rowFork["forked_from"].(string); !ok || got != "source" {
		t.Errorf("forked_from = %v, want 'source'", rowFork["forked_from"])
	}
	if got, ok := rowFork["forked_at_round"].(float64); !ok || got != 2 {
		t.Errorf("forked_at_round = %v, want 2", rowFork["forked_at_round"])
	}
}

func TestStatusDetailsBrokenBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.State = store.StateBroken
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The builder is gone; only the planner is live.
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]

	if got.Display != "NEEDS YOU" {
		t.Errorf("display = %q, want NEEDS YOU (the collapse must not change)", got.Display)
	}
	want := DiagnoseBuilder(b).Detail(b.Round)
	if got.Detail != want {
		t.Errorf("detail =\n  %q\nwant\n  %q", got.Detail, want)
	}
	// sentBinding leaves the builder session-less, so the warning must appear.
	if !strings.Contains(got.Detail, "moved pane") {
		t.Errorf("detail must warn about a moved pane, got %q", got.Detail)
	}
}

func TestStatusOmitsDetailForHealthyBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.Detail != "" {
		t.Errorf("detail = %q, want empty for a healthy binding", got.Detail)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "detail") {
		t.Errorf("detail must be omitempty, got %s", raw)
	}
}

// orphaned also collapses into NEEDS YOU but is not overloaded, so it gets no
// detail. This pins the scope decision.
func TestStatusOmitsDetailForOrphanedBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.State = store.StateOrphaned
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = nil

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if d := rep.Bindings[0].Detail; d != "" {
		t.Errorf("detail = %q, want empty for orphaned", d)
	}
}

func TestRenderStatusShowsDetailLine(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "doctor", CWD: "/repo", Workspace: "wM", Round: 3,
		Display:      "NEEDS YOU",
		BuilderAlias: "abuilder",
		PlannerPane:  "wM:p1", PlannerKind: "claude", PlannerStatus: "idle",
		BuilderPane: "wM:pV", BuilderKind: "agy", BuilderStatus: "gone",
		Detail: "round 2 report delivered; nothing outstanding -- unless you want another round",
	}}})

	if !strings.Contains(out, "  detail   round 2 report delivered") {
		t.Errorf("detail line missing from:\n%s", out)
	}
	// It must sit between the builder line and pending, where a human about to
	// rebind is already looking.
	builderAt := strings.Index(out, "  builder ")
	detailAt := strings.Index(out, "  detail ")
	pendingAt := strings.Index(out, "  pending ")
	if !(builderAt < detailAt && detailAt < pendingAt) {
		t.Errorf("detail must follow builder and precede pending, got:\n%s", out)
	}
}

func TestRenderStatusOmitsEmptyDetail(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "ok", CWD: "/repo", Round: 1, Display: "ACTIVE",
		PlannerPane: "wM:p1", PlannerKind: "claude", PlannerStatus: "idle",
		BuilderPane: "wM:p2", BuilderKind: "agy", BuilderStatus: "working",
		BuilderAlias: "abuilder",
	}}})

	if strings.Contains(out, "detail") {
		t.Errorf("no detail line may appear for a healthy binding:\n%s", out)
	}
}
