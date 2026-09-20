# codex harness: `codex/openai/gpt-5.6-terra:high` as a builder

## 1. Goal

Add `codex` (OpenAI's Codex CLI, `codex-cli 0.155.1` on this machine) as a
harness kind relay can run, so that

```json
{ "harness": "codex", "provider": "openai", "model": "gpt-5.6-terra:high",
  "roles": ["builder", "reviewer"] }
```

is a valid candidate, `relay add --headless` / `relay send` run a round on
it, `relay ask` can consult it, and its plan-executor dispatches research
sub-agents that run `gpt-5.6-luna` at `medium` effort.

Everything below was checked against `codex exec --help`, the codex config
reference (`learn.chatgpt.com/docs/config-file/config-reference`) and two
live probes on 2026-09-19 (`codex exec --json`, one plain and one spawning
a sub-agent role declared through `-c agents.researcher.*`). Nothing is
inferred from other harnesses.

## 2. Non-goals

- Pane-mode usage accounting for codex (reading `~/.codex/sessions`).
  Headless usage is in scope (§8); pane usage returns the note
  `codex pane usage not read` and is a follow-up.
- Wiring codex sub-agents for reviewer or architect. Only plan-executor
  declares `[agents.researcher]`.
- Validating the reasoning-effort vocabulary. It is model-dependent
  (`gpt-5.6-luna` accepts `none|low|medium|high|xhigh|max` and rejects
  `minimal` with an HTTP 400 the stream shows as `turn.failed`), so relay
  passes the suffix through and lets codex refuse.
- `agy`, `claude`, `opencode` behaviour. At `TierHarness` every existing
  kind's `Launch` result stays byte-identical; the existing tests enforce it.

## 3. Candidate and launch

### 3.1 Effort suffix

For kind `codex` only, the candidate's `model` is `<id>[:<effort>]`, split
on the LAST `:`. `SplitEffort(model) (id, effort string, err error)`: `"gpt-5.6-terra:high"`
is `("gpt-5.6-terra", "high", nil)`; `"gpt-5.6-terra"` is `("gpt-5.6-terra", "", nil)`. An
empty id (`":high"`) or an empty effort with a colon present
(`"gpt-5.6-terra:"`) is `ErrBadModel` from `Launch` -- a new sentinel
beside `ErrTierUnsupported`, `errors.New("bad model")` -- which `candidate.Load`
already surfaces as a load error the way it does a bad tier. The token
keeps the suffix: `codex/openai/gpt-5.6-terra:high`, so a later
`...:medium` candidate is a distinct token for `relay unavailable`, policy
order and the usage ledger. `candidate.ParseRef` splits on `/` only, so
`:` needs no change there.

`SplitEffort` is exported from `internal/harness` because `internal/usage`
needs the bare id for price lookup (§8).

### 3.2 argv

With `id, effort := SplitEffort(model)`, `p := provider`, `r :=
role.Definition`:

Print (headless) form, `PromptAt = 1`:

```
exec <prompt> -p <r> -m <id> -c model_provider=<p> [-c model_reasoning_effort=<effort>] --json -C <dir>
```

`-c model_reasoning_effort=<effort>` is present only when effort != "".
`<dir>` is `DirPlaceholder`, filled by `PrintArgs`; codex's `-C` makes the
worktree the workspace root, the same fix agy needed in #192. There is no
`BudgetPlaceholder`: codex has no timeout flag, and relay's own round
budget already kills the process. `--skip-git-repo-check` is not passed:
every relay worktree is a git checkout, and a `--cwd` binding on a
non-repo is the user's choice to surface. `--ephemeral` is not passed:
codex's session files are what a later pane-usage reader will read.
`proc` already runs the process with `Stdin = nil`
(`internal/proc/proc.go:111`), so the "Reading additional input from
stdin" path codex takes on a tty is never entered.

Pane (interactive) form, what herdr's `agent start --kind codex --` gets:

```
-p <r> -m <id> -c model_provider=<p> [-c model_reasoning_effort=<effort>]
```

In both forms the tier's `PermissionArgs` follow the base form and
`extra_args` follow those, exactly as for the other kinds.

`model_provider=openai` is passed even though it is codex's default, so a
future `codex/<other>/...` candidate works with no launch change and the
argv says what it runs.

### 3.3 knownHarnesses entry

```
Kind "codex", Binary "codex", Integration "codex", MinVersion "0.155.0",
SubAgents SubAgentsHidden, DocExt "toml", Roles:
  plan-executor  .codex/plan-executor.config.toml  Doc plan-executor.codex  ExpectModel ""
  researcher     .codex/researcher.config.toml     Doc researcher.codex     ExpectModel "gpt-5.6-luna"
  reviewer       .codex/reviewer.config.toml       Doc reviewer.codex       ExpectModel ""
  architect      .codex/architect.config.toml      Doc architect.codex      ExpectModel ""
```

`MinVersion 0.155.0` is the version the probes ran against; `-p` profile
files, `[agents.*]` roles and `--json` are all present there.
`SubAgentsHidden`: a spawned role is a thread inside the same `codex exec`
process, reported as `collab_tool_call` items on the parent's stream; herdr
sees one pane and no extra agent (observed 2026-09-19).

`DocExt` is a new `Harness` field: the extension of the shipped definition
file under `internal/harness/agents/`; `""` means `md`. Only codex sets
it. `AgentDoc` reads `agents/<Doc>.<ext>`, the embed pattern becomes
`agents/*.md agents/*.toml`.

## 4. Permission tiers

`PermissionArgs("codex", tier)`:

| tier | args |
|------|------|
| harness | none (`~/.codex/config.toml` decides) |
| read | refuse (#230: read-only cannot write the report) |
| edit | `-s workspace-write -c sandbox_workspace_write.writable_roots=["<binding state dir>"]` |
| yolo | `--dangerously-bypass-approvals-and-sandbox` |

One refusal cell, `read`, since #230. `codex exec` is non-interactive: a command the sandbox
does not allow fails inside the run rather than raising a dialog, which is
relay's `permission-blocked` outcome (#141), detected through
`DenialPatterns` (§6).

`PermissionFlags("codex")`: `-s`, `--sandbox`, `-a`, `--ask-for-approval`,
`--full-auto`, `--approve-for-me`,
`--dangerously-bypass-approvals-and-sandbox`. Matching is the existing
whole-element-or-`flag=` rule. A `-c sandbox_mode=...` or `-c
approval_policy=...` pair in `extra_args` is NOT detected: `-c` is a
generic override and the two-element shape does not fit the matcher;
documented in the `PermissionFlags` comment as the known gap, same class
as claude's `--settings`.

## 5. Role definitions: one profile per role

Codex has no `--agent`. It has profiles: `-p <name>` layers
`$CODEX_HOME/<name>.config.toml` over `~/.codex/config.toml`. relay ships
one profile per role and selects it with `-p <role>`.

Each profile is a TOML file whose role text is `developer_instructions`
(appended to codex's built-in prompt -- `model_instructions_file` would
replace that prompt and its tool/sandbox guidance, and is not used). The
text is a TOML multi-line literal string (`'''...'''`), so no escaping;
none of the four role bodies contains `'''`.

### 5.1 Files

`~/.codex/plan-executor.config.toml`:

```toml
# relay plan-executor role for codex. Installed by `relay agent install
# --kind codex`; select with `codex -p plan-executor`.
[agents.researcher]
config_file = "researcher.config.toml"
description = "Read-only codebase research: locates code, traces conventions, answers questions. Never edits."

developer_instructions = '''
<body of plan-executor.claude.md, frontmatter dropped, with the one
dispatch paragraph reworded for codex: "Dispatch every research
sub-agent with the spawn_agent tool as the `researcher` role, then
wait on it." replacing the Agent-tool / subagent_type wording>
'''
```

`config_file` is relative and resolves from the profile's own directory,
so it names the researcher profile below: one file is both the `-p
researcher` consult profile and the sub-agent layer.

`~/.codex/researcher.config.toml`:

```toml
model = "gpt-5.6-luna"
model_reasoning_effort = "medium"
sandbox_mode = "read-only"

developer_instructions = '''
<body of researcher.claude.md, frontmatter dropped>
'''
```

The pin is the point: a researcher spawned by any codex plan-executor
runs luna/medium regardless of the candidate's `-m`. Used as a consult
through `relay ask`, the candidate's `-m` on the command line still wins
over the profile's `model`, which is codex's documented precedence.

`~/.codex/reviewer.config.toml` and `~/.codex/architect.config.toml`: no
model keys, `sandbox_mode = "read-only"` on reviewer only, and the
respective `.claude.md` body as `developer_instructions`.

### 5.2 What the probe showed

`codex exec --json -c 'agents.researcher.config_file="…/researcher.toml"'
-c 'agents.researcher.description="…"' "Spawn a sub-agent with the
researcher role …"` on 0.155.1: the parent emitted `collab_tool_call`
items for `spawn_agent` and `wait`; the child ran with the role file's
`developer_instructions`, `model` and `model_reasoning_effort` (its reply
began with the sentinel the instructions asked for). No
`agents.enabled` or feature flag was needed.

## 6. Patterns

Shipped as defaults on the `codex` entry, all `(?i)`, unverified against
a real limit or denial line and to be replaced when one is observed
(same discipline as the other kinds):

- Limit: `usage limit`, `rate limit`, `quota`, `"status": 429`,
  `too many requests`.
- Denial: `(command|operation|write) (was )?(rejected|denied|blocked)`,
  `sandbox.*(denied|blocked|not permitted)`, `not permitted`,
  `permission denied`.
- Dialog: `defaultDialogPatterns`.

The three `Test*PatternsSetOnEveryKind` tests cover the new entry with no
change.

## 7. Transcript rendering

`transcript.Render("codex", line)` renders the `codex exec --json` stream.
Event shapes, from the 2026-09-19 capture (kept as
`internal/transcript/testdata/codex.jsonl`):

| event | rendering |
|-------|-----------|
| `thread.started`, `turn.started` | nothing |
| `item.started` (any item) | nothing -- codex repeats the item on `item.completed` with the result |
| `item.completed` item.type `agent_message` | `item.text` |
| `item.completed` item.type `command_execution` | `toolLine("bash", {command})`, then `okLine(aggregated_output)` when `exit_code` is 0, else `errLine("exit <code>: " + aggregated_output)` |
| `item.completed` item.type `file_change` | `toolLine("edit", {path})` per entry in `item.changes` (each has `path` and `kind`), `[file_change]` if the list is empty |
| `item.completed` item.type `reasoning` | nothing |
| `item.completed` item.type `error` | `errLine(item.message)` |
| `item.completed` item.type `collab_tool_call` | `toolLine(item.tool, {prompt})` for `spawn_agent`; for `wait`, one `okLine(message)` per `agents_states` entry whose `status` is `completed` (sorted by thread id for a stable order); other tools `[collab_tool_call <tool>]` |
| `turn.completed` | nothing (usage is §8's job) |
| `turn.failed` | `errLine(error.message)` |
| `error` (top level) | `errLine(message)` |
| anything else | `unknown(obj)` |

`file_change` was not in the capture (the probes were read-only); its
shape is from the codex reference and the renderer must not panic on any
variant of it -- `[file_change]` is the fallback.

## 8. Usage accounting (headless)

`usage.readStream` gains `case "codex": codexStream(f, src.Provider, id)`
where `id, _ = harness.SplitEffort(src.Model)`, so the sample's `Model` is
the bare id that `prices.json` keys on. One sample per `turn.completed`:

```
In         = usage.input_tokens - usage.cached_input_tokens
CacheRead  = usage.cached_input_tokens
CacheWrite = usage.cache_write_input_tokens
Out        = usage.output_tokens        (reasoning_output_tokens is a subset; not added again)
HasCost    = false                      (codex reports no cost)
```

OpenAI's `input_tokens` is the total including the cached part (probe:
34933 input, 26112 cached), hence the subtraction. A sub-agent's tokens
are folded into the parent turn's usage by codex and are attributed to the
candidate's model; the researcher's luna tokens are therefore priced as
terra. Noted in the reader's comment, accepted for now.

`usage.Source.Harness`'s comment lists the four kinds. `readPane` returns
`nil, "codex pane usage not read"` for codex.

## 9. Doctor

- `Integration "codex"` makes `relay doctor` report herdr's codex
  integration, which is `not installed` on this machine until
  `herdr integration install codex`.
- The model-pin check (`doctor.roleCheck`) reads a TOML pin for a kind
  whose `DocExt` is `toml`: `pinnedModel(h, raw)` returns
  `frontmatterModel(raw)` for md and, for toml, the value of the first
  top-level `model = "<v>"` line before any `[table]` header (`""` when
  none). The warning text `pins a tier; the candidate's --model is
  ignored` is agy's; for a codex mismatch the detail is `pins <model>;
  relay ships <ExpectModel>` and the fix is the existing
  `relay agent install --kind codex --role researcher --force`. The
  drift check (`DocEqual` against the shipped bytes) applies to the
  researcher row because its `ExpectModel` is set, and only to it, the
  existing rule.
- `Definitions` for the builder role (plan-executor + researcher) is
  unchanged and is what makes a missing `researcher.config.toml` a
  doctor finding even though the launch line never names it.

## 10. Install

`relay agent install --kind codex` writes the four profiles through the
existing `installOne`/`writeDoc` path; nothing there is md-specific. The
README's harness table gains a codex row (paths, `-p` selection, the
luna/medium researcher pin, the `:effort` suffix).

## 11. Testing

Pure-function tests in `internal/harness`, `internal/transcript`,
`internal/usage`, `internal/doctor`; no `cmd/relay` test executes a
subcommand (CI has no herdr).

- `TestSplitEffort`: the four cases in §3.1.
- `TestLaunchCodex`: print and pane argv for `gpt-5.6-terra:high` and for
  a bare id at each tier, byte-exact; `PrintArgs` fills `-C`.
- `TestLaunchCodexBadModel`: `":high"` and `"gpt-5.6-terra:"` return
  `ErrBadModel`.
- `TestPermissionFlagsCodex`: `-s workspace-write` in extra_args at tier
  edit is `ErrExtraArgsPermission`; at tier harness it passes.
- `TestAgentDocCodexIsToml`: string checks, no TOML parser: every codex
  role's shipped doc contains `developer_instructions = '''` and a closing
  `'''` after it, and no `'''` between; the plan-executor doc
  contains `[agents.researcher]` and `config_file = "researcher.config.toml"`;
  the researcher doc contains `model = "gpt-5.6-luna"` and
  `model_reasoning_effort = "medium"`. No TOML library is added to
  go.mod for this.
- `TestCodexTable`: one case per row of §7, plus a `file_change` with an
  empty `changes` list; `TestFixtures` renders `testdata/codex.jsonl`
  against `codex.log`.
- `TestCodexStream`: the capture plus one synthetic `turn.completed`
  yields two samples with the §8 arithmetic.
- `TestDoctorCodexResearcherPin`: researcher profile pinned to luna is clean;
  pinned to something else is a warn with the §9 text; plan-executor with
  no pin is clean.
- Existing `Test*SetOnEveryKind`, `TestLaunch*` for the other kinds and
  `TestCanServe` run unchanged and must stay green.

Mutation checks the round's report must name: remove the `-C` element and
`TestLaunchCodex` fails; drop the cached subtraction and `TestCodexStream`
fails; make `pinnedModel` ignore toml and `TestDoctorCodexResearcherPin` fails.

## 12. Live verification (planner, after the round)

1. `relay agent install --kind codex`, `herdr integration install codex`,
   `relay doctor` shows codex rows green.
2. Add the candidate to `~/.config/relay/candidates.json`, append the token
   to `order.builder`; `relay candidates` lists it with no note.
3. A headless round on a throwaway plan; `NNN-builder.log` shows
   `spawn_agent`/`wait` lines and the report arrives with the marker;
   `relay status` shows a usage row for `codex/openai/gpt-5.6-terra:high`.
