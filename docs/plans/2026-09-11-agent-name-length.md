# Derived agent names are validated before a pane exists (#64)

**Design spec:** `docs/specs/2026-09-11-agent-name-length-design.md`
**Issue:** #64

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- **No test may execute a `cmd/relay` subcommand that reaches herdr.** CI
  runners have no `herdr` binary. Every test in this plan is in
  `internal/herdr` or `internal/relay` against the fakes.
- A refused name leaves nothing behind: no pane, no worktree, no reservation,
  no question file. Every test in steps 3-4 asserts that.
- Do not change how names are composed, and do not truncate (spec §1: since
  #66 identity is by name, and a truncated prefix collision would make two
  builders look like one).
- Do not change the store's binding-name rule.
- Every new exported symbol gets a doc comment explaining the rationale, in
  the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: validate derived agent names at their composition sites

**Files:**
- Modify: `internal/herdr/types.go`, `internal/herdr/types_test.go`
- Modify: `internal/relay/bind.go`, `internal/relay/bind_test.go`
- Modify: `internal/relay/add.go`, `internal/relay/add_test.go`
- Modify: `internal/relay/fork.go`, `internal/relay/fork_test.go`
- Modify: `internal/relay/ask.go`, `internal/relay/ask_test.go`
- Create: `internal/relay/names.go`, `internal/relay/names_test.go`
- Modify: `internal/relay/fake_test.go`
- Modify: `cmd/relay/main.go`

**Interfaces produced** (spec §3):
- `herdr.MaxAgentNameLen`, `herdr.ErrInvalidAgentName`, `herdr.ValidateAgentName(name string) error`
- `relay.builderAgentName(name string) (string, error)` (unexported helper)
- `relay.ConsultRolesTooLong(t *alias.Table, bindingName string) []string`

