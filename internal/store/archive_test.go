package store

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTarGz writes a flat <name>/<member> tarball at dest, the layout a
// pre-P3d relevo archived bindings in. The import's own tests build their
// tarballs with it rather than with production code, since nothing writes
// tarballs any more (P3d §4.2).
func writeTarGz(t *testing.T, dest, name string, members map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for base, body := range members {
		hdr := &tar.Header{
			Name:     name + "/" + base,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestArchiveSealsAndFreesTheName pins the P3d §4.1 archive: archiving writes
// the binding's round files into round_file, archives the record, removes the
// directory, returns "" for the path, and leaves the name free to bind again.
func TestArchiveSealsAndFreesTheName(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entry := LogEntry{TS: time.Unix(1, 0).UTC(), Round: 1, Direction: DirToBuilder, Kind: KindPlan, Confirmed: true}
	if err := s.AppendLog("webshop", entry); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if err := os.WriteFile(s.PlanPath("webshop", 1), []byte("round 1 plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	dest, err := s.Archive("webshop")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if dest != "" {
		t.Errorf("Archive path = %q, want \"\" (nothing is tarred)", dest)
	}
	if _, err := s.Load("webshop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived binding must be gone from the live set, got %v", err)
	}

	archived, err := s.ListArchived()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, want exactly one", archived)
	}
	a := archived[0]
	if a.Binding.Name != "webshop" || a.RecordID == "" {
		t.Errorf("ArchivedBinding = %+v, want webshop with a record id", a)
	}
	if a.ArchivedAt.IsZero() {
		t.Error("ArchivedAt is zero, want the archive stamp")
	}

	// The log survives as the record's events.
	events, err := s.ArchivedLog(a.RecordID)
	if err != nil {
		t.Fatalf("ArchivedLog: %v", err)
	}
	if len(events) != 1 || events[0].Kind != KindPlan || events[0].Seq != 1 {
		t.Errorf("ArchivedLog = %+v, want the one plan entry", events)
	}

	// And the round file survives as a sealed row, readable on the old path.
	body, err := s.ReadFile(s.PlanPath("webshop", 1))
	if err != nil || string(body) != "round 1 plan\n" {
		t.Errorf("ReadFile(plan) = %q, %v; want the sealed plan", body, err)
	}

	// The name and working tree are free again.
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Errorf("archiving must free the name and the working tree: %v", err)
	}
}

// TestListArchivedImportsAndRemovesATarball pins §4.2: a tarball present in
// .archive/ becomes an archived record with its events and round files, and
// the tarball -- and the now-empty .archive/ -- are removed.
func TestListArchivedImportsAndRemovesATarball(t *testing.T) {
	s := New(t.TempDir())
	dest := filepath.Join(s.ArchiveDir(), "webshop-20260911-215319.tar.gz")
	logLine := `{"ts":"2026-09-10T10:00:00Z","seq":1,"round":1,"direction":"to_builder","kind":"plan","confirmed":true}`
	writeTarGz(t, dest, "webshop", map[string]string{
		"bind.json":   `{"name":"webshop","cwd":"/repo","round":2,"state":"done"}`,
		"log.jsonl":   logLine + "\n",
		"001-plan.md": "round 1 plan\n",
	})

	archived, err := s.ListArchived()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, want exactly one imported record", archived)
	}
	a := archived[0]
	want := time.Date(2026, 9, 11, 21, 53, 19, 0, time.UTC)
	if a.Binding.Name != "webshop" || !a.ArchivedAt.Equal(want) {
		t.Errorf("imported = %+v at %v, want webshop at %v", a, a.ArchivedAt, want)
	}
	if a.Binding.CWD != "/repo" || a.Binding.Round != 2 {
		t.Errorf("imported binding = %+v, want the tarball's bind.json", a.Binding)
	}

	events, err := s.ArchivedLog(a.RecordID)
	if err != nil || len(events) != 1 || events[0].Kind != KindPlan {
		t.Errorf("ArchivedLog = %+v, %v; want the tarball's one plan entry", events, err)
	}
	if body, err := s.ReadFile(filepath.Join(s.Dir("webshop"), "001-plan.md")); err != nil || string(body) != "round 1 plan\n" {
		t.Errorf("ReadFile(001-plan.md) = %q, %v; want the imported member", body, err)
	}

	// A file that is present is imported, and then it is gone.
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the imported tarball is still there: %v", err)
	}
	if _, err := os.Stat(s.ArchiveDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".archive/ is still there after the last tarball was imported: %v", err)
	}

	// A second read imports nothing new.
	again, err := s.ListArchived()
	if err != nil || len(again) != 1 {
		t.Errorf("second ListArchived = %+v, %v; want the one record", again, err)
	}
}

// TestListArchivedImportDoesNotTouchALiveBinding pins the collision rule: a
// tarball whose name a live binding already holds becomes its own archived
// record, and the live binding is left exactly as it was.
func TestListArchivedImportDoesNotTouchALiveBinding(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/live/tree")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dest := filepath.Join(s.ArchiveDir(), "webshop-20260911-215319.tar.gz")
	writeTarGz(t, dest, "webshop", map[string]string{
		"bind.json": `{"name":"webshop","cwd":"/old/tree","round":1,"state":"done"}`,
	})

	archived, err := s.ListArchived()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 1 || archived[0].Binding.CWD != "/old/tree" {
		t.Fatalf("ListArchived = %+v, want the tarball imported as its own record", archived)
	}

	live, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("the live binding must survive the import: %v", err)
	}
	if live.CWD != "/live/tree" {
		t.Errorf("live binding CWD = %q, want /live/tree", live.CWD)
	}
}

