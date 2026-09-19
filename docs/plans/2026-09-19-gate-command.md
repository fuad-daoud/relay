# Gate command on the completion marker: run a check in the worktree, annotate the round (#132, part 1 of 2)

Part 1 of #132: the gate itself -- configure, run after the marker, record
the result on the round, never decide. Part 2 (the opt-in repair round,
`--regate`, the stall signature) is a separate plan after this one lands.
Design in this file; no separate spec.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else.

**Scope guard.** Touch only the files listed in §2. Do not run `make e2e`
(the planner runs it: this touches `closeOnMarker`). New flags are exactly
`--gate` and `--no-gate` on `bind`, `add`, `fork`; no new verb. Do not add a
module dependency. Do not implement any repair/re-send behaviour. Run every
command in the foreground; dispatch no sub-agents. Do not widen any exported
signature §4 does not name.

**Commits.** Squash to ONE commit before the gate, subject starting
`feat(relay):`.

**Commit the plan with the work.** Copy this plan file to
`docs/plans/2026-09-19-gate-command.md` in your worktree and include it.

**Before step 1**: `git status --short` and `git branch --show-current`; you
must be on `relay/<binding-name>` under `~/.local/state/relay/.worktrees/`.
Otherwise halt.

## 1. System overview

Today the planner runs `make check` in the worktree by hand after every
round because the report cannot be trusted. This plan lets a binding carry a
**gate command** that relay runs in the worktree the moment the builder's
completion marker appears -- against the tree exactly as the builder left it,
the same instant the round diff is captured -- and records `pass`, `fail`
or `timeout` on the round. The round still closes on the marker, the report
is still delivered, and the human still judges; the gate only adds one line
to the payload, a `gate=` note and a structured record on the report entry,
and the full output in `NNN-gate.log`.

Mechanics: `closeOnMarker` runs inside the store lock each tick, so the gate
cannot run to completion there. Instead it is a small state machine across
ticks: marker seen and no gate running -> start the command through
`Runtime.Runner` (the same supervisor headless builders use; `Start`
returns at once) and record the process on the binding; each following
tick -> alive? keep waiting (timeout -> `Kill`, result `timeout`); exited
-> read the exit code from the supervisor's trailer, write the result, and
close the round as today with the gate's annotation. While the gate runs the
round is open but **nothing else may act on the builder**: no nudge, no
"exited without a report" switch, no timeout halt -- both marker call sites
return early on `gating`.

Configuration: `bind|add|fork --gate '<cmd>'`, default `policy.json`
`gate.default` (`""` = no gate), `--no-gate` to opt a binding out of the
default. `gate.timeout_ms` (default 10 minutes). Per binding because the
command is per repo and relay has no per-repo config file. Bindings written
before this plan have no gate and are byte-for-byte unchanged.

## 2. File structure

```
internal/store/
  types.go           EDIT Binding.Gate, Binding.GateTimeoutMS, Binding.GateRun *GateRun; GateRun type
  log.go             EDIT KindGate; LogEntry.Gate *GateRecord; GateRecord type
  store.go           EDIT GateLogPath(name, round)
internal/policy/
  policy.go          EDIT Policy.Gate *GatePolicy; validation; accessors
  policy_test.go     EDIT cases
internal/relay/
  gate.go            NEW  gateStep, gateResult, gateLine, tailLines, gate constants
  gate_test.go       NEW  pure: gateLine text for each result; tailLines; timeout arithmetic
  reconcile.go       EDIT closeOnMarker returns gating; runs gateStep; pane call site returns early on gating
  headless.go        EDIT marker call site returns early on gating
  bind.go            EDIT BindOptions.Gate/NoGate; create stores Gate/GateTimeoutMS
  add.go             EDIT AddOptions.Gate/NoGate
  fork.go            EDIT ForkOptions.Gate/NoGate; default inherits source's Gate
  status.go          EDIT BuilderStatus "gating 1m12s" while GateRun != nil
  logline.go         EDIT " gate=<result>" on report entries with a GateRecord
  reconcile_test.go  EDIT gate lifecycle tests (§8)
  remote.go          EDIT queueReport caller passes nil (planner omission, added after round 1)
  send_test.go       EDIT same
  bind_test.go       EDIT Gate stored from flag / policy / --no-gate
  status_test.go     EDIT gating row text
  logline_test.go    EDIT gate= case
cmd/relay/
  main.go            EDIT --gate / --no-gate on bind, add, fork; pass through
README.md            EDIT "Gate" section; policy `gate` block in the example JSON
docs/plans/2026-09-19-gate-command.md   NEW  this file
```

## 3. Data structures & type definitions

