# Plan: #292 round 3a: `internal/migrate`, the core that moves relay-era state

Spec: `docs/specs/2026-09-23-rename-relevo-design.md` §3 (read the "Revised
2026-09-24" block first). Rounds 1-2 are on this branch: the rename, and
`internal/legacy` with `Roots`/`Status`/`Probe`.

This round builds the **core** of `relevo migrate` as a library:

- detection and refusals;
- the directory moves;
- the DB file renames;
- rewriting old-root paths inside relevo's own JSON records and the database;
- `git worktree repair`.

Units, the old binary and the CLI verb come in round 3b. Do not add them here.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 1. System overview

Every local worktree lives inside the state root (`<state>/.worktrees/<name>`).
So do a server's worktrees (`<state>/serve/bindings/<owner>/.worktrees/<name>`)
and bare repos (`<state>/serve/repos/<owner>/...`). Records hold absolute paths
into the root: `bind.json`'s `cwd`, `worktree`, `serve.bare_repo`,
`endpoint.log_path` and consult paths, and the DB's `binding.cwd`, `worktree` and
`archive_path`, among others.

A plain `mv` of the root therefore leaves dangling paths and broken git worktree
links. The core moves the roots, then rewrites the old prefix to the new one, then
asks git to repair each worktree.

## 2. File structure

```
internal/migrate/migrate.go        NEW  Options, Result, Step, Run (the ordered procedure)
internal/migrate/detect.go         NEW  detect + refusals (read-only)
internal/migrate/rewrite.go        NEW  byte-level JSON prefix rewrite over a root
internal/migrate/samefs_unix.go    NEW  //go:build unix -- same filesystem check (st_dev)
internal/migrate/samefs_other.go   NEW  //go:build !unix -- always true
internal/migrate/migrate_test.go   NEW
internal/migrate/rewrite_test.go   NEW
internal/git/client.go             EDIT add (*Client).WorktreeRepair
internal/git/client_test.go        EDIT (or a new worktree_repair_test.go) -- real git
internal/db/rewrite.go             NEW  RewritePathPrefix
internal/db/rewrite_test.go        NEW
```

`internal/migrate` may import `internal/legacy`, `internal/store`, `internal/serve`
(only for `ReadPointer`/`PointerFileName`) and stdlib. It must **not** import
`internal/git` or `internal/db`. Those arrive as the function values in `Options`,
so tests use fakes.

## 3. Data structures

```
type Options struct {
    StateFrom, StateTo   string // required, absolute, distinct
    ConfigFrom, ConfigTo string // both "" (explicit-pair mode) or both set
    DryRun               bool
    // DefaultServeRoot is the old default serve root (<old default state>/serve),
    // where a running `relay serve` writes daemon.json even when it serves an
    // explicit --state. "" = StateFrom/serve only.
    DefaultServeRoot string
    // SkipDaemonCheck drops the daemon-lock refusal only. Round 3b's CLI runs a
    // dry run with it set BEFORE stopping relay.service (the daemon is still
    // up then), and the real run without it after the stop.
    SkipDaemonCheck bool
    Alive     func(pid int) bool                                   // pid liveness; required
    Repair    func(ctx context.Context, repo, worktree string) error // git worktree repair; required
    RewriteDB func(ctx context.Context, dbPath string, pairs []Prefix) (rows int64, newer bool, err error) // required
    Out       io.Writer                                            // one line per step; required
}

type Prefix struct{ Old, New string } // absolute dirs, no trailing slash

type Step struct {
    Name   string // "refuse" | "move-config" | "move-state" | "rename-db" | "rewrite-json" | "rewrite-db" | "repair-worktrees"
    Detail string // human line: what was (or would be) done, or why it was skipped
    Skipped bool  // already done (resume) or not applicable
    Warn   bool   // done with a caveat the user must see (newer DB, a failed repair)
}

type Result struct {
    Steps   []Step
    Nothing bool // nothing relay-era was found; no step ran
}

var ErrRefused = errors.New("migrate: refused")
// Refusal wraps ErrRefused with every reason (not just the first).
type Refusal struct{ Reasons []string }
func (r *Refusal) Error() string   // "migrate: refused:\n  - <reason>\n  - ..."
func (r *Refusal) Unwrap() error    // ErrRefused
```

## 4. Interfaces

**`func Run(ctx context.Context, o Options) (Result, error)`**

- Validates `Options`. A missing required field is a programming error: return
  it plain, not as a `Refusal`.
- Runs §5 in order and writes each `Step` to `o.Out` as
  `<name>: <detail>`, prefixed with `(dry run) ` when `DryRun` is set.
- **Dry run:** runs detection and refusals, then reports every later step as it
  *would* run. It writes nothing, renames nothing, and calls none of `Repair` or
  `RewriteDB`.
- A refusal returns `(Result{Steps: [refuse step]}, *Refusal)`.
- Any other error names the step it happened in. The state is left for a re-run to
  resume.

**`func RewriteJSON(root string, pairs []Prefix) (files int, err error)`** (exported,
so round 3b and the tests can call it directly):

- Walk `root`. Rewrite only regular files named `*.json` or `*.jsonl`.
- **Skip these whole subtrees:**
  - any directory named `.worktrees`;
  - the `repos` and `worktrees` directories directly under a directory named
    `serve`;
  - any directory containing a `.git` entry.

  These hold user repositories, never relevo records.
- In each file, for each pair, replace the byte sequence `"` + Old + `/` with `"`
  + New + `/`, and `"` + Old + `"` with `"` + New + `"`. Only JSON string values
  that start with the path change, and nothing else moves.
- Write a changed file atomically: a temp file in the same directory, the
  original's mode, then rename. Leave unchanged files untouched (mtime included).
- Return how many files changed.

**`internal/git`:** `func (c *Client) WorktreeRepair(ctx context.Context, repo,
worktree string) error` runs `git -C <repo> worktree repair <worktree>` through
the existing `c.run`. Errors carry git's stderr.

**`internal/db`:** `func RewritePathPrefix(ctx context.Context, path string, pairs
[]Prefix) (rows int64, newer bool, err error)`.

- Declare `type Prefix struct{ Old, New string }` in `internal/db` too.
  `internal/migrate` keeps its own; round 3b adapts one to the other.
- Opens with `Open(path)`. If `Newer()` is true, return `(0, true, nil)` with no
  write. Else, in one `Tx`:
  - enumerate the user tables (`sqlite_master` type `table`, excluding `sqlite_%`
    and `schema_version`);
  - for each column whose declared type is `TEXT` (case-insensitive), run
    `UPDATE <t> SET <c> = ? || substr(<c>, ?) WHERE <c> = ? OR substr(<c>, 1, ?) = ?`,
    with the new prefix, `len(Old)+1`, `Old` and `len(Old)+1`, then `Old + "/"`
    (use `substr`, not `LIKE`: no escaping, and it's case-exact);
  - quote identifiers with `"`.
- Returns the total rows changed. Close the DB before returning.

## 5. Pseudocode: `Run`

```
validate options
detect (read-only):
    stateOld := exists(StateFrom); stateNew := exists(StateTo)
    confOld/confNew likewise when config pair set
    nothing old at all -> Result{Nothing: true}, nil
refusals (collect ALL reasons, then refuse once if any):
    stateOld && stateNew && !emptyDir(StateTo)   -> "<StateTo> already exists and is not empty"
    confOld && confNew: config MERGES instead (below); refuse only on a conflict:
        for each top-level entry E of ConfigFrom that also exists in ConfigTo:
            both regular files with identical bytes -> fine (old copy is dropped at move-config)
            otherwise -> "<ConfigFrom>/<E> and <ConfigTo>/<E> both exist and differ; keep one and delete the other"
    stateOld && !sameFS(StateFrom, parent(StateTo)) -> "<StateFrom> and <StateTo> are on different filesystems"
    (config likewise)
    for each store root R in [StateFrom (if it holds bind files)] + dirs StateFrom/serve/bindings/*/:
        for b in store.New(R).List():
            b.State in {active, needs_you, broken}  -> "binding <R-relative name> is <state>; finish it (relay done) or pause it first"
            any c in b.Consults with State running  -> "binding <name> has a running consult"
    for each pointer dir D in [StateFrom/serve, DefaultServeRoot (if set)]:
        p, ok := serve.ReadPointer(D); ok && Alive(p.PID) -> "a relay serve daemon (pid <n>) is running on <p.Root>; stop it first"
    daemon lock (unless SkipDaemonCheck): store.New(StateFrom).DaemonRunning() true -> "a relay daemon holds <StateFrom>; stop it first (systemctl --user stop relay.service)"
    (round 3b stops the unit before calling Run; the core only checks)
    any reason -> return refuse step + *Refusal
move-config:  confOld && !confNew: os.Rename(ConfigFrom, ConfigTo)
              confOld && confNew (merge; why: contabo's `srv install` seeds ~/.config/relevo/*.json before migrate can run there):
                  for each top-level entry E of ConfigFrom: absent in ConfigTo -> os.Rename(ConfigFrom/E, ConfigTo/E);
                  identical regular file in both -> os.Remove(ConfigFrom/E)
                  then os.Remove(ConfigFrom) (now empty; if not empty, that is a bug -> error)
              !confOld && confNew -> Skipped "already moved"
move-state:   the same for StateFrom -> StateTo
rename-db:    in StateTo: for suffix in ["", "-wal", "-shm"]: relay.db<suffix> exists && relevo.db<suffix> absent -> rename
              (relevo.db already present and relay.db absent -> Skipped)
rewrite-json: pairs = [{StateFrom,StateTo}] (+ {ConfigFrom,ConfigTo} when set); RewriteJSON(StateTo, pairs); RewriteJSON(ConfigTo, pairs) when set
rewrite-db:   StateTo/relevo.db exists -> RewriteDB(ctx, it, pairs); newer -> Warn "database schema is newer than this relevo; paths inside it were not rewritten"
repair-worktrees:
    for each store root R' under StateTo (same enumeration as above):
        for b in store.New(R').List():
            wt := b.Worktree, else b.CWD
            wt not under StateTo, or not an existing dir -> skip silently
            repo := b.Serve.BareRepo if b.Serve != nil && != "" else b.Repo
            repo == "" -> Warn line "binding <name>: worktree <wt> has no recorded repo; run git worktree repair yourself"
            err := Repair(ctx, repo, wt); err -> Warn "binding <name>: git -C <repo> worktree repair <wt> failed: <err>" (continue)
return Result
```

Look up `b.Consults[i].State`'s running constant and the `Serve` field's type in
`internal/store/types.go` (~470 and ~531). Don't guess them.

**Old names in messages:** every old name in a message (`relay done`,
`relay.service`, `relay serve`, `relay daemon`) is built from `internal/legacy`'s
constants (`legacy.Binary`, `legacy.ClientUnit`), never written as a literal. Round
4 adds a guard that fails the build on a bare `relay` outside `internal/legacy`.

**Emptiness:** `emptyDir` is true for a directory with no entries.

**Resume:** a re-run after a crash between any two steps must finish the job.
Each step decides "already done" from the filesystem alone, as above, and the
rewrites are idempotent.

## 6. Error handling

- Refusals are complete, not first-hit: the user fixes everything in one go.
- A rename failure returns an error naming both paths and the step.
- Repair failures and a newer DB are **warnings**, not errors: the data is moved
  and correct, and the warning prints the exact manual command.
- `RewriteJSON` stops at the first I/O error and returns it. Files already
  rewritten stay rewritten; a re-run is idempotent.

## 8. Working efficiently

Read these once:

- `internal/store/types.go`: Binding ~190-470, Consult ~500-600;
- `internal/store/store.go`: `List` ~303, `DaemonRunning` in `daemonlock.go` ~48;
- `internal/serve/pointer.go`;
- `internal/git/client.go`: `run` ~67;
- `internal/db/db.go`: `Open` ~53, `Tx` ~159.

- **Focused loop:** `go test ./internal/migrate/ ./internal/db/ ./internal/git/ -run 'Rewrite|Repair|Migrate'`.
- **Full check, once at the end:** `make check`.
- Tests use `t.TempDir()` trees and fakes for `Alive`/`Repair`/`RewriteDB`. The
  git test uses real `git` (CI has it). Nothing spawns a harness or reaches the
  network.

## 9. Ordered implementation steps

**Step 1: `db.RewritePathPrefix`** plus a test.

- Build a temp DB with `Open`, and insert a `binding` row whose `cwd` is
  `/old/root/.worktrees/x`. Fill required columns from the schema in
  `internal/db/migrations/`.
- Also insert a value `/old/rootother` (a sibling that must **not** change) and an
  exact `/old/root`.
- Rewrite `/old/root` → `/new/root` and assert all three outcomes.
- Mutation: drop the `= ?` exact-match clause and the exact row must fail.

**Step 2: `(*Client).WorktreeRepair`** plus a real-git test:

1. `git init` a repo, commit once, `git worktree add <tmp>/a/wt`.
2. `mv <tmp>/a <tmp>/b`.
3. `WorktreeRepair(repo, <tmp>/b/wt)`.
4. `git -C repo worktree list --porcelain` names `<tmp>/b/wt`, and `git -C
   <tmp>/b/wt status` succeeds.

**Step 3: `RewriteJSON`** plus tests:

- A root with `bind.json` (`"cwd":"/o/r/.worktrees/x"`), `log.jsonl` (two lines,
  one with the path), `.worktrees/x/notes.json` holding the old path (**must not
  change**), `serve/repos/o/r.git/config.json` (**must not change**), and
  `other.txt` holding the old path (**must not change**: not JSON).
- Assert the changed-file count, the byte-exact results, and that the unchanged
  files keep their mtime.
- Mutation: remove the `.worktrees` skip and the test must fail.

**Step 4: `internal/migrate` Run,** detection and refusals, with the tests below.
Build trees with `store.New(root)` plus the store's own save API for bindings; find
it in `store.go`. Don't hand-write `bind.json`.

1. **Happy path.**
   - The old state root has one `done` binding whose worktree dir exists under
     `.worktrees/`, `relay.db` and `relay.db-wal` files (any bytes, with a fake
     `RewriteDB`), and a config root with `candidates.json`.
   - After `Run`:
     - both roots are moved and the old ones are gone;
     - `relevo.db` and `relevo.db-wal` exist;
     - `bind.json`'s cwd has the new prefix;
     - fake `Repair` was called once with (repo, new worktree);
     - fake `RewriteDB` was called with the new DB path and both pairs.
2. **Dry run.** The same tree is byte-identical afterwards, no fake was called, and
   `Out` holds `(dry run)` lines for every step.
3. **Refusals, all at once.** Set up an active binding, a non-empty new state root
   and a live serve pointer (`Alive` returns true for its pid). One `*Refusal`
   with 3 reasons, and nothing moved.
4. **Resume.** The config is already moved (old config absent, new present) and
   state not. `Run` skips `move-config` with `already moved` and completes the rest.
5. **Nothing.** No old roots → `Result.Nothing`.
6. **Config merge.** ConfigTo already holds a `candidates.json` identical to the
   old one, and the old root also has `policy.json`. After `Run`, ConfigTo has
   both files and ConfigFrom is gone. With a *different* `candidates.json` in
   ConfigTo, `Run` refuses, naming the file, and nothing moves.
7. **Explicit pair.** `ConfigFrom/To` empty, and only the state is moved.
8. **SkipDaemonCheck.** With the old root's daemon lock held (take it with
   `store.New(StateFrom).AcquireDaemonLock()` in the test), `Run` refuses with a
   daemon reason. The same run with `SkipDaemonCheck` and `DryRun` succeeds.
9. **Warnings.** Fake `RewriteDB` returns `newer=true` → a `Warn` step, and `Run`
   still returns nil. Fake `Repair` errors → a `Warn` naming the manual command.

Mutation-test at least:
- the active-binding refusal;
- the rename-db suffix loop (drop `-wal`);
- the resume skip;
- the identical-bytes check in the config merge;
- the dry-run guard on `move-state`.

List them in the report.

**Step 5: full check and commit.**
- `make check` must pass.
- One commit:
  `feat(migrate): the core that moves relay-era state, rewrites its paths and repairs worktrees (#292)`.
- Don't push.

**Declared scope:** exactly §2's files.

**Report:**
- per-step status;
- mutations;
- `git diff --stat HEAD~1`;
- the result of `make check`.
