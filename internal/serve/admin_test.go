package serve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/history"
	"github.com/fuad-daoud/relevo/internal/ledger"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

type aliveRunner struct{}

func (aliveRunner) Start(context.Context, relevo.ProcSpec) (relevo.ProcHandle, error) {
	return relevo.ProcHandle{}, nil
}

func (aliveRunner) Alive(context.Context, relevo.ProcHandle) (bool, error) { return true, nil }

func (aliveRunner) ExitCode(context.Context, relevo.ProcHandle, string) (int, bool) {
	return 0, false
}

func (aliveRunner) Kill(context.Context, relevo.ProcHandle) error { return nil }

func (aliveRunner) Rusage(context.Context, relevo.ProcHandle, string) (relevo.ProcRusage, bool) {
	return relevo.ProcRusage{}, false
}

// TestFlatStatusStampsOwnersAndDedupsGates: FlatStatus is the whole fleet
// as one report -- Owner/OwnerLabel stamped on every row, owners by label,
// Key() distinct across two owners that share a binding name, and the
// server-wide ledger gate appearing once, not once per owner.
func TestOwnerRuntimeMalformedID(t *testing.T) {
	s, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.OwnerRuntime(remote.ClientID("nope")); err == nil {
		t.Fatal("OwnerRuntime(\"nope\") error = nil, want a malformed-id error")
	}
}

func TestAdminStatusAllOwners(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	s, err := New(Config{
		DB:   testServeDB(t),
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
	statuses, builders, err := AdminStatus(ctx, s)
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

	rendered := RenderAdminStatus(statuses, builders)
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
	if got := RenderAdminStatus(nil, remote.BuildersView{}); got != "builders 0/0, queued 0\nno owners\n" {
		t.Errorf("RenderAdminStatus(nil) = %q, want %q", got, "builders 0/0, queued 0\nno owners\n")
	}

	// Owner with no bindings
	noBindingsStatus := []OwnerStatus{
		{Owner: idA, Label: "charlie", Report: relevo.Report{}},
	}
	if got := RenderAdminStatus(noBindingsStatus, remote.BuildersView{}); got != "builders 0/0, queued 0\ncharlie  no bindings\n" {
		t.Errorf("RenderAdminStatus(no bindings) = %q, want %q", got, "builders 0/0, queued 0\ncharlie  no bindings\n")
	}
}

func TestAdminStatusReportsHeadlessLivenessThroughRunner(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	d := testServeDB(t)
	newServer := func(r relevo.Runner) *Server {
		t.Helper()
		s, err := New(Config{DB: d, Root: root, Now: func() time.Time { return now }, Runner: r})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return s
	}

	s := newServer(aliveRunner{})
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
	if err := rt.Store.Save(store.Binding{
		Name:  "app",
		Owner: string(id),
		CWD:   rt.Store.WorktreePath("app"),
		State: store.StateActive,
		Round: 1,
		Builder: store.Endpoint{
			Kind: "claude", Mode: store.ModeHeadless, PID: 4242, LogPath: filepath.Join(root, "builder.log"),
		},
	}); err != nil {
		t.Fatal(err)
	}

	statuses, _, err := AdminStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("AdminStatus with runner: %v", err)
	}
	if got := statuses[0].Report.Bindings[0].BuilderStatus; got != "working" {
		t.Errorf("BuilderStatus with runner = %q, want working", got)
	}

	statuses, _, err = AdminStatus(context.Background(), newServer(nil))
	if err != nil {
		t.Fatalf("AdminStatus without runner: %v", err)
	}
	if got := statuses[0].Report.Bindings[0].BuilderStatus; got != "unknown" {
		t.Errorf("BuilderStatus without runner = %q, want unknown", got)
	}
}

// TestAdminStatusBuildersHeader pins #285's admin rendering: with cap 1, one
// running owner and one queued owner, AdminStatus's Builders return is
// {Running:1 Queued:1 Cap:1}, RenderAdminStatus's first line is "builders
// 1/1, queued 1", and the queued row's builder status reads "queued <age>
// (<ahead> ahead)" in place of "idle".
//
// Mutation check: drop the Builders return (or the row.BuilderStatus
// overwrite) in AdminStatus and this test fails.
func TestAdminStatusBuildersHeader(t *testing.T) {
	clock := time.Now()
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return clock }
	})
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	owners, builders, err := AdminStatus(context.Background(), env.srv)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	if builders.Running != 1 || builders.Queued != 1 || builders.Cap != 1 {
		t.Errorf("Builders = %+v, want {Running:1 Queued:1 Cap:1 ...}", builders)
	}

	rendered := RenderAdminStatus(owners, builders)
	if !strings.HasPrefix(rendered, "builders 1/1, queued 1\n") {
		t.Fatalf("rendered does not start with the builders header; got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "queued 0s (0 ahead)") {
		t.Errorf("rendered missing the queued row's builder status; got:\n%s", rendered)
	}
}

