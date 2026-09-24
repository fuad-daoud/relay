package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func newBinding(name, cwd string) Binding {
	return Binding{
		Name:             name,
		CWD:              cwd,
		Planner:          Endpoint{PaneID: "w2:p3", SessionID: "abc", Kind: "claude"},
		Builder:          Endpoint{AgentName: name + "-builder", PaneID: "w2:p4", Kind: "opencode"},
		BuilderCandidate: "builder",
		Round:            1,
		State:            StateActive,
		RoundCap:         20,
		RoundTimeoutMS:   1800000,
	}
}

// bindingRecordJSON returns the record_json a saved binding is stored as, the
// DB equivalent of the bind.json bytes the file-backed store wrote (D1).
func bindingRecordJSON(t *testing.T, s *Store, name string) []byte {
	t.Helper()
	d, err := s.dbForWrite()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil || !ok {
		t.Fatalf("RecordGet(%q): ok=%v err=%v", name, ok, err)
	}
	return []byte(rec.JSON)
}

// bindingEvents returns the stored binding_event rows for name, so a test can
// assert on entry_json, the DB equivalent of a log.jsonl line (D2).
func bindingEvents(t *testing.T, s *Store, name string) []db.RecordEvent {
	t.Helper()
	d, err := s.dbForWrite()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil || !ok {
		t.Fatalf("RecordGet(%q): ok=%v err=%v", name, ok, err)
	}
	evs, err := d.EventsOf(rec.ID, 0)
	if err != nil {
		t.Fatalf("EventsOf: %v", err)
	}
	return evs
}

