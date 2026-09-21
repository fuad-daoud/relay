# Wave 2 chain I, step 1: relay add --branch <name> -- a builder on an existing local or remote branch; relay deletes a branch in zero places (#145)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. Never
run `make check` here (its drift guard and plugin-build case need the tag
set; the planner runs it); run the gate commands in §7 exactly as written.
Every command in the foreground; no sub-agents for edits. If the pre-flight
`git status` shows a dirty tree, follow your definition's rule for a tree
that already carries part of the plan.

## 1. System overview

`relay add` always cuts `relay/<name>` from HEAD (`internal/relay/add.go:158-179`)
and refuses if that branch exists. The only way to put a builder on an
existing branch -- yesterday's PR branch, a colleague's branch, a `relay/x`
left by an unbound binding -- is `--cwd` with a worktree you made yourself,
which relay then does not own. After this round `relay add --branch <name>`
checks the existing branch out into relay's own worktree
(`Git.CheckoutWorktree`, the no-`-b` form that `bind --resume` already uses,
`bind.go:218`), creating a local tracking branch first when only
`origin/<name>` exists; the binding records `ExistingBranch: true`; the
binding name defaults to the branch's last path segment; and README states
the invariant the code already has: relay deletes a branch in zero places
(`DeleteBranch`'s only caller is `addRemote`'s cleanup of a branch it just
created, `remote.go:196`, which this mode never triggers). `--server` works
with `--branch` client-side only: the wire carries `BaseCommit` (the branch
tip), the server names its own `relay/<name>` as today, and `send`/`pull`
key on the client's `b.Branch` (`remote.go:338-350`, `:922-926`).
`relay fork --branch` is **not** in this round (say so in the report).

## 2. File structure

```
internal/relay/herdr.go          Git + CreateTrackingBranch(ctx, dir, branch, upstream string) error
internal/git/client.go           + CreateTrackingBranch: git branch --track <branch> <upstream>
internal/git/client_test.go      + TestCreateTrackingBranch (real temp repo with a bare "origin"; signing off)
internal/relay/fake_test.go      fakeGit + createTrackingBranchCalls/Err; TestFakeSatisfiesGit still compiles
internal/store/types.go          Binding + ExistingBranch bool `json:"existing_branch,omitempty"`
internal/relay/add.go            AddOptions + Branch; Add: the existing-branch path; DefaultBindingName(branch) helper
internal/relay/remote.go         addRemote: Branch option -> no CreateBranch, BaseCommit = tip, ExistingBranch, no DeleteBranch in cleanup
internal/relay/add_test.go       + tests (§7)
internal/relay/remote_test.go    + TestAddRemoteExistingBranch
internal/relay/bind_test.go      + TestUnbindExistingBranchNeverDeletes (or add_test.go)
cmd/relay/main.go                cmdAdd: --branch flag; --name optional with --branch; output line; help text
cmd/relay/main_test.go           + TestAddBranchWithCwdIsRefusedBeforeRuntime (fails in validation, reaches no herdr)
README.md                        add section: --branch; "Cleaning up finished bindings": the zero-places sentence
docs/plans/2026-09-21-w2i1-add-branch.md   copy of this plan
```

`grep -rn "var _ Git" internal/` must still compile: the only fake is
`fakeGit` (`internal/relay/fake_test.go:79`); `internal/serve` uses the real
client. List the grep in the report.

## 3. Data structures

```
// internal/store/types.go  Binding
ExistingBranch bool `json:"existing_branch,omitempty"`
    // true when add --branch adopted a branch relay did not create. Informational: every teardown
    // path already leaves branches alone; this records that the branch predates the binding.

// internal/relay/add.go  AddOptions
Branch string   // existing branch to check out (local name, e.g. "feature/api-auth"); "" = cut relay/<name> as today.
                // Mutually exclusive with CWD. Name may be "" only when Branch is set (see DefaultBindingName).
```

## 4. Interfaces

