# Candidates T5: `ask` resolves a role through the role table and a candidate (#80)

**Design spec:** `docs/specs/2026-09-11-candidates-design.md` -- read §3.7, §4.8, §4.9, §5.4, §6
**Issue:** #80
**Depends on:** T4b -- merged into this tree.

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
- After this task, **nothing in `internal/relay` reads `rt.Aliases`**.
  `Runtime.Aliases` itself stays (T6 moves `doctor` off it, T7 deletes
  it). `consultTable` in `fake_test.go` is deleted here.
- **No test may execute a `cmd/relay` subcommand that reaches herdr.**
- The error order in `Ask` (spec §4.8) is: unknown role → wrong shape →
  candidate resolution → tree `none` → agent-name length. All before the
  lock. Preserve every existing "nothing left behind on refusal"
  assertion.
- The consult agent name is `<binding>-<role>-<id>` where `role` is the
  **role-table name**, which with the test fixture is the same string
  (`reviewer`) as before, so name-length tests keep their arithmetic.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: `Ask` on roles + candidates; `ConsultRolesTooLong` on roles

**Files:**
- Modify: `internal/relay/ask.go`, `internal/relay/ask_test.go`
- Modify: `internal/relay/names.go`, `internal/relay/names_test.go`
- Modify: `internal/relay/fake_test.go` (delete `consultTable`)
- Modify: `internal/store/types.go` (the `Consult.Role` comment only)
- Modify: `cmd/relay/main.go` (`cmdAsk`: `--candidate` flag; `noteConsultRolesTooLong`)

**Interfaces consumed:**
- T1: `harness.RoleByName`, `harness.RoleNames`, `harness.ShapeConsult`, `harness.Lookup`, `Harness.Launch`
- T2: `candidate.Set`
- T4a: `resolveCandidate`, `ErrUnknownRole`; fixtures `candidateSet`, `testCandidatesJSON`, `testClaudeRef`
- T4b: `Runtime.Candidates`

**Interfaces produced:**
- `relay.AskOptions.Candidate string`
- `relay.ConsultRolesTooLong(bindingName string) []string` (signature change: no table)
- `relay.ErrNotAConsultRole` (kept; new text)

- [ ] **Step 1: `ConsultRolesTooLong` over the role table** (spec §4.9)

  `names.go`: drop the `alias` import and the table parameter:

  ```go
  func ConsultRolesTooLong(bindingName string) []string
  ```

  Iterate `harness.RoleNames()`, keep those whose `RoleByName(...).Shape ==
  harness.ShapeConsult`, apply the same arithmetic with the role name.
  Rewrite the doc comment: the roles are relay's own now, so the note no
  longer depends on what the machine has configured -- only on the name.

  `names_test.go`: drop the `alias` import and every `consultTable` use.
  The limit is `len(name) + 1 + len(role) + 1 + 8 > 32`, i.e. a role is
  too long once `len(name) > 22 - len(role)`: `reviewer` (8) from 15
  characters, `researcher` (10) from 13. Assert:

  | binding name | want |
  |---|---|
  | 12 characters (`abcdefghijkl`) | empty |
  | 13 characters (`abcdefghijklm`) | `[researcher]` |
  | 14 characters (`abcdefghijklmn`) | `[researcher]` |
  | 15 characters (`abcdefghijklmno`) | `[researcher reviewer]` (sorted) |
  | empty | empty |

  `main.go` `noteConsultRolesTooLong(aliases *alias.Table, name string)`
  → `noteConsultRolesTooLong(name string)`; update its three callers and
  drop the `alias` import from `main.go` **only if** nothing else in the
  file still uses it (`newRuntime` does until T6 -- so leave the import).

  Run: `go test ./internal/relay/ -run ConsultRolesTooLong`. Expected:
  green.

