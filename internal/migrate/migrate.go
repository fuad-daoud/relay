// Package migrate is the core of `relevo migrate` (#292): detection and
// refusals, the directory moves, the database file renames, the rewriting of
// old-root paths inside relevo's own JSON records and database, and
// `git worktree repair`.
//
// This is round 3a, so the package is a library: the CLI verb, the units and
// the old binary are round 3b's. The two calls that would drag internal/git
// and internal/db in are function values on Options, so tests use fakes and
// the CLI supplies the real ones.
//
// Every step decides "already done" from the filesystem alone, so a re-run
// after a crash between any two steps resumes, and the rewrites are
// idempotent.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Options configures one Run.
type Options struct {
	// StateFrom and StateTo are the old and the new state root. Required,
	// absolute and distinct.
	StateFrom, StateTo string
	// ConfigFrom and ConfigTo are the old and the new config root: both ""
	// (no config to move) or both set.
	ConfigFrom, ConfigTo string
	// DryRun makes Run report every step without writing, renaming or calling
	// Repair or RewriteDB.
	DryRun bool
	// DefaultServeRoot is the old default serve root, where a running
	// `relevo serve` writes daemon.json even when it serves an explicit
	// --state. "" means StateFrom/serve only.
	DefaultServeRoot string
	// SkipDaemonCheck drops the daemon-lock refusal only. The CLI runs a dry
	// run with it set before stopping the service -- the daemon is still up
	// then -- and the real run without it after the stop.
	SkipDaemonCheck bool

	// Alive reports whether a pid is live. Required.
	Alive func(pid int) bool
	// Repair repairs one git worktree:
	// `git -C <repo> worktree repair <worktree>`. Required.
	Repair func(ctx context.Context, repo, worktree string) error
	// RewriteDB rewrites old-root paths in the database at dbPath. Required.
	RewriteDB func(ctx context.Context, dbPath string, pairs []Prefix) (rows int64, newer bool, err error)
	// Out receives one line per step. Required.
	Out io.Writer
}

// Prefix is one absolute-directory substitution: every stored path that is Old
// exactly, or starts with Old followed by "/", becomes New plus the same tail.
type Prefix struct{ Old, New string }

// Step is one line of the migration report.
type Step struct {
	// Name is the step: refuse, move-config, move-state, rename-db,
	// rewrite-json, rewrite-db or repair-worktrees.
	Name string
	// Detail is the human line: what was (or would be) done, or why it was
	// skipped.
	Detail string
	// Skipped is true when the step was already done (resume) or did not
	// apply.
	Skipped bool
	// Warn is true when the step was done with a caveat the user must see: a
	// newer database, or a failed repair.
	Warn bool
}

// Result is what Run found and did.
type Result struct {
	Steps []Step
	// Nothing is true when nothing old was found: no step ran.
	Nothing bool
}

// ErrRefused is what every Refusal unwraps to.
var ErrRefused = errors.New("migrate: refused")

// Refusal reports every reason the migration was refused, not just the first,
// so the user can fix them all in one go.
type Refusal struct{ Reasons []string }

// Error renders every reason, one per line.
func (r *Refusal) Error() string {
	var b strings.Builder
	b.WriteString("migrate: refused:")
	for _, reason := range r.Reasons {
		b.WriteString("\n  - ")
		b.WriteString(reason)
	}
	return b.String()
}

// Unwrap reports ErrRefused.
func (r *Refusal) Unwrap() error { return ErrRefused }

