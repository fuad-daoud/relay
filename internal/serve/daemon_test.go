package serve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestRunFinishesInFlightTickWithoutCancel: a tick already in flight when ctx is
// cancelled runs to completion on a context cancellation cannot reach, and Run
// returns nil only after that tick returns. Tick cannot be held open through the
// real implementation, so this uses the unexported tickFn seam.
func TestRunFinishesInFlightTickWithoutCancel(t *testing.T) {
	srv, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Now: time.Now, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var mu sync.Mutex
	var sawCancelled bool

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv.tickFn = func(ctx context.Context) error {
		if ctx.Err() != nil {
			mu.Lock()
			sawCancelled = true
			mu.Unlock()
		}
		once.Do(func() { close(started) })
		<-release
		if ctx.Err() != nil {
			mu.Lock()
			sawCancelled = true
			mu.Unlock()
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first tick never started")
	}

	cancel()

	select {
	case err := <-done:
		t.Fatalf("Run returned %v while a tick was in flight, want it to wait", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the in-flight tick finished")
	}

	mu.Lock()
	defer mu.Unlock()
	if sawCancelled {
		t.Error("the tick saw a cancelled context, want the WithoutCancel one")
	}
}

// waitForRemoved polls until path is gone, up to five seconds.
func waitForRemoved(t *testing.T, path string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestRunImportsArchivedTarballsAtStartup: a tarball placed in an owner's
// .archive/ before Run is imported and removed at startup, and ListArchived
// answers with it. Run reaches its import step before the first tick, so no
// harness and no listener are touched.
func TestRunImportsArchivedTarballsAtStartup(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	srv, err := New(Config{DB: testServeDB(t), Root: root, Now: func() time.Time { return now }, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := srv.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), now); err != nil {
		t.Fatal(err)
	}
	ownerRoot, err := srv.ownerRoot(id)
	if err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(store.Binding{
		Name: "api", Owner: string(id), CWD: "/repo", Round: 1, State: store.StateDone,
	})
	if err != nil {
		t.Fatal(err)
	}
	tarball := filepath.Join(ownerRoot, ".archive", "api-20260101-000000.tar.gz")
	writeServeTarball(t, tarball, "api", map[string]string{"bind.json": string(body)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// The tarball leaving .archive/ is the import step finishing.
	if !waitForRemoved(t, tarball) {
		cancel()
		<-done
		t.Fatal("the startup import never removed the tarball")
	}

	archived, err := func() ([]store.ArchivedBinding, error) {
		// OwnerRuntime takes s.mu, the lock Run holds while it imports.
		rt, err := srv.OwnerRuntime(id)
		if err != nil {
			return nil, err
		}
		return rt.Store.ListArchived()
	}()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	found := false
	for _, rec := range archived {
		if rec.Binding.Name == "api" {
			found = true
		}
	}
	if !found {
		t.Errorf("archived records = %+v, want the imported api", archived)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
}