// TestListArchivedKeepsACorruptTarball pins §6's "a bad tarball is kept, and
// warned about once": one unreadable tarball neither imports nor fails the
// read, and it stays where it was.
func TestListArchivedKeepsACorruptTarball(t *testing.T) {
	s := New(t.TempDir())
	dest := filepath.Join(s.ArchiveDir(), "webshop-20260911-215319.tar.gz")
	if err := os.MkdirAll(s.ArchiveDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("this is not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}

	archived, err := s.ListArchived()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 0 {
		t.Errorf("ListArchived = %+v, want nothing imported", archived)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("a corrupt tarball must be left in place: %v", err)
	}
}

// TestImportTarballFailureKeepsTheTarball pins the ordering §4.2 and §6 ask
// for: the tarball is removed only after the import transaction commits, so an
// import that fails inside the transaction leaves the file for the next
// attempt.
//
// Mutation check: move the os.Remove(path) in importTarball to before d.Tx and
// this fails -- the tarball is already gone by the time the import fails.
func TestImportTarballFailureKeepsTheTarball(t *testing.T) {
	s := New(t.TempDir())
	dest := filepath.Join(s.ArchiveDir(), "webshop-20260911-215319.tar.gz")
	// A bind.json that parses, and a log.jsonl that does not: the failure
	// lands inside the import transaction.
	writeTarGz(t, dest, "webshop", map[string]string{
		"bind.json": `{"name":"webshop","cwd":"/repo","round":1,"state":"done"}`,
		"log.jsonl": "{not json}\n",
	})

	archived, err := s.ListArchived()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 0 {
		t.Errorf("ListArchived = %+v, want nothing imported from a bad log", archived)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("a tarball whose import failed must be left in place: %v", err)
	}

	// Its record did not survive the rolled-back transaction either.
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if _, ok, err := d.RecordGet(s.owner, "webshop"); err != nil || ok {
		t.Errorf("RecordGet(webshop) = %v, %v; want no live row after a failed import", ok, err)
	}
	if recs, err := d.RecordListArchived(s.owner); err != nil || len(recs) != 0 {
		t.Errorf("RecordListArchived = %+v, %v; want no archived row after a failed import", recs, err)
	}
}

// TestReadFileResolvesTheMostRecentlyArchivedRecord pins the archived half of
// sealedLookup: when a name has no live record, a round file path under its
// old directory reads the newest archived record of that name.
func TestReadFileResolvesTheMostRecentlyArchivedRecord(t *testing.T) {
	s := New(t.TempDir())
	for _, round := range []string{"first", "second"} {
		if err := s.Save(newBinding("webshop", "/repo")); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if err := os.WriteFile(s.PlanPath("webshop", 1), []byte(round+"\n"), 0o644); err != nil {
			t.Fatalf("write plan: %v", err)
		}
		if _, err := s.Archive("webshop"); err != nil {
			t.Fatalf("Archive: %v", err)
		}
	}

	body, err := s.ReadFile(s.PlanPath("webshop", 1))
	if err != nil || string(body) != "second\n" {
		t.Errorf("ReadFile = %q, %v; want the newest archived record's plan", body, err)
	}

	archived, err := s.ListArchived()
	if err != nil || len(archived) != 2 {
		t.Fatalf("ListArchived = %+v, %v; want both records", archived, err)
	}
	if !archived[0].ArchivedAt.Before(archived[1].ArchivedAt) && !archived[0].ArchivedAt.Equal(archived[1].ArchivedAt) {
		t.Errorf("ListArchived is not oldest-first: %v then %v", archived[0].ArchivedAt, archived[1].ArchivedAt)
	}
}

// TestArchivedLogDecodesEntries pins ArchivedLog against a record whose events
// were written by the tarball import: entry_json is authoritative and Seq,
// Confirmed, DeliveredAt and Route come from the promoted columns.
func TestArchivedLogDecodesEntries(t *testing.T) {
	s := New(t.TempDir())
	dest := filepath.Join(s.ArchiveDir(), "webshop-20260911-215319.tar.gz")
	lines := `{"ts":"2026-09-10T10:00:00Z","round":1,"direction":"to_builder","kind":"plan","confirmed":true}` + "\n" +
		`{"ts":"2026-09-10T10:01:00Z","round":1,"direction":"to_planner","kind":"report","confirmed":true,"delivered_at":"2026-09-10T10:02:00Z","route":"channel"}` + "\n"
	writeTarGz(t, dest, "webshop", map[string]string{
		"bind.json": `{"name":"webshop","cwd":"/repo","round":2,"state":"done"}`,
		"log.jsonl": lines,
	})

	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v", archived, err)
	}
	entries, err := s.ArchivedLog(archived[0].RecordID)
	if err != nil {
		t.Fatalf("ArchivedLog: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Seq != 1 || entries[1].Seq != 2 {
		t.Errorf("seqs = %d, %d; want 1, 2", entries[0].Seq, entries[1].Seq)
	}
	if !entries[1].Confirmed || entries[1].Route != "channel" || entries[1].DeliveredAt == nil {
		t.Errorf("entries[1] = %+v, want the promoted confirm columns", entries[1])
	}

	// The raw JSON survived verbatim, exactly as P3a's import keeps it.
	recs, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	events, err := recs.EventsOf(archived[0].RecordID, 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("EventsOf = %+v, %v", events, err)
	}
	var decoded LogEntry
	if err := json.Unmarshal([]byte(events[1].JSON), &decoded); err != nil {
		t.Fatalf("entry_json is not the original line: %v", err)
	}
	if decoded.Kind != KindReport {
		t.Errorf("entry_json kind = %q, want report", decoded.Kind)
	}
}
