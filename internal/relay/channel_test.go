package relay

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testClaimPlanner is a valid planner id (pl_ plus 12 characters of [a-z2-7]),
// the shape ClaimFileName, Live, Write and Remove all key on.
const testClaimPlanner = "pl_aaaaaaaabbbb"

// otherClaimPlanner is a second valid id, for the "a different planner's
// claim is not this one's" cases.
const otherClaimPlanner = "pl_ccccccccdddd"

// paneClaimFileName is a pre-#303 claim file's name: the pane id with ":" as
// "_", which is what the old ClaimFileName produced.
const paneClaimFileName = "wG_pQ.json"

func TestClaimFileName(t *testing.T) {
	if got, want := ClaimFileName(testClaimPlanner), testClaimPlanner+".json"; got != want {
		t.Errorf("ClaimFileName(%q) = %q, want %q", testClaimPlanner, got, want)
	}
}

func alwaysAlive(int) bool { return true }
func neverAlive(int) bool  { return false }

func TestClaimLiveAbsent(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	c, err := f.Live(testClaimPlanner, time.Now())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if c != nil {
		t.Fatalf("Live on an absent claim = %+v, want nil", c)
	}
}

// TestClaimKeyedByPlannerID is the plan's required case (§3.3): a claim is
// written to channels/<planner-id>.json, is found by that id, and is not
// found by another planner's id.
func TestClaimKeyedByPlannerID(t *testing.T) {
	root := t.TempDir()
	f := &FileClaims{Root: root, Alive: alwaysAlive}
	now := time.Now()

	claim := Claim{Planner: testClaimPlanner, PID: 123, HostPID: 99, HostStartedAt: 42, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, testClaimPlanner+".json")); err != nil {
		t.Fatalf("the claim must live at channels/<planner-id>.json: %v", err)
	}

	got, err := f.Live(testClaimPlanner, now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got == nil || got.PID != 123 {
		t.Fatalf("Live = %+v, want the written claim", got)
	}
	if got.HostPID != 99 || got.HostStartedAt != 42 {
		t.Errorf("host fields = %d@%d, want 99@42", got.HostPID, got.HostStartedAt)
	}

	other, err := f.Live(otherClaimPlanner, now)
	if err != nil {
		t.Fatalf("Live for another planner: %v", err)
	}
	if other != nil {
		t.Fatalf("another planner's id found a claim: %+v", other)
	}
}

// TestClaimLiveIgnoresPaneKeyedFile: a pre-#303 pane-keyed file is not a
// claim this version wrote. Live must ignore it (it would otherwise have to
// answer a question about a pane it no longer has) and leave it on disk for
// the sweep, which is the only thing allowed to remove it.
func TestClaimLiveIgnoresPaneKeyedFile(t *testing.T) {
	root := t.TempDir()
	f := &FileClaims{Root: root, Alive: alwaysAlive}
	now := time.Now()

	// A live, current pane-keyed claim: exactly what an older relay mcp
	// still running during the upgrade has on disk.
	raw := []byte(`{"pane":"wG:pQ","pid":4242,"seen_at":"` + now.Format(time.RFC3339Nano) + `"}`)
	if err := os.WriteFile(filepath.Join(root, paneClaimFileName), raw, 0o644); err != nil {
		t.Fatalf("seed pane-keyed claim: %v", err)
	}

	// Live never asks about a pane name, and asking about the pane name must
	// not remove the file either.
	got, err := f.Live("wG:pQ", now)
	if err != nil {
		t.Fatalf("Live(pane): %v", err)
	}
	if got != nil {
		t.Fatalf("Live(pane name) = %+v, want nil", got)
	}
	if _, err := os.Stat(filepath.Join(root, paneClaimFileName)); err != nil {
		t.Fatalf("Live must leave a pane-keyed file alone: %v", err)
	}
}

