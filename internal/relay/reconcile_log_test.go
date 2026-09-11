package relay

import (
	"bytes"
	"context"
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