// TestSharedStoresScopeByOwner pins P5 step 1: two stores sharing one machine
// database, one per owner, can each hold a live "api", and neither sees the
// other's rows. It is a mutation guard for RecordList's owner filter: drop it
// and each store's List grows the other owner's row.
func TestSharedStoresScopeByOwner(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(filepath.Join(root, "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	storeA := NewShared(filepath.Join(root, "bindings", "a"), "owner-A", d)
	storeB := NewShared(filepath.Join(root, "bindings", "b"), "owner-B", d)

	if err := storeA.Save(newBinding("api", "/repo/a")); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := storeB.Save(newBinding("api", "/repo/b")); err != nil {
		t.Fatalf("Save B: %v", err)
	}

	gotA, err := storeA.Load("api")
	if err != nil {
		t.Fatalf("Load A: %v", err)
	}
	if gotA.CWD != "/repo/a" {
		t.Errorf("A's api CWD = %q, want /repo/a", gotA.CWD)
	}
	gotB, err := storeB.Load("api")
	if err != nil {
		t.Fatalf("Load B: %v", err)
	}
	if gotB.CWD != "/repo/b" {
		t.Errorf("B's api CWD = %q, want /repo/b", gotB.CWD)
	}

	listA, err := storeA.List()
	if err != nil {
		t.Fatalf("List A: %v", err)
	}
	if len(listA) != 1 || listA[0].Name != "api" || listA[0].CWD != "/repo/a" {
		t.Fatalf("A.List = %+v, want only A's api", listA)
	}
	listB, err := storeB.List()
	if err != nil {
		t.Fatalf("List B: %v", err)
	}
	if len(listB) != 1 || listB[0].Name != "api" || listB[0].CWD != "/repo/b" {
		t.Fatalf("B.List = %+v, want only B's api", listB)
	}

	// A shared store opens no handle of its own: nothing appears under its root
	// beyond the binding directories it writes.
	if _, err := os.Stat(filepath.Join(root, "bindings", "a", "relevo.db")); !os.IsNotExist(err) {
		t.Errorf("shared store A created a relevo.db under its root: %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	want := newBinding("webshop", "/home/dev/projects/webshop")

	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CWD != want.CWD || got.Builder.AgentName != want.Builder.AgentName {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("Save must stamp CreatedAt and UpdatedAt")
	}
	if got.Worktree != "" || got.ForkedFrom != "" || got.ForkedAtRound != 0 {
		t.Errorf("fork fields must default to zero: %+v", got)
	}
	if got.BuilderScreen != "" || !got.BuilderScreenAt.IsZero() {
		t.Errorf("builder screen fields must default to zero: %+v", got)
	}
	if !got.StalledSince.IsZero() {
		t.Errorf("StalledSince must default to zero: %+v", got)
	}
}

// TestStalledSinceRoundTrip pins #252's store field: the daemon's stall stamp
// survives Save and Load unchanged.
func TestStalledSinceRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	want := newBinding("webshop", "/home/dev/projects/webshop")
	want.StalledSince = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.StalledSince.Equal(want.StalledSince) {
		t.Errorf("StalledSince round trip: got %s, want %s", got.StalledSince, want.StalledSince)
	}
}

func TestBuilderScreenRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	want := newBinding("webshop", "/home/dev/projects/webshop")
	want.BuilderScreen = "abc123def456"
	want.BuilderScreenAt = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.BuilderScreen != want.BuilderScreen || !got.BuilderScreenAt.Equal(want.BuilderScreenAt) {
		t.Errorf("builder screen mismatch: got screen=%q at=%v, want screen=%q at=%v",
			got.BuilderScreen, got.BuilderScreenAt, want.BuilderScreen, want.BuilderScreenAt)
	}
}

func TestForkFieldsRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	want := newBinding("forked", "/repo-fork")
	want.Worktree = "/state/.worktrees/forked"
	want.ForkedFrom = "webshop"
	want.ForkedAtRound = 3

	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("forked")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Worktree != want.Worktree || got.ForkedFrom != want.ForkedFrom || got.ForkedAtRound != want.ForkedAtRound {
		t.Errorf("fork fields mismatch: got %+v, want %+v", got, want)
	}
}

func TestSaveRefusesDuplicateCWD(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	err := s.Save(newBinding("webshop2", "/repo"))
	if !errors.Is(err, ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken", err)
	}
}

func TestSaveAllowsRewritingSameBinding(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/repo")
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
	if got, want := s.PlanPath("webshop", 3), filepath.Join("/state", "webshop", "003-plan.md"); got != want {
		t.Errorf("PlanPath = %q, want %q", got, want)
	}
	if got, want := s.ReportPath("webshop", 12), filepath.Join("/state", "webshop", "012-report.md"); got != want {
		t.Errorf("ReportPath = %q, want %q", got, want)
	}
	if got, want := s.DonePath("webshop", 7), filepath.Join("/state", "webshop", "007-done"); got != want {
		t.Errorf("DonePath = %q, want %q", got, want)
	}
}

// TestViewedRoundTrip pins #143's .viewed sidecar: no stamp reads ok false,
// MarkViewed creates and stamps the file, and ViewedAt then reads that mtime
// back within a second.
func TestViewedRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/repo")
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, ok := s.ViewedAt("webshop"); ok {
		t.Fatal("ViewedAt before any stamp: got ok true, want false")
	}

	at := time.Now().UTC().Truncate(time.Second)
	if err := s.MarkViewed("webshop", at); err != nil {
		t.Fatalf("MarkViewed: %v", err)
	}

	got, ok := s.ViewedAt("webshop")
	if !ok {
		t.Fatal("ViewedAt after MarkViewed: got ok false, want true")
	}
	if diff := got.Sub(at); diff < -time.Second || diff > time.Second {
		t.Errorf("ViewedAt = %s, want within a second of %s", got, at)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"webshop", "a", "money-ai", "x_1"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "1abc", "Upjo", "has space", "way-too-long-a-binding-name-for-a-pane"} {
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

// TestFindByCWDSkipsDoneBindings guards the cwd fallback the CLI resolves
// almost every command through: a done binding no longer drives its tree, and
// resolving onto one would point `relevo send` at a finished session.
func TestFindByCWDSkipsDoneBindings(t *testing.T) {
	s := New(t.TempDir())
	done := newBinding("webshop", "/repo")
	done.State = StateDone
	if err := s.Save(done); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, found, err := s.FindByCWD("/repo"); err != nil || found {
		t.Fatalf("found=%v err=%v, want a done binding to be invisible here", found, err)
	}

	// The live binding that replaces it is still found.
	if err := s.Save(newBinding("webshop2", "/repo")); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, found, err := s.FindByCWD("/repo")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v, want the active binding", found, err)
	}
	if got.Name != "webshop2" {
		t.Errorf("name = %q, want webshop2", got.Name)
	}
}

// TestSaveStillRefusesASecondActiveBindingBesideADoneOne pins the other half:
// assertCWDFree scans on its own, so skipping done bindings in FindByCWD must
// not relax the two-builders-in-one-tree refusal.
func TestSaveStillRefusesASecondActiveBindingBesideADoneOne(t *testing.T) {
	s := New(t.TempDir())
	done := newBinding("webshop", "/repo")
	done.State = StateDone
	if err := s.Save(done); err != nil {
		t.Fatalf("Save done: %v", err)
	}
	if err := s.Save(newBinding("webshop2", "/repo")); err != nil {
		t.Fatalf("Save active beside done: %v", err)
	}

	if err := s.Save(newBinding("webshop3", "/repo")); !errors.Is(err, ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken from the still-active binding", err)
	}
}

// TestAssertCWDFreeIgnoresRemote pins #100's CWD-uniqueness exemption: a
// remote binding's CWD is only the repo its branch is cut from and results
// are fetched into, never a working tree a builder writes in, so it must not
// collide with other remote bindings and must not stop a local binding from
// naming the same repo -- but two builders in one tree is still refused
// between local bindings.
func TestAssertCWDFreeIgnoresRemote(t *testing.T) {
	s := New(t.TempDir())

	a := newBinding("a", "/repo")
	if err := s.Save(a); err != nil {
		t.Fatalf("Save local a: %v", err)
	}

	b := newBinding("b", "/repo")
	b.Builder.Mode = ModeRemote
	if err := s.Save(b); err != nil {
		t.Fatalf("Save remote b beside local a: %v", err)
	}

	c := newBinding("c", "/repo")
	c.Builder.Mode = ModeRemote
	if err := s.Save(c); err != nil {
		t.Fatalf("Save remote c beside local a and remote b: %v", err)
	}

	d := newBinding("d", "/repo")
	err := s.Save(d)
	if !errors.Is(err, ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken from local d", err)
	}
	if !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("error %q must name a, not b", err)
	}
	if strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("error %q must not name remote binding b", err)
	}
}

