package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestCaptureRoundDiff_NilGit(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	rt := Runtime{Store: s, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "abc"}

	res := CaptureRoundDiff(ctx, rt, b)
	if res.Available {
		t.Fatal("expected Available=false with nil Git")
	}
	if line := DiffLine(res); line != "" {
		t.Fatalf("expected empty line for nil Git, got %q", line)
	}
}

func TestCaptureRoundDiff_ErrNotRepo(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{snapshotTreeErr: git.ErrNotRepo}
	rt := Runtime{Store: s, Git: fg, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "abc"}

	res := CaptureRoundDiff(ctx, rt, b)
	if res.Available {
		t.Fatal("expected Available=false for ErrNotRepo")
	}
	if line := DiffLine(res); line != "" {
		t.Fatalf("expected empty line for ErrNotRepo, got %q", line)
	}
}

func TestCaptureRoundDiff_GitFailure(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{snapshotTreeErr: errors.New("boom: git broken")}
	rt := Runtime{Store: s, Git: fg, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "abc"}

	res := CaptureRoundDiff(ctx, rt, b)
	if res.Available {
		t.Fatal("expected Available=false for git failure")
	}
	if res.Reason != "boom: git broken" {
		t.Fatalf("unexpected Reason: %q", res.Reason)
	}
	wantLine := "Diff: unavailable (boom: git broken)"
	if line := DiffLine(res); line != wantLine {
		t.Fatalf("got %q, want %q", line, wantLine)
	}
}

func TestCaptureRoundDiff_EmptyDiff(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		diffResult:     git.Diff{Stat: git.Stat{FilesChanged: 0}},
	}
	rt := Runtime{Store: s, Git: fg, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "tree-start"}

	res := CaptureRoundDiff(ctx, rt, b)
	if !res.Available {
		t.Fatal("expected Available=true for empty diff")
	}
	if res.Path != "" {
		t.Fatalf("expected empty Path, got %q", res.Path)
	}
	if _, err := os.Stat(s.DiffPath("webshop", 1)); !os.IsNotExist(err) {
		t.Fatalf("expected no patch file written, got err %v", err)
	}
	wantLine := "Diff: no file changes"
	if line := DiffLine(res); line != wantLine {
		t.Fatalf("got %q, want %q", line, wantLine)
	}
}

func TestCaptureRoundDiff_TruncatedDiff(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		diffResult: git.Diff{
			Stat:      git.Stat{FilesChanged: 312, Insertions: 48120, Deletions: 9033},
			Truncated: true,
		},
	}
	rt := Runtime{Store: s, Git: fg, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "tree-start"}

	res := CaptureRoundDiff(ctx, rt, b)
	if !res.Available {
		t.Fatal("expected Available=true for truncated diff")
	}
	if !res.Truncated {
		t.Fatal("expected Truncated=true")
	}
	if res.Path != "" {
		t.Fatalf("expected empty Path, got %q", res.Path)
	}
	if _, err := os.Stat(s.DiffPath("webshop", 1)); !os.IsNotExist(err) {
		t.Fatalf("expected no patch file written, got err %v", err)
	}
	wantLine := "Diff: 312 files, +48120 -9033 (patch omitted, over the 4 MiB cap)"
	if line := DiffLine(res); line != wantLine {
		t.Fatalf("got %q, want %q", line, wantLine)
	}
}

func TestCaptureRoundDiff_NormalDiff(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		diffResult: git.Diff{
			Stat:  git.Stat{FilesChanged: 7, Insertions: 214, Deletions: 38},
			Patch: []byte("--- a/file\n+++ b/file\n@@ ...\n"),
		},
	}
	rt := Runtime{Store: s, Git: fg, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 2, RoundBaselineTree: "tree-start"}

	// Must save binding so s.Dir("webshop") exists for writing the patch
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}

	res := CaptureRoundDiff(ctx, rt, b)
	if !res.Available {
		t.Fatal("expected Available=true")
	}
	expectedPath := s.DiffPath("webshop", 2)
	if res.Path != expectedPath {
		t.Fatalf("got path %q, want %q", res.Path, expectedPath)
	}

	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("read written patch: %v", err)
	}
	if string(data) != "--- a/file\n+++ b/file\n@@ ...\n" {
		t.Fatalf("unexpected patch content: %s", string(data))
	}

	wantLine := "Diff: " + expectedPath + " (7 files, +214 -38)"
	if line := DiffLine(res); line != wantLine {
		t.Fatalf("got %q, want %q", line, wantLine)
	}

	// ReadDiff
	patch, ok, err := ReadDiff(rt, "webshop", 2)
	if err != nil || !ok {
		t.Fatalf("ReadDiff failed: ok=%v, err=%v", ok, err)
	}
	if string(patch) != string(data) {
		t.Fatalf("ReadDiff mismatch: %s", string(patch))
	}

	// ReadDiff for round without patch
	patch, ok, err = ReadDiff(rt, "webshop", 1)
	if err != nil || ok || patch != nil {
		t.Fatalf("expected nil, false, nil for round 1, got %v, %v, %v", patch, ok, err)
	}

	// ReadDiff for missing binding
	_, _, err = ReadDiff(rt, "nonexistent", 1)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing binding, got %v", err)
	}
}

