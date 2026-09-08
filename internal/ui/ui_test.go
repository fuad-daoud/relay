package ui

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

type fakeCharDevice struct{}

func (fakeCharDevice) Name() string       { return "fake" }
func (fakeCharDevice) Size() int64        { return 0 }
func (fakeCharDevice) Mode() os.FileMode  { return os.ModeCharDevice }
func (fakeCharDevice) ModTime() time.Time { return time.Time{} }
func (fakeCharDevice) IsDir() bool        { return false }
func (fakeCharDevice) Sys() any           { return nil }

func TestRunCancelledContextReturnsNil(t *testing.T) {
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	rt := relay.Runtime{Store: st, Herdr: fh}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	origStat := stdoutStat
	stdoutStat = func() (os.FileInfo, error) {
		return fakeCharDevice{}, nil
	}
	defer func() { stdoutStat = origStat }()

	err := Run(ctx, rt, Options{})
	if err != nil {
		t.Fatalf("expected nil error on cancelled context, got %v", err)
	}
}
