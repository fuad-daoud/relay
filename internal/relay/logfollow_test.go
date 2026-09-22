package relay

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// followedBinding saves a bare binding with two log entries and returns its
// name: enough to prove FollowLog drains what is already there, then follows.
func followedBinding(t *testing.T, rt Runtime) string {
	t.Helper()
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := rt.Store.AppendLog(b.Name, store.LogEntry{
			TS: rt.Now().UTC(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true,
		}); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	return b.Name
}
