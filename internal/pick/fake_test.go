package pick

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

type sentKeys struct{ Target, Keys string }

type fakeHerdr struct {
	t       *testing.T
	agents  []herdr.Agent
	listErr error

	readOut string
	readErr error
	reads   []string // targets ReadAgentSource was addressed to

	keys []sentKeys
}

func newFakeHerdr(t *testing.T) *fakeHerdr { return &fakeHerdr{t: t} }

func (f *fakeHerdr) ListAgents(_ context.Context) ([]herdr.Agent, error) {
	return f.agents, f.listErr
}

func (f *fakeHerdr) SendKeys(_ context.Context, target, keys string) error {
	f.keys = append(f.keys, sentKeys{Target: target, Keys: keys})
	return nil
}

func (f *fakeHerdr) ReadAgentSource(_ context.Context, target, _ string, _ int) (string, error) {
	f.reads = append(f.reads, target)
	return f.readOut, f.readErr
}

func (f *fakeHerdr) ReadAgent(_ context.Context, _ string, _ int) (string, error) {
	f.t.Errorf("pick never reads scrollback: ReadAgent called")
	return "", errors.New("ReadAgent called")
}

func (f *fakeHerdr) Prompt(_ context.Context, _, _ string) error {
	f.t.Errorf("pick never prompts: Prompt called")
	return errors.New("Prompt called")
}

func (f *fakeHerdr) CreateTab(_ context.Context, _, _, _ string) (string, error) {
	f.t.Errorf("pick never spawns: CreateTab called")
	return "", errors.New("CreateTab called")
}

func (f *fakeHerdr) StartAgent(_ context.Context, _, _, _ string, _ []string) error {
	f.t.Errorf("pick never spawns: StartAgent called")
	return errors.New("StartAgent called")
}

func (f *fakeHerdr) Notify(_ context.Context, _ string) error {
	f.t.Errorf("pick never notifies: Notify called")
	return errors.New("Notify called")
}

func (f *fakeHerdr) ClosePane(_ context.Context, _ string) error {
	f.t.Errorf("pick never closes panes: ClosePane called")
	return errors.New("ClosePane called")
}

// testBinding is a bound, active binding whose builder herdr knows by name.
func testBinding(name string) store.Binding {
	return store.Binding{
		Name:             name,
		CWD:              "/tmp/" + name,
		Planner:          store.Endpoint{PaneID: "w1:p1", SessionID: "planner-session", Kind: "claude"},
		Builder:          store.Endpoint{AgentName: name + "-builder", PaneID: "w1:p2", Kind: "opencode"},
		BuilderCandidate: "agy",
		Round:            2,
		State:            store.StateActive,
	}
}

// blockedAgent is the herdr view of testBinding(name)'s builder at a dialog.
func blockedAgent(name string) herdr.Agent {
	return herdr.Agent{Name: name + "-builder", Kind: "opencode", PaneID: "w1:p2", Status: herdr.StatusBlocked}
}

// testRuntime seeds a store with the given bindings and returns a runtime
// over it and the fake herdr.
func testRuntime(t *testing.T, fh *fakeHerdr, bindings ...store.Binding) relay.Runtime {
	t.Helper()
	st := store.New(t.TempDir())
	for _, b := range bindings {
		if err := st.Save(b); err != nil {
			t.Fatalf("Save %s: %v", b.Name, err)
		}
	}
	return relay.Runtime{Store: st, Herdr: fh}
}

// rowsMsg builds the statusMsg the list would receive for these rows.
func rowsMsg(rows ...relay.BindingStatus) statusMsg {
	return statusMsg{report: relay.Report{Bindings: rows}}
}

func row(name, display, builderStatus string) relay.BindingStatus {
	return relay.BindingStatus{Name: name, Display: display, Round: 2, BuilderCandidate: "agy", BuilderStatus: builderStatus}
}

// update runs one message through the model and returns the Model back.
func update(t *testing.T, m Model, msg interface{}) (Model, func() interface{}) {
	t.Helper()
	nm, cmd := m.Update(msg)
	out, ok := nm.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", nm)
	}
	if cmd == nil {
		return out, nil
	}
	return out, func() interface{} { return cmd() }
}
