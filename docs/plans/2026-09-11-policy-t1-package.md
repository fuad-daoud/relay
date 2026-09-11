# Policy T1: `internal/policy` package and `Runtime.Policy` (#61 step 2)

**Design spec:** `docs/specs/2026-09-11-policy-order-design.md` -- read §1, §3.1, §3.4, §4.1, §6
**Issue:** #61 (step 2)
**Depends on:** nothing new. `internal/candidate` and `internal/harness` are already in this tree.

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
- **`Load` validates only the file's own shape** (spec §3.1). It must not
  open `candidates.json`, must not import `internal/relay`, and must not
  reject a token merely because no candidate has it. Cross-file checks are
  T4's `PolicyWarnings`.
- **A missing file is the zero `Policy` and no error.** Every machine
  without a `policy.json` must behave exactly as today after this task.
- **No caller reads `Runtime.Policy` in this task.** T2 wires it into the
  resolver. Here it is loaded and carried, nothing more.
- No test may execute a `cmd/relay` subcommand that reaches herdr. Nothing
  here needs to.
- Every new exported symbol gets a doc comment in the house style (read
  `internal/candidate/candidate.go` for the voice: what it is, then why).
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: the `policy` package

**Files:**
- Create: `internal/policy/policy.go`, `internal/policy/policy_test.go`
- Modify: `internal/relay/herdr.go` (`Runtime`)
- Modify: `cmd/relay/main.go` (`newRuntime`)

**Interfaces consumed:** `candidate.ParseRef` (token shape),
`harness.RoleByName`, `harness.RoleNames` (the closed role table),
`userConfigRoot()` in `cmd/relay/main.go` (compose the path through it;
never by hand -- see CLAUDE.md).

**Interfaces produced** (spec §3.1, §3.4, §4.1):

```go
// internal/policy/policy.go
var ErrBadPolicy = errors.New("bad policy")

type Policy struct {
    Order map[string][]string `json:"order,omitempty"`
}

func Load(path string) (Policy, error)
func (p Policy) OrderFor(role string) []string

// internal/relay/herdr.go, Runtime
Policy policy.Policy
```

- [ ] **Step 1: the failing tests** (spec §3.1, §4.1, §6)

  Create `internal/policy/policy_test.go`. A helper writes a body to a temp
  file and calls `Load`:

  ```go
  func load(t *testing.T, body string) (Policy, error) {
      t.Helper()
      path := filepath.Join(t.TempDir(), "policy.json")
      if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
          t.Fatal(err)
      }
      return Load(path)
  }
  ```

  Tests:

  - `TestLoadMissingFileIsZeroPolicy`: `Load(filepath.Join(t.TempDir(),
    "absent.json"))` → nil error, `len(p.Order) == 0`, and
    `p.OrderFor("builder") == nil`.
  - `TestLoadValidPolicy`: body
    ```json
    {"order":{"builder":["agy/google/m","claude/anthropic/sonnet"],"reviewer":["claude/anthropic/opus"]}}
    ```
    → nil error; `p.OrderFor("builder")` equals the two tokens in that
    order; `p.OrderFor("reviewer")` equals the one; `p.OrderFor("researcher")`
    is nil.
  - `TestOrderReturnsACopy`: load the valid body, `got :=
    p.OrderFor("builder")`, `got[0] = "x"`, then `p.OrderFor("builder")[0]` is
    still `"agy/google/m"`.
  - `TestLoadErrors`: a table, each row `{name, body, contains []string}`;
    every row wants `errors.Is(err, ErrBadPolicy)` and each `contains`
    substring in `err.Error()`:

    | name | body | contains |
    |---|---|---|
    | bad json | `{` | `policy.json` |
    | unknown top-level key | `{"orders":{}}` | `orders` |
    | unknown role | `{"order":{"reviwer":["a/b/c"]}}` | `order.reviwer`, `unknown role`, `builder reviewer researcher` |
    | null list | `{"order":{"builder":null}}` | `order.builder`, `must be an array` |
    | bad token | `{"order":{"builder":["claude/sonnet"]}}` | `order.builder[0]`, `harness/provider/model` |
    | empty token | `{"order":{"builder":[""]}}` | `order.builder[0]` |
    | duplicate | `{"order":{"builder":["a/b/c","x/y/z","a/b/c"]}}` | `order.builder[2]`, `duplicate`, `a/b/c` |

    The `bad token` row's `harness/provider/model` substring comes from
    `candidate.ErrBadRef`'s wrapped message -- wrap the `ParseRef` error
    with `%v`, do not restate it.
  - `TestLoadEmptyOrderIsValid`: `{"order":{}}` and `{}` both load with no
    error and `OrderFor("builder") == nil`.

  Run: `go test ./internal/policy/`. Expected: FAIL to compile (package
  does not exist).

