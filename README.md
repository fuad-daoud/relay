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

- `relay bind [--name N] [--builder ALIAS|PANE_ID] [--resume]` — start a
  binding between the calling planner pane (read from `$HERDR_PANE_ID`) and a
  builder. `--builder` is looked up as an alias unless it contains `:`, in
  which case it is treated as a herdr pane id and that pane is **adopted**
  instead of spawned. A name that already exists is refused rather than
  reused: only `bind.json` would be rewritten, so a fresh round 1 would
  collide with the previous session's round log. `--resume --name N`
  re-points that existing binding's planner side at the calling pane without
  touching the builder; `relay unbind N` is the other way out.
- `relay send --file PATH [--name N]` — stage the file as the current round's
  plan and prompt the builder with it.
- `relay pull [--name N]` — print the newest pending payload to stdout and
  mark it delivered, without typing into any pane. This is the safe way for
  the planner to fetch a report mid-turn.
- `relay answer --name N (--keys K | --choice N | --text S)` — answer a
  builder that's blocked at a dialog, via `send-keys` rather than a typed
  prompt (herdr refuses `agent prompt` against a blocked agent).
- `relay status [--json]` — one row per binding: round, display state, both
  panes' live herdr status, the last relayed event, and anything pending.
- `relay log NAME` — the binding's append-only round log.
- `relay watch [--interval D]` — `status`, redrawn on a timer, default 2s.
- `relay done [NAME]` — mark a binding done; relaying stops (panes are left
  alone).
- `relay unbind NAME` — forget a binding. Never touches the panes.
- `relay daemon [--interval D]` — the long-running reconciler; this is what
  `relay.service` runs.

`--name` is optional almost everywhere: it defaults to whichever binding owns
the current working directory.

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

## Prerequisite: agy has no herdr integration yet

herdr integrations are currently installed for **opencode** and **claude**
only on this machine. Until `herdr integration install antigravity-cli` is
run, an `abuilder` (agy) pane reports `unknown` lifecycle state instead of
real status. Relay never treats `unknown` as done — so an `abuilder` binding
will stall rather than misbehave, but it *will* stall until that integration
is installed.

## Design

`~/docs/superpowers/specs/2026-09-04-relay-planner-builder-design.md`
