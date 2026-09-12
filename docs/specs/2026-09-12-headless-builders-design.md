# Headless builders: a builder that is a process, not a pane

**Issue:** headless half of #6 (split; the remote half is its own issue)
**Depends on:** nothing open. #79 (tab-only placement, #96) and #92 (`--rebind`, #97) landed.
**Amends:** CLAUDE.md "Working with builders" (relay now stops one more kind of
builder: a headless process it started, on `done`/`unbind`); README "Where the
builder appears", "Command surface", "Recovery"

## 1. System overview

Every builder relay spawns today is a herdr pane: `bind` opens a tab, starts
the harness interactively in it, and the daemon reads the pane's agent status
and screen to follow the round. That is the right shape when a human watches
the builder work. It is the wrong shape when nobody does: an idle opencode
pane holds ~800 MB, three builders are three tabs of noise, and the whole
arrangement needs herdr on the machine the builder runs on.

This design adds a second builder shape, chosen per binding with `--headless`
on `bind`, `add` and `fork`. A headless builder is a **process relay runs
directly**, one fresh process per round, in the binding's working tree:
`relay send` launches `<harness> -p <prompt>` (each harness's non-interactive
form), stdout and stderr go to a log file beside the round's plan and report,
and the process exits when the report is written or when it fails. The
report file was already the round's contract; headless makes it the only
contract. Between rounds a headless binding has no process and is idle, not
broken.

The pane shape stays, unchanged, as the other way. Which one wins is an open
question this design deliberately does not answer: it keeps both so the
answer can come from use. If headless wins, the pane path is deleted in a
later issue; if it does not, `--headless` is.

Principles kept:

- **The report file is the contract.** Nothing downstream of `finishRound`
  changes. A headless builder that wrote a report has done its job even if it
  then exited non-zero.
- **relay reads no screen text for meaning** (policy-order spec §1). A
  headless builder's log is shown to humans, never parsed. Rate limits are
  still declared by the planner with `relay unavailable`.
- **Plans are round-complete.** A fresh process per round means no memory
  across rounds. relay plans already carry their own context (worktree
  table, conventions, "stop rather than improvise"); headless makes that a
  stated constraint instead of something an agent's session happened to
  cover.
- **One seam for "where does the process run".** The `Runner` interface is
  local-only here. A remote runner (ssh) plugs into the same seam; nothing in
  this design is undone by it.

### Scope boundary

- Builders only. Consults (`relay ask`) keep spawning tabs; a headless consult
  is a follow-up issue on the same runner.
- Local processes only. No `--remote`, no host on the endpoint, no file sync.
- No session continuity across rounds. `Endpoint` gets no session field for
  headless; if continuity earns its place later it is a new field, not a
  reinterpretation of `SessionID`.
- No mode change on an existing binding: `--resume --headless` is refused.
  Unbind and bind again.
- No live log streaming. `relay ui` polls the log file the way it polls a
  pane today.
- `relay answer` is builder-only and pane-only. A headless builder does not
  take dialogs; the command is refused on a headless binding.

## 2. File structure

```
internal/proc/
  proc.go            Local Runner: detached start, liveness with start-time check, kill with grace
  proc_test.go       One real-process test (/bin/sh) pinning detach, log capture, exit code

internal/relay/
  runner.go          Runner interface, ProcSpec, ProcHandle, ErrRunnerUnavailable
  headless.go        headlessLaunch (argv from harness.Launch), startRound, headless reconcile branch,
                     logTail, refuseHeadless helpers
  headless_test.go   Reconcile/send/done paths against fakeRunner; mutation targets named in §7
  fake_test.go       fakeRunner beside fakeHerdr (extended)
  bind.go            BindOptions.Headless; create/resume/resolveBuilder branch (modified)
  add.go, fork.go    Headless option plumbing (modified)
  send.go            Branch: pane → Prompt, headless → startRound (modified)
  reconcile.go       Branch on Builder.Mode at the top of the builder section (modified)
  done.go / unbind.go  Kill a live headless process (modified; whichever holds the teardown today)
  status.go          Builder line for headless (modified)
  answer.go          Refuse on headless (modified)
  herdr.go           Runtime.Runner field (modified)

internal/harness/
  harness.go         Launch gains the print form (modified)
  harness_test.go    Argv per kind, both forms (modified)

internal/store/
  types.go           Endpoint.Mode, PID, StartedAt, LogPath; Store.BuilderLogPath (modified)

internal/ui/
  terminal tab reads the log file for headless bindings (modified)

cmd/relay/
  main.go            --headless on bind/add/fork; runtime wiring (modified)
  main_test.go       Flag-conflict tests that fail before newRuntime (modified)

README.md, CLAUDE.md
```

