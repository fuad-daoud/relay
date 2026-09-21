# Wave 1 batch B: the halt always says why, a re-send that cannot start is refused, a daemon restart is not a builder failure, a candidate with missing role files is never picked (#250, #244, #238)

Three switch-budget bugs seen on the serve box on 2026-09-20. This plan
stands alone: everything you need is in this file and in the tree. If a step
is impossible as written or contradicts the code, **halt and report** -- do
not improvise around it.

You are a headless builder on a server-side worktree of this repo. The repo
there has **no tags**, so never run `make check`; run the gate commands in
§7 exactly as written. Run every command in the foreground and read its
exit code; never as a background task. Do not spawn sub-agents for the
edits.

## 1. System overview

The local daemon and `relay serve` run the same code: `internal/serve/daemon.go`
`Tick` builds a `relay.Runtime` per owner (`serve.go:103` `runtimeAt`) and
calls `relay.Daemon.Tick` -> `Reconcile` -> `reconcileHeadless`. So every
fix below lands once, in `internal/relay`, and both get it.

1. **Empty halt (#250 item 2).** `haltBinding` (`internal/relay/reconcile.go:282`)
   assigns `b.Halt`/`b.HaltAt` only inside the `HaltNotifiedRound != b.Round`
   guard that dedups the notification. `Send` (`send.go:288-290`) and the
   client's `sendRemote` (`remote.go:426-427`) clear `Halt` on a re-send but
   never reset `HaltNotifiedRound`, so the second halt of a re-sent round
   (here: `already switched 2 time(s) this round (max_switches 2)`) sets
   `State = needs_you` and leaves `Halt == ""`; the client then prints
   `no reason recorded (binding predates the halt record)`. Fix: the halt
   text is always recorded; only the notification and the log line are
   deduped. And a successful send resets the round's halt bookkeeping
   (`HaltNotifiedRound`, `RoundSwitches`, `RoundExcluded`): the human asked
   for another attempt, the attempt gets a fresh budget.
2. **A re-send that cannot start is accepted silently (#250 items 1 and 3).**
   `internal/serve/rounds.go:213-218`: when the server's `relay.Send` fails
   and the binding is `needs_you` (e.g. `startRound` failed and set
   `Halt = "builder spawn failed: ..."`), the handler answers **201** with the
   view. The client (`remote.go:398-435`) takes 201 as success: writes the
   plan entry, `State = active`, prints `sent round N`. Nothing runs. Fix:
   the server logs the failure and answers 409 with the halt text; the client
   returns that as the send's error and writes no local state.
3. **A daemon restart is charged as a builder failure (#244 half 1).**
   Liveness is `ps`-based (`internal/proc/proc.go:140`), the supervisor is
   `Setsid`, so a builder survives a plain daemon exit; but a cgroup/group
   kill (systemd restart, `kill -9` of the tree) takes the supervisor with
   it before it writes the `relay-exit:` trailer. `reconcileHeadless`
   (`headless.go:384-388`) then reads `code unknown` and at `:463-464`
   excludes the candidate and charges a counted switch. The daemon can tell
   this case apart: it knows when it itself started, and the builder's
   recorded start (`Endpoint.StartedAt`) is earlier than that. Fix: a
   `Runtime.StartedAt` (the daemon's own start; zero = unknown, current
   behaviour); on `code unknown` with `builder.StartedAt < rt.StartedAt`,
   relaunch the **same** candidate on the same round with the same prompt,
   log it, charge nothing. The relaunched process starts after
   `rt.StartedAt`, so the branch cannot fire twice per daemon life.
   (Half 2 of #244, `KillMode=process` in the unit files, is not in this plan.)
4. **A candidate with missing role files is spawned and dies in seconds (#238).**
   Nothing on the pick path (`internal/relay/candidate.go:182` `resolveCandidate`,
   `headless.go:83` `startRound`) looks at the harness's role files; only
   `relay doctor` does (`internal/doctor/doctor.go:280` `roleCheck`). Fix: a
   new in-memory gate kind, `roles_missing`, synthesised in `relay.Gates`
   from an injectable `Runtime.Roles` checker (nil in tests = no check, so
   the existing suites are untouched), so `resolveCandidate` skips the
   candidate like any gated one -- and, unlike a rate-limit gate, an
   **explicit** `--builder` pick of such a candidate is refused, because it
   cannot succeed. `relay serve` logs the per-candidate result once at
   startup.

## 2. File structure

```
internal/ledger/ledger.go              + Kind RolesMissing = "roles_missing" (in-memory only, like ExitedNoReport)
internal/harness/roles.go              + RoleChecker, MissingDefinitions(env, kind, definitions) []string, OSRoleChecker()
internal/harness/roles_test.go         + tests
internal/relay/herdr.go                Runtime + StartedAt time.Time, + Roles harness.RoleChecker
internal/relay/ledger.go               Gates(rt): + rolesMissingGates(rt); GateKindText: + "roles missing"
internal/relay/candidate.go            resolveCandidate: explicit token with a RolesMissing gate is refused
internal/relay/candidate_test.go       + TestRolesMissingSkipsInOrder, TestRolesMissingRefusesExplicit
internal/relay/reconcile.go            haltBinding: Halt/HaltAt always set
internal/relay/send.go                 successful Send resets HaltNotifiedRound, RoundSwitches, RoundExcluded
internal/relay/remote.go               sendRemote: needs_you view / 409 CodeRoundHalted -> error, no local write
internal/relay/headless.go             exit-without-report: restart-loss branch relaunches the same candidate
internal/relay/switch_test.go          + TestExhaustionAfterResendStillSaysWhy
internal/relay/headless_test.go        + TestReconcileHeadlessLostToDaemonRestartRelaunches, TestReconcileHeadlessUnknownExitBeforeDaemonStartStillSwitches, TestSendResetsRoundBudget
internal/relay/remote_test.go          + TestSendRemoteHaltedIsAnError
internal/relay/fake_test.go            (only if a fake needs a field for the new tests)
internal/remote/proto.go               + CodeRoundHalted error code
internal/serve/rounds.go               handleStartRound: failed Send on a needs_you binding -> slog.Warn + 409 CodeRoundHalted with the halt text
internal/serve/serve.go                Config + StartedAt time.Time, + Roles harness.RoleChecker; runtimeAt passes both
internal/serve/serve_test.go           + TestRoundResendThatCannotStartIs409
cmd/relay/main.go                      newRuntime: Roles = harness.OSRoleChecker(); cmdDaemon: rt.StartedAt = time.Now()
cmd/relay/serve.go                     cmdServeRun: Config.StartedAt, Config.Roles; startup per-candidate roles log
README.md                              two sentences (see task 5)
docs/plans/2026-09-21-w1b-switch-budget.md   copy of this plan
```

Nothing outside these files, except call-site updates that a widened
struct literal forces (`grep -rn "relay.Runtime{" --include=*.go` and
`grep -rn "serve.Config{" --include=*.go` -- new fields are optional, so
there should be none; list the grep output in the report).

## 3. Data structures

```
// internal/ledger/ledger.go
const RolesMissing Kind = "roles_missing"
    // Never written to the ledger file: synthesised in memory by relay.Gates from
    // Runtime.Roles, like ExitedNoReport is from Binding.RoundExcluded. Load never sees it.

// internal/harness/roles.go
type RoleChecker interface {
    // Missing returns the home-relative paths (harness.Role.Path) of the definitions the
    // builder role needs for this harness kind that are not readable on disk. nil = all present.
    Missing(kind string) []string
}
func MissingDefinitions(env InstallEnv, kind string, definitions []string) []string
    // definitions = harness.RoleByName("builder").Definitions ({"plan-executor","researcher"});
    // for each, h := Lookup(kind); r := h.Role(def); path, _ := env.HomePath(r.Path); env.ReadFile(path) error -> append r.Path
    // unknown kind -> nil (the candidate loader already refused it)
type osRoleChecker struct{ env InstallEnv }   // Missing(kind) = MissingDefinitions(env, kind, RoleByName("builder").Definitions)
func OSRoleChecker() RoleChecker              // osRoleChecker{OSInstallEnv()}

// internal/relay/herdr.go  Runtime
StartedAt time.Time          // when this daemon process started; zero = unknown (CLI one-shots, tests)
Roles     harness.RoleChecker // nil = never check role files (tests); cmd/relay sets OSRoleChecker()

// internal/serve/serve.go  Config
StartedAt time.Time
Roles     harness.RoleChecker

// internal/remote/proto.go
CodeRoundHalted = "round_halted"   // 409: the round could not start; message = the binding's Halt text
```

## 4. Interfaces

```
// internal/relay/reconcile.go
func haltBinding(ctx, rt, b, message string) (store.Binding, error)
    b.Halt = strings.TrimPrefix(message, b.Name+": "); b.HaltAt = rt.Now().UTC()      // ALWAYS
    if b.HaltNotifiedRound != b.Round { notify (error returned as today); slog.Info("binding halted"...); b.HaltNotifiedRound = b.Round }
    b.State = StateNeedsYou
    // postcondition: State == needs_you implies Halt != "" whenever message is non-empty

// internal/relay/send.go  (the success path that today sets State=Active, Halt="", HaltAt=zero, ~:288-290)
    also: b.HaltNotifiedRound = 0; b.RoundSwitches = 0; b.RoundExcluded = nil
    // a human re-send is a fresh attempt; the next halt in this round notifies again

// internal/relay/remote.go  sendRemote
    after StartRound returns without error: if view.RoundState == remote.RoundNeedsYou {
        return SendResult{}, fmt.Errorf("%s: round %d could not start on %s: %s", name, b.Round, server, orText(view.Halt, "no reason given"))   // before the WithLock block; nothing written
    }
    StartRound error with code CodeRoundHalted -> fmt.Errorf("%s: round %d could not start on %s: %s", name, b.Round, server, <server message>)
    (both branches; a server built before this plan may still answer 201 + needs_you, hence the first)

// internal/serve/rounds.go  handleStartRound, the sendErr branch (~:213-218)
    if b.State == store.StateNeedsYou || b.Halt != "" {
        slog.Warn("round start failed", "binding", name, "round", b.Round, "err", sendErr, "halt", b.Halt)
        writeErr(w, http.StatusConflict, remote.CodeRoundHalted, orText(b.Halt, sendErr.Error()))
        return
    }
    // every other sendErr path (503 NoRunner, 409 busy, 422 tier, 500) unchanged

// internal/relay/headless.go  reconcileHeadless, exited-without-report path, after the exitEntry is appended
// and before the escape / limit / denial / !switchable checks:
    lost := codeText == "unknown" && !rt.StartedAt.IsZero() && b.Builder.StartedAt != 0 &&
            time.Unix(b.Builder.StartedAt, 0).Before(rt.StartedAt)
    if lost && switchable {
        text := composePrompt(...)   // exactly what switchBuilder builds at switch.go:190 for the same round
        b, err := startRound(ctx, rt, b, text)
        if err != nil { return haltBinding(ctx, rt, b, fmt.Sprintf("%s: builder lost to a daemon restart and could not be relaunched: %v", b.Name, err)) }
        tx.AppendLog(b.Name, LogEntry{Kind: KindExit... NO: use a pick-shaped entry? -> use store.KindSwitch with Note
             "relaunched builder (lost to a daemon restart at <rt.StartedAt RFC3339>): picked <b.BuilderCandidate> for builder: same candidate, not counted"})
        appendLogMarker(..., "relaunched "+b.BuilderCandidate+" (lost to a daemon restart)")
        b.RoundStartedAt = now; b.State = StateActive
        slog.Info("headless builder relaunched after daemon restart", "binding", "round", "candidate")
        return b, nil
        // RoundSwitches and RoundExcluded untouched. The exit entry stays in the log (it happened).
    }
    // The note shape "…: picked <tok> for builder: …" is what internal/ingest parsePickNote reads (batch A fixes it to stop at the first space), so history attributes the round to the right candidate.

// internal/relay/ledger.go
func Gates(rt Runtime) []ledger.Gate          // existing ledger gates + rolesMissingGates(rt), unchanged when rt.Roles == nil
func rolesMissingGates(rt Runtime) []ledger.Gate
    for each ref in rt.Candidates.Refs(): kind := harness of ref; if rt.Roles.Missing(kind) non-empty ->
        Gate{Token: ref, Kind: ledger.RolesMissing, Since: rt.Now(), Note: "roles missing: " + strings.Join(paths, ", ") + "; run relay agent install --kind " + kind, Source: "relay"}
    cache per Gates call only (call Missing once per distinct kind, not per candidate)
GateKindText(ledger.RolesMissing) = "roles missing"

// internal/relay/candidate.go  resolveCandidate
    explicit token: if any gate for that token has Kind == ledger.RolesMissing -> return Resolution{}, fmt.Errorf("%s: %s", token, gate.Note)
    (other explicit-pick gates keep today's record-but-proceed behaviour)
    ranked walk: unchanged -- skipsFor already skips any gated token, and skipText renders "(roles missing <until>)" via GateKindText; make sure a zero Until renders as "until cleared" or empty, whichever GateUntilText does today.
```

## 5. Pseudocode

### serve startup (cmd/relay/serve.go cmdServeRun, after candidates are loaded)
```
roles := harness.OSRoleChecker()
for each kind in distinct harness kinds of the candidate set:
    if missing := roles.Missing(kind); len(missing) > 0:
        slog.Warn("candidate roles missing; those candidates will be skipped", "harness", kind, "missing", missing, "fix", "relay agent install --kind "+kind)
    else: slog.Info("roles present", "harness", kind)
cfg.Roles = roles; cfg.StartedAt = time.Now()
```

### local daemon (cmd/relay/main.go cmdDaemon)
```
rt.StartedAt = time.Now()   // right after newRuntime(); nowhere else -- a CLI one-shot must not relaunch anything
```

## 6. Error handling

- `haltBinding`'s notify error is still returned (unchanged); the halt text
  is set before the notify so a notify failure never loses the reason.
- `CodeRoundHalted` is a 409 like the other "not now" answers; the client
  prints the server's message. An old client against a new server sees the
  409 as `"<server message>"` through the generic 4xx path at
  `remote.go:369-390` -- confirm that path prints the message and does not
  crash on an unknown code.
- Relaunch failure after a restart halts with a message naming both facts
  (lost + could not relaunch); nothing is charged.
- `rt.Roles.Missing` never errors: an unreadable file is "missing" for the
  purpose of picking (it would fail at launch anyway).

## 7. Ordered implementation steps

Commit prefix for every commit in this round: `fix(...)`. Never `feat:`.

### Task 1 -- the halt always says why; a send resets the round's budget (#250 item 2)

**Files:** `internal/relay/reconcile.go`, `send.go`, `switch_test.go`, `headless_test.go`.

**Tests first**
- `TestExhaustionAfterResendStillSaysWhy` (`switch_test.go`, next to
  `TestExhaustionHalts` ~404): drive a binding to the exhaustion halt as
  that test does; assert `Halt` contains `max_switches`. Then `Send` the
  same binding again (same round; the fixture's `Send` helper -- see
  `sentHeadless` `headless_test.go:716` for the headless shape, or the pane
  shape `TestExhaustionHalts` uses), assert `Halt == ""`,
  `HaltNotifiedRound == 0`, `RoundSwitches == 0`, `RoundExcluded == nil`,
  `State == active`. Then set `RoundSwitches` to the limit again and drive
  one more exhaustion halt: `Halt` contains `max_switches` again (this is
  the assertion that fails today), and the notifier received a **second**
  notice for this round. **Mutation check:** move the `b.Halt =` line back
  inside the guard and this must fail.
- `TestSendResetsRoundBudget` (`headless_test.go`): a sent headless binding
  with `RoundSwitches: 1, RoundExcluded: ["x/y/z"], HaltNotifiedRound: 1`
  after `Send` has all three zero/nil.
- Existing `TestExhaustionHalts` (one notice, dedup on next tick) and
  `TestReconcileHeadlessExitHaltsAfterMaxSwitches` must still pass; add
  `Halt != ""` to the latter's assertions.

**Then** the code per §4. **Verify:** `go test -race -count=1 ./internal/relay/`.
Commit: `fix(relay): a halt always records its reason; a re-send resets the round's switch budget (#250)`.

### Task 2 -- a re-send that cannot start is a 409, not a 201 (#250 items 1 and 3)

**Files:** `internal/remote/proto.go`, `internal/serve/rounds.go`, `serve_test.go`,
`internal/relay/remote.go`, `remote_test.go`.

**Tests first**
- `TestRoundResendThatCannotStartIs409` (`internal/serve`): build on
  `TestRoundResendSamePlanIs200` (~1521) / `TestRoundStartAbsorbsAndChecksOut`
  (~1291). Use a runner whose `Start` returns an error (a variant of
  `scriptRunner` with a `startErr` field). The first start -> the server's
  `Send` fails at `startRound`, binding is `needs_you` with
  `Halt = "builder spawn failed: ..."`; assert the response is **409** with
  code `round_halted` and a message containing `spawn failed`. Assert the
  server-side binding has no plan entry for that round. **Mutation check:**
  restore the 201 branch and this must fail.
- `TestSendRemoteHaltedIsAnError` (`internal/relay`): `fakeRemote.startRoundResp`
  = a view with `RoundState: RoundNeedsYou, Halt: "builder spawn failed: boom"`
  -> `sendRemote` returns an error containing `could not start` and `boom`;
  the binding on disk is unchanged (no plan entry, `State` as before,
  `LastShipped` unchanged). Second case: `startRoundErr` = a
  `client.StatusError` (or whatever typed error `sendRemote` matches codes
  on -- read `remote.go:369-390`) with code `CodeRoundHalted` and message
  `already switched 2 time(s)` -> same error shape.

**Then** the code per §4. **Verify:** `go test -race -count=1 ./internal/serve/ ./internal/relay/ ./internal/remote/...`.
Commit: `fix(serve): a re-send whose round cannot start is refused with the halt text, and the client says so (#250)`.

### Task 3 -- a builder lost to a daemon restart is relaunched, not switched (#244)

**Files:** `internal/relay/herdr.go`, `headless.go`, `headless_test.go`,
`internal/serve/serve.go`, `cmd/relay/main.go`, `cmd/relay/serve.go`.

**Tests first**
- `TestReconcileHeadlessLostToDaemonRestartRelaunches`: build on
  `TestReconcileHeadlessExitUnknownCode` (~1059). Set
  `rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0)` (the daemon started
  after the builder). Script `Alive=false`, no `exit()`. After one tick:
  `len(fr.specs) == 2` and the second spec's `Argv` equals the first's
  (same candidate, same prompt file / args -- compare what `startRound`
  puts in `ProcSpec`), `RoundSwitches == 0`, `RoundExcluded` empty,
  `State == active`, `Builder.PID == the new handle's pid`, the log has a
  `KindExit` entry followed by a `KindSwitch` entry whose note contains
  `lost to a daemon restart` and `picked <candidate> for builder`. A second
  tick with the new pid alive does nothing. **Mutation check:** drop the
  `Before(rt.StartedAt)` condition (always false) and this must fail.
- `TestReconcileHeadlessUnknownExitBeforeDaemonStartStillSwitches`: same,
  but `rt.StartedAt = time.Unix(b.Builder.StartedAt-60, 0)` (builder started
  after the daemon: a real death) -> today's behaviour: counted switch,
  candidate excluded. And with `rt.StartedAt` zero -> today's behaviour.
- Relaunch failure: `fr.startErr` set for the second start -> `needs_you`,
  `Halt` contains `lost to a daemon restart` and `could not be relaunched`,
  `RoundSwitches == 0`.

**Then** the code per §4/§5. `cmd/relay`: `cmdDaemon` sets `rt.StartedAt`;
`cmdServeRun` sets `cfg.StartedAt`; `serve.runtimeAt` copies it. No
`cmd/relay` test (the daemon path reaches herdr; the rule is tested in
`internal/relay`).

**Verify:** `go test -race -count=1 ./internal/relay/ ./internal/serve/ && go build ./...`.
Commit: `fix(headless): a builder whose supervisor died with the daemon is relaunched on the same candidate, not switched (#244)`.

### Task 4 -- missing role files gate the candidate (#238)

**Files:** `internal/ledger/ledger.go`, `internal/harness/roles.go`, `roles_test.go`,
`internal/relay/herdr.go`, `ledger.go`, `candidate.go`, `candidate_test.go`,
`internal/serve/serve.go`, `cmd/relay/main.go`, `cmd/relay/serve.go`.

**Tests first**
- `TestMissingDefinitions` (`internal/harness`): with the fake `InstallEnv`
  the install tests use (`install_test.go`), kind `opencode` with only
  `plan-executor` present -> `[".config/opencode/agents/researcher.md"]`;
  both present -> nil; unknown kind -> nil.
- `TestRolesMissingSkipsInOrder` (`internal/relay/candidate_test.go`): two
  candidates in order, `rt.Roles` = a fake checker whose `Missing` returns a
  path for the first's kind; `resolveCandidate(..., Gates(rt), "", "builder")`
  picks the second, and `ExplainResolution` mentions
  `skipped <first> (roles missing`. With `rt.Roles == nil` the first is picked
  (existing behaviour; this is the mutation check's control).
- `TestRolesMissingRefusesExplicit`: explicit token for the gated kind ->
  error containing `roles missing` and `relay agent install --kind`.
- `TestGateKindTextRolesMissing`: `"roles missing"`.

**Then** the code per §4/§5. Wire `Roles` in `cmd/relay/main.go` `newRuntime`
(so `relay add`, `bind`, `policy`, `candidates`, `status` all see the gate)
and in `cmdServeRun` with the startup log. `internal/serve/serve.go`
`runtimeAt` copies `cfg.Roles`.

**Verify:** `go test -race -count=1 ./internal/harness/ ./internal/relay/ ./internal/serve/ && go build ./...`.
Commit: `fix(pick): a candidate whose harness role files are missing is gated, refused when named explicitly, and logged at serve startup (#238)`.

### Task 5 -- README, plan copy, gate

README: in the section on switching / `max_switches` add one sentence: a
re-send resets the round's switch budget and the halt always states its
reason; in the headless section: a builder whose supervisor died with the
daemon (a systemd restart) is relaunched on the same candidate and not
counted as a switch; in the candidates/doctor section: a candidate whose
role files are missing is skipped (`roles missing` in `relay policy`) and
refused when named with `--builder`. Find the sections with
`grep -n "max_switches\|## Headless\|relay policy" README.md`.

Copy the plan file you were handed to `docs/plans/2026-09-21-w1b-switch-budget.md`
and commit it (`chore(plans): record wave 1 batch B plan`).

Gate, in the foreground, in this order; stop at the first failure and report it:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/ ./internal/serve/ ./internal/harness/ ./internal/ledger/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```
Do not run `make check` or `make e2e` (planner runs them; `make e2e`
covers `reconcile.go`, which this round touches).

## Report

Per task: what was done, the test names, the verify result, the mutation
check's outcome (name the failing test). The §2 grep outputs. Then the
commit shas and the gate output's last lines. If any step was impossible
as written, say which and stop there.