- [ ] **Step 2: the failing `ask` tests** (spec §4.8)

  In `ask_test.go`, the fixture: every `rt.Aliases = consultTable(t)` line
  goes; the default `newRuntime` set (`testCandidatesJSON`) has exactly one
  reviewer-serving candidate (`claude/test/m`), so `Role: "reviewer"` with
  no `Candidate` resolves to it. Replace `TestAskRefusesABuilderAlias`
  with two tests, and add three:

  - `TestAskRefusesAnUnknownRole`: `Role: "reviwer"`; want
    `errors.Is(err, ErrUnknownRole)`, message contains `known:`, no
    starts, no splits, no reservation in the store.
  - `TestAskRefusesTheBuilderRole`: `Role: "builder"`; want
    `ErrNotAConsultRole`, same nothing-left-behind assertions.
  - `TestAskRefusesACandidateThatDoesNotServeTheRole`: `Role: "reviewer",
    Candidate: testAgyRef`; want `ErrRoleNotServed`.
  - `TestAskRefusesAnAmbiguousCandidate`: `rt.Candidates = candidateSet(t,
    `[{"harness":"claude","provider":"a","model":"m","roles":["reviewer"]},{"harness":"claude","provider":"b","model":"m","roles":["reviewer"]}]`)`,
    `Role: "reviewer"`; want `ErrAmbiguousCandidate`.
  - `TestAskLaunchesTheCandidateWithTheRoleDefinition`: default fixture,
    `Role: "reviewer"`; want `f.starts[0].Kind == "claude"` and
    `f.starts[0].Args` `DeepEqual` `[]string{"--model", "m", "--agent",
    "reviewer"}`; and the recorded consult's `Role == "reviewer"`.
  - `TestAskPrependsThePreambleForAPreambleHarness`: `rt.Candidates =
    candidateSet(t, `[{"harness":"agy","provider":"t","model":"m","roles":["reviewer"]}]`)`,
    `Role: "reviewer"`; want `f.prompts[0].Text` to start with
    `harness.RoleByName("reviewer").Preamble`.

  Run: `go test ./internal/relay/ -run Ask`. Expected: compile error
  (`AskOptions.Candidate` undefined) or failures.

- [ ] **Step 3: `Ask`** (spec §4.8)

  `ask.go`: add `Candidate string` to `AskOptions` (comment: a
  `harness/provider/model` token; empty resolves through the one rule in
  `resolveCandidate`). `ErrNotAConsultRole` text → `that role is the
  builder role; bind it with relay bind, not relay ask`. Replace the
  alias lookup block with, in this order:

  ```
  role, ok := harness.RoleByName(opts.Role)
  if !ok: return fmt.Errorf("unknown role %q (known: %v): %w", opts.Role, harness.RoleNames(), ErrUnknownRole)
  if role.Shape != harness.ShapeConsult: return fmt.Errorf("%q: %w", opts.Role, ErrNotAConsultRole)
  c, err := resolveCandidate(rt.Candidates, opts.Candidate, opts.Role); if err: return err
  if c.Tree == "none": return fmt.Errorf("candidate %q declares tree \"none\": %w", c.Ref().String(), ErrTreelessUnsupported)
  h, _ := harness.Lookup(c.Harness)
  l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)
  ```

  Then `agentName := opts.Name + "-" + role.Name + "-" + id` (the
  validation message uses `role.Name` where it used `spec.Name`). In phase
  2, `StartAgent(ctx, c.Endpoint.AgentName, l.Kind, pane, l.Args)` and
  `text := l.Preamble` in place of `spec.Preamble`. The consult record's
  `Role` is `role.Name`. Update the `Errors:` line in `Ask`'s doc comment:
  `ErrUnknownRole, ErrNotAConsultRole, ErrNoCandidates, ErrRoleNotServed,
  ErrAmbiguousCandidate, candidate.ErrUnknownCandidate,
  ErrTreelessUnsupported, ErrConsultCap, store.ErrNotFound, a wrapped
  herdr failure`.

  `internal/store/types.go` `Consult.Role` comment: "Role is the role-table
  name that was asked (`reviewer`), recorded as a name rather than the
  candidate that ran it: the candidate is the planner's choice at the
  time, the role is what was intended."

  Delete `consultTable` from `fake_test.go` and its `alias`, `os`,
  `filepath` imports if now unused.

  Run: `go test ./internal/relay/`. Expected: green.
  Run: `grep -n "rt.Aliases\|\.Aliases\." internal/relay/*.go`. Expected:
  no hits outside the `Runtime` struct definition.

- [ ] **Step 4: `cmdAsk --candidate`** (spec §4.13)

  `main.go` `cmdAsk`: add `cand := fs.String("candidate", "", "candidate
  harness/provider/model; omit when exactly one serves the role")`; the
  `--role` help becomes `consult role: reviewer, researcher`; pass
  `Candidate: *cand`. No test (would reach herdr).

  Run: `go build ./...`. Expected: clean.

- [ ] **Step 5: `make check`, commit**

  ```bash
  git add -A internal cmd
  git commit -m "feat(relay): ask resolves a role through the role table and a candidate (#80)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, the output of
`grep -rn "Aliases" internal/relay/*.go`, and the list of test functions
added or renamed.
