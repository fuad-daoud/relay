package ingest

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

func TestDirSourceListsAndOpens(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bind.json"), `{"name":"fixture"}`)
	writeFile(t, filepath.Join(dir, "log.jsonl"), `{"round":1}`+"\n")
	writeFile(t, filepath.Join(dir, "001-plan.md"), "plan text")

	src := DirSource(dir)

	if got := src.Name(); got != filepath.Base(dir) {
		t.Errorf("Name() = %q, want %q", got, filepath.Base(dir))
	}

	b, err := src.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Name != "fixture" {
		t.Errorf("Bind().Name = %q, want fixture", b.Name)
	}

	names, err := src.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"001-plan.md", "bind.json", "log.jsonl"}
	if !equalStrings(names, want) {
		t.Errorf("List() = %v, want %v", names, want)
	}

	rc, size, err := src.Open("001-plan.md")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "plan text" {
		t.Errorf("Open(001-plan.md) = %q, want %q", data, "plan text")
	}
	if size != int64(len("plan text")) {
		t.Errorf("size = %d, want %d", size, len("plan text"))
	}

	kind, path := src.Origin()
	if kind != "live" || path != dir {
		t.Errorf("Origin() = %q, %q, want live, %q", kind, path, dir)
	}

	if _, _, err := src.Open("does-not-exist"); !os.IsNotExist(err) {
		t.Errorf("Open(missing) err = %v, want os.ErrNotExist", err)
	}
}

func TestDirSourceMissingBindIsErrSource(t *testing.T) {
	dir := t.TempDir()
	src := DirSource(dir)

	if _, err := src.Bind(); err == nil {
		t.Fatal("Bind() over a directory with no bind.json must fail")
	} else if !errors.Is(err, ErrSource) {
		t.Errorf("Bind() err = %v, want ErrSource", err)
	}
}

// buildArchive saves a binding through store.Store the normal way, adds
// extra round files to its directory, then archives it -- producing a real
// gc tarball with exactly the layout production writes
// (internal/store/store.go archive/tarGzDir), rather than one hand-rolled
// in the test.
func buildArchive(t *testing.T, root, name string) string {
	t.Helper()
	s := store.New(root)
	if err := s.Save(store.Binding{Name: name, CWD: "/work/" + name, State: store.StateDone}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	writeFile(t, filepath.Join(s.Dir(name), "001-plan.md"), "plan text")
	writeFile(t, filepath.Join(s.Dir(name), "log.jsonl"), `{"round":1}`+"\n")

	path, err := s.Archive(name)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	return path
}

func TestTarSourceReadsMembersWithoutExtracting(t *testing.T) {
	root := t.TempDir()
	path := buildArchive(t, root, "fixture")

	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	src, err := TarSource(path)
	if err != nil {
		t.Fatalf("TarSource: %v", err)
	}

	if got := src.Name(); got != "fixture" {
		t.Errorf("Name() = %q, want fixture", got)
	}

	names, err := src.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"001-plan.md", "bind.json", "log.jsonl"}
	if !equalStrings(names, want) {
		t.Errorf("List() = %v, want %v", names, want)
	}

	b, err := src.Bind()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Name != "fixture" {
		t.Errorf("Bind().Name = %q, want fixture", b.Name)
	}

	rc, size, err := src.Open("001-plan.md")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "plan text" {
		t.Errorf("Open(001-plan.md) = %q, want %q", data, "plan text")
	}
	if size != int64(len("plan text")) {
		t.Errorf("size = %d, want %d", size, len("plan text"))
	}

	if _, _, err := src.Open("does-not-exist"); !os.IsNotExist(err) {
		t.Errorf("Open(missing) err = %v, want os.ErrNotExist", err)
	}

	// The archive directory (a sibling of root's binding dirs) is the only
	// place a file was created; nothing was extracted anywhere else under
	// root's own top level.
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("root directory gained entries reading the tarball: before %v, after %v", before, after)
	}
}

func TestTarSourceOriginStamp(t *testing.T) {
	root := t.TempDir()
	path := buildArchive(t, root, "fixture")

	src, err := TarSource(path)
	if err != nil {
		t.Fatalf("TarSource: %v", err)
	}

	kind, gotPath := src.Origin()
	if kind != "archive" || gotPath != path {
		t.Errorf("Origin() = %q, %q, want archive, %q", kind, gotPath, path)
	}

	ts, ok := src.(*tarSource)
	if !ok {
		t.Fatalf("TarSource() did not return *tarSource, got %T", src)
	}
	at, found := ts.ArchivedAt()
	if !found {
		t.Fatal("ArchivedAt() found = false, want true")
	}
	if at.IsZero() {
		t.Error("ArchivedAt() returned the zero time")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
