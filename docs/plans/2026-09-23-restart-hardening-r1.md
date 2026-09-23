# Plan: restart hardening, round 1 of 3: liveness correctness (#370)

**Read first:** `/home/fuad/projects/relay/docs/specs/2026-09-23-restart-hardening-design.md`. That file is the design. This plan says what to build in this round and in what order. Where the two disagree, the spec wins. Stop and report rather than pick.

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code (a function isn't where this plan says, or an existing test encodes the opposite rule), halt, write the report saying which step and why, and create the done marker. Don't bend a test to fit.

## 1. System overview

`relay daemon` restarts on every upgrade. Builders, gates and verify consults already survive a restart, because each runs in its own systemd scope (#309, verified live on 2026-09-23). This round makes the daemon's *judgement* about those processes correct across a restart:

- a failed `ps` no longer reads as a dead process;
- "lost to a restart" means "this daemon never saw it alive", not only "it started before this daemon";
- a lost gate is re-run once;
- a consult killed before it finished is never delivered as findings;
- a relaunch keeps the round's clock and tells the builder about its own partial edits;
- a panic in one binding can't take the daemon down.

Out of this round: the probe retry, the doctor/status restart-safety check and the unit start limit (round 2), resume (round 3), and **any change to `Daemon.Run`**. #371 owns `Run`, and a parallel branch edits it.

## 2. File structure

```
docs/specs/2026-09-23-restart-hardening-design.md   COPY from the absolute path above (commit it with this round)
internal/proc/pscheck.go        NEW  classifyPS + psVerdict (spec §4.1); `//go:build unix` like proc.go
internal/proc/pscheck_test.go   NEW  table test; `//go:build unix`
                                (CI cross-compiles windows/amd64: every file using unix-only syscall
                                 types must carry the tag; proc_other.go is the !unix stub)
internal/proc/proc.go           psInfo uses classifyPS
internal/relay/watched.go       NEW  Watched, NewWatched, Mark, Seen, lostToRestart (spec §4.2)
internal/relay/watched_test.go  NEW
internal/relay/runtime.go       Runtime.Watched *Watched
internal/relay/headless.go      Mark on alive / on Start; lost uses lostToRestart; keep RoundStartedAt; interruptedNote
internal/relay/gate.go          startGate helper; lost gate re-run once (spec §4.4)
internal/relay/consult.go       Mark on alive; no trailer -> Silent, never Done (spec §4.5)
internal/relay/verify.go        Mark after a successful verify consult Start
internal/relay/daemon.go        recover in tickOne; safely() around runFires/ingest/refreshRelease (spec §4.6)
internal/store/types.go         GateRun.Attempt int `json:"attempt,omitempty"`
cmd/relay/main.go               daemon command: rt.Watched = relay.NewWatched() next to rt.StartedAt
*_test.go in internal/relay     tests listed in step 8
```

## 3. Data structures

- `relay.Watched`: spec §3. The fields are unexported: `mu sync.Mutex` and `seen map[watchKey]struct{}`, with `watchKey{pid int; startedAt int64}`.
- `store.GateRun.Attempt int`: 0 is the first run and 1 is the re-run after a loss. Invariant: it is at most 1. An existing `bind.json` without the field decodes as 0.
- `proc.psVerdict`: an unexported enum with the values `psOK`, `psNoProcess` and `psTransient`.

## 4. Interfaces and contracts

The exact rules are in the spec sections given. Signatures:

- `func classifyPS(ctxErr error, exitErr *exec.ExitError, stdout []byte) psVerdict` (spec §4.1). Detect a signal with `exitErr.ProcessState.Sys().(syscall.WaitStatus)`, and treat a failed type assertion as `psTransient`.
- `func NewWatched() *Watched`
- `func (w *Watched) Mark(pid int, startedAt int64)`. It is nil-safe and ignores `pid <= 0` and `startedAt == 0`.
- `func (w *Watched) Seen(pid int, startedAt int64) bool`. It is nil-safe and returns false.
- `func lostToRestart(rt Runtime, pid int, startedAt int64) bool` (spec §4.2).
- `func interruptedNote(t time.Time) string`. A package-level format string that renders `t.UTC().Format(time.RFC3339)`. The text covers the three points in spec §4.3.
- `func startGate(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, attempt int, note string) (store.Binding, *store.GateRecord, error)`. It is the body of today's `GateRun == nil` branch, with `attempt` stored on the new `GateRun` and `note` used as the `KindGate` entry's note. A Start error returns a non-nil `GateRecord{Result:"error", Note: err}`, exactly as today.
- `func (d *Daemon) safely(phase string, f func())`. It recovers and logs at Error with `phase`, the panic value and `string(debug.Stack())`.

Preconditions and postconditions:
- `lostToRestart` never returns true in a CLI process (`rt.StartedAt` is zero).
- After this round, a builder, gate or consult that this daemon has seen alive is never classified as lost.

## 5. High-level pseudocode

```
psInfo(ctx,pid):
  out, err := ps ...
  if err == nil -> parse as today
  var ee *exec.ExitError; errors.As(err,&ee)
  switch classifyPS(ctx.Err(), ee /*nil if not ExitError*/, out):
    psNoProcess -> errNoProcess
    psTransient -> fmt.Errorf("proc: ps: %w", err)
  (a non-ExitError err is returned wrapped, as today)

reconcileHeadless (builder):
  alive, err := Alive(...)
  if err == nil && alive -> rt.Watched.Mark(b.Builder.PID, b.Builder.StartedAt)
  ...
  lost := codeText == "unknown" && lostToRestart(rt, b.Builder.PID, b.Builder.StartedAt)   // read PID/StartedAt BEFORE they are zeroed
  if lost && switchable:
     served -> unchanged
     local  -> text := composePrompt(...) + "\n\n" + interruptedNote(rt.StartedAt)
               keep := b.RoundStartedAt
               relaunched := startRound(...)
               relaunched.RoundStartedAt = keep            // replaces `b.RoundStartedAt = now`
               Mark(relaunched.Builder.PID, relaunched.Builder.StartedAt)
               log note unchanged in shape

startRound success paths that run in the daemon (the switch path and the relaunch):
  Mark after Start. Marking inside startRound itself is fine: Watched is nil in CLI processes.

gateStep:
  GateRun == nil -> startGate(ctx, rt, tx, b, 0, "gate started: "+b.Gate)
  alive (no err) -> Mark(GateRun.PID, GateRun.StartedAt)
  exited, !ok:
     if lostToRestart(rt, GateRun.PID, GateRun.StartedAt) && GateRun.Attempt == 0:
         b.GateRun = nil
         b, rec, err := startGate(ctx, rt, tx, b, 1, "gate restarted (lost to a daemon restart): "+cmd)
         if rec != nil -> return b, true, rec, err     // start failed: report the error
         return b, false, nil, err
     else -> rec{error, "no exit trailer"} (unchanged)
  startGate also Marks the new handle.

reconcileConsults, headless consult:
  alive (no err) -> Mark(c.Endpoint.PID, c.Endpoint.StartedAt)
  exited:
     code, ok := ExitCode(...)
     if !ok:
        note := lostToRestart(...) ? "lost to a daemon restart before it finished (no exit trailer); partial output: "+log
                                   : "ended without an exit trailer (killed before it finished); partial output: "+log
        finishConsult(Silent, note); continue
     text := FinalText(...)            // unchanged from here
     ...

startVerifyConsult: Mark after a successful Start.

Daemon.tickOne(ctx,name) (err error):
  defer func(){ if r := recover(); r != nil { log Error("reconcile panicked", binding, panic, stack); err = fmt.Errorf("reconcile %s panicked: %v", name, r) } }()
  ...unchanged...
Daemon.Tick: runFires / ingestLiveBindings / refreshRelease each wrapped: d.safely("fires", func(){...}) etc.

cmd/relay daemon command:
  rt.StartedAt = time.Now()
  rt.Watched = relay.NewWatched()
```

## 6. Error handling strategy

- A transient `ps` failure is returned as an error. Callers already log "liveness check failed; treating as alive" and continue. Don't add a new halt anywhere.
- A lost gate re-run that fails to start is reported like today's gate start error: `GateRecord{Result:"error"}`, attached to the report.
- A consult with no trailer always closes Silent with a note naming its log. The findings file is not written.
- A panic is logged at Error with a stack, and it is never re-panicked. The binding is retried next tick.
- There is no new `NEEDS YOU` path in this round.

## 7. Ordered implementation steps

**Step 1: bring the spec in.** Copy `/home/fuad/projects/relay/docs/specs/2026-09-23-restart-hardening-design.md` to `docs/specs/` in your tree.
Verify: `git status` shows it.

**Step 2: `classifyPS`, and use it in psInfo** (spec §4.1).
Test `internal/proc/pscheck_test.go`, table-driven. Build real `*exec.ExitError` values by running `sh -c 'exit 1'`, `sh -c 'exit 2'` and `sh -c 'kill -TERM $$'` (sh is fine in CI, it isn't a harness). Cases: exit 1 with empty stdout gives NoProcess; exit 1 with non-empty stdout gives Transient; exit 2 gives Transient; signalled gives Transient; ctxErr set gives Transient; a nil exitErr gives OK.
Verify: `go test ./internal/proc/`. Mutation: make the signalled case return NoProcess, and confirm a named case fails.

**Step 3: `Watched` and `lostToRestart`** (spec §4.2), plus `Runtime.Watched`.
Tests in `watched_test.go`:
- nil-safety;
- Mark then Seen;
- zero-value inputs are ignored;
- `lostToRestart`: false when `rt.StartedAt` is zero; false when the process started after the daemon; true when it started before and was not seen; false when it started before and was seen; true with a nil `Watched` when it started before (the #244 compatibility case).
Verify: `go test ./internal/relay/ -run Watched`.

**Step 4: headless builder** (§4.3 R1 part).
- Add the Mark points.
- Replace the `lost :=` expression.
- Keep `RoundStartedAt` across the relaunch.
- Append `interruptedNote` to the relaunch prompt.
- Before you change the prompt, grep for any code that parses the relaunch prompt's text or its last lines. If some code depends on it, halt.
Verify: the existing #244 relaunch tests still pass without edits. If one has to change, halt and report which one and why.

**Step 5: gate** (§4.4). Add the `GateRun.Attempt` field, extract `startGate`, and add the re-run branch.
Verify: the existing gate tests pass unchanged.

**Step 6: consult** (§4.5). Add the Mark point and read `ExitCode` before `FinalText`. Also add the Mark after a successful verify consult start.
Verify: the existing consult and verify tests pass. A test that currently expects Done from a stream without a trailer encodes the old rule, so halt rather than edit it, unless its stream lacks the trailer only because the fixture omitted it. In that case add the trailer to the fixture and say so in the report.

**Step 7: panic recovery** (§4.6), in `tickOne` and `Tick` only. Do not modify `Run`.

**Step 8: tests for the new rules** (fake Runner in `internal/relay`, the pattern the existing headless tests use):
- a builder started before `rt.StartedAt` and marked seen, then exited with no trailer: **not** relaunched, and it takes the normal exited-without-report path;
- the same builder not seen: relaunched uncounted; the prompt passed to `Runner.Start` contains the interrupted note; `RoundStartedAt` equals its pre-relaunch value;
- gate lost (started before the daemon, not seen, no trailer, Attempt 0): a second Start with `Attempt == 1` and a `KindGate` entry "gate restarted"; lost again at Attempt 1: `Result "error"`;
- gate seen alive, then no trailer: `Result "error"`, no re-run;
- headless consult exited without a trailer while its stream has assistant text: Silent, the findings file absent, and the note containing "no exit trailer";
- a Runner whose `Alive` panics for binding A: `Tick` still reconciles binding B and returns nil;
- `lostToRestart` is never true when `rt.StartedAt` is zero (the CLI).

Mutation checks, recorded in the report with the test that failed for each:
- (a) drop the `Seen` clause from `lostToRestart`;
- (b) drop `RoundStartedAt = keep`;
- (c) drop the `Attempt == 0` guard;
- (d) move `FinalText` back before the trailer check.

**Step 9: wire the daemon.** In `cmd/relay/main.go`'s daemon command, add `rt.Watched = relay.NewWatched()` next to `rt.StartedAt`. Add no cmd/relay test that runs the daemon: CI has no harness.

**Step 10: gate.** Run `make check` (gofmt, vet, `go test -race`, tidy, scripts) and `make e2e`. Both must pass. Commit on this branch with a message ending `(#370)`.

Declared scope: exactly the files in §2. A change outside them is a halt unless it is a test fixture the steps name.