## 3. Data structures and type definitions

### 3.1 `store.Endpoint` (extended)

```
Endpoint
  AgentName string   (existing)
  PaneID    string   (existing; "" for headless)
  SessionID string   (existing; "" for headless)
  Kind      string   (existing; harness kind)
  Mode      Mode     "pane" | "headless"; "" reads as "pane" so every stored binding is unchanged
  PID       int      headless only; 0 when no process (between rounds, or never started)
  StartedAt time.Time headless only; the process's start time as the OS reports it, for pid-reuse defence
  LogPath   string   headless only; absolute path of the current round's log; "" between rounds
```

Invariants: `Mode == headless` ⇒ `PaneID == ""` and `SessionID == ""`.
`PID != 0` ⇒ `LogPath != ""`. `Mode == pane` ⇒ `PID == 0`.

### 3.2 `store.Mode`

```
type Mode string
const (
  ModePane     Mode = "pane"
  ModeHeadless Mode = "headless"
)
func (e Endpoint) Headless() bool   // Mode == ModeHeadless
```

### 3.3 `store.Store.BuilderLogPath(name string, round int) string`

`<state>/<name>/NNN-builder.log`, beside `PlanPath` and `ReportPath`, so
`gc` archives it with the round and `fork` copies it with the history.

### 3.4 `relay.ProcSpec`, `relay.ProcHandle`

```
ProcSpec
  Dir     string     working directory (the binding's CWD)
  Argv    []string   Argv[0] is the binary name, resolved on PATH by the runner
  Env     []string   additions to the parent environment; nil for none
  LogPath string     stdout and stderr, appended, created if absent

ProcHandle
  PID       int
  StartedAt time.Time
```

### 3.5 `harness.Launch` (extended)

```
Launch
  Kind     string    (existing)
  Args     []string  (existing; the interactive argv, after the binary)
  Print    []string  the non-interactive argv, after the binary, with harness.PromptPlaceholder
                     ("<prompt>") and, where the kind has a timeout flag, harness.BudgetPlaceholder
                     ("<budget>") as their own elements
  PromptAt int       index of PromptPlaceholder in Print; -1 for an unknown kind

func (l Launch) PrintArgs(prompt string, budget time.Duration) []string
                     Print with both placeholders filled (budget as a Go duration string), a fresh
                     slice. Amended at step 2: the substitution lives here, so no other package
                     learns where a kind puts its prompt or its budget.
```

Rendered per kind, `extra` appended to both forms exactly as today:

| kind | Print (before extra) |
|---|---|
| agy | `-p <prompt> --model M --agent <def> --output-format text --print-timeout <budget>` |
| claude | `-p <prompt> --model M --agent <def> --output-format text` |
| opencode | `run <prompt> -m P/M --agent <def>` |

`<budget>` is the binding's round budget rendered as a Go duration; agy's
default of 5m would kill any real round.

### 3.6 `relay.BindOptions`, `AddOptions`, `ForkOptions` (extended)

`Headless bool`. On `BindOptions` it is refused with `BuilderPane` (adopt) and
with `Resume` (mode change).

### 3.7 `relay.Runtime` (extended)

`Runner Runner`. `cmd/relay` wires `proc.New()`. Tests wire `fakeRunner`.

### 3.8 Log entries

One new kind: `store.KindExit = "exit"` (relay → log only, like `pick` and
`switch`), `DirToPlanner`, `Confirmed`, note `builder exited (code N)
without a report`, payload the last `logTailLines` (20) lines of the log.
When the exit triggers a switch, the `switch` entry follows with reason
`exited` and the pick, exactly as the `gone` reason does today. A process
that exits *after* writing its report produces no entry: the report is the
record.

## 4. Interface definitions and component contracts

### 4.1 `relay.Runner`

Single responsibility: start, observe and stop one process on behalf of a
binding. Knows nothing about rounds, reports or harnesses.

