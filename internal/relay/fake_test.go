package relay

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

type promptCall struct{ Target, Text string }
type keyCall struct{ Target, Keys string }
type startCall struct {
	Name, Kind, Pane string
	Args             []string
}

// fakeHerdr is the in-memory Herdr used by every test in this package.
type fakeHerdr struct {
	agents    []herdr.Agent
	prompts   []promptCall
	keys      []keyCall
	starts    []startCall
	notices   []string
	readOut   string
	newPane   string
	promptErr error
	stalls    int // when >0, Prompt returns ErrPromptStalled and decrements
	listCalls int
}

func (f *fakeHerdr) ListAgents(context.Context) ([]herdr.Agent, error) {
	f.listCalls++
	return f.agents, nil
}

func (f *fakeHerdr) Prompt(_ context.Context, target, text string) error {
	if f.stalls > 0 {
		f.stalls--
		return herdr.ErrPromptStalled
	}
	if f.promptErr != nil {
		return f.promptErr
	}
	f.prompts = append(f.prompts, promptCall{Target: target, Text: text})
	return nil
}

func (f *fakeHerdr) SendKeys(_ context.Context, target, keys string) error {
	f.keys = append(f.keys, keyCall{Target: target, Keys: keys})
	return nil
}

func (f *fakeHerdr) ReadAgent(context.Context, string, int) (string, error) { return f.readOut, nil }

func (f *fakeHerdr) SplitPane(context.Context, string, string, string) (string, error) {
	return f.newPane, nil
}

func (f *fakeHerdr) StartAgent(_ context.Context, name, kind, pane string, args []string) error {
	f.starts = append(f.starts, startCall{Name: name, Kind: kind, Pane: pane, Args: args})
	return nil
}

func (f *fakeHerdr) Notify(_ context.Context, msg string) error {
	f.notices = append(f.notices, msg)
	return nil
}

func TestFakeSatisfiesHerdr(t *testing.T) {
	var _ Herdr = (*fakeHerdr)(nil)
}

func TestFindAgentPrefersSessionOverPane(t *testing.T) {
	agents := []herdr.Agent{
		{PaneID: "w2:p3", Session: herdr.Session{Value: "stale"}, Status: herdr.StatusIdle},
		{PaneID: "w9:pZ", Session: herdr.Session{Value: "sess-1"}, Status: herdr.StatusWorking},
	}
	ep := store.Endpoint{PaneID: "w2:p3", SessionID: "sess-1"}

	got, ok := FindAgent(agents, ep)
	if !ok {
		t.Fatal("FindAgent found nothing")
	}
	if got.PaneID != "w9:pZ" {
		t.Errorf("matched %q; session id must win, since a moved pane gets a new id", got.PaneID)
	}
}

func TestFindAgentFallsBackToPane(t *testing.T) {
	agents := []herdr.Agent{{PaneID: "w2:p4", Status: herdr.StatusIdle}}

	if _, ok := FindAgent(agents, store.Endpoint{PaneID: "w2:p4"}); !ok {
		t.Fatal("pane fallback failed")
	}
	if _, ok := FindAgent(agents, store.Endpoint{PaneID: "w2:p9"}); ok {
		t.Fatal("matched a pane that is not present")
	}
}
