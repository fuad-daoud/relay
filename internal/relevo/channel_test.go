package relevo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// testClaimPlanner is a valid planner id (pl_ plus 12 characters of [a-z2-7]),
// the shape Live, Write and Remove all key on.
const testClaimPlanner = "pl_aaaaaaaabbbb"

// otherClaimPlanner is a second valid id, for the "a different planner's
// claim is not this one's" cases.
const otherClaimPlanner = "pl_ccccccccdddd"

// paneClaimID is a pre-#303 claim's key: the pane id with ":" as "_", which is
// what the old pane-keyed claim file name held.
const paneClaimID = "wG_pQ"

// claimRow reads one claim row back, reporting whether it is there.
func claimRow(t *testing.T, d *db.DB, plannerID string) (Claim, bool) {
	t.Helper()
	raw, ok, err := d.KVGet(claimKey(plannerID))
	if err != nil {
		t.Fatalf("KVGet(%s): %v", claimKey(plannerID), err)
	}
	if !ok {
		return Claim{}, false
	}
	var c Claim
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("decode %s: %v", claimKey(plannerID), err)
	}
	return c, true
}

// seedClaim writes one claim row the way an older relevo would have, so a test
// can pin what Live, Remove and the sweep do with it.
func seedClaim(t *testing.T, d *db.DB, plannerID string, c Claim) {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal claim: %v", err)
	}
	if err := d.KVPut(claimKey(plannerID), raw); err != nil {
		t.Fatalf("KVPut(%s): %v", claimKey(plannerID), err)
	}
}

func alwaysAlive(int) bool { return true }
func neverAlive(int) bool  { return false }

func TestClaimLiveAbsent(t *testing.T) {
	f, _, _ := testClaims(t)
	c, err := f.Live(testClaimPlanner, time.Now())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if c != nil {
		t.Fatalf("Live on an absent claim = %+v, want nil", c)
	}
}

// TestClaimKeyedByPlannerID is the plan's required case (§3.3): a claim is
// written to the claim/<planner-id> row, is found by that id, and is not
// found by another planner's id.
func TestClaimKeyedByPlannerID(t *testing.T) {
	f, d, _ := testClaims(t)
	now := time.Now()

	claim := Claim{Planner: testClaimPlanner, PID: 123, HostPID: 99, HostStartedAt: 42, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write: %v", err)
	}

	stored, ok := claimRow(t, d, testClaimPlanner)
	if !ok {
		t.Fatalf("the claim must live in the %s row", claimKey(testClaimPlanner))
	}
	if stored.PID != 123 || stored.HostPID != 99 || stored.HostStartedAt != 42 {
		t.Errorf("stored claim = %+v", stored)
	}

	got, err := f.Live(testClaimPlanner, now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got == nil || got.PID != 123 {
		t.Fatalf("Live = %+v, want the written claim", got)
	}

	other, err := f.Live(otherClaimPlanner, now)
	if err != nil {
		t.Fatalf("Live for another planner: %v", err)
	}
	if other != nil {
		t.Fatalf("another planner's id found a claim: %+v", other)
	}
}

// TestClaimLiveIgnoresPaneKeyedRow: a pre-#303 pane-keyed claim is not a claim
// this version wrote. Live must ignore it (it would otherwise have to answer a
// question about a pane it no longer has) and leave the row for the sweep,
// which is the only thing allowed to remove it.
func TestClaimLiveIgnoresPaneKeyedRow(t *testing.T) {
	f, d, _ := testClaims(t)
	now := time.Now()

	// A live, current pane-keyed claim: exactly what an older relevo mcp
	// still running during the upgrade has on record.
	seedClaim(t, d, paneClaimID, Claim{PID: 4242, StartedAt: now, SeenAt: now})

	// Live never asks about a pane name, and asking about the pane name must
	// not remove the row either.
	got, err := f.Live("wG:pQ", now)
	if err != nil {
		t.Fatalf("Live(pane): %v", err)
	}
	if got != nil {
		t.Fatalf("Live(pane name) = %+v, want nil", got)
	}
	if _, ok := claimRow(t, d, paneClaimID); !ok {
		t.Fatal("Live must leave a pane-keyed claim alone")
	}
}

// TestMCPStartRemovesOnlyDeadPaneKeyedClaims is the plan's required case for
// the sweep rule: a pane-keyed claim whose pid is dead goes, a live one stays,
// a planner-keyed claim is never swept, and a row with no pid is left alone.
func TestMCPStartRemovesOnlyDeadPaneKeyedClaims(t *testing.T) {
	f, d, _ := testClaims(t)
	f.Alive = func(pid int) bool { return pid == 4242 }

	now := time.Now()
	seedClaim(t, d, "wG_pQ", Claim{PID: 111, StartedAt: now, SeenAt: now})  // dead
	seedClaim(t, d, "w9_p1", Claim{PID: 4242, StartedAt: now, SeenAt: now}) // live
	seedClaim(t, d, testClaimPlanner, Claim{Planner: testClaimPlanner, PID: 111, StartedAt: now, SeenAt: now})
	if err := d.KVPut(claimKey("junk"), []byte(`{"pane":"wG:pQ"}`)); err != nil {
		t.Fatalf("seed junk row: %v", err)
	}

	if removed := f.SweepPaneKeyed(); removed != 1 {
		t.Fatalf("SweepPaneKeyed removed %d claims, want 1", removed)
	}

	if _, ok := claimRow(t, d, "wG_pQ"); ok {
		t.Error("a dead pane-keyed claim must be removed")
	}
	if _, ok := claimRow(t, d, "w9_p1"); !ok {
		t.Error("a live pane-keyed claim must survive the sweep")
	}
	if _, ok := claimRow(t, d, testClaimPlanner); !ok {
		t.Error("a planner-keyed claim must survive the sweep")
	}
	if _, ok := claimRow(t, d, "junk"); !ok {
		t.Error("a pane-keyed claim with no pid has no dead writer to prove, so it stays")
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
	f, d, _ := testClaims(t)
	start := time.Now()
	seedClaim(t, d, testClaimPlanner, Claim{Planner: testClaimPlanner, PID: 123, StartedAt: start, SeenAt: start})

	later := start.Add(ClaimTTL + time.Second)
	got, err := f.Live(testClaimPlanner, later)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live past TTL = %+v, want nil", got)
	}
	if _, ok := claimRow(t, d, testClaimPlanner); ok {
		t.Error("a stale claim row must be removed")
	}
}

// TestClaimLiveRemovesDeadPID is the plan's required case: a claim with a
// dead pid must read as not-live and the row must be gone afterward.
func TestClaimLiveRemovesDeadPID(t *testing.T) {
	f, d, _ := testClaims(t)
	f.Alive = neverAlive
	now := time.Now()
	seedClaim(t, d, testClaimPlanner, Claim{Planner: testClaimPlanner, PID: 999, StartedAt: now, SeenAt: now})

	got, err := f.Live(testClaimPlanner, now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live on a dead pid = %+v, want nil", got)
	}
	if _, ok := claimRow(t, d, testClaimPlanner); ok {
		t.Error("a dead-pid claim row must be removed")
	}
}

// invalidJSONKV hands out unparseable bytes for one key -- a row no writer of
// this version could have written -- and counts its removal.
type invalidJSONKV struct {
	db.DBTxKV
	key     string
	deleted int
}

func (k *invalidJSONKV) KVGet(key string) ([]byte, bool, error) {
	if key == k.key {
		return []byte("not json"), true, nil
	}
	return k.DBTxKV.KVGet(key)
}

func (k *invalidJSONKV) KVDelete(key string) error {
	if key == k.key {
		k.deleted++
	}
	return k.DBTxKV.KVDelete(key)
}

func TestClaimLiveRemovesUnparseable(t *testing.T) {
	d := testSecretDB(t)
	kv := &invalidJSONKV{DBTxKV: db.TxKV{DB: d}, key: claimKey(testClaimPlanner)}
	f := &KVClaims{KV: kv, Alive: alwaysAlive}

	got, err := f.Live(testClaimPlanner, time.Now())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live on unparseable json = %+v, want nil", got)
	}
	if kv.deleted == 0 {
		t.Error("an unparseable claim row must be removed")
	}
}