```go
// internal/store/types.go -- on Binding, after Tier/RoundTier:

// Gate is the acceptance command relay runs in the worktree when the
// round's completion marker appears (#132); "" means no gate. Run through
// `sh -c`, so it may be any shell line. Set at bind/add/fork; never changed
// by relay.
Gate string `json:"gate,omitempty"`
// GateTimeoutMS bounds one gate run; 0 means policy.GateTimeout().
GateTimeoutMS int `json:"gate_timeout_ms,omitempty"`
// GateRun is the gate process for the CURRENT round while it runs; nil
// otherwise. Transient: written when the gate starts, cleared when the
// round closes.
GateRun *GateRun `json:"gate_run,omitempty"`

type GateRun struct {
    PID       int   `json:"pid"`
    StartedAt int64 `json:"started_at"` // Unix seconds, as Endpoint.StartedAt
    Round     int   `json:"round"`
    Command   string `json:"command"`
}

// internal/store/log.go
KindGate Kind = "gate" // relay -> log only: the gate started (#132)

// on LogEntry, after Tier:
// Gate is the acceptance check's result, on report entries of a gated
// round (#132). Nil when the binding has no gate or the entry predates it.
Gate *GateRecord `json:"gate,omitempty"`

type GateRecord struct {
    Command    string `json:"command"`
    Result     string `json:"result"`             // "pass" | "fail" | "timeout" | "error"
    ExitCode   int    `json:"exit_code"`          // meaningful for pass/fail
    DurationMS int64  `json:"duration_ms"`
    LogPath    string `json:"log_path"`
    Note       string `json:"note,omitempty"`     // why "error": start failed, no runner, ...
}
```

```go
// internal/policy/policy.go
type GatePolicy struct {
    Default   string `json:"default,omitempty"`    // "" = no gate unless --gate
    TimeoutMS *int   `json:"timeout_ms,omitempty"` // nil = DefaultGateTimeout; must be > 0
}
Gate *GatePolicy `json:"gate,omitempty"`
const DefaultGateTimeout = 10 * time.Minute
func (p Policy) GateDefault() string          // "" when Gate nil
func (p Policy) GateTimeout() time.Duration   // default applied; safe on nil Gate
```
Validation: `gate.timeout_ms` present and `<= 0` -> `ErrBadPolicy`
(`gate.timeout_ms: must be > 0, got %d`).

```go
// internal/store/store.go
func (s *Store) GateLogPath(name string, round int) string   // <dir>/NNN-gate.log, NNN zero-padded like the other round files
```

## 4. Interface definitions & component contracts

### 4.1 `closeOnMarker` -- widened (unexported)

