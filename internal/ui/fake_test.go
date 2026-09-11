package ui

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
)

type fakeHerdr struct {
	t         *testing.T
	agents    []herdr.Agent
	listErr   error
	readOut   string
	readErr   error
	readCalls int
	// readTargets records what each ReadAgent call was addressed to. A count
	// alone cannot catch addressing the builder by a name herdr has forgotten.
	readTargets []string
}

func newFakeHerdr(t *testing.T) *fakeHerdr {
	return &fakeHerdr{t: t}
}

func (f *fakeHerdr) ListAgents(_ context.Context) ([]herdr.Agent, error) {
	return f.agents, f.listErr
}

func (f *fakeHerdr) ReadAgent(_ context.Context, target string, _ int) (string, error) {
	f.readCalls++
	f.readTargets = append(f.readTargets, target)
	return f.readOut, f.readErr
}

func (f *fakeHerdr) Prompt(_ context.Context, _, _ string) error {
	f.t.Errorf("read-only violation: Prompt called")
	return errors.New("read-only violation: Prompt called")
}

func (f *fakeHerdr) SendKeys(_ context.Context, _, _ string) error {
	f.t.Errorf("read-only violation: SendKeys called")
	return errors.New("read-only violation: SendKeys called")
}

func (f *fakeHerdr) ReadAgentSource(_ context.Context, _, _ string, _ int) (string, error) {
	f.t.Errorf("read-only violation: ReadAgentSource called")
	return "", errors.New("read-only violation: ReadAgentSource called")
}

func (f *fakeHerdr) CreateTab(_ context.Context, _, _, _ string) (string, error) {
	f.t.Errorf("read-only violation: CreateTab called")
	return "", errors.New("read-only violation: CreateTab called")
}

func (f *fakeHerdr) StartAgent(_ context.Context, _, _, _ string, _ []string) error {
	f.t.Errorf("read-only violation: StartAgent called")
	return errors.New("read-only violation: StartAgent called")
}

func (f *fakeHerdr) Notify(_ context.Context, _ string) error {
	f.t.Errorf("read-only violation: Notify called")
	return errors.New("read-only violation: Notify called")
}

func (f *fakeHerdr) ClosePane(_ context.Context, _ string) error {
	f.t.Errorf("read-only violation: ClosePane called")
	return errors.New("read-only violation: ClosePane called")
}
