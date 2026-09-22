package relay

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// verifyConsultID is the deterministic id the verify tests mint.
const verifyConsultID = "7f2a3c1d"

// claudeStream renders a claude harness stream whose last assistant text is
// text: the shape transcript.FinalText reads (#147).
func claudeStream(t *testing.T, text string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	if err != nil {
		t.Fatalf("marshal stream: %v", err)
	}
	return append(line, '\n')
}

// startVerifyRound puts webshop one reconcile past a verify round's close:
// the round closed, the reviewer was started in its throwaway worktree, and
// the process is scripted to have exited 0. The caller writes the stream the
// reviewer left behind, then ticks the consult.
func startVerifyRound(t *testing.T, f *fakePanes) (Runtime, *fakeRunner, *fakeGit, store.Binding) {
	t.Helper()
	rt, b := sentBinding(t, f)
	fr := newFakeRunner()
	fg := &fakeGit{headCommitID: "head1"}
	rt.Runner = fr
	rt.Git = fg
	rt.NewID = func() string { return verifyConsultID }

	b.RoundVerify = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	agents := []stubAgent{plannerWith(stubIdle, false), builderAgent(stubWorking)}
	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.handles) != 1 {
		t.Fatalf("verify consult processes = %d, want 1", len(fr.handles))
	}
	return rt, fr, fg, got
}

// leaveVerifyStream writes what the reviewer's process left on its stream and
// scripts it as exited with code 0.
func leaveVerifyStream(t *testing.T, rt Runtime, fr *fakeRunner, text string) {
	t.Helper()
	streamPath := rt.Store.ConsultStreamPath("webshop", 1, verifyConsultID)
	if err := os.WriteFile(streamPath, claudeStream(t, text), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	pid := fr.handles[0].PID
	fr.script(pid, false)
	fr.exit(pid, 0)
}

// consultAgent is the live pane a seeded consult occupies.
func consultAgent(status string) stubAgent {
	return stubAgent{
		Name:   "webshop-reviewer-7f2a3c1d",
		Kind:   "claude",
		Status: status,
		CWD:    "/repo",
		PaneID: "w2:p9",
		Title:  "webshop-reviewer-7f2a3c1d",
	}
}

// seedConsult puts one running consult on a bound binding and returns the
// runtime, a movable clock, and the consult record.
func seedConsult(t *testing.T, f *fakePanes) (Runtime, *fakeClock, store.Consult) {
	t.Helper()
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "review it")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	f.agents = append(f.agents, consultAgent(stubWorking))
	clock := &fakeClock{now: baseTime}
	return withClock(rt, clock), clock, res.Consult
}

// tickConsults runs one reconcile pass over the consults only.
func tickConsults(t *testing.T, rt Runtime, f *fakePanes) store.Binding {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		out, err = reconcileConsults(context.Background(), rt, tx, b)
		if err != nil {
			return err
		}
		return tx.Save(out)
	})
	if err != nil {
		t.Fatalf("reconcileConsults: %v", err)
	}
	return out
}

func writeFindings(t *testing.T, c store.Consult) {
	t.Helper()
	if err := os.WriteFile(c.FindingsPath, []byte("looks fine"), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}
}

func setConsultAgentStatus(f *fakePanes, status string) {
	for i := range f.agents {
		if f.agents[i].PaneID == "w2:p9" {
			f.agents[i].Status = status
		}
	}
}

func TestWorkingConsultTimesOut(t *testing.T) {
	f := &fakePanes{}
	rt, clock, _ := seedConsult(t, f)
	// Status stays `working`, so the nudge path never runs.

	if b := tickConsults(t, rt, f); b.Consults[0].State != store.ConsultRunning {
		t.Fatalf("gave up early: %q", b.Consults[0].State)
	}

	clock.Advance(consultTimeout + 1)
	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent after the timeout", b.Consults[0].State)
	}
}

func TestReconcileConsultsMakesNoHerdrCallsForTerminalRecords(t *testing.T) {
	f := &fakePanes{}
	rt, _, c := seedConsult(t, f)
	writeFindings(t, c)
	setConsultAgentStatus(f, stubIdle)
	tickConsults(t, rt, f)

	listsBefore := f.listCalls
	tickConsults(t, rt, f)

	if f.listCalls != listsBefore {
		t.Errorf("reconcileConsults made %d ListAgents calls; it reads the snapshot it is given", f.listCalls-listsBefore)
	}
}

