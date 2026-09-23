package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestRewritePathPrefix checks the three outcomes at once: a value under the
// old root with a slash changes, a value that is the old root exactly changes,
// and a sibling whose name merely starts with the old root does not.
func TestRewritePathPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := seedRewriteBindings(d); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	rows, newer, err := RewritePathPrefix(context.Background(), path,
		[]Prefix{{Old: "/old/root", New: "/new/root"}})
	if err != nil {
		t.Fatalf("RewritePathPrefix: %v", err)
	}
	if newer {
		t.Errorf("newer = true, want false")
	}
	if rows != 3 {
		t.Errorf("rows = %d, want 3", rows)
	}

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d2.Close()

	cases := []struct {
		name string
		col  string
		want string
	}{
		{"one", "cwd", "/new/root/.worktrees/x"},
		{"four", "cwd", "/new/root/other"},
		{"three", "worktree", "/new/root"},
		{"two", "archive_path", "/old/rootother"},
	}
	for _, c := range cases {
		var got string
		if err := d2.sqlDB.QueryRow(
			`SELECT `+c.col+` FROM binding WHERE name = ?`, c.name).Scan(&got); err != nil {
			t.Fatalf("query %s.%s: %v", c.name, c.col, err)
		}
		if got != c.want {
			t.Errorf("%s.%s = %q, want %q", c.name, c.col, got, c.want)
		}
	}
}

// TestRewritePathPrefixNewerSchema checks that a newer database is reported and
// left alone, as Open's #372 rule requires.
func TestRewritePathPrefixNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedNewerSchema(t, path)

	rows, newer, err := RewritePathPrefix(context.Background(), path,
		[]Prefix{{Old: "/old/root", New: "/new/root"}})
	if err != nil {
		t.Fatalf("RewritePathPrefix: %v", err)
	}
	if !newer {
		t.Errorf("newer = false, want true")
	}
	if rows != 0 {
		t.Errorf("rows = %d, want 0", rows)
	}
}

// seedRewriteBindings writes four bindings whose TEXT path columns cover the
// three outcomes, plus a control that must not change.
func seedRewriteBindings(d *DB) error {
	now := time.Now()
	worktree := "/old/root"
	archive := "/old/rootother"

	for _, b := range []Binding{
		{Name: "one", CWD: "/old/root/.worktrees/x", BuilderMode: "headless", CreatedAt: now, IngestSource: IngestLive},
		{Name: "two", CWD: "/tmp/plain", ArchivePath: &archive, BuilderMode: "headless", CreatedAt: now, IngestSource: IngestLive},
		{Name: "three", CWD: "/tmp/plain", Worktree: &worktree, BuilderMode: "headless", CreatedAt: now, IngestSource: IngestLive},
		{Name: "four", CWD: "/old/root/other", BuilderMode: "headless", CreatedAt: now, IngestSource: IngestLive},
	} {
		if _, err := d.UpsertBinding(b); err != nil {
			return err
		}
	}
	return nil
}
