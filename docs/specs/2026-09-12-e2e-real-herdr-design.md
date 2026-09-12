# End-to-end test on real herdr: one relay round with a scripted builder

**Issue:** #114, recommended step 2 ("one end-to-end test on real herdr
covering nudge, quiescence, and scrape with a real builder"). The spike that
proved this feasible is recorded on the issue (2026-09-12).
**Depends on:** #117 (completion marker). The cases below are written against
the marker-era fallback path.
**Amends:** `Makefile` (new `e2e` target); README "Contributing" (how to run
it); CLAUDE.md "Verifying a builder's work" (one sentence: `make e2e` exists,
is local-only, and is not part of `make check`).

## 1. System overview

Every test relay has runs against a fake herdr that returns a constant screen.
The two heuristics the fake cannot exercise -- screen-fingerprint quiescence
and the terminal scrape -- were the audit's highest-risk rows. Since #117 they
are the fallback for a builder that never writes its marker rather than the
main loop, but they still decide what the planner receives when a builder
misbehaves, and nothing has ever run them against a real pane.

This design adds one Go test, behind a build tag, that runs a full relay
round through the real `herdr.Client` on a private, detached herdr session:
`Bind` opens a real tab and starts a real agent; `Send` really types the
prompt; `Reconcile` reads real agent status and real screens; the report is
really delivered to a planner pane. The "agents" are two six-line shell
scripts that herdr accepts as `agy` and `claude` (verified: herdr's
detection manifest reports a known-kind process as `idle` by default), so
every run is deterministic and free. Only the clock is fake: `rt.Now` is a
`fakeClock`, so `startGrace` (30s) and `nudgeGrace` (60s) cost nothing while
every herdr call is real.

The test is **local only**. CI runners have no herdr and this design does not
try to change that. `make check` does not run it; `make e2e` does.

### Scope boundary

Pane builders and the pane fallback path only. Blocked-dialog detection (the
shim has no permission prompt), headless builders (never touch herdr),
mid-round switching, fork, consults and the TUI are out. The test asserts
relay's behaviour, never herdr's: it does not read `herdr-server.log` for
assertions and does not test herdr's detection rules beyond relying on the
one fallback the spike observed.

## 2. File structure

```
internal/relay/e2e_test.go          //go:build e2e -- the fixture and TestE2E
internal/relay/testdata/e2e-shim.sh the scripted agent, copied to <tmp>/shim/{agy,claude}
Makefile                            e2e: go test -tags e2e -count=1 -run TestE2E ./internal/relay -v
README.md, CLAUDE.md                amends in the header
```

`e2e_test.go` is in package `relay` so it can reuse `fakeClock`, `touch`,
`reconcile`-style helpers and read unexported constants (`startGrace`,
`nudgeGrace`, `nudgeNote`, `scrapeLines`) rather than duplicating them. Under
the default build (no tag) the file does not exist to the compiler, so
`go test ./...` and `make check` are unaffected.

## 3. Data structures and type definitions

### 3.1 `e2eSession` (test-only)

```
type e2eSession struct {
    name     string   // "relay-e2e-<pid>"
    dir      string   // ~/.config/herdr/sessions/<name>
    socket   string   // <dir>/herdr.sock
    shimDir  string   // <t.TempDir()>/shim, holds agy and claude
    herdr    *herdr.Client
}
```

Lifecycle: `startSession(t) *e2eSession` launches, polls, registers cleanup.
`(*e2eSession).startShim(t, name, kind, cwd) (paneID string)` creates a tab
and starts an agent in it. `(*e2eSession).screen(t, target) string` is
`agent read --source recent-unwrapped --lines scrapeLines`.
`(*e2eSession).waitScreen(t, target, substr)` polls `screen` until it
contains `substr`, bounded at 5s real time.

### 3.2 The shim (`testdata/e2e-shim.sh`)

```sh
#!/bin/sh
# relay e2e agent shim: herdr reports a known-kind process as idle by
# default, so this only has to exist, echo, and never write a file.
echo "shim ready"
while IFS= read -r line; do
  echo "you said: $line"
  echo "I implemented the guard clause but could not write the file."
done
```

Argv is ignored (`--agent plan-executor`, `--dangerously-skip-permissions`
and any extra args from the candidate are accepted silently). Exit on EOF.

### 3.3 Candidate set

One entry, written to a temp `candidates.json`:

```
[{"harness":"agy","provider":"e2e","model":"shim","roles":["builder"]}]
```

