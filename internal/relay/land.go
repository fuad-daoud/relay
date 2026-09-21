package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrLandDirty reports a worktree with uncommitted changes: land will not
// commit for the human, and it will not rebase a tree it cannot leave exactly
// as it found it. Exit code 2.
var ErrLandDirty = errors.New("worktree has uncommitted changes; commit first, or send a round that commits")

// ErrLandConflict reports that the rebase or merge onto the base hit
// conflicts; it is wrapped with the conflicting paths. The worktree is left
// as it was (the rebase or merge was aborted). Exit code 3.
var ErrLandConflict = errors.New("rebase conflicts")

// ErrLandGate reports that the binding's gate failed on the rebased tree.
// Nothing was pushed. Exit code 2.
var ErrLandGate = errors.New("gate failed")

// ErrLandRoundOpen reports a binding with a round in flight; land refuses to
// rebase a tree a builder is still writing to unless --force says otherwise.
var ErrLandRoundOpen = errors.New("round is open; wait for it or pass --force")

// LandOptions is what one `relay land` adds to the binding's own state.
type LandOptions struct {
	// Onto is the base ref to rebase onto, overriding Binding.BaseRef. It is
	// required when the binding recorded no base ref (an older binding, a
	// --cwd binding, a --branch adoption, a detached source HEAD).
	Onto string

	// PR asks for `gh pr create` once the push has happened. Without it (or
	// with no gh on PATH) the exact command is printed instead.
	PR bool

	// NoGate skips the binding's gate for this land. Never silently: the
	// result says so.
	NoGate bool

	// Force lands a binding whose round is still open.
	Force bool

	// Merge merges origin/<base> instead of rebasing onto it, which does not
	// rewrite the branch and so needs no force-with-lease push.
	Merge bool

	// Exec runs the gate command and gh. Nil means the real os/exec: the
	// combined output is captured, and when logPath is non-empty it is written
	// there too. Tests stub it.
	Exec func(ctx context.Context, dir string, logPath string, argv ...string) (exitCode int, out string, err error)

	// LookPath decides whether gh is available. Nil means exec.LookPath.
	LookPath func(file string) (string, error)
}

// LandResult is what one successful land did, so the CLI can tell the human
// exactly how far it got without re-deriving any of it.
type LandResult struct {
	Branch string
	Base   string
	// Rebased is false when --merge integrated the base instead, so the
	// printed line never calls a merge a rebase.
	Rebased bool
	// GateResult is "pass", "skipped" (--no-gate) or "none" (no gate).
	GateResult string
	Pushed     bool
	// PRURL is the PR gh created, or "".
	PRURL string
	// PRCommand is the command that would create the PR, printed when none
	// was created.
	PRCommand string
}

