package migrate

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestOrphanWorktrees(t *testing.T) {
	t.Run("finds linked worktrees and skips clones and empty dirs", func(t *testing.T) {
		base := t.TempDir()

		a := gitFileTree(t, filepath.Join(base, ".worktrees", "a"))
		verify := gitFileTree(t, filepath.Join(base, ".worktrees", ".verify", "b-001"))
		// o1 has no bind.json, yet its orphan must still be found.
		c := gitFileTree(t, filepath.Join(base, "serve", "bindings", "o1", ".worktrees", "c"))

		mustMkdir(t, filepath.Join(base, ".worktrees", "full", ".git"))
		mustMkdir(t, filepath.Join(base, ".worktrees", "empty"))

		want := []string{a, verify, c}
		sort.Strings(want)
		if got := orphanWorktrees(base); !reflect.DeepEqual(got, want) {
			t.Errorf("orphanWorktrees = %v, want %v", got, want)
		}
	})

	t.Run("finds scratch worktrees beside verify", func(t *testing.T) {
		base := t.TempDir()
		scratch := gitFileTree(t, filepath.Join(base, ".worktrees", ".scratch", "api-001"))

		if got := orphanWorktrees(base); !reflect.DeepEqual(got, []string{scratch}) {
			t.Errorf("orphanWorktrees = %v, want [%s]", got, scratch)
		}
	})

	t.Run("works with no binding at all", func(t *testing.T) {
		base := t.TempDir()
		a := gitFileTree(t, filepath.Join(base, ".worktrees", "a"))

		if got := orphanWorktrees(base); !reflect.DeepEqual(got, []string{a}) {
			t.Errorf("orphanWorktrees = %v, want [%s]", got, a)
		}
	})
}

func TestAdminRepo(t *testing.T) {
	base := t.TempDir()
	from := filepath.Join(base, "old")
	to := filepath.Join(base, "new")

	tests := []struct {
		name      string
		content   string
		mkdir     []string
		wantRepo  string
		wantAdmin string
		wantErr   string
	}{
		{
			name:      "inside from is mapped to to and the repo is the bare one",
			content:   filepath.Join(from, "serve", "repos", "o", "r.git", "worktrees", "w"),
			mkdir:     []string{filepath.Join(to, "serve", "repos", "o", "r.git", "worktrees", "w")},
			wantRepo:  filepath.Join(to, "serve", "repos", "o", "r.git"),
			wantAdmin: filepath.Join(to, "serve", "repos", "o", "r.git", "worktrees", "w"),
		},
		{
			name:      "outside from is not mapped and /.git is stripped from the repo",
			content:   filepath.Join(base, "x", "proj", ".git", "worktrees", "n"),
			mkdir:     []string{filepath.Join(base, "x", "proj", ".git", "worktrees", "n")},
			wantRepo:  filepath.Join(base, "x", "proj"),
			wantAdmin: filepath.Join(base, "x", "proj", ".git", "worktrees", "n"),
		},
		{
			name:    "malformed .git file",
			content: "not a gitdir line",
			wantErr: "gitdir",
		},
		{
			name:    "no /worktrees/<name> suffix",
			content: filepath.Join(base, "x", "r.git"),
			wantErr: "worktrees",
		},
		{
			name:    "admin dir missing on disk",
			content: filepath.Join(base, "x", "r.git", "worktrees", "missing"),
			wantErr: "missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wt := filepath.Join(base, "wt", strings.ReplaceAll(tt.name, " ", "-"))
			mustMkdir(t, wt)
			mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+tt.content+"\n")
			for _, dir := range tt.mkdir {
				mustMkdir(t, dir)
			}

			repo, admin, err := adminRepo(wt, from, to)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("adminRepo error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("adminRepo: %v", err)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
			if admin != tt.wantAdmin {
				t.Errorf("admin = %q, want %q", admin, tt.wantAdmin)
			}
		})
	}
}
