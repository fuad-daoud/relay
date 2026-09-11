# Candidates T1: the role table and launch rendering in `internal/harness` (#80)

**Design spec:** `docs/specs/2026-09-11-candidates-design.md` -- read §3.4, §3.5
**Issue:** #80

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
- This task touches **only** `internal/harness/harness.go` and
  `internal/harness/harness_test.go`. No caller changes. `internal/alias`
  stays exactly as it is.
- Every new exported symbol gets a doc comment explaining the rationale, in
  the house style (read the existing comments in `harness.go` first).
- The `builder` preamble text is today's `abuilder` preamble from
  `internal/alias/alias.go` **verbatim, byte for byte**. Copy it; do not
  retype it.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: role table, `CanServe`, `Launch`

**Files:**
- Modify: `internal/harness/harness.go`
- Modify: `internal/harness/harness_test.go`

**Interfaces produced** (spec §3.4, §3.5) -- later tasks import these by
exactly these names:

```go
type RoleShape string
const (
    ShapeBuilder RoleShape = "builder"
    ShapeConsult RoleShape = "consult"
)

type RoleSpec struct {
    Name       string
    Shape      RoleShape
    Definition string
    Preamble   string
}

func RoleByName(name string) (RoleSpec, bool)
func RoleNames() []string

type Launch struct {
    Kind     string
    Args     []string
    Preamble string
}

// on Harness:
SelectsRoleByPreamble bool   // new field
func (h Harness) CanServe(role string) bool
func (h Harness) Launch(provider, model string, extra []string, role RoleSpec) Launch
```

- [ ] **Step 1: the role table** (spec §3.4)

  In `harness.go`, add `RoleShape`, the two constants, `RoleSpec`, and an
  unexported package-level slice `roleTable []RoleSpec` in this order:

  | Name | Shape | Definition | Preamble |
  |---|---|---|---|
  | `builder` | `ShapeBuilder` | `plan-executor` | the `abuilder` preamble from `internal/alias/alias.go`, verbatim |
  | `reviewer` | `ShapeConsult` | `reviewer` | `Activate your 'reviewer' skill and act exactly as it specifies.` |
  | `researcher` | `ShapeConsult` | `researcher` | `Activate your 'researcher' skill and act exactly as it specifies.` |

  Add `RoleByName(name string) (RoleSpec, bool)` (linear scan; ok=false for
  an unknown name, which is not an error -- the caller decides) and
  `RoleNames() []string` returning the names in table order.

  Doc comment for `roleTable`: a role is relay's name for a job
  (`builder`, `reviewer`), with a shape relay's loop depends on and the
  harness agent definition that implements it. The candidate that runs it
  is a separate choice (#80).

  Test (`harness_test.go`):
  - `TestRoleTable`: `RoleNames()` equals `[]string{"builder", "reviewer",
    "researcher"}` (use `reflect.DeepEqual`); `RoleByName("builder")` has
    `Shape == ShapeBuilder` and `Definition == "plan-executor"`;
    `RoleByName("reviewer")` and `("researcher")` have `Shape ==
    ShapeConsult`; every entry has a non-empty `Preamble`;
    `RoleByName("nope")` returns ok=false.

  Verify: `go test ./internal/harness/ -run TestRoleTable` green.

- [ ] **Step 2: `SelectsRoleByPreamble` and `CanServe`** (spec §3.5)

  Add the field `SelectsRoleByPreamble bool` to `Harness` with the comment
  from the spec (true for a kind with no `--agent` flag; its `Roles` is nil
  and every role in the table is servable). Set it to `true` on the `agy`
  entry in `knownHarnesses` and leave the other two unset.

  Add:

  ```go
  func (h Harness) CanServe(role string) bool
  ```

  Returns false when `RoleByName(role)` is not ok. Otherwise true when
  `h.SelectsRoleByPreamble`, or when `h.Role(spec.Definition)` is found.

  Test: `TestCanServe` -- a table over every (kind, role) pair:

  | kind | builder | reviewer | researcher | `"nope"` |
  |---|---|---|---|---|
  | agy | true | true | true | false |
  | claude | true | true | true | false |
  | opencode | true | true | true | false |

  Also assert `Lookup("agy").SelectsRoleByPreamble == true` and the other two
  false. `TestHarnessRules` keeps passing because it compares `Lookup` to
  `All` -- both carry the new field.

  Verify: `go test ./internal/harness/ -run 'TestCanServe|TestHarnessRules'`
  green.

- [ ] **Step 3: `Launch`** (spec §3.5)

  Add the `Launch` struct and:

  ```go
  func (h Harness) Launch(provider, model string, extra []string, role RoleSpec) Launch
  ```

  Rendering by `h.Kind`, then `extra` appended in order:

  | kind | Args | Preamble |
  |---|---|---|
  | `claude` | `--model <model> --agent <role.Definition>` | `""` |
  | `opencode` | `--agent <role.Definition> -m <provider>/<model>` | `""` |
  | `agy` | `--model <model>` | `role.Preamble` |

  `Kind` is always `h.Kind`. For a kind not in that table (cannot happen for
  a `Harness` from `Lookup`, but the type is constructible), return
  `Args: extra` and `Preamble: ""` -- no panic. Never alias the `extra`
  slice: build a fresh one with `append([]string(nil), ...)` so the caller's
  `ExtraArgs` is never mutated.

  Doc comment: relay renders the argv because `model` and the role are now
  fields (spec §1 point 2); with a verbatim args list in config, the model
  would be a label relay could not check against what it launched.

  Test: `TestLaunch` -- table with `provider="prov"`, `model="m/x"`
  (a model containing a slash, on purpose), role `RoleByName("builder")`:

  | kind | extra | want Args | want Preamble |
  |---|---|---|---|
  | claude | nil | `[--model m/x --agent plan-executor]` | `""` |
  | opencode | nil | `[--agent plan-executor -m prov/m/x]` | `""` |
  | agy | nil | `[--model m/x]` | the builder preamble |
  | agy | `[--dangerously-skip-permissions]` | `[--model m/x --dangerously-skip-permissions]` | the builder preamble |
  | claude | `[--auto]` | `[--model m/x --agent plan-executor --auto]` | `""` |

  Plus one case with `RoleByName("reviewer")` on opencode: Args
  `[--agent reviewer -m prov/m/x]`. Compare `Args` with
  `reflect.DeepEqual`; compare `Preamble` to `RoleByName(...).Preamble`,
  not to a retyped string. Add a final assertion that passing
  `extra := []string{"--a"}` and then appending to the returned `Args` does
  not change `extra`.

  Verify: `go test ./internal/harness/` green.

- [ ] **Step 4: `make check`, commit**

  Run `make check`. Then:

  ```bash
  git add internal/harness/harness.go internal/harness/harness_test.go
  git commit -m "feat(harness): role table, CanServe and Launch rendering (#80)"
  ```

## Report

Write the report relay asked for. Include: the `go test ./internal/harness/
-v` summary line, the `make check` result, and `git diff --stat HEAD~1`,
which must list exactly two files.