// Run performs the migration in the fixed step order, writing one line per
// step to o.Out and returning the steps it took.
//
// A missing required option is a programming error and is returned plain. An
// install that is not safe to move is returned as a *Refusal wrapping
// ErrRefused, after every reason has been collected. Any other failure names
// the step it happened in and leaves the state for a re-run to resume.
func Run(ctx context.Context, o Options) (Result, error) {
	if err := validateOptions(o); err != nil {
		return Result{}, err
	}

	r := &runner{opts: o}

	d, err := detect(o)
	if err != nil {
		return r.result, fmt.Errorf("detect: %w", err)
	}
	if !d.anyOld() {
		return Result{Nothing: true}, nil
	}

	if reasons := r.refusals(d); len(reasons) > 0 {
		r.addStep("refuse", strings.Join(reasons, "; "), false, false)
		return r.result, &Refusal{Reasons: reasons}
	}

	if err := r.moveConfig(d); err != nil {
		return r.result, err
	}
	if err := r.moveState(d); err != nil {
		return r.result, err
	}
	if err := r.renameDB(d); err != nil {
		return r.result, err
	}
	if err := r.rewriteJSON(d); err != nil {
		return r.result, err
	}
	if err := r.rewriteDB(ctx, d); err != nil {
		return r.result, err
	}
	r.repairWorktrees(ctx, d)

	return r.result, nil
}

// validateOptions rejects a missing or nonsensical option before any of the
// filesystem is touched. Every failure here is a programming error, not a
// refusal, so it is returned plain.
func validateOptions(o Options) error {
	if o.StateFrom == "" {
		return errors.New("migrate: StateFrom is required")
	}
	if o.StateTo == "" {
		return errors.New("migrate: StateTo is required")
	}
	if !filepath.IsAbs(o.StateFrom) {
		return fmt.Errorf("migrate: StateFrom %q is not absolute", o.StateFrom)
	}
	if !filepath.IsAbs(o.StateTo) {
		return fmt.Errorf("migrate: StateTo %q is not absolute", o.StateTo)
	}
	if filepath.Clean(o.StateFrom) == filepath.Clean(o.StateTo) {
		return fmt.Errorf("migrate: StateFrom and StateTo are the same directory %q", o.StateFrom)
	}
	if (o.ConfigFrom == "") != (o.ConfigTo == "") {
		return errors.New("migrate: ConfigFrom and ConfigTo must both be set or both empty")
	}
	if o.ConfigFrom != "" {
		if !filepath.IsAbs(o.ConfigFrom) {
			return fmt.Errorf("migrate: ConfigFrom %q is not absolute", o.ConfigFrom)
		}
		if !filepath.IsAbs(o.ConfigTo) {
			return fmt.Errorf("migrate: ConfigTo %q is not absolute", o.ConfigTo)
		}
		if filepath.Clean(o.ConfigFrom) == filepath.Clean(o.ConfigTo) {
			return fmt.Errorf("migrate: ConfigFrom and ConfigTo are the same directory %q", o.ConfigFrom)
		}
	}
	if o.Alive == nil {
		return errors.New("migrate: Alive is required")
	}
	if o.Repair == nil {
		return errors.New("migrate: Repair is required")
	}
	if o.RewriteDB == nil {
		return errors.New("migrate: RewriteDB is required")
	}
	if o.Out == nil {
		return errors.New("migrate: Out is required")
	}
	return nil
}

// runner accumulates the steps and knows how to report them.
type runner struct {
	opts   Options
	result Result
}

// addStep records one step and writes its line. A dry run prefixes every line,
// so the user cannot mistake a report for a performed migration.
func (r *runner) addStep(name, detail string, skipped, warn bool) {
	r.result.Steps = append(r.result.Steps, Step{Name: name, Detail: detail, Skipped: skipped, Warn: warn})
	prefix := ""
	if r.opts.DryRun {
		prefix = "(dry run) "
	}
	fmt.Fprintf(r.opts.Out, "%s%s: %s\n", prefix, name, detail)
}

// pairs is the substitution list every rewrite gets: the state root always,
// the config root when there is one.
func (r *runner) pairs() []Prefix {
	pairs := []Prefix{{Old: r.opts.StateFrom, New: r.opts.StateTo}}
	if r.opts.ConfigFrom != "" {
		pairs = append(pairs, Prefix{Old: r.opts.ConfigFrom, New: r.opts.ConfigTo})
	}
	return pairs
}