- [ ] **Step 1: the rule, in `internal/herdr`** (spec §3.1)

  In `internal/herdr/types.go` add `MaxAgentNameLen = 32`,
  `ErrInvalidAgentName`, and `ValidateAgentName`. The refusal text must be
  herdr's sentence verbatim -- `agent name must start with a lowercase letter
  and contain only lowercase letters, digits, '-' or '_' (1-32 characters)`
  -- prefixed by the name and, when too long, its length, e.g.
  `agent name "…" is 37 characters: agent name must …`. Comment: transcribed
  from herdr 0.9.0's refusal; must follow herdr.

  Test (`types_test.go`): `TestValidateAgentName` table per spec §8 item 1.

  Verify: `go test ./internal/herdr/` green.

- [ ] **Step 2: the fake enforces it** (spec §4.3)

  `internal/relay/fake_test.go` `StartAgent`: after recording the call,
  `if err := herdr.ValidateAgentName(name); err != nil { return err }`,
  before the `startErr` check. Comment: the fixture cannot drift from the
  real client again (#64 was found because it had).

  Test (`fake_test.go`, next to `TestFakeSatisfiesGit`):
  `TestFakeStartAgentRefusesAnInvalidName` -- a 33-character name returns
  an error wrapping `herdr.ErrInvalidAgentName`.

  Verify: full `go test ./internal/relay/` still green (every fixture name
  is short).

- [ ] **Step 3: builders** (spec §4.1)

  In `internal/relay/bind.go` add, near `resolveBuilder`:

  ```
  // builderAgentName composes and validates the herdr agent name for a
  // binding's builder. It runs before any pane or worktree exists, so a name
  // herdr would refuse fails as a validation error with nothing to clean up.
  func builderAgentName(name string) (string, error)
  ```

  returning `name + "-builder"`, or the wrapped error from spec §4.1 with
  the `at most N characters` clause computed from `MaxAgentNameLen`.

  Call it:
  - in `resolveBuilder`, replacing the bare composition;
  - at the top of `Add` (`add.go`) **before** `AddWorktree`, discarding the
    name (resolveBuilder recomputes it) -- the point is to refuse before the
    worktree is cut;
  - at the top of `Fork` (`fork.go`) likewise, before its `AddWorktree`,
    validating `opts.NewName`.

  Tests:
  - `bind_test.go` `TestBindRefusesABuilderNameHerdrWouldRefuse` -- 25-char
    binding name, spawn path: error wraps `herdr.ErrInvalidAgentName`,
    `f.splits == 0`, `len(f.starts) == 0`, `rt.Store.Load` returns
    `store.ErrNotFound`.
  - `add_test.go` `TestAddRefusesALongNameBeforeCuttingAWorktree` -- same,
    plus `len(g.addWorktreeCalls) == 0` (look at how existing add tests
    reach the fake git).
  - `fork_test.go` `TestForkRefusesALongNameBeforeCuttingAWorktree` --
    same shape for `Fork`.

  Verify: `go test ./internal/relay/ -run 'Bind|Add|Fork'` green.

- [ ] **Step 4: consults** (spec §4.2)

  In `internal/relay/ask.go`: move `id := newID()` out of the phase 1
  closure to just before it; compose `agentName` from `opts.Name`,
  `spec.Name`, `id`; validate with the wrapped error from spec §4.2 (the
  `at most N characters to run %q consults` clause computed from
  `MaxAgentNameLen`, the role name and the 8-hex id). Phase 1 uses the
  pre-minted `id` and `agentName`; nothing else in `Ask` changes.

  Test (`ask_test.go`): `TestAskRefusesAConsultNameHerdrWouldRefuse` --
  seed via `seedForAsk` but with a 15-character binding name (check how
  `seedBound` names the binding; if it is fixed at `webshop`, seed a second
  binding by hand with the long name and the same CWD rules). `Ask` errors
  wrapping `herdr.ErrInvalidAgentName`; `f.splits == 0`; the binding has no
  `Consults`; no `*-ask.md` exists under the binding's state dir.

  Verify: `go test ./internal/relay/ -run Ask` green.

- [ ] **Step 5: the bind-time note** (spec §3.2, §4.4)

  Create `internal/relay/names.go` with `ConsultRolesTooLong` per spec
  §3.2 and §5, and `names_test.go` with the three cases in spec §8 item 5
  (use `consultTable(t)` from `fake_test.go` for the table with `reviewer`;
  `alias.DefaultTable()` or an empty table for the no-consult case --
  check what the package offers).

  In `cmd/relay/main.go`, after the success `Printf` of `bind` (spawn path
  only: skip when `--resume` or a pane was adopted), `add` and `fork`, call
  `relay.ConsultRolesTooLong(rt.Aliases, name)` and, when non-empty, print
  the note from spec §4.4. The limit is `herdr.MaxAgentNameLen - 10 -
  len(longestRole)`; name the role that sets it.

  Verify: `go build ./... && go test ./internal/relay/ -run ConsultRoles` green.

- [ ] **Step 6: full check and mutations**

  `make check` (or constituents) green.

  Do each, run the named test, revert, record:
  1. Remove the validation call from `resolveBuilder`. Expect
     `TestBindRefusesABuilderNameHerdrWouldRefuse` to fail on `f.splits`.
  2. Remove the early call from `Add`. Expect
     `TestAddRefusesALongNameBeforeCuttingAWorktree` to fail on
     `addWorktreeCalls`.
  3. Remove the validation from `Ask`. Expect
     `TestAskRefusesAConsultNameHerdrWouldRefuse` to fail on the question
     file existing.
  4. Make the fake accept any name. Expect
     `TestFakeStartAgentRefusesAnInvalidName` to fail.

- [ ] **Step 7: commit**

  One commit. Subject:
  `fix(relay): refuse a derived agent name herdr would refuse, before anything exists (#64)`.
  Body: the two suffixes and their limits, that the rule lives in
  `internal/herdr` and the fake enforces it, and that truncation was
  rejected because identity is now by name. Do not push.

## Report

- `git diff --stat` against your base.
- The `make check` (or constituents) output tail.
- The mutation table, all four.
- Anything you stopped on, or any place the plan and the code disagreed.
