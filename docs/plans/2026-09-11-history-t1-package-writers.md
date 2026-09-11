# History T1: `internal/history`, `HistoryPath`, ledger writers mirror into history (#61 step 7)

**Design spec:** `docs/specs/2026-09-11-availability-history-design.md` -- read §1, §3, §4.1, §4.3, §6
**Issue:** #61 (step 7, observation half)
**Depends on:** #61 steps 1, 2 and 6 -- all in this tree (`grep -n recordSpawnFailureLocked internal/relay/ledger.go` must hit).

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
- **A history failure never fails a ledger write** (spec §4.1). The
  ledger gates; history is bookkeeping. Stderr line, then carry on. A
  test pins it.
- **`Available` writes no history.** A clear is not an observation.
- **Nothing reads history in this task.** T2 renders it.
- **Lock discipline is unchanged**: `appendEntryLocked` assumes the store
  lock is held; `Unavailable` and the unlocked spawn-failure path wrap it
  in `WithLock`; the locked path (daemon switch) does not. Do not call
  `WithLock` from inside `appendEntryLocked`.
- No test may execute a `cmd/relay` subcommand that reaches herdr.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: the package and the writers

**Files:**
- Create: `internal/history/history.go`, `internal/history/history_test.go`
- Modify: `internal/store/store.go` (`HistoryPath`)
- Modify: `internal/relay/herdr.go` (`Runtime.HistoryPath`), `cmd/relay/main.go` (`newRuntime`)
- Modify: `internal/relay/ledger.go`, `internal/relay/ledger_test.go`
- Modify: every `Runtime{` literal in `internal/relay/*_test.go` that sets `LedgerPath` gets `HistoryPath` too (`grep -n "LedgerPath:" internal/relay/*_test.go`)

**Interfaces consumed:** `ledger.Entry`, `ledger.Kind`, `ledger.SpawnFailed`,
`ledger.RateLimited`, `ledger.Load/Save/Prune/Append` (for the existing
half of `appendEntryLocked`), `candidate.ParseRef`, `store.WithLock`,
`rt.Now`.

**Interfaces produced** (spec §3, §4.1, §4.3):

```go
// internal/history/history.go
const RetainWindow = 30 * 24 * time.Hour
type Event struct { At time.Time; Kind ledger.Kind; Provider, Token, Source, Binding, Note string }   // json tags per spec §3.1
type History struct { Events []Event `json:"events"` }
func Load(path string) (History, error)
func Save(path string, h History) error
func (h History) Prune(now time.Time) History
func (h History) Append(e Event) History
func FromEntry(e ledger.Entry, providerOf func(string) string) Event
func HourCounts(h History, provider string, kind ledger.Kind, loc *time.Location) [24]int

// internal/store/store.go
func (s *Store) HistoryPath() string

// internal/relay/herdr.go
HistoryPath string

// internal/relay/ledger.go
func appendEntryLocked(rt Runtime, e ledger.Entry) error
func recordSpawnFailureWith(rt Runtime, locked bool, token, binding string, cause error)   // replaces the mutate-func form
```

- [ ] **Step 1: failing `history` tests** (spec §3.1–3.2)

  Create `internal/history/history_test.go`. Fixed clock `now :=
  time.Date(2026, 9, 11, 21, 15, 0, 0, time.UTC)`.

  - `TestRoundTrip`: `Save` two events to a temp path, `Load` → equal
    (compare `At.Equal` and the string fields). `Load` on a missing path →
    empty, nil error.
  - `TestPruneWindow`: events at `now.Add(-RetainWindow)` (kept) and
    `now.Add(-RetainWindow - time.Second)` (dropped); `Prune(now)` leaves
    exactly the first.
  - `TestFromEntry`: `ledger.Entry{Kind: RateLimited, Subject: "anthropic",
    At: now, Source: "planner", Note: "5h"}` → `{Provider: "anthropic",
    Token: "", Kind: RateLimited, Source: "planner", Note: "5h", At: now}`;
    `ledger.Entry{Kind: SpawnFailed, Subject: "claude/anthropic/sonnet",
    Binding: "webshop", Source: "relay"}` with `providerOf` returning
    `"anthropic"` → `{Provider: "anthropic", Token:
    "claude/anthropic/sonnet", Binding: "webshop"}`.
  - `TestHourCounts`: events for provider `test` kind `RateLimited` at
    21:30 and 21:59 UTC, one at 22:00, one for provider `other` at 21:10,
    one `SpawnFailed` for `test` at 21:20; `HourCounts(h, "test",
    RateLimited, time.UTC)` → `[21] == 2`, `[22] == 1`, every other cell
    0; with `loc = time.FixedZone("plus1", 3600)` → `[22] == 2`, `[23] == 1`.

  Run: `go test ./internal/history/`. Expected: FAIL to compile.

