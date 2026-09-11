# Ledger T3: the readers -- `status`, `candidates`, `doctor`, the spawn-time note (#61 step 1)

**Design spec:** `docs/specs/2026-09-11-availability-ledger-design.md` -- read §3.4, §3.5, §4.4–4.8, §6
**Issue:** #61 (step 1)
**Depends on:** T1, T2 -- already in this tree.

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
- **A reader never fails on the ledger.** A `ledger.Load` error in
  `Status`, `FormatCandidates`' caller, `doctor`, or `gatedNote` is printed
  to stderr once and treated as an empty ledger (spec §6). Step 5 tests it.
- **Readers load without the lock** (spec §3.3). Do not wrap a read in
  `WithLock`.
- **Nothing gated → output byte-identical to today.** Every renderer test
  from before this task must still pass unchanged; the new block/column/rows
  appear only when `len(gates) > 0`.
- `ledgerChecks` rows are `SevWarn`, never `SevFail`, so `doctor`'s exit
  code is unchanged by a gate.
- **No test may execute a `cmd/relay` subcommand that reaches herdr.**
  `ledgerChecks` is a pure function tested in `cmd/relay/doctor_test.go`;
  wiring it into `cmdDoctor` is untested.
- CLI commands (`unavailable`, `available`) and the note's call sites in
  `main.go` are T4. This task produces the functions they call.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: `Gates`, `Report.Gated`, three renderers, `gatedNote`

**Files:**
- Modify: `internal/relay/ledger.go`, `internal/relay/ledger_test.go`
- Modify: `internal/relay/status.go`, `internal/relay/status_test.go`
- Modify: `internal/relay/candidates_list.go`, `internal/relay/candidates_list_test.go`
- Modify: `cmd/relay/doctor.go`, `cmd/relay/doctor_test.go`
- Modify: `cmd/relay/main.go` (`cmdCandidates` only)

**Interfaces consumed:** `ledger.Load`, `Ledger.Prune`, `ledger.Gated`,
`ledger.Gate`, `ledger.SpawnFailed`/`RateLimited`; `Runtime.LedgerPath`,
`Runtime.Candidates`, `Runtime.Now`; `candidate.ParseRef`, `Set.Refs()`;
`doctor.Check`, `doctor.SevWarn`, `insertGlobalCheck` (exists in
`cmd/relay/doctor.go`; note `ledgerChecks` rows have a `Group`, so they are
appended, not inserted among globals).

**Interfaces produced** (spec §3.5, §4.4–4.8):

```go
// internal/relay/ledger.go
func Gates(rt Runtime) []ledger.Gate          // load (lock-free), prune, Gated over rt.Candidates; load error → stderr + nil
func gatedNote(rt Runtime, token string) string
func GatedNote(rt Runtime, token string) string   // exported wrapper for cmd/relay; same result

// internal/relay/status.go
Report.Gated []ledger.Gate `json:"gated,omitempty"`

// internal/relay/candidates_list.go
func FormatCandidates(set *candidate.Set, gates []ledger.Gate) string    // signature change

// cmd/relay/doctor.go
func ledgerChecks(gates []ledger.Gate) []doctor.Check
```

