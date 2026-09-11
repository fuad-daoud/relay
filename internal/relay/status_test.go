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

func TestStatusReportsForeignAgentInBoundTree(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	stranger := herdr.Agent{
		PaneID: "w9:p9", Kind: "claude", Status: herdr.StatusIdle,
		CWD: b.CWD, Title: "plan-executor",
	}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking), stranger}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0].Foreign
	if len(got) != 1 {
		t.Fatalf("got %d foreign agents, want 1: %+v", len(got), got)
	}
	if got[0].PaneID != "w9:p9" || got[0].Title != "plan-executor" {
		t.Errorf("foreign = %+v", got[0])
	}
}

func TestStatusReportsNoForeignAgentsForHealthyBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep.Bindings[0].Foreign; got != nil {
		t.Errorf("foreign = %+v, want nil", got)
	}
}

func TestStatusForeignDoesNotChangeDisplay(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		builderAgent(herdr.StatusWorking),
		{PaneID: "w9:p9", Kind: "claude", Status: herdr.StatusIdle, CWD: b.CWD},
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].Display != "ACTIVE" {
		t.Errorf("display = %q, want ACTIVE: a foreign agent is an observation, not a state",
			rep.Bindings[0].Display)
	}
}

func TestStatusJSONOmitsForeignWhenEmpty(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "foreign") {
		t.Errorf("empty Foreign must be omitted from JSON, got %s", raw)
	}
}

func TestRenderStatusShowsForeignLine(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p9", Kind: "claude", Status: "idle", CWD: "/repo", Title: "plan-executor"},
		},
	}}})
	if !strings.Contains(out, "foreign") {
		t.Errorf("missing foreign line:\n%s", out)
	}
	if !strings.Contains(out, "w9:p9") || !strings.Contains(out, "plan-executor") {
		t.Errorf("foreign line missing pane or title:\n%s", out)
	}
}

func TestRenderStatusOmitsForeignLineWhenNone(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
	}}})
	if strings.Contains(out, "foreign") {
		t.Errorf("unexpected foreign line:\n%s", out)
	}
}

func TestRenderStatusForeignShowsRelativeCWDWhenNested(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p9", Kind: "opencode", Status: "working", CWD: "/repo/internal/ui", Title: "researcher"},
		},
	}}})
	if !strings.Contains(out, "internal/ui") {
		t.Errorf("nested foreign agent must show its location:\n%s", out)
	}
	if strings.Contains(out, "/repo/internal/ui") {
		t.Errorf("location must be relative to the binding cwd, not absolute:\n%s", out)
	}
}

func TestRenderStatusForeignOmitsCWDAtTreeRoot(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p9", Kind: "claude", Status: "idle", CWD: "/repo", Title: "plan-executor"},
		},
	}}})
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "foreign") {
			continue
		}
		if strings.Contains(line, "/repo") {
			t.Errorf("an agent at the tree root must not repeat the cwd: %q", line)
		}
	}
}

func TestRenderStatusShowsEveryForeignAgent(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p1", Kind: "claude", Status: "idle", CWD: "/repo"},
			{PaneID: "w9:p2", Kind: "agy", Status: "working", CWD: "/repo"},
		},
	}}})
	if n := strings.Count(out, "foreign"); n != 2 {
		t.Errorf("got %d foreign lines, want 2:\n%s", n, out)
	}
}

func TestStatusCountsOnlyRunningConsults(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Consults = []store.Consult{
		{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultRunning},
		{ID: "bbbbbbbb", Role: "reviewer", State: store.ConsultRunning},
		{ID: "cccccccc", Role: "reviewer", State: store.ConsultDone},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings", len(rep.Bindings))
	}
	if rep.Bindings[0].Consults != 2 {
		t.Errorf("Consults = %d, want 2 running (the done one awaits reap)", rep.Bindings[0].Consults)
	}
}

func TestRenderStatusOmitsTheConsultCountWhenZero(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Round: 3, Consults: 0,
	}}}

	if out := RenderStatus(r); strings.Contains(out, "+0c") {
		t.Errorf("rendered a zero consult count:\n%s", out)
	}
}

func TestHideDoneRemovesOnlyDoneRows(t *testing.T) {
	in := Report{
		Bindings: []BindingStatus{
			{Name: "first", State: string(store.StateActive)},
			{Name: "second", State: string(store.StateDone)},
			{Name: "third", State: string(store.StateBroken)},
		},
	}

	got := HideDone(in)

	if len(in.Bindings) != 3 {
		t.Fatalf("HideDone modified input report: len = %d, want 3", len(in.Bindings))
	}
	if got.DoneHidden != 1 {
		t.Errorf("DoneHidden = %d, want 1", got.DoneHidden)
	}
	if len(got.Bindings) != 2 {
		t.Fatalf("got %d bindings, want 2", len(got.Bindings))
	}
	if got.Bindings[0].Name != "first" || got.Bindings[1].Name != "third" {
		t.Errorf("bindings = %+v, want first and third in original order", got.Bindings)
	}
}

func TestRenderStatusFooterCountsHidden(t *testing.T) {
	cases := []struct {
		hidden    int
		wantSub   string
		wantNoSub string
	}{
		{hidden: 3, wantSub: "3 done · relay gc to clear"},
		{hidden: 1, wantSub: "1 done · relay gc to clear"},
		{hidden: 0, wantNoSub: "done ·"},
	}

	for _, tc := range cases {
		r := Report{
			Bindings: []BindingStatus{
				{Name: "live", CWD: "/repo", Workspace: "w1", Round: 1, Display: "ACTIVE"},
			},
			DoneHidden: tc.hidden,
		}
		out := RenderStatus(r)
		if tc.wantSub != "" && !strings.Contains(out, tc.wantSub) {
			t.Errorf("hidden=%d: RenderStatus output missing %q:\n%s", tc.hidden, tc.wantSub, out)
		}
		if tc.wantNoSub != "" && strings.Contains(out, tc.wantNoSub) {
			t.Errorf("hidden=%d: RenderStatus output should not contain %q:\n%s", tc.hidden, tc.wantNoSub, out)
		}
	}
}

func TestRenderStatusFooterOnlyWhenEverythingIsDone(t *testing.T) {
	r := Report{
		Bindings:   nil,
		DoneHidden: 2,
	}
	out := RenderStatus(r)
	if !strings.Contains(out, "2 done · relay gc to clear") {
		t.Errorf("RenderStatus output missing footer:\n%s", out)
	}
	if strings.Contains(out, "no bindings") {
		t.Errorf("RenderStatus output should not contain 'no bindings':\n%s", out)
	}
}
