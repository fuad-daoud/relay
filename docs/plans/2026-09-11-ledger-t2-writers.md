# Ledger T2: `Runtime.LedgerPath`, spawn-failure appends, `Unavailable`, `Available` (#61 step 1)

**Design spec:** `docs/specs/2026-09-11-availability-ledger-design.md` -- read §3.6, §3.7, §4.1–4.3, §5.1, §5.4, §6
**Issue:** #61 (step 1)
**Depends on:** T1 (`internal/ledger`, `store.LedgerPath`) -- already in this tree.

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
- **`recordSpawnFailure` never changes what its caller returns.** The
  spawn error the caller was about to return is returned unchanged; a
  ledger write failure is printed to stderr and dropped. Every test in
  step 3 asserts the original error is still there.
- **Only a `StartAgent` error is a spawn failure.** A pane split failure
  (`builderPane` / `consultPane`) writes nothing. Step 3 tests that.
- **No test may execute a `cmd/relay` subcommand that reaches herdr.**
  Everything here is in `internal/relay` against `fakeHerdr`.
- No renderer or CLI change in this task (T3, T4). `resolveCandidate` does
  not read the ledger (spec §1 scope).
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: the writers

**Files:**
- Modify: `internal/relay/herdr.go` (Runtime)
- Create: `internal/relay/ledger.go`, `internal/relay/ledger_test.go`
- Modify: `internal/relay/bind.go` (resolveBuilder), `internal/relay/ask.go` (AskResult, phase 2)
- Modify: `internal/relay/bind_test.go`, `internal/relay/fork_test.go` (test runtimes), `internal/relay/ask_test.go`
- Modify: `cmd/relay/main.go` (`newRuntime` sets `LedgerPath`)

**Interfaces consumed** (T1): `ledger.Load`, `ledger.Save`, `Ledger.Prune`,
`Ledger.Append`, `Ledger.Clear`, `ledger.Entry`, `ledger.SpawnFailed`,
`ledger.RateLimited`; `store.LedgerPath()`; `store.WithLock`;
`candidate.ParseRef`, `Set.Lookup`, `candidate.ErrUnknownCandidate`;
`rt.Now`.

**Interfaces produced** (spec §3.6, §3.7, §4.1–4.3):

```go
// Runtime
LedgerPath string

// AskResult
Candidate string

// internal/relay/ledger.go
const SpawnFailedCooldown = 10 * time.Minute
func recordSpawnFailure(rt Runtime, token, binding string, cause error)   // unexported
func Unavailable(rt Runtime, token string, until time.Time, reason string) (provider string, err error)
func Available(rt Runtime, subject string) (provider string, removed int, err error)
```

