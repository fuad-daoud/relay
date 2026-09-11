# Candidates T6: `doctor` scopes from candidates; `relay candidates` lists them (#80)

**Design spec:** `docs/specs/2026-09-11-candidates-design.md` -- read §4.10, §4.12, §5.3
**Issue:** #80
**Depends on:** T5 -- merged into this tree.

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
- After this task, **nothing outside `internal/relay/herdr.go` (the
  `Runtime` struct) and `cmd/relay/main.go` (`newRuntime`) references
  `internal/alias`**. T7 deletes those two and the package.
- **No test may execute a `cmd/relay` subcommand that reaches herdr.**
  `assembleKinds` is already tested as a pure function in `cmd/relay`;
  keep it that way. The `relay candidates` formatter lives in
  `internal/relay` and is tested there.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: doctor scope, the no-candidates row, the listing, the agy hint

**Files:**
- Modify: `cmd/relay/doctor.go`, `cmd/relay/doctor_test.go`
- Create: `internal/relay/candidates_list.go`, `internal/relay/candidates_list_test.go`
- Modify: `cmd/relay/main.go` (`cmdCandidates`, dispatch, help text)
- Modify: `cmd/relay/agent.go` (hint text)

**Interfaces consumed:** `candidate.Set` (`Len`, `Refs`, `Lookup`),
`candidate.ParseRef`, `Runtime.Candidates`; test fixtures `candidateSet`,
`testCandidatesJSON` (package `relay`).

**Interfaces produced:**
- `assembleKinds(set *candidate.Set, st *store.Store) ([]string, error)` (signature change)
- `relay.FormatCandidates(set *candidate.Set) string`
- `relay candidates` subcommand

- [ ] **Step 1: `assembleKinds` over candidates** (spec §4.10)

  `doctor.go`: change the first parameter to `*candidate.Set`. Kinds are
  the distinct `Harness` of every candidate (`for _, ref := range
  set.Refs()` → `set.Lookup(candidate.ParseRef(ref))` → `.Harness`;
  `ParseRef` cannot fail on a key the set produced -- comment that) plus
  every binding's `Builder.Kind`, exactly as today. A nil set is treated
  as empty. Update the doc comment ("every kind named by a configured
  candidate, plus …").

  `cmdDoctor`: `assembleKinds(rt.Candidates, rt.Store)`. After the
  existing `storeErr` row insertion add:

  ```
  if rt.Candidates.Len() == 0 {
      rep.Checks = insertGlobalCheck(rep.Checks, doctor.Check{
          Name:     "candidates",
          Severity: doctor.SevWarn,
          Detail:   "none configured",
          Fix:      "write ~/.config/relay/candidates.json; see README \"Candidates\"",
      })
  }
  ```

  (Read `doctor.Check` for the exact field names; `Fix` exists --
  `roleCheck` uses it.)

  `doctor_test.go`: replace `alias.DefaultTable()` with a set built the
  way `internal/relay` tests do it -- but `candidateSet` is in package
  `relay` and not importable. Write a small local helper in
  `doctor_test.go`:

  ```go
  func testSet(t *testing.T, body string) *candidate.Set   // temp file + candidate.Load
  ```

  and a `const threeKinds = `[{"harness":"agy","provider":"t","model":"m","roles":["builder"]},{"harness":"claude","provider":"t","model":"m","roles":["builder"]},{"harness":"opencode","provider":"t","model":"m","roles":["builder"]}]``.
  The two existing tests keep their expectations. Add
  `TestAssembleKindsWithNoCandidatesUsesBindingsOnly`: `testSet(t, "[]")`
  plus a store with one binding whose `Builder.Kind == "claude"` → kinds
  `[claude]`. Drop the `alias` import.

  Run: `go test ./cmd/relay/ -run AssembleKinds`. Expected: green.

- [ ] **Step 2: `FormatCandidates`** (spec §4.12, §5.3)

  Create `internal/relay/candidates_list.go`:

  ```go
  // FormatCandidates renders the configured candidates for `relay candidates`:
  // one line per token, sorted, with the roles it serves and any extra args in
  // brackets. It is a listing, not a check -- zero candidates prints the same
  // sentence the bind refusal uses, so the planner learns the file name once.
  func FormatCandidates(set *candidate.Set) string
  ```

  Output: when `set.Len() == 0`, the string
  `no candidates configured; write ~/.config/relay/candidates.json (see README "Candidates")\n`.
  Otherwise, for each ref in `Refs()` order:
  `fmt.Sprintf("%-*s  %s", width, ref, strings.Join(c.Roles, ", "))`
  where `width` is the longest ref; if `len(c.ExtraArgs) > 0`, append
  `"   [" + strings.Join(c.ExtraArgs, " ") + "]"`; then `\n`.

  Test `candidates_list_test.go`: `TestFormatCandidates` -- with
  `candidateSet(t, testCandidatesJSON)` the output is exactly

  ```
  agy/test/m       builder   [--dangerously-skip-permissions]
  claude/test/m    builder, reviewer
  opencode/test/m  builder
  ```

  (three lines, each `\n`-terminated; compute the padding from the
  longest ref, `opencode/test/m`, 15 characters). And
  `TestFormatCandidatesEmpty` -- `candidateSet(t, "[]")` yields the
  one-line sentence.

  Run: `go test ./internal/relay/ -run FormatCandidates`. Expected: green.

- [ ] **Step 3: `relay candidates`**

  `main.go`: add `case "candidates": return cmdCandidates(rest)` beside
  `doctor` in the dispatch (match the surrounding style for how `rest` is
  passed), and

  ```go
  func cmdCandidates(args []string) error
  ```

  which parses an empty `flag.FlagSet` (so `--help` works and stray args
  error like other subcommands), builds the runtime, and
  `fmt.Print(relay.FormatCandidates(rt.Candidates))`. Exit 0 always.

  Add the command to the help text (`cmdHelp` or wherever the subcommand
  list lives): `candidates   list the configured harness/provider/model
  candidates`. Also update every help line that says `--builder ALIAS`
  to `--builder CANDIDATE`.

  No test (the runtime touches herdr).

  Run: `go build ./... && go run ./cmd/relay candidates` on this machine
  is **not** required -- the planner verifies that. `go build` clean is
  the gate.

- [ ] **Step 4: the agy hint in `agent.go`**

  Replace the sentence after `agy selects its role with a preamble on the
  first prompt, not an agent file;` with `relay prepends it when it starts
  an agy candidate (see README "Candidates")`.

- [ ] **Step 5: `make check`, commit**

  Run `grep -rn "internal/alias" --include='*.go' .`. Expected: exactly
  two hits, `internal/relay/herdr.go` and `cmd/relay/main.go`. If there
  are more, something in this task was missed -- fix before committing.

  ```bash
  git add -A internal cmd
  git commit -m "feat(relay): doctor scopes from candidates; relay candidates lists them (#80)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, and the output
of `grep -rn "internal/alias" --include='*.go' .`.
