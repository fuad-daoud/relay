# Plan: agy planners get their reports pushed through agy's own inbox (#349)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. Halt in
particular if any of these turns out to be false:

- `DeliverPending` (`internal/relay/deliver.go`) calls
  `rt.Deliverers[b.Planner.Kind].Deliver(ctx, b.Planner, text, pending.Path, pending.TS)`
  under the state lock. It confirms the entry only on `OutcomeDelivered`, and
  it treats `OutcomeNotMine` and `OutcomeUnavailable` alike: the entry stays
  pending on route `pull`. This plan relies on exactly that contract, and on
  `b.Planner` being `recordEndpoint(rec)`, i.e. its `SessionID` is the planner
  record's `SessionID` (`internal/relay/bind.go`).
- `planner.Detect` (`internal/planner/ident.go`) is the only place that decides
  a caller's harness kind from its environment, and making it return an `agy`
  `Ident` breaks nothing that assumes "detected means claude". Check every
  caller: `cmd/relay/doctor.go` ~531 (it already guards `ident.Kind == "claude"`),
  `cmd/relay/planner.go` ~110 (`relay planner init` with no flags), and
  `planner.Resolve`.
- `plannerHostStart(0)` returns 0 (`cmd/relay/planner.go` ~209), so an agy
  `Ident` with `HostPID 0` passes `Record.Validate`'s rule "host_started_at
  is 0 whenever host_pid is 0".
- The planner registry's `list` (`internal/planner/registry.go` ~219) skips
  directories and dot-prefixed names, so `planners/.agy/` is invisible to it.
  The binding store's listing never walks `planners/`, just as today.

**Ground truth is the spike on #349** (the last comment on the issue). Do not
run `agy`, do not run `agy agentapi`, and do not read any real
`~/.gemini/antigravity-cli/brain/*/.system_generated/logs`: they contain a
secret. Every test uses fakes and fixtures shaped exactly like the spike's
observations.

## 1. System Overview

