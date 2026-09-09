# Peer Builders — `relay add` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a planner attach additional builders as peers, each on its own git worktree, from a cold start — without pretending they are forks of anything.

**Architecture:** `relay bind` always binds `os.Getwd()` and `ErrCWDTaken` allows one binding per tree, so a planner pane gets exactly one builder. The only existing route to a second is `relay fork`, which requires a source binding *and* a round with a plan already in the log — so three parallel builders are impossible from a cold start. `relay add` is a new verb that creates a worktree and binds a builder to it the way `fork` does, minus the parts that only make sense for a branched timeline: no source binding, no round-history copy, no `ForkedFrom`/`ForkedAtRound` provenance, and the new binding starts at round 1. It reuses `resolveBuilder`, `endpointOf`, `store.WorktreePath` and the same rollback discipline.

**Tech Stack:** Go 1.x, stdlib `testing`, the existing `internal/git` client. No new dependencies.

## Global Constraints

- New code lives in `internal/relay/add.go` and `internal/relay/add_test.go`. Do not add cases to `fork.go`.
- Reuse, do not reimplement: `resolveBuilder(ctx context.Context, rt Runtime, opts BindOptions, name, plannerPane string) (store.Endpoint, error)`, `endpointOf(a herdr.Agent) store.Endpoint`, `rt.Store.WorktreePath(name string) string`, `rt.Git.HeadCommit` / `BranchExists` / `AddWorktree` / `RemoveWorktree`, `store.ValidName`.
- Branch naming matches `fork` exactly: `"relay/" + name`.
- `RoundCap` and `RoundTimeoutMS` are left zero on the binding — `store.Save` defaults them (`internal/store/store.go:274-279`). Do not hardcode 20 or 86400000.
- `Add` dispatches **no** hook event. `Bind` dispatches none either; `EventForkCreated` is specific to forks and must not be reused.
- Test style, verbatim from this package: no testify, `t.Fatalf` for setup failures, `t.Error`/`t.Errorf` for behavioural assertions, `%+v` for structs, assertion messages that state the rule.
- Every task ends green: `go build ./... && go test ./...`.

---

### Task 1: `Add` core

**Files:**
- Create: `internal/relay/add.go`
- Test: `internal/relay/add_test.go`

**Interfaces:**
- Consumes: `Runtime`, `BindOptions`, `resolveBuilder`, `endpointOf`, `store.Binding`, `store.Endpoint`, `store.ValidName`, `store.ErrNotFound`, `store.ErrCWDTaken`, `store.StateActive`, `store.StateDone`, `git.ErrBranchExists`, `ErrGitRequired` (already declared in `fork.go:23`), `FindAgent`.
- Produces:
  - `type AddOptions struct { Name, Alias, PlannerPane, Repo string; NewTab bool; WorkspaceID, CWD string }`
  - `type AddResult struct { Binding store.Binding; Worktree, Branch, Base string }`
  - `func Add(ctx context.Context, rt Runtime, opts AddOptions) (AddResult, error)`
  - `var ErrAliasRequired = errors.New("relay add needs --builder ALIAS")`

- [ ] **Step 1: Confirm the current suite is green**

Run: `cd /home/fuad/projects/relay && go test ./...`
Expected: PASS (all packages).

- [ ] **Step 2: Write the failing tests**

Create `internal/relay/add_test.go`:

