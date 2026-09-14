# Wait and waiting-on-you: block until a round needs the planner, and name the bindings it forgot

**Issues:** #127 (`relay wait`), #128 (blind-turn guard). One spec because
both need the same fact -- "this binding is waiting on a human, for this
reason, since then" -- and today relay does not record it.
**Depends on:** nothing open. Builds on the completion marker (#117: the
report notes `wait` classifies on), `DiagnoseBuilder` (#20), `relay pull`
(the consume path), `AgeText` (#122).
**Consumed by (later):** #133 (a structured report tail would add an exit 5
for a builder-declared `blocked`/`failed`), #124 (the push side of the same
event), #135 (stall labels would become a fifth cause).
**Status:** draft; plan at `docs/plans/2026-09-14-wait-and-waiting-on-you.md`.

## 1. System overview

Nothing in relay blocks. A planner driving a headless round spends its
turns on `until relay status | grep ...; do sleep 10; done`, and a script
has nothing better. And when a planner runs several builders, the common
shape of a forgotten dialog is: the question was delivered once, the
planner moved on to `relay send --name other`, and the blocked builder
sits until someone runs `relay status`. No mutating verb mentions it.

This design adds one fact and two readers of it:

- **The fact.** A binding that is NEEDS YOU can say *what it is waiting on
  and since when*. Blocked already can (the round's question file and its
  log entry); broken already can (`DiagnoseBuilder`, `BuilderMissingSince`);
  a halt cannot -- `haltBinding` toasts its reason and forgets it. Two
  fields on the binding, `Halt` and `HaltAt`, written at the single halt
  site, fix that. One pure classifier, `WaitingOn`, turns a binding plus
  its log into a `Waiting` value or nothing.
