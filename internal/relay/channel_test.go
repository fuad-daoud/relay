package relay

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaimFileName(t *testing.T) {
	if got, want := ClaimFileName("wG:pQ"), "wG_pQ.json"; got != want {
		t.Errorf("ClaimFileName(%q) = %q, want %q", "wG:pQ", got, want)
	}
}

func alwaysAlive(int) bool { return true }
func neverAlive(int) bool  { return false }

func TestClaimLiveAbsent(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	c, err := f.Live("w2:p3", time.Now())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if c != nil {
		t.Fatalf("Live on an absent claim = %+v, want nil", c)
	}
}

func TestClaimLiveReturnsALiveClaim(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	now := time.Now()
	claim := Claim{Pane: "w2:p3", PID: 123, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := f.Live("w2:p3", now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got == nil || got.PID != 123 {
		t.Fatalf("Live = %+v, want the written claim", got)
	}
}

func TestClaimLiveRemovesStaleTTL(t *testing.T) {
	root := t.TempDir()
	f := &FileClaims{Root: root, Alive: alwaysAlive}
	start := time.Now()
	claim := Claim{Pane: "w2:p3", PID: 123, StartedAt: start, SeenAt: start}
	if err := f.Write(claim, start); err != nil {
		t.Fatalf("Write: %v", err)
	}

	later := start.Add(ClaimTTL + time.Second)
	got, err := f.Live("w2:p3", later)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live past TTL = %+v, want nil", got)
	}
	if _, statErr := os.Stat(filepath.Join(root, ClaimFileName("w2:p3"))); !os.IsNotExist(statErr) {
		t.Errorf("stale claim file must be removed, stat err = %v", statErr)
	}
}

// TestClaimLiveRemovesDeadPID is the plan's required case: a claim with a
// dead pid must read as not-live and the file must be gone afterward.
func TestClaimLiveRemovesDeadPID(t *testing.T) {
	root := t.TempDir()
	f := &FileClaims{Root: root, Alive: neverAlive}
	now := time.Now()
	claim := Claim{Pane: "w2:p3", PID: 999, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := f.Live("w2:p3", now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live on a dead pid = %+v, want nil", got)
	}
	if _, statErr := os.Stat(filepath.Join(root, ClaimFileName("w2:p3"))); !os.IsNotExist(statErr) {
		t.Errorf("a dead-pid claim file must be removed, stat err = %v", statErr)
	}
}

func TestClaimLiveRemovesUnparseable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ClaimFileName("w2:p3")), []byte("not json"), 0o644); err != nil {
		t.Fatalf("seed corrupt claim: %v", err)
	}
	f := &FileClaims{Root: root, Alive: alwaysAlive}

	got, err := f.Live("w2:p3", time.Now())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live on unparseable json = %+v, want nil", got)
	}
	if _, statErr := os.Stat(filepath.Join(root, ClaimFileName("w2:p3"))); !os.IsNotExist(statErr) {
		t.Errorf("an unparseable claim file must be removed, stat err = %v", statErr)
	}
}

func TestClaimLiveEmptyPane(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	if _, err := f.Live("", time.Now()); err == nil {
		t.Fatal("Live with an empty pane must error")
	}
}

func TestClaimWriteRefusesSecondLiveWriter(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	now := time.Now()
	first := Claim{Pane: "w2:p3", PID: 111, StartedAt: now, SeenAt: now}
	if err := f.Write(first, now); err != nil {
		t.Fatalf("Write first: %v", err)
	}

	second := Claim{Pane: "w2:p3", PID: 222, StartedAt: now, SeenAt: now}
	if err := f.Write(second, now); err == nil {
		t.Fatal("Write from a second pid over a live claim must be refused")
	} else if !errors.Is(err, ErrClaimHeld) {
		t.Errorf("Write error = %v, want ErrClaimHeld", err)
	}
}

func TestClaimWriteSameWriterRefreshes(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	now := time.Now()
	claim := Claim{Pane: "w2:p3", PID: 111, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write first: %v", err)
	}

	later := now.Add(time.Second)
	claim.SeenAt = later
	if err := f.Write(claim, later); err != nil {
		t.Fatalf("Write refresh from the same pid must succeed: %v", err)
	}

	got, err := f.Live("w2:p3", later)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got == nil || !got.SeenAt.Equal(later) {
		t.Fatalf("Live after refresh = %+v, want SeenAt %v", got, later)
	}
}

func TestClaimWriteOverwritesStaleClaim(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	start := time.Now()
	first := Claim{Pane: "w2:p3", PID: 111, StartedAt: start, SeenAt: start}
	if err := f.Write(first, start); err != nil {
		t.Fatalf("Write first: %v", err)
	}

	later := start.Add(ClaimTTL + time.Second)
	second := Claim{Pane: "w2:p3", PID: 222, StartedAt: later, SeenAt: later}
	if err := f.Write(second, later); err != nil {
		t.Fatalf("Write over a stale claim must succeed: %v", err)
	}

	got, err := f.Live("w2:p3", later)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got == nil || got.PID != 222 {
		t.Fatalf("Live after overwrite = %+v, want pid 222", got)
	}
}

func TestClaimRemoveOnlyMatchingPID(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	now := time.Now()
	claim := Claim{Pane: "w2:p3", PID: 111, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := f.Remove("w2:p3", 222); err != nil {
		t.Fatalf("Remove with a mismatched pid must not error: %v", err)
	}
	if got, err := f.Live("w2:p3", now); err != nil || got == nil {
		t.Fatalf("a mismatched-pid Remove must not remove the claim; Live = %+v, err = %v", got, err)
	}

	if err := f.Remove("w2:p3", 111); err != nil {
		t.Fatalf("Remove with a matching pid: %v", err)
	}
	if got, err := f.Live("w2:p3", now); err != nil || got != nil {
		t.Fatalf("a matching-pid Remove must remove the claim; Live = %+v, err = %v", got, err)
	}
}

func TestClaimRemoveAbsentIsNotAnError(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	if err := f.Remove("w2:p3", 111); err != nil {
		t.Fatalf("Remove on an absent claim must not error: %v", err)
	}
}