```
Start(ctx, ProcSpec) (ProcHandle, error)
  pre:  Dir exists; Argv non-empty; LogPath's directory exists
  post: a detached supervisor (own session and process group, stdin closed) runs Argv with
        stdout+stderr appended to LogPath and, when Argv exits, appends one final line
        `relay-exit:<code>` to LogPath. The handle is the supervisor's pid; the caller never
        waits on it. Returns as soon as the pid exists. The supervisor is `sh -c` (no bash-isms).
  err:  binary not found on PATH (the runner checks with exec.LookPath before starting, so the
        error is immediate and not a `relay-exit:127` in the log); Dir missing; log unwritable
Alive(ctx, ProcHandle) (bool, error)
  post: true iff a process with that pid exists AND its OS start time matches StartedAt
        within one second; false for a reused pid
  err:  only when the OS refuses to answer (permissions); a missing pid is (false, nil)
ExitCode(ctx, ProcHandle, logPath string) (code int, ok bool)
  post: ok iff the log's last line is `relay-exit:N`; the only thing relay ever reads from a log
Kill(ctx, ProcHandle) error
  post: SIGTERM to the process group; if the supervisor is still alive after killGrace (5s),
        SIGKILL to the group. Not alive → nil. Never signals a reused pid (Alive's start-time
        check first).
```

### 4.2 `relay.headlessLaunch(c candidate.Candidate, role harness.RoleSpec, budget time.Duration, prompt string) ([]string, error)`

Renders `[binary] + Launch.PrintArgs(prompt, budget)`. Pure. Errors: unknown kind (cannot happen after
`Load` validated the set; returned, not panicked).

### 4.3 `relay.startRound(ctx, rt, tx, b) (store.Binding, error)`

Called by `Send` in place of `Herdr.Prompt` when `b.Builder.Headless()`.

- pre: round is open (Send already staged the plan and bumped the round); `b.Builder.PID == 0`
  (a live process from a previous round is a bug: Send refuses with `ErrBuilderBusy`)
- does: composes the prompt exactly as the pane path does; `Runner.Start` with
  `Dir=b.CWD`, `LogPath=BuilderLogPath(name, round)`; records `PID`, `StartedAt`, `LogPath`
- post: binding saved with the handle; `KindPlan` entry as today
- err: `Start` failure → the round stays open with `PID == 0`, the binding goes `NEEDS YOU`
  with the error as note, and `recordSpawnFailure` writes the ledger `spawn_failed` entry
  exactly as a pane spawn failure does (it is the same failure: the candidate could not
  be launched)

### 4.4 `Reconcile` (modified): the headless branch

At the point where today's code locates the builder among `agents`, branch on
`b.Builder.Headless()`. The pane branch is untouched. The headless branch is
§5.1.

### 4.5 `relay.logTail(path string, n int) string`

Last `n` lines of the file, or `""` if absent. Read-only; never parsed.

### 4.6 `Done` / `Unbind` (modified)

If `b.Builder.Headless()` and `PID != 0`: `Runner.Kill`. Errors are reported,
not fatal: the binding is still marked done/unbound. The two existing
"relay closes a pane" places are unchanged; this is the one place relay
stops a process.

### 4.7 `Answer` (modified)

`b.Builder.Headless()` → `ErrHeadlessNoDialog`:
`"<name>'s builder is headless and takes no dialogs; read <LogPath>"`.

### 4.8 `status` (modified)

Builder line for headless:
`builder   headless  <kind>  <working|idle|exited N>  [pid P since HH:MM]  \`<token>\``.
The screen snippet slot shows `logTail(LogPath, 3)`.

### 4.9 `harness.Launch` (extended)

Same signature; fills `Print` and `PromptAt`. `Args` unchanged so every
existing caller and test is unaffected.

## 5. High-level pseudocode

### 5.1 One tick, headless binding

```
if b.Builder.Headless():
    if roundOpen(b):
        if fileExists(reportPath):
            finishRound(...)                      -- unchanged; report is the contract
            b.Builder.PID, StartedAt, LogPath = 0, zero, ""   -- process is done with; do not Kill,
                                                   -- it exits on its own after writing
            return
        alive, err := Runner.Alive(b.Builder.handle())
        if err: log, treat as alive (do not switch on an OS hiccup)
        if alive:
            b.BuilderScreen = logTail(LogPath, screenLines)   -- same field the pane path fills
            budget check as today → NEEDS YOU, never Kill
            return
        -- exited without a report
        code, ok := Runner.ExitCode(handle, LogPath)  -- the supervisor's trailer line; "unknown" if !ok
        append KindExit entry "builder exited (code N) without a report" + logTail(LogPath, 20)
        b.Builder.PID = 0
        hand to the mid-round switch exactly as "builder gone" does today:
            if switches < max_switches: resolve next candidate, startRound again
            else: NEEDS YOU, notify once
        return
    else:
        -- no round open: idle is normal
        b.Builder.PID must be 0 (a process with no round is a stray: Kill it, note it)
        state stays as it is; never BROKEN for "no process"
```

