package ui

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

type fakeCharDevice struct {
	mode os.FileMode
}

func (f fakeCharDevice) Name() string       { return "fake" }
func (f fakeCharDevice) Size() int64        { return 0 }
func (f fakeCharDevice) Mode() os.FileMode  { return f.mode }
func (f fakeCharDevice) ModTime() time.Time { return time.Time{} }
func (f fakeCharDevice) IsDir() bool        { return false }
func (f fakeCharDevice) Sys() any           { return nil }

func TestRunRefusesNonCharacterDevice(t *testing.T) {
	origStat := stdoutStat
	stdoutStat = func() (os.FileInfo, error) {
		return fakeCharDevice{mode: 0}, nil
	}
	defer func() { stdoutStat = origStat }()

	err := Run(context.Background(), relay.Runtime{}, Options{})
	want := "relay ui needs a terminal; use `relay status` or `relay watch` when piping"
	if err == nil || err.Error() != want {
		t.Fatalf("expected error %q, got %v", want, err)
	}
}

func TestRunResult(t *testing.T) {
	otherErr := errors.New("something else")

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	liveCtx := context.Background()

	tests := []struct {
		name    string
		ctx     context.Context
		err     error
		wantErr error
	}{
		{
			name:    "cancelled context with ErrProgramKilled returns nil",
			ctx:     cancelledCtx,
			err:     tea.ErrProgramKilled,
			wantErr: nil,
		},
		{
			name:    "cancelled context with ErrInterrupted returns nil",
			ctx:     cancelledCtx,
			err:     tea.ErrInterrupted,
			wantErr: nil,
		},
		{
			name:    "cancelled context with other error returns error",
			ctx:     cancelledCtx,
			err:     otherErr,
			wantErr: otherErr,
		},
		{
			name:    "live context with ErrProgramKilled returns error",
			ctx:     liveCtx,
			err:     tea.ErrProgramKilled,
			wantErr: tea.ErrProgramKilled,
		},
		{
			name:    "live context with ErrInterrupted returns error",
			ctx:     liveCtx,
			err:     tea.ErrInterrupted,
			wantErr: tea.ErrInterrupted,
		},
		{
			name:    "live context with nil returns nil",
			ctx:     liveCtx,
			err:     nil,
			wantErr: nil,
		},
		{
			name:    "cancelled context with nil returns nil",
			ctx:     cancelledCtx,
			err:     nil,
			wantErr: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runResult(tc.ctx, tc.err)
			if !errors.Is(got, tc.wantErr) {
				t.Fatalf("runResult() = %v, want %v", got, tc.wantErr)
			}
		})
	}
}
