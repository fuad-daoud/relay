//go:build !unix

// Package proc is relay's local process Runner. relay targets Linux and
// macOS; this file exists so the tree compiles elsewhere, where every call
// reports relay.ErrRunnerUnavailable.
package proc

import (
	"context"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

const ExitTrailer = "relay-exit:"

const DefaultKillGrace = 5 * time.Second

type Runner struct {
	KillGrace time.Duration
}

var _ relay.Runner = (*Runner)(nil)

func New() *Runner { return &Runner{} }

func (r *Runner) Start(context.Context, relay.ProcSpec) (relay.ProcHandle, error) {
	return relay.ProcHandle{}, relay.ErrRunnerUnavailable
}

func (r *Runner) Alive(context.Context, relay.ProcHandle) (bool, error) {
	return false, relay.ErrRunnerUnavailable
}

func (r *Runner) ExitCode(context.Context, relay.ProcHandle, string) (int, bool) {
	return 0, false
}

func (r *Runner) Kill(context.Context, relay.ProcHandle) error {
	return relay.ErrRunnerUnavailable
}
