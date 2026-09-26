package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// orphanWorktrees lists the linked worktrees under base that no binding
// records. After a move their .git file still names the old root, so git
// refuses to work in them until they too are repaired. base is named
// directly, not through storeRoots, since a root with no bindings left can
// still hold orphans.
func orphanWorktrees(base string) []string {
	var found []string
	for _, dir := range worktreeDirs(base) {
		found = append(found, linkedWorktrees(dir)...)
		found = append(found, linkedWorktrees(filepath.Join(dir, ".verify"))...)
		found = append(found, linkedWorktrees(filepath.Join(dir, ".scratch"))...)
	}
	sort.Strings(found)
	return found
}

// worktreeDirs lists the .worktrees directories base can hold: its own, plus
// one per owner directory under serve/bindings.
func worktreeDirs(base string) []string {
	dirs := []string{filepath.Join(base, ".worktrees")}
	entries, err := os.ReadDir(filepath.Join(base, "serve", "bindings"))
	if err != nil {
		return dirs
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(base, "serve", "bindings", e.Name(), ".worktrees"))
		}
	}
	return dirs
}

// linkedWorktrees lists the linked worktrees directly under dir, as absolute
// cleaned paths: a directory holding a regular file named .git (a .git
// directory is a full clone and is skipped).
func linkedWorktrees(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(filepath.Join(path, ".git"))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		found = append(found, filepath.Clean(abs))
	}
	return found
}

// adminRepo derives the repository a linked worktree belongs to from the
// gitdir line of wt/.git, mapping the admin path from the old root (from) to
// the new one (to); from == to maps nothing.
func adminRepo(wt, from, to string) (repo, admin string, err error) {
	data, err := os.ReadFile(filepath.Join(wt, ".git"))
	if err != nil {
		return "", "", err
	}

	const prefix = "gitdir: "
	line := strings.TrimSuffix(string(data), "\n")
	if !strings.HasPrefix(line, prefix) || strings.ContainsAny(line, "\r\n") {
		return "", "", fmt.Errorf("%s: .git is not a single \"gitdir: <path>\" line", wt)
	}
	admin = strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if admin == "" {
		return "", "", fmt.Errorf("%s: .git gitdir line names no path", wt)
	}

	if from != to {
		switch {
		case admin == from:
			admin = to
		case strings.HasPrefix(admin, from+"/"):
			admin = to + strings.TrimPrefix(admin, from)
		}
	}

	const marker = "/worktrees/"
	i := strings.LastIndex(admin, marker)
	if i < 0 || i+len(marker) == len(admin) {
		return "", "", fmt.Errorf("%s: gitdir %s does not end in /worktrees/<name>", wt, admin)
	}
	// A non-bare repo is named by its worktree root, so the administrative
	// ".git" the gitdir points through is not part of the repo's name. A bare
	// repo keeps its own name, which never ends in "/.git".
	repo = strings.TrimSuffix(admin[:i], "/.git")
	if repo == "" {
		return "", "", fmt.Errorf("%s: gitdir %s names no repository", wt, admin)
	}

	if _, err := os.Stat(admin); err != nil {
		return "", "", fmt.Errorf("%s: admin directory %s: %w", wt, admin, err)
	}
	return repo, admin, nil
}
