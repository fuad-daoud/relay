package store

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newBinding(name, cwd string) Binding {
	return Binding{
		Name:           name,
		CWD:            cwd,
		Planner:        Endpoint{PaneID: "w2:p3", SessionID: "abc", Kind: "claude"},
		Builder:        Endpoint{AgentName: name + "-builder", PaneID: "w2:p4", Kind: "opencode"},
		BuilderAlias:   "builder",
		Round:          1,
		State:          StateActive,
		RoundCap:       20,
		RoundTimeoutMS: 1800000,
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	want := newBinding("upjo", "/home/fuad/projects/uniqueperfumesjo")

	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("upjo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CWD != want.CWD || got.Builder.AgentName != want.Builder.AgentName {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("Save must stamp CreatedAt and UpdatedAt")
	}
}

func TestSaveRefusesDuplicateCWD(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("upjo", "/repo")); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	err := s.Save(newBinding("upjo2", "/repo"))
	if !errors.Is(err, ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken", err)
	}
}

func TestSaveAllowsRewritingSameBinding(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("upjo", "/repo")
	if err := s.Save(b); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	b.Round = 2
	if err := s.Save(b); err != nil {
		t.Fatalf("rewriting the same name must not trip ErrCWDTaken: %v", err)
	}
}

func TestLoadMissingIsErrNotFound(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Load("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestPathsAreZeroPaddedUnderBindingDir(t *testing.T) {
	s := New("/state")
	if got, want := s.PlanPath("upjo", 3), filepath.Join("/state", "upjo", "003-plan.md"); got != want {
		t.Errorf("PlanPath = %q, want %q", got, want)
	}
	if got, want := s.ReportPath("upjo", 12), filepath.Join("/state", "upjo", "012-report.md"); got != want {
		t.Errorf("ReportPath = %q, want %q", got, want)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"upjo", "a", "money-ai", "x_1"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "1abc", "Upjo", "has space", "way-too-long-a-binding-name-for-herdr"} {
		if err := ValidName(bad); err == nil {
			t.Errorf("ValidName(%q) = nil, want error", bad)
		}
	}
}

func TestConcurrentSaveRaceRefusesDuplicateCWD(t *testing.T) {
	s := New(t.TempDir())
	cwd := "/repo"

	var successCount int32
	var wg sync.WaitGroup
	wg.Add(2)

	// First goroutine tries to save binding "b1"
	go func() {
		defer wg.Done()
		if err := s.Save(newBinding("b1", cwd)); err == nil {
			atomic.AddInt32(&successCount, 1)
		}
	}()

	// Second goroutine tries to save binding "b2" with same CWD
	go func() {
		defer wg.Done()
		if err := s.Save(newBinding("b2", cwd)); err == nil {
			atomic.AddInt32(&successCount, 1)
		}
	}()

	wg.Wait()

	if atomic.LoadInt32(&successCount) != 1 {
		t.Errorf("expected exactly 1 Save to succeed, got %d", atomic.LoadInt32(&successCount))
	}
}

func TestWithLockSerializesLoadModifySave(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("counter", "/repo")
	if err := s.Save(b); err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	// N goroutines each doing load-modify-save of the same binding
	n := 10
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			err := s.WithLock(func(tx *Tx) error {
				b, err := tx.Load("counter")
				if err != nil {
					return err
				}
				b.Round++
				return tx.Save(b)
			})
			if err != nil {
				t.Errorf("WithLock: %v", err)
			}
		}()
	}

	wg.Wait()

	// Final Round should be exactly n (no lost updates)
	final, err := s.Load("counter")
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	if final.Round != n+1 { // started at 1, incremented n times
		t.Errorf("Round = %d, want %d (lost updates detected)", final.Round, n+1)
	}
}

func TestNestedAccessDoesNotDeadlock(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("nested", "/repo")
	if err := s.Save(b); err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	// Run nested access in a goroutine with timeout guard
	done := make(chan error, 1)
	go func() {
		err := s.WithLock(func(tx *Tx) error {
			// Load inside the lock
			loaded, err := tx.Load("nested")
			if err != nil {
				return err
			}
			// Modify
			loaded.Round++
			// Save inside the lock (this would deadlock with the old design)
			return tx.Save(loaded)
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("nested access failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nested access deadlocked (timeout after 5s)")
	}

	// Verify the round-trip
	final, err := s.Load("nested")
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	if final.Round != 2 {
		t.Errorf("Round = %d, want 2", final.Round)
	}
}

func TestAtomicWriteCleanupTempFile(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("cleanup", "/repo")

	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Check that binding dir contains only bind.json, no temp files
	entries, err := os.ReadDir(s.Dir("cleanup"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	if len(entries) != 1 {
		t.Errorf("binding dir has %d entries, want 1", len(entries))
	}
	if entries[0].Name() != "bind.json" {
		t.Errorf("expected bind.json, got %q", entries[0].Name())
	}
}
