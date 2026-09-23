# Plan: `relay candidates --probe` can't hang on a child that keeps stdout open

If any step is impossible as written or contradicts the code you find, STOP
and report. Do not bend a test to fit.

## Working efficiently

Each model step costs a few seconds of round trip, so read the two files
below in one step, write each file in one call, and loop with
`go test ./cmd/relay/ -run 'TestLineExec' -count=1`. Run `make check` once
at the end.

Read first: `cmd/relay/probe_exec.go` (whole file, ~50 lines), and
`internal/relay/probe.go` for the `LineExec` interface only.

## 1. System Overview

`lineExec.Run` (`cmd/relay/probe_exec.go`, from #343) runs one harness for
`relay candidates --probe`. It reads stdout with `cmd.StdoutPipe()` and a
`bufio.Scanner` loop **before** calling `cmd.Wait()`. If the harness leaves
a descendant holding the stdout pipe open (a background child, an MCP
server, a helper daemon), the scan loop never sees EOF and the probe hangs.
The 90 s context timeout doesn't help: it kills only the direct process,
and the loop is still blocked on the grandchild's pipe.

`exec.Cmd.WaitDelay` bounds exactly this, but only for I/O that `Wait`
itself copies. So `Run` must stop reading the pipe itself: it hands `exec` an
`io.Writer` that splits lines, and lets `Wait` own the copy.

## 2. File Structure

```
cmd/relay/probe_exec.go       MODIFY  lineWriter; Run uses cmd.Stdout + WaitDelay
cmd/relay/probe_exec_test.go  CREATE  two tests, using /bin/sh only
```

No other file changes.

## 3. Data Structures & Type Definitions

### `lineWriter` (unexported, `probe_exec.go`)

| Field | Type | Meaning |
|-------|------|---------|
| `onLine` | `func([]byte)` | called once per complete line, without the trailing `\n`; may be nil |
| `buf` | `[]byte` | the bytes of a line not yet terminated |

### `probeWaitDelay` (unexported const)

`5 * time.Second`: how long `Wait` waits for stdout/stderr to close after the
process exits or is killed.

## 4. Interface Definitions & Component Contracts

### `(*lineWriter) Write(p []byte) (int, error)`

- Appends `p` to `buf`. For every `\n` in `buf`, calls `onLine` with the
  bytes before it (a `\r` right before the `\n` is dropped as well), then
  removes that line and its newline from `buf`.
- Always returns `len(p), nil`.
- `onLine` must not retain the slice; `transcript.FirstOutput` doesn't.
  Pass a slice of `buf` or a copy, your choice.

### `(*lineWriter) Flush()`

If `buf` is non-empty, calls `onLine(buf)` once and empties it. This is for
a final line with no trailing newline.

### `lineExec.Run` (same signature, changed body)

- Keep `exec.CommandContext(ctx, argv[0], argv[1:]...)`, `cmd.Dir` and
  `cmd.Env` exactly as they are.
- `lw := &lineWriter{onLine: onLine}`, then `cmd.Stdout = lw`. Keep stderr
  as today (a buffer, whose last 300 bytes go into the error).
- `cmd.WaitDelay = probeWaitDelay`.
- `err := cmd.Run()` (Start and Wait), then `lw.Flush()`.
- If `errors.Is(err, exec.ErrWaitDelay)`, treat it as success (`err = nil`).
  That error means the process itself exited 0 and only a descendant held
  the pipe, so the probe's answer is valid.
- Any other err: `fmt.Errorf("%w: %s", err, lastStderr)`, as today.
- Remove the `StdoutPipe` and `bufio.Scanner` code. `onLine` is now called on
  exec's copy goroutine, and `cmd.Run` returns only after that goroutine
  finishes, so the caller's reads after `Run` returns are ordered after
  every `onLine` call. No locking is needed. Add a one-line comment saying
  so.
- Out of scope: killing the process group. That needs platform-specific
  `SysProcAttr`, and the `cross-compile` CI job builds non-unix targets. A
  leftover descendant can outlive the probe, but it can no longer hang it.

## 5. High-Level Pseudocode

```
Run(ctx, dir, argv, onLine):
    cmd = CommandContext(ctx, argv...); cmd.Dir, cmd.Env as before
    lw = lineWriter{onLine}; cmd.Stdout = lw; cmd.Stderr = stderrBuf
    cmd.WaitDelay = 5s
    err = cmd.Run()
    lw.Flush()
    if err is ErrWaitDelay: err = nil
    if err: return wrap(err, tail(stderrBuf, 300))
    return nil
```

## 6. Error Handling Strategy

- `ErrWaitDelay` becomes success, as explained in §4.
- Every other error keeps today's wrapping.
- No new logging.

## 7. Ordered Implementation Steps

### Step 1: `lineWriter` and the new `Run`

- Deliverable: §3/§4 in `cmd/relay/probe_exec.go`.
- Depends on: nothing.
- Verify: `go build ./... && go vet ./cmd/relay/`.

### Step 2: tests (`cmd/relay/probe_exec_test.go`)

These tests run `/bin/sh` only. They spawn no harness and touch no network,
which CI allows. They must not read the user's config or state; the
package's `TestMain` already points `HOME` and the XDG variables at a temp
root. Skip each test with `t.Skip` when `exec.LookPath("sh")` fails.

1. `TestLineWriterSplitsLines`: write `"a\nb"`, then `"c\r\nd\n"`, then
   `"tail"`, then `Flush`. `onLine` receives `a`, `bc`, `d`, `tail`, in
   that order.
2. `TestLineExecDoesNotHangOnInheritedStdout`: `lineExec{}.Run` with
   `argv = []string{"sh", "-c", "echo first; sleep 20 & exit 0"}`, a
   `t.TempDir()` dir, and a 60 s context. The background `sleep` inherits
   stdout and holds it open for 20 s. Assert that:
   - Run returns within **12 s** (measure with `time.Now()`);
   - the error is nil (the ErrWaitDelay mapping);
   - `onLine` received `first`.
3. `TestLineExecNonZeroExitCarriesStderr`:
   `argv = []string{"sh", "-c", "echo oops 1>&2; exit 3"}`. The error is
   non-nil, and its text contains `exit status 3` and `oops`.

- Depends on: step 1.
- Verify: `go test ./cmd/relay/ -run 'TestLineExec|TestLineWriter' -count=1 -v`
  passes, and test 2 reports roughly 5 s.
- Mutation (report it): set `cmd.WaitDelay = 0`. Test 2 must fail on the
  12 s bound. It will take about 20 s to fail, which is expected. Restore.

### Step 3: full check

- `make check` passes, and `git diff --stat` shows exactly the two files in
  §2.
- Report: each verify output, the mutation result, and the diff stat.
  Commit on the binding's branch with the message
  `fix(candidates): a probe cannot hang on a child that keeps stdout open`.
