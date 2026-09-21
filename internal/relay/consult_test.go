package relay

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// consultAgent is the live pane a seeded consult occupies.
func consultAgent(status string) herdr.Agent {
	return herdr.Agent{
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
func seedConsult(t *testing.T, f *fakeHerdr) (Runtime, *fakeClock, store.Consult) {
	t.Helper()
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "review it")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	f.agents = append(f.agents, consultAgent(herdr.StatusWorking))
	clock := &fakeClock{now: baseTime}
	return withClock(rt, clock), clock, res.Consult
}

// tickConsults runs one reconcile pass over the consults only.
func tickConsults(t *testing.T, rt Runtime, f *fakeHerdr) store.Binding {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		out, err = reconcileConsults(context.Background(), rt, tx, b, f.agents)
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

func setConsultAgentStatus(f *fakeHerdr, status string) {
	for i := range f.agents {
		if f.agents[i].PaneID == "w2:p9" {
			f.agents[i].Status = status
		}
	}
}

func TestConsultWithFindingsAndIdleIsDelivered(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, c := seedConsult(t, f)
	writeFindings(t, c)
	setConsultAgentStatus(f, herdr.StatusIdle)

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultDone {
		t.Fatalf("state = %q, want done", b.Consults[0].State)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("nothing queued for the planner: found=%v err=%v", found, err)
	}
	if pending.Kind != store.KindFindings || pending.Direction != store.DirToPlanner {
		t.Errorf("entry = %s/%s, want to_planner/findings", pending.Direction, pending.Kind)
	}
	if !strings.Contains(pending.Payload, c.FindingsPath) {
		t.Errorf("payload does not name the findings path: %q", pending.Payload)
	}
	if pending.Path != c.FindingsPath {
		t.Errorf("Path = %q, want the findings path", pending.Path)
	}
}

func TestConsultIdleWithoutFindingsIsNudgedOnce(t *testing.T) {
	f := &fakeHerdr{}
	rt, clock, _ := seedConsult(t, f)
	setConsultAgentStatus(f, herdr.StatusIdle)
	before := len(f.prompts)

	b := tickConsults(t, rt, f)
	if b.Consults[0].NudgedAt.IsZero() {
		t.Fatal("NudgedAt not stamped")
	}
	if len(f.prompts) != before+1 {
		t.Fatalf("got %d prompts, want 1 nudge", len(f.prompts)-before)
	}
	// Assert the TARGET. Counting prompts cannot tell a nudge sent to the
	// consult from one typed into the planner's pane, which would land in the
	// human's conversation instead -- the anti-clobber failure again.
	if got := f.prompts[len(f.prompts)-1].Target; got != "w2:p9" {
		t.Errorf("nudge went to %q, want the consult's pane w2:p9", got)
	}

	// A second tick inside the grace window must not nudge again.
	clock.Advance(consultGrace / 2)
	tickConsults(t, rt, f)
	if len(f.prompts) != before+1 {
		t.Errorf("nudged %d times; a consult gets exactly one", len(f.prompts)-before)
	}
}

func TestConsultGoesSilentAfterTheGraceWindow(t *testing.T) {
	f := &fakeHerdr{}
	rt, clock, _ := seedConsult(t, f)
	setConsultAgentStatus(f, herdr.StatusIdle)

	tickConsults(t, rt, f) // nudge
	clock.Advance(consultGrace + 1)
	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("silence was not reported: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, "wrote no findings") {
		t.Errorf("payload = %q", pending.Payload)
	}
	if pending.Path != "" {
		t.Errorf("Path = %q; a silent consult wrote no file to point at", pending.Path)
	}
}

func TestBlockedConsultIsReportedNotNegotiatedWith(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, _ := seedConsult(t, f)
	setConsultAgentStatus(f, herdr.StatusBlocked)
	before := len(f.prompts)

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	if len(f.prompts) != before {
		t.Error("relay prompted a blocked consult; a consult is one-shot and is not answered")
	}
	pending, _, _ := rt.Store.PendingForPlanner("webshop")
	if !strings.Contains(pending.Payload, "open until the next `relay reap`") {
		t.Errorf("payload must tell the human the pane is open, and for how long: %q", pending.Payload)
	}
}

func TestConsultPaneGoneWithFindingsStillDelivers(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, c := seedConsult(t, f)
	writeFindings(t, c)
	// The pane wrote its findings and then died.
	f.agents = f.agents[:len(f.agents)-1]

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultDone {
		t.Fatalf("state = %q, want done: the findings file is the record, not the pane", b.Consults[0].State)
	}
}

func TestConsultPaneGoneWithoutFindingsIsSilent(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, _ := seedConsult(t, f)
	f.agents = f.agents[:len(f.agents)-1]

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	if !strings.Contains(b.Consults[0].Note, "gone") {
		t.Errorf("note = %q", b.Consults[0].Note)
	}
}

func TestWorkingConsultTimesOut(t *testing.T) {
	f := &fakeHerdr{}
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

// This is the mutation-test target named in the spec: delete the
// `State != ConsultRunning` guard at the top of reconcileConsults and this
// fails. Without the guard every tick re-queues findings already delivered.
func TestTerminalConsultsAreNeverRevisited(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, c := seedConsult(t, f)
	writeFindings(t, c)
	setConsultAgentStatus(f, herdr.StatusIdle)

	tickConsults(t, rt, f) // -> done, one findings entry

	countFindings := func() int {
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatalf("ReadLog: %v", err)
		}
		n := 0
		for _, e := range entries {
			if e.Kind == store.KindFindings {
				n++
			}
		}
		return n
	}
	if countFindings() != 1 {
		t.Fatalf("got %d findings entries after one tick, want 1", countFindings())
	}

	promptsBefore := len(f.prompts)
	for i := 0; i < 5; i++ {
		tickConsults(t, rt, f)
	}

	if got := countFindings(); got != 1 {
		t.Errorf("got %d findings entries after 6 ticks, want 1: a terminal consult must never be re-queued", got)
	}
	if len(f.prompts) != promptsBefore {
		t.Errorf("a terminal consult was prompted %d times", len(f.prompts)-promptsBefore)
	}
}

func TestReconcileConsultsMakesNoHerdrCallsForTerminalRecords(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, c := seedConsult(t, f)
	writeFindings(t, c)
	setConsultAgentStatus(f, herdr.StatusIdle)
	tickConsults(t, rt, f)

	listsBefore := f.listCalls
	tickConsults(t, rt, f)

	if f.listCalls != listsBefore {
		t.Errorf("reconcileConsults made %d ListAgents calls; it reads the snapshot it is given", f.listCalls-listsBefore)
	}
}

func seedSpawning(t *testing.T, f *fakeHerdr) (Runtime, *fakeClock) {
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
	f := &fakeHerdr{}
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

func TestReconcileExpiresAStaleReservation(t *testing.T) {
	f := &fakeHerdr{}
	rt, clock := seedSpawning(t, f)
	clock.Advance(consultSpawnTimeout + time.Second)

	b := tickConsults(t, rt, f)
	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	if !strings.Contains(b.Consults[0].Note, "spawn did not complete") {
		t.Errorf("note = %q, want it to mention 'spawn did not complete'", b.Consults[0].Note)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var findings []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindFindings {
			findings = append(findings, e)
		}
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings entries, want 1", len(findings))
	}
	if strings.Contains(findings[0].Payload, "Pane ") {
		t.Errorf("payload %q names a pane with 'Pane '", findings[0].Payload)
	}
	rest := strings.ReplaceAll(findings[0].Payload, "No pane was spawned.", "")
	if strings.Contains(rest, "pane ") {
		t.Errorf("payload rest %q contains 'pane '", rest)
	}
	if !strings.HasSuffix(findings[0].Payload, "No pane was spawned.") {
		t.Errorf("payload %q does not end with 'No pane was spawned.'", findings[0].Payload)
	}
}

// seedHeadlessConsult asks for a consult as a process on the webshop binding
// and returns the runtime and the record relay made.
func seedHeadlessConsult(t *testing.T, f *fakeHerdr, fr *fakeRunner) (Runtime, store.Consult) {
	t.Helper()
	rt, _ := seedForAsk(t, f)
	rt.Runner = fr
	q := writeQuestion(t, "review it")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", Headless: true, File: q, Name: "webshop", PlannerPane: "w2:p3",
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
	f := &fakeHerdr{}
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
	f := &fakeHerdr{}
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
	f := &fakeHerdr{}
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
	f := &fakeHerdr{}
	rt, _, _ := seedConsult(t, f)

	for i := range f.agents {
		if f.agents[i].Name == "webshop-reviewer-7f2a3c1d" {
			f.agents[i].Session = herdr.Session{Value: "consult-parent"}
		}
	}

	// Tick once to record the session.
	tickConsults(t, rt, f)

	// Swap in Session.Value: "child" + StatusIdle, no findings.
	for i := range f.agents {
		if f.agents[i].Name == "webshop-reviewer-7f2a3c1d" {
			f.agents[i].Session = herdr.Session{Value: "child"}
			f.agents[i].Status = herdr.StatusIdle
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
