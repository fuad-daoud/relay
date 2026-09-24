package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
)

type repairCall struct{ repo, worktree string }

// fakes records every call Run made, so a test can assert none, one or many.
type fakes struct {
	repairs   []repairCall
	repairErr error
	dbPath    string
	dbPairs   []Prefix
	dbRows    int64
	dbNewer   bool
	dbErr     error
	alive     map[int]bool
}

func newFakes() *fakes { return &fakes{} }

func (f *fakes) options(stateFrom, stateTo, configFrom, configTo string, out *bytes.Buffer) Options {
	return Options{
		StateFrom:  stateFrom,
		StateTo:    stateTo,
		ConfigFrom: configFrom,
		ConfigTo:   configTo,
		Alive:      func(pid int) bool { return f.alive[pid] },
		Repair: func(_ context.Context, repo, wt string) error {
			f.repairs = append(f.repairs, repairCall{repo: repo, worktree: wt})
			return f.repairErr
		},
		RewriteDB: func(_ context.Context, path string, pairs []Prefix) (int64, bool, error) {
			f.dbPath = path
			f.dbPairs = pairs
			return f.dbRows, f.dbNewer, f.dbErr
		},
		Out: out,
	}
}

func TestRunHappyPath(t *testing.T) {
	base := t.TempDir()
	stateFrom := filepath.Join(base, "oldstate")
	stateTo := filepath.Join(base, "newstate")
	configFrom := filepath.Join(base, "oldconfig")
	configTo := filepath.Join(base, "newconfig")
	repo := filepath.Join(base, "repo")

	wt := filepath.Join(stateFrom, ".worktrees", "one")
	mustMkdir(t, wt)
	saveBinding(t, stateFrom, "one", wt, repo, store.StateDone)

	mustWrite(t, filepath.Join(stateFrom, legacy.DBFile), "")
	mustWrite(t, filepath.Join(stateFrom, legacy.DBFile+"-wal"), "")
	mustWrite(t, filepath.Join(configFrom, "candidates.json"), "{}")

	f := newFakes()
	var out bytes.Buffer
	res, err := Run(context.Background(), f.options(stateFrom, stateTo, configFrom, configTo, &out))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Nothing {
		t.Fatal("Result.Nothing = true, want false")
	}

	if _, err := os.Stat(stateFrom); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old state root still exists: %v", err)
	}
	if _, err := os.Stat(configFrom); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old config root still exists: %v", err)
	}
	for _, rel := range []string{"relevo.db", "relevo.db-wal"} {
		if _, err := os.Stat(filepath.Join(stateTo, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}

	moved, err := store.New(stateTo).Load("one")
	if err != nil {
		t.Fatalf("load moved binding: %v", err)
	}
	wantCWD := filepath.Join(stateTo, ".worktrees", "one")
	if moved.CWD != wantCWD {
		t.Errorf("moved CWD = %q, want %q", moved.CWD, wantCWD)
	}

	if len(f.repairs) != 1 {
		t.Fatalf("Repair calls = %d, want 1: %+v", len(f.repairs), f.repairs)
	}
	if f.repairs[0] != (repairCall{repo: repo, worktree: wantCWD}) {
		t.Errorf("Repair(%q, %q), want (%q, %q)",
			f.repairs[0].repo, f.repairs[0].worktree, repo, wantCWD)
	}

	if want := filepath.Join(stateTo, "relevo.db"); f.dbPath != want {
		t.Errorf("RewriteDB path = %q, want %q", f.dbPath, want)
	}
	wantPairs := []Prefix{{Old: stateFrom, New: stateTo}, {Old: configFrom, New: configTo}}
	if !reflect.DeepEqual(f.dbPairs, wantPairs) {
		t.Errorf("RewriteDB pairs = %v, want %v", f.dbPairs, wantPairs)
	}
}

func TestRunDryRun(t *testing.T) {
	base := t.TempDir()
	stateFrom := filepath.Join(base, "oldstate")
	stateTo := filepath.Join(base, "newstate")
	configFrom := filepath.Join(base, "oldconfig")
	configTo := filepath.Join(base, "newconfig")

	wt := filepath.Join(stateFrom, ".worktrees", "one")
	mustMkdir(t, wt)
	saveBinding(t, stateFrom, "one", wt, filepath.Join(base, "repo"), store.StateDone)
	mustWrite(t, filepath.Join(stateFrom, legacy.DBFile), "db")
	mustWrite(t, filepath.Join(configFrom, "candidates.json"), "{}")

	before := treeSnapshot(t, base)

	f := newFakes()
	var out bytes.Buffer
	o := f.options(stateFrom, stateTo, configFrom, configTo, &out)
	o.DryRun = true
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Nothing {
		t.Fatal("Result.Nothing = true, want false")
	}

	after := treeSnapshot(t, base)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("dry run changed the tree:\nbefore: %v\nafter:  %v", before, after)
	}
	if len(f.repairs) != 0 || f.dbPath != "" {
		t.Errorf("dry run called a fake: repairs=%v dbPath=%q", f.repairs, f.dbPath)
	}

	wantNames := []string{"move-config", "move-state", "rename-db", "rewrite-json", "rewrite-db", "repair-worktrees"}
	var gotNames []string
	for _, s := range res.Steps {
		gotNames = append(gotNames, s.Name)
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Errorf("steps = %v, want %v", gotNames, wantNames)
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.HasPrefix(line, "(dry run) ") {
			t.Errorf("line %q lacks the dry-run prefix", line)
		}
	}
}

