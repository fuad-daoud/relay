# Plan: `relay pull` prints the report text (#332)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit.

## 1. System Overview

`relay pull` is the second half of every tools-mode background wait
(`relay wait --name N --timeout B; relay pull --name N`). Today it prints the
pending log entry's **Payload**, a pointer line ("Builder finished round N …
Report: <path>" plus a diff line), so the planner then needs an extra step to
`cat` the report. The channel push and the deliverers already expand that
payload with the report's text through `relay.PushText`
(`internal/relay/push.go`), capped at `MaxPushBytes` (64 KiB) with a
`[truncated at 64 KiB -- full text at <path>]` tail.

After this change `relay pull` prints exactly what `PushText` produces for the
pending entry: the payload (origin line first, unchanged) plus a blank line
plus the report's text. `--path-only` keeps the old pointer form for scripts.
Delivery semantics (which entry, confirm with route=pull, lock discipline) do
not change.

## 2. File Structure

```
internal/relay/pull.go        MODIFY  Pull gains a PullOptions parameter; expands via PushText unless PathOnly
internal/relay/pull_test.go   MODIFY  existing calls pass PullOptions{}; four new tests (§7 step 2)
internal/relay/send_test.go   MODIFY  the one Pull call (line ~294) passes PullOptions{}
internal/e2e/headless_test.go MODIFY  both Pull calls pass PullOptions{}; step 8 asserts the report text is in the pull output
cmd/relay/main.go             MODIFY  cmdPull: --path-only flag; help line for `pull` (line ~61)
README.md                     MODIFY  the `relay pull` bullet (line ~216)
```

No new files. `internal/mcp/instructions.go` already says "The relay pull half
prints the report text"; leave it alone. Do not touch `docs/design.md` or
anything under `docs/specs/`.

## 3. Data Structures & Type Definitions

### `relay.PullOptions` (new, in `internal/relay/pull.go`)

| Field      | Type | Meaning |
|------------|------|---------|
| `PathOnly` | bool | true: return the entry's stored `Payload` verbatim (today's behaviour). false (zero value, the default): return `PushText(entry, os.ReadFile)`. |

No other fields. Zero value must mean "print the text".

No changes to `store.LogEntry`, `PushText`, `MaxPushBytes` or
`truncatePushText`.

## 4. Interface Definitions & Component Contracts

### `relay.Pull` (changed signature)

```
func Pull(ctx context.Context, rt Runtime, name string, opts PullOptions) (string, bool, error)
```

- Responsibility: claim the oldest entry pending for binding `name`, mark it
  delivered with route `pull`, and return the text a planner should read.
- Returns `(text, true, nil)` when an entry was claimed; `("", false, nil)`
  when nothing is pending; `("", false, err)` on a store error.
- Preconditions: none beyond today's.
- Postconditions:
  - The claimed entry is confirmed with route `pull` exactly as today.
  - `opts.PathOnly == true`: text == entry.Payload, byte for byte.
  - `opts.PathOnly == false`: text == `PushText(entry, os.ReadFile)`'s first
    return value. That means: payload unchanged for kinds PushText does not
    expand (diff, drift, etc.) or when Path is empty; payload + "\n\n" +
    (possibly truncated) file text for report/findings/edge; and the bare
    payload when the file cannot be read (an unreadable report is not an
    error, and the entry stays confirmed).
  - The file read happens **after** `rt.Store.WithLock` returns: capture
    the `store.LogEntry` inside the lock and expand it outside. No file I/O
    under the state lock.
- Errors: only the store errors `WithLock`/`PendingForPlanner`/`ConfirmIndex`
  already return. PushText never errors.
- Update the doc comment to say it returns the report's text (PushText), and
  that PathOnly returns the stored pointer payload.

### `cmdPull` (CLI, `cmd/relay/main.go`)

- New flag: `--path-only` (bool, default false), usage string:
  `"print the pointer payload (report path and diff line) instead of the report text"`.
- Passes `relay.PullOptions{PathOnly: *pathOnly}` to `relay.Pull`.
- Output: prints the returned text with `fmt.Println`, as today. "nothing
  pending" is unchanged.
- Help line (the `Commands:` block, currently
  `pull      print the oldest pending payload to stdout, without typing anywhere`)
  becomes:
  `pull      print the oldest pending report's text to stdout and mark it delivered [--path-only]`
  Keep the column alignment of the surrounding lines.

## 5. High-Level Pseudocode

```
Pull(ctx, rt, name, opts):
    entry, found = zero, false
    err = rt.Store.WithLock(tx ->
        pending, idx, ok, err = tx.PendingForPlanner(name)
        if err: return err
        if not ok: return nil
        err = tx.ConfirmIndex(name, idx, "pull"); if err: return err
        entry, found = pending, true
    )
    if err: return "", false, err
    if not found: return "", false, nil
    if opts.PathOnly: return entry.Payload, true, nil
    text, _ = PushText(entry, os.ReadFile)
    return text, true, nil
```

