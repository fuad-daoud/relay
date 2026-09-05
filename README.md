# relay

`relay` automates the plan/report handoff between two AI coding agent panes
running under `herdr`, a terminal multiplexer. A human talks to a **planner**
agent; the planner hands work to a **builder** agent; relay moves the files
between them so the human never copy-pastes a plan or a report by hand.

Relay makes no judgements. It moves files, types prompts, and watches herdr's
live agent state — whether a report is good, whether a question needs a
human, whether the work is done, is a decision that stays with the planner
(or the human) at every step.

## Command surface

- `relay bind [--name N] [--builder ALIAS|PANE_ID] [--resume] [--tab] [--timeout D]`
  — start a binding between the calling planner pane (read from
  `$HERDR_PANE_ID`) and a builder. `--builder` is looked up as an alias unless
  it contains `:`, in which case it is treated as a herdr pane id and that pane
  is **adopted** instead of spawned. A name that already exists is refused
  rather than reused: only `bind.json` would be rewritten, so a fresh round 1
  would collide with the previous session's round log. `--resume --name N`
  re-points that existing binding's planner side at the calling pane without
  touching the builder; `relay unbind N` is the other way out.
- `relay send --file PATH [--name N]` — stage the file as the current round's
  plan and prompt the builder with it.
- `relay pull [--name N]` — print the newest pending payload to stdout and
  mark it delivered, without typing into any pane. This is the safe way for
  the planner to fetch a report mid-turn.
- `relay answer [--name N] (--keys K | --choice N | --text S)` — answer a
  builder that's blocked at a dialog, via `send-keys` rather than a typed
  prompt (herdr refuses `agent prompt` against a blocked agent).
- `relay status [--json]` — one row per binding: round, display state, both
  panes' live herdr status, the last relayed event, and anything pending.
- `relay log NAME` — the binding's append-only round log.
- `relay watch [--interval D]` — `status`, redrawn on a timer, default 2s.
- `relay done NAME|--name N` — mark a binding done; relaying stops.
- `relay unbind NAME|--name N [--archive]` — forget a binding, deleting its directory or
  packing it into `.archive/` first.
- `relay gc [--dry-run] [--archive]` — clear every binding the planner marked
  `DONE`, in one pass.
- `relay daemon [--interval D]` — the long-running reconciler; this is what
  `relay.service` runs.

`--name` defaults to whichever binding owns the current working directory for
`send`, `pull`, `answer` and `status`. It is **required** for `done` and
`unbind`: those are the destructive verbs and they refuse to guess (see below).

### Panes are yours, always

relay never opens, closes or kills a pane except the one builder pane it spawns
for you at `bind`. In particular:

- `done` and `unbind` leave the builder running. Its terminal is often the only
  record of *why* a round went wrong, and throwing that away automatically is
  worse than leaving a process up.
- a `BROKEN`, `ORPHANED` or timed-out binding is flagged and reported, never
  cleaned up. relay stops relaying and waits for you.
- closing builder panes when you are finished with them is a manual step, and
  worth remembering: an idle opencode builder holds roughly 800 MB.

### Where the builder appears

By default `relay bind` splits the planner's pane, so you can watch the builder
work beside you. `relay bind --tab` opens it in its own herdr tab instead —
the planner keeps full width, at the cost of not seeing the builder live.

### Round budget

Each round carries a budget; past it, relay flags the binding `NEEDS YOU` and
notifies once. It never kills anything — a builder working a real stage of a
plan runs for hours, so the budget is a runaway guard, not a progress estimate.
The default is 24 hours; `relay bind --timeout 2h` sets it per binding.

### Cleaning up finished bindings

A binding leaves `~/.local/state/relay/<name>/` behind: `bind.json`, `log.jsonl`,
and every round's plan, report and captured dialog. `relay done` stops relaying
but removes nothing — the log is the record of what the planner actually told
the builder.

```
relay unbind ai              # delete the binding and its whole directory
relay unbind ai --archive    # pack it into .archive/ai-<date>.tar.gz, keeping the log
relay gc --dry-run           # list every DONE binding that would be cleared
relay gc --archive           # archive them all in one go
```

Archives are gzipped tarballs under `~/.local/state/relay/.archive/`. A typical
eight-round binding compresses about 60x — a thousand of them is under 2 MB — so
archiving is effectively free. To read one back:

```
tar -xzf ~/.local/state/relay/.archive/ai-20260905-121500.tar.gz -O ai/log.jsonl
```

`gc` only touches bindings the planner marked `DONE`. A `BROKEN` or `ORPHANED`
one is left alone: it still needs a human, and clearing it would throw away the
state that explains why it stopped.

Neither command closes a pane — the builder's terminal stays where it is, for
you to read and close yourself.

### done and unbind are the destructive verbs

`relay done` and `relay unbind` both require a binding name (`relay done ai`, or
`--name ai`). Neither resolves the current directory for you: a bare `relay done` once ended a live
loop by accident, and the recovery is `relay bind --resume --name <name>`.