// stateBase is the directory that holds the state root the later steps act on:
// StateTo once it exists -- after this run's move, or because it was already
// moved -- and in a dry run the old root that would become it.
func (r *runner) stateBase(d detection) string {
	if dirExists(r.opts.StateTo) {
		return r.opts.StateTo
	}
	if r.opts.DryRun && d.stateOld {
		return r.opts.StateFrom
	}
	return ""
}

// configBase is stateBase for the config root, "" when no config pair is set.
func (r *runner) configBase(d detection) string {
	if !d.configPair {
		return ""
	}
	if dirExists(r.opts.ConfigTo) {
		return r.opts.ConfigTo
	}
	if r.opts.DryRun && d.confOld {
		return r.opts.ConfigFrom
	}
	return ""
}

// moveConfig moves or merges the config root. A conflict was refused before
// this runs, so the merge never has to decide one.
func (r *runner) moveConfig(d detection) error {
	o := r.opts
	if !d.configPair {
		r.addStep("move-config", "no config root", true, false)
		return nil
	}
	switch {
	case d.confOld && !d.confNew:
		if o.DryRun {
			r.addStep("move-config", fmt.Sprintf("rename %s to %s", o.ConfigFrom, o.ConfigTo), false, false)
			return nil
		}
		if err := moveRoot(o.ConfigFrom, o.ConfigTo); err != nil {
			return fmt.Errorf("move-config: %w", err)
		}
		r.addStep("move-config", fmt.Sprintf("renamed %s to %s", o.ConfigFrom, o.ConfigTo), false, false)
	case d.confOld && d.confNew:
		if o.DryRun {
			r.addStep("move-config", fmt.Sprintf("merge %s into %s", o.ConfigFrom, o.ConfigTo), false, false)
			return nil
		}
		if err := moveRoot(o.ConfigFrom, o.ConfigTo); err != nil {
			return fmt.Errorf("move-config: %w", err)
		}
		r.addStep("move-config", fmt.Sprintf("merged %s into %s", o.ConfigFrom, o.ConfigTo), false, false)
	case !d.confOld && d.confNew:
		r.addStep("move-config", "already moved", true, false)
	default:
		r.addStep("move-config", "no config root", true, false)
	}
	return nil
}

// moveState moves or merges the state root, exactly as moveConfig does. A new
// root that exists and is not empty was refused before this runs, so a merge
// only ever fills an empty or already-moved root.
func (r *runner) moveState(d detection) error {
	o := r.opts
	switch {
	case d.stateOld && !d.stateNew:
		if o.DryRun {
			r.addStep("move-state", fmt.Sprintf("rename %s to %s", o.StateFrom, o.StateTo), false, false)
			return nil
		}
		if err := moveRoot(o.StateFrom, o.StateTo); err != nil {
			return fmt.Errorf("move-state: %w", err)
		}
		r.addStep("move-state", fmt.Sprintf("renamed %s to %s", o.StateFrom, o.StateTo), false, false)
	case d.stateOld && d.stateNew:
		if o.DryRun {
			r.addStep("move-state", fmt.Sprintf("merge %s into %s", o.StateFrom, o.StateTo), false, false)
			return nil
		}
		if err := moveRoot(o.StateFrom, o.StateTo); err != nil {
			return fmt.Errorf("move-state: %w", err)
		}
		r.addStep("move-state", fmt.Sprintf("merged %s into %s", o.StateFrom, o.StateTo), false, false)
	case !d.stateOld && d.stateNew:
		r.addStep("move-state", "already moved", true, false)
	default:
		r.addStep("move-state", "no state root", true, false)
	}
	return nil
}

