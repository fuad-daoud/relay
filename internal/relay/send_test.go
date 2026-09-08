package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func seedBound(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []herdr.Agent{plannerAgent()}
	f.newPane = "w2:p4"
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Alias: "abuilder", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return rt, b
}

func writePlan(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

func TestSendCopiesPlanAndPromptsBuilder(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	src := writePlan(t, "# do the thing")

	round, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if round != 1 {
		t.Fatalf("round = %d, want 1", round)
	}

	copied, err := os.ReadFile(rt.Store.PlanPath("webshop", 1))
	if err != nil {
		t.Fatalf("plan not copied into state: %v", err)
	}
	if string(copied) != "# do the thing" {
		t.Errorf("copied plan = %q", copied)
	}

	if len(f.prompts) != 1 {
		t.Fatalf("got %d prompts, want 1", len(f.prompts))
	}
	text := f.prompts[0].Text
	if !strings.Contains(text, rt.Store.PlanPath("webshop", 1)) {
		t.Error("prompt must name the plan path")
	}
	if !strings.Contains(text, rt.Store.ReportPath("webshop", 1)) {
		t.Error("prompt must name the report path")
	}
	if f.prompts[0].Target != "webshop-builder" {
		t.Errorf("target = %q, want the herdr agent name", f.prompts[0].Target)
	}
}

func TestSendIncludesPreambleOnFirstRoundOnly(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f) // abuilder carries a preamble
	src := writePlan(t, "x")

	if _, err := Send(context.Background(), rt, "webshop", src); err != nil {
		t.Fatalf("round 1 Send: %v", err)
	}
	if !strings.Contains(f.prompts[0].Text, "plan-executor") {
		t.Error("round 1 prompt must carry the abuilder preamble")
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Send(context.Background(), rt, "webshop", src); err != nil {
		t.Fatalf("round 2 Send: %v", err)
	}
	if strings.Contains(f.prompts[1].Text, "plan-executor") {
		t.Error("the preamble must not repeat after round 1")
	}
}

func TestSendIncludesPreambleWhenPending(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	src := writePlan(t, "x")

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Round = 5
	b.PreamblePending = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Send(context.Background(), rt, "webshop", src); err != nil {
		t.Fatalf("round 5 Send: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("got %d prompts, want 1", len(f.prompts))
	}
	if !strings.Contains(f.prompts[0].Text, "plan-executor") {
		t.Error("round 5 prompt with PreamblePending must carry the preamble")
	}

	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.PreamblePending {
		t.Error("PreamblePending must be cleared after successful Send")
	}

	if _, err := Send(context.Background(), rt, "webshop", src); err != nil {
		t.Fatalf("subsequent Send: %v", err)
	}
	if len(f.prompts) != 2 {
		t.Fatalf("got %d prompts, want 2", len(f.prompts))
	}
	if strings.Contains(f.prompts[1].Text, "plan-executor") {
		t.Error("round 5 prompt without PreamblePending must not carry the preamble")
	}
}

func TestSendFailedPromptLeavesPreamblePending(t *testing.T) {
	f := &fakeHerdr{promptErr: errors.New("builder crashed")}
	rt, _ := seedBound(t, f)
	src := writePlan(t, "x")

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Round = 5
	b.PreamblePending = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Send(context.Background(), rt, "webshop", src); err == nil {
		t.Fatal("Send must fail when builder prompt fails")
	}

	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !b.PreamblePending {
		t.Error("PreamblePending must remain set if prompt failed")
	}
}

func TestSendLogsThePlan(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != store.KindPlan || entries[0].Direction != store.DirToBuilder {
		t.Fatalf("log = %+v", entries)
	}
	if !entries[0].Confirmed {
		t.Error("an outbound plan is confirmed the moment herdr accepts it")
	}
}

func TestSendRetriesOnceOnStall(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	f.stalls = 1
	f.prompts = nil

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x")); err != nil {
		t.Fatalf("Send must retry once past a stall: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("got %d accepted prompts, want 1", len(f.prompts))
	}
}

func TestSendGivesUpAfterTwoStalls(t *testing.T) {
	f := &fakeHerdr{stalls: 2}
	rt, _ := seedBound(t, f)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x")); err == nil {
		t.Fatal("two stalls must fail rather than fire a third time")
	}
}

func TestSendSurfacesBlockedBuilder(t *testing.T) {
	f := &fakeHerdr{promptErr: herdr.ErrAgentBlocked}
	rt, _ := seedBound(t, f)

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"))
	if !errors.Is(err, ErrBuilderBlocked) {
		t.Fatalf("got %v, want ErrBuilderBlocked", err)
	}
}

// TestPromptRetryReportsANonStallFailureAsItself keeps the second failure
// honest: the retry can fail for an unrelated reason -- the builder became
// blocked between the two attempts, say -- and calling that a stall sends the
// human looking at the wrong thing.
func TestPromptRetryReportsANonStallFailureAsItself(t *testing.T) {
	f := &fakeHerdr{stalls: 1, promptErr: herdr.ErrAgentBlocked}
	rt, _ := seedBound(t, f)

	err := promptWithRetry(context.Background(), rt, "webshop-builder", "text")
	if err == nil {
		t.Fatal("a failing retry must surface an error")
	}
	if !errors.Is(err, herdr.ErrAgentBlocked) {
		t.Errorf("the real cause must survive the wrap, got %v", err)
	}
	if strings.Contains(err.Error(), "stalled twice") {
		t.Errorf("a non-stall retry failure must not be reported as a stall: %v", err)
	}
	if !strings.Contains(err.Error(), "failed on retry") {
		t.Errorf("error = %v, want it to name the retry", err)
	}
}

// TestPromptRetryReportsASecondStallAsAStall is the other branch: two genuine
// stalls stay labelled as such, and relay never fires a third time.
func TestPromptRetryReportsASecondStallAsAStall(t *testing.T) {
	f := &fakeHerdr{stalls: 2}
	rt, _ := seedBound(t, f)

	err := promptWithRetry(context.Background(), rt, "webshop-builder", "text")
	if err == nil || !strings.Contains(err.Error(), "stalled twice") {
		t.Fatalf("err = %v, want a stalled-twice error", err)
	}
	if len(f.prompts) != 0 {
		t.Errorf("neither attempt was accepted, so nothing may be recorded: %+v", f.prompts)
	}
}

func TestSendCapturesBaselineWithFakeGit(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	fg := &fakeGit{snapshotTreeID: "tree-abc123"}
	rt.Git = fg

	src := writePlan(t, "# test plan")
	round, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if round != 1 {
		t.Fatalf("round = %d, want 1", round)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree != "tree-abc123" {
		t.Errorf("RoundBaselineTree = %q, want tree-abc123", b.RoundBaselineTree)
	}
	if fg.snapshotCalls != 1 {
		t.Errorf("snapshotCalls = %d, want 1", fg.snapshotCalls)
	}
}

func TestSendBaselineFailureTolerated(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	fg := &fakeGit{snapshotTreeErr: errors.New("git broken")}
	rt.Git = fg

	src := writePlan(t, "# test plan")
	round, err := Send(context.Background(), rt, "webshop", src)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if round != 1 {
		t.Fatalf("round = %d, want 1", round)
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree != "" {
		t.Errorf("RoundBaselineTree = %q, want empty", b.RoundBaselineTree)
	}

	// Verify plan was still copied and logged
	copied, err := os.ReadFile(rt.Store.PlanPath("webshop", 1))
	if err != nil || string(copied) != "# test plan" {
		t.Fatalf("plan file error: %v, content: %q", err, string(copied))
	}
	log, err := rt.Store.ReadLog("webshop")
	if err != nil || len(log) == 0 || log[len(log)-1].Kind != store.KindPlan {
		t.Fatalf("expected plan log entry, got %v, err: %v", log, err)
	}
}