- [ ] **Step 1: `Runtime.LedgerPath`; `AskResult.Candidate`** (spec §3.6, §3.7)

  `herdr.go`: add `LedgerPath string` to `Runtime` below `Candidates`,
  comment: the availability ledger file (#61 step 1); `newRuntime` sets
  it from `Store.LedgerPath()`; tests set a temp path.

  `main.go` `newRuntime`: `LedgerPath: store.New(root).LedgerPath()` --
  simplest is to build the store once into a local and use it for both
  `Store:` and `LedgerPath:`.

  Test runtimes: in `bind_test.go` `newRuntime` and `fork_test.go`
  `newForkRuntime`, add `LedgerPath: filepath.Join(t.TempDir(),
  "ledger.json"),`. Any other `Runtime{` literal in `internal/relay/*_test.go`
  (`grep -n "Runtime{" internal/relay/*_test.go`) gets the same line.

  `ask.go`: add `Candidate string` to `AskResult` with the spec's comment,
  and set it (`c.Ref().String()`) in all three `AskResult{...}` return
  literals at the end of `Ask` -- the two error returns included, since
  the CLI may still want it.

  Run: `go build ./... && go vet ./...`. Expected: clean.

- [ ] **Step 2: the three writers** (spec §4.1–4.3, §5.4)

  Create `internal/relay/ledger.go`:

  ```go
  // SpawnFailedCooldown is how long a spawn failure gates its candidate. A
  // constant, not config: a failed StartAgent is nearly always a pane race
  // or a binary mid-upgrade, and ten minutes outlasts both. #61 step 2 may
  // move it to policy.json with the other cooldowns.
  const SpawnFailedCooldown = 10 * time.Minute
  ```

  A private helper both writers use:

  ```go
  // mutateLedger loads, prunes, applies fn and saves the ledger under the
  // state lock, so a bind recording a failure and a planner running
  // `relay unavailable` in another pane serialise on the flock that already
  // serialises bind.json (spec §3.3).
  func mutateLedger(rt Runtime, fn func(ledger.Ledger) ledger.Ledger) error
  ```

  Body: `rt.Store.WithLock(func(*store.Tx) error { l, err := ledger.Load(rt.LedgerPath); if err → return; l = fn(l.Prune(rt.Now())); return ledger.Save(rt.LedgerPath, l) })`.

  `recordSpawnFailure(rt, token, binding, cause)`: `mutateLedger` appending
  `ledger.Entry{Kind: SpawnFailed, Subject: token, At: now, Until:
  now.Add(SpawnFailedCooldown), Note: cause.Error(), Source: "relay",
  Binding: binding}`. On error: `fmt.Fprintf(os.Stderr, "relay: could not
  record spawn failure: %v\n", err)` and return. Doc comment quotes spec
  §4.1's rule: never returns an error, because a failed bookkeeping write
  must not mask the spawn error the caller is about to return.

  `Unavailable(rt, token, until, reason)`: `ref, err := candidate.ParseRef(token)`
  → return err; `rt.Candidates.Lookup(ref)` → return err (so a typo is
  refused, not recorded); then `mutateLedger` appending `{RateLimited,
  ref.Provider, now, until, reason, "planner", ""}`; return `ref.Provider`.

  `Available(rt, subject)`: `provider := subject`; if `candidate.ParseRef(subject)`
  succeeds, `provider = ref.Provider`. Then `mutateLedger` with a closure
  that counts entries matching `{RateLimited, provider}` before calling
  `Clear` and stores the count in a captured variable. Return `provider,
  removed, err`. Zero removed is not an error.

  Run: `go build ./...`. Expected: clean.

- [ ] **Step 3: the failing tests, then wire the spawn paths** (spec §4.1, §5.1)

  Tests in `internal/relay/ledger_test.go`. A helper:

  ```go
  // loadLedger reads the runtime's ledger for assertions.
  func loadLedger(t *testing.T, rt Runtime) ledger.Ledger
  ```

  - `TestBindRecordsASpawnFailure`: `fakeHerdr{agents: [plannerAgent()],
    newPane: "w2:p4", startErr: errors.New("agent start: exit 1")}`,
    `Bind` with `Candidate: testAgyRef`; want the error to wrap the start
    error (`strings.Contains(err.Error(), "agent start: exit 1")`), and
    the ledger to hold exactly one entry: `Kind == SpawnFailed`, `Subject
    == "agy/test/m"`, `Binding == "webshop"`, `Source == "relay"`, `Until
    == baseTime.Add(SpawnFailedCooldown)`, `Note` contains `agent start`.
  - `TestAddRecordsASpawnFailure` and `TestForkRecordsASpawnFailure`: same
    shape through `Add` / `Fork` (use the existing add/fork test scaffolding
    -- `newForkRuntime`, `addRepo`, `seedFourRoundBinding`); one entry with
    `Binding` equal to the new binding's name.
  - `TestAskRecordsASpawnFailure`: `Role: "reviewer"` on the default
    fixture with `startErr`; one entry, `Subject == "claude/test/m"`,
    `Binding == "webshop"`, and the consult is still recorded
    `ConsultSilent` with `Note` starting `start failed:` (unchanged
    behaviour).
  - `TestSplitFailureIsNotASpawnFailure`: make the split fail (read
    `fakeHerdr` for how -- `newPane == ""` makes `SplitPane` error, per
    line ~250) and `Bind`; want an error and **zero** ledger entries.
  - `TestSpawnFailureDoesNotMaskTheError`: set `rt.LedgerPath` to a path
    under a **file** (e.g. `filepath.Join(t.TempDir(), "blocker",
    "ledger.json")` after creating `blocker` as a regular file) so `Save`
    fails; `Bind` with `startErr`; want the returned error to still be
    the start error, not a ledger error.

  Run: `go test ./internal/relay/ -run 'RecordsASpawnFailure|SplitFailure|DoesNotMask'`.
  Expected: FAIL (no entries written yet).

  Wire:
  - `bind.go` `resolveBuilder`, the `StartAgent` error branch: insert
    `recordSpawnFailure(rt, c.Ref().String(), name, err)` before the
    `return`. (`name` is the binding name parameter already in scope.)
  - `ask.go` phase 2, the `StartAgent` error branch: insert
    `recordSpawnFailure(rt, c.Ref().String(), opts.Name, err)` before
    `consult.State = store.ConsultSilent`.

  Run the same tests. Expected: green. Then the whole package.

- [ ] **Step 4: `Unavailable` / `Available` tests** (spec §4.2, §4.3)

  In `ledger_test.go`:
  - `TestUnavailableRecordsTheProvider`: `Unavailable(rt, testClaudeRef,
    time.Time{}, "5h window")` → provider `"test"`, one entry `{RateLimited,
    "test", Until zero, Note "5h window", Source "planner", Binding ""}`.
  - `TestUnavailableWithUntil`: `until := baseTime.Add(2*time.Hour)` is
    stored verbatim.
  - `TestUnavailableRefusesAnUnknownToken`: `"claude/test/nope"` →
    `errors.Is(err, candidate.ErrUnknownCandidate)` and zero entries;
    `"claude/test"` → `candidate.ErrBadRef`.
  - `TestAvailableByTokenAndByProvider`: after two `Unavailable` calls on
    `testClaudeRef`, `Available(rt, "test")` → provider `"test"`, removed
    2, ledger empty; then `Available(rt, testClaudeRef)` → removed 0, no
    error.
  - `TestAvailableLeavesSpawnFailures`: one `recordSpawnFailure` and one
    `Unavailable` on the same provider; `Available` removes 1 and the
    spawn failure remains.
  - `TestMutateLedgerPrunes`: write a ledger file by hand with one entry
    whose `Until` is before `baseTime`; call `Unavailable`; the file
    afterwards holds only the new entry.

  Run: `go test ./internal/relay/`. Expected: green.

- [ ] **Step 5: `make check`, commit**

  ```bash
  git add -A internal cmd
  git commit -m "feat(relay): record spawn failures; relay unavailable/available write the ledger (#61)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, and the list of
test functions added.