// Land integrates a binding's branch with its base and publishes it: fetch,
// rebase onto origin/<base> (or merge it in with --merge), run the binding's
// gate on the rebased tree, push, and open or print the PR (#136).
//
// It is the human's verb, so it is mechanical and never merges: no PR is
// merged, no branch or worktree is deleted, and nothing is triggered by a
// marker. It stops at the first failure with nothing pushed -- except for a
// gh failure after the push, which is reported as such because the branch is
// already on origin.
//
// The store lock is taken only at the end, to append the log entry and save
// LandedAt: the git work is minutes long, and land must not block the daemon
// for it.
//
// Errors: ErrLandDirty, ErrLandConflict, ErrLandGate, ErrLandRoundOpen, or a
// wrapped git failure.
func Land(ctx context.Context, rt Runtime, name string, opts LandOptions) (LandResult, error) {
	b, err := rt.Store.Load(name)
	if err != nil {
		return LandResult{}, err
	}

	if b.Builder.Remote() {
		return LandResult{}, errors.New("remote bindings land on the server side; not supported yet")
	}
	if b.Worktree == "" {
		return LandResult{}, errors.New("nothing to land -- the planner's tree is the base")
	}
	if b.State == store.StatePaused || b.State == store.StateDone {
		return LandResult{}, fmt.Errorf("binding is %s", b.State)
	}

	base := opts.Onto
	if base == "" {
		base = b.BaseRef
	}
	if base == "" {
		return LandResult{}, fmt.Errorf("binding %q recorded no base branch; pass --onto <ref>", name)
	}
	if !opts.Force && !b.RoundStartedAt.IsZero() {
		return LandResult{}, fmt.Errorf("binding %q: round %d %w", name, b.Round, ErrLandRoundOpen)
	}
	if rt.Git == nil {
		return LandResult{}, ErrGitRequired
	}

	// Refuse before any git runs: a rebase of a dirty tree either fails
	// halfway or silently carries the builder's unfinished edits into the
	// rebased branch, and neither is a land.
	dirty, err := rt.Git.Dirty(ctx, b.Worktree)
	if err != nil {
		return LandResult{}, err
	}
	if dirty {
		return LandResult{}, fmt.Errorf("%w: %s", ErrLandDirty, b.Worktree)
	}

	if err := rt.Git.Fetch(ctx, b.Worktree, "origin", base); err != nil {
		return LandResult{}, err
	}

	res := LandResult{Branch: b.Branch, Base: base, Rebased: !opts.Merge}

	var paths []string
	if opts.Merge {
		paths, err = rt.Git.Merge(ctx, b.Worktree, "origin/"+base)
	} else {
		paths, err = rt.Git.Rebase(ctx, b.Worktree, "origin/"+base)
	}
	if err != nil {
		if errors.Is(err, git.ErrMergeConflict) {
			return LandResult{}, fmt.Errorf("%w in %s: %s", ErrLandConflict, b.Worktree, strings.Join(paths, ", "))
		}
		return LandResult{}, err
	}

	execFn := opts.Exec
	if execFn == nil {
		execFn = realExec
	}

	// The gate runs on the rebased tree, before anything leaves this machine:
	// a base that breaks the build must never reach origin.
	switch {
	case b.Gate != "" && !opts.NoGate:
		logPath := filepath.Join(rt.Store.Dir(name), "land-gate.log")
		code, out, gerr := execFn(ctx, b.Worktree, logPath, "sh", "-c", b.Gate+" 2>&1")
		if gerr != nil {
			return LandResult{}, fmt.Errorf("%w: run %q: %v", ErrLandGate, b.Gate, gerr)
		}
		if code != 0 {
			return LandResult{}, fmt.Errorf("%w (exit %d): %s; output: %s", ErrLandGate, code, lastLines(out, 5), logPath)
		}
		res.GateResult = "pass"
	case b.Gate == "":
		res.GateResult = "none"
	default:
		res.GateResult = "skipped"
	}

	exists, err := rt.Git.RemoteBranchExists(ctx, b.Worktree, "origin", b.Branch)
	if err != nil {
		return LandResult{}, err
	}
	// A rebase rewrote the branch, so a branch already on the remote is no
	// longer a fast-forward and needs the lease. --merge does not rewrite it.
	if err := rt.Git.Push(ctx, b.Worktree, "origin", b.Branch, exists && !opts.Merge); err != nil {
		return LandResult{}, err
	}
	res.Pushed = true

	prCmd := []string{"gh", "pr", "create", "--head", b.Branch, "--base", base, "--fill"}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if opts.PR && lookPathOK(lookPath, "gh") {
		code, out, gerr := execFn(ctx, b.Worktree, "", prCmd...)
		if gerr != nil || code != 0 {
			// The push already happened: the branch is on origin, so the
			// human only has to open the PR. Say both.
			return LandResult{}, fmt.Errorf("gh pr create failed: %s; the push already happened, so the branch is on origin -- open the PR with: %s",
				lastLines(out, 5), strings.Join(prCmd, " "))
		}
		res.PRURL = lastNonEmptyLine(out)
	} else {
		res.PRCommand = strings.Join(prCmd, " ")
	}

	note := fmt.Sprintf("landed %s -> %s", b.Branch, base)
	if res.PRURL != "" {
		note = fmt.Sprintf("landed %s -> %s pr %s", b.Branch, base, res.PRURL)
	}

	now := time.Now().UTC()
	if rt.Now != nil {
		now = rt.Now().UTC()
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		// Re-load under the lock: the daemon rewrites this binding every
		// tick, and saving the snapshot the git work started from would undo
		// every tick since.
		bb, err := tx.Load(name)
		if err != nil {
			return err
		}
		if err := tx.AppendLog(name, store.LogEntry{
			TS:        now,
			Round:     bb.Round,
			Direction: store.DirToPlanner,
			Kind:      store.KindLand,
			Confirmed: true,
			Note:      note,
		}); err != nil {
			return err
		}
		bb.LandedAt = now
		bb.LandedPR = res.PRURL
		return tx.Save(bb)
	}); err != nil {
		return LandResult{}, err
	}

	return res, nil
}

// realExec is LandOptions.Exec's default: run argv in dir with
// exec.CommandContext, capture the combined output, and -- when logPath is
// non-empty -- write that output there as well. A non-zero exit is reported
// through the exit code, not as an error: the caller judges it.
func realExec(ctx context.Context, dir string, logPath string, argv ...string) (int, string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := string(out)

	if logPath != "" {
		if werr := os.WriteFile(logPath, out, 0o644); werr != nil {
			return -1, text, werr
		}
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), text, nil
		}
		return -1, text, err
	}
	return 0, text, nil
}

// lookPathOK reports whether look resolves file, ignoring the path it found.
func lookPathOK(look func(file string) (string, error), file string) bool {
	_, err := look(file)
	return err == nil
}

// lastLines returns the last n non-empty lines of s, joined by newlines. It
// bounds what an error message quotes from a failing command.
func lastLines(s string, n int) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, "\n")
}

// lastNonEmptyLine returns the last non-blank line of s, or "" when there is
// none. `gh pr create` prints the PR URL last, so that line is the URL.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
