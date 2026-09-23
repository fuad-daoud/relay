package planner

import (
	"errors"
	"testing"
)

// TestStateLiveGoneExplicit is §4.7's state column: live when the record's
// host process (pid and start time together) is still there, gone when it is
// not, and "-" for an explicit registration (host_pid == 0). The ProcStart
// read is injected, so no test touches a real process.
func TestStateLiveGoneExplicit(t *testing.T) {
	live := record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-a", 101)
	gone := record("pl_bbbbbbbbbbbb", "beta", "claude", "sess-b", 202)
	explicit := record("pl_cccccccccccc", "gamma", "opencode", "ses_g", 0)

	// Only alpha's exact process is alive; anything else reads as gone.
	procStart := func(pid int) (int64, error) {
		if pid == live.HostPID {
			return live.HostStartedAt, nil
		}
		return 0, errors.New("ps: no such process")
	}

	tests := []struct {
		name string
		rec  Record
		proc func(int) (int64, error)
		want State
	}{
		{"live: pid and start time match", live, procStart, StateLive},
		{"gone: process absent", gone, procStart, StateGone},
		{"explicit: no host to check", explicit, procStart, StateExplicit},
		{"gone: pid reused, start time differs", live, procStartAt(live.HostStartedAt + 1), StateGone},
		{"gone: no ProcStart reader", live, nil, StateGone},
		{"gone: ProcStart error", live, procStartFails(), StateGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RecordState(tt.rec, tt.proc); got != tt.want {
				t.Errorf("RecordState(%s) = %q, want %q", tt.rec.Name, got, tt.want)
			}
		})
	}
}

// TestPruneForgetsOnlyGoneWithoutBindings is §4.7's prune rule: a gone record
// with no non-DONE binding is forgotten, a live record survives, a gone record
// a non-DONE binding names survives, and an explicit registration is never a
// candidate. --dry-run lists the candidate and forgets nothing.
func TestPruneForgetsOnlyGoneWithoutBindings(t *testing.T) {
	reg := testRegistry(t)

	live := mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-a", 101))
	named := mustCreate(t, reg, record("pl_bbbbbbbbbbbb", "beta", "claude", "sess-b", 202))
	stale := mustCreate(t, reg, record("pl_cccccccccccc", "gamma", "claude", "sess-c", 303))
	explicit := mustCreate(t, reg, record("pl_dddddddddddd", "delta", "opencode", "ses_d", 0))

	// Only alpha's exact process is alive.
	procStart := func(pid int) (int64, error) {
		if pid == live.HostPID {
			return live.HostStartedAt, nil
		}
		return 0, errors.New("ps: no such process")
	}
	// beta's gone host is still named by a binding that is not DONE.
	bindings := func(id string) int {
		if id == named.ID {
			return 1
		}
		return 0
	}

	// --dry-run lists the one candidate and forgets nothing.
	planned, err := Prune(reg, procStart, bindings, true)
	if err != nil {
		t.Fatalf("Prune(dry-run): %v", err)
	}
	if len(planned) != 1 || planned[0].ID != stale.ID {
		t.Fatalf("Prune(dry-run) planned %d records (%v), want just %s", len(planned), planned, stale.ID)
	}
	if _, err := reg.Get(stale.ID); err != nil {
		t.Fatalf("dry-run forgot %s: %v", stale.ID, err)
	}

	// The real run forgets exactly the gone record no binding names.
	forgotten, err := Prune(reg, procStart, bindings, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(forgotten) != 1 || forgotten[0].ID != stale.ID {
		t.Fatalf("Prune forgot %d records (%v), want just %s", len(forgotten), forgotten, stale.ID)
	}

	// Live, named and explicit records are untouched; the stale one is gone.
	for _, rec := range []Record{live, named, explicit} {
		if _, err := reg.Get(rec.ID); err != nil {
			t.Errorf("%s (%s) was forgotten: %v", rec.Name, rec.ID, err)
		}
	}
	if _, err := reg.Get(stale.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("gone record %s is still present: err = %v", stale.ID, err)
	}
}
