package relay

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// stopEntries returns the binding's KindStop entries, oldest first.
func stopEntries(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindStop {
			out = append(out, e)
		}
	}
	return out
}

func TestStopDecisionTable(t *testing.T) {
	open := store.Binding{Round: 1, RoundStartedAt: baseTime}

	cases := []struct {
		name string
		b    store.Binding
		want stopAction
	}{
		{"no round", store.Binding{Round: 1}, stopNothing},
		{"open round", open, stopKill},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stopDecision(c.b, baseTime); got != c.want {
				t.Errorf("stopDecision = %v, want %v", got, c.want)
			}
		})
	}
}

// TestStopPaneRequestsAndRecords pins the pane path: the wrap-up prompt goes
// through the same delivery path as a plan, and the request and its grace are
// recorded while the round stays open.
// TestStopHeadlessKillsAndClosesWithoutSwitch pins design question 1's option
// (a): a headless round has no stdin, so a stop kills now and closes the
// round without a report -- never through the exit-without-report path, which
// would switch the builder and charge the round.
func TestStopHeadlessKillsAndClosesWithoutSwitch(t *testing.T) {
	t.Run("no report", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, b := sentHeadless(t, f, fr)
		h := handleOf(b.Builder)

		res, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if res.Action != "killed" {
			t.Errorf("Action = %q, want killed", res.Action)
		}

		if len(fr.kills) != 1 || fr.kills[0] != h {
			t.Fatalf("kills = %+v, want the round's process %+v", fr.kills, h)
		}
		if len(fr.specs) != 1 {
			t.Errorf("specs = %d, want 1: a stop must not start a replacement", len(fr.specs))
		}

		got, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		if got.Round != 2 {
			t.Errorf("Round = %d, want 2: the round closed", got.Round)
		}
		if got.RoundSwitches != 0 {
			t.Errorf("RoundSwitches = %d, want 0: a stop never charges a switch", got.RoundSwitches)
		}
		if got.RoundExcluded != nil {
			t.Errorf("RoundExcluded = %v, want it to exclude nobody", got.RoundExcluded)
		}
		if got.Builder.PID != 0 {
			t.Errorf("Builder.PID = %d, want 0 after the process was stopped", got.Builder.PID)
		}

		pending, found, err := rt.Store.PendingForPlanner("webshop")
		if err != nil || !found {
			t.Fatalf("report must be queued: found=%v err=%v", found, err)
		}
		if !strings.Contains(pending.Note, "noreport stopped") {
			t.Errorf("report note = %q, want noreport stopped", pending.Note)
		}
		stops := stopEntries(t, rt, "webshop")
		if len(stops) != 1 || stops[0].Note != "stopped/killed" {
			t.Errorf("stop entries = %+v, want one stopped/killed", stops)
		}
	})

	t.Run("with a report on disk", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, _ := sentHeadless(t, f, fr)
		reportPath := rt.Store.ReportPath("webshop", 1)
		if err := os.WriteFile(reportPath, []byte("I stopped where I was\n"), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}

		if _, err := Stop(context.Background(), rt, "webshop", StopOptions{}); err != nil {
			t.Fatalf("Stop: %v", err)
		}

		pending, found, err := rt.Store.PendingForPlanner("webshop")
		if err != nil || !found {
			t.Fatalf("report must be queued: found=%v err=%v", found, err)
		}
		if !strings.Contains(pending.Note, "stopped") {
			t.Errorf("report note = %q, want it to say stopped", pending.Note)
		}
		if !strings.Contains(pending.Payload, reportPath) {
			t.Errorf("payload = %q, must name the report on disk", pending.Payload)
		}
	})
}

func TestStopNothingToStop(t *testing.T) {
	t.Run("no open round", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		b.RoundStartedAt = time.Time{}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		if _, err := Stop(context.Background(), rt, "webshop", StopOptions{}); !errors.Is(err, ErrNothingToStop) {
			t.Fatalf("err = %v, want ErrNothingToStop", err)
		}
	})

	t.Run("done", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		b.State = store.StateDone
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err == nil || !strings.Contains(err.Error(), "done") {
			t.Fatalf("err = %v, want a refusal naming done", err)
		}
	})

	t.Run("paused", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		b.State = store.StatePaused
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err == nil || !strings.Contains(err.Error(), "paused") {
			t.Fatalf("err = %v, want a refusal naming paused", err)
		}
	})

	t.Run("remote", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		b.Builder.Mode = store.ModeRemote
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err == nil || !strings.Contains(err.Error(), "remote") {
			t.Fatalf("err = %v, want a refusal naming remote", err)
		}
	})
}

func TestSendClearsStopRequest(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.StopRequestedAt = baseTime
	b.StopGraceMS = 300000
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	endProcess(t, rt, b)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "keep going"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if !got.StopRequestedAt.IsZero() || got.StopGraceMS != 0 {
		t.Errorf("a send must clear the stop bookkeeping: at=%s grace=%d", got.StopRequestedAt, got.StopGraceMS)
	}
}