func TestGCAbandonedArchivesOnlyIdleOld(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	s, err := New(Config{
		DB:   testServeDB(t),
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
	if dryResults[0].Archive {
		t.Errorf("dry run Archive = true, want false")
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
	if !results[0].Archive {
		t.Errorf("result Archive is false, want the binding archived")
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
		DB:   testServeDB(t),
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

// TestAdminOwnerRuntimeAndTabEntries covers the two server-side read
// helpers (#216), reusing TestAdminUnbindByLabelAndId's setup shape: two
// enrolled owners, each with one binding holding one report entry, plus the
// two refusal cases -- a shared label, and an owner with no bindings
// directory at all.
func TestAdminOwnerRuntimeAndTabEntries(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	s, err := New(Config{DB: testServeDB(t), Root: root, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	alice, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	idA := remote.IDOf(alice.Public)
	if _, err := s.clients.Add("alice", remote.MarshalPublic(alice.Public, "alice"), now); err != nil {
		t.Fatal(err)
	}

	bob, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	idB := remote.IDOf(bob.Public)
	if _, err := s.clients.Add("bob", remote.MarshalPublic(bob.Public, "bob"), now); err != nil {
		t.Fatal(err)
	}

	// Two clients sharing one label, for the ambiguity case.
	dup1, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	idDup1 := remote.IDOf(dup1.Public)
	if _, err := s.clients.Add("dup", remote.MarshalPublic(dup1.Public, "dup"), now); err != nil {
		t.Fatal(err)
	}
	dup2, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	idDup2 := remote.IDOf(dup2.Public)
	if _, err := s.clients.Add("dup", remote.MarshalPublic(dup2.Public, "dup"), now); err != nil {
		t.Fatal(err)
	}

	// Enrolled, but has never bound anything: no bindings directory.
	carol, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	idCarol := remote.IDOf(carol.Public)
	if _, err := s.clients.Add("carol", remote.MarshalPublic(carol.Public, "carol"), now); err != nil {
		t.Fatal(err)
	}

	for _, o := range []struct {
		id   remote.ClientID
		name string
	}{{idA, "api"}, {idB, "web"}} {
		rt, err := s.OwnerRuntime(o.id)
		if err != nil {
			t.Fatalf("OwnerRuntime: %v", err)
		}
		if err := rt.Store.Save(store.Binding{
			Name: o.name, Owner: string(o.id), CWD: rt.Store.WorktreePath(o.name),
			State: store.StateActive, Round: 1, Builder: store.Endpoint{Kind: "claude"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := rt.Store.AppendLog(o.name, store.LogEntry{
			Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Resolves by label and by id to the same owner and label.
	byLabel, label, err := AdminOwnerRuntime(s, "alice")
	if err != nil {
		t.Fatalf("AdminOwnerRuntime(alice): %v", err)
	}
	if label != "alice" {
		t.Errorf("label = %q, want alice", label)
	}
	byID, labelByID, err := AdminOwnerRuntime(s, string(idA))
	if err != nil {
		t.Fatalf("AdminOwnerRuntime(id): %v", err)
	}
	if labelByID != "alice" {
		t.Errorf("label by id = %q, want alice", labelByID)
	}
	if byLabel.Store.Dir("api") != byID.Store.Dir("api") {
		t.Errorf("by label resolved to %q, by id to %q", byLabel.Store.Dir("api"), byID.Store.Dir("api"))
	}

	if _, _, err := AdminOwnerRuntime(s, "dup"); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") ||
		!strings.Contains(err.Error(), string(idDup1)) || !strings.Contains(err.Error(), string(idDup2)) {
		t.Errorf("ambiguous label err = %v, want one naming %s and %s", err, idDup1, idDup2)
	}

	if _, _, err := AdminOwnerRuntime(s, "nobody"); !errors.Is(err, ErrNoSuchClient) {
		t.Errorf("unknown owner err = %v, want ErrNoSuchClient", err)
	}

	// An enrolled owner with no bindings directory: ErrNotFound, and the
	// directory must still not exist afterwards.
	carolRoot, err := s.ownerRoot(idCarol)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := AdminOwnerRuntime(s, "carol"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("owner with no bindings dir err = %v, want store.ErrNotFound", err)
	}
	if _, statErr := os.Stat(carolRoot); !os.IsNotExist(statErr) {
		t.Errorf("AdminOwnerRuntime created %s (stat err %v)", carolRoot, statErr)
	}

	// Without an owner: every owner's entries, labelled "label/name".
	entries, err := AdminTabEntries(s, "", time.Time{}, func(string) {})
	if err != nil {
		t.Fatalf("AdminTabEntries(all): %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("every-owner entries = %d, want 2", len(entries))
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.Binding] = e.Owner
	}
	if got["alice/api"] != "alice" || got["bob/web"] != "bob" {
		t.Errorf("every-owner entries = %+v, want label/name bindings with Owner set", got)
	}

	// With an owner: bare names for that owner only.
	entries, err = AdminTabEntries(s, "alice", time.Time{}, func(string) {})
	if err != nil {
		t.Fatalf("AdminTabEntries(alice): %v", err)
	}
	if len(entries) != 1 || entries[0].Binding != "api" || entries[0].Owner != "alice" {
		t.Errorf("alice entries = %+v, want one bare \"api\" owned by alice", entries)
	}
}

func TestAdminUnbindRefusesRunningUnlessForce(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	s, err := New(Config{
		DB:   testServeDB(t),
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
		DB:   testServeDB(t),
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

// TestAdminGatesAvailableUnavailable: the server-side gate verbs read and
// write the one server-wide ledger. AdminGates on an empty ledger is empty and
// RenderGates says so; AdminUnavailable records a gate RenderGates names; and
// AdminAvailable lifts it. The lock store the ledger mutates through must not
// make an uninitialised root look initialised.
func TestAdminGatesAvailableUnavailable(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	candPath := filepath.Join(root, "candidates.json")
	if err := os.WriteFile(candPath, []byte(`[{"harness":"claude","provider":"t","model":"m","roles":["builder"]}]`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	candidates, err := candidate.Load(candPath)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}

	s, err := New(Config{DB: testServeDB(t), Root: root, Candidates: candidates, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	initialisedBefore, err := Initialised(root, s.DB())
	if err != nil {
		t.Fatalf("Initialised: %v", err)
	}

	if gates := AdminGates(s); len(gates) != 0 {
		t.Fatalf("AdminGates = %v, want none on an empty ledger", gates)
	}
	if out := RenderGates(AdminGates(s), now); out != "no gates\n" {
		t.Errorf("RenderGates(empty) = %q, want %q", out, "no gates\n")
	}

	provider, err := AdminUnavailable(s, "claude/t/m", time.Time{}, "quota")
	if err != nil {
		t.Fatalf("AdminUnavailable: %v", err)
	}
	if provider != "t" {
		t.Errorf("AdminUnavailable provider = %q, want t", provider)
	}

	gates := AdminGates(s)
	if len(gates) != 1 {
		t.Fatalf("AdminGates = %v, want one gate", gates)
	}
	if gates[0].Kind != ledger.RateLimited {
		t.Errorf("gate kind = %q, want %q", gates[0].Kind, ledger.RateLimited)
	}
	out := RenderGates(gates, now)
	if !strings.Contains(out, "claude/t/m") {
		t.Errorf("RenderGates = %q, want it naming claude/t/m", out)
	}
	if !strings.Contains(out, "quota") {
		t.Errorf("RenderGates = %q, want it naming quota", out)
	}

	provider, removed, err := AdminAvailable(s, "t")
	if err != nil {
		t.Fatalf("AdminAvailable: %v", err)
	}
	if provider != "t" || removed != 1 {
		t.Errorf("AdminAvailable = %q, %d, want t, 1", provider, removed)
	}

	if gates := AdminGates(s); len(gates) != 0 {
		t.Errorf("AdminGates = %v, want none after AdminAvailable", gates)
	}

	initialisedAfter, err := Initialised(root, s.DB())
	if err != nil {
		t.Fatalf("Initialised: %v", err)
	}
	if initialisedAfter != initialisedBefore {
		t.Errorf("Initialised(root) = %v after the admin calls, want %v: the lock store must not fake an init", initialisedAfter, initialisedBefore)
	}
}

// TestAdminAvailableRecordsServerClear: the server host's own clear is an
// observation too (#302), and it is recorded as the server's, not a
// planner's -- `relevo serve available` is not a forwarded client verb.
func TestAdminAvailableRecordsServerClear(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	candPath := filepath.Join(root, "candidates.json")
	if err := os.WriteFile(candPath, []byte(`[{"harness":"claude","provider":"t","model":"m","roles":["builder"]}]`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	candidates, err := candidate.Load(candPath)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}

	s, err := New(Config{DB: testServeDB(t), Root: root, Candidates: candidates, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := AdminUnavailable(s, "claude/t/m", time.Time{}, "quota"); err != nil {
		t.Fatalf("AdminUnavailable: %v", err)
	}
	if _, removed, err := AdminAvailable(s, "t"); err != nil {
		t.Fatalf("AdminAvailable: %v", err)
	} else if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	h, err := history.LoadKV(db.PrefixKV{KV: s.DB(), Prefix: "serve."}, "")
	if err != nil {
		t.Fatalf("history.LoadKV: %v", err)
	}
	if len(h.Events) == 0 {
		t.Fatal("the availability row has no events, want a Cleared event")
	}
	ev := h.Events[len(h.Events)-1]
	if ev.Kind != history.Cleared {
		t.Errorf("kind = %q, want %q", ev.Kind, history.Cleared)
	}
	if ev.Source != relevo.ClearedByServer {
		t.Errorf("source = %q, want %q", ev.Source, relevo.ClearedByServer)
	}
	if ev.Provider != "t" {
		t.Errorf("provider = %q, want t", ev.Provider)
	}
}

// TestFlatStatusStampsOwnersAndDedupsGates: FlatStatus is the whole fleet
// as one report -- Owner/OwnerLabel stamped on every row, owners by label,
// Key() distinct across two owners that share a binding name, and the
// server-wide ledger gate appearing once, not once per owner.
func TestFlatStatusStampsOwnersAndDedupsGates(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	candPath := filepath.Join(root, "candidates.json")
	if err := os.WriteFile(candPath, []byte(`[{"harness":"claude","provider":"t","model":"m","roles":["builder"]}]`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	candidates, err := candidate.Load(candPath)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}

	s, err := New(Config{DB: testServeDB(t), Root: root, Candidates: candidates, Now: func() time.Time { return now }})
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
	if err := rtA.Store.Save(store.Binding{
		Name: "persist", Owner: string(idA), CWD: rtA.Store.WorktreePath("persist"),
		State: store.StateActive, Round: 1, Builder: store.Endpoint{Kind: "claude"},
	}); err != nil {
		t.Fatal(err)
	}
	rtB, err := s.runtime(idB)
	if err != nil {
		t.Fatal(err)
	}
	if err := rtB.Store.Save(store.Binding{
		Name: "persist", Owner: string(idB), CWD: rtB.Store.WorktreePath("persist"),
		State: store.StateActive, Round: 1, Builder: store.Endpoint{Kind: "claude"},
	}); err != nil {
		t.Fatal(err)
	}

	// One ledger gate through the server's (server-wide) ledger path.
	if _, err := relevo.Unavailable(rtA, "claude/t/m", now.Add(time.Hour), "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	out, err := FlatStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("FlatStatus: %v", err)
	}

	if len(out.Bindings) != 2 {
		t.Fatalf("got %d rows, want 2", len(out.Bindings))
	}
	if out.Bindings[0].Owner != string(idA) || out.Bindings[0].OwnerLabel != "alice" || out.Bindings[0].Name != "persist" {
		t.Errorf("row 0 = %+v", out.Bindings[0])
	}
	if out.Bindings[1].Owner != string(idB) || out.Bindings[1].OwnerLabel != "bob" || out.Bindings[1].Name != "persist" {
		t.Errorf("row 1 = %+v", out.Bindings[1])
	}
	if out.Bindings[0].Key() == out.Bindings[1].Key() {
		t.Errorf("Key() must separate two owners' same-named bindings: %q", out.Bindings[0].Key())
	}
	if len(out.Gated) != 1 {
		t.Errorf("len(out.Gated) = %d, want 1 (one ledger gate, not one per owner)", len(out.Gated))
	}
	if out.DoneHidden != 0 {
		t.Errorf("DoneHidden = %d, want 0", out.DoneHidden)
	}
}

// TestStatusDocumentLastContact: last_contact is the max LastSeen over every
// owner; an owner that has never seen a request marshals last_seen as JSON
// null; and the owners array keeps the label order AdminStatus hands over.
//
// Mutation check: take the min instead of the max and this fails.
func TestStatusDocumentLastContact(t *testing.T) {
	t1 := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Hour)

	owners := []OwnerStatus{
		{Owner: remote.ClientID("SHA256:alice"), Label: "alice"},
		{Owner: remote.ClientID("SHA256:bob"), Label: "bob", LastSeen: t1},
		{Owner: remote.ClientID("SHA256:carol"), Label: "carol", LastSeen: t2},
	}

	doc := StatusDocument(owners, remote.BuildersView{Running: 1, Cap: 2})
	if doc.LastContact == nil {
		t.Fatal("LastContact = nil, want the newest owner LastSeen")
	}
	if !doc.LastContact.Equal(t2) {
		t.Errorf("LastContact = %v, want %v", doc.LastContact, t2)
	}
	if len(doc.Owners) != 3 {
		t.Fatalf("got %d owners, want 3", len(doc.Owners))
	}
	for i, want := range []string{"alice", "bob", "carol"} {
		if doc.Owners[i].Label != want {
			t.Errorf("owners[%d].Label = %q, want %q: owners stay label-sorted", i, doc.Owners[i].Label, want)
		}
	}
	if doc.Owners[0].Owner != "SHA256:alice" {
		t.Errorf("owners[0].Owner = %q, want SHA256:alice", doc.Owners[0].Owner)
	}
	if doc.Owners[0].LastSeen != nil {
		t.Errorf("owners[0].LastSeen = %v, want nil for the owner with no contact", doc.Owners[0].LastSeen)
	}
	if doc.Owners[1].LastSeen == nil || !doc.Owners[1].LastSeen.Equal(t1) {
		t.Errorf("owners[1].LastSeen = %v, want %v", doc.Owners[1].LastSeen, t1)
	}
	if doc.Owners[2].LastSeen == nil || !doc.Owners[2].LastSeen.Equal(t2) {
		t.Errorf("owners[2].LastSeen = %v, want %v", doc.Owners[2].LastSeen, t2)
	}

	blob, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"last_seen":null`) {
		t.Errorf("marshalled document has no \"last_seen\":null for the zero owner:\n%s", blob)
	}
	if !strings.Contains(string(blob), `"last_contact":"2026-09-20T12:00:00Z"`) {
		t.Errorf("marshalled last_contact is not the max, as RFC 3339 UTC:\n%s", blob)
	}
}

// TestStatusDocumentEmpty: no owners still prints the three top-level keys,
// with last_contact null and owners an empty array -- [] and never null.
func TestStatusDocumentEmpty(t *testing.T) {
	blob, err := json.Marshal(StatusDocument(nil, remote.BuildersView{}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"builders":{"running":0,"queued":0,"cap":0,"scopes":false},"last_contact":null,"owners":[]}`
	if string(blob) != want {
		t.Errorf("StatusDocument(nil) JSON = %s, want %s", blob, want)
	}
}

// TestAdminStatusLastSeen: an owner's LastSeen is the newest Serve.LastSeen
// over its bindings. RoundStartedAt is no fallback -- an owner whose only
// binding was last touched by a round start has no recorded contact at all.
func TestAdminStatusLastSeen(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

	s, err := New(Config{DB: testServeDB(t), Root: root, Now: func() time.Time { return now }})
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
	rtA, err := s.runtime(idA)
	if err != nil {
		t.Fatal(err)
	}
	seen := now.Add(-3 * time.Hour)
	if err := rtA.Store.Save(store.Binding{
		Name:    "app-a",
		Owner:   string(idA),
		CWD:     rtA.Store.WorktreePath("app-a"),
		State:   store.StateActive,
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
		Serve:   &store.ServeFacts{LastSeen: seen},
	}); err != nil {
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
	rtB, err := s.runtime(idB)
	if err != nil {
		t.Fatal(err)
	}
	if err := rtB.Store.Save(store.Binding{
		Name:           "app-b",
		Owner:          string(idB),
		CWD:            rtB.Store.WorktreePath("app-b"),
		State:          store.StateActive,
		Round:          1,
		Builder:        store.Endpoint{Kind: "claude"},
		RoundStartedAt: now.Add(-9 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	owners, _, err := AdminStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	if len(owners) != 2 || owners[0].Label != "alice" || owners[1].Label != "bob" {
		t.Fatalf("owners = %+v, want alice then bob", owners)
	}
	if !owners[0].LastSeen.Equal(seen) {
		t.Errorf("alice LastSeen = %v, want the binding's Serve.LastSeen %v", owners[0].LastSeen, seen)
	}
	if !owners[1].LastSeen.IsZero() {
		t.Errorf("bob LastSeen = %v, want the zero time: RoundStartedAt is no contact", owners[1].LastSeen)
	}

	doc := StatusDocument(owners, remote.BuildersView{})
	if doc.LastContact == nil || !doc.LastContact.Equal(seen) {
		t.Errorf("LastContact = %v, want %v", doc.LastContact, seen)
	}
}