func TestClaimLiveEmptyPlanner(t *testing.T) {
	f, _, _ := testClaims(t)
	if _, err := f.Live("", time.Now()); err == nil {
		t.Fatal("Live with an empty planner must error")
	}
}

func TestClaimWriteRefusesSecondLiveWriter(t *testing.T) {
	f, _, _ := testClaims(t)
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
// than silently creating a row the sweep would later reap.
func TestClaimWriteRefusesANonPlannerID(t *testing.T) {
	f, d, _ := testClaims(t)
	now := time.Now()
	err := f.Write(Claim{Planner: "wG:pQ", PID: 111, StartedAt: now, SeenAt: now}, now)
	if err == nil {
		t.Fatal("Write with a non-planner key must be refused")
	}
	if _, ok := claimRow(t, d, "wG:pQ"); ok {
		t.Error("a refused Write must leave no row behind")
	}
}

func TestClaimWriteSameWriterRefreshes(t *testing.T) {
	f, _, _ := testClaims(t)
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
	f, _, _ := testClaims(t)
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
	f, _, _ := testClaims(t)
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
	f, _, _ := testClaims(t)
	if err := f.Remove(testClaimPlanner, 111); err != nil {
		t.Fatalf("Remove on an absent claim must not error: %v", err)
	}
}

// TestClaimsImportAdoptsChannelFiles is §4.2's import: a present
// channels/<name>.json is put to claim/<name> and removed, and the emptied
// directory goes with it.
func TestClaimsImportAdoptsChannelFiles(t *testing.T) {
	d := testSecretDB(t)
	dir := filepath.Join(t.TempDir(), "channels")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	now := time.Now()
	for name, c := range map[string]Claim{
		testClaimPlanner + ".json": {Planner: testClaimPlanner, PID: 123, StartedAt: now, SeenAt: now},
		paneClaimID + ".json":      {PID: 4242, StartedAt: now, SeenAt: now},
	} {
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	f := &KVClaims{KV: db.TxKV{DB: d}, Root: dir, Alive: alwaysAlive}
	if _, err := f.Live(testClaimPlanner, now); err != nil {
		t.Fatalf("Live: %v", err)
	}

	if _, ok := claimRow(t, d, testClaimPlanner); !ok {
		t.Error("the planner-keyed claim file was not imported")
	}
	if _, ok := claimRow(t, d, paneClaimID); !ok {
		t.Error("the pane-keyed claim file was not imported")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the emptied channels directory is still there: %v", err)
	}
}
