package relay

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

func seedDone(t *testing.T, rt Runtime, name, cwd string) {
	t.Helper()
	b := store.Binding{
		Name: name, CWD: cwd,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2"},
		Round:   3, State: store.StateDone,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

func TestGCClearsOnlyDoneBindings(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	seedDone(t, rt, "finished", "/repo-done")

	live := store.Binding{
		Name: "live", CWD: "/repo-live",
		Planner: store.Endpoint{PaneID: "w1:p3"},
		Builder: store.Endpoint{PaneID: "w1:p4"},
		Round:   1, State: store.StateActive,
	}
	if err := rt.Store.Save(live); err != nil {
		t.Fatalf("save live: %v", err)
	}
	// A binding needing a human must survive: removing it would throw away
	// the state that explains why it stopped.
	broken := store.Binding{
		Name: "broke", CWD: "/repo-broke",
		Planner: store.Endpoint{PaneID: "w1:p5"},
		Builder: store.Endpoint{PaneID: "w1:p6"},
		Round:   2, State: store.StateBroken,
	}
	if err := rt.Store.Save(broken); err != nil {
		t.Fatalf("save broken: %v", err)
	}

	got, err := GC(context.Background(), rt, GCOptions{})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Name != "finished" || !got[0].Deleted {
		t.Fatalf("gc result = %+v, want only the done binding deleted", got)
	}

	remaining, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("live and broken bindings must survive, got %d", len(remaining))
	}
}

func TestGCDryRunChangesNothing(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	seedDone(t, rt, "finished", "/repo-done")

	got, err := GC(context.Background(), rt, GCOptions{DryRun: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].Deleted || got[0].ArchivedTo != "" {
		t.Fatalf("dry run must report without acting, got %+v", got)
	}
	if _, err := rt.Store.Load("finished"); err != nil {
		t.Errorf("dry run deleted the binding: %v", err)
	}
}

func TestGCArchiveKeepsTheRoundLog(t *testing.T) {
	f := &fakeHerdr{}
	rt := newRuntime(t, f)
	seedDone(t, rt, "finished", "/repo-done")
	if err := rt.Store.AppendLog("finished", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	got, err := GC(context.Background(), rt, GCOptions{Archive: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(got) != 1 || got[0].ArchivedTo == "" || got[0].Deleted {
		t.Fatalf("gc result = %+v, want an archive not a delete", got)
	}
	if !strings.HasSuffix(got[0].ArchivedTo, ".tar.gz") {
		t.Errorf("archive path = %q, want a .tar.gz", got[0].ArchivedTo)
	}
	if _, err := os.Stat(got[0].ArchivedTo); err != nil {
		t.Errorf("archive missing: %v", err)
	}
}
