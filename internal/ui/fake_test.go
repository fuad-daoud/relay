package ui

import (
	"context"
	"errors"
	"testing"
)

type fakePanes struct {
	t         *testing.T
	agents    []stubAgent
	listErr   error
	readOut   string
	readErr   error
	readCalls int
	// readTargets records what each ReadAgent call was addressed to. A count
	// alone cannot catch addressing the builder by a name herdr has forgotten.
	readTargets []string
}

func newFakeHerdr(t *testing.T) *fakePanes {
	return &fakePanes{t: t}
}

func (f *fakePanes) ListAgents(_ context.Context) ([]stubAgent, error) {
	return f.agents, f.listErr
}

func (f *fakePanes) ReadAgent(_ context.Context, target string, _ int) (string, error) {
	f.readCalls++
	f.readTargets = append(f.readTargets, target)
	return f.readOut, f.readErr
}

func (f *fakePanes) Prompt(_ context.Context, _, _ string) error {
	f.t.Errorf("read-only violation: Prompt called")
	return errors.New("read-only violation: Prompt called")
}

func (f *fakePanes) SendKeys(_ context.Context, _, _ string) error {
	f.t.Errorf("read-only violation: SendKeys called")
	return errors.New("read-only violation: SendKeys called")
}

func (f *fakePanes) ReadAgentSource(_ context.Context, _, _ string, _ int) (string, error) {
	f.t.Errorf("read-only violation: ReadAgentSource called")
	return "", errors.New("read-only violation: ReadAgentSource called")
}

func (f *fakePanes) CreateTab(_ context.Context, _, _, _ string) (string, error) {
	f.t.Errorf("read-only violation: CreateTab called")
	return "", errors.New("read-only violation: CreateTab called")
}

func (f *fakePanes) StartAgent(_ context.Context, _, _, _ string, _ []string) error {
	f.t.Errorf("read-only violation: StartAgent called")
	return errors.New("read-only violation: StartAgent called")
}

func (f *fakePanes) Notify(_ context.Context, _, _ string, _ herdr.Sound) error {
	f.t.Errorf("read-only violation: Notify called")
	return errors.New("read-only violation: Notify called")
}

func (f *fakePanes) ReportMetadata(_ context.Context, _ string, _ herdr.PaneMetadata) error {
	f.t.Errorf("read-only violation: ReportMetadata called")
	return errors.New("read-only violation: ReportMetadata called")
}

func (f *fakePanes) ClosePane(_ context.Context, _ string) error {
	f.t.Errorf("read-only violation: ClosePane called")
	return errors.New("read-only violation: ClosePane called")
}

func (f *fakePanes) Subscribe(_ context.Context, _ []string) (<-chan herdr.Event, error) {
	return nil, herdr.ErrNoSocket
}
