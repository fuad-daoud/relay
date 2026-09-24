package ui

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// Options configures the reader. Interval is the only knob: the ReadAgent line
// count is always the viewport height, so it is not one.
type Options struct {
	// Interval is the list poll period. Floored at minInterval, default 2s to
	// match the daemon tick.
	Interval time.Duration

	// Prefs is where the ui keeps its own preferences (spec §6.0; P3b plan
	// §4.4). A zero Prefs (KV nil) keeps the ui stateless -- nothing loaded,
	// nothing saved.
	Prefs PrefsStore

	// Here is the cwd scope all resolves into a repo filter for
	// relevo.Bindings, exactly as `relevo history --here` does; "" means
	// every binding (docs/specs/2026-09-20-persistence-design.md §5.8).
	Here string

	// Notice is shown once, on the first frame, and cleared on the first
	// keypress like any other notice -- cmdUI's own db-open failure
	// ("no database: <err>") lands here so the ui still runs, in live
	// scope, rather than ever failing to start over it (§6).
	Notice string

	// PipeHint is the full refusal line RunSource prints when stdout is
	// not a terminal -- not a suffix. "" keeps the planner's own text,
	// which names `relevo status`.
	PipeHint string

	// Dashboard starts the reader on the dashboard screen instead of the
	// fleet (`relevo ui --dashboard`), once the first statusMsg has given
	// the rail rows to jump to
	// (docs/specs/2026-09-21-dashboard-design.md §6).
	Dashboard bool
}

const minInterval = 500 * time.Millisecond
const defaultInterval = 2 * time.Second

// stdoutStat is os.Stdout.Stat, replaceable so the terminal refusal path can be
// tested without a terminal.
var stdoutStat = os.Stdout.Stat

// Run renders relevo's state until the user quits or ctx is cancelled.
// It never mutates state.
//
// Preconditions:  stdout is a character device; rt.Store non-nil.
// Postconditions: the terminal is restored, including on panic.
// Errors:         startup failures only. Refresh failures never escape.
func Run(ctx context.Context, rt relevo.Runtime, opts Options) error {
	if notTTY() {
		return pipeRefusal(opts.PipeHint)
	}
	if rt.Store == nil {
		return errors.New("runtime requires Store")
	}
	return RunSource(ctx, plannerSource{rt}, opts)
}