- [ ] **Step 1: `Gates` and the time formatting helper**

  In `internal/relay/ledger.go`:

  ```go
  // Gates is what every reader renders from: the live ledger projected onto
  // the configured candidates. A load error is reported once on stderr and
  // read as an empty ledger -- status, candidates and doctor must not go
  // down over a bookkeeping file (spec §6).
  func Gates(rt Runtime) []ledger.Gate
  ```

  Body: `l, err := ledger.Load(rt.LedgerPath)`; on error print
  `relay: could not read ledger: <err>` to stderr and return `nil`.
  `providerOf := func(tok string) string { r, err := candidate.ParseRef(tok); if err != nil { return "" }; return r.Provider }`.
  Return `ledger.Gated(l, rt.Candidates.Refs(), providerOf, rt.Now())`.
  A nil `rt.Candidates` → `nil` (defensive; `newRuntime` always sets it).

  Two unexported helpers the renderers share, in the same file:

  ```go
  // gateKindText is the human wording for a gate kind in status, candidates
  // and doctor, so the three never drift: "spawn failed", "rate-limited".
  func gateKindText(k ledger.Kind) string

  // gateUntilText renders Until as "until HH:MM" in local time, or
  // "until cleared" for a zero Until.
  func gateUntilText(until time.Time) string
  ```

  Tests (`ledger_test.go`):
  - `TestGatesEmptyWhenNoLedger`: fresh runtime → `nil`.
  - `TestGatesProjectsOntoCandidates`: `Unavailable(rt, testClaudeRef, …)`
    then `recordSpawnFailure(rt, testAgyRef, "webshop", errors.New("x"))`.
    All three fixture tokens share provider `test`, so the rate limit gates
    every one of them. Want four gates: `agy/test/m` twice (one
    `RateLimited`, one `SpawnFailed` -- both have `Since == baseTime`, so
    assert the pair as a set, not an order), then `claude/test/m`
    `RateLimited`, then `opencode/test/m` `RateLimited`. Tokens are in
    sorted order.
  - `TestGatesToleratesABadLedger`: write `not json` to `rt.LedgerPath`;
    `Gates(rt)` returns `nil` and does not panic.
  - `TestGateUntilText`: zero → `until cleared`; a fixed time → `until
    HH:MM` in local time (compare against `t.Local().Format("15:04")`).

  Run: `go test ./internal/relay/ -run 'Gates|GateUntil'`. Expected: green.

- [ ] **Step 2: `Report.Gated` and the status block** (spec §3.5, §4.5, §4.6)

  `status.go`: add `Gated []ledger.Gate \`json:"gated,omitempty"\`` to
  `Report` with the spec's comment about staying absent when empty. At the
  end of `Status`, before `return`: `rep := Report{Bindings: rows}; rep.Gated
  = Gates(rt); return rep, nil`.

  `RenderStatus`: after the bindings loop and **before** the `DoneHidden`
  footer, when `len(r.Gated) > 0`:

  ```
  candidates
    <token padded to longest>  <kind text padded to 12>  <since HH:MM>  <until text>  <note>  (<binding>)
  ```

  One row per gate. `since` is `g.Since.Local().Format("15:04")`. `note`
  and `(binding)` are omitted when empty (no trailing spaces). When there
  are zero bindings, the block comes after the `no bindings` line: change
  the early return so that `no bindings\n` is written to the builder and
  the function falls through to the gated block and footer (keep the
  `DoneHidden` footer's behaviour: with zero bindings and `DoneHidden > 0`
  the first line is `N done · relay gc to clear` as today, then the gated
  block if any). End the block with a blank line only if a footer follows.

  Tests (`status_test.go`):
  - `TestRenderStatusGatedBlock`: a `Report` with one binding and two
    gates; the output contains a `candidates` line followed by the two
    rows, in order, each containing the token, the kind text, and `until
    cleared` / `until HH:MM`.
  - `TestRenderStatusNoGatesIsUnchanged`: the same `Report` with `Gated:
    nil` renders exactly what it rendered before this task (take an
    existing render test's expected string as the oracle, or assert the
    output does not contain `candidates`).
  - `TestRenderStatusGatesWithNoBindings`: `Report{Gated: […]}` →
    starts with `no bindings\n`, then the block.
  - `TestStatusPopulatesGated`: seed a bound binding, `Unavailable(...)`,
    `Status(ctx, rt)`; `rep.Gated` non-empty. JSON-encode the report and
    assert the `gated` key is present; encode a `Report{}` and assert it is
    absent.

  Run: `go test ./internal/relay/ -run 'Status'`. Expected: green.