## Builder aliases

| alias      | kind     | model                              | role selection |
|------------|----------|-------------------------------------|----------------|
| `builder`  | opencode | `openrouter/z-ai/glm-5.3-flash`     | `--agent plan-executor` |
| `cbuilder` | claude   | `sonnet`                            | `--agent plan-executor` |
| `abuilder` | agy      | `gemini-3.8-flash-high`             | preamble on the first prompt |

`agy` has no `--agent` flag, so `abuilder` instead gets a preamble — "Activate
your 'plan-executor' skill..." — prepended to round 1's prompt only.

An **adopted** pane (bind by pane id, or `--resume`) gets no preamble at all:
the human's own launcher already put that session in the right role, and
relay has no alias to consult for an adopted builder.

Aliases can be overridden or extended via `~/.config/relay/aliases.json`.

## Display states

`relay status` collapses the binding's internal state into four:

- **ACTIVE** — someone is working (planner or builder), nothing needs a human
  yet.
- **NEEDS YOU** — relay has stopped and a person must act. Covers a blocked
  builder (answer its dialog), a dead builder pane, a lost planner pane, a
  round that ran past its timeout, and a binding that hit its round cap.
- **HELD** — a payload is ready for the planner, but the planner pane is
  focused, so relay is holding it rather than typing into it.
- **DONE** — the planner declared the work verified via `relay done`, and
  relaying has stopped deliberately, not because anything went wrong: unlike
  NEEDS YOU, nothing needs a human here. `Reconcile` returns immediately for
  a done binding — no reports are queued, no dialogs captured, no timeouts
  flagged. The binding and its round log stay on disk (`relay log <name>`
  still works as an audit trail) until `relay unbind` removes them.

## The anti-clobber rule

`herdr agent prompt` types text into a pane and presses Enter. Since relay
cannot see what a human has half-typed, it treats a focused planner pane as
unsafe to inject into: it holds the payload and sends one herdr notification
(not one per tick) instead of typing over the human. As soon as the human's
focus moves to another pane, the daemon delivers the held payload on its next
tick. `relay pull` bypasses this entirely — it prints the payload to stdout
instead of injecting it, so it's safe to run from inside the focused planner
pane at any time.

## Installation

```
make install   # builds and installs ~/.local/bin/relay
make service   # installs dist/relay.service and starts it as a user unit
```

`make check` runs `gofmt -l .`, `go vet ./...`, and `go test -count=1 ./...`.
`make uninstall` stops the unit and removes both the binary and the unit
file.

The unit runs `relay daemon` outside any herdr-managed pane, so it starts
with none of the `HERDR_*` environment variables herdr injects into a pane
it manages. The cheap way to confirm this is fine before trusting the
service: if `relay status` works from a plain terminal (not inside a herdr
pane), the daemon will work there too, since both resolve the running herdr
session the same way.

## opencode permission allowlist

relay stages plans and reports under `~/.local/state/relay/<binding>/`, outside
the repo the builder is working in, so a fresh opencode builder blocks on an
"Access external directory" dialog on its first round. relay handles it — the
daemon captures the dialog and the planner answers with `relay answer` — but to
skip it entirely, `~/.config/opencode/opencode.jsonc` carries:

```jsonc
"permission": {
  "external_directory": {
    "/home/fuad/.local/state/relay/*": "allow",
    "/home/fuad/.local/state/relay/**": "allow"
  }
}
```

Claude builders (`cbuilder`) have their own permission model and are not covered
by that entry.

## herdr integrations

relay reads lifecycle state (`idle` / `working` / `blocked` / `done` /
`unknown`) from herdr, which learns it from a hook each harness installs.
All three builders are covered on this machine:

```
opencode   ~/.config/opencode/plugins/herdr-agent-state.js
claude     ~/.claude/hooks/herdr-agent-state.sh
agy        ~/.gemini/config/hooks/herdr-agent-state.sh   (herdr integration install antigravity-cli)
```

Without a harness's integration, herdr falls back to heuristic screen
detection and reports `unknown`. relay never treats `unknown` as done, so such
a binding stalls rather than misbehaving — but it does stall. A binding whose
builder reports no session id also never self-heals from `BROKEN`, since
recovery requires matching the same herdr session (see below).

## Recovering a broken binding

If relay cannot find the builder — a detection flicker, a restarted agent — the
binding goes `BROKEN` and relaying stops. It clears itself only when the **same
herdr session id** reappears, never on a pane-id match alone: a pane you closed
and reused for something else must never start receiving plans meant for a
builder. Spawned builders record their session id at `bind` on a best-effort
basis, so a harness reporting none simply never self-heals, which is the safe
direction.

If it stays broken, `relay bind --resume --name N` re-points it, or
`relay unbind N` and bind fresh.

## Design

`~/docs/superpowers/specs/2026-09-04-relay-planner-builder-design.md`
