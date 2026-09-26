// Package migrate is the core of `relevo migrate`: detection and refusals,
// directory moves, database file renames, rewriting old-root paths in
// relevo's own JSON records and database, and `git worktree repair`. The two
// calls that would drag internal/git and internal/db in are function values
// on Options, so tests use fakes. Every step decides "already done" from the
// filesystem alone, so a re-run after a crash resumes and the rewrites stay
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

// Options configures one Run. StateFrom/StateTo are required, absolute and
// distinct; ConfigFrom/ConfigTo are both "" or both set.
type Options struct {
	StateFrom, StateTo   string
	ConfigFrom, ConfigTo string
	DryRun               bool
	// DefaultServeRoot is the old default serve root, where `relevo serve`
	// writes daemon.json even under an explicit --state. "" means StateFrom/serve only.
	DefaultServeRoot string
	// SkipDaemonCheck drops the daemon-lock refusal, for the CLI's dry run before it stops the service.
	SkipDaemonCheck bool

	// Alive, Repair, RewriteDB and Out are all required.
	Alive     func(pid int) bool
	Repair    func(ctx context.Context, repo, worktree string) error
	RewriteDB func(ctx context.Context, dbPath string, pairs []Prefix) (rows int64, newer bool, err error)
	Out       io.Writer
}

// Prefix is legacy.Prefix, kept under this name for migrate's callers.
type Prefix = legacy.Prefix

// Step is one line of the migration report: Name is the step, Skipped means
// already done or not applicable, Warn means done with a caveat to show.
type Step struct {
	Name    string
	Detail  string
	Skipped bool
	Warn    bool
}

// Result is what Run found and did.
type Result struct {
	Steps []Step
	// Nothing is true when nothing old was found: no step ran.
	Nothing bool
}

// ErrRefused is what every Refusal unwraps to.
var ErrRefused = errors.New("migrate: refused")

// Refusal reports every reason the migration was refused, so the user can fix
// them all in one go.
type Refusal struct{ Reasons []string }

func (r *Refusal) Error() string {
	var b strings.Builder
	b.WriteString("migrate: refused:")
	for _, reason := range r.Reasons {
		b.WriteString("\n  - ")
		b.WriteString(reason)
	}
	return b.String()
}

func (r *Refusal) Unwrap() error { return ErrRefused }

// Run performs the migration in the fixed step order, writing one line per
// step to o.Out. A refused install returns a *Refusal; any other failure
// names the step it happened in and leaves the state for a re-run to resume.
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

// validateOptions rejects a bad option as a plain error before anything is touched.
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

type runner struct {
	opts   Options
	result Result
}

// addStep records one step and writes its line, prefixed for a dry run.
func (r *runner) addStep(name, detail string, skipped, warn bool) {
	r.result.Steps = append(r.result.Steps, Step{Name: name, Detail: detail, Skipped: skipped, Warn: warn})
	prefix := ""
	if r.opts.DryRun {
		prefix = "(dry run) "
	}
	_, _ = fmt.Fprintf(r.opts.Out, "%s%s: %s\n", prefix, name, detail)
}

// pairs is the substitution list every rewrite gets.
func (r *runner) pairs() []Prefix {
	pairs := []Prefix{{Old: r.opts.StateFrom, New: r.opts.StateTo}}
	if r.opts.ConfigFrom != "" {
		pairs = append(pairs, Prefix{Old: r.opts.ConfigFrom, New: r.opts.ConfigTo})
	}
	return pairs
}

// stateBase is the directory the later steps act on: StateTo once it exists,
// else the old root a dry run would move there.
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

func (r *runner) moveConfig(d detection) error {
	if !d.configPair {
		r.addStep("move-config", "no config root", true, false)
		return nil
	}
	return r.moveStep("move-config", d.confOld, d.confNew, r.opts.ConfigFrom, r.opts.ConfigTo, "no config root")
}

func (r *runner) moveState(d detection) error {
	return r.moveStep("move-state", d.stateOld, d.stateNew, r.opts.StateFrom, r.opts.StateTo, "no state root")
}

// moveStep is the shape moveConfig and moveState share: rename when only the
// old root exists, merge when both do, skip when already moved.
func (r *runner) moveStep(name string, oldExists, newExists bool, from, to, nothing string) error {
	switch {
	case oldExists && !newExists:
		if r.opts.DryRun {
			r.addStep(name, fmt.Sprintf("rename %s to %s", from, to), false, false)
			return nil
		}
		if err := moveRoot(from, to); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		r.addStep(name, fmt.Sprintf("renamed %s to %s", from, to), false, false)
	case oldExists && newExists:
		if r.opts.DryRun {
			r.addStep(name, fmt.Sprintf("merge %s into %s", from, to), false, false)
			return nil
		}
		if err := moveRoot(from, to); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		r.addStep(name, fmt.Sprintf("merged %s into %s", from, to), false, false)
	case !oldExists && newExists:
		r.addStep(name, "already moved", true, false)
	default:
		r.addStep(name, nothing, true, false)
	}
	return nil
}

// moveRoot renames from to to when to does not exist, else merges the two: an
// entry absent from to is renamed in, an identical file in both is dropped
// from from, and anything else is an error.
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

// renameDB renames the database file and its -wal/-shm siblings.
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
			continue // both names exist already: leave them, the new one wins
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

// rewriteJSON rewrites the old-root paths in the JSON records of both roots.
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

// rewriteDB rewrites the old-root paths in the database; a newer schema is a
// warning, since the data moved but the paths inside it did not.
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

// dbExists checks both names: in a dry run the rename has not happened yet.
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
// missing repo or a failed repair is a warning: a re-run can fix it later.
func (r *runner) repairWorktrees(ctx context.Context, d detection) {
	base := r.stateBase(d)
	if base == "" {
		r.addStep("repair-worktrees", "no worktrees to repair", true, false)
		return
	}

	repaired, repairedPaths, warnings := r.repairBoundWorktrees(ctx, base)
	n, w := r.repairOrphans(ctx, base, repairedPaths)
	repaired += n
	warnings = append(warnings, w...)

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

// repairBoundWorktrees repairs every worktree a binding records and returns
// the paths touched, so the orphan pass never repairs one twice.
func (r *runner) repairBoundWorktrees(ctx context.Context, base string) (repaired int, repairedPaths map[string]bool, warnings []string) {
	repairedPaths = map[string]bool{}
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
	return repaired, repairedPaths, warnings
}

// repairOrphans repairs worktree directories no binding records, deriving
// each one's repo from its .git file.
func (r *runner) repairOrphans(ctx context.Context, base string, repairedPaths map[string]bool) (repaired int, warnings []string) {
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
	return repaired, warnings
}