// moveRoot renames from to to when to does not exist. When to exists it merges
// the two: an entry absent from to is renamed in, an identical regular file in
// both is dropped from from, and anything else is an error -- the refusal
// phase normally catches that case first.
func moveRoot(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return fmt.Errorf("read %s: %w", from, err)
	}

	if _, err := os.Stat(to); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("rename %s to %s: %w", from, to, err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", to, err)
	}

	for _, e := range entries {
		src := filepath.Join(from, e.Name())
		dst := filepath.Join(to, e.Name())
		di, err := os.Lstat(dst)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("rename %s to %s: %w", src, dst, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("stat %s: %w", dst, err)
		}
		if di.Mode().IsRegular() && e.Type().IsRegular() && sameBytes(src, dst) {
			if err := os.Remove(src); err != nil {
				return fmt.Errorf("remove duplicate %s: %w", src, err)
			}
			continue
		}
		return fmt.Errorf("merge %s into %s: %s and %s both exist and differ", from, to, src, dst)
	}

	if err := os.Remove(from); err != nil {
		return fmt.Errorf("remove %s: %w", from, err)
	}
	return nil
}

// renameDB renames the old database file and its -wal and -shm siblings inside
// the new state root, each one only where the new name is not already taken.
func (r *runner) renameDB(d detection) error {
	base := r.stateBase(d)
	if base == "" {
		r.addStep("rename-db", "no state root", true, false)
		return nil
	}

	baseDB := store.New(base).DBPath()
	var suffixes []string
	for _, s := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(filepath.Join(base, legacy.DBFile+s)); err != nil {
			continue
		}
		if _, err := os.Stat(baseDB + s); err == nil {
			// Both names already exist: leave them, the new one wins.
			continue
		}
		suffixes = append(suffixes, s)
	}
	if len(suffixes) == 0 {
		r.addStep("rename-db", "already renamed", true, false)
		return nil
	}

	oldNames := make([]string, 0, len(suffixes))
	newNames := make([]string, 0, len(suffixes))
	for _, s := range suffixes {
		oldNames = append(oldNames, legacy.DBFile+s)
		newNames = append(newNames, filepath.Base(baseDB)+s)
	}

	if r.opts.DryRun {
		r.addStep("rename-db",
			fmt.Sprintf("rename %s to %s in %s", strings.Join(oldNames, ", "), strings.Join(newNames, ", "), base),
			false, false)
		return nil
	}

	for _, s := range suffixes {
		src := filepath.Join(base, legacy.DBFile+s)
		dst := baseDB + s
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("rename-db: rename %s to %s: %w", src, dst, err)
		}
	}
	r.addStep("rename-db",
		fmt.Sprintf("renamed %s to %s", strings.Join(oldNames, ", "), strings.Join(newNames, ", ")),
		false, false)
	return nil
}

// rewriteJSON rewrites the old-root paths stored in the JSON records of both
// roots. A dry run reports the roots it would walk and touches nothing.
func (r *runner) rewriteJSON(d detection) error {
	var targets []string
	if base := r.stateBase(d); base != "" {
		targets = append(targets, base)
	}
	if base := r.configBase(d); base != "" {
		targets = append(targets, base)
	}
	if len(targets) == 0 {
		r.addStep("rewrite-json", "no records to rewrite", true, false)
		return nil
	}

	if r.opts.DryRun {
		r.addStep("rewrite-json",
			fmt.Sprintf("rewrite JSON records under %s", strings.Join(targets, ", ")), false, false)
		return nil
	}

	total := 0
	for _, root := range targets {
		n, err := RewriteJSON(root, r.pairs())
		if err != nil {
			return fmt.Errorf("rewrite-json: %w", err)
		}
		total += n
	}
	r.addStep("rewrite-json", fmt.Sprintf("rewrote %d file(s)", total), false, false)
	return nil
}

// rewriteDB rewrites the old-root paths stored in the database. A newer schema
// is a warning, not an error: the data moved and is correct, and the warning
// says the paths inside it were not touched.
func (r *runner) rewriteDB(ctx context.Context, d detection) error {
	base := r.stateBase(d)
	if base == "" {
		r.addStep("rewrite-db", "no database", true, false)
		return nil
	}
	dbPath := store.New(base).DBPath()
	if !r.dbExists(base) {
		r.addStep("rewrite-db", "no database", true, false)
		return nil
	}

	if r.opts.DryRun {
		r.addStep("rewrite-db", fmt.Sprintf("rewrite paths in %s", dbPath), false, false)
		return nil
	}

	rows, newer, err := r.opts.RewriteDB(ctx, dbPath, r.pairs())
	if err != nil {
		return fmt.Errorf("rewrite-db: %w", err)
	}
	if newer {
		r.addStep("rewrite-db",
			"database schema is newer than this relevo; paths inside it were not rewritten", false, true)
		return nil
	}
	r.addStep("rewrite-db", fmt.Sprintf("rewrote %d row(s)", rows), false, false)
	return nil
}

