# A reader consult is asked for its agent's output, not always "findings"

Commit this plan as `docs/plans/2026-09-26-consult-output-prompt.md` in the same PR (last step).
Stop and report instead of improvising if a step is impossible as written or a
quoted location does not match the code.

## 1. System Overview

Every headless consult (`relevo ask --actor X`, and the verify reviewer) runs with
one hardcoded prompt, `internal/consult/prompt.go:13`:

> Answer as your final message: your findings, complete, in markdown. Do not modify any file in this repository. Do not write a findings file; relevo records your final message.

The prompt must instead name the output label of the actor's **agent**:
- shipped `architect` → `plan`;
- `reviewer` → `findings`;
- `researcher` → `notes`;
- a custom source agent → its `output:` field.

So `relevo ask --actor lite-planner` asks for "your plan". Today it asks for
"your findings", which makes the planner prefix its plan with a `# Findings`
section.

**Scope is the prompt wording only.** Everything else stays exactly as it is:
- stored file names (`NNN-<id>-findings.md`);
- `show --findings`;
- the `findings:` line `ask` prints;
- consult records;
- the verify reviewer.

Reader artifacts, artifact directories and naming belong to another planner's
work (A5), which asked that none of that change now.

**Do not touch** these files, which are in flight elsewhere:
- `internal/store/{binding,consult,log,format}.go`
- `internal/view/status.go`, `internal/view/statusline.go`
- `internal/harness/opencodeplugin/tui.tsx`, `scripts/testdata/opencode-plugin/*`
- `internal/remote/proto.go`, the `internal/serve` status document
- the contract goldens for status, statusline, show-log and proto-*

If the change seems to need any of them, halt and report.

## 2. Files

```
internal/consult/prompt.go        headlessPrompt takes the output label; headlessRender(output); DefaultOutput
internal/consult/ask.go           Request.Output; Spawn renders with it (~line 78)
internal/consult/verify.go        verify renders with DefaultOutput (~line 241): behaviour unchanged
internal/consult/prompt_test.go   update TestInlinePrompt's render; new TestHeadlessRenderNamesOutput
internal/relevo/actoroutput.go    NEW: ActorOutput, a pure lookup of an actor's output label
internal/relevo/actoroutput_test.go  NEW: its table test
internal/relevo/ask.go            Ask resolves the label and passes Request.Output (~line 181)
docs/plans/2026-09-26-consult-output-prompt.md
```

## 3. Data and Contracts

### `internal/consult/prompt.go`

- `const DefaultOutput = "findings"`: the label used when an actor's output is unknown.
- `headlessPrompt` becomes a two-argument template, using indexed verbs `%[1]s`
  for the question reference and `%[2]s` for the output label:
  `"%[1]s\n\nAnswer as your final message: your %[2]s, complete, in markdown. Do not modify any file in this repository. Do not write a %[2]s file; relevo records your final message."`
  Update its doc comment: the second argument is the output label.
- `func headlessRender(output string) func(ref string) string` returns the
  render function the inline logic takes. An empty `output` means `DefaultOutput`.
  Both existing `fmt.Sprintf(headlessPrompt, ref)` call sites use it:
  `ask.go:78` with `req.Output`, and `verify.go:241` with `DefaultOutput`.

### `internal/consult/ask.go` — `Request`

- Add the field `Output string`: the output label the headless prompt asks for.
  Empty means `DefaultOutput`. It is used only when `Round == 0`, because a round
  consult has its own prompt (`roundAskPrompt`), which is unchanged.

### `internal/relevo/actoroutput.go` (new)

```
// ActorOutput is the output label of the agent actor plays, the word a consult's
// prompt asks for.
func ActorOutput(actors map[string]roles.Actor, agents map[string]roles.AgentEntry, actor, definition string) string
```

Resolution, first match wins:
1. `agent := actors[actor].Agent` when the actor is in `actors`. Otherwise
   `agent := definition`, the harness definition the spec resolved. That covers
   legacy and built-in roles, where the definition equals the shipped agent name.
2. `roles.Shipped(agent)` is ok → `.Output`.
3. `agents[agent].Source != ""` and `agentsrc.Parse([]byte(Source))` succeeds →
   `.Output`, when non-empty.
4. Otherwise → `consult.DefaultOutput`. This covers native agents, an unknown
   agent and a parse error.

It never errors. It lives in its own file to stay out of `ask.go`'s way; the
package comment exists already, so add none.