Candidate ref `agy/e2e/shim`. Kind `agy` selects the shim named `agy` on the
session's PATH.

### 3.4 Runtime

```
Runtime{
    Herdr:       herdr.NewClient("herdr", 30*time.Second),
    Store:       store.New(t.TempDir()),
    Candidates:  the set above,
    LedgerPath:  <tmp>/ledger.json,
    HistoryPath: <tmp>/history.json,
    Now:         clock.Now,          // *fakeClock starting at baseTime
    Runner:      nil, Policy: zero, Hooks: nil, NewID: nil,
}
```

Nothing in the runtime points at the user's `~/.local/state/relay` or
`~/.config/relay`.

### 3.5 Clock discipline

`T` is `baseTime`. Each `reconcileAt(t, rt, clock, T+d, b)` sets the clock
then runs one `Reconcile` inside `rt.Store.WithLock`, passing a fresh
`rt.Herdr.ListAgents(ctx)` as `agents`, saves, and returns the binding.
`Send` stamps `RoundStartedAt` from the same clock, so grace arithmetic is
exact.

## 4. Interface definitions and component contracts

### 4.1 `startSession`

Preconditions: none. Postconditions: a session whose `agent list` answers;
`HERDR_SOCKET_PATH` set for the test process via `t.Setenv`; cleanup
registered. Behaviour:

```
if herdr not on PATH:                       t.Skip("herdr not installed")
name := "relay-e2e-" + pid
cmd := exec.Command("herdr", "--session", name)
cmd.Stdin = /dev/null; cmd.Stdout/Stderr = a temp log
cmd.Env = os.Environ() minus every HERDR_*  plus SHELL=/bin/sh, PATH=<shimDir>:$PATH
cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
start; the client process exits on "Not a tty" -- that is expected; the server it spawned lives on
poll every 200ms up to 10s: HERDR_SOCKET_PATH=<socket> herdr agent list succeeds
    on timeout: t.Skip("herdr session did not answer: <last error>")
t.Setenv("HERDR_SOCKET_PATH", socket)
t.Cleanup:
    if RELAY_E2E_KEEP=1: log the socket path and return
    herdr server stop; herdr session delete <name>
    on test failure: print the last 40 lines of <dir>/herdr-server.log
```

`SHELL=/bin/sh` is load-bearing: a login bash re-sources `/etc/profile` and
drops the shim directory from PATH, so `agent start --kind agy` launches the
real Antigravity CLI (observed in the spike).

### 4.2 `startShim(t, name, kind, cwd) paneID`

```
resp := herdr tab create --cwd <cwd> --no-focus      -> root_pane.pane_id
herdr agent start <name> --kind <kind> --pane <paneID> --timeout 15000
    must report agent_status "idle", interactive_ready true; else t.Fatal
waitScreen(paneID, "shim ready")
return paneID
```

Used directly for the planner (`kind: claude`). The builder is started by
`Bind`, not by this helper; `Bind` goes through the same herdr verbs.

### 4.3 `bindBuilder(t, rt, s, name, plannerPane) store.Binding`

```
repo := t.TempDir(); git init there (real git, as existing tests do)
b := Bind(ctx, rt, BindOptions{Name: name, Candidate: "agy/e2e/shim",
                              PlannerPane: plannerPane, CWD: repo})
waitScreen(b.Builder.PaneID, "shim ready")
return rt.Store.Load(name)
```

Postconditions: `herdr agent list` shows an agent named `<name>-builder`,
kind `agy`, idle, in a tab relay created.

### 4.4 `reconcileAt(t, rt, clock, when, b) store.Binding`

As §3.5. Errors from `Reconcile` are fatal with the tick's `when`.

## 5. Cases (`TestE2E` subtests, in order)

One session and one planner for the whole test; each case binds a fresh
builder under its own binding name. `T` = `baseTime`.

**5.1 `marker_closes_round`** -- the main loop, end to end.

```
b := bindBuilder("marker")
Send(rt, "marker", planfile)                     -- clock at T
waitScreen(builder, "Round 1 from the planner")  -- Send really typed it
write ReportPath("marker",1) = "builder's words"; touch DonePath("marker",1)
b = reconcileAt(T+5s, b)
assert b.Round == 2
assert PendingForPlanner("marker") reports nothing pending   -- deliverAndSettle ran on the same tick:
                                                               the planner shim is idle and unfocused
waitScreen(planner, "you said: Builder finished round 1")   -- delivery landed in a real pane
assert the report entry's Note == ""
```