```go
package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// addRepo makes a directory to stand in for the planner's repository.
func addRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAddCreatesAWorktreeBindingAtRoundOne(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	repo := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Alias: "abuilder", PlannerPane: "w2:p3", Repo: repo,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got.Binding.Round != 1 {
		t.Errorf("a peer builder starts at round 1, got %d", got.Binding.Round)
	}
	if got.Binding.State != store.StateActive {
		t.Errorf("state = %q, want active", got.Binding.State)
	}
	if got.Binding.ForkedFrom != "" || got.Binding.ForkedAtRound != 0 {
		t.Errorf("a peer builder was never forked from anything, got %+v", got.Binding)
	}
	if got.Branch != "relay/frontend" {
		t.Errorf("branch = %q, want relay/frontend", got.Branch)
	}
	if got.Base != "commit-head-123" {
		t.Errorf("base = %q, want the repo HEAD", got.Base)
	}

	want := rt.Store.WorktreePath("frontend")
	if got.Worktree != want || got.Binding.CWD != want {
		t.Errorf("worktree = %q, cwd = %q, want both %q", got.Worktree, got.Binding.CWD, want)
	}
	if got.Binding.Worktree != want {
		t.Errorf("relay must record the tree it created so it may remove it, got %q", got.Binding.Worktree)
	}

	if len(fg.addWorktreeCalls) != 1 {
		t.Fatalf("addWorktreeCalls = %+v", fg.addWorktreeCalls)
	}
	call := fg.addWorktreeCalls[0]
	if call.Dir != repo || call.Path != want || call.Branch != "relay/frontend" || call.Commit != "commit-head-123" {
		t.Errorf("worktree cut wrongly: %+v", call)
	}

	// The round log must exist and be empty of relayed messages: nothing has
	// been handed over yet.
	entries, err := rt.Store.ReadLog("frontend")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a fresh peer binding has relayed nothing, got %+v", entries)
	}
}

func TestAddRequiresABuilderAlias(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", PlannerPane: "w2:p3", Repo: addRepo(t),
	})
	if !errors.Is(err, ErrAliasRequired) {
		t.Fatalf("want ErrAliasRequired, got %v", err)
	}
}

func TestAddRefusesADuplicateName(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	repo := addRepo(t)

	if _, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Alias: "abuilder", PlannerPane: "w2:p3", Repo: repo,
	}); err != nil {
		t.Fatalf("first Add: %v", err)
	}

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Alias: "abuilder", PlannerPane: "w2:p3", Repo: repo,
	})
	if err == nil {
		t.Fatal("a name already in use must be refused")
	}
}

func TestAddRollsBackTheWorktreeWhenTheBuilderFailsToStart(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	// No newPane, so splitting the planner's pane yields nothing to start in.
	fh.newPane = ""
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Alias: "abuilder", PlannerPane: "w2:p3", Repo: addRepo(t),
	})
	if err == nil {
		t.Fatal("expected the add to fail")
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Errorf("a failed add must not leave its worktree behind, calls = %+v", fg.removeWorktreeCalls)
	}
	if _, err := rt.Store.Load("frontend"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a failed add must leave no binding, got %v", err)
	}
}

func TestAddBindsAPreparedDirectoryWithCWD(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	prepared := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "legacy", Alias: "abuilder", PlannerPane: "w2:p3",
		Repo: addRepo(t), CWD: prepared,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.Binding.CWD != prepared {
		t.Errorf("cwd = %q, want the prepared directory %q", got.Binding.CWD, prepared)
	}
	if got.Worktree != "" || got.Binding.Worktree != "" {
		t.Error("relay did not create this tree, so it must never record ownership of it")
	}
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("--cwd must not cut a worktree, calls = %+v", fg.addWorktreeCalls)
	}
}

func TestAddRefusesATreeAnotherBindingDrives(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	prepared := addRepo(t)

	if err := rt.Store.Save(store.Binding{
		Name: "incumbent", CWD: prepared, Round: 1, State: store.StateActive,
		Builder: store.Endpoint{PaneID: "w2:p4"},
	}); err != nil {
		t.Fatalf("seed incumbent: %v", err)
	}

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Alias: "abuilder", PlannerPane: "w2:p3",
		Repo: addRepo(t), CWD: prepared,
	})
	if !errors.Is(err, store.ErrCWDTaken) {
		t.Fatalf("want ErrCWDTaken, got %v", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /home/fuad/projects/relay && go test ./internal/relay/ -run TestAdd -v`
Expected: FAIL to compile — `undefined: Add`, `undefined: AddOptions`, `undefined: ErrAliasRequired`.

- [ ] **Step 4: Write the implementation**

Create `internal/relay/add.go`:

```go
package relay

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrAliasRequired reports an add with no builder to spawn. Unlike a fork,
// which can inherit its source's alias, a peer builder has nothing to inherit
// from.
var ErrAliasRequired = errors.New("relay add needs --builder ALIAS")

// AddOptions describes one peer-builder request.
type AddOptions struct {
	Name        string // name for the new binding; required, must be free
	Alias       string // builder alias to spawn; required
	PlannerPane string // the calling pane, from $HERDR_PANE_ID; required
	Repo        string // the repository the worktree is cut from; the caller's cwd

	NewTab      bool // open the builder in its own tab
	WorkspaceID string

	// CWD binds the peer to a directory the human already prepared instead of
	// creating a worktree. It is the escape hatch for a non-git tree; relay
	// records no Worktree for it and will never remove it.
	CWD string
}

// AddResult is what an add produced, so the CLI can tell the human where the
// new tree and branch are without re-deriving them.
type AddResult struct {
	Binding  store.Binding
	Worktree string // "" when --cwd was used
	Branch   string // "" when --cwd was used
	Base     string // commit the worktree was cut from; "" when --cwd was used
}

// Add attaches an additional builder to the calling planner, on its own tree.
//
// It is deliberately not `fork`. A fork continues a timeline: it copies round
// history through some round, starts at the round after it, and records where
// it came from. A peer was never a continuation of anything -- it starts at
// round 1 with an empty log and no provenance -- so writing ForkedFrom on it
// would record a relationship that does not exist.
//
// Preconditions:  opts.PlannerPane names a live agent pane; opts.Name is valid
//
//	and unused; opts.Alias is non-empty; opts.Repo is a git
//	repository unless opts.CWD is given.
//
// Postconditions: on success a new binding exists at round 1, in StateActive,
//
//	with a running builder and its own working tree. On ANY
//	error, no binding exists and any worktree Add created has
//	been removed.
//
// Errors: ErrAliasRequired, ErrGitRequired, store.ErrCWDTaken,
//
//	git.ErrBranchExists, or a wrapped herdr failure.
func Add(ctx context.Context, rt Runtime, opts AddOptions) (AddResult, error) {
	if opts.PlannerPane == "" {
		return AddResult{}, errors.New("no planner pane; is HERDR_PANE_ID set")
	}
	if err := store.ValidName(opts.Name); err != nil {
		return AddResult{}, err
	}
	if opts.Alias == "" {
		return AddResult{}, ErrAliasRequired
	}

	agents, err := rt.Herdr.ListAgents(ctx)
	if err != nil {
		return AddResult{}, fmt.Errorf("list agents: %w", err)
	}
	planner, ok := FindAgent(agents, store.Endpoint{PaneID: opts.PlannerPane})
	if !ok {
		return AddResult{}, fmt.Errorf("no planner agent in pane %s", opts.PlannerPane)
	}

	if _, err := rt.Store.Load(opts.Name); err == nil {
		return AddResult{}, fmt.Errorf(
			"binding %q already exists: `relay unbind %s` to start fresh, or `relay bind --resume --name %s` to adopt it",
			opts.Name, opts.Name, opts.Name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return AddResult{}, err
	}

	var (
		cwd      string
		worktree string
		branch   string
		base     string
	)

	if opts.CWD != "" {
		info, err := os.Stat(opts.CWD)
		if err != nil {
			return AddResult{}, fmt.Errorf("stat %s: %w", opts.CWD, err)
		}
		if !info.IsDir() {
			return AddResult{}, fmt.Errorf("%s is not a directory", opts.CWD)
		}
		cwd = opts.CWD
	} else {
		if rt.Git == nil {
			return AddResult{}, ErrGitRequired
		}
		base, err = rt.Git.HeadCommit(ctx, opts.Repo)
		if err != nil {
			return AddResult{}, err
		}
		branch = "relay/" + opts.Name
		exists, err := rt.Git.BranchExists(ctx, opts.Repo, branch)
		if err != nil {
			return AddResult{}, err
		}
		if exists {
			return AddResult{}, git.ErrBranchExists
		}
		cwd = rt.Store.WorktreePath(opts.Name)
		if err := rt.Git.AddWorktree(ctx, opts.Repo, cwd, branch, base); err != nil {
			return AddResult{}, err
		}
		worktree = cwd
	}

	rollback := func() {
		if worktree != "" && rt.Git != nil {
			_ = rt.Git.RemoveWorktree(ctx, opts.Repo, worktree, true)
		}
	}

	other, found, err := rt.Store.FindByCWD(cwd)
	if err != nil {
		rollback()
		return AddResult{}, err
	}
	if found && other.Name != opts.Name && other.State != store.StateDone {
		rollback()
		return AddResult{}, fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
			cwd, other.Name, other.Builder.PaneID, other.Round, store.ErrCWDTaken)
	}

	bindOpts := BindOptions{
		Name:        opts.Name,
		Alias:       opts.Alias,
		PlannerPane: planner.PaneID,
		CWD:         cwd,
		NewTab:      opts.NewTab,
		WorkspaceID: opts.WorkspaceID,
	}
	builder, err := resolveBuilder(ctx, rt, bindOpts, opts.Name, planner.PaneID)
	if err != nil {
		rollback()
		return AddResult{}, err
	}

	// RoundCap, RoundTimeoutMS, CreatedAt and UpdatedAt are all left zero on
	// purpose: store.Save fills them in (internal/store/store.go:269-279), so a
	// peer builder gets exactly the same defaults a plain `relay bind` does.
	b := store.Binding{
		Name:         opts.Name,
		CWD:          cwd,
		Planner:      endpointOf(planner),
		Builder:      builder,
		BuilderAlias: opts.Alias,
		Round:        1,
		State:        store.StateActive,
		Worktree:     worktree,
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.Save(b)
	}); err != nil {
		rollback()
		return AddResult{}, fmt.Errorf("add failed after starting builder in pane %s (close it yourself): %w",
			builder.PaneID, err)
	}

	if stored, err := rt.Store.Load(b.Name); err == nil {
		b = stored
	}

	return AddResult{Binding: b, Worktree: worktree, Branch: branch, Base: base}, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/fuad/projects/relay && go test ./internal/relay/ -run TestAdd -v`