- [ ] **Step 2: the package** (spec §3.1, §4.1)

  Create `internal/policy/policy.go`, package `policy`. Package comment:
  `policy.json` is where the planner tells relay how to choose among
  candidates; this step carries `order[role]` only, later #61 steps add
  the scoring knobs (spec §1 "Why this is not #61's step 2 as written").

  `Policy` as in the interface block. Doc comment on `Order` (the field):
  role → candidate tokens, most preferred first; a role absent here is
  unordered and the resolver refuses when several candidates serve it.

  `Load(path string) (Policy, error)`:
  - `os.ReadFile`; `errors.Is(err, os.ErrNotExist)` → `Policy{}, nil`;
    other read error → `fmt.Errorf("read %s: %w", path, err)`.
  - Decode with `json.NewDecoder(bytes.NewReader(raw))` and
    `DisallowUnknownFields()` into a `Policy`; a decode error →
    `fmt.Errorf("%s: %v: %w", path, err, ErrBadPolicy)`. (Unknown keys are
    errors on purpose: a typo'd `"orders"` silently ordering nothing is
    the failure this file exists to prevent.)
  - Validate roles in **sorted** key order so an error message is
    deterministic when two roles are bad:
    - `harness.RoleByName(role)` not ok → `%s: order.%s: unknown role
      (known: %v): %w` with `harness.RoleNames()`.
    - list is nil (JSON `null`) → `%s: order.%s: must be an array: %w`.
    - for each `i, tok`: `candidate.ParseRef(tok)` error → `%s:
      order.%s[%d]: %v: %w`; seen before → `%s: order.%s[%d]: duplicate
      token %q: %w`.
  - Return the decoded `Policy`.

  `OrderFor(role)`: `nil` when absent; otherwise `append([]string(nil),
  p.Order[role]...)`. Doc comment says it copies so a caller cannot
  reorder the loaded policy by accident.

  Run: `go test ./internal/policy/`. Expected: green.

- [ ] **Step 3: `Runtime.Policy` and `newRuntime`** (spec §3.4)

  `internal/relay/herdr.go`: add to `Runtime`, directly below
  `LedgerPath`:

  ```go
  // Policy is ~/.config/relay/policy.json: the planner's candidate order
  // per role (#61 step 2). The zero value means nothing is ordered, so
  // tests that do not set it behave as a machine with no policy file.
  Policy policy.Policy
  ```

  `cmd/relay/main.go` `newRuntime`: after `candidate.Load(...)`:

  ```go
  pol, err := policy.Load(filepath.Join(configDir, "relay", "policy.json"))
  if err != nil {
      return relay.Runtime{}, err
  }
  ```

  and `Policy: pol,` in the returned literal, below `LedgerPath`. Update
  the comment above `newRuntime` if it enumerates what is loaded.

  Run: `go build ./... && go vet ./... && go test -count=1 ./...`.
  Expected: green; no existing test changes, because the zero value is
  the old behaviour.

- [ ] **Step 4: `make check`, commit**

  ```bash
  git add -A internal cmd
  git commit -m "feat(policy): load ~/.config/relay/policy.json order[role]; Runtime.Policy (#61 step 2)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, the list of
test functions added, and the exact error text `Load` produces for the
`unknown role` and `duplicate` rows (copy it from a `t.Log` or a quick
`go run`), so the planner can check the wording against spec §6.