func TestRunRefusalsAllAtOnce(t *testing.T) {
	base := t.TempDir()
	stateFrom := filepath.Join(base, "oldstate")
	stateTo := filepath.Join(base, "newstate")

	saveBinding(t, stateFrom, "busy", filepath.Join(stateFrom, ".worktrees", "busy"), "", store.StateActive)
	mustWrite(t, filepath.Join(stateTo, "keep.txt"), "x")

	pid := 4242
	// The daemon pointer is a kv row now (P5 §4.6); this writes the legacy
	// file a pre-P5 daemon left, which migrate's detect step still reads (D1).
	ptrDir := filepath.Join(stateFrom, "serve")
	if err := os.MkdirAll(ptrDir, 0o755); err != nil {
		t.Fatalf("mkdir pointer dir: %v", err)
	}
	ptr, err := json.Marshal(serve.DaemonPointer{Root: stateFrom, PID: pid})
	if err != nil {
		t.Fatalf("marshal pointer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ptrDir, serve.PointerFileName), ptr, 0o644); err != nil {
		t.Fatalf("write pointer: %v", err)
	}

	f := newFakes()
	f.alive = map[int]bool{pid: true}
	var out bytes.Buffer
	_, err = Run(context.Background(), f.options(stateFrom, stateTo, "", "", &out))

	var ref *Refusal
	if !errors.As(err, &ref) {
		t.Fatalf("err = %v, want *Refusal", err)
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("errors.Is(err, ErrRefused) = false")
	}
	if len(ref.Reasons) != 3 {
		t.Fatalf("reasons = %d, want 3: %v", len(ref.Reasons), ref.Reasons)
	}

	if _, err := os.Stat(stateFrom); err != nil {
		t.Errorf("old state root moved: %v", err)
	}
	if got := readFixture(t, filepath.Join(stateTo, "keep.txt")); got != "x" {
		t.Errorf("new state root changed: %q", got)
	}
}

func TestRunResume(t *testing.T) {
	base := t.TempDir()
	stateFrom := filepath.Join(base, "oldstate")
	stateTo := filepath.Join(base, "newstate")
	configFrom := filepath.Join(base, "oldconfig")
	configTo := filepath.Join(base, "newconfig")

	saveBinding(t, stateFrom, "one", filepath.Join(stateFrom, ".worktrees", "one"), "", store.StateDone)
	mustWrite(t, filepath.Join(configTo, "candidates.json"), "{}")

	f := newFakes()
	var out bytes.Buffer
	res, err := Run(context.Background(), f.options(stateFrom, stateTo, configFrom, configTo, &out))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(stateTo); err != nil {
		t.Errorf("state not moved: %v", err)
	}
	mc := findStep(t, res, "move-config")
	if !mc.Skipped || mc.Detail != "already moved" {
		t.Errorf("move-config = %+v, want skipped already moved", mc)
	}
}

func TestRunNothing(t *testing.T) {
	base := t.TempDir()
	f := newFakes()
	var out bytes.Buffer
	res, err := Run(context.Background(), f.options(
		filepath.Join(base, "oldstate"), filepath.Join(base, "newstate"),
		filepath.Join(base, "oldconfig"), filepath.Join(base, "newconfig"), &out))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Nothing {
		t.Errorf("Result.Nothing = false, want true")
	}
	if len(res.Steps) != 0 {
		t.Errorf("steps = %v, want none", res.Steps)
	}
}

func TestRunConfigMerge(t *testing.T) {
	t.Run("identical file merges", func(t *testing.T) {
		base := t.TempDir()
		stateFrom := filepath.Join(base, "oldstate")
		stateTo := filepath.Join(base, "newstate")
		configFrom := filepath.Join(base, "oldconfig")
		configTo := filepath.Join(base, "newconfig")

		saveBinding(t, stateFrom, "one", filepath.Join(stateFrom, ".worktrees", "one"), "", store.StateDone)
		mustWrite(t, filepath.Join(configFrom, "candidates.json"), "same")
		mustWrite(t, filepath.Join(configFrom, "policy.json"), "policy")
		mustWrite(t, filepath.Join(configTo, "candidates.json"), "same")

		f := newFakes()
		var out bytes.Buffer
		if _, err := Run(context.Background(), f.options(stateFrom, stateTo, configFrom, configTo, &out)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := readFixture(t, filepath.Join(configTo, "candidates.json")); got != "same" {
			t.Errorf("candidates.json = %q, want %q", got, "same")
		}
		if got := readFixture(t, filepath.Join(configTo, "policy.json")); got != "policy" {
			t.Errorf("policy.json = %q, want %q", got, "policy")
		}
		if _, err := os.Stat(configFrom); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("old config root still exists: %v", err)
		}
	})

	t.Run("slice rename only merges", func(t *testing.T) {
		base := t.TempDir()
		stateFrom := filepath.Join(base, "oldstate")
		stateTo := filepath.Join(base, "newstate")
		configFrom := filepath.Join(base, "oldconfig")
		configTo := filepath.Join(base, "newconfig")

		saveBinding(t, stateFrom, "one", filepath.Join(stateFrom, ".worktrees", "one"), "", store.StateDone)
		newPolicy := `{"slice": "relevo.slice"}`
		oldPolicy := strings.Replace(newPolicy, `"relevo.slice"`, `"`+legacy.Slice+`"`, 1)
		mustWrite(t, filepath.Join(configFrom, "policy.json"), oldPolicy)
		mustWrite(t, filepath.Join(configTo, "policy.json"), newPolicy)

		f := newFakes()
		var out bytes.Buffer
		if _, err := Run(context.Background(), f.options(stateFrom, stateTo, configFrom, configTo, &out)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := readFixture(t, filepath.Join(configTo, "policy.json")); got != newPolicy {
			t.Errorf("policy.json = %q, want %q", got, newPolicy)
		}
		if _, err := os.Stat(configFrom); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("old config root still exists: %v", err)
		}
	})

	t.Run("differing file refuses", func(t *testing.T) {
		base := t.TempDir()
		stateFrom := filepath.Join(base, "oldstate")
		stateTo := filepath.Join(base, "newstate")
		configFrom := filepath.Join(base, "oldconfig")
		configTo := filepath.Join(base, "newconfig")

		saveBinding(t, stateFrom, "one", filepath.Join(stateFrom, ".worktrees", "one"), "", store.StateDone)
		mustWrite(t, filepath.Join(configFrom, "candidates.json"), "left")
		mustWrite(t, filepath.Join(configTo, "candidates.json"), "right")

		f := newFakes()
		var out bytes.Buffer
		_, err := Run(context.Background(), f.options(stateFrom, stateTo, configFrom, configTo, &out))

		var ref *Refusal
		if !errors.As(err, &ref) {
			t.Fatalf("err = %v, want *Refusal", err)
		}
		joined := strings.Join(ref.Reasons, "; ")
		wantName := filepath.Join(configFrom, "candidates.json")
		if !strings.Contains(joined, wantName) || !strings.Contains(joined, "differ") {
			t.Errorf("reasons = %v, want one naming %s", ref.Reasons, wantName)
		}
		if _, err := os.Stat(configFrom); err != nil {
			t.Errorf("old config root moved: %v", err)
		}
	})
}

func TestRunExplicitPair(t *testing.T) {
	base := t.TempDir()
	stateFrom := filepath.Join(base, "oldstate")
	stateTo := filepath.Join(base, "newstate")

	saveBinding(t, stateFrom, "one", filepath.Join(stateFrom, ".worktrees", "one"), "", store.StateDone)

	f := newFakes()
	var out bytes.Buffer
	res, err := Run(context.Background(), f.options(stateFrom, stateTo, "", "", &out))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(stateTo); err != nil {
		t.Errorf("state not moved: %v", err)
	}
	if _, err := os.Stat(stateFrom); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old state root still exists: %v", err)
	}
	mc := findStep(t, res, "move-config")
	if !mc.Skipped {
		t.Errorf("move-config = %+v, want skipped", mc)
	}
}

