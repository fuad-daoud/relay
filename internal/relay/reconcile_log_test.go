package relay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// captureLog routes slog's default logger into a buffer for one test. The
// daemon logs through the default logger, so this is the only seam.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// settleTick is one daemon tick's deliverAndSettle over a binding, under the
// state lock the daemon would hold, returning the binding to persist.
func settleTick(t *testing.T, rt Runtime, b store.Binding, f *fakeHerdr) store.Binding {
	t.Helper()
	var next store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		next, err = deliverAndSettle(context.Background(), rt, tx, b, f.agents)
		return err
	})
	if err != nil {
		t.Fatalf("deliverAndSettle: %v", err)
	}
	return next
}

func TestSettleLogsHeldOnceAndDeliveredWithReason(t *testing.T) {
	buf := captureLog(t)
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
	f.readOut = claudeDraftScreen
	f.prompts = nil
	now, rt := heldClock(rt)

	next := settleTick(t, rt, b, f)
	if next.State != store.StateHeld {
		t.Fatalf("first tick: State = %q, want held", next.State)
	}
	for i := 0; i < 2; i++ {
		*now = now.Add(10 * time.Second)
		next = settleTick(t, rt, next, f)
		if next.State != store.StateHeld {
			t.Fatalf("tick %d: State = %q, want held", i+2, next.State)
		}
	}

	if n := strings.Count(buf.String(), `msg="payload held"`); n != 1 {
		t.Fatalf("payload held logged %d times across three held ticks, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "binding=webshop") || !strings.Contains(buf.String(), "round=1") {
		t.Errorf("held line must name the binding and round:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), `msg="payload delivered"`) {
		t.Fatalf("nothing was delivered yet:\n%s", buf.String())
	}

	*now = now.Add(DefaultHeldGrace)
	next = settleTick(t, rt, next, f)
	if next.State != store.StateActive {
		t.Fatalf("after grace: State = %q, want active", next.State)
	}
	if n := strings.Count(buf.String(), `msg="payload delivered"`); n != 1 {
		t.Fatalf("payload delivered logged %d times, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), `reason="planner focused, quiet for 1m0s"`) {
		t.Errorf("delivered line must carry the grace reason:\n%s", buf.String())
	}
}

func TestSettleStaysSilentOnTheUnfocusedPath(t *testing.T) {
	buf := captureLog(t)
	f := &fakeHerdr{}
	rt, b := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false)}
	f.prompts = nil

	settleTick(t, rt, b, f)
	if len(f.prompts) != 1 {
		t.Fatalf("prompts = %+v, want one", f.prompts)
	}
	if strings.Contains(buf.String(), "payload") {
		t.Errorf("the unfocused delivery is the ordinary path and must not log:\n%s", buf.String())
	}
}

// nudgedBinding drives one binding through the nudge: plan sent, builder
// idle past startGrace, no report on disk. It returns the binding after the
// nudge tick and the clock the caller advances between later ticks.
func nudgedBinding(t *testing.T, f *fakeHerdr) (Runtime, store.Binding, *fakeClock, []herdr.Agent) {
	t.Helper()
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("nudge Reconcile: %v", err)
	}
	return rt, b, clock, agents
}

func TestReconcileLogsNudgeOnceAndScrapeOnce(t *testing.T) {
	buf := captureLog(t)
	f := &fakeHerdr{readOut: "half a screen of output"}
	rt, b, clock, agents := nudgedBinding(t, f)

	if n := strings.Count(buf.String(), `msg="builder nudged"`); n != 1 {
		t.Fatalf("builder nudged logged %d times after the nudge tick, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "binding=webshop") || !strings.Contains(buf.String(), "round=1") {
		t.Errorf("nudge line must name the binding and round:\n%s", buf.String())
	}

	// Three ticks inside the grace: the quiescence clock runs, nothing is
	// decided, nothing is logged.
	before := buf.Len()
	for i := 0; i < 3; i++ {
		clock.Advance(10 * time.Second)
		var err error
		if b, err = reconcile(t, rt, b, agents); err != nil {
			t.Fatalf("tick %d: %v", i+2, err)
		}
	}
	if buf.Len() != before {
		t.Fatalf("ticks inside the grace must not log:\n%s", buf.String()[before:])
	}

	clock.Advance(nudgeGrace)
	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("scrape Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2 after the scrape", got.Round)
	}
	if n := strings.Count(buf.String(), `msg="builder quiescent, scraping report"`); n != 1 {
		t.Fatalf("scrape logged %d times, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "quiet=1m30s") {
		t.Errorf("scrape line must carry how long the screen was quiet:\n%s", buf.String())
	}
}

func TestReconcileWarnsWhenBuilderScreenUnreadable(t *testing.T) {
	buf := captureLog(t)
	f := &fakeHerdr{readOut: "terminal at nudge"}
	rt, b, clock, agents := nudgedBinding(t, f)

	clock.Advance(nudgeGrace + time.Second)
	f.readErr = errors.New("simulated herdr read error")
	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile must still swallow the read error: %v", err)
	}
	if got.Round != 1 {
		t.Fatalf("round = %d, want 1: the behaviour must not change", got.Round)
	}
	if !strings.Contains(buf.String(), `level=WARN msg="builder screen unreadable"`) {
		t.Errorf("a dropped read error must be logged at WARN:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "simulated herdr read error") {
		t.Errorf("the line must carry the error:\n%s", buf.String())
	}
}

func TestReconcileLogsHaltOncePerRound(t *testing.T) {
	buf := captureLog(t)
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
	b.RoundStartedAt = baseTime.Add(-31 * time.Minute)
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusWorking)}

	for i := 0; i < 3; i++ {
		var err error
		if b, err = reconcile(t, rt, b, agents); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}
	if b.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", b.State)
	}
	if n := strings.Count(buf.String(), `msg="binding halted"`); n != 1 {
		t.Fatalf("binding halted logged %d times across three halted ticks, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "has run past") {
		t.Errorf("halt line must carry the reason:\n%s", buf.String())
	}
}

func TestReconcileLogsBlockedOncePerRound(t *testing.T) {
	buf := captureLog(t)
	f := &fakeHerdr{readOut: "Allow edit to src/main.go?  1. Yes  2. No"}
	rt, b := sentBinding(t, f)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusBlocked)}

	for i := 0; i < 2; i++ {
		var err error
		if b, err = reconcile(t, rt, b, agents); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}
	if n := strings.Count(buf.String(), `msg="builder blocked"`); n != 1 {
		t.Fatalf("builder blocked logged %d times across two blocked ticks, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "question="+rt.Store.QuestionPath("webshop", 1)) {
		t.Errorf("blocked line must point at the question file:\n%s", buf.String())
	}
}
