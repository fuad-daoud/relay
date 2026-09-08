package git

import (
	"errors"
)

// ErrNotRepo reports that a directory is not inside a git working tree.
var ErrNotRepo = errors.New("not a git repository")

// ErrGitUnavailable reports that the git binary could not be executed at all.
var ErrGitUnavailable = errors.New("git binary unavailable")

// ErrBranchExists reports an attempt to create a branch that already exists.
var ErrBranchExists = errors.New("branch already exists")

// ErrWorktreeDirty reports an attempt to remove a worktree with uncommitted changes without force.
var ErrWorktreeDirty = errors.New("worktree has uncommitted changes")

// DefaultMaxPatchBytes is the default byte cap for patch bodies.
const DefaultMaxPatchBytes = 4 << 20

// Stat is the shape of a diff, independent of its body. It is always available
// even when the patch itself was too large to keep.
type Stat struct {
	FilesChanged int // number of paths with any change; >= 0
	Insertions   int // added lines across all paths; >= 0
	Deletions    int // removed lines across all paths; >= 0
}

// Empty reports a diff with no changed paths.
func (s Stat) Empty() bool {
	return s.FilesChanged == 0
}

// Diff is one tree-to-tree comparison.
type Diff struct {
	Stat      Stat
	Patch     []byte // unified patch body; nil when Truncated or Stat.Empty()
	Truncated bool   // body exceeded the client's cap; Stat is still exact
}