Since #303, a report reaches a planner through the MCP channel (Claude Code),
the `PlannerDeliverer` port (opencode only, #300), or route `pull`. An agy
planner falls to `pull`, so an idle agy session never learns that its builder
finished.

The spike found a native push path:

- `agy agentapi send-message --title=<t> <conversation_id> <content>` drops a
  message in that conversation's inbox, and an idle session takes a turn by
  itself.
- It needs `ANTIGRAVITY_LS_ADDRESS` (`localhost:<port>`) and
  `ANTIGRAVITY_CSRF_TOKEN`. Both rotate on every agy launch, neither is in the
  agy process's own environment or on disk, and agy injects them only into
  commands **it** runs, together with `ANTIGRAVITY_CONVERSATION_ID` and
  `ANTIGRAVITY_AGENTAPI_EXE`.
- Receipt shows up as
  `brain/<conv>/.system_generated/messages/<uuid>.json`, followed by
  `messages/read.json` gaining `{"<uuid>": true}`.

This change has three parts:

1. **Detect.** `planner.Detect` recognises an agy caller: a valid
   `ANTIGRAVITY_CONVERSATION_ID` gives `Ident{Kind: "agy", SessionID: <conv>,
   HostPID: 0}`. So `relay planner init` with no flags, run inside agy,
   registers the planner under its conversation id, and `planner.Resolve`'s
   session step finds that record without `RELAY_PLANNER`.
2. **Capture.** Every `relay` invocation whose environment carries all three
   `ANTIGRAVITY_*` values writes them to a 0600 credentials file keyed by
   conversation id: `<state root>/planners/.agy/<conv>.json`. It never prints
   or logs them, and rewrites the file only when the values changed. An agy
   planner runs relay constantly (`send`, `wait`, `pull`, `status`), so the
   file is fresh after every agy restart, as soon as the planner next touches
   relay.
3. **Deliver.** `AgyDeliverer` implements `PlannerDeliverer`, keyed `agy`. It
   reads the credentials for `planner.SessionID`, checks the inbox first so a
   retry never double-sends, runs `send-message` through an env-carrying exec
   seam (argv only, no shell, token only in env), then polls briefly for
   `read.json` to confirm. A stale token, a closed session or missing
   credentials give `OutcomeUnavailable`, and the entry stays on route `pull`.

Each delivered report costs the agy planner one model turn. That is intended:
it is what waking an idle planner means.

**Decisions already made. Do not revisit them:**

- **Where the token lives:** a separate file,
  `<root>/planners/.agy/<conversation_id>.json`, mode 0600, in a 0700
  directory. It is keyed by conversation id, **not** planner id, because
  `Deliver` receives only `b.Planner` (an `Endpoint` with `Kind` and
  `SessionID`), and the conversation id is that `SessionID`. The token never
  enters the planner record, `relay planner list`, `relay status`, any JSON
  output, any log line or any error string.
- **Which verbs capture:** all of them. Capture runs once at the top of
  `run()` in `cmd/relay/main.go`, before dispatch, and costs nothing when the
  env vars are absent. A per-verb list would miss whichever verb the planner
  happens to run after an agy restart. `relay wait` and `relay pull` are the
  commonest, and they get it for free.
- **No `get-conversation-metadata` probe** before sending. A stale token fails
  the send itself fast with a transport error, so a probe would add one exec
  per tick for no information.
- **agy is registered unconditionally** in `newDeliverers`. It does not depend
  on `sqlite3`; the opencode deliverer keeps its `sqlite3` gate.
- **Doctor:** out of scope.

## 2. File Structure

```
internal/planner/ident.go            MODIFY  Detect recognises agy (ANTIGRAVITY_CONVERSATION_ID)
internal/planner/resolve_test.go     MODIFY  Detect table rows for agy
internal/store/store.go              MODIFY  AgyCredsDir() = PlannersDir()/.agy
internal/relay/agy_creds.go          CREATE  AgyCreds type, CaptureAgyCreds (pure over env func), ReadAgyCreds, validConversationID
internal/relay/agy_creds_test.go     CREATE  capture/read/prune tests (temp dir)
internal/relay/deliver_agy.go        CREATE  AgyDeliverer, EnvExec seam, inbox scan, redaction
internal/relay/deliver_agy_test.go   CREATE  Deliver table tests with a fake EnvExec and a temp agy home
cmd/relay/exec.go                    MODIFY  binEnvExec (EnvExec over os/exec, env = os.Environ() + extra)
cmd/relay/main.go                    MODIFY  run(): best-effort capture before dispatch; newDeliverers registers "agy"
internal/harness/agents/architect.agy.md  MODIFY  one sentence in "Handing off" (see §3)
README.md                            MODIFY  "How reports arrive" (~2089-2094) + one agy paragraph
```

No other `architect.*` file changes. The `internal/harness` test at ~201 pins
only the section heading and three phrases, and the edit keeps all of them.

## 3. Data Structures & Type Definitions

### `relay.AgyCreds` (`internal/relay/agy_creds.go`)

The JSON file `<root>/planners/.agy/<conv>.json`:

| Field | JSON | Type | Meaning / constraint |
|---|---|---|---|
| `ConversationID` | `conversation_id` | string | `validConversationID` (lower-case 8-4-4-4-12 hex UUID). It equals the file's base name |
| `LSAddress` | `ls_address` | string | from `ANTIGRAVITY_LS_ADDRESS`, e.g. `localhost:42139`. It must pass `loopbackAgyAddress` at capture time, or nothing is written |
| `CSRFToken` | `csrf_token` | string | from `ANTIGRAVITY_CSRF_TOKEN`; non-empty, no whitespace. **Secret** |
| `AgentAPIExe` | `agentapi_exe` | string | from `ANTIGRAVITY_AGENTAPI_EXE`, an absolute path; "" when unset |
| `CapturedAt` | `captured_at` | time.Time | UTC capture time |

`AgyCreds` has a `String()`/`GoString()` that renders the token as
`<redacted>`, so an accidental `%v` cannot leak it.

### `relay.EnvExec` (`internal/relay/deliver_agy.go`)

```
type EnvExec interface {
    Run(ctx context.Context, extraEnv []string, bin string, args ...string) ([]byte, error)
}
```

`extraEnv` is `KEY=VALUE` pairs appended to the parent environment. There is
no shell. `cmd/relay/exec.go`'s `binEnvExec` implements it on top of
`exec.CommandContext`, with `cmd.Env = append(os.Environ(), extraEnv...)` and
stderr folded into the error the way `binExec` does.

### `relay.AgyDeliverer`

| Field | Type | Zero value means |
|---|---|---|
| `Exec` | `EnvExec` | nil → `OutcomeNotMine`, reason `no exec` |
| `CredsDir` | string | `Store.AgyCredsDir()`; "" → `OutcomeNotMine` |
| `Home` | string | agy's state root; "" → `~/.gemini/antigravity-cli` |
| `Now` | `func() time.Time` | `time.Now` |
| `FallbackAfter` | `time.Duration` | `DefaultFallbackAfter` (the existing 30s constant) |
| `ConfirmWindow` | `time.Duration` | `AgyConfirmWindow` = 3s |
| `ConfirmPoll` | `time.Duration` | `AgyConfirmPoll` = 250ms |

Constants: `AgyMaxContent = 100_000` bytes, which stays under Linux's
per-argument limit of 131072; `agyClockSkew = 2 * time.Second`.

### agy inbox shapes (read-only; from the spike)

- `messages/<uuid>.json`:
  `{"id","recipient","sender","priority","timestamp"(RFC3339Nano),"renderDetails":{"messageTitle"},"content"}`.
  Unmarshal only `id`, `recipient`, `timestamp` and `content`.
- `messages/read.json`: `map[string]bool`.
- `messages/undelivered/`: may hold message files in the same shape.

### Exact strings

| Where | Text |
|---|---|
| title arg | `--title=` + the origin line (`firstPayloadLine(payload)`), cut to 100 runes |
| oversize content | `<origin line>\n\nThe report is too long to push (<n> bytes). Read it: <path>` (when `path == ""`: `…too long to push (<n> bytes); run relay pull.`) |
| reason, not agy | `""` (NotMine) |
| reason, bad conversation id | `agy planner session is not a conversation id; run relay planner init inside agy` |
| reason, gave up | `agy push gave up after <FallbackAfter>` |
| reason, no creds | `no agy credentials for this conversation; run any relay command inside the agy session` |
| reason, non-loopback | `agy language server address is not loopback` |
| reason, sent not read | `sent to agy but not yet read` |
| reason, undelivered | `agy reports the message undelivered` |
| reason, send failed | `send-message: <first line of the error or of the JSON "error" field>`, with every occurrence of the token replaced by `<redacted>` |
| slog on delivery | `slog.Info("agy push delivered", "conversation", <conv>)`. Never any credential field |
| architect.agy.md, after the "Other harnesses run `relay wait` … in a loop …" sentence | `An agy planner registered from inside agy (relay planner init) is also woken by a pushed report, so for agy that loop is a fallback, not the only route.` |

## 4. Interface Definitions & Component Contracts

### `planner.Detect(env, ppid) (Ident, bool)`, extended

- Claude is checked first and is unchanged: `CLAUDECODE=1` wins even when
  `ANTIGRAVITY_*` is also set.
- Otherwise, if `env("ANTIGRAVITY_CONVERSATION_ID")` matches the UUID shape:
  `Ident{Kind: "agy", SessionID: <it>, HostPID: 0}, true`.
- Otherwise `false`. Update the doc comment, which currently says agy "has no
  deliverer yet".
- The UUID regexp lives in `planner`. `relay.validConversationID` uses its own
  copy of the same pattern, because `relay` may import `planner` but the
  pattern is two lines. Pin both with a table test.

### `relay.CaptureAgyCreds(env func(string) string, dir string, now time.Time) (bool, error)`

- Responsibility: persist the calling agy session's agentapi credentials.
  Returns whether it wrote.
- It writes nothing and returns `false, nil` unless all three of
  `ANTIGRAVITY_CONVERSATION_ID` (valid UUID), `ANTIGRAVITY_LS_ADDRESS`
  (loopback) and `ANTIGRAVITY_CSRF_TOKEN` (non-empty, no whitespace) are
  present.
- `MkdirAll(dir, 0700)`, then write `<conv>.json` with mode 0600 through
  temp-and-rename. It skips the write, returning `false, nil`, when the
  existing file has the same address, token and exe.
- Prune: after a write, delete any other `*.json` in `dir` whose
  `captured_at` is older than 7 days. Best effort; errors are ignored.
- Errors never contain the token. Wrap path errors with the path only.

### `relay.ReadAgyCreds(dir, conv string) (AgyCreds, error)`

Reads and validates. A missing file returns an `os.ErrNotExist`-wrapping
error.

### `(*AgyDeliverer).Deliver(ctx, planner store.Endpoint, payload, path string, queuedAt time.Time) (Outcome, string, error)`

The contract is `PlannerDeliverer`'s. It must never deliver twice, and it
must never put a credential in the reason. The order is in §5.

### `loopbackAgyAddress(addr string) bool`

The host part of `host:port` is `localhost` or `127.0.0.1`, and the port is
numeric.

### `cmd/relay` wiring

- `run(args)`: first line, `captureAgyEnv()`. It resolves
  `store.DefaultRoot()`, builds the dir as the store's `AgyCredsDir()` (or
  `filepath.Join(root, "planners", ".agy")` through a tiny `store.New(root)`
  if that is how other code gets store paths; follow the existing pattern),
  and calls `relay.CaptureAgyCreds(os.Getenv, dir, time.Now().UTC())`. It
  ignores every error and prints nothing, because capture must never fail a
  command.