// TestMCPStartRemovesOnlyDeadPaneKeyedClaims is the plan's required case for
// the sweep rule: a pane-keyed file whose pid is dead goes, a live one stays,
// a planner-keyed file is never swept, and bytes with no pid are left alone.
func TestMCPStartRemovesOnlyDeadPaneKeyedClaims(t *testing.T) {
	root := t.TempDir()
	f := &FileClaims{Root: root, Alive: func(pid int) bool { return pid == 4242 }}

	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	write("wG_pQ.json", `{"pane":"wG:pQ","pid":111,"seen_at":"2026-01-01T00:00:00Z"}`)  // dead
	write("w9_p1.json", `{"pane":"w9:p1","pid":4242,"seen_at":"2026-01-01T00:00:00Z"}`) // live
	write(ClaimFileName(testClaimPlanner), `{"planner":"`+testClaimPlanner+`","pid":111,"seen_at":"2026-01-01T00:00:00Z"}`)
	write("junk.json", "not json")

	if removed := f.SweepPaneKeyed(); removed != 1 {
		t.Fatalf("SweepPaneKeyed removed %d files, want 1", removed)
	}

	if _, err := os.Stat(filepath.Join(root, "wG_pQ.json")); !os.IsNotExist(err) {
		t.Errorf("a dead pane-keyed claim must be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "w9_p1.json")); err != nil {
		t.Errorf("a live pane-keyed claim must survive the sweep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ClaimFileName(testClaimPlanner))); err != nil {
		t.Errorf("a planner-keyed claim must survive the sweep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "junk.json")); err != nil {
		t.Errorf("an unparseable pane-keyed file has no dead pid to prove, so it stays: %v", err)
	}
}

func TestPaneKeyedClaimDead(t *testing.T) {
	cases := []struct {
		label string
		raw   string
		want  bool
	}{
		{"dead pid", `{"pane":"wG:pQ","pid":111}`, true},
		{"live pid", `{"pane":"wG:pQ","pid":4242}`, false},
		{"no pid", `{"pane":"wG:pQ"}`, false},
		{"zero pid", `{"pane":"wG:pQ","pid":0}`, false},
		{"unparseable", `not json`, false},
	}
	for _, c := range cases {
		if got := paneKeyedClaimDead([]byte(c.raw), func(pid int) bool { return pid == 4242 }); got != c.want {
			t.Errorf("%s: paneKeyedClaimDead = %v, want %v", c.label, got, c.want)
		}
	}
}

func TestClaimLiveRemovesStaleTTL(t *testing.T) {
	root := t.TempDir()
	f := &FileClaims{Root: root, Alive: alwaysAlive}
	start := time.Now()
	claim := Claim{Planner: testClaimPlanner, PID: 123, StartedAt: start, SeenAt: start}
	if err := f.Write(claim, start); err != nil {
		t.Fatalf("Write: %v", err)
	}

	later := start.Add(ClaimTTL + time.Second)
	got, err := f.Live(testClaimPlanner, later)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live past TTL = %+v, want nil", got)
	}
	if _, statErr := os.Stat(filepath.Join(root, ClaimFileName(testClaimPlanner))); !os.IsNotExist(statErr) {
		t.Errorf("stale claim file must be removed, stat err = %v", statErr)
	}
}

// TestClaimLiveRemovesDeadPID is the plan's required case: a claim with a
// dead pid must read as not-live and the file must be gone afterward.
func TestClaimLiveRemovesDeadPID(t *testing.T) {
	root := t.TempDir()
	f := &FileClaims{Root: root, Alive: neverAlive}
	now := time.Now()
	claim := Claim{Planner: testClaimPlanner, PID: 999, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := f.Live(testClaimPlanner, now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live on a dead pid = %+v, want nil", got)
	}
	if _, statErr := os.Stat(filepath.Join(root, ClaimFileName(testClaimPlanner))); !os.IsNotExist(statErr) {
		t.Errorf("a dead-pid claim file must be removed, stat err = %v", statErr)
	}
}

func TestClaimLiveRemovesUnparseable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ClaimFileName(testClaimPlanner)), []byte("not json"), 0o644); err != nil {
		t.Fatalf("seed corrupt claim: %v", err)
	}
	f := &FileClaims{Root: root, Alive: alwaysAlive}

	got, err := f.Live(testClaimPlanner, time.Now())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live on unparseable json = %+v, want nil", got)
	}
	if _, statErr := os.Stat(filepath.Join(root, ClaimFileName(testClaimPlanner))); !os.IsNotExist(statErr) {
		t.Errorf("an unparseable claim file must be removed, stat err = %v", statErr)
	}
}