### 5.2 `relay send`, headless

```
Send(name, file):
    stage plan, bump round, KindPlan entry               -- as today
    if b.Builder.Headless():
        if b.Builder.PID != 0 and Alive: ErrBuilderBusy   -- previous round still running
        startRound(...)
    else:
        Herdr.Prompt(...)                                 -- as today
```

### 5.3 `relay bind --headless`

```
create(opts):
    resolveCandidate as today; pick entry as today
    if opts.Headless:
        Builder = Endpoint{Mode: headless, Kind: c.Harness, AgentName: name+"-builder"}
        -- no tab, no StartAgent, no process; nothing to strand
    else:
        openTab / StartAgent as today
```

### 5.4 Mid-round switch, headless

`switchBuilder` already re-resolves and respawns. For a headless endpoint
the "close the replaced builder's pane" step becomes `Kill` if the old
process is somehow alive (it is not, in the exit path; it may be, in the
`relay unavailable` path — the planner gated the provider while the
process was still running). The new builder inherits the mode.

## 6. Error handling strategy

| error | where | recoverable | surfaces as |
|---|---|---|---|
| binary not on PATH / `Start` fails | `startRound` | yes: switch or `--rebind` | `NEEDS YOU`; ledger `spawn_failed` (10 m) as for panes |
| exited without report | reconcile | yes: switch, up to `max_switches`; then human | log entry + notice with log tail |
| `Alive` OS error | reconcile | n/a | logged to stderr; treated as alive this tick |
| `Kill` fails on done/unbind | teardown | human | printed; binding still finalised |
| `--headless` with `--builder <pane>` / `--resume` | CLI | user | refused before `newRuntime` |
| `relay answer` on headless | CLI/relay | user | `ErrHeadlessNoDialog` |
| `send` while previous process alive | `Send` | user | `ErrBuilderBusy`: wait, or `relay done` |
| stray process with no round open | reconcile | auto | killed, noted in log |

Logging: every process start (`plan` entry, as today) and every exit without
a report (`exit` entry) is in the round log. The log file is never parsed
except for the trailing `relay-exit:N` line the supervisor itself appends.

## 7. Ordered implementation steps

Each step is one plan/PR-sized unit; each ends with `make check` green.

1. **`store`: `Mode`, endpoint fields, `BuilderLogPath`, `KindExit`.** Pure data. Verify: round-trip
   test of a headless endpoint; a stored pane binding with no `mode` reads as pane.
2. **`harness.Launch.Print`.** Argv table for three kinds, budget substitution. Verify:
   table test per kind; `Args` unchanged (existing tests untouched).
3. **`relay.Runner` + `internal/proc`.** Interface, local impl, `fakeRunner`. Verify: the
   `/bin/sh` test — process outlives the test's own process group, log has both streams,
   `ExitCode` reads 3 from the trailer, `Alive` false after exit, `Alive` false for a
   fabricated handle with a wrong `StartedAt`, `Kill` on a `sleep 60` returns within the grace.
4. **`bind --headless` (+ `add`, `fork`).** Endpoint recorded, no spawn. CLI conflicts refused
   before `newRuntime` (herdr-free tests). Verify: bind produces a headless endpoint with
   `PID 0`; `fakeHerdr.tabs` and `starts` empty; `--headless --builder w2:p4` and
   `--resume --headless` are refused.
5. **`send` → `startRound`.** Verify: `fakeRunner` records the spec (dir = CWD, log path =
   `BuilderLogPath`, argv = binary + Print with the prompt at `PromptAt`); `PID` saved;
   `ErrBuilderBusy` when a live handle exists. Mutation: skip saving the handle → the
   liveness test in step 6 fails.
6. **Reconcile headless branch.** Report → finish; alive → screen from log; exited → failure
   entry + switch; idle is not broken. Verify each row of §5.1 with scripted `fakeRunner`
   `Alive` sequences. Mutation: remove the exited-without-report branch → the named test
   fails; remove the "idle is not broken" guard → the named test fails.
7. **`done`/`unbind` kill; `answer` refusal; `status` line; `ui` log tab.** Verify:
   `fakeRunner.kills` has the handle after `done`; `answer` returns `ErrHeadlessNoDialog`;
   status golden line.
8. **Docs.** README (three sections), CLAUDE.md bullet, this spec's "Amends" lines applied.
   Planner verification (after merge, live): `relay bind --headless --name x`, `relay send`,
   watch `relay status` show `working pid N`, report lands, binding idle; `relay done` on a
   mid-round binding kills the process.