// dbExists reports whether the database is there under either name: in a dry
// run the rename has not happened yet, so the old name counts.
func (r *runner) dbExists(base string) bool {
	if _, err := os.Stat(store.New(base).DBPath()); err == nil {
		return true
	}
	if r.opts.DryRun {
		if _, err := os.Stat(filepath.Join(base, legacy.DBFile)); err == nil {
			return true
		}
	}
	return false
}

// repairWorktrees asks git to repair every worktree the move relocated. A
// binding with no recorded repo or a failed repair is a warning, not an error:
// the data is moved and a re-run can repair the tree once the cause is fixed.
func (r *runner) repairWorktrees(ctx context.Context, d detection) {
	base := r.stateBase(d)
	if base == "" {
		r.addStep("repair-worktrees", "no worktrees to repair", true, false)
		return
	}

	repaired := 0
	// repairedPaths holds every worktree the binding pass repaired, or counted
	// in a dry run, so the orphan pass never repairs one of them twice.
	repairedPaths := map[string]bool{}
	var warnings []string
	for _, root := range storeRoots(base) {
		bindings, err := store.ListFiles(root)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("cannot read bindings in %s: %v", root, err))
			continue
		}
		for _, b := range bindings {
			wt := b.Worktree
			if wt == "" {
				wt = b.CWD
			}
			if !pathUnder(base, wt) {
				continue
			}
			if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
				continue
			}

			repo := b.Repo
			if b.Serve != nil && b.Serve.BareRepo != "" {
				repo = b.Serve.BareRepo
			}
			if repo == "" {
				warnings = append(warnings,
					fmt.Sprintf("binding %s: worktree %s has no recorded repo; run git worktree repair yourself", b.Name, wt))
				continue
			}
			if r.opts.DryRun {
				repaired++
				repairedPaths[filepath.Clean(wt)] = true
				continue
			}
			if err := r.opts.Repair(ctx, repo, wt); err != nil {
				warnings = append(warnings,
					fmt.Sprintf("binding %s: git -C %s worktree repair %s failed: %v", b.Name, repo, wt, err))
				continue
			}
			repaired++
			repairedPaths[filepath.Clean(wt)] = true
		}
	}

	// Second pass: worktree directories no binding records, which the binding
	// pass above never sees. A dry run has not moved anything, so the admin
	// path maps from the old root onto itself.
	from, to := r.opts.StateFrom, r.opts.StateTo
	if r.opts.DryRun {
		from = to
	}
	for _, wt := range orphanWorktrees(base) {
		if repairedPaths[filepath.Clean(wt)] {
			continue
		}
		repo, _, err := adminRepo(wt, from, to)
		if err != nil {
			warnings = append(warnings,
				fmt.Sprintf("worktree %s: %v; run git worktree repair yourself", wt, err))
			continue
		}
		if r.opts.DryRun {
			repaired++
			continue
		}
		if err := r.opts.Repair(ctx, repo, wt); err != nil {
			warnings = append(warnings,
				fmt.Sprintf("worktree %s: git -C %s worktree repair %s failed: %v", wt, repo, wt, err))
			continue
		}
		repaired++
	}

	switch {
	case len(warnings) > 0:
		detail := fmt.Sprintf("repaired %d worktree(s); %s", repaired, strings.Join(warnings, "; "))
		r.addStep("repair-worktrees", detail, false, true)
	case repaired == 0:
		r.addStep("repair-worktrees", "no worktrees needed repair", true, false)
	default:
		r.addStep("repair-worktrees", fmt.Sprintf("repaired %d worktree(s)", repaired), false, false)
	}
}