- `newDeliverers()`: always includes
  `"agy": &relay.AgyDeliverer{Exec: binEnvExec{}, CredsDir: <root>/planners/.agy}`.
  `"opencode"` is added only when `sqlite3` is on PATH, as today. Update the
  doc comment.

## 5. High-Level Pseudocode: `AgyDeliverer.Deliver`

```
if planner.Kind != "agy"                              → NotMine, ""
if d.Exec == nil or d.CredsDir == ""                  → NotMine, "no exec"
conv = planner.SessionID
if !validConversationID(conv)                         → NotMine, bad-conversation reason
if queuedAt non-zero and now - queuedAt > FallbackAfter → NotMine, gave-up reason   (mirror opencode, incl. its slog.Info)
creds, err = ReadAgyCreds(d.CredsDir, conv)
  err                                                 → Unavailable, no-creds reason
if !loopbackAgyAddress(creds.LSAddress)               → Unavailable, non-loopback reason
origin = firstPayloadLine(payload)

state = d.inbox(conv, origin, queuedAt)   // scans messages/*.json (not read.json) and messages/undelivered/*.json
  state.read                                          → Delivered, "already present"
  state.undelivered                                   → Unavailable, undelivered reason   (sent once; never resend)
  state.sent (file present, not yet in read.json)     → Unavailable, sent-not-read reason (never resend)

content = payload; if len(content) > AgyMaxContent: content = oversize text (§3)
exe = creds.AgentAPIExe or "agy"
out, err = d.Exec.Run(ctx,
    ["ANTIGRAVITY_LS_ADDRESS="+creds.LSAddress, "ANTIGRAVITY_CSRF_TOKEN="+creds.CSRFToken],
    exe, "agentapi", "send-message", "--title="+title(origin), conv, content)
  err                                                 → Unavailable, redact("send-message: "+firstErrorLine(err))
  out parses as JSON with non-empty "error"           → Unavailable, redact("send-message: "+first line of it)
  (out not JSON: treat as success and go on to confirm; confirmation is the proof)

poll until ConfirmWindow elapses, every ConfirmPoll (honour ctx.Done):
  state = d.inbox(conv, origin, queuedAt)
  state.read                                          → slog.Info delivered; Delivered, ""
  state.undelivered                                   → Unavailable, undelivered reason
→ Unavailable, sent-not-read reason
```

