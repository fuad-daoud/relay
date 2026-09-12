package store

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"webshop", "a", "money-ai", "x_1"} {
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

// TestFindByCWDSkipsDoneBindings guards the cwd fallback the CLI resolves
// almost every command through: a done binding no longer drives its tree, and
// resolving onto one would point `relay send` at a finished session.
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

	// The round log survives the archive -- that is the whole point.
	if !filepath.IsAbs(dest) || !strings.HasSuffix(dest, ".tar.gz") {
		t.Errorf("archive path = %q, want an absolute .tar.gz", dest)
	}
	if got := archiveEntries(t, dest); !slices.Contains(got, "webshop/log.jsonl") {
		t.Errorf("archived tarball entries = %v, want it to contain webshop/log.jsonl", got)
	}
	if _, err := s.Load("webshop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived binding must be gone from the live set, got %v", err)
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

// archiveEntries lists the paths inside a gzipped tar, so tests can assert on
// what an archive preserved without shelling out.
func archiveEntries(t *testing.T, path string) []string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip archive: %v", err)
	}
	defer gz.Close()

	var names []string
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		names = append(names, hdr.Name)
	}

	return names
}

func TestArchiveIsCompressed(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Highly compressible content, as relay's own state files are.
	big := strings.Repeat("the planner told the builder to read the plan file\n", 2000)
	if err := os.WriteFile(s.PlanPath("webshop", 1), []byte(big), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	dest, err := s.Archive("webshop")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if info.Size() >= int64(len(big)) {
		t.Errorf("archive is %d bytes for %d bytes of input; compression is not happening",
			info.Size(), len(big))
	}
}

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
	// NNN-question.md belongs to the blocked-dialog capture. A consult being
	// asked something is not a builder being blocked on something.
	if s.AskPath("webshop", 3, "7f2a3c1d") == s.QuestionPath("webshop", 3) {
		t.Error("AskPath collides with QuestionPath")
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
	raw, err := os.ReadFile(filepath.Join(dir, "bind.json"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(raw), "preamble_pending") {
		t.Errorf("round-trip must drop preamble_pending, got:\n%s", raw)
	}
}

// LedgerPath sits at the state root beside .lock so writes can serialize under
// WithLock (#61).
func TestLedgerPath(t *testing.T) {
	dir := t.TempDir()
	if got, want := New(dir).LedgerPath(), filepath.Join(dir, "ledger.json"); got != want {
		t.Errorf("LedgerPath = %q, want %q", got, want)
	}
}

func TestLegacyPaneBindingReSavesByteIdentical(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/projects/webshop")
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(s.Dir("webshop"), "bind.json"))
	if err != nil {
		t.Fatalf("read bind.json: %v", err)
	}
	for _, key := range []string{`"mode"`, `"pid"`, `"started_at"`, `"log_path"`} {
		if bytes.Contains(before, []byte(key)) {
			t.Errorf("a pane binding's bind.json must not carry %s:\n%s", key, before)
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
}