// TestFindByCWDSkipsRemote pins the other half of #100's exemption: the
// cwd-addressed verbs (`relevo send` with no --name, `relevo status` for "this
// tree") must never resolve onto a remote binding, since a remote binding's
// CWD is not a working tree it drives. Remote bindings are always addressed
// by --name.
func TestFindByCWDSkipsRemote(t *testing.T) {
	s := New(t.TempDir())

	remoteOnly := newBinding("remoteonly", "/repo")
	remoteOnly.Builder.Mode = ModeRemote
	if err := s.Save(remoteOnly); err != nil {
		t.Fatalf("Save remote: %v", err)
	}

	if _, found, err := s.FindByCWD("/repo"); err != nil || found {
		t.Fatalf("found=%v err=%v, want a remote-only CWD to be invisible here", found, err)
	}

	local := newBinding("local", "/repo2")
	if err := s.Save(local); err != nil {
		t.Fatalf("Save local: %v", err)
	}
	remoteAlso := newBinding("remotealso", "/repo2")
	remoteAlso.Builder.Mode = ModeRemote
	if err := s.Save(remoteAlso); err != nil {
		t.Fatalf("Save remote beside local: %v", err)
	}

	got, found, err := s.FindByCWD("/repo2")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v, want the local binding", found, err)
	}
	if got.Name != "local" {
		t.Errorf("name = %q, want local", got.Name)
	}
}