`inbox` matching rule: a message file matches when its `recipient == conv`,
the **first line** of its `content` equals `origin`, and its `timestamp` is
not before `queuedAt - agyClockSkew` (a zero `queuedAt` skips the time
check). An unreadable or malformed file is skipped, not an error. `read` is
true when `read.json[id] == true` for a matching id.

`redact(s)` replaces every occurrence of `creds.CSRFToken` in `s` with
`<redacted>`, and does nothing when the token is "".

## 6. Error Handling Strategy

- **Not mine** (another kind, no exec, a planner not registered by
  conversation id, gave up): the entry stays on route `pull` with the reason.
- **Unavailable** (no creds yet, a stale token or closed session so the send
  fails, sent but not yet read, undelivered): the entry stays pending, and the
  next tick retries until `FallbackAfter`. Resending happens only when no
  matching message exists in the inbox at all.
- **Go error:** none in practice. Deliver has no request to build, so every
  failure is an Outcome. Keep the `error` return nil.
- **Secrets:** the token passes only through the `extraEnv` of one exec. It
  is never in argv, a log line, a reason, an error, `AgyCreds.String()`, or
  any file other than the 0600 creds file.
- **Capture failures** are silent by design, and never change a command's
  exit code or output.

## 7. Ordered Implementation Steps

Run `make check` after every step, and also check gofmt with
`gofmt -l $(git ls-files '*.go')`, which must print nothing. All new tests go
in `internal/planner` and `internal/relay`. **Add no test in `cmd/relay`**:
CI has no harness binary and no network, and no test may run `agy`.

1. **Detect.** Extend `planner.Detect` and its doc comment.
   *Verify:* in the `resolve_test.go` table:
   - agy env with a valid UUID gives `{agy, <uuid>, 0}, true`.
   - agy env with a malformed id gives `false`.
   - `CLAUDECODE=1` plus agy env gives `claude`.
   - A `Resolve` case: a registry holding an `agy` record for the UUID
     resolves through `ResolutionSession` with no `RELAY_PLANNER`.
   - Existing rows are unchanged.

