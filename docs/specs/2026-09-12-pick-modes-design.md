# Pick modes: `done`, `unbind` and `answer` at a keystroke

**Issue:** #15 (the binding and answer pickers; the `send` file picker is
deferred, see §2).
**Depends on:** the herdr plugin packaging
(`docs/specs/2026-09-08-relay-herdr-plugin-design.md`, #25), which is the
surface these render into.
**Amends:** that spec's §10 "Out of scope" (interactive pickers are no longer
out of scope) and its `open-ui` action script, which becomes the general
`plugin-open-pane.sh`.

## 1. Problem

A herdr keybinding can only target a plugin action, and an action declares a
fixed argv: it takes no arguments and its stdout goes to the plugin log. So the
three relay verbs a human runs most -- `done`, `unbind`, `answer` -- cannot be
bound to a key today, because each one needs a binding name and refuses,
deliberately, to guess one (`explicitBinding` in `cmd/relay/main.go`).

`answer` is the one that hurts. The daemon writes the builder's dialog to
`question-N.txt`, sets `NEEDS YOU`, and the human has to open the file, read
it, and type `relay answer --name X --choice N` by hand. "Answer the blocked
builder" should be one keystroke and one line of typing.

## 2. Scope

Each verb grows a `--pick` flag that renders a small terminal picker, runs the
verb on the choice, shows the result, and exits. The herdr plugin declares one
popup pane and one action per verb, so each is bindable to a key.

In scope:

- `relay done --pick`, `relay unbind --pick`, `relay answer --pick`.
- A binding picker shared by the three verbs, listing what each verb can act on.
- An answer screen that shows the builder's live dialog text and takes one
  line of input.
- A result screen that prints what the CLI would have printed and waits for a
  key, so a popup pane does not vanish before the human has read it.
- Plugin manifest entries (`[[panes]]` + `[[actions]]`) for all three, in both
  `herdr-plugin.toml` and `from-source/herdr-plugin.toml`.

Out of scope:

- **The `send` file picker.** `relay send --file` is run by the planner agent,
  which already knows the path; a file browser is the largest of the three
  pickers and has no human caller today. Still tracked by #15.
- **Parsing the dialog into a menu.** relay reads no screen text for meaning
  (builder-switching spec §1, consults spec §1). The answer screen shows the
  bytes herdr returned and the human types the answer.
- **Implicit entry.** `relay done` with no name still prints usage and refuses.
  "stdin is a TTY" is exactly the condition inside a herdr agent pane, so an
  implicit picker would catch the planner agent, which cannot drive one.
- **A confirm step.** The list shows each row's state, which is what a confirm
  dialog would repeat. `unbind` is already reversible through
  `relay bind --resume`.
- Pickers for `send --name`, `nudge`, `reset`, `switch`. Same mechanism; add
  them when someone binds a key for them.

## 3. CLI contract

```
relay done   --pick
relay unbind --pick [--archive]
relay answer --pick
```

- `--pick` with `--name` or a positional is a usage error: "`--pick` chooses
  the binding; do not also name one". `answer --pick` with `--keys`, `--choice`
  or `--text` is likewise refused: the screen supplies the answer, so a flag
  would either be ignored or override what the human typed, and neither is
  acceptable silently.
- `--pick` needs a terminal on stdout, checked the way `ui.Run` checks
  (`os.Stdout.Stat` is a character device). When it is not, the error is
  "`--pick` needs a terminal; name the binding instead".
- Exit status: `0` when the verb ran and succeeded; `1` when the human
  cancelled, when there was nothing to pick, or when the verb failed. The
  picker prints `cancelled` on cancel so a shell caller can tell the cases
  apart by output as well. `pick.Run` reports the three non-zero outcomes as
  the sentinels `pick.ErrCancelled`, `pick.ErrNothingToPick` and
  `pick.ErrVerbFailed`; the command maps each to `exitCodeErr{code: 1}` so
  `main` exits silently -- the screen has already shown the text, and a
  second `relay: ...` line on stderr would land in the plugin log, not in
  front of the human.
- The no-`--pick` paths of all three verbs are byte-for-byte unchanged.

## 4. The binding picker

One list, three filters. Rows come from `relay.Status(ctx, rt)`, the same
snapshot `relay status` prints.

| verb | rows | why |
|---|---|---|
| `done` | `relay.HideDone(report)` | a DONE binding cannot be marked done again |
| `unbind` | the full report, DONE included | DONE rows are what `unbind` (and `gc`) clear |
| `answer` | rows whose `BuilderStatus == herdr.StatusBlocked` | `relay.Answer` refuses anything else |

For `answer`, when exactly one row qualifies the list is skipped and the answer
screen opens on it directly: the keystroke economy is the point, and the
answer screen's header names the binding, so nothing is hidden.