```
// internal/git/client.go
func (c *Client) CreateTrackingBranch(ctx, dir, branch, upstream string) error
    // git branch --track <branch> <upstream>   (upstream e.g. "origin/feature/api-auth")
    // "already exists" in stderr -> ErrBranchExists (same mapping as CreateBranch)

// internal/relay/add.go
func DefaultBindingName(branch string) (string, error)
    // last path segment of branch ("feature/api-auth" -> "api-auth", "v2" -> "v2"), lowercased,
    // every run of characters store.ValidName does not accept replaced by one "-", trimmed of "-";
    // then store.ValidName(result) -- its error is returned unchanged so the CLI says "pass --name".
    // Read store.ValidName (internal/store) for the accepted alphabet before writing the replacer.

// Add, when opts.Branch != "" (replaces the :158-179 block; the --cwd block is unreachable because the CLI refuses the pair, but Add itself must also refuse: `--branch and --cwd are exclusive`):
    name := opts.Name (already validated / defaulted by the CLI; Add still requires non-empty)
    // 1. driven-by-live-binding guard
    for each b in rt.Store.List(): if b.Branch == opts.Branch && b.State != StateDone -> error
        fmt.Errorf("branch %s is driven by binding %q (round %d); relay done or unbind it first", opts.Branch, b.Name, b.Round)
    // 2. resolve
    exists := rt.Git.BranchExists(ctx, opts.Repo, opts.Branch)
    if !exists {
        sha, ok := rt.Git.RefSHA(ctx, opts.Repo, "refs/remotes/origin/"+opts.Branch)
        if !ok -> fmt.Errorf("branch %q not found locally or on origin", opts.Branch)
        rt.Git.CreateTrackingBranch(ctx, opts.Repo, opts.Branch, "origin/"+opts.Branch)   // error -> return it
    }
    tip, ok := rt.Git.RefSHA(ctx, opts.Repo, "refs/heads/"+opts.Branch)   // !ok after the above -> error "branch %q vanished"
    // 3. checkout
    cwd = rt.Store.WorktreePath(name)
    err := rt.Git.CheckoutWorktree(ctx, opts.Repo, cwd, opts.Branch)
        errors.Is(err, git.ErrBranchCheckedOut) -> fmt.Errorf("branch %s is checked out in another worktree (git worktree list); free it first", opts.Branch)
        other err -> return it
    worktree, branch, base = cwd, opts.Branch, tip
    // rest of Add unchanged (FindByCWD guard, resolveBuilder, rollback = RemoveWorktree(force) exactly as today; rollback never deletes the branch)
    Binding: ..., Branch: branch, Base: base, ExistingBranch: true

// addRemote, when opts.Branch != "":
    branch := opts.Branch; the same driven-by-live-binding guard; the same local/origin resolution (steps 1-2) -- no worktree is made on the client for a remote binding (as today)
    base := tip of refs/heads/<branch>   // overrides opts.Base; if the caller passed --base as well, refuse: "--base and --branch are exclusive"
    NO CreateBranch; branchCreated stays false so the deferred cleanup never calls DeleteBranch
    Binding: Branch: branch, Base: base, ExistingBranch: true (rest as today)

// cmd/relay/main.go cmdAdd
    --branch string  "existing local or origin/ branch to check out instead of cutting relay/<name>"
    validation before newRuntime, in this order: --branch with --cwd -> error (exit 2 like the feature check);
        --name empty and --branch empty -> today's "relay add requires --name NAME";
        --name empty and --branch set -> name, err := relay.DefaultBindingName(branch); err -> "relay add --branch %s: cannot derive a binding name (%v); pass --name"
    output (worktree case) when result.Binding.ExistingBranch: "worktree %s on existing branch %s (tip %s)"; remote case: "branch %s (existing, tip %s) on %s"
    help line for add gains "[--branch B]"
```

`Fork` is untouched. `worktreeTeardown` (`bind.go:712`), `Unbind`, `GC`,
`Done` are untouched -- the new test only pins that they make no
`DeleteBranch` call for an `ExistingBranch` binding.

## 5. Pseudocode

Covered by §4. Order matters in `Add`: name validation and candidate/tier
resolution first (as today, so a bad candidate never touches git), then
the guard, then git.

## 6. Error handling

- Every refusal above happens before any git write except one: a
  `CheckoutWorktree` failure after `CreateTrackingBranch` leaves the new
  local tracking branch behind. That is correct -- relay deletes no
  branch -- and the error message for that case appends `; local branch %s
  now tracks origin/%s`.
- `resolveBuilder` failure after checkout -> existing rollback removes the
  worktree (force) and returns the error; the branch stays.

## 7. Ordered implementation steps

Commit prefix: exactly **one** `feat(add):` commit on the branch for the
code (squash step commits, or `chore:`/`test:` per step); the plan copy may
be its own `chore(plans):` commit.

### Task 1 -- git primitive and fake

**Files:** `internal/git/client.go`, `client_test.go`, `internal/relay/herdr.go`, `internal/relay/fake_test.go`.

**Test first:** `TestCreateTrackingBranch`: temp repo A with one commit
(`-c commit.gpgsign=false -c tag.gpgsign=false`), `git clone --bare A B`,
`git -C A remote add origin B && git -C A fetch origin`; branch `feat` exists
only as `refs/remotes/origin/feat` (create it in B via `git -C B branch feat
<sha>` then fetch in A). `CreateTrackingBranch(A, "feat", "origin/feat")` ->
`git -C A rev-parse --abbrev-ref feat@{u}` prints `origin/feat`; calling it
again -> `ErrBranchExists`.

**Verify:** `go test -count=1 ./internal/git/ ./internal/relay/` (the fake compiles).

### Task 2 -- Add and addRemote

**Files:** `internal/store/types.go`, `internal/relay/add.go`, `remote.go`, `add_test.go`, `remote_test.go`, `bind_test.go` (or `add_test.go`).