**5.2 `idle_without_marker_nudges_once`**

```
b := bindBuilder("nudge"); Send
b = reconcileAt(T+5s, b)            -- inside startGrace
assert b.Round == 1; builder screen does NOT contain "You went idle"
b = reconcileAt(T+35s, b)           -- past startGrace, builder idle, no marker
waitScreen(builder, "You went idle without finishing")
assert screen contains ReportPath and DonePath for round 1
assert exactly one log entry with Note == nudgeNote for round 1
assert b.BuilderScreen != "" and b.BuilderScreenAt == T+35s
```

**5.3 `still_screen_closes_unmarked`** -- continues 5.2's binding.

```
write ReportPath("nudge",1) = "the builder's own words"   -- no marker
b = reconcileAt(T+36s, b)
assert b.Round == 1                 -- 1s is inside nudgeGrace whether or not the shim's echo
                                       had landed before the nudge-time fingerprint
b = reconcileAt(T+100s, b)          -- > nudgeGrace since the last change, screen still
assert b.Round == 2; report entry Note == "unmarked"
assert payload contains "never confirmed completion (no 001-done)"
assert ReportPath body == "the builder's own words"   -- not scraped over
```

**5.4 `still_screen_scrapes`**

```
b := bindBuilder("scrape"); Send
reconcileAt(T+35s)                  -- nudge
reconcileAt(T+36s)                  -- fingerprint after the echo
b = reconcileAt(T+100s, b)
assert b.Round == 2; Note == "scraped"
body := ReportPath("scrape",1); assert HasPrefix(body, "<!-- SCRAPED")
assert body contains "I implemented the guard clause"   -- the real pane's text
```

**5.5 `screen_movement_resets_grace`**

```
b := bindBuilder("moving"); Send
reconcileAt(T+35s)                  -- nudge
reconcileAt(T+36s)                  -- fingerprint
herdr agent prompt <builder> "keep talking"   -- the shim echoes: screen changes
waitScreen(builder, "you said: keep talking")
b = reconcileAt(T+95s, b)
assert b.Round == 1                 -- moved since the fingerprint: not quiescent
b = reconcileAt(T+160s, b)
assert b.Round == 2; Note == "scraped"    -- still for nudgeGrace after the move
```

## 6. Error handling strategy

- Skip, never fail, on: `herdr` missing from PATH; the session not answering
  within 10s. Both print the reason. Everything after bootstrap is a failure.
- A herdr error mid-case fails with the verb, target and error envelope
  (the client already wraps these); cleanup still runs.
- `waitScreen` bounds every real-time wait at 5s and fails with the last
  screen it saw, so a hung shim or a wrong pane id is diagnosable.
- `RELAY_E2E_KEEP=1` skips teardown and logs `HERDR_SOCKET_PATH=<socket>`
  so a human can `herdr --session <name>` and look.
- The test never modifies anything under `~/.config/relay`,
  `~/.local/state/relay`, or the user's default herdr session. The only
  shared resource is `~/.config/herdr/sessions/<name>`, created and deleted
  by the test.

## 7. Ordered implementation steps

Each step is one plan task. No task adds a test to `cmd/relay`; the whole
file is tag-gated and never runs in CI. Verification for every step is
`make e2e` on a machine with herdr 0.9.x, plus `make check` to prove the
default build is untouched.

1. **Shim + session fixture + smoke.** `testdata/e2e-shim.sh`, `e2eSession`
   with `startSession`, `startShim`, `screen`, `waitScreen`, cleanup;
   `TestE2E/smoke` starts the planner shim and asserts `ListAgents` shows it
   as kind `claude`, status `idle`. Mutation: remove `SHELL=/bin/sh` from the
   launch env -> smoke fails (real agent or timeout).
2. **Runtime + `bindBuilder` + case 5.1.** Mutation: drop the `touch` ->
   `Round == 1`.
3. **Cases 5.2 and 5.3.** Mutations: nudge tick at `T+20s` -> no nudge
   (start grace holds); 5.3's second tick at `T+50s` -> `Round == 1`
   (nudge grace holds).
4. **Cases 5.4 and 5.5.** Mutation: skip the extra `agent prompt` in 5.5 ->
   the `T+95s` tick closes the round.
5. **`make e2e`, README, CLAUDE.md; comment on #114.** The Makefile target
   is `go test -tags e2e -count=1 -run TestE2E ./internal/relay -v`. Also
   verify `go vet -tags e2e ./internal/relay` is clean, since `make check`'s
   vet never sees the file.