Row format, one line per binding:

```
> webshop        NEEDS YOU  r3  builder agy blocked
  api-client     WORKING    r1  builder claude running
```

Cursor `>` on the selected row; `Display` styled the way `relay ui` styles it
(`internal/ui/styles.go` is not imported -- the picker has its own two
styles). Keys: `↑`/`k` and `↓`/`j` move; `Enter` picks; `Esc`, `q` and
`Ctrl+C` cancel. The list is bounded to the terminal height with the same
window rule `internal/ui/list.go` uses (`listWindow`), copied rather than
imported so `pick` does not depend on `ui`.

An empty list is a message, not a blank screen: "no bindings to mark done" /
"nothing bound" / "no builder is blocked", then `press any key`, then exit 1.

## 5. Running the verb; the result screen

`Enter` runs the verb at once, in the picker's process:

| verb | call |
|---|---|
| `done` | `relay.Done(ctx, rt, name)` |
| `unbind` | `relay.Unbind(ctx, rt, name, archive)` |
| `answer` | `relay.Answer(ctx, rt, name, in)` (from the answer screen, §6) |

The result screen then prints exactly the lines the CLI prints for the same
call -- the three `fmt.Printf` blocks in `cmdDone`, `cmdUnbind` and `cmdAnswer`
are extracted into pure functions in `internal/relay` (`DoneText(name)`,
`UnbindText(name, res)`, `AnswerText(name)`) that both paths render, so the popup and the terminal can
never say different things. On error the screen prints the error wrapped to
the terminal width, capped like `ui.renderError`. Either way the last line is
`press any key`, and the key exits with the status from §3.

## 6. The answer screen

```
answer webshop (round 3)                          ↑/↓ scroll  Enter send  Esc cancel
────────────────────────────────────────────────────────────────────────────
 Allow Bash(rm -rf node_modules)?
   1. Yes
   2. Yes, and don't ask again this session
   3. No
────────────────────────────────────────────────────────────────────────────
answer> 1_
```

- Header: verb, binding, round (from the picked row).
- Body: a scrollable viewport over the builder's live dialog. It is fetched
  once on entry with `rt.Herdr.ReadAgentSource(ctx, relay.Target(builder),
  "detection", 200)` -- the same source and bound the daemon uses in
  `handleBlockedBuilder`, so the human sees what the daemon saw, refreshed.
  When that read fails, the body falls back to the round's
  `rt.Store.QuestionPath(name, round)` if the file exists, and shows a
  visible `could not read dialog: <err>` line above it either way. Input is
  still allowed on a failed read: the human can see the builder pane.
- Input: one line. `Enter` submits; empty input is refused inline with
  "type a key name, a number or text" and does not exit.
- `relay.ParseAnswer(s string) AnswerInput` turns the line into what
  `relay.Answer` takes. It is a pure function in `internal/relay/answer.go`:

  | input | field | rule |
  |---|---|---|
  | `12` | `Choice: 12` | `strconv.Atoi` succeeds and the value is `> 0` |
  | `enter`, `esc`, `tab`, `up`, `down`, `space` | `Keys: s` | case-insensitive match on this fixed list, the logical key names herdr `send-keys` accepts |
  | anything else | `Text: s` | as typed, surrounding whitespace trimmed |

  `0` and negative numbers fall through to `Text` (they are not dialog
  options; `AnswerInput.resolve` already treats `Choice: 0` as unset).
- `relay.Answer`'s blocked guard runs as today, against a fresh `ListAgents`:
  a dialog that resolved itself while the human read it is refused
  (`ErrBuilderNotBlocked`), shown on the result screen, not typed into.

The screen parses nothing out of the body. There is no option list to move a
cursor over.

## 7. Plugin surface

`herdr-plugin.toml` and `from-source/herdr-plugin.toml` both gain:

```toml
[[panes]]
id = "pick-done"
title = "relay done"
placement = "popup"
command = ["./relay", "done", "--pick"]

[[panes]]
id = "pick-unbind"
title = "relay unbind"
placement = "popup"
command = ["./relay", "unbind", "--pick"]

[[panes]]
id = "pick-answer"
title = "relay answer"
placement = "popup"
command = ["./relay", "answer", "--pick"]

[[actions]]
id = "pick-done"
title = "Mark a binding done"
command = ["sh", "scripts/plugin-open-pane.sh", "pick-done"]

[[actions]]
id = "pick-unbind"
title = "Unbind a binding"
command = ["sh", "scripts/plugin-open-pane.sh", "pick-unbind"]

[[actions]]
id = "pick-answer"
title = "Answer the blocked builder"
command = ["sh", "scripts/plugin-open-pane.sh", "pick-answer"]
```