**Tests first** (shape of `TestAddCreatesAWorktreeBindingAtRoundOne`, `add_test.go:60`; `fakeGit` scripts one `branchExists` bool for every name and `refSHA` map lookups -- nil map answers ok for any ref, so give every test an explicit map):
- `TestAddBranchLocalChecksOutWithoutCutting`: `fg.branchExists = true`, `fg.refSHA = {"refs/heads/feature/api-auth": "tip123"}`; `Add{Name: "api-auth", Branch: "feature/api-auth"}` -> `checkoutWorktreeCalls == [{repo, WorktreePath("api-auth"), "feature/api-auth"}]`, `addWorktreeCalls` and `createBranchCalls` and `createTrackingBranchCalls` empty, `Binding.Branch == "feature/api-auth"`, `Base == "tip123"`, `ExistingBranch`, `Worktree == CWD == WorktreePath`. **Mutation check:** make `Add` ignore `opts.Branch` (fall through to the cut path) and this must fail on `addWorktreeCalls`.
- `TestAddBranchOriginOnlyTracksFirst`: `branchExists = false`, `refSHA = {"refs/remotes/origin/feature/x": "o1", "refs/heads/feature/x": "o1"}` -> one `createTrackingBranchCalls == [{repo, "feature/x", "origin/feature/x"}]`, then the checkout; `Base == "o1"`.
- `TestAddBranchMissingRefuses`: `branchExists = false`, `refSHA = {}` -> error contains `not found locally or on origin`; no git write calls.
- `TestAddBranchCheckedOutRefuses`: `checkoutWorktreeErr = git.ErrBranchCheckedOut` -> error contains `checked out in another worktree`; no `resolveBuilder` (no `fakeHerdr` StartAgent / no runner Start); `deleteBranchCalls` empty.
- `TestAddBranchDrivenByLiveBindingRefuses`: seed a binding with `Branch: "feature/x", State: active`; `Add{Branch: "feature/x", Name: "other"}` -> error names the binding; a DONE binding with the same branch does not block.
- `TestAddBranchWithCwdRefused` (Add level).
- `TestDefaultBindingName`: table: `feature/api-auth`->`api-auth`, `v2`->`v2`, `Fix/Login_Form`->whatever the ValidName alphabet yields (write the expectation after reading ValidName; assert `store.ValidName` accepts it), `//`->error.
- `TestUnbindExistingBranchNeverDeletes`: an `ExistingBranch` add binding (from the first test), `Unbind` (see `bind.go:768` and an existing unbind test for the call shape) -> `removeWorktreeCalls == 1`, `deleteBranchCalls == 0`. Also `Done` then `GC`: still `0`.
- `TestAddRemoteExistingBranch` (`remote_test.go`, shape of `TestAddRemoteBranchExistsUnbindsServer` ~670): `branchExists = true`, `refSHA = {"refs/heads/feature/x": "tip"}`; the recorded `CreateBinding` request has `BaseCommit == "tip"`; `createBranchCalls` empty; make `CreateBinding` fail on a second run -> cleanup makes no `DeleteBranch` call.

**Then** the code per §4. **Verify:** `go test -race -count=1 ./internal/relay/`.

### Task 3 -- CLI, README, plan copy, gate

**Files:** `cmd/relay/main.go`, `main_test.go`, `README.md`.

- `TestAddBranchWithCwdIsRefusedBeforeRuntime` (shape of `TestAddValidation` ~480): `run([]string{"add", "--branch", "x", "--cwd", "/tmp"})` -> error mentions `exclusive`; this fails in flag validation before `newRuntime`, so it reaches no herdr (CI runners have none).
- `TestAddBranchDerivesName`: `run([]string{"add", "--branch", "feature/api-auth"})` without `HERDR_PANE_ID`... check what `cmdAdd` does with an empty pane id (Add refuses `PlannerPane` required at `add.go:95`) -- if that error fires before any herdr call, assert on it and that the derived name appears nowhere; if it would reach herdr, drop this test and say so.
- README: in the `relay add` section add a `--branch` paragraph (local first, `origin/` second, checked-out refusal, name default, `ExistingBranch`, works with `--headless` and `--server`, not with `--cwd`; `fork --branch` not yet). In `### Cleaning up finished bindings` add: "relay deletes a branch in **zero** places: not at `unbind`, `gc`, `done`, nor on an add rollback. The one exception is a `relay/<name>` branch `add --server` created seconds earlier and must undo because the server refused the binding."

Copy the plan file to `docs/plans/2026-09-21-w2i1-add-branch.md` and commit.

Gate, in the foreground, stop at the first failure:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Final commit message: `feat(add): --branch checks an existing local or origin branch out into relay's worktree; relay deletes a branch in zero places (#145)`

## Report

Per task: what was done, test names, verify output, the mutation check's
failing test. The `var _ Git` grep. Commit shas (one `feat:`). Say
explicitly that `fork --branch` was not built. If any step was impossible
as written, say which and stop there.