## 6. Error Handling Strategy

- Store errors propagate unchanged (non-recoverable for this call; the CLI
  prints them as today).
- An unreadable or missing report file is recoverable and silent: PushText
  already returns the bare payload, which still names the path. Pull returns
  it with found=true. No new logging.
- No new error types.

## 7. Ordered Implementation Steps

### Step 1: change `Pull` and fix every caller

- Deliverable: `PullOptions` type and the new `Pull` signature/behaviour in
  `internal/relay/pull.go`, per §4/§5. Update every caller to compile:
  `cmd/relay/main.go` (temporarily `PullOptions{}`; step 3 wires the flag),
  `internal/relay/pull_test.go` (three existing tests → `PullOptions{}`),
  `internal/relay/send_test.go` (~line 294 → `PullOptions{}`),
  `internal/e2e/headless_test.go` (~lines 169 and 254 → `PullOptions{}`).
- Depends on: nothing.
- Verify: `go build ./...` and `go test ./internal/relay/ ./internal/e2e/`
  pass. The existing send_test assertion `strings.Contains(payload,
  "001-report.md")` must still pass unchanged, because the payload is still
  the prefix of the text.

### Step 2: unit tests in `internal/relay/pull_test.go`

Do **not** use `seedPending`'s entry for these tests. Its Path is the
literal `/tmp/report.md`, which may exist on a developer machine. Add a
small helper in `pull_test.go`, `seedPendingReport(t, rt, name, path string,
kind store.Kind)`, modelled on `seedPending` in `deliver_test.go`: same
binding, but it queues an entry with Payload `"round 1 report"` and the given
Path and Kind. Each test writes its report under `t.TempDir()`.

Add these tests:

1. `TestPullPrintsReportText`: report file contains
   `"line one\nline two\n"`, kind KindReport. `Pull(..., PullOptions{})`
   returns text that starts with `"round 1 report\n\n"` and contains
   `"line two"`. found=true, and the entry is confirmed with route `pull`.
2. `TestPullCapsReportText`: report file is `MaxPushBytes + 4096` bytes of
   newline-terminated lines. The text contains
   `fmt.Sprintf("[truncated at %d KiB -- full text at %s]", MaxPushBytes/1024, path)`
   and `len(text) < MaxPushBytes + len(payload) + 200`.
3. `TestPullPathOnlyPrintsPayload`: same file as test 1. With
   `PullOptions{PathOnly: true}` the text == `"round 1 report"` exactly.
4. `TestPullUnreadableReportFallsBackToPayload`: Path points at a
   non-existent file under `t.TempDir()`. text == `"round 1 report"`,
   found=true, err=nil, and the entry is confirmed.

- Depends on: step 1.
- Verify: all four pass. Mutation check, and put the result in your report:
  temporarily make Pull return `entry.Payload` unconditionally. Tests 1 and 2
  must fail. Then restore.

### Step 3: CLI flag, help text, README

- Deliverable: `--path-only` in `cmdPull` and the help line in
  `cmd/relay/main.go`, both per §4. In `README.md`, replace the `relay pull`
  bullet (~line 216) with:
  ```
  - `relay pull [NAME|--name N] [--path-only]` — print the oldest pending
    report's text to stdout (the report's pointer line, a blank line, then the
    report itself, capped at 64 KiB) and mark it delivered. This is how a
    planner fetches a report directly, and what the background wait's
    `relay pull` uses. `--path-only` prints just the pointer line for scripts.
  ```
- Depends on: step 1.
- Verify: `go build ./cmd/relay && ./relay pull --help 2>&1 | grep path-only`
  shows the flag. Then delete the built binary. Do **not** add a cmd/relay
  test that runs `pull` against real state. The rule is tested in
  internal/relay (step 2), and CI runners have no harness or network.

### Step 4: tighten the headless e2e (step 8 of `TestHeadlessE2E`)

- Deliverable: in `internal/e2e/headless_test.go`, after the existing
  `output := waited.res.Line + "\n" + pulled`, add an assertion that `pulled`
  contains `fakeReportText`. Failure message:
  `"relay pull output does not carry the report's text; want %q in:\n%s"`.
  Update the 8.4 comment above it to say that relay pull prints the payload
  **and the report's text** (PushText), so the planner needs no second read.
  Keep the existing assertions: the path is named, and the file on disk has
  the text.
- Depends on: step 1.
- Verify: `make e2e` passes. Mutation (report it): with Pull temporarily
  returning `entry.Payload` unconditionally, `make e2e` fails on the new
  assertion. Restore.

### Step 5: full check

- `make check` passes: gofmt, vet, the tidy check, all tests.
- `git diff --stat` touches only the six files in §2 (plus nothing else).
- Report: each step's verify output, both mutation results, and the final
  `git diff --stat`. Commit on the binding's branch with message
  `feat(pull): relay pull prints the report's text, --path-only keeps the pointer (#332)`.