func TestClaimLiveEmptyPlanner(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	if _, err := f.Live("", time.Now()); err == nil {
		t.Fatal("Live with an empty planner must error")
	}
}

func TestClaimWriteRefusesSecondLiveWriter(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	now := time.Now()
	first := Claim{Planner: testClaimPlanner, PID: 111, StartedAt: now, SeenAt: now}
	if err := f.Write(first, now); err != nil {
		t.Fatalf("Write first: %v", err)
	}

	second := Claim{Planner: testClaimPlanner, PID: 222, StartedAt: now, SeenAt: now}
	if err := f.Write(second, now); err == nil {
		t.Fatal("Write from a second pid over a live claim must be refused")
	} else if !errors.Is(err, ErrClaimHeld) {
		t.Errorf("Write error = %v, want ErrClaimHeld", err)
	}
}

// TestClaimWriteRefusesANonPlannerID: the old pane-keyed shape must never be
// written again, so a claim whose key is not a planner id is refused rather
// than silently creating a file the sweep would later reap.
func TestClaimWriteRefusesANonPlannerID(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	now := time.Now()
	err := f.Write(Claim{Planner: "wG:pQ", PID: 111, StartedAt: now, SeenAt: now}, now)
	if err == nil {
		t.Fatal("Write with a non-planner key must be refused")
	}
	if _, statErr := os.Stat(filepath.Join(f.Root, "wG:pQ.json")); !os.IsNotExist(statErr) {
		t.Errorf("a refused Write must leave no file behind, stat err = %v", statErr)
	}
}

func TestClaimWriteSameWriterRefreshes(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	now := time.Now()
	claim := Claim{Planner: testClaimPlanner, PID: 111, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write first: %v", err)
	}

	later := now.Add(time.Second)
	claim.SeenAt = later
	if err := f.Write(claim, later); err != nil {
		t.Fatalf("Write refresh from the same pid must succeed: %v", err)
	}

	got, err := f.Live(testClaimPlanner, later)
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
	first := Claim{Planner: testClaimPlanner, PID: 111, StartedAt: start, SeenAt: start}
	if err := f.Write(first, start); err != nil {
		t.Fatalf("Write first: %v", err)
	}

	later := start.Add(ClaimTTL + time.Second)
	second := Claim{Planner: testClaimPlanner, PID: 222, StartedAt: later, SeenAt: later}
	if err := f.Write(second, later); err != nil {
		t.Fatalf("Write over a stale claim must succeed: %v", err)
	}

	got, err := f.Live(testClaimPlanner, later)
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
	claim := Claim{Planner: testClaimPlanner, PID: 111, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := f.Remove(testClaimPlanner, 222); err != nil {
		t.Fatalf("Remove with a mismatched pid must not error: %v", err)
	}
	if got, err := f.Live(testClaimPlanner, now); err != nil || got == nil {
		t.Fatalf("a mismatched-pid Remove must not remove the claim; Live = %+v, err = %v", got, err)
	}

	if err := f.Remove(testClaimPlanner, 111); err != nil {
		t.Fatalf("Remove with a matching pid: %v", err)
	}
	if got, err := f.Live(testClaimPlanner, now); err != nil || got != nil {
		t.Fatalf("a matching-pid Remove must remove the claim; Live = %+v, err = %v", got, err)
	}
}

func TestClaimRemoveAbsentIsNotAnError(t *testing.T) {
	f := &FileClaims{Root: t.TempDir(), Alive: alwaysAlive}
	if err := f.Remove(testClaimPlanner, 111); err != nil {
		t.Fatalf("Remove on an absent claim must not error: %v", err)
	}
}