Expected: PASS — all six `TestAdd*` tests.

- [ ] **Step 6: Run the whole suite**

Run: `cd /home/fuad/projects/relay && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /home/fuad/projects/relay
git add internal/relay/add.go internal/relay/add_test.go
git commit -m "feat(relay): add peer builders (#1)

relay bind always binds the caller's cwd, and one binding per tree means a
planner pane gets exactly one builder. The only route to a second was
relay fork, which needs a source binding and a round with a plan already
logged -- so three parallel builders were impossible from a cold start.

Add attaches a builder on its own worktree at round 1, with an empty log
and no provenance. It is not a fork: nothing was continued, so nothing
records ForkedFrom.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

### Task 2: The `relay add` command

**Files:**
- Modify: `cmd/relay/main.go` — the usage block near line 43, the dispatch switch near line 137, and a new `cmdAdd` placed immediately after `cmdFork` (which ends at line 336)
- Test: `cmd/relay/main_test.go` (append)

**Interfaces:**
- Consumes: `relay.Add`, `relay.AddOptions`, `relay.AddResult`, `relay.ErrAliasRequired` from Task 1; `parseFlags(fs *flag.FlagSet, args []string) error`; `newRuntime() (relay.Runtime, error)`; `run(args []string) error`; `errHelpShown`.
- Produces: `func cmdAdd(args []string) error`, and a `case "add":` arm in `run`.

- [ ] **Step 1: Write the failing test**

Append to `cmd/relay/main_test.go`:

```go
func TestAddHelp(t *testing.T) {
	err := run([]string{"add", "-h"})
	if !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestAddValidation(t *testing.T) {
	// Missing --name
	err := run([]string{"add", "--builder", "cbuilder"})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("expected an error about --name, got %v", err)
	}

	// Missing --builder
	err = run([]string{"add", "--name", "frontend"})
	if err == nil || !strings.Contains(err.Error(), "--builder") {
		t.Fatalf("expected an error about --builder, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestAdd -v`
Expected: FAIL — `run` returns `unknown subcommand "add"`, so neither the help sentinel nor the flag errors appear.

- [ ] **Step 3: Add the usage line**

In `cmd/relay/main.go`, in the usage block, insert this line immediately after the `bind` line (currently line 43):

```
  add       attach an additional builder to this planner, on its own worktree
```

- [ ] **Step 4: Add the dispatch arm**

In `cmd/relay/main.go`, in `run`'s switch, immediately after:

```go
	case "bind":
		return cmdBind(args[1:])
```

insert:

```go
	case "add":
		return cmdAdd(args[1:])
```

- [ ] **Step 5: Write cmdAdd**

In `cmd/relay/main.go`, immediately after `cmdFork` ends (line 336), insert:

```go
func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	name := fs.String("name", "", "name for the new binding")
	builderAlias := fs.String("builder", "", "builder alias to spawn")
	newTab := fs.Bool("tab", false, "open the builder in its own tab instead of splitting this pane")
	cwd := fs.String("cwd", "", "bind the peer to an existing directory instead of creating a git worktree")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *name == "" {
		return fmt.Errorf("relay add requires --name NAME")
	}
	if *builderAlias == "" {
		return fmt.Errorf("relay add requires --builder ALIAS")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	repo, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	res, err := relay.Add(context.Background(), rt, relay.AddOptions{
		Name:        *name,
		Alias:       *builderAlias,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
		Repo:        repo,
		NewTab:      *newTab,
		WorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"),
		CWD:         *cwd,
	})
	if err != nil {
		return err
	}

	fmt.Printf("added %s: builder %s in pane %s\n",
		res.Binding.Name, res.Binding.BuilderAlias, res.Binding.Builder.PaneID)
	if res.Worktree != "" {
		fmt.Printf("  worktree %s on %s (from %s)\n", res.Worktree, res.Branch, res.Base)
	} else {
		fmt.Printf("  tree %s\n", res.Binding.CWD)
	}
	fmt.Printf("  relay send --name %s --file <plan.md>\n", res.Binding.Name)

	return nil
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestAdd -v`
Expected: PASS.

- [ ] **Step 7: Run the whole suite**

Run: `cd /home/fuad/projects/relay && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
cd /home/fuad/projects/relay
git add cmd/relay/main.go cmd/relay/main_test.go
git commit -m "feat(relay): relay add command

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

### Task 3: Documentation

**Files:**
- Modify: `README.md` — the command list (`add` bullet after the `bind` entry), and a new section after "### Forking a binding"

**Interfaces:**
- Consumes: the behaviour shipped in Tasks 1 and 2.
- Produces: nothing code-facing.

- [ ] **Step 1: Add the command-list bullet**

In `README.md`, insert immediately before the `relay fork` bullet:

```markdown
- `relay add --name N --builder ALIAS [--tab] [--cwd DIR]` — attach an
  additional builder to this planner on its own git worktree, starting at
  round 1. This is how one planner drives several builders at once.
```

- [ ] **Step 2: Add the peer-builders section**

In `README.md`, insert this section immediately before "### Forking a binding":

````markdown
### Running several builders at once

`relay bind` gives the planner one builder over the current tree. `relay add`
attaches more, each on its own git worktree, so they never contend for files:

```
relay bind --builder cbuilder --name api
relay add  --name frontend --builder cbuilder
relay add  --name backend  --builder builder

relay send --name frontend --file ui_plan.md
relay send --name backend  --file api_plan.md
```

Each peer is an ordinary binding: its own round counter, round log, captured
diffs and budget. `relay status` lists them all, and every verb that acts on a
binding takes `--name`.

Relay does not sequence them and does not merge their trees. The planner
decides how many builders it needs, which run in parallel and which wait, and
integrates the results — relay only carries plans out and reports back.

**`relay add` is not `relay fork`.** A fork continues a timeline: it copies
round history through a chosen round and starts at the round after it. A peer
starts at round 1 with an empty log, because it is not a continuation of
anything.
````

- [ ] **Step 3: Verify the docs match the shipped flags**

Run: `cd /home/fuad/projects/relay && go run ./cmd/relay add -h`
Expected: the printed flags are exactly `--name`, `--builder`, `--tab`, `--cwd` — matching the README bullet with no extras and nothing missing.

- [ ] **Step 4: Commit**

```bash
cd /home/fuad/projects/relay
git add README.md
git commit -m "docs(relay): document peer builders

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

## Notes for the reviewer

- **Why a new verb rather than `bind --cwd`?** `bind` means "this pane, this tree, my builder". A flag that redirects it elsewhere makes the common invocation ambiguous at the call site, and gives no place to hang worktree creation. `add` reads as what it is, and its output can tell the human where the new tree is.
- **Why does `Add` take `Repo` explicitly instead of calling `os.Getwd()`?** So it is testable without changing process state, matching `Fork`, which takes the source binding's CWD. The CLI supplies the caller's cwd.
- **Ordering against the other two plans.** `add` makes several builders per planner routine, which is what turns the fan-in clobber (#46) and the `relay answer` guess from latent hazards into everyday ones. Land those two first.
