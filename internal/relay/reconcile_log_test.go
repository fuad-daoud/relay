package relay

import (
	"bytes"
	"log/slog"
	"testing"
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
// nudgedBinding drives one binding through the nudge: plan sent, builder
// idle past startGrace, no report on disk. It returns the binding after the
// nudge tick and the clock the caller advances between later ticks.