### `internal/relevo/ask.go` — `Ask` (~lines 125-195)

- Resolve the label once, after `role` (the spec) is known:
  - `var actors, agents` come from `rt.Config.Load()` when `rt.Config != nil`.
    A load error is **not** fatal: fall back to nil maps, since the label only
    words the prompt.
  - `output := ActorOutput(actors, agents, opts.Role, role.Definition)`.
- Pass `Output: output` in the `consult.Request` literal (~line 181).
- `askRound` is unchanged.
- Keep `Ask` at 70 lines or fewer. If it would grow past that, move the config
  load and the lookup into one helper, `func consultOutput(rt Runtime, actor, definition string) string`,
  in `actoroutput.go`.

## 4. Pseudocode

```
Spawn(req):
  if req.Round == 0:
     render = headlessRender(req.Output)
     prompt, inline = inlinePrompt(render, req.Body)
  ...unchanged

Ask(opts):
  ...resolve role spec (unchanged)...
  output := consultOutput(rt, opts.Role, role.Definition)
  consult.Spawn(..., Request{..., Output: output})
```

## 5. Errors

No new errors. `ActorOutput` and `consultOutput` are total. Every existing
error path is unchanged.

## 6. Working Efficiently

- Read in one parallel batch:
  - `internal/consult/prompt.go`, `internal/consult/prompt_test.go`;
  - `internal/consult/ask.go` 30-100;
  - `internal/consult/verify.go` 225-250;
  - `internal/relevo/ask.go` 100-200;
  - `internal/roles/actors_shipped.go`, `internal/roles/actors.go` 30-60;
  - `internal/agentsrc/source.go` 25-60 (`Source`, `Parse`);
  - `internal/config/config.go` 79-110 (`Loaded`).
- One edit call per file.
- Focused: `go test ./internal/consult/ ./internal/relevo/ -run 'Prompt|Render|ActorOutput|Ask|Verify'`.
  No test may spawn a harness or reach the network. Test `headlessRender` and
  `ActorOutput` as pure functions.
- Full check once at the end: `make check`, then `gofmt -l .`.

## 7. Steps

1. **Prompt template.** Add `DefaultOutput`, the two-argument `headlessPrompt`
   and `headlessRender`. Switch `consult/ask.go:78` (with the new
   `Request.Output`) and `verify.go:241` (with `DefaultOutput`). Update
   `TestInlinePrompt`'s render to `headlessRender("")`. Add
   `TestHeadlessRenderNamesOutput`:
   - `headlessRender("plan")("Q")` contains `your plan, complete` and
     `Do not write a plan file`, and contains no `findings`;
   - `headlessRender("")("Q")` contains `your findings, complete`.

   If any existing consult or verify test pins the old prompt text, it should
   still pass, because the default wording is byte-identical to today's. Confirm
   that; do not edit such a test.
   Verify: `go test ./internal/consult/`.
2. **ActorOutput.** Create `actoroutput.go` and `actoroutput_test.go`. Table test `TestActorOutput`:
   - actor `lite-planner` with agent `architect` → `plan`;
   - actor `reviewer` with agent `reviewer` → `findings`;
   - actor `researcher` → `notes`;
   - actor `designer` with a custom source agent whose source has
     `output: design` → `design`. Build the source with the agentsrc format the
     package's own tests use; copy a minimal valid source from
     `internal/agentsrc`'s tests, or from `internal/relevo`'s custom-agent tests;
   - an actor with a native agent (no Source) → `findings`;
   - an actor not in `actors`, with definition `architect` → `plan` (the
     legacy/built-in path);
   - an unknown actor with an unknown definition → `findings`.

   Mutation check: make step 2 of the resolution (the `roles.Shipped` lookup)
   always miss, confirm `TestActorOutput` fails on the `lite-planner` case, then
   restore.
   Verify: `go test ./internal/relevo/ -run ActorOutput`.
3. **Wire it into Ask.** Add `consultOutput` if needed, and `Output` in the
   request. Verify with the focused command (existing Ask tests must pass).
4. **Full check and commit.** Run `make check` and `gofmt -l .`. Copy this plan to
   `docs/plans/2026-09-26-consult-output-prompt.md`. Commit:
   `consult: a reader actor is asked for its agent's output (plan, notes, ...) not always findings`.

## Report must include

`git diff --stat`, the mutation check and its result, the `make check`
result, and confirmation that none of the §1 "do not touch" files changed.
