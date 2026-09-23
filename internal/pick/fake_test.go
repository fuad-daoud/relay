package pick

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

type sentKeys struct{ Target, Keys string }

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

// testRuntime seeds a store with the given bindings and returns a runtime
// over it and the fake herdr. Now is set because relay.Answer stamps its log
// entry with it; a nil clock panics.
func testRuntime(t *testing.T, bindings ...store.Binding) relay.Runtime {
	t.Helper()
	st := store.New(t.TempDir())
	for _, b := range bindings {
		if err := st.Save(b); err != nil {
			t.Fatalf("Save %s: %v", b.Name, err)
		}
	}
	return relay.Runtime{Store: st, Now: time.Now}
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