```go
// closeOnMarker closes an open round when the builder's completion marker
// exists. With a gate configured it first runs the gate across ticks:
// gating is true while the gate is running and the round must be left
// alone; closed is true when the round was closed this tick. Never both.
func closeOnMarker(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, extraNote string) (next store.Binding, closed, gating bool, err error)
```
Both callers: `if gating { return next, nil }` (the binding with `GateRun`
set is saved by the tick's `tx.Save`; do not call `deliverAndSettle`).

### 4.2 `gateStep` (gate.go)

```go
// gateStep advances the gate for a round whose marker exists. Pure with
// respect to the store: it touches only rt.Runner, the clock and files under
// the binding dir. Returns the updated binding, and either done == false
// (the gate is running; caller returns gating) or done == true with the
// record to attach to the report.
//
//   b.Gate == ""                         -> done, rec == nil          (no gate; zero cost)
//   rt.Runner == nil                     -> done, rec{Result:"error", Note:"no runner"}
//   GateRun == nil                       -> Start; on error rec{Result:"error", Note: err}; else GateRun set, KindGate entry appended, done == false
//   GateRun != nil, Alive                -> if now - StartedAt >= timeout: Kill, rec{Result:"timeout"}; else done == false
//   GateRun != nil, exited               -> code, ok := ExitCode(stream); !ok -> rec{Result:"error", Note:"no exit trailer"}; code == 0 -> "pass"; else "fail"
// On done, GateRun is cleared on the returned binding.
func gateStep(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (next store.Binding, done bool, rec *store.GateRecord, err error)
```
Process spec: `ProcSpec{Dir: b.CWD, Argv: []string{"sh", "-c", b.Gate + " 2>&1"}, LogPath: gateLog, StreamPath: gateLog}` -- one file holds command output and the supervisor's `relay-exit:` trailer (`ExitCode` reads the trailer from `StreamPath`; both paths the same file is deliberate and documented in the code).

```go
// gateLine is the payload line. pass:
//   "Gate: make check -- PASS (exit 0, 1m40s). Output: <log>"
// fail adds the tail:
//   "Gate: make check -- FAIL (exit 2, 1m40s). Output: <log>\n  <last 5 non-empty lines of the log, each indented two spaces>"
// timeout: "Gate: make check -- TIMEOUT after 10m0s. Output: <log>"
// error:   "Gate: make check -- ERROR: <note>."
func gateLine(rec store.GateRecord, tail []string) string

// tailLines returns the last n non-empty lines of the file at path, or nil
// when it cannot be read; never an error (the log is a convenience).
func tailLines(path string, n int) []string
const gateTailLines = 5
```

### 4.3 Options and storage

`BindOptions`, `AddOptions`, `ForkOptions` gain `Gate string` and `NoGate bool`.
Resolution at create time: `NoGate` -> `""`; `Gate != ""` -> that; fork with
both empty -> the source binding's `Gate`; else `rt.Policy.GateDefault()`.
Stored as `Binding.Gate`. `GateTimeoutMS` stays 0 (policy decides) -- no
flag for it in this plan.

### 4.4 Status and log line

`Status`: after the builder status is filled, `if b.GateRun != nil { row.BuilderStatus = "gating " + age }` where age is `rt.Now() - time.Unix(GateRun.StartedAt, 0)` rounded to seconds, formatted like the other ages in the file (read how `headlessStatus` formats "since"; match it).

`LogLine`: report entries with `e.Gate != nil` append ` gate=<Result>`.

## 5. High-level pseudocode

```
closeOnMarker(ctx, rt, tx, b, entries, extraNote):
  if marker absent: return b, false, false, nil
  b, done, rec, err = gateStep(ctx, rt, tx, b)
  if err: return b, false, false, err
  if !done: return b, false, true, nil                  // gating
  note = extraNote
  gateSuffix = ""
  if rec != nil:
    note = joinNotes(note, "gate="+rec.Result)
    tail = nil; if rec.Result == "fail": tail = tailLines(rec.LogPath, gateTailLines)
    gateSuffix = "\n" + gateLine(*rec, tail)
  reportPath = ...
  if report exists:
    payload = "Builder finished round N. Report: <p>" + gateSuffix
    next = queueReport(..., payload, joinNotes("", note)); attach rec: see below
  else:
    payload = "Builder wrote its completion marker ... no report ..." + gateSuffix
    next = queueReport(..., payload, joinNotes("noreport", note))
  return next, true, false, nil
```
Attaching `rec` to the report entry: `queueReport` builds the entry
internally. Add an unexported field on `Runtime`? No -- thread it: give
`queueReport` one more parameter `gate *store.GateRecord` (unexported
function; six callers pass `nil`, `closeOnMarker` passes `rec`), and set
`Gate: gate` on the entry. Say in the report that you updated every caller.

```
gateStep:
  if b.Gate == "": return b, true, nil, nil
  if rt.Runner == nil: return b, true, &GateRecord{Command: b.Gate, Result: "error", Note: "no runner", LogPath: log}, nil
  log = rt.Store.GateLogPath(b.Name, b.Round)
  if b.GateRun == nil:
    h, err = rt.Runner.Start(ctx, ProcSpec{Dir: b.CWD, Argv: ["sh","-c", b.Gate+" 2>&1"], LogPath: log, StreamPath: log})
    if err: return b, true, &GateRecord{..., Result:"error", Note: err.Error()}, nil
    b.GateRun = &GateRun{PID: h.PID, StartedAt: h.StartedAt.Unix(), Round: b.Round, Command: b.Gate}
    tx.AppendLog(b.Name, LogEntry{TS: now, Round: b.Round, Direction: DirToPlanner, Kind: KindGate, Path: log, Note: "gate started: "+b.Gate, Confirmed: true})
    return b, false, nil, nil
  h = ProcHandle{PID: GateRun.PID, StartedAt: time.Unix(GateRun.StartedAt,0)}
  alive, err = rt.Runner.Alive(ctx, h); if err: treat as alive (as headless does), log warn
  elapsed = now - time.Unix(GateRun.StartedAt,0)
  if alive:
    if elapsed >= timeout(b, rt.Policy): rt.Runner.Kill(ctx, h); rec = {Result:"timeout", DurationMS: elapsed}; b.GateRun = nil; return b, true, rec, nil
    return b, false, nil, nil
  code, ok = rt.Runner.ExitCode(ctx, h, log)
  rec = {Command, LogPath: log, DurationMS: elapsed}
  if !ok: rec.Result = "error"; rec.Note = "no exit trailer"
  elif code == 0: rec.Result = "pass"; rec.ExitCode = 0
  else: rec.Result = "fail"; rec.ExitCode = code
  b.GateRun = nil
  return b, true, &rec, nil

timeout(b, pol): b.GateTimeoutMS > 0 ? that : pol.GateTimeout()
```

`queueReport` already clears the round's transient fields; `GateRun` is
cleared in `gateStep` before the close, and `queueReport` also sets
`b.GateRun = nil` defensively where it clears `RoundTier`.

## 6. Error handling strategy

The gate never fails a round or a tick. `Start` failure, no runner, missing
trailer -> `Result: "error"` with the reason in `Note` and the payload; the
round closes normally. `Alive` error -> treated as alive this tick (same
rule as the headless path). `Kill` error on timeout -> logged, result still
`timeout`. `tailLines` failure -> no tail. No ledger entry, no switch, no
`NEEDS YOU` from a gate result in this plan.

## 7. README

New "Gate" section after "Permission tiers": what it does, the stance
(annotates, never decides; the human still judges), `--gate`/`--no-gate`,
`policy.json` `gate.default` and `gate.timeout_ms`, where the output lives,
the payload line, that the repair loop is a follow-up. Add `gate` to the
policy example JSON.

## 8. Tests

All through the existing fakes (`fakeHerdr`, `fakeRunner`, `sentBinding`,
`reconcile`); no herdr, no real process.

`reconcile_test.go` (name them exactly):
- `TestGateNotConfiguredIsUnchanged`: marker + report, `Gate == ""` -> `fakeRunner.specs` empty, round closed this tick, payload identical to today's.
- `TestGateStartsOnMarkerAndHoldsTheRound`: `Gate: "make check"`, marker + report -> exactly one `Start` with `Dir == b.CWD`, `Argv == ["sh","-c","make check 2>&1"]`, `LogPath == StreamPath == GateLogPath`; `GateRun` set with the fake's pid; a `KindGate` entry; round NOT closed; no report entry. Second tick with `alive` scripted true -> still not closed, still one `Start`, **and no nudge**: assert no `KindPlan`-direction prompt/`Prompt` call reached `fakeHerdr` and no `KindExit`/`KindSwitch` entries. **Mutation check (run and report):** make the pane call site ignore `gating` (fall through to the status switch); this test fails on the nudge/prompt; restore; passes.
- `TestGatePassClosesWithAnnotation`: alive false, exit 0 -> round closed; report entry `Note` contains `gate=pass`, `Gate.Result == "pass"`, `Gate.ExitCode == 0`, payload contains `Gate: make check -- PASS (exit 0,` and the log path; `GateRun == nil`.
- `TestGateFailAddsTail`: write six lines to the gate log before the exit tick; exit 2 -> `gate=fail`, payload contains `FAIL (exit 2,` and the last five lines, not the first.
- `TestGateTimeoutKills`: alive true; advance `rt.Now` past `GateTimeout()` -> one `Kill` on the gate handle, `Result == "timeout"`, round closed, payload contains `TIMEOUT after`.
- `TestGateNoRunnerIsErrorNotHang`: `rt.Runner = nil`, `Gate` set -> round closes this tick with `Result == "error"`, `Note == "no runner"`.
- `TestGateHeadlessCallSiteHolds`: the headless marker path with a running gate -> no "exited without a report" handling (no `KindExit`, no switch, `RoundSwitches == 0`).

`bind_test.go`: `--gate` stored; policy `gate.default` applied when flag empty; `--no-gate` yields `""` despite the default; fork inherits the source's gate.
`policy_test.go`: `gate.timeout_ms: 0` -> `ErrBadPolicy`; defaults.
`status_test.go`: `GateRun` set -> `BuilderStatus` starts with `gating `.
`logline_test.go`: report entry with `Gate{Result:"fail"}` -> ` gate=fail`.
`gate_test.go`: `gateLine` four forms; `tailLines` last-n non-empty; missing file -> nil.

## 9. Ordered implementation steps

1. Store: `types.go`, `log.go`, `store.go` (`GateLogPath`). `go test ./internal/store`.
2. Policy block + tests.
3. `gate.go` (`gateStep`, `gateLine`, `tailLines`) + `gate_test.go`.
4. `closeOnMarker` widening, `queueReport` `gate` parameter (all callers), both call sites' `gating` early return; the seven reconcile tests; run the mutation check, record both outcomes.
5. Options + storage in `bind.go`/`add.go`/`fork.go` + `bind_test.go`.
6. `status.go`, `logline.go` + tests.
7. CLI flags in `main.go`; `go vet ./cmd/relay`.
8. README.
9. Squash to one `feat(relay):` commit including the plan copy. Gate, exactly:
   ```
   test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
   make check
   ```
   Report the tail verbatim, the mutation outcomes, and `git diff --stat main..HEAD`.

## 10. Report

End `NNN-report.md` with the ```relay block (`status`, `halted_at`,
`changed_paths`, `commands_run`, `not_done`). In prose: list every
`queueReport` caller you updated.