`scripts/plugin-open-ui.sh` becomes `scripts/plugin-open-pane.sh`, taking the
entrypoint id as `$1`; the existing `open-ui` action's command changes to
`["sh", "scripts/plugin-open-pane.sh", "ui"]` and keeps its id, so existing
keybindings on it survive. The script's `ui_busy` handling is unchanged.

Users bind keys the same way the plugin spec documents for `open-ui`:

```toml
[[keys.command]]
type = "plugin_action"
plugin = "fuad-daoud.relay"
action = "pick-answer"
```

The `from-source` manifest's `command` arrays point at the same relative
`./relay` binary; only the build steps differ, as today.

## 8. Package layout

```
internal/pick/
  pick.go        Run(ctx, rt, Options) error -- terminal check, program setup,
                 exit-status mapping. Options{Verb, Archive}.
  bindings.go    the list model (§4): rows, cursor, window, filter per verb
  answer.go      the answer screen (§6): fetch, viewport, input
  result.go      the result screen (§5): text, wrap, wait for key
  fake_test.go   fakeHerdr, the internal/ui pattern
  *_test.go
internal/relay/answer.go     + ParseAnswer
internal/relay/text.go       DoneText/UnbindText/AnswerText, shared by cmd and pick
cmd/relay/main.go            --pick on done/unbind/answer
cmd/relay/main_test.go       flag validation only (no herdr)
scripts/plugin-open-pane.sh  renamed from plugin-open-ui.sh, takes $1
herdr-plugin.toml, from-source/herdr-plugin.toml
README.md                    "Plugin" section: the three actions
```

`pick` imports `relay`, `store`, `herdr` and bubbletea; it does not import
`ui`. The two packages share no code on purpose: `ui` polls, has tabs and
detail fetches and sticky selection; `pick` opens, asks one thing, and exits.
A popup should open in the time it takes to run `relay status` once.

The model is one bubbletea `Model` with a `screen` enum (`list`, `answer`,
`result`), the pattern `internal/ui/model.go` already uses. Every `Update` is
pure over messages; herdr and store calls happen in `tea.Cmd`s that return
messages (`statusMsg`, `dialogMsg`, `verbDoneMsg`), so tests drive the model
with messages and a fake `Runtime`.

## 9. Errors

| where | what | shown as | exit |
|---|---|---|---|
| entry | stdout not a terminal | error on stderr | 1 |
| entry | `--pick` with a name | usage error | 1 |
| list | `relay.Status` fails | result screen with the error | 1 |
| list | nothing to pick | one-line message, key | 1 |
| answer | `ReadAgentSource` fails | `could not read dialog` line + fallback body; input allowed | -- |
| answer | empty input | inline refusal, stays on screen | -- |
| verb | `Done`/`Unbind`/`Answer` returns an error | result screen with the wrapped error | 1 |
| any | `Esc`/`q`/`Ctrl+C` | `cancelled` | 1 |
| any | context cancelled | program exits | 1 |

Nothing here writes to the log (`log.jsonl`): the verbs already log what they
do, and the picker adds no decision of its own.

## 10. Testing

- `internal/pick`: table tests over `Update` with a fake `Runtime`
  (`store.New(t.TempDir())`, `fakeHerdr`). Pin: each verb's filter (§4 table);
  the single-blocked-row shortcut; cursor bounds; empty-list message per
  verb; the answer screen's fallback order (live read, then question file,
  then error line); empty input refused; `Enter` on the result screen exits
  with the right status; the rendered result text equals `doneText` /
  `unbindText` / `answerText` output.
- `internal/relay`: `TestParseAnswer` over the §6 table, including `0`, `-1`,
  ` enter `, `ENTER`, and free text.
- `cmd/relay`: `--pick` plus a name is a usage error, for all three verbs. The
  test fails in `parseFlags` before `newRuntime`, so it never reaches herdr
  (CI runners have none).
- `make check` green.

## 11. To verify by hand, after merge

The plugin spec's constraint 1 says popup panes exist; #15 asserts three
behaviours of them that nothing in this repository has exercised. Each is a
line in the PR, with the herdr version:

1. A `placement = "popup"` pane closes when its command exits (the result
   screen's "press any key" is the last thing the human sees, then the
   popup is gone).
2. `Esc` reaches the program (cancel works) rather than closing the popup
   around it.
3. The pane reports a real size on `WindowSizeMsg` (the list is windowed,
   not clipped).

If (1) fails, the result screen additionally runs `herdr pane close` on its
own pane -- a one-line follow-up, not a redesign. If (2) fails, `q` is the
documented cancel and `Esc` is dropped from the key help.
