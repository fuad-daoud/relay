//go:build !unix

// Package proc is relevo's local process Runner. relevo targets Linux and
// macOS; this file exists so the tree compiles elsewhere, where every call
// reports relevo.ErrRunnerUnavailable.
package proc

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

const ExitTrailer = "relevo-exit:"

const DefaultKillGrace = 5 * time.Second

type Runner struct {
	KillGrace time.Duration
}

var _ relevo.Runner = (*Runner)(nil)

func New() *Runner { return &Runner{} }

func (r *Runner) Start(context.Context, relevo.ProcSpec) (relevo.ProcHandle, error) {
	return relevo.ProcHandle{}, relevo.ErrRunnerUnavailable
}

func (r *Runner) Alive(context.Context, relevo.ProcHandle) (bool, error) {
	return false, relevo.ErrRunnerUnavailable
}

func (r *Runner) ExitCode(context.Context, relevo.ProcHandle, string) (int, bool) {
	return 0, false
}

func (r *Runner) Kill(context.Context, relevo.ProcHandle) error {
	return relevo.ErrRunnerUnavailable
}

func (r *Runner) Rusage(context.Context, relevo.ProcHandle, string) (relevo.ProcRusage, bool) {
	return relevo.ProcRusage{}, false
}

// StartTime reports ErrRunnerUnavailable too: psInfo does not exist here.
func StartTime(context.Context, int) (time.Time, error) {
	return time.Time{}, relevo.ErrRunnerUnavailable
}
