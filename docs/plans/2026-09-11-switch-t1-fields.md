# Switch T1: `max_switches`, `RoundSwitches`, `BuilderMissingSince`, `KindSwitch` (#61 step 6)

**Design spec:** `docs/specs/2026-09-11-builder-switching-design.md` -- read §1, §3.1–3.3, §4.5
**Issue:** #61 (step 6)
**Depends on:** #61 step 2 (`internal/policy`, landed in #88) -- already in this tree.

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
- **`max_switches` absent and `max_switches: 0` are different states**
  (spec §3.1): absent is the default of 2, `0` turns switching off. That
  is why the field is a pointer. Do not collapse them.
- **No trigger, no switch, no status change in this task.** T2 reads these
  fields; here they are declared, validated, persisted and reset.
- No test may execute a `cmd/relay` subcommand that reaches herdr.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: the fields

**Files:**
- Modify: `internal/policy/policy.go`, `internal/policy/policy_test.go`
- Modify: `internal/store/types.go`, `internal/store/log.go`
- Modify: `internal/store/types_test.go` (round-trip)
- Modify: `internal/relay/reconcile.go` (`queueReport`), one existing test in `internal/relay/reconcile_test.go`

**Interfaces consumed:** `policy.Load` / `policy.ErrBadPolicy` (step 2);
`store.Binding`; `store.Kind`; `queueReport` in `reconcile.go` (~line 448).

**Interfaces produced** (spec §3.1–3.3, §4.5):

```go
// internal/policy/policy.go
const DefaultMaxSwitches = 2
// Policy gains:
MaxSwitches *int `json:"max_switches,omitempty"`
func (p Policy) SwitchLimit() int

// internal/store/types.go, Binding gains:
RoundSwitches       int       `json:"round_switches,omitempty"`
BuilderMissingSince time.Time `json:"builder_missing_since,omitempty"`

// internal/store/log.go
KindSwitch Kind = "switch"
```

- [ ] **Step 1: failing policy tests** (spec §3.1)

  In `internal/policy/policy_test.go`, add `TestSwitchLimit`, a table over
  bodies loaded through the existing `load` helper:

  | body | want `SwitchLimit()` | want err |
  |---|---|---|
  | `{}` | 2 | nil |
  | `{"max_switches":0}` | 0 | nil |
  | `{"max_switches":5}` | 5 | nil |
  | `{"max_switches":-1}` | -- | `ErrBadPolicy`, message contains `max_switches` and `must be >= 0` |
  | `{"max_switches":"two"}` | -- | `ErrBadPolicy` (the decoder's error, wrapped as today) |

  Also assert `Policy{}.SwitchLimit() == DefaultMaxSwitches` directly.

  Run: `go test ./internal/policy/ -run SwitchLimit`. Expected: FAIL to
  compile.

- [ ] **Step 2: `MaxSwitches`, `SwitchLimit`, validation** (spec §3.1)

  Add the field below `Order` with the comment: how many times the daemon
  may replace a builder within one round before the binding goes NEEDS
  YOU; nil is `DefaultMaxSwitches`; `0` disables switching. Add
  `DefaultMaxSwitches = 2` with a one-line comment (two replacements cover
  "the first pick was gated and the second failed to spawn"; a third in
  one round is a pattern a human should see).

  ```go
  // SwitchLimit is MaxSwitches with the default applied.
  func (p Policy) SwitchLimit() int {
      if p.MaxSwitches == nil {
          return DefaultMaxSwitches
      }
      return *p.MaxSwitches
  }
  ```

  In `Load`, after the decode and before the role loop:
  `if p.MaxSwitches != nil && *p.MaxSwitches < 0 { return Policy{},
  fmt.Errorf("%s: max_switches: must be >= 0, got %d: %w", path,
  *p.MaxSwitches, ErrBadPolicy) }`.

  Run: `go test ./internal/policy/`. Expected: green.

- [ ] **Step 3: binding fields and `KindSwitch`** (spec §3.2, §3.3)

  `internal/store/types.go`, on `Binding`, directly after
  `HaltNotifiedRound`:

  ```go
  // RoundSwitches counts builder switches in the current round (#61 step
  // 6). Reset when the round advances. Compared against
  // policy.Policy.SwitchLimit().
  RoundSwitches int `json:"round_switches,omitempty"`

  // BuilderMissingSince is when the daemon first failed to locate the
  // builder during the current absence; zero while it is located. Stamped
  // on the first miss and cleared on any hit, so a detection flicker never
  // accumulates toward a switch.
  BuilderMissingSince time.Time `json:"builder_missing_since,omitempty"`
  ```

  `internal/store/log.go`, after `KindPick`:

  ```go
  KindSwitch Kind = "switch" // relay -> log only: the builder was replaced mid-round, and why (#61 step 6)
  ```

  Test in `internal/store/types_test.go`: find the existing bind.json
  round-trip test (grep `json.Marshal` or `Save`/`Load` on a `Binding`)
  and add a case, or a new `TestBindingSwitchFieldsRoundTrip`: a
  `Binding` with `RoundSwitches: 2` and `BuilderMissingSince: <fixed
  UTC time>` saved and loaded through the store keeps both; a `Binding`
  with both zero marshals to JSON that contains neither key
  (`omitempty`). Use the store's own `Save`/`Load` on a `t.TempDir()`
  root, as the neighbouring tests do.

  Run: `go build ./... && go test ./internal/store/`. Expected: green.

- [ ] **Step 4: `queueReport` resets the counter** (spec §4.5)

  `internal/relay/reconcile.go`, in `queueReport`, next to
  `b.HaltNotifiedRound = 0`:

  ```go
  // A switch counted against the old round says nothing about the new one.
  b.RoundSwitches = 0
  ```

  Test: in `internal/relay/reconcile_test.go` find a test that drives a
  report through `queueReport` (a builder goes idle with a report file
  present and the round advances -- grep `Round != 2` or `KindReport`).
  In that test, before the tick, set the seeded binding's `RoundSwitches
  = 1` via `rt.Store.Save`; after the tick assert `RoundSwitches == 0`.
  If no existing test fits cleanly, add `TestRoundAdvanceResetsSwitches`
  by copying the closest one's setup.

  Run: `go test -count=1 ./...`. Expected: green.

- [ ] **Step 5: `make check`, commit**

  ```bash
  git add -A internal
  git commit -m "feat: policy max_switches; Binding.RoundSwitches/BuilderMissingSince; KindSwitch (#61 step 6)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, the test
functions added or extended, and which existing reconcile test carried the
`RoundSwitches` reset assertion.
