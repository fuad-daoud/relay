package serve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestAdminStatusAllOwners(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	s, err := New(Config{
		Root: root,
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	kpA, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	idA := remote.IDOf(kpA.Public)
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kpA.Public, "alice"), now); err != nil {
		t.Fatal(err)
	}

	kpB, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	idB := remote.IDOf(kpB.Public)
	if _, err := s.clients.Add("bob", remote.MarshalPublic(kpB.Public, "bob"), now); err != nil {
		t.Fatal(err)
	}

	rtA, err := s.runtime(idA)
	if err != nil {
		t.Fatal(err)
	}
	bA := store.Binding{
		Name:    "app-a",
		Owner:   string(idA),
		CWD:     rtA.Store.WorktreePath("app-a"),
		State:   store.StateActive,
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
	}
	if err := rtA.Store.Save(bA); err != nil {
		t.Fatal(err)
	}

	rtB, err := s.runtime(idB)
	if err != nil {
		t.Fatal(err)
	}
	bB := store.Binding{
		Name:    "app-b",
		Owner:   string(idB),
		CWD:     rtB.Store.WorktreePath("app-b"),
		State:   store.StateActive,
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
	}
	if err := rtB.Store.Save(bB); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	statuses, err := AdminStatus(ctx, s)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}

	if len(statuses) != 2 {
		t.Fatalf("got %d owner statuses, want 2", len(statuses))
	}

	if statuses[0].Label != "alice" || statuses[1].Label != "bob" {
		t.Errorf("owners not sorted by label: got [%s, %s], want [alice, bob]", statuses[0].Label, statuses[1].Label)
	}

	if len(statuses[0].Report.Bindings) != 1 || statuses[0].Report.Bindings[0].Name != "app-a" {
		t.Errorf("alice bindings = %+v, want [app-a]", statuses[0].Report.Bindings)
	}
	if len(statuses[1].Report.Bindings) != 1 || statuses[1].Report.Bindings[0].Name != "app-b" {
		t.Errorf("bob bindings = %+v, want [app-b]", statuses[1].Report.Bindings)
	}

	rendered := RenderAdminStatus(statuses)
	if !strings.Contains(rendered, "alice  (") {
		t.Errorf("rendered missing alice header: %q", rendered)
	}
	if !strings.Contains(rendered, "bob  (") {
		t.Errorf("rendered missing bob header: %q", rendered)
	}
	if !strings.Contains(rendered, "  app-a") {
		t.Errorf("rendered missing indented app-a: %q", rendered)
	}
	if !strings.Contains(rendered, "  app-b") {
		t.Errorf("rendered missing indented app-b: %q", rendered)
	}

	aliceIdx := strings.Index(rendered, "alice")
	bobIdx := strings.Index(rendered, "bob")
	if aliceIdx > bobIdx {
		t.Errorf("alice (idx %d) should appear before bob (idx %d)", aliceIdx, bobIdx)
	}

	// Empty input
	if got := RenderAdminStatus(nil); got != "no owners\n" {
		t.Errorf("RenderAdminStatus(nil) = %q, want %q", got, "no owners\n")
	}

	// Owner with no bindings
	noBindingsStatus := []OwnerStatus{
		{Owner: idA, Label: "charlie", Report: relay.Report{}},
	}
	if got := RenderAdminStatus(noBindingsStatus); got != "charlie  no bindings\n" {
		t.Errorf("RenderAdminStatus(no bindings) = %q, want %q", got, "charlie  no bindings\n")
	}
}