func TestRunSkipDaemonCheck(t *testing.T) {
	base := t.TempDir()
	stateFrom := filepath.Join(base, "oldstate")
	stateTo := filepath.Join(base, "newstate")

	saveBinding(t, stateFrom, "one", filepath.Join(stateFrom, ".worktrees", "one"), "", store.StateDone)

	lock, err := store.New(stateFrom).AcquireDaemonLock()
	if err != nil {
		t.Fatalf("AcquireDaemonLock: %v", err)
	}
	defer lock.Close()

	f := newFakes()
	var out bytes.Buffer
	_, err = Run(context.Background(), f.options(stateFrom, stateTo, "", "", &out))

	var ref *Refusal
	if !errors.As(err, &ref) {
		t.Fatalf("err = %v, want *Refusal", err)
	}
	if !strings.Contains(strings.Join(ref.Reasons, "; "), "daemon") {
		t.Errorf("reasons = %v, want a daemon reason", ref.Reasons)
	}

	f2 := newFakes()
	var out2 bytes.Buffer
	o := f2.options(stateFrom, stateTo, "", "", &out2)
	o.SkipDaemonCheck = true
	o.DryRun = true
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run with SkipDaemonCheck: %v", err)
	}
	if res.Nothing {
		t.Errorf("Result.Nothing = true, want false")
	}
}