- [ ] **Step 2: the package** (spec §3.1–3.2)

  Create `internal/history/history.go`, package comment: the ledger's
  observations, kept for 30 days by provider and hour, so `relay policy`
  can show when a provider tends to be limited (#61 step 7); it decides
  nothing. Mirror `internal/ledger/ledger.go` for `Load` (missing →
  empty), `Save` (write-then-rename), `Prune`, `Append`. `FromEntry` per
  spec §3.2. `HourCounts` per spec §3.2: `e.At.In(loc).Hour()`.

  Run: `go test ./internal/history/`. Expected: green.

- [ ] **Step 3: `HistoryPath`, `Runtime.HistoryPath`** (spec §3.3, §4.3)

  `store.go`: `func (s *Store) HistoryPath() string { return
  filepath.Join(s.root, "history.json") }` next to `LedgerPath` with a
  one-line comment. `herdr.go`: `HistoryPath string` below `LedgerPath`,
  comment: the availability history file (#61 step 7). `main.go`
  `newRuntime`: `HistoryPath: st.HistoryPath(),`. Every test `Runtime{`
  literal that sets `LedgerPath` also sets `HistoryPath:
  filepath.Join(t.TempDir(), "history.json")` (there are at least
  `newRuntime` in `bind_test.go`, `newForkRuntime` in `fork_test.go`, and
  `TestCandidateKind` in `candidate_test.go`; grep to be sure).

  Run: `go build ./... && go test -count=1 ./internal/relay/`. Expected: green.

- [ ] **Step 4: failing writer tests** (spec §4.1)

  In `internal/relay/ledger_test.go`, a helper `loadHistory(t, rt)
  history.History` like `loadLedger`. Tests:

  - `TestUnavailableRecordsHistory`: `Unavailable(rt, testClaudeRef,
    time.Time{}, "5h window")` → one ledger entry (as today) **and** one
    history event `{Kind: RateLimited, Provider: "test", Token: "",
    Source: "planner", Note: "5h window", At: baseTime}`.
  - `TestSpawnFailureRecordsHistory`: the `TestBindRecordsASpawnFailure`
    setup (fake `startErr`, `Bind` with `testAgyRef`) → one history event
    `{Kind: SpawnFailed, Provider: "test", Token: testAgyRef, Binding:
    "webshop", Source: "relay"}`, `Note` containing `agent start`.
  - `TestSwitchSpawnFailureRecordsHistory`: run `recordSpawnFailureLocked`
    inside `rt.Store.WithLock` (as `TestRecordSpawnFailureLockedUnderHeldLock`
    does) → one ledger entry and one history event.
  - `TestAvailableLeavesHistory`: `Unavailable` then `Available(rt,
    "test")` → ledger empty, history still has the one event.
  - `TestHistoryFailureDoesNotFailTheLedger`: `rt.HistoryPath` set to a
    path under a regular file (the `blocker` trick from
    `TestSpawnFailureDoesNotMaskTheError`); `Unavailable` returns nil
    error and the ledger has the entry.

  Run: `go test ./internal/relay/ -run 'History'`. Expected: FAIL.

- [ ] **Step 5: the writers** (spec §4.1)

  `internal/relay/ledger.go`:

  ```go
  // appendEntryLocked commits one observation: the ledger entry that gates,
  // then its mirror in the history that remembers (#61 step 7). The caller
  // holds the store lock. A history failure is printed and dropped -- the
  // ledger write is the one that matters, and it already happened.
  func appendEntryLocked(rt Runtime, e ledger.Entry) error {
      if err := mutateLedgerLocked(rt, func(l ledger.Ledger) ledger.Ledger { return l.Append(e) }); err != nil {
          return err
      }
      h, err := history.Load(rt.HistoryPath)
      if err == nil {
          err = history.Save(rt.HistoryPath, h.Prune(rt.Now()).Append(history.FromEntry(e, providerOf)))
      }
      if err != nil {
          fmt.Fprintf(os.Stderr, "relay: could not record history: %v\n", err)
      }
      return nil
  }
  ```

  where `providerOf` is the closure `Gates` builds -- lift it to a
  package-level `func providerOf(tok string) string` and use it in both
  places. `Unavailable`: replace its `mutateLedger(...)` call with
  `rt.Store.WithLock(func(*store.Tx) error { return appendEntryLocked(rt,
  entry) })`. `recordSpawnFailureWith(rt, locked bool, token, binding,
  cause)`: build the entry as today; `commit := func() error { return
  appendEntryLocked(rt, entry) }`; if `locked` call it directly, else
  `rt.Store.WithLock(func(*store.Tx) error { return commit() })`; stderr
  rule unchanged. `recordSpawnFailure` passes `false`,
  `recordSpawnFailureLocked` passes `true`. Update their doc comments.
  `mutateLedger`/`mutateLedgerLocked` remain for `Available`.

  Run: `go test -count=1 ./...`. Expected: green.

- [ ] **Step 6: `make check`, commit**

  ```bash
  git add -A internal cmd
  git commit -m "feat(history): keep the ledger's observations 30 days by provider and hour (#61 step 7)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, the test
functions added, and the list of test files whose `Runtime{` literals
gained `HistoryPath`.