func TestCaptureBaseline(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{snapshotTreeID: "tree-base"}
	rt := Runtime{Store: s, Git: fg, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
	b := store.Binding{Name: "webshop", CWD: "/repo"}

	tree := CaptureBaseline(ctx, rt, b)
	if tree != "tree-base" {
		t.Fatalf("got tree %q, want tree-base", tree)
	}

	// With nil git
	treeNil := CaptureBaseline(ctx, Runtime{Store: s, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}, b)
	if treeNil != "" {
		t.Fatalf("got %q with nil git, want empty string", treeNil)
	}

	// With error
	fgErr := &fakeGit{snapshotTreeErr: errors.New("fail")}
	treeErr := CaptureBaseline(ctx, Runtime{Store: s, Git: fgErr, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}, b)
	if treeErr != "" {
		t.Fatalf("got %q with failing git, want empty string", treeErr)
	}
}

func TestCaptureRoundDiff_EmptyBaselineSkipsSnapshot(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{snapshotTreeID: "tree-end"}
	rt := Runtime{Store: s, Git: fg, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: ""}

	res := CaptureRoundDiff(ctx, rt, b)
	if res.Available {
		t.Fatal("expected Available=false for empty baseline")
	}
	if res.Reason != "no baseline" {
		t.Fatalf("got Reason %q, want %q", res.Reason, "no baseline")
	}
	if fg.snapshotCalls != 0 {
		t.Fatalf("expected 0 SnapshotTree calls, got %d", fg.snapshotCalls)
	}
}

func TestCaptureRoundDiff_EndTree(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name               string
		git                func() *fakeGit
		nilGit             bool
		baseline           string
		wantEndTree        string
		wantAvailable      bool
		wantReasonNonEmpty bool
		assertCalls        func(t *testing.T, fg *fakeGit)
	}{
		{
			name: "successful snapshot, successful diff -> EndTree set, Available true",
			git: func() *fakeGit {
				return &fakeGit{
					snapshotTreeID: "tree-end",
					diffResult: git.Diff{
						Stat: git.Stat{FilesChanged: 1, Insertions: 2, Deletions: 1},
					},
				}
			},
			baseline:           "tree-start",
			wantEndTree:        "tree-end",
			wantAvailable:      true,
			wantReasonNonEmpty: false,
			assertCalls: func(t *testing.T, fg *fakeGit) {
				if fg.snapshotCalls != 1 {
					t.Fatalf("expected 1 SnapshotTree call, got %d", fg.snapshotCalls)
				}
				if fg.diffCalls != 1 {
					t.Fatalf("expected 1 DiffTrees call, got %d", fg.diffCalls)
				}
			},
		},
		{
			name: "successful snapshot, DiffTrees fails -> EndTree still set, Available false with a Reason",
			git: func() *fakeGit {
				return &fakeGit{
					snapshotTreeID: "tree-end",
					diffErr:        errors.New("boom: diff failed"),
				}
			},
			baseline:           "tree-start",
			wantEndTree:        "tree-end",
			wantAvailable:      false,
			wantReasonNonEmpty: true,
			assertCalls: func(t *testing.T, fg *fakeGit) {
				if fg.snapshotCalls != 1 {
					t.Fatalf("expected 1 SnapshotTree call, got %d", fg.snapshotCalls)
				}
				if fg.diffCalls != 1 {
					t.Fatalf("expected 1 DiffTrees call, got %d", fg.diffCalls)
				}
			},
		},
		{
			name:               "rt.Git == nil -> EndTree empty",
			nilGit:             true,
			baseline:           "tree-start",
			wantEndTree:        "",
			wantAvailable:      false,
			wantReasonNonEmpty: false,
		},
		{
			name: "b.RoundBaselineTree == \"\" -> EndTree empty, and no snapshot was attempted",
			git: func() *fakeGit {
				return &fakeGit{snapshotTreeID: "tree-end"}
			},
			baseline:           "",
			wantEndTree:        "",
			wantAvailable:      false,
			wantReasonNonEmpty: true,
			assertCalls: func(t *testing.T, fg *fakeGit) {
				if fg.snapshotCalls != 0 {
					t.Fatalf("expected 0 SnapshotTree calls, got %d", fg.snapshotCalls)
				}
			},
		},
		{
			name: "SnapshotTree returns git.ErrNotRepo -> EndTree empty",
			git: func() *fakeGit {
				return &fakeGit{snapshotTreeErr: git.ErrNotRepo}
			},
			baseline:           "tree-start",
			wantEndTree:        "",
			wantAvailable:      false,
			wantReasonNonEmpty: false,
			assertCalls: func(t *testing.T, fg *fakeGit) {
				if fg.snapshotCalls != 1 {
					t.Fatalf("expected 1 SnapshotTree call, got %d", fg.snapshotCalls)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := store.New(t.TempDir())
			var fg *fakeGit
			var g Git
			if !tc.nilGit {
				if tc.git != nil {
					fg = tc.git()
				} else {
					fg = &fakeGit{}
				}
				g = fg
			}
			rt := Runtime{Store: s, Git: g, LedgerPath: filepath.Join(t.TempDir(), "ledger.json")}
			b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: tc.baseline}
			if err := s.Save(b); err != nil {
				t.Fatal(err)
			}

			res := CaptureRoundDiff(ctx, rt, b)

			if res.EndTree != tc.wantEndTree {
				t.Fatalf("EndTree = %q, want %q", res.EndTree, tc.wantEndTree)
			}
			if res.Available != tc.wantAvailable {
				t.Fatalf("Available = %v, want %v", res.Available, tc.wantAvailable)
			}
			if tc.wantReasonNonEmpty && res.Reason == "" {
				t.Fatal("expected non-empty Reason")
			}
			if !tc.wantReasonNonEmpty && res.Reason != "" {
				t.Fatalf("expected empty Reason, got %q", res.Reason)
			}
			if tc.assertCalls != nil && fg != nil {
				tc.assertCalls(t, fg)
			}
		})
	}
}