func TestRunWarnings(t *testing.T) {
	t.Run("newer database", func(t *testing.T) {
		base := t.TempDir()
		stateFrom := filepath.Join(base, "oldstate")
		stateTo := filepath.Join(base, "newstate")

		saveBinding(t, stateFrom, "one", filepath.Join(stateFrom, ".worktrees", "one"), "", store.StateDone)
		mustWrite(t, filepath.Join(stateFrom, legacy.DBFile), "db")

		f := newFakes()
		f.dbNewer = true
		var out bytes.Buffer
		res, err := Run(context.Background(), f.options(stateFrom, stateTo, "", "", &out))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		s := findStep(t, res, "rewrite-db")
		if !s.Warn {
			t.Errorf("rewrite-db = %+v, want Warn", s)
		}
	})

	t.Run("failed repair", func(t *testing.T) {
		base := t.TempDir()
		stateFrom := filepath.Join(base, "oldstate")
		stateTo := filepath.Join(base, "newstate")
		repo := filepath.Join(base, "repo")

		wt := filepath.Join(stateFrom, ".worktrees", "one")
		mustMkdir(t, wt)
		saveBinding(t, stateFrom, "one", wt, repo, store.StateDone)

		f := newFakes()
		f.repairErr = errors.New("boom")
		var out bytes.Buffer
		res, err := Run(context.Background(), f.options(stateFrom, stateTo, "", "", &out))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		s := findStep(t, res, "repair-worktrees")
		if !s.Warn {
			t.Errorf("repair-worktrees = %+v, want Warn", s)
		}
		if !strings.Contains(s.Detail, "git -C") || !strings.Contains(s.Detail, "worktree repair") {
			t.Errorf("detail = %q, want the manual command", s.Detail)
		}
	})
}

