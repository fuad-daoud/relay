package serve

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestRunFinishesInFlightTickWithoutCancel pins #373 §4.1 (and #371's
// Daemon.Run pattern): a tick already in flight when ctx is cancelled runs to
// completion on a context cancellation cannot reach, and Run returns nil only
// after that tick returns.
//
// Tick cannot be made to block through the real implementation without a large
// refactor, so this uses the unexported tickFn seam.
//
// Mutation check: drop context.WithoutCancel from Run's tick call and the tick
// sees a cancelled context, failing this test.
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

	// Run must not return while its tick is still in flight.
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
