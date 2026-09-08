package ui

import (
	"context"
	"errors"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

// Options configures the reader. Interval is the only knob: the ReadAgent line
// count is always the viewport height, so it is not one.
type Options struct {
	// Interval is the list poll period. Floored at minInterval, default 2s to
	// match the daemon tick and `relay watch`.
	Interval time.Duration
}

const minInterval = 500 * time.Millisecond
const defaultInterval = 2 * time.Second

// stdoutStat is os.Stdout.Stat, replaceable so the terminal refusal path can be
// tested without a terminal.
var stdoutStat = os.Stdout.Stat

// Run renders relay's state until the user quits or ctx is cancelled.
// It never mutates state.
//
// Preconditions:  stdout is a character device; rt.Herdr and rt.Store non-nil.
// Postconditions: the terminal is restored, including on panic.
// Errors:         startup failures only. Refresh failures never escape.
func Run(ctx context.Context, rt relay.Runtime, opts Options) error {
	info, err := stdoutStat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return errors.New("relay ui needs a terminal; use `relay status` or `relay watch` when piping")
	}
	if rt.Herdr == nil || rt.Store == nil {
		return errors.New("runtime requires Herdr and Store")
	}

	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	} else if opts.Interval < minInterval {
		opts.Interval = minInterval
	}

	p := tea.NewProgram(newModel(ctx, rt, opts), tea.WithAltScreen(), tea.WithContext(ctx))
	_, err = p.Run()
	return runResult(ctx, err)
}

// runResult maps bubbletea's exit into Run's contract: a cancelled context is a
// clean exit, not an error. Kept separate from Run so it can be tested without
// starting a terminal program -- the test that did that failed anywhere without
// a tty, including every CI runner.
func runResult(ctx context.Context, err error) error {
	if ctx.Err() != nil && (errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, tea.ErrInterrupted)) {
		return nil
	}
	return err
}