- **`relay wait`** (#127) polls the store -- never herdr -- and returns
  when the round closed (exit 0 on the marker, 2 without it), the binding
  needs a human (3, with the `Waiting` line on stdout), the binding is DONE
  or gone (4), or `--timeout` elapsed (124). `--any` waits on several.
- **Waiting-on-you lines** (#128). After any mutating verb succeeds, one
  stderr line per *other* binding that is waiting on a human, from the
  same classifier, with the verb that fixes it. Exit code unchanged.

### Decisions taken on the issues' open questions

1. **Delivery while the planner waits: leave it (#127 q1).** `wait` reads
   the store only. A pane planner that does not want the report typed
   afterwards runs `relay wait N && relay pull N`; `pull` already claims
   the payload without delivering it, so `--consume` would be a second
   spelling of an existing verb. No flag.
2. **Exit 3 is `needs_you`, `broken`, `orphaned`; HELD is not (#127 q2).**
   A held binding's round is closed and its report entry exists, so `wait`
   returns 0/2 on it -- the human's next action is to read the report,
   which is what 0/2 means.
3. **Lines only, exit 0, no policy knob (#128).** The planner reads the
   verb's stderr in its tool result, so a line is not ignored the way it
   is for a human at a shell. `blind_turn: refuse` is deferred until a
   forgotten dialog happens *with* the lines in place. No `--quiet`
   either: a script that wants silence has `2>/dev/null`, and adding a
   flag to seven verbs for an unproven need is the wrong trade.
4. **The halt is stored on the binding, not derived.** Two fields, written
   where the halt happens. A generic `StateSince` would touch every state
   write and still not say why; re-deriving the reason from
   `RoundSwitches` and the budget is fragile when both hold and has no
   answer for "cannot switch: no candidate".
5. **`broken` counts only when the daemon will not fix it itself.** A
   switchable broken binding (`BuilderCandidate != "" &&
   !RoundStartedAt.IsZero()`) is transient: after `switchGrace` the daemon
   switches it or halts it into `needs_you`, which then counts. Listing it
   for those thirty seconds would tell the planner to act on something
   relay is about to act on.
6. **#128's "unobserved" secondary is dropped.** It needs a `seen` stamp
   written by every read verb; the issue said "only if cheap" and it is
   not.
7. **`wait`'s default round is the newest round with a plan entry**, not
   `b.Round`. After a close `b.Round` is already the next, unsent round;
   defaulting to it would wait on nothing until the timeout. The newest
   planned round is the open one while it is open and the last closed one
   after, which is exactly "return immediately if it already closed".
8. **The command-surface freeze (#114 step 1) yields for `wait`.** It is a
   read verb, and `statusline` already set the precedent that a read verb
   for a host's benefit is not what the freeze was for.

### Scope boundary

Two fields on `store.Binding`; one write site and three clear sites; two
new files in `internal/relay` (`waiting.go`, `wait.go`); one new verb and
one helper in `cmd/relay/main.go` called from seven verbs; README. No
herdr call is added anywhere. No prompt text changes. `make e2e` is not
affected (nothing on the nudge, fingerprint or scrape path changes).

Out of scope: `--consume` (use `pull`); `blind_turn` policy; `--quiet`;
the `seen`/unobserved stamp; waiting on a consult; waiting for delivery
rather than close; pushing events (#124); progress output while waiting;
exit 5 from a structured report (#133); notifications (#4, #129).

## 2. File structure

```
internal/store/types.go             Binding.Halt, Binding.HaltAt (new, omitempty)
internal/relay/reconcile.go         haltBinding writes Halt/HaltAt; queueReport's close clears them
internal/relay/send.go              headless spawn failure writes Halt/HaltAt; success clears them
internal/relay/bind.go              --resume --rebind clears them
internal/relay/waiting.go           Waiting, WaitingOn, WaitingLine, WaitingOnYou, questionFirstLine (new)
internal/relay/waiting_test.go      classifier table; line rendering; WaitingOnYou over a seeded store
internal/relay/wait.go              WaitResult, exit constants, WaitOutcome, DefaultWaitRound, Wait (new)
internal/relay/wait_test.go         outcome table; default-round rule; Wait loop with a fake clock/short interval
internal/relay/reconcile_blocked_test.go  halt writes Halt/HaltAt once; close clears
internal/relay/send_test.go (or headless_test.go)  spawn failure writes Halt; resend clears
cmd/relay/main.go                   cmdWait; warnWaitingOnYou; usage line; seven call sites
README.md                           `relay wait` in the command surface; a paragraph under "Running several builders at once"
```

## 3. Data structures

### 3.1 `store.Binding.Halt string` (`json:"halt,omitempty"`), `HaltAt time.Time` (`json:"halt_at,omitempty"`)

Why the binding is NEEDS YOU when neither a dialog nor a missing pane
explains it, and when relay decided so. `Halt` is the sentence
`haltBinding` already composes for the toast, minus the leading
`<name>: ` prefix (the reader knows the name): `round 4 has run past
2h0m0s`, `builder gone for 30s (agy/google/x); already switched 2 time(s)
this round (max_switches 2)`, `builder gone for 30s (agy/google/x); cannot
switch: <err>`, and from `Send`, `builder spawn failed: <err>`.

Meaningful only while `State == needs_you`; readers check the state first,
so a stale value is harmless. Hygiene clears it anyway at the three points
where a halted round stops being the current fact: round close
(`queueReport`, where `HaltNotifiedRound` is zeroed), `bind --resume
--rebind` (same place), and a successful `Send` (which sets
`State = active` for the round it just handed over). A binding written
before this field existed has `Halt == ""`; the classifier reports that
honestly (§4.1, cause `needs you`).

### 3.2 `relay.Waiting`

```
type Waiting struct {
    Name  string     // binding
    Round int        // b.Round as stored (the open round for blocked/halted; the next for a closed-round broken)
    Cause string     // "blocked" | "halted" | "broken" | "orphaned" | "needs you"
    Line  string     // what it is waiting on, one line, <= 120 runes, never empty
    Since time.Time  // when it started waiting; zero when relay does not know
    Hint  string     // the relay verb that resolves it, e.g. `relay answer --name api`
}
```

### 3.3 `relay.WaitResult`

```
type WaitResult struct {
    Code int     // one of the Wait* exit constants below; meaningful only when Done
    Line string  // stdout line: report path, "-" (noreport), or the Waiting line
    Done bool    // false while the round is open and nothing needs a human
}

const (
    WaitClosed    = 0    // report entry for the round with note ""
    WaitUnmarked  = 2    // report entry with note unmarked | scraped | noreport
    WaitNeedsYou  = 3    // WaitingOn returned a Waiting
    WaitGone      = 4    // binding DONE, or ErrNotFound after it was found once
    WaitTimeout   = 124  // --timeout elapsed
)
```

Exit 1 is not a `WaitResult`: it is an ordinary error from `cmd/relay`
(usage, a name that was never found, a store read failure).

### 3.4 `relay.WaitOptions`

```
type WaitOptions struct {
    Names    []string      // one name, or several for --any; len >= 1
    Round    int           // 0 = DefaultWaitRound per binding, resolved once at start
    Timeout  time.Duration // > 0; the CLI defaults 10m
    Interval time.Duration // poll period; the CLI passes 1s; tests pass something small
}
```

## 4. Contracts

### 4.1 `WaitingOn(b store.Binding, entries []store.LogEntry, questionOf func(name string, round int) string) (Waiting, bool)`

Pure apart from `questionOf`, which the caller supplies (production:
`questionFirstLine` reads the file; tests: a map). Returns `ok=false` for
every state except `needs_you`, `broken`, `orphaned`, and for a switchable
`broken` (decision 5). Otherwise, in this order:

| state | condition | Cause | Line | Since | Hint |
|---|---|---|---|---|---|
| `needs_you` | a `to_planner`/`question` entry exists for `b.Round` | `blocked` | `questionOf(name, round)`; if that is `""`, `dialog captured at <QuestionPath>` | that entry's `TS` | `relay answer --name <n>` |
| `needs_you` | else `b.Halt != ""` | `halted` | `b.Halt` | `b.HaltAt` | `relay status --name <n>` |
| `needs_you` | else | `needs you` | `no reason recorded (binding predates the halt record)` | zero | `relay status --name <n>` |
| `broken` | `!switchable` | `broken` | `DiagnoseBuilder(b).Detail(b.Round)` | `b.BuilderMissingSince` | `relay bind --resume --name <n> --rebind` when `Identified`, else `relay status --name <n>` |
| `orphaned` | -- | `orphaned` | `planner pane is gone` | zero | `relay bind --resume --name <n>` |

`Line` is trimmed and capped at 120 runes with `…`; the first non-blank
line only. The `blocked` row is checked before `halted` because a binding
can be both (a dialog appeared, then the budget ran out), and the dialog
is the thing the human can act on.

### 4.2 `questionFirstLine(rt Runtime) func(name string, round int) string`

Returns the first non-blank line of `rt.Store.QuestionPath(name, round)`,
trimmed; `""` when the file is missing or unreadable (never an error --
the classifier has a fallback). The one place `WaitingOn`'s callers
touch the filesystem beyond the store.

### 4.3 `WaitingLine(w Waiting, now time.Time) string`

Pure. Renders:

```
waiting on you: <name> round <N> <cause>[ <age>] -- <line>  (<hint>)
```

`<age>` is `AgeText(now.Sub(w.Since))`, omitted (with its space) when
`Since` is zero. Examples:

```
waiting on you: e2e-herdr round 4 blocked 23m -- Do you want to proceed? > 1. Yes  (relay answer --name e2e-herdr)
waiting on you: spaceapi round 2 halted 1h 5m -- builder gone for 30s (agy/google/x); already switched 2 time(s) this round (max_switches 2)  (relay status --name spaceapi)
waiting on you: old round 3 orphaned -- planner pane is gone  (relay bind --resume --name old)
```

### 4.4 `WaitingOnYou(rt Runtime, except string) ([]string, error)`

Store-backed. `rt.Store.List()`, skip `except` and every DONE binding,
`ReadLog` each remaining one, `WaitingOn` with `questionFirstLine(rt)`,
`WaitingLine` with `rt.Now()`. Bindings in `List()` order. A `ReadLog`
error on one binding is returned (the caller prints it as a warning and
moves on -- §6). Never calls herdr.

### 4.5 `DefaultWaitRound(b store.Binding, entries []store.LogEntry) int`

Pure. The highest `Round` among `to_builder`/`plan` entries (nudges have
`Note == nudgeNote` and are excluded, as `HasEntry` excludes them); when
there is none, `b.Round`. Decision 7.

### 4.6 `WaitOutcome(b store.Binding, entries []store.LogEntry, round int, questionOf) WaitResult`

Pure apart from `questionOf`. Preconditions: `round >= 1`. In this order:

```
if e := report entry (to_planner/report) for round exists:
    code = WaitClosed if e.Note == "" else WaitUnmarked
    line = e.Path, or "-" when e.Note == "noreport"
    return {code, line, Done: true}
if b.State == done:
    return {WaitGone, "", true}
if w, ok := WaitingOn(b, entries, questionOf); ok:
    return {WaitNeedsYou, w.Line, true}
return {Done: false}
```

The report check runs first so `--round N` on an earlier round answers
from the log regardless of what the binding is doing now, and so a
binding that went broken *after* a clean close (the everyday "builder
exited after its round" case) still reports the close.

### 4.7 `Wait(ctx context.Context, rt Runtime, opts WaitOptions) (name string, res WaitResult, err error)`

The loop. On entry: load every name once; `ErrNotFound` here is an error
(exit 1 -- the caller named something that does not exist). Resolve each
binding's round: `opts.Round` if non-zero, else `DefaultWaitRound`. Then
until `ctx` is done or `opts.Timeout` elapses (measured with `rt.Now()`
so tests can drive it):

```
for each name in opts.Names order:
    b, err := Load(name)
    if errors.Is(err, store.ErrNotFound): return name, {WaitGone, "", true}, nil   -- unbound while waiting
    if err != nil: return name, {}, err
    entries, err := ReadLog(name); if err != nil: return name, {}, err
    if r := WaitOutcome(b, entries, round[name], questionOf); r.Done: return name, r, nil
sleep opts.Interval (or return on ctx)
```

The first `Done` in `Names` order on a given pass wins (`--any`). Timeout
returns `("", {WaitTimeout, "", true}, nil)`. `ctx` cancellation returns
`ctx.Err()`. The first pass runs before any sleep, so a round that closed
before `wait` started returns immediately.

### 4.8 `cmd/relay`: `relay wait [NAME|--name N] [--any N1 N2 ...] [--round R] [--timeout D]`

- Exactly one of: a single binding (positional, `--name`, or the cwd
  fallback like `send`/`pull`) or `--any` followed by one or more names
  (the remaining positionals). Both, or neither with no cwd binding, is a
  usage error (exit 1).
- `--timeout` default `10m`; `<= 0` is a usage error. `--round` default 0
  (§4.5); `< 0` is a usage error.
- Calls `Wait` with `Interval: time.Second`. Prints: for `--any`, the
  winning name on its own stdout line first (also for exit 4 when a named
  binding was unbound); then `res.Line` when non-empty. Exits with
  `res.Code` via `exitCodeErr` -- including 0, which is a plain `nil`
  return.
- Never touches herdr; does not call `newRuntime`'s herdr client. (It
  still uses `newRuntime` for the store and clock; the client is simply
  unused.)

### 4.9 `cmd/relay`: `warnWaitingOnYou(rt relay.Runtime, except string)`

Called as the last thing before `return nil` in `cmdSend`, `cmdFork`,
`cmdAdd`, `cmdAnswer`, `cmdDone`, `cmdUnbind` and `cmdBind`, after the
verb's own success output. `except` is the binding the verb acted on (for
`unbind`, the name it removed; `WaitingOnYou` skips a name it cannot
find anyway). Each returned line goes to stderr as is. An error from
`WaitingOnYou` is printed to stderr as `relay: waiting-on-you check: <err>`
and otherwise ignored: the verb succeeded and its exit code is not this
check's to change.

### 4.10 `haltBinding` and the clear sites

`haltBinding(ctx, rt, b, message)` keeps its signature. Inside the
existing `HaltNotifiedRound != b.Round` guard, after the notify, it sets
`b.Halt = strings.TrimPrefix(message, b.Name+": ")` and
`b.HaltAt = rt.Now().UTC()`. A halting tick that repeats (the guard
false) leaves both untouched, so the age does not restart every poll.

`Send`'s headless spawn-failure branch sets `b.Halt = "builder spawn
failed: " + err.Error()` and `b.HaltAt` before saving NEEDS YOU. `Send`'s
success path, `queueReport`'s close block, and `Bind`'s `rebinding` block
set both to their zero values.

## 5. Flows

### `relay wait api` on a headless round, in a planner's Bash tool

```
planner: relay send --name api --file plan.md      -> "sent round 3 to api's builder"
planner: relay wait api                            -> blocks (polls bind.json + log.jsonl every 1s)
daemon:  process exits with marker; queueReport writes report entry (note "")
wait:    next pass sees the entry               -> stdout ".../003-report.md", exit 0
planner: relay pull api                            -> report text; nothing typed later
```

Same round, builder halts on budget instead: `haltBinding` sets
`Halt="round 3 has run past 2h0m0s"`, `State=needs_you`; `wait` prints
`round 3 has run past 2h0m0s`, exit 3. Same round, planner ran
`relay done api` from another pane: exit 4. Ten minutes pass with nothing:
exit 124; the planner loops.

### `relay send --name b` while `a` is blocked

```
send succeeds -> stdout "sent round 2 to b's builder"
warnWaitingOnYou(rt, "b") -> stderr:
  waiting on you: a round 4 blocked 23m -- Do you want to proceed? > 1. Yes  (relay answer --name a)
exit 0
```

## 6. Error handling

| failure | behaviour |
|---|---|
| `wait` names a binding that does not exist | error before the loop; exit 1 |
| a named binding disappears mid-wait | exit 4 (its name printed first under `--any`) |
| store read error mid-wait | error; exit 1 |
| question file missing/unreadable | `Line` falls back to `dialog captured at <path>`; no error |
| `--timeout` elapses | exit 124, nothing on stdout |
| SIGINT during `wait` | `ctx.Err()`; exit 1 |
| `WaitingOnYou` fails inside a mutating verb | one stderr warning; the verb's own result and exit code stand |
| a binding predates `Halt` | cause `needs you`, honest line; never guessed |

A false "waiting on you" line costs one line of stderr; a missed one is
what exists today. Exit 3 misclassifying HELD as needs-you is prevented by
ordering (the report entry is checked first).

## 7. Observability

- `relay log` is unchanged: `wait` writes nothing.
- `Halt` and `HaltAt` are visible in `bind.json` and, through the
  `Waiting` line, in `wait`'s exit-3 output and every mutating verb's
  stderr. `relay status` is not changed by this spec (it already shows
  NEEDS YOU and the broken detail); showing `Halt` there is a one-line
  follow-up once the field exists.
- No new slog lines in the daemon; the halt already logs `binding halted`
  with the reason.

## 8. Testing

All in `internal/relay`, pure or over a `t.TempDir()` store with the
existing `fakeHerdr`; nothing under `cmd/relay`; nothing reaches herdr.

- `WaitingOn` table: active/held/done -> `!ok`; needs_you + question entry
  -> blocked with the question's first line, `Since` = entry TS, the
  answer hint; needs_you + question entry + `Halt` set -> still blocked
  (order); needs_you + `Halt` only -> halted with `HaltAt`; needs_you with
  neither -> `needs you`, zero `Since`; broken + switchable -> `!ok`;
  broken + adopted (`BuilderCandidate == ""`) -> broken, detail text,
  rebind hint when identified, status hint when not; broken + closed round
  -> broken; orphaned -> orphaned. A 200-rune dialog line is capped at 120
  with `…`; a question file whose first line is blank uses its second.
- `WaitingLine`: age present/absent; the exact three example strings in
  §4.3.
- `WaitingOnYou`: three bindings seeded (one blocked, one active, one
  DONE) plus `except` naming a fourth blocked one -> exactly one line, for
  the blocked one not excepted.
- `DefaultWaitRound`: no plan -> `b.Round`; open round -> that round; after
  close (`b.Round` = N+1, newest plan N) -> N; a nudge entry for N+1 does
  not count.
- `WaitOutcome`: note `""` -> 0 with path; `unmarked`/`scraped` -> 2 with
  path; `noreport` -> 2 with `-`; DONE without a report -> 4; needs_you ->
  3 with the `Waiting` line; active open round -> `!Done`; DONE *with* a
  report for the asked round -> 0 (report wins); broken after a clean
  close, asked for the closed round -> 0.
- `Wait`: with `Interval` 1ms and a `fakeClock`: already-closed round
  returns on the first pass without sleeping (assert the clock was not
  advanced past one interval); `--any` over `[a, b]` where only `b` closes
  returns `b`; a binding deleted between passes returns 4; timeout returns
  124 when the clock passes `Timeout`.
- `haltBinding`: `TestReconcileFlagsRoundTimeout` gains assertions that
  `Halt == "round 1 has run past 30m0s"` and `HaltAt` equals the clock at
  the halting tick, and that a further tick leaves `HaltAt` unchanged;
  `TestExhaustionHalts` (or the max_switches halt test) asserts `Halt`
  contains `max_switches`. Round close: after a marker close `Halt == ""`
  and `HaltAt.IsZero()`.
- `Send`: the existing headless spawn-failure test asserts `Halt` starts
  with `builder spawn failed:`; a following successful `Send` clears it.
- Mutations the plan names: drop the `blocked`-before-`halted` order
  (the both-set case fails); drop the switchable guard (the
  broken+switchable case fails); check `State == done` before the report
  entry in `WaitOutcome` (the DONE-with-report case fails); default
  `wait`'s round to `b.Round` (the after-close case fails); move the
  `Halt` write outside the `HaltNotifiedRound` guard (the unchanged
  `HaltAt` assertion fails).