- [ ] **Step 3: `FormatCandidates` with gates** (spec §4.7)

  Change the signature to `FormatCandidates(set *candidate.Set, gates
  []ledger.Gate) string`. Build `byToken := map[string][]ledger.Gate` once.
  For a gated row, append `   unavailable: <kind text> <until text>` per
  gate, joined with `; ` when there are several. The empty-set sentence is
  unchanged. Update the single caller `cmdCandidates` in `main.go` to
  `relay.FormatCandidates(rt.Candidates, relay.Gates(rt))`.

  Tests (`candidates_list_test.go`): the existing `TestFormatCandidates`
  passes `nil` gates and keeps its exact expected output. New
  `TestFormatCandidatesMarksGated`: one RateLimited gate on
  `claude/test/m` with zero `Until` → that row ends with
  `   unavailable: rate-limited until cleared` and the other two rows are
  unchanged.

  Run: `go test ./internal/relay/ -run FormatCandidates && go build ./...`.
  Expected: green.

- [ ] **Step 4: `ledgerChecks` in doctor** (spec §4.8)

  `cmd/relay/doctor.go`:

  ```go
  // ledgerChecks turns live gates into doctor rows under the candidate's
  // harness. They warn, never fail: a gated provider is a fact about right
  // now, not a broken install, and must not change doctor's exit code.
  func ledgerChecks(gates []ledger.Gate) []doctor.Check
  ```

  One `Check` per gate: `Group` = `candidate.ParseRef(g.Token).Harness`,
  `Name` = `ledger`, `Severity` = `doctor.SevWarn`, `Detail` =
  `fmt.Sprintf("%s: %s since %s (%s)", g.Token, kindText, since, untilText)`,
  `Fix` = `relay available <provider>` for `RateLimited`, `wait until
  HH:MM` for `SpawnFailed` (a `SpawnFailed` always has an `Until`).
  `kindText`/`untilText`: `cmd/relay` cannot call the unexported helpers in
  `internal/relay`, so export them as `relay.GateKindText` and
  `relay.GateUntilText` (rename the step-1 helpers; update their callers).

  `cmdDoctor`: after the existing `insertGlobalCheck` calls, `rep.Checks =
  append(rep.Checks, ledgerChecks(relay.Gates(rt))...)`. `renderReport`
  already groups by `Group`, so the rows land under their kind.

  Test (`doctor_test.go`) `TestLedgerChecks`: two gates (one of each kind)
  → two checks with the right `Group`, `Name == "ledger"`, `Severity ==
  SevWarn`, `Fix` starting `relay available ` for the rate limit and `wait
  until ` for the spawn failure. And `ledgerChecks(nil)` → empty.

  Run: `go test ./cmd/relay/ -run LedgerChecks`. Expected: green.

- [ ] **Step 5: `gatedNote`** (spec §4.4)

  In `internal/relay/ledger.go`:

  ```go
  // gatedNote is the one advisory line bind, add, fork and ask print after a
  // successful spawn of a candidate the ledger says is gated. Advisory only:
  // the agent is already running, and refusing is #61 step 4's job.
  func gatedNote(rt Runtime, token string) string
  ```

  Returns `""` when no gate in `Gates(rt)` has `Token == token`. Otherwise
  `fmt.Sprintf("note: %s is gated: %s; proceeding", token, parts)` where
  each part is `"<kind text> since HH:MM <until text>"` plus `": <note>"`
  when `Note != ""`, joined with `; `. Plus the exported `GatedNote` that
  `cmd/relay` will call (T4): identical, one line, calls `gatedNote`.

  Tests: `TestGatedNoteEmptyWhenNotGated`; `TestGatedNoteFormatsEveryGate`
  -- two gates on one token → both parts present, in order, one line, ends
  with `; proceeding`.

  Run: `go test ./internal/relay/`. Expected: green.

- [ ] **Step 6: `make check`, commit**

  ```bash
  git add -A internal cmd
  git commit -m "feat(relay): status, candidates and doctor show gated candidates; spawn-time note (#61)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, and the list of
test functions added. Confirm every pre-existing renderer test passed
without modification (name any you had to touch and why).