func TestArchiveMovesBindingAsideAndFreesTheName(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.AppendLog("webshop", LogEntry{Round: 1, Direction: DirToBuilder, Kind: KindPlan, Confirmed: true}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	dest, err := s.Archive("webshop")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if dest != "" {
		t.Errorf("Archive path = %q, want \"\" (nothing is tarred any more)", dest)
	}

	// The round log survives the archive as the record's events -- that is
	// the whole point.
	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one", archived, err)
	}
	events, err := s.ArchivedLog(archived[0].RecordID)
	if err != nil || len(events) != 1 || events[0].Kind != KindPlan {
		t.Errorf("ArchivedLog = %+v, %v, want the plan entry", events, err)
	}
	if _, err := s.Load("webshop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived binding must be gone from the live set, got %v", err)
	}
	if _, err := os.Stat(s.Dir("webshop")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the binding directory must be gone after an archive, got %v", err)
	}

	// And the name is free for a fresh bind on the same tree.
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Errorf("archiving must free the name and the working tree: %v", err)
	}
}

func TestListSkipsTheArchiveDirectory(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := s.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List must not see archived bindings, got %+v", got)
	}
}

func TestArchiveRefusesAnUnknownBinding(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Archive("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// archiveEntries listed the paths inside a gzipped tar, so tests could assert
// on what an archive preserved. It went with the tarball (P3d D1): a binding's
// files are round_file rows now, and ListArchived/ArchivedLog/ReadFile are how
// a test reads them back.

func TestDiffPath(t *testing.T) {
	s := New("/state")
	if got := s.DiffPath("ai", 2); !strings.HasSuffix(got, "002-diff.patch") {
		t.Errorf("DiffPath = %q, want ending in 002-diff.patch", got)
	}
	if got, want := s.PlanPath("ai", 2), filepath.Join("/state", "ai", "002-plan.md"); got != want {
		t.Errorf("PlanPath = %q, want %q", got, want)
	}
	if got, want := s.ReportPath("ai", 2), filepath.Join("/state", "ai", "002-report.md"); got != want {
		t.Errorf("ReportPath = %q, want %q", got, want)
	}
	if got, want := s.QuestionPath("ai", 2), filepath.Join("/state", "ai", "002-question.md"); got != want {
		t.Errorf("QuestionPath = %q, want %q", got, want)
	}
}

func TestDriftPath(t *testing.T) {
	s := New("/state")
	drift := s.DriftPath("webshop", 5)
	if !strings.HasSuffix(drift, "005-drift.patch") {
		t.Errorf("DriftPath = %q, want ending in 005-drift.patch", drift)
	}
	diff := s.DiffPath("webshop", 5)
	if filepath.Dir(drift) != filepath.Dir(diff) {
		t.Errorf("DriftPath dir = %q, want %q", filepath.Dir(drift), filepath.Dir(diff))
	}
}

func TestConsultPathsCarryRoundAndID(t *testing.T) {
	s := New("/state")

	if got, want := s.AskPath("webshop", 3, "7f2a3c1d"), "/state/webshop/003-7f2a3c1d-ask.md"; got != want {
		t.Errorf("AskPath = %q, want %q", got, want)
	}
	if got, want := s.FindingsPath("webshop", 12, "7f2a3c1d"), "/state/webshop/012-7f2a3c1d-findings.md"; got != want {
		t.Errorf("FindingsPath = %q, want %q", got, want)
	}
	// A headless consult's stream and stderr sit beside its ask and findings,
	// under the same round and id.
	if got, want := s.ConsultStreamPath("webshop", 3, "7f2a3c1d"), "/state/webshop/003-7f2a3c1d-consult.jsonl"; got != want {
		t.Errorf("ConsultStreamPath = %q, want %q", got, want)
	}
	if got, want := s.ConsultLogPath("webshop", 3, "7f2a3c1d"), "/state/webshop/003-7f2a3c1d-consult.log"; got != want {
		t.Errorf("ConsultLogPath = %q, want %q", got, want)
	}
	// NNN-question.md belongs to the blocked-dialog capture. A consult being
	// asked something is not a builder being blocked on something.
	if s.AskPath("webshop", 3, "7f2a3c1d") == s.QuestionPath("webshop", 3) {
		t.Error("AskPath collides with QuestionPath")
	}
	// Fork copies round files by their leading NNN-; both consult files must
	// parse as round files or a fork would silently drop a consult's stream.
	if r, ok := roundOfFile(filepath.Base(s.ConsultStreamPath("webshop", 12, "7f2a3c1d"))); !ok || r != 12 {
		t.Errorf("roundOfFile(consult.jsonl) = %d, %v; want 12, true", r, ok)
	}
	if r, ok := roundOfFile(filepath.Base(s.ConsultLogPath("webshop", 12, "7f2a3c1d"))); !ok || r != 12 {
		t.Errorf("roundOfFile(consult.log) = %d, %v; want 12, true", r, ok)
	}
}

// A binding written before #80 carries builder_alias; the decoder drops it, so
// the binding reads as adopted -- that is the whole migration (spec §3.6).
func TestLoadIgnoresLegacyBuilderAlias(t *testing.T) {
	s := New(t.TempDir())
	dir := s.Dir("old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"name":"old","cwd":"/repo","planner":{"pane_id":"w2:p3","kind":"claude"},"builder":{"pane_id":"w2:p4","kind":"agy"},"builder_alias":"abuilder","round":1,"state":"active","round_cap":20,"round_timeout_ms":1800000}`
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}

	got, err := s.Load("old")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != "old" {
		t.Errorf("got.Name = %q, want %q", got.Name, "old")
	}
	if got.BuilderCandidate != "" {
		t.Errorf("got.BuilderCandidate = %q, want empty", got.BuilderCandidate)
	}
}

// A binding written before #85 carries preamble_pending; the decoder drops it,
// and nothing reads it: the role is selected at launch now.
func TestLoadIgnoresLegacyPreamblePending(t *testing.T) {
	s := New(t.TempDir())
	dir := s.Dir("old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"name":"old","cwd":"/repo","planner":{"pane_id":"w2:p3","kind":"claude"},"builder":{"pane_id":"w2:p4","kind":"agy"},"preamble_pending":true,"round":3,"state":"active","round_cap":20,"round_timeout_ms":1800000}`
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}
	got, err := s.Load("old")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Round != 3 {
		t.Errorf("got.Round = %d, want 3", got.Round)
	}
	if err := s.Save(got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw := bindingRecordJSON(t, s, "old")
	if strings.Contains(string(raw), "preamble_pending") {
		t.Errorf("round-trip must drop preamble_pending, got:\n%s", raw)
	}
}

func TestLegacyPaneBindingReSavesByteIdentical(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/projects/webshop")
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before := bindingRecordJSON(t, s, "webshop")
	for _, key := range []string{`"mode"`, `"pid"`, `"started_at"`, `"log_path"`} {
		if bytes.Contains(before, []byte(key)) {
			t.Errorf("a pane binding's record must not carry %s:\n%s", key, before)
		}
	}
	got, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Builder.Headless() || got.Builder.Mode != "" {
		t.Errorf("legacy builder must read as pane with empty Mode: %+v", got.Builder)
	}
	if got.Builder.PID != 0 || got.Builder.StartedAt != 0 || got.Builder.LogPath != "" {
		t.Errorf("legacy builder must have zero process fields: %+v", got.Builder)
	}
}

func TestBuilderLogPathIsARoundFileBesideTheReport(t *testing.T) {
	s := New("/state")
	if got, want := s.BuilderLogPath("webshop", 3), filepath.Join("/state", "webshop", "003-builder.log"); got != want {
		t.Errorf("BuilderLogPath = %q, want %q", got, want)
	}
	// roundOfFile is what ForkState uses to decide which files to copy; the
	// log must be one of them.
	if r, ok := roundOfFile(filepath.Base(s.BuilderLogPath("webshop", 12))); !ok || r != 12 {
		t.Errorf("roundOfFile(012-builder.log) = %d, %v; want 12, true", r, ok)
	}
	if got, want := s.BuilderStreamPath("webshop", 3), filepath.Join("/state", "webshop", "003-builder.jsonl"); got != want {
		t.Errorf("BuilderStreamPath = %q, want %q", got, want)
	}
	if r, ok := roundOfFile(filepath.Base(s.BuilderStreamPath("webshop", 12))); !ok || r != 12 {
		t.Errorf("roundOfFile(012-builder.jsonl) = %d, %v; want 12, true", r, ok)
	}
}
