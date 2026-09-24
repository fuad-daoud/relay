package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestCaptureRoundDiff_NilGit(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	rt := Runtime{Store: s, Gates: testGateKV(t)}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "abc"}

	res := CaptureRoundDiff(ctx, rt, b)
	if res.Available {
		t.Fatal("expected Available=false with nil Git")
	}
	if line := DiffLine(res, CommitResult{}, ""); line != "" {
		t.Fatalf("expected empty line for nil Git, got %q", line)
	}
}

func TestCaptureRoundDiff_ErrNotRepo(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{snapshotTreeErr: git.ErrNotRepo}
	rt := Runtime{Store: s, Git: fg, Gates: testGateKV(t)}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "abc"}

	res := CaptureRoundDiff(ctx, rt, b)
	if res.Available {
		t.Fatal("expected Available=false for ErrNotRepo")
	}
	if line := DiffLine(res, CommitResult{}, ""); line != "" {
		t.Fatalf("expected empty line for ErrNotRepo, got %q", line)
	}
}

func TestCaptureRoundDiff_GitFailure(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{snapshotTreeErr: errors.New("boom: git broken")}
	rt := Runtime{Store: s, Git: fg, Gates: testGateKV(t)}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineTree: "abc"}

	res := CaptureRoundDiff(ctx, rt, b)
	if res.Available {
		t.Fatal("expected Available=false for git failure")
	}
	if res.Reason != "boom: git broken" {
		t.Fatalf("unexpected Reason: %q", res.Reason)
	}
	wantLine := "Diff: unavailable (boom: git broken)"
	if line := DiffLine(res, CommitResult{}, ""); line != wantLine {
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
	rt := Runtime{Store: s, Git: fg, Gates: testGateKV(t)}
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
	if line := DiffLine(res, CommitResult{}, ""); line != wantLine {
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
	rt := Runtime{Store: s, Git: fg, Gates: testGateKV(t)}
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
	if line := DiffLine(res, CommitResult{}, ""); line != wantLine {
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
	rt := Runtime{Store: s, Git: fg, Gates: testGateKV(t)}
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
	if line := DiffLine(res, CommitResult{}, ""); line != wantLine {
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
	b := store.Binding{Name: "webshop", CWD: "/repo"}
	newRT := func(g Git) Runtime {
		return Runtime{Store: s, Git: g, Gates: testGateKV(t)}
	}

	tree, head := CaptureBaseline(ctx, newRT(&fakeGit{snapshotTreeID: "tree-base", headCommitID: "head-base"}), b)
	if tree != "tree-base" || head != "head-base" {
		t.Fatalf("got (%q, %q), want (tree-base, head-base)", tree, head)
	}

	if tree, head := CaptureBaseline(ctx, newRT(nil), b); tree != "" || head != "" {
		t.Fatalf("nil git: got (%q, %q), want both empty", tree, head)
	}

	fgSnap := &fakeGit{snapshotTreeErr: errors.New("fail"), headCommitID: "head-base"}
	if tree, head := CaptureBaseline(ctx, newRT(fgSnap), b); tree != "" || head != "" {
		t.Fatalf("snapshot failure: got (%q, %q), want both empty", tree, head)
	}
	if fgSnap.headCalls != 0 {
		t.Fatalf("HeadCommit called %d times after a failed snapshot, want 0", fgSnap.headCalls)
	}

	if tree, head := CaptureBaseline(ctx, newRT(&fakeGit{snapshotTreeID: "tree-base", headCommitErr: errors.New("unborn")}), b); tree != "tree-base" || head != "" {
		t.Fatalf("head failure: got (%q, %q), want (tree-base, \"\")", tree, head)
	}
}

func TestCaptureRoundDiff_EmptyBaselineSkipsSnapshot(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	fg := &fakeGit{snapshotTreeID: "tree-end"}
	rt := Runtime{Store: s, Git: fg, Gates: testGateKV(t)}
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
			rt := Runtime{Store: s, Git: g, Gates: testGateKV(t)}
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

func TestCommitFacts(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	newRT := func(g Git) Runtime {
		return Runtime{Store: s, Git: g, Gates: testGateKV(t)}
	}
	withHead := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineHead: "head-start"}

	t.Run("nil git", func(t *testing.T) {
		got := CommitFacts(ctx, newRT(nil), withHead)
		if got.Known || got.Reason != "" {
			t.Fatalf("got %+v, want Known=false, Reason empty", got)
		}
	})

	t.Run("no baseline head", func(t *testing.T) {
		fg := &fakeGit{headCommitID: "h"}
		got := CommitFacts(ctx, newRT(fg), store.Binding{Name: "webshop", CWD: "/repo", Round: 1})
		if got.Known || got.Reason != "no baseline" {
			t.Fatalf("got %+v, want Reason \"no baseline\"", got)
		}
		if fg.headCalls != 0 || fg.revListCalls != 0 || fg.dirtyCalls != 0 {
			t.Fatalf("git was called without a baseline: %+v", fg)
		}
	})

	t.Run("head fails", func(t *testing.T) {
		fg := &fakeGit{headCommitErr: errors.New("boom: head")}
		got := CommitFacts(ctx, newRT(fg), withHead)
		if got.Known || got.Reason != "head: boom: head" {
			t.Fatalf("got %+v, want Reason \"head: boom: head\"", got)
		}
		if fg.revListCalls != 0 || fg.dirtyCalls != 0 {
			t.Fatalf("sequence did not stop at the first failure: %+v", fg)
		}
	})

	t.Run("rev-list fails", func(t *testing.T) {
		fg := &fakeGit{headCommitID: "head-end", revListErr: errors.New("boom: rev-list")}
		got := CommitFacts(ctx, newRT(fg), withHead)
		if got.Known || got.Reason != "rev-list: boom: rev-list" {
			t.Fatalf("got %+v, want Reason \"rev-list: boom: rev-list\"", got)
		}
		if fg.dirtyCalls != 0 {
			t.Fatalf("Dirty called after rev-list failed: %+v", fg)
		}
	})

	t.Run("dirty check fails", func(t *testing.T) {
		fg := &fakeGit{headCommitID: "head-end", revListCount: 2, dirtyErr: errors.New("boom: status")}
		got := CommitFacts(ctx, newRT(fg), withHead)
		if got.Known || got.Reason != "dirty check: boom: status" {
			t.Fatalf("got %+v, want Reason \"dirty check: boom: status\"", got)
		}
	})

	t.Run("not a repository is silent", func(t *testing.T) {
		fg := &fakeGit{headCommitErr: fmt.Errorf("%w: nope", git.ErrNotRepo)}
		got := CommitFacts(ctx, newRT(fg), withHead)
		if got.Known || got.Reason != "" {
			t.Fatalf("got %+v, want Known=false with empty Reason", got)
		}
	})

	t.Run("all succeed", func(t *testing.T) {
		fg := &fakeGit{headCommitID: "head-end", revListCount: 3, dirtyResult: true}
		got := CommitFacts(ctx, newRT(fg), withHead)
		want := CommitResult{Known: true, Commits: 3, Dirty: true}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
		if fg.lastRevListDir != "/repo" || fg.lastRevListFrom != "head-start" || fg.lastRevListTo != "head-end" {
			t.Fatalf("rev-list range: dir=%q from=%q to=%q", fg.lastRevListDir, fg.lastRevListFrom, fg.lastRevListTo)
		}
		if fg.lastDirtyDir != "/repo" {
			t.Fatalf("Dirty dir = %q, want /repo", fg.lastDirtyDir)
		}
	})
}

func TestDiffTextWithCommitFacts(t *testing.T) {
	normal := DiffResult{Available: true, Path: "/p/007-diff.patch", Stat: git.Stat{FilesChanged: 6, Insertions: 120, Deletions: 30}}
	empty := DiffResult{Available: true}
	truncated := DiffResult{Available: true, Truncated: true, Stat: git.Stat{FilesChanged: 312, Insertions: 48120, Deletions: 9033}}
	unavailable := DiffResult{Available: false, Reason: "no baseline"}
	silent := DiffResult{Available: false}

	cases := []struct {
		name        string
		res         DiffResult
		facts       CommitResult
		branch      string
		wantSummary string
		wantLine    string
	}{
		{"commits clean with branch", normal, CommitResult{Known: true, Commits: 3}, "relevo/api-auth",
			"6 files, +120 -30; 3 commits, clean",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- 3 commits on relevo/api-auth, tree clean"},
		{"one commit singular", normal, CommitResult{Known: true, Commits: 1}, "relevo/api-auth",
			"6 files, +120 -30; 1 commit, clean",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- 1 commit on relevo/api-auth, tree clean"},
		{"commits dirty with branch", normal, CommitResult{Known: true, Commits: 3, Dirty: true}, "relevo/api-auth",
			"6 files, +120 -30; 3 commits, dirty",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- 3 commits on relevo/api-auth, tree dirty"},
		{"commits clean without branch", normal, CommitResult{Known: true, Commits: 3}, "",
			"6 files, +120 -30; 3 commits, clean",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- 3 commits, tree clean"},
		{"no commits dirty", normal, CommitResult{Known: true, Commits: 0, Dirty: true}, "relevo/api-auth",
			"6 files, +120 -30; no commits, dirty",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- no commits; changes are uncommitted in the worktree"},
		{"no commits clean", normal, CommitResult{Known: true}, "relevo/api-auth",
			"6 files, +120 -30; no commits, clean",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- no commits, tree clean"},
		{"unknown with reason", normal, CommitResult{Reason: "rev-list: boom"}, "relevo/api-auth",
			"6 files, +120 -30; commits unknown (rev-list: boom)",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- commits unknown (rev-list: boom)"},
		{"unknown silent", normal, CommitResult{}, "relevo/api-auth",
			"6 files, +120 -30",
			"Diff: /p/007-diff.patch (6 files, +120 -30)"},
		{"empty diff gains nothing", empty, CommitResult{Known: true, Commits: 2}, "relevo/api-auth",
			"no changes",
			"Diff: no file changes"},
		{"truncated keeps the clause", truncated, CommitResult{Known: true, Dirty: true}, "",
			"truncated; no commits, dirty",
			"Diff: 312 files, +48120 -9033 (patch omitted, over the 4 MiB cap) -- no commits; changes are uncommitted in the worktree"},
		{"unavailable keeps the clause", unavailable, CommitResult{Known: true, Commits: 2}, "relevo/api-auth",
			"unavailable: no baseline; 2 commits, clean",
			"Diff: unavailable (no baseline) -- 2 commits on relevo/api-auth, tree clean"},
		{"unavailable and unknown", unavailable, CommitResult{Reason: "no baseline"}, "",
			"unavailable: no baseline; commits unknown (no baseline)",
			"Diff: unavailable (no baseline) -- commits unknown (no baseline)"},
		{"silent diff stays silent", silent, CommitResult{Known: true, Commits: 2}, "relevo/api-auth",
			"unavailable; 2 commits, clean",
			""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DiffSummary(tc.res, tc.facts); got != tc.wantSummary {
				t.Errorf("DiffSummary = %q, want %q", got, tc.wantSummary)
			}
			if got := DiffLine(tc.res, tc.facts, tc.branch); got != tc.wantLine {
				t.Errorf("DiffLine = %q, want %q", got, tc.wantLine)
			}
		})
	}
}

// TestDiffLineFromNoteMatchesDiffLine checks DiffLineFromNote against
// DiffLine's own output for the same res/facts pair, over the note shapes
// where the wire facts (Note, Commits, Tree) carry everything DiffLine
// needs: an available diff always has a res.Path DiffLineFromNote never
// sees, so those cases are deliberately not compared here (see the
// function's doc comment).
func TestDiffLineFromNoteMatchesDiffLine(t *testing.T) {
	unavailable := DiffResult{Available: false, Reason: "no baseline"}
	empty := DiffResult{Available: true}
	silent := DiffResult{Available: false}
	branch := "relevo/api-auth"

	cases := []struct {
		name  string
		res   DiffResult
		facts CommitResult
		tree  string
	}{
		// Mutation target: drop the trailing commitClause append for the
		// unavailable case and this stops matching DiffLine, which appends
		// it too.
		{"unavailable keeps the clause", unavailable, CommitResult{Known: true, Commits: 2}, "clean"},
		// Mutation target: treat "no changes" like the default case (append
		// a clause) and this starts differing from DiffLine, which returns
		// early with no clause on an empty diff.
		{"empty diff gains nothing", empty, CommitResult{Known: true, Commits: 2}, "clean"},
		// Mutation target: drop the "base == \"unavailable\"" short-circuit
		// and this returns a non-empty line instead of "", unlike DiffLine
		// on a result with no reason.
		{"silent diff stays silent", silent, CommitResult{Known: true, Commits: 2}, "clean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			note := DiffSummary(tc.res, tc.facts)
			want := DiffLine(tc.res, tc.facts, branch)
			if got := DiffLineFromNote(note, tc.facts.Commits, tc.tree, branch); got != want {
				t.Fatalf("DiffLineFromNote(%q, %d, %q, %q) = %q, want %q (DiffLine's own output for the same facts)",
					note, tc.facts.Commits, tc.tree, branch, got, want)
			}
		})
	}
}

// TestPathsLine pins the payload line for a changed_paths mismatch (#216):
// its exact wording, and both plural forms formatFiles renders.
func TestPathsLine(t *testing.T) {
	if got, want := PathsLine(0, 24), "Paths: the report's changed_paths lists 0, the diff has 24 files -- check the diff, not the list"; got != want {
		t.Errorf("PathsLine(0, 24) = %q, want %q", got, want)
	}
	if got, want := PathsLine(2, 1), "Paths: the report's changed_paths lists 2, the diff has 1 file -- check the diff, not the list"; got != want {
		t.Errorf("PathsLine(2, 1) = %q, want %q", got, want)
	}
	if !strings.HasSuffix(PathsLine(2, 1), "1 file -- check the diff, not the list") {
		t.Errorf("PathsLine(2, 1) = %q, want it to end '1 file -- check the diff, not the list'", PathsLine(2, 1))
	}
}

// TestPathsLineFromNote pins the reading of the clause out of a KindDiff
// note (#216), including the position joinNotes leaves it in and the
// no-clause cases that must stay silent.
func TestPathsLineFromNote(t *testing.T) {
	joined := "24 files, +1 -2; 1 commit on relevo/x, tree clean paths: report 0, diff 24"
	if got, want := PathsLineFromNote(joined), PathsLine(0, 24); got != want {
		t.Errorf("PathsLineFromNote(%q) = %q, want %q", joined, got, want)
	}
	if got := PathsLineFromNote("3 files, +1 -1"); got != "" {
		t.Errorf("PathsLineFromNote(%q) = %q, want \"\"", "3 files, +1 -1", got)
	}
	if got := PathsLineFromNote(""); got != "" {
		t.Errorf("PathsLineFromNote(\"\") = %q, want \"\"", got)
	}
}