2. **Creds file.** Add `Store.AgyCredsDir()`, `AgyCreds` (with its
   redacting `String`/`GoString`), `validConversationID`,
   `loopbackAgyAddress`, `CaptureAgyCreds` and `ReadAgyCreds`.
   *Verify:* `agy_creds_test.go` with a `t.TempDir()`:
   - A full env writes `<conv>.json` with mode 0600 in a 0700 dir, and the
     round-trip via `ReadAgyCreds` is equal.
   - Each missing or invalid var writes nothing (table).
   - A non-loopback address writes nothing.
   - Identical values return `false` and leave the file's mtime unchanged.
   - A changed token rewrites the file.
   - A 10-day-old sibling file is pruned after a write, and a 1-day-old one is
     kept.
   - `fmt.Sprintf("%v %+v %#v", c, c, c)` does not contain the token.

3. **Deliverer.** Add `EnvExec` and `AgyDeliverer` per §5. The fake
   `EnvExec` records `bin`, `args` and `extraEnv`. On "send" it optionally
   writes a message file into the temp agy home, shaped like the spike, and
   optionally marks it in `read.json`, simulating agy.
   *Verify:* `deliver_agy_test.go`, one named test per row. Set
   `ConfirmWindow` and `ConfirmPoll` to a few milliseconds.
   - `TestAgyDeliverNotMineForOtherKind`, `…BadConversationID`,
     `…GaveUpAfterFallback`, `…NoCredsIsUnavailable`,
     `…NonLoopbackIsUnavailable`.
   - `TestAgyDeliverAlreadyReadDoesNotSend`: the fake is **never called**.
   - `TestAgyDeliverSentNotReadDoesNotResend`: a matching file exists without
     a read mark. The result is Unavailable, and the fake is never called.
   - `TestAgyDeliverConfirmsViaReadJSON`: the fake writes the file and marks
     it read. The result is Delivered.
   - `TestAgyDeliverSentButNeverReadIsUnavailable`: the fake writes the file
     and never marks it. The result is Unavailable with the sent-not-read
     reason.
   - `TestAgyDeliverTokenOnlyInEnv`:
     - `args` contain `agentapi`, `send-message`, `--title=…`, the
       conversation and the content.
     - No arg contains the token.
     - `extraEnv` is exactly the two `ANTIGRAVITY_*` pairs.
     - `bin` is `AgentAPIExe` when set, else `agy`.
   - `TestAgyDeliverSendErrorIsRedacted`: the fake returns an error whose text
     contains the token. The reason contains `<redacted>` and not the token.
     Same for a JSON `{"error": "...<token>..."}` stdout.
   - `TestAgyDeliverOversizeSendsPointer`: a payload of `AgyMaxContent+1`
     bytes. The content arg starts with the origin line, contains the report
     path, and is under `AgyMaxContent`.
   - `TestAgyDeliverOlderMessageDoesNotMatch`: a message with the same origin
     but a timestamp well before `queuedAt` is not treated as sent.

   *Mutation (required, 1):* make Deliver return `OutcomeDelivered`
   immediately after a successful send, skipping the confirm poll. Confirm
   `TestAgyDeliverSentButNeverReadIsUnavailable` fails. Restore.
   *Mutation (required, 2):* also pass the token as an extra argv element.
   Confirm `TestAgyDeliverTokenOnlyInEnv` fails. Restore.

4. **Wiring.** Add `binEnvExec`, the capture call at the top of `run()`, and
   `newDeliverers` registering `agy`.
   *Verify:* `make check` passes. In `internal/relay` (not `cmd/relay`),
   add one `DeliverPending` test: a runtime whose `Deliverers["agy"]` is a
   real `AgyDeliverer` over the fake exec and the temp home, and a binding
   whose `Planner` is `{Kind: agy, SessionID: <uuid>}` with one pending
   report. The result is `Delivered` with route `deliverer:agy`, and the log
   entry is confirmed.

5. **Docs.**
   - README "How reports arrive" (~2089-2094): the deliverer list becomes
     "(opencode, agy)".
   - Add one paragraph: an agy planner runs `relay planner init` once inside
     agy, with no flags; it is detected from agy's environment. Every relay
     command it runs refreshes the session's local agentapi credentials,
     stored 0600 under the state dir and never printed. A pushed report wakes
     the agy session and costs one turn.
   - Add the `architect.agy.md` sentence from §3.

   *Verify:* `make check` passes, and `internal/harness` tests pass.

## Report

Report the following:

- The files touched per step.
- Both mutation checks: what you broke and which named test failed.
- The halt-condition evidence: every `Detect` caller you checked, and
  `plannerHostStart(0)`.
- Any agy behaviour this plan assumed that you found no fixture or code to
  support. Name it; do not invent a shape.