func TestGCAbandonedArchivesOnlyIdleOld(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	s, err := New(Config{
		Root: root,
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), now); err != nil {
		t.Fatal(err)
	}

	rt, err := s.runtime(id)
	if err != nil {
		t.Fatal(err)
	}

	// 1. old+idle: LastSeen 48h ago, idle -> should be archived
	bOldIdle := store.Binding{
		Name:    "old-idle",
		Owner:   string(id),
		CWD:     rt.Store.WorktreePath("old-idle"),
		State:   store.StateActive,
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
		Serve: &store.ServeFacts{
			LastSeen: now.Add(-48 * time.Hour),
		},
	}
	if err := rt.Store.Save(bOldIdle); err != nil {
		t.Fatal(err)
	}

	// 2. old+running: LastSeen 48h ago, running -> kept
	bOldRunning := store.Binding{
		Name:    "old-running",
		Owner:   string(id),
		CWD:     rt.Store.WorktreePath("old-running"),
		State:   store.StateActive,
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
		Serve: &store.ServeFacts{
			LastSeen: now.Add(-48 * time.Hour),
		},
	}
	if err := rt.Store.Save(bOldRunning); err != nil {
		t.Fatal(err)
	}
	// Make bOldRunning's round 1 running by appending KindPlan to builder without KindReport
	if err := rt.Store.AppendLog("old-running", store.LogEntry{
		Round:     1,
		Kind:      store.KindPlan,
		Direction: store.DirToBuilder,
		TS:        now.Add(-48 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// 3. new+idle: LastSeen 1h ago -> kept
	bNewIdle := store.Binding{
		Name:    "new-idle",
		Owner:   string(id),
		CWD:     rt.Store.WorktreePath("new-idle"),
		State:   store.StateActive,
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
		Serve: &store.ServeFacts{
			LastSeen: now.Add(-1 * time.Hour),
		},
	}
	if err := rt.Store.Save(bNewIdle); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	olderThan := 24 * time.Hour

	// Dry run: should list old-idle, archive nothing
	dryResults, err := GCAbandoned(ctx, s, olderThan, now, true)
	if err != nil {
		t.Fatalf("GCAbandoned dry run: %v", err)
	}
	if len(dryResults) != 1 {
		t.Fatalf("dry run got %d results, want 1", len(dryResults))
	}
	if dryResults[0].Name != "old-idle" {
		t.Errorf("dry run result name = %q, want old-idle", dryResults[0].Name)
	}
	if dryResults[0].Archive != "" {
		t.Errorf("dry run archive path = %q, want empty", dryResults[0].Archive)
	}

	// Verify old-idle still exists in store
	if _, err := rt.Store.Load("old-idle"); err != nil {
		t.Fatalf("old-idle was removed during dry run: %v", err)
	}

	// Actual run: should archive old-idle
	results, err := GCAbandoned(ctx, s, olderThan, now, false)
	if err != nil {
		t.Fatalf("GCAbandoned actual: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("actual run got %d results, want 1", len(results))
	}
	if results[0].Name != "old-idle" {
		t.Errorf("result name = %q, want old-idle", results[0].Name)
	}
	if results[0].Archive == "" {
		t.Errorf("result archive path is empty, want archive destination")
	}

	// Verify old-idle is gone from active store
	if _, err := rt.Store.Load("old-idle"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old-idle load err = %v, want ErrNotFound", err)
	}

	// Verify old-running is kept
	if _, err := rt.Store.Load("old-running"); err != nil {
		t.Errorf("old-running was archived but should have been kept: %v", err)
	}

	// Verify new-idle is kept
	if _, err := rt.Store.Load("new-idle"); err != nil {
		t.Errorf("new-idle was archived but should have been kept: %v", err)
	}
}

func TestAdminUnbindByLabelAndId(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	s, err := New(Config{
		Root: root,
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), now); err != nil {
		t.Fatal(err)
	}

	rt, err := s.runtime(id)
	if err != nil {
		t.Fatal(err)
	}

	bByLabel := store.Binding{
		Name:    "by-label",
		Owner:   string(id),
		CWD:     rt.Store.WorktreePath("by-label"),
		State:   store.StateActive,
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
	}
	if err := rt.Store.Save(bByLabel); err != nil {
		t.Fatal(err)
	}

	bByID := store.Binding{
		Name:    "by-id",
		Owner:   string(id),
		CWD:     rt.Store.WorktreePath("by-id"),
		State:   store.StateActive,
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
	}
	if err := rt.Store.Save(bByID); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	if _, err := AdminUnbind(ctx, s, "alice", "by-label", false); err != nil {
		t.Fatalf("AdminUnbind by label: %v", err)
	}
	if _, err := rt.Store.Load("by-label"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("by-label load err = %v, want ErrNotFound", err)
	}

	if _, err := AdminUnbind(ctx, s, string(id), "by-id", false); err != nil {
		t.Fatalf("AdminUnbind by id: %v", err)
	}
	if _, err := rt.Store.Load("by-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("by-id load err = %v, want ErrNotFound", err)
	}
}

func TestAdminUnbindRefusesRunningUnlessForce(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	s, err := New(Config{
		Root: root,
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), now); err != nil {
		t.Fatal(err)
	}

	rt, err := s.runtime(id)
	if err != nil {
		t.Fatal(err)
	}

	b := store.Binding{
		Name:    "running",
		Owner:   string(id),
		CWD:     rt.Store.WorktreePath("running"),
		State:   store.StateActive,
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.AppendLog("running", store.LogEntry{
		Round:     1,
		Kind:      store.KindPlan,
		Direction: store.DirToBuilder,
		TS:        now,
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	if _, err := AdminUnbind(ctx, s, "alice", "running", false); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("AdminUnbind without force: got err %v, want a refusal mentioning 'running'", err)
	}
	if _, err := rt.Store.Load("running"); err != nil {
		t.Errorf("running binding was removed despite the refusal: %v", err)
	}

	if _, err := AdminUnbind(ctx, s, "alice", "running", true); err != nil {
		t.Fatalf("AdminUnbind with force: %v", err)
	}
	if _, err := rt.Store.Load("running"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("running binding still present after forced unbind: %v", err)
	}
}

func TestAdminUnbindAmbiguousLabel(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	s, err := New(Config{
		Root: root,
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	kp1, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("dup", remote.MarshalPublic(kp1.Public, "dup"), now); err != nil {
		t.Fatal(err)
	}

	kp2, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("dup", remote.MarshalPublic(kp2.Public, "dup"), now); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := AdminUnbind(ctx, s, "dup", "whatever", false); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("AdminUnbind with ambiguous label: got err %v, want an 'ambiguous' error", err)
	}
}

func TestRenderClients(t *testing.T) {
	enrolled := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	revoked := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	t.Run("empty input", func(t *testing.T) {
		got := RenderClients(nil)
		if got != "no clients\n" {
			t.Errorf("RenderClients(nil) = %q, want %q", got, "no clients\n")
		}
	})

	t.Run("one enrolled client", func(t *testing.T) {
		clients := []Client{
			{
				ID:         remote.ClientID("SHA256:abcdefgh"),
				Label:      "alice",
				EnrolledAt: enrolled,
			},
		}
		got := RenderClients(clients)
		want := "SHA256:abcdefgh  alice  enrolled 2026-01-15\n"
		if got != want {
			t.Errorf("RenderClients(one enrolled) = %q, want %q", got, want)
		}
	})

	t.Run("one revoked client", func(t *testing.T) {
		clients := []Client{
			{
				ID:         remote.ClientID("SHA256:abcdefgh"),
				Label:      "alice",
				EnrolledAt: enrolled,
				RevokedAt:  revoked,
			},
		}
		got := RenderClients(clients)
		want := "SHA256:abcdefgh  alice  enrolled 2026-01-15  revoked 2026-03-20\n"
		if got != want {
			t.Errorf("RenderClients(one revoked) = %q, want %q", got, want)
		}
	})

	t.Run("two clients order preserved", func(t *testing.T) {
		clients := []Client{
			{
				ID:         remote.ClientID("SHA256:aaaaaaaa"),
				Label:      "bob",
				EnrolledAt: enrolled,
			},
			{
				ID:         remote.ClientID("SHA256:bbbbbbbb"),
				Label:      "alice",
				EnrolledAt: enrolled,
				RevokedAt:  revoked,
			},
		}
		got := RenderClients(clients)
		want := "SHA256:aaaaaaaa  bob  enrolled 2026-01-15\nSHA256:bbbbbbbb  alice  enrolled 2026-01-15  revoked 2026-03-20\n"
		if got != want {
			t.Errorf("RenderClients(two clients) = %q, want %q", got, want)
		}
	})
}
