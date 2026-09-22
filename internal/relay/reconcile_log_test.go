package relay

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

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
func settleTick(t *testing.T, rt Runtime, b store.Binding, f *fakePanes) store.Binding {
	t.Helper()
	var next store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		next, err = deliverAndSettle(context.Background(), rt, tx, b)
		return err
	})
	if err != nil {
		t.Fatalf("deliverAndSettle: %v", err)
	}
	return next
}

func TestReconcileLogsHaltOncePerRound(t *testing.T) {
	buf := captureLog(t)
	f := &fakePanes{}
	rt, b := sentBinding(t, f)
	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
	b.RoundStartedAt = baseTime.Add(-31 * time.Minute)
	agents := []stubAgent{plannerWith(stubIdle, false), builderAgent(stubWorking)}

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
