# Planner delivery as a port, opencode's HTTP push path, and the text a push carries (#300, #297)

Status: designed 2026-09-22, not yet built. First step of retiring pane
injection. Closes #300 and #297 in one round: they are the same defect
seen twice -- a payload shaped for an input box, delivered to something
that is not one. Follows the Claude Code channel (#124, spec
`2026-09-21-planner-channel-design.md`, verified live 2026-09-22) and is a
second caller for #205's backend-agnostic core.

## 1. Problem

relay delivers a planner-bound payload by **typing it into the planner's
terminal pane** (`DeliverPending` -> `promptWithRetry` -> `herdr agent
prompt`). Because herdr cannot see the human's input buffer, that risks
merging relay's text with a half-typed message, and the whole HELD
apparatus exists to avoid it: `plannerHold`, screen fingerprinting, the
quiet-grace clock in `held.go`, and a `Notify` nudge per held episode.
A planner sitting in its own pane can hold a report for hours.

#124 replaced that path for Claude Code with a real push (an MCP channel),
and the live check on 2026-09-22 confirmed it works: the report arrived
unprompted, the daemon yielded, nothing was typed. Two of the three
harnesses relay plans with -- opencode and agy -- still get text typed at
them.

Two spikes on 2026-09-22 established that both have a native push path:

- **opencode** (v2.0.12): `POST /api/session/{sessionID}/prompt` on the
  opencode server. Verified end to end against a real TUI -- an external
  POST became a user turn the agent answered, with no typing and no
  screen scraping.
- **agy** (Antigravity 1.2.7): a hidden `agy agentapi send-message
  <conversation_id> <text>`, whose messages land in
  `~/.gemini/antigravity-cli/brain/<id>/.system_generated/messages/`.
  The mechanism is evidenced (80 of 169 local sessions have such an
  inbox, already used for "background task finished" notices) but relay
  sending one is **untested**.

This design covers opencode only. agy gets its own spec once someone has
proved the send path against a live session.

## 2. Goals and non-goals

Goals:

- An opencode planner receives its payloads through opencode's own API
  instead of having them typed into its TUI. No HELD, no grace clock, no
  fingerprint for that planner.
- Delivery is a **port**: one interface, one implementation per planner
  kind, chosen by `Planner.Kind`. agy slots in later without touching
  `DeliverPending` again.
- A payload is confirmed **only** when relay has evidence the planner's
  session actually received it. A transport-level success is not evidence
  (see §3.4).
- Every failure falls back to today's pane injection on the next tick.
  Nothing is lost, nothing is delivered twice.