func seedSpawning(t *testing.T, f *fakePanes) (Runtime, *fakeClock) {
	t.Helper()
	rt, _ := seedBound(t, f)
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	clock := &fakeClock{now: baseTime}
	b.Consults = []store.Consult{
		{
			ID:           "7f2a3c1d",
			Role:         "reviewer",
			Round:        1,
			AskPath:      "/repo/.relay/consults/7f2a3c1d-ask.md",
			FindingsPath: "/repo/.relay/consults/7f2a3c1d-findings.md",
			Endpoint:     store.Endpoint{AgentName: "reviewer", Kind: "claude"},
			State:        store.ConsultSpawning,
			SpawnedAt:    baseTime,
		},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return withClock(rt, clock), clock
}

func TestReconcileSkipsAFreshReservation(t *testing.T) {
	f := &fakePanes{}
	rt, _ := seedSpawning(t, f)

	b := tickConsults(t, rt, f)
	if b.Consults[0].State != store.ConsultSpawning {
		t.Fatalf("state = %q, want spawning", b.Consults[0].State)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	// seedSpawning's underlying seedBound already wrote the builder bind's
	// pick entry; the reservation itself queues nothing further.
	if len(entries) != 1 || entries[0].Kind != store.KindPick {
		t.Errorf("got %d log entries queued, want the single pick entry from the bind: %+v", len(entries), entries)
	}
	if len(f.prompts) != 0 {
		t.Errorf("len(f.prompts) = %d, want 0", len(f.prompts))
	}
}

// seedHeadlessConsult asks for a consult as a process on the webshop binding
// and returns the runtime and the record relay made.
func seedHeadlessConsult(t *testing.T, f *fakePanes, fr *fakeRunner) (Runtime, store.Consult) {
	t.Helper()
	rt, _ := seedForAsk(t, f)
	rt.Runner = fr
	q := writeQuestion(t, "review it")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Ask --headless: %v", err)
	}
	return rt, res.Consult
}

// TestHeadlessConsultFinalMessageBecomesFindings: the process's last
// assistant message is the findings. Deleting the WriteFile leaves the
// consult running and this fails.
func TestHeadlessConsultFinalMessageBecomesFindings(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, c := seedHeadlessConsult(t, f, fr)

	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"FINDINGS BODY"}]}}` + "\n" +
		"relay-exit:0\n"
	if err := os.WriteFile(c.Endpoint.LogPath, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	fr.script(c.Endpoint.PID, false)
	fr.exit(c.Endpoint.PID, 0)

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultDone {
		t.Fatalf("state = %q, want done", b.Consults[0].State)
	}
	body, err := os.ReadFile(c.FindingsPath)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	if !strings.Contains(string(body), "FINDINGS BODY") {
		t.Errorf("findings = %q, want it to contain the final message", body)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("findings were not queued: found=%v err=%v", found, err)
	}
	if pending.Kind != store.KindFindings || pending.Path != c.FindingsPath {
		t.Errorf("entry = %s path=%q, want findings at %q", pending.Kind, pending.Path, c.FindingsPath)
	}
}

// TestHeadlessConsultExitWithoutTextIsSilent: a process that died without a
// final message is reported silent with its exit code and where to look.
func TestHeadlessConsultExitWithoutTextIsSilent(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, c := seedHeadlessConsult(t, f, fr)

	if err := os.WriteFile(c.Endpoint.LogPath, []byte("relay-exit:1\n"), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	fr.script(c.Endpoint.PID, false)
	fr.exit(c.Endpoint.PID, 1)

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	note := b.Consults[0].Note
	if !strings.Contains(note, "code 1") {
		t.Errorf("note = %q, want it to name the exit code", note)
	}
	if !strings.Contains(note, c.Endpoint.LogPath) {
		t.Errorf("note = %q, want it to point at the stream %s", note, c.Endpoint.LogPath)
	}
	if _, err := os.Stat(c.FindingsPath); err == nil {
		t.Error("a silent consult must write no findings file")
	}
}

// TestHeadlessConsultTimesOut: a process still alive past consultTimeout is
// killed and reported silent.
func TestHeadlessConsultTimesOut(t *testing.T) {
	f := &fakePanes{}
	fr := newFakeRunner()
	rt, c := seedHeadlessConsult(t, f, fr)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)

	clock.Advance(consultTimeout + time.Second)
	b := tickConsults(t, rt, f)

	if len(fr.kills) != 1 {
		t.Fatalf("kills = %d, want 1", len(fr.kills))
	}
	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	if !strings.Contains(b.Consults[0].Note, "timed out") {
		t.Errorf("note = %q, want it to say timed out", b.Consults[0].Note)
	}
	if c.Endpoint.PID == 0 {
		t.Error("seeded headless consult has no pid")
	}
}

func TestReconcileIgnoresAConsultSubAgentsIdle(t *testing.T) {
	f := &fakePanes{}
	rt, _, _ := seedConsult(t, f)

	for i := range f.agents {
		if f.agents[i].Name == "webshop-reviewer-7f2a3c1d" {
			f.agents[i].Session = stubSession{Value: "consult-parent"}
		}
	}

	// Tick once to record the session.
	tickConsults(t, rt, f)

	// Swap in Session.Value: "child" + StatusIdle, no findings.
	for i := range f.agents {
		if f.agents[i].Name == "webshop-reviewer-7f2a3c1d" {
			f.agents[i].Session = stubSession{Value: "child"}
			f.agents[i].Status = stubIdle
		}
	}
	f.prompts = nil

	b := tickConsults(t, rt, f)

	if len(f.prompts) != 0 {
		t.Errorf("expected no nudge prompt under sub-agent idle, got %+v", f.prompts)
	}
	if len(b.Consults) != 1 {
		t.Fatalf("expected 1 consult, got %d", len(b.Consults))
	}
	c := b.Consults[0]
	if c.State != store.ConsultRunning {
		t.Errorf("consult state = %v, want %v", c.State, store.ConsultRunning)
	}
	if !c.NudgedAt.IsZero() {
		t.Errorf("NudgedAt = %v, want zero", c.NudgedAt)
	}
}

// TestVerifyVerdictParsedOntoFindingsAndBinding pins #144's verdict path: a
// verify consult whose findings end with `verdict: rejected` records the
// verdict and its two reasons on the findings entry and on the binding, names
// them in the payload, removes the throwaway worktree, and shows in `relay
// status` until the round after next.
func TestVerifyVerdictParsedOntoFindingsAndBinding(t *testing.T) {
	f := &fakePanes{}
	rt, fr, fg, _ := startVerifyRound(t, f)
	leaveVerifyStream(t, rt, fr, "checked it\n\n```relay\nverdict: rejected\nreasons: [\"a\",\"b\"]\n```\n")

	b := tickConsults(t, rt, f)

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	var findings *store.LogEntry
	for i := range entries {
		if entries[i].Kind == store.KindFindings {
			findings = &entries[i]
		}
	}
	if findings == nil {
		t.Fatalf("no findings entry queued: %+v", entries)
	}
	if findings.Verdict != "rejected" {
		t.Errorf("findings verdict = %q, want rejected", findings.Verdict)
	}
	if len(findings.Reasons) != 2 || findings.Reasons[0] != "a" || findings.Reasons[1] != "b" {
		t.Errorf("findings reasons = %v, want [a b]", findings.Reasons)
	}
	if !strings.Contains(findings.Payload, "verdict rejected · 2 reasons") {
		t.Errorf("payload = %q, want it to carry the verdict line", findings.Payload)
	}

	if b.LastVerdict == nil {
		t.Fatal("LastVerdict is nil after a verdict")
	}
	if b.LastVerdict.Round != 1 || b.LastVerdict.Verdict != "rejected" {
		t.Errorf("LastVerdict = %+v, want round 1 rejected", b.LastVerdict)
	}

	wantWT := rt.Store.VerifyWorktreePath("webshop", 1)
	removed := false
	for _, c := range fg.removeWorktreeCalls {
		if c.Path == wantWT && c.Force {
			removed = true
		}
	}
	if !removed {
		t.Errorf("verify worktree %s not removed with force: %+v", wantWT, fg.removeWorktreeCalls)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep.Bindings[0].Verdict; got != "verdict: rejected (2 reasons)" {
		t.Errorf("status verdict = %q, want \"verdict: rejected (2 reasons)\"", got)
	}

	// The next round closes without a verdict of its own, and the stale one
	// stops being shown: it judged round 1, and b.Round-1 is now 2.
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it again"), SendOptions{}); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 2), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 2))
	agents := []stubAgent{plannerWith(stubIdle, false), builderAgent(stubWorking)}
	closed, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile round 2: %v", err)
	}
	// The daemon persists what Reconcile returned; status reads the store.
	if err := rt.Store.Save(closed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if closed.Round != 3 {
		t.Fatalf("round = %d, want 3 after the second close", closed.Round)
	}

	rep, err = Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep.Bindings[0].Verdict; got != "" {
		t.Errorf("status verdict = %q after the next round, want it dropped", got)
	}
}

// TestVerifyUnstructuredWhenNoBlock pins #144's prose case: findings without
// a readable block are delivered as unstructured rather than guessed at.
func TestVerifyUnstructuredWhenNoBlock(t *testing.T) {
	f := &fakePanes{}
	rt, fr, _, _ := startVerifyRound(t, f)
	leaveVerifyStream(t, rt, fr, "I read it; it looks fine to me, no block here.\n")

	b := tickConsults(t, rt, f)

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	var findings *store.LogEntry
	for i := range entries {
		if entries[i].Kind == store.KindFindings {
			findings = &entries[i]
		}
	}
	if findings == nil {
		t.Fatalf("no findings entry queued: %+v", entries)
	}
	if findings.Verdict != "unstructured" {
		t.Errorf("findings verdict = %q, want unstructured", findings.Verdict)
	}
	if len(findings.Reasons) != 0 {
		t.Errorf("findings reasons = %v, want none", findings.Reasons)
	}
	if findings.Path == "" {
		t.Errorf("unstructured findings must still name the findings file")
	}

	if b.LastVerdict == nil || b.LastVerdict.Verdict != "unstructured" {
		t.Errorf("LastVerdict = %+v, want unstructured", b.LastVerdict)
	}
}