// TestRunRepairsOrphanWorktree covers a worktree directory no binding records:
// the binding pass never sees it, so the orphan pass must repair it from the
// gitdir line of its .git file, mapped onto the new state root (#385).
func TestRunRepairsOrphanWorktree(t *testing.T) {
	base := t.TempDir()
	stateFrom := filepath.Join(base, "oldstate")
	stateTo := filepath.Join(base, "newstate")

	wt := filepath.Join(stateFrom, ".worktrees", "x")
	mustMkdir(t, wt)
	admin := filepath.Join(stateFrom, "serve", "repos", "o", "r.git", "worktrees", "x")
	mustMkdir(t, admin)
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")

	f := newFakes()
	var out bytes.Buffer
	res, err := Run(context.Background(), f.options(stateFrom, stateTo, "", "", &out))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantRepo := filepath.Join(stateTo, "serve", "repos", "o", "r.git")
	wantWT := filepath.Join(stateTo, ".worktrees", "x")
	if len(f.repairs) != 1 {
		t.Fatalf("Repair calls = %d, want 1: %+v", len(f.repairs), f.repairs)
	}
	if f.repairs[0] != (repairCall{repo: wantRepo, worktree: wantWT}) {
		t.Errorf("Repair(%q, %q), want (%q, %q)",
			f.repairs[0].repo, f.repairs[0].worktree, wantRepo, wantWT)
	}
	s := findStep(t, res, "repair-worktrees")
	if s.Detail != "repaired 1 worktree(s)" {
		t.Errorf("repair-worktrees detail = %q, want %q", s.Detail, "repaired 1 worktree(s)")
	}
	if s.Warn || s.Skipped {
		t.Errorf("repair-worktrees = %+v, want neither warned nor skipped", s)
	}
}

// TestRunOrphanNotRepairedTwice checks the binding pass records what it
// repaired, so the orphan pass leaves that same worktree alone (#385).
func TestRunOrphanNotRepairedTwice(t *testing.T) {
	base := t.TempDir()
	stateFrom := filepath.Join(base, "oldstate")
	stateTo := filepath.Join(base, "newstate")

	wt := filepath.Join(stateFrom, ".worktrees", "y")
	mustMkdir(t, wt)
	admin := filepath.Join(stateFrom, "serve", "repos", "o", "r.git", "worktrees", "y")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")
	saveBinding(t, stateFrom, "y", wt, filepath.Join(base, "repo"), store.StateDone)

	f := newFakes()
	var out bytes.Buffer
	if _, err := Run(context.Background(), f.options(stateFrom, stateTo, "", "", &out)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantWT := filepath.Join(stateTo, ".worktrees", "y")
	if len(f.repairs) != 1 {
		t.Fatalf("Repair calls = %d, want 1: %+v", len(f.repairs), f.repairs)
	}
	if f.repairs[0].worktree != wantWT {
		t.Errorf("Repair worktree = %q, want %q", f.repairs[0].worktree, wantWT)
	}
}

// TestRunOrphanDryRun checks the orphan pass counts a would-be repair without
// calling Repair (#385).
func TestRunOrphanDryRun(t *testing.T) {
	base := t.TempDir()
	stateFrom := filepath.Join(base, "oldstate")
	stateTo := filepath.Join(base, "newstate")

	wt := filepath.Join(stateFrom, ".worktrees", "x")
	mustMkdir(t, wt)
	admin := filepath.Join(stateFrom, "serve", "repos", "o", "r.git", "worktrees", "x")
	mustMkdir(t, admin)
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")

	f := newFakes()
	var out bytes.Buffer
	o := f.options(stateFrom, stateTo, "", "", &out)
	o.DryRun = true
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.repairs) != 0 {
		t.Errorf("dry run called Repair: %+v", f.repairs)
	}
	s := findStep(t, res, "repair-worktrees")
	if s.Detail != "repaired 1 worktree(s)" {
		t.Errorf("repair-worktrees detail = %q, want %q", s.Detail, "repaired 1 worktree(s)")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// saveBinding seeds what stands for an old, pre-DB legacy root: it writes the
// pre-P3a layout directly, <root>/<name>/bind.json, rather than going through
// the now DB-backed store (P3a round 4, C1).
func saveBinding(t *testing.T, root, name, cwd, repo string, state store.State) {
	t.Helper()
	b := store.Binding{Name: name, CWD: cwd, Worktree: cwd, Repo: repo, State: state}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create binding dir %s: %v", name, err)
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatalf("marshal binding %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), raw, 0o644); err != nil {
		t.Fatalf("write binding %s: %v", name, err)
	}
}

// treeSnapshot maps every regular file under root to its mode and bytes, which
// is what "the dry run wrote nothing" has to mean.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snap[path] = fmt.Sprintf("%v|%s", info.Mode(), data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snap
}

func findStep(t *testing.T, res Result, name string) Step {
	t.Helper()
	for _, s := range res.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no step named %q in %v", name, res.Steps)
	return Step{}
}