- Every **push** path -- the Claude channel and the opencode POST --
  carries the report's text, not a pointer to a file the planner must
  then open (#297). The pane path keeps the pointer.
- No change to Claude Code delivery apart from that text, to the claim, or
  to any harness without a deliverer.

Non-goals:

- Deleting pane injection, HELD, `held.go`, or the `Notify` nudge. That is
  a later round, once agy also has a deliverer.
- agy delivery (its own spec).
- `delivery: "steer"` -- barging into a running turn. §3.3 explains why
  `queue` is this round's choice.
- Delivering to an opencode planner relay did not bind (no session id).
- `/api/session/{id}/synthetic`, the notification-chip variant. A report
  must become a turn the planner acts on, not a chip it may ignore.
- Any use of opencode's `GET /api/event` SSE stream. It is observe-only
  and relay's existing poll is sufficient.

## 3. Mechanism

### 3.1 The pick

`DeliverPending` gains one step after the existing channel-claim check and
before the `FindAgent` pane lookup:

```
d, ok := rt.Deliverers[b.Planner.Kind]
if ok:
    text, _ := PushText(pending, os.ReadFile)   // §3.7
    out, err := d.Deliver(ctx, b.Planner, text, pending.Path)
    switch out:
      Delivered  -> ConfirmIndex; return Delivery{Delivered: true}
      NotMine    -> fall through to the pane path
      Unavailable-> return Delivery{Reason: ...}   // all-false: retry next tick
```

`Unavailable` leaves the entry pending. After `FallbackAfter` (§3.6) the
deliverer reports `NotMine` instead, and the pane path takes over.

A binding with `Owner != ""` (remote) never reaches here, as today.

### 3.2 Identity

`b.Planner.SessionID` is already the opencode session id. herdr's opencode
integration (`~/.config/opencode/plugins/herdr-agent-state.js`, v12)
reports it with `pane.report_agent_session`, and `bind.go:706` stores
whatever herdr reports. Confirmed shape: `ses_158b4e895ffeIw66vpo0NBTPX1`.

An empty `SessionID`, or one that does not match `^ses_[A-Za-z0-9]+$`,
means relay cannot address the session: return `NotMine` and let the pane
path have it. The pattern check matters because the id goes into a URL
path and a SQL string.

### 3.3 The request

```
POST {server.url}/api/session/{sessionID}/prompt
Authorization: Basic base64("opencode:" + server.password)
Content-Type: application/json

{"text": "<the entry's Payload>", "delivery": "queue", "resume": true}
```

`text` is the only required field (verified against the live service's
`/openapi.json`: `required: ["text"]`, properties `id, text, files,
agents, skills, metadata, delivery, resume`).

**`queue`, not `steer`.** Both work; the spike proved `steer` is obeyed
mid-turn within the same turn. But relay's contract today is that a
payload is delivered only when the planner is idle -- that is what the
`planner.Status` gate in `DeliverPending` means -- and `queue` preserves
it: the input is durably admitted and runs when the current turn ends.
It is also the same semantics the Claude channel has (events queue and are
handled on the next turn). Steering is a behaviour change and belongs to
its own decision, not to the round that introduces the transport.

`resume: true` because the spec's wording is "schedule agent-loop
execution unless resume is false" -- a queued prompt that never runs the
loop would be a silent hang.

### 3.4 200 is not delivery

The single most important rule in this design.

During the spike, a POST of a **valid session id to the wrong server
process** returned `200` and the message never appeared in the session --
not in the TUI, not in its message list. `opencode.db` is shared between
server processes, but live delivery rides the owning process's event bus.
A default TUI uses the background service; an `opencode serve --standalone`
TUI does not.

So relay must never `ConfirmIndex` on a 2xx. Delivery is confirmed by
**reading the session back** out of the shared db:

```sql
select count(*) from part p join message m on m.id = p.message_id
 where p.session_id = '<sessionID>'
   and json_extract(m.data, '$.role') = 'user'
   and json_extract(p.data, '$.type') = 'text'
   and json_extract(p.data, '$.text') like '%<origin line>%'
```

The origin line is relay's existing per-payload marker
(`OriginLine(name, round, dir, kind)`, already prefixed onto every queued
payload by `Queue`), so it is unique to this entry and needs no new field.

Why `part`/`message` and not `session_inbox`: the inbox table holds
*admitted* work and its rows are consumed -- it is empty on this machine
even though sessions have run. Admission is what the wrong server also
achieves. A user text part proves the session actually took the turn.

Run through the same `sqlite3 -readonly` shell-out `internal/usage`
already uses (`usage.Exec`), against
`$XDG_DATA_HOME/opencode/opencode.db` (default
`~/.local/share/opencode/opencode.db`).

### 3.5 Server discovery

`$XDG_STATE_HOME/opencode/service.json` (default
`~/.local/state/opencode/service.json`) carries everything needed:

```json
{"id": "...", "version": "2.0.12", "url": "http://127.0.0.1:49374",
 "pid": 2593283, "password": "..."}
```

Read it per delivery attempt (it is small, and a restarted service
rewrites it). The service is usable when the file parses, `url` is a
`http://127.0.0.1[:port]` or `http://localhost[:port]` origin, and `pid`
is alive. Any other case -> `Unavailable`.

Note `~/.config/opencode/service.json` also exists and holds only
`{"password": ...}`. It is **not** the file to read; the state one is
authoritative and carries the url.

Refuse a non-loopback `url` outright: relay would be sending a planner's
report, which can contain source, to whatever host the file names. This
is a security boundary, not a nicety.

### 3.6 Fallback

`FallbackAfter` = 30 s, measured from the entry's first delivery attempt
(the existing `LogEntry.TS` is the queue time, which is close enough and
needs no new state). While `now - TS <= FallbackAfter`, a failed attempt
returns `Unavailable` and the payload waits. Past it, the deliverer
returns `NotMine` and the payload is typed into the pane exactly as today.

This is what makes a standalone TUI, a stopped service, or a changed API
a degradation rather than an outage.

### 3.7 What a push carries (#297)

A queued planner payload is composed for typing: `reconcile.go:646` builds
`"Builder finished round %d. Report: %s"` plus a diff line. Pasting a
5 KB report into a terminal input box would be hostile, so the pointer is
right *for a pane* -- and wrong for everything else. The first live
channel round proved it: the planner got the pointer and had to `cat` the
path, while the server's own instructions promised the body was the
report.

A push has no input box and no width limit, so it carries the text. This
is one helper, shared by both push paths, applied at the moment of
delivery -- the stored entry is untouched, so the pane path and every
existing reader see exactly what they see today.

```
PushText(e store.LogEntry, read func(string) ([]byte, error)) (string, bool)
```

- Expands only when `e.Path != ""` and `e.Kind` is one of `report`,
  `findings` (a consult's findings) or `edge` (a handed-over artifact).
  `diff` and `drift` are patches the planner reads with `relay diff`, and
  every other kind's payload is already its whole text.
- Result is the entry's `Payload` (which already carries the origin line),
  then a blank line, then the file's contents.
- Cap `MaxPushBytes` = 64 KiB of file text. Past it, the text is cut at
  the last newline within the cap and a final line is appended:
  `[truncated at 64 KiB -- full text at <path>]`. The `path` meta key
  already carries the location, so nothing is lost.
- A read error is not a delivery failure: return the unexpanded payload
  and `false`. A report that cannot be read is still a report that
  arrived, and the pointer still works.

`internal/mcp/instructions.go` then becomes true as written ("the block's
body is the report") and needs one added sentence for the truncated case.

## 4. Types and contracts

`internal/relay/deliverer.go`:

```go
// Outcome is what a PlannerDeliverer reports back to DeliverPending.
type Outcome int

const (
    // NotMine: this deliverer cannot address that planner (wrong kind, no
    // session id, or it has given up). The pane path takes the payload.
    NotMine Outcome = iota
    // Unavailable: the push path exists but is not reachable right now.
    // The payload stays pending and the next tick retries.
    Unavailable
    // Delivered: the planner's session provably received the payload.
    // Only this value may confirm the entry.
    Delivered
)

// PlannerDeliverer hands one payload to one planner over that harness's
// own push path. Implementations are addressed by planner kind and must
// be safe to call from the daemon's tick goroutine.
//
// Deliver must be idempotent in effect: it is called again on the next
// tick after any non-Delivered outcome, for the same payload.
type PlannerDeliverer interface {
    Deliver(ctx context.Context, planner store.Endpoint, payload, path string) (Outcome, string, error)
}
```

The `string` return is the reason, for `Delivery.Reason` and the log; the
`error` is reserved for a bug in relay (a malformed request it built),
never for an unreachable planner -- that is `Unavailable`.

`internal/relay/deliver_opencode.go`:

```go
type OpencodeDeliverer struct {
    Client       *http.Client   // nil -> a client with RequestTimeout
    StateFile    string         // $XDG_STATE_HOME/opencode/service.json
    DBPath       string         // $XDG_DATA_HOME/opencode/opencode.db
    Exec         usage.Exec     // the sqlite3 shell-out; nil -> never confirms
    Now          func() time.Time
    Alive        func(pid int) bool  // nil -> the unix default
    FallbackAfter time.Duration      // zero -> DefaultFallbackAfter (30s)
}

type opencodeService struct {
    URL      string `json:"url"`
    Password string `json:"password"`
    PID      int    `json:"pid"`
    Version  string `json:"version"`
}
```

Constants: `RequestTimeout = 5 * time.Second`,
`ConfirmWindow = 3 * time.Second`, `ConfirmPoll = 250 * time.Millisecond`,
`DefaultFallbackAfter = 30 * time.Second`.

`internal/relay/push.go` holds `PushText` and `MaxPushBytes` (§3.7). It is pure
apart from the injected `read`: production callers pass `os.ReadFile`,
and `push_test.go` passes a map so the cases need no temp files.

`Runtime` gains `Deliverers map[string]PlannerDeliverer` -- nil means no
deliverer for any kind, so every existing test behaves exactly as before.

## 5. Flow

```
OpencodeDeliverer.Deliver(planner, payload, path):
    if planner.Kind != "opencode":                 return NotMine
    if !validSessionID(planner.SessionID):         return NotMine, "no opencode session id"
    if Exec == nil:                                return NotMine, "no sqlite3"

    svc, err := readService(StateFile)
    if err != nil || !loopback(svc.URL) || !Alive(svc.PID):
                                                   return Unavailable, "<why>"

    origin := <the first line of payload>          // OriginLine, already prefixed;
                                                   // still the first line after PushText

    // Already there? A previous tick may have delivered and crashed before
    // confirming. Check first, so a retry never double-posts.
    if seen(origin):                               return Delivered, "already present"

    POST svc.URL/api/session/<id>/prompt  {text: payload, delivery: "queue", resume: true}
    if transport error:                            return Unavailable, "post: <err>"
    if status != 2xx:                              return Unavailable, "post: <status>"

    // 200 means admitted, not delivered (§3.4).
    poll every ConfirmPoll up to ConfirmWindow:
        if seen(origin):                           return Delivered, ""
    return Unavailable, "posted but not seen in the session"

Drain (internal/relay/drain.go, the Claude channel):
    ...unchanged, except the pushed content:
    content, _ := PushText(entry, os.ReadFile)     // was: entry.Payload
    p.Push(ctx, content, meta)

DeliverPending(...):                               // the only change
    ...existing channel-claim check...
    if d, ok := rt.Deliverers[b.Planner.Kind]; ok:
        text, _ := PushText(pending, os.ReadFile)  // §3.7
        out, reason, err := d.Deliver(ctx, b.Planner, text, pending.Path)
        if err != nil:                             return b, Delivery{}, err
        switch out:
        case Delivered:
            tx.ConfirmIndex(b.Name, idx)
            return clearPlannerScreen(b), Delivery{Delivered: true, Reason: reason, Round: pending.Round}, nil
        case Unavailable:
            return clearPlannerScreen(b), Delivery{Reason: reason, Round: pending.Round}, nil
        // NotMine: fall through
    ...existing pane path, unchanged...
```

Note the pending entry must be read **before** the deliverer is consulted,
which means moving the existing `PendingForPlanner` call above the
`FindAgent`/status gate. That reordering is behaviour-neutral for the pane
path (it already returns `Empty` when nothing is pending) but it must be
done deliberately, and the existing "planner is gone/working" tests must
still pass unchanged.

## 6. Errors

| condition | outcome | who retries |
|---|---|---|
| planner kind is not opencode | `NotMine` | pane path, same tick |
| empty or malformed session id | `NotMine` | pane path, same tick |
| no `sqlite3` on PATH | `NotMine` | pane path, same tick |
| service.json missing / unparseable / pid dead | `Unavailable` | next tick |
| `url` not loopback | `Unavailable`, logged once per binding | next tick |
| POST transport error or non-2xx | `Unavailable` | next tick |
| POST 2xx, not seen within `ConfirmWindow` | `Unavailable` | next tick |
| any of the above, past `FallbackAfter` | `NotMine` | pane path |
| sqlite3 fails | `Unavailable` | next tick |

Nothing in this table can lose a payload: the entry is confirmed only on
`Delivered`, and every other path leaves it pending for the pane.

Logging: one `slog.Info` per state change (first `Unavailable` for an
entry, the `Delivered`, the fallback), never per tick -- the daemon
reconciles every couple of seconds and a per-tick line would drown the
log, the same rule `payload held` already follows.

## 7. Security

- The password is read from a 0600 file and used for Basic auth to
  loopback only. It is never logged, never put in an error message, and
  never in a `Delivery.Reason`.
- Non-loopback `url` is refused (§3.5).
- The session id is pattern-checked before it reaches a URL path or a SQL
  string (§3.2).
- The origin line is put into the confirmation query with `'` doubled,
  the same escaping `usage.OpencodeQuery` uses for a directory.

## 8. Testing

- `internal/relay/deliver_opencode_test.go`, against `httptest.Server` and
  a temp sqlite db seeded through the same `Exec` seam:
  - happy path: POST is made with the right path, auth header, and body
    (`delivery: "queue"`, `resume: true`); the seeded db contains the
    origin; outcome `Delivered`.
  - **the silent-200 case**: server returns 200, db never gains the row ->
    `Unavailable`, and `ConfirmIndex` is not called. This is the test the
    design exists for; mutation-check it by making the deliverer confirm
    on 2xx and watching it fail.
  - idempotence: origin already in the db -> `Delivered` with no POST at
    all.
  - `NotMine` for a claude planner, an empty session id, a `ses_../x` id,
    and a nil `Exec`.
  - `Unavailable` for: missing state file, dead pid, non-loopback url,
    500 from the server, transport error.
  - past `FallbackAfter` every `Unavailable` case becomes `NotMine`.
  - the password never appears in any returned reason or error.
- `internal/relay/deliver_test.go`: with `Deliverers` set, an opencode
  planner's payload is not typed (`fakeHerdr` records zero prompts) and is
  confirmed; a claude planner's path is untouched; a nil `Deliverers` map
  behaves exactly as today.
- `internal/relay/push_test.go`: a report entry with a readable path
  expands to payload + blank line + text; a `diff` entry does not expand;
  an entry with no path does not expand; a read error returns the payload
  unchanged and `false`; a file over `MaxPushBytes` is cut at a newline
  and carries the truncation line naming the path; the origin line
  survives as the first line in every expanded case (the opencode
  confirmation query depends on it -- §3.4).
- `internal/relay/drain_test.go` (extend): the pushed content contains the
  report's text, not just the pointer.
- `make e2e` after the round: the deliver path is touched. The suite has
  no opencode planner, so it also proves the pick is inert by default.
- By hand, once, after merge: bind a relay binding from an opencode
  planner pane, send a trivial round, and watch the report arrive in the
  TUI as a user turn with nothing typed. Record it on the issue, as #124's
  live check was.

## 9. Risks and open questions

- **The API is not versioned for us.** `/api/session/{id}/prompt` is
  opencode's own surface and may change between 2.x releases. The failure
  mode is a non-2xx -> `Unavailable` -> fallback, which is survivable, and
  `service.json` carries `version` if a guard is ever wanted.
- **A standalone TUI never confirms.** It falls back after 30 s, which is
  correct but silent from the planner's point of view. If that turns out
  to be common, the next step is reading the TUI process's environment for
  its own server url -- deliberately out of scope here.
- **`queue` vs `steer` is unmeasured in practice.** If a queued payload
  turns out to sit behind a long turn in a way that annoys, `steer` is a
  one-field change with its own decision.
- **A very large report is truncated in the push.** 64 KiB is far above
  any report relay has produced, and the full text stays one `cat` away
  at the path the meta carries. If it ever bites, the cap is one constant.
- **agy remains typed at.** Until its spec lands, `held.go` and the whole
  injection path stay. This round deletes nothing.
