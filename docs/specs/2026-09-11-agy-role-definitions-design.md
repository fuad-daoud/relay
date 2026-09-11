# agy role definitions: `--agent` replaces the preamble

**Issue:** #85
**Amends:** `docs/specs/2026-09-11-candidates-design.md` §3.4 `RoleSpec`, §3.5
`Harness`, §4.7 `composePrompt`, §4.8 `Ask`; README "How relay
launches one", "Emit the builder's role definitions", and the rebind note on
re-sending the preamble.

## 1. System overview

relay runs three roles -- `builder` (definition `plan-executor`), `reviewer`,
`researcher` -- on three harness kinds. For claude and opencode relay ships an
agent definition per role, `relay agent print` emits it, `relay doctor` checks
it landed, and `Harness.Launch` selects it with `--agent`. For agy relay ships
nothing: the harness table records agy as having no `--agent` flag, and the
role is selected by a sentence prepended to the first prompt --
"Activate your 'plan-executor' skill …" -- naming a skill relay neither ships
nor checks for.

The premise is stale. agy added `--agent` and the `agent`/`agents` subcommand
in 1.1.1 and Markdown agent definitions in 1.1.6 (agy changelog; the machine
this was written on runs 1.2.1). Its definition format is a YAML frontmatter
over an H1-sectioned Markdown body, discovered globally in
`~/.gemini/config/agents/<name>.md` or per workspace in
`.agents/agents/<name>.md`, with these fields (agy docs, "Custom Subagents"):

| field | type | default | meaning |
| --- | --- | --- | --- |
| `name` | string | required | identifier `--agent` and `invoke_subagent` select by |
| `description` | string | required | what the planner reads when deciding to delegate |
| `tools` | string[] | `[]` | **allowlist** of tools the agent may call |
| `mainAgent` | bool | true | selectable as the session's primary agent |
| `subagent` | bool | true | invocable via `invoke_subagent` by a primary |
| `model` | string | `inherit` | model **tier**: `inherit`, `flash`, `pro` |
| `commandExecutionPolicy` | string | `sandbox` | shell policy: `off`, `auto`, `eager`, `sandbox` |
| `skills`, `plugins`, `mcpServers` | list | `[]` | dependencies; unused here |

Three of those change what relay can promise on agy compared with the other
two kinds:

1. **`tools` is an allowlist.** The claude and opencode `researcher`
   definitions are read-only because their prose says so. An agy researcher
   whose `tools` omits every write and edit tool is read-only because the
   harness refuses the call. relay's one-writer-per-tree invariant (README,
   `TestPlanExecutorDefinitionsForbidWritingSubAgents`) becomes structural on
   the harness CLAUDE.md lists first.
2. **`model` is a tier, not a model id.** claude and opencode definitions pin
   a concrete model "as a worked example" and relay's `--model` on the launch
   line is what actually runs. On agy a `flash` or `pro` pin in the file would
   override the candidate the planner chose, silently. The agy definitions
   pin `inherit`, and `doctor` warns when an installed copy pins anything
   else.
3. **`subagent`/`mainAgent` are per-definition switches.** `plan-executor` is
   `mainAgent: true, subagent: false`: it may only ever be a session's
   primary, so no agent can dispatch a second writer into the tree.
   `researcher` and `reviewer` are both `true`: relay starts either as a
   primary via `relay ask --role`, and plan-executor dispatches researchers as
   sub-agents.

This design ships `plan-executor.agy.md`, `researcher.agy.md` and
`reviewer.agy.md`; launches agy with `--model <model> --agent <definition>`
exactly as claude is launched; brings agy under `doctor` and `agent print`;
and deletes the preamble machinery -- `RoleSpec.Preamble`,
`Harness.SelectsRoleByPreamble`, `Launch.Preamble`, `Binding.PreamblePending`,
and the branches in `send.go` and `ask.go` that consult them -- because
nothing selects a role by preamble any more. A version floor for agy is
added to `doctor` so a machine whose agy predates Markdown agents fails
loudly instead of starting a role-less builder.

The session-flip that #66 observed on agy during sub-agent dispatch is
untouched: #71's identity-by-name and `effectiveStatus` remain, and this
design neither depends on nor changes them.

### Why delete rather than version-gate

The preamble path could stay behind `agy < 1.1.6`. It would keep
`PreamblePending` in the store, two branches in `composePrompt`, the
`SelectsRoleByPreamble` special case in `CanServe`, `doctor` and
`agent print`, and a role-less builder that the rest of relay's guarantees
do not hold for. The floor is a year of agy releases old at the time of
writing and `doctor` reports it with a fix. Nothing in relay's own history
argues for supporting a harness it cannot verify.

### Scope boundary

In scope: the three agy definitions and their table rows; `Launch` for agy;
removal of the preamble fields and branches; `doctor` rows for agy roles, the
`inherit` pin rule, and the agy version floor; `agent print` for agy; the
pin test extended to agy; README and candidates-spec amendments.

Out of scope, unchanged: #66/#71 session handling; sub-agent visibility to
herdr (#84); candidate scoring (#61); adopted panes, which get no role from
relay under either design.

## 2. File structure

```
internal/harness/agents/plan-executor.agy.md   new: agy twin of plan-executor
internal/harness/agents/researcher.agy.md      new: agy twin of researcher, tools allowlist
internal/harness/agents/reviewer.agy.md        new: agy twin of reviewer, tools allowlist
internal/harness/harness.go                    agy Roles, MinVersion; drop Preamble, SelectsRoleByPreamble, Launch.Preamble; ExpectModel on Role
internal/harness/harness_test.go               launch matrix: agy renders --agent; SelectsRoleByPreamble test removed
internal/harness/harness_role_test.go          agy carries three roles, not none
internal/harness/agents.go                     unchanged (AgentDoc already reads by table)
internal/harness/agents_test.go                pin test covers agy; AgentDoc(plan-executor, agy) now resolves
internal/store/types.go                        Binding.PreamblePending removed; legacy key ignored
internal/store/store_test.go                   legacy preamble_pending key is dropped on load, not an error
internal/relay/send.go                         composePrompt loses the preamble branch
internal/relay/send_test.go                    round 1 and post-rebind prompts carry no preamble
internal/relay/ask.go                          consult prompt is the consult prompt, nothing prepended
internal/relay/ask_test.go                     agy consult prompt has no preamble
internal/relay/bind.go                         rebind no longer sets PreamblePending; doc comment updated
internal/doctor/doctor.go                      agy role rows via roleCheck; ExpectModel warn; harness version floor row
internal/doctor/env.go                         Env.BinaryVersion
internal/doctor/doctor_test.go                 agy rows, inherit warn, version floor rows
cmd/relay/agent.go                             agy accepted; usage strings list three kinds
cmd/relay/agent_test.go                        agy print emits the embedded doc
README.md                                      launch table, install block, rebind note, agent print usage
docs/specs/2026-09-11-candidates-design.md     one-line amendment note at §3.4, §3.5, §4.7, §4.8
```

## 3. Data structures and type definitions

### 3.1 `harness.RoleSpec` (modified)

```
RoleSpec
  Name       string     relay's role name: builder | reviewer | researcher
  Shape      RoleShape  builder | consult
  Definition string     agent definition name every kind selects with --agent
```

`Preamble` is removed. No other field changes.

### 3.2 `harness.Role` (modified)

```
Role
  Name        string  definition name as the user types it
  Path        string  home-relative install path
  Doc         string  embedded file stem "<role>.<kind>"
  ExpectModel string  "" = any pin is fine; otherwise the only pin doctor accepts without a warning
```

`ExpectModel` is `"inherit"` on every agy row and `""` on every claude and
opencode row.

### 3.3 `harness.Harness` (modified)

```
Harness
  Kind        string
  Binary      string
  Integration string
  Roles       []Role   never empty for a known kind
  MinVersion  string   semver floor for the binary; "" = unchecked
```

`SelectsRoleByPreamble` is removed. `MinVersion` is `"1.1.6"` for agy and
`""` for claude and opencode.

agy's table entry:

```
Kind: "agy", Binary: "agy", Integration: "antigravity-cli", MinVersion: "1.1.6",
Roles:
  {Name: "plan-executor", Path: ".gemini/config/agents/plan-executor.md", Doc: "plan-executor.agy", ExpectModel: "inherit"}
  {Name: "researcher",    Path: ".gemini/config/agents/researcher.md",    Doc: "researcher.agy",    ExpectModel: "inherit"}
  {Name: "reviewer",      Path: ".gemini/config/agents/reviewer.md",      Doc: "reviewer.agy",      ExpectModel: "inherit"}
```

Order is plan-executor first, as for the other kinds, because `doctor` reports
the role relay's loop depends on before the rest.

### 3.4 `harness.Launch` (modified)

```
Launch
  Kind string
  Args []string
```

`Preamble` is removed.

### 3.5 `store.Binding` (modified)

`PreamblePending bool \`json:"preamble_pending,omitempty"\`` is removed. A
binding file on disk carrying the key loads with the key ignored, the same
treatment #81 gave `builder_alias`.

### 3.6 The three agy definitions

Frontmatter, exactly:

```
plan-executor.agy.md
  name: plan-executor
  description: <same text as plan-executor.claude.md>
  mainAgent: true
  subagent: false
  model: inherit
  commandExecutionPolicy: auto

researcher.agy.md
  name: researcher
  description: <same text as researcher.claude.md>
  mainAgent: true
  subagent: true
  model: inherit
  commandExecutionPolicy: sandbox
  tools: <agy's read-only tool names -- see §7.4>

reviewer.agy.md
  name: reviewer
  description: <same text as reviewer.claude.md>
  mainAgent: true
  subagent: true
  model: inherit
  commandExecutionPolicy: sandbox
  tools: <same list as researcher>
```

Body: the claude twin's body with three substitutions. (a) H1 section
headings replace the SHOUTING-CAPS section labels, because agy delimits
sections by H1. (b) "pass `subagent_type: researcher` to the Agent tool"
becomes "invoke the `researcher` agent with `invoke_subagent`". (c) The
"CHOOSING THE MODEL" paragraph is replaced by one that says `inherit` is
not an example but a requirement: relay passes the model on the launch line
and a tier pinned here would override it. The one-writer sentence the pin
test looks for is carried verbatim into plan-executor.agy.md.

## 4. Interface definitions and component contracts

### 4.1 `harness.Harness.Launch(provider, model string, extra []string, role RoleSpec) Launch`

Responsibility: render argv for one role on one kind.

```
claude:   --model <model> --agent <role.Definition>
opencode: --agent <role.Definition> -m <provider>/<model>
agy:      --model <model> --agent <role.Definition>
```

followed by `extra` verbatim. Postcondition: `Args` never empty for a known
kind; unknown kind renders `extra` only, as today. No error.

### 4.2 `harness.Harness.CanServe(role string) bool`

Responsibility: whether this kind can run the named role. Now uniformly
"role is in the role table AND `h.Role(spec.Definition)` is found". The
preamble short-circuit is gone; the truth table for agy is unchanged
(true for all three roles) because agy now has all three rows.

### 4.3 `harness.AgentDoc(role, kind string) ([]byte, error)`

Unchanged in signature and body. `AgentDoc("plan-executor", "agy")` now
returns the embedded doc instead of `ErrNoAgentDoc`; the test case asserting
the opposite is removed.

### 4.4 `doctor.Env.BinaryVersion(ctx context.Context, path string) (string, error)`

New. Responsibility: run `<path> --version` and return the trimmed first line
of stdout. Errors: exec failure, non-zero exit, empty output -- each returned
as an error; the caller renders a probe-failed row. Precondition: `path` came
from `LookPath`. The fake `Env` in tests gets a `versions map[string]string`.

### 4.5 `doctor.roleCheck(env Env, kind string, r harness.Role) Check`

Modified. After the existing missing/present logic, when the file is present,
readable, `r.ExpectModel != ""`, and the parsed pin is non-empty and differs
from `r.ExpectModel`: severity `SevWarn`, detail
`"<path> (model: <pin>) -- pins a tier; the candidate's --model is ignored"`,
fix `"set model: inherit in <path>"`. A missing pin is not a warning: agy's
default is `inherit`.

### 4.6 `doctor.Run` -- per-kind rows

Modified. For a known kind with `MinVersion != ""` and a binary found on
PATH, one row `Group: kind, Name: "version"`:

- probe failed: `SevWarn`, `ProbeFailed: true`, detail names the error
- unparseable: `SevWarn`, `"unparseable version %q"`
- below floor: `SevFail`, `"%s (below floor %s)"`, fix `"upgrade agy to >= 1.1.6"`
- at or above: `SevOK`, `"%s (floor %s)"`

The `len(h.Roles) == 0` branch and its "selected by preamble" row are
deleted; every known kind goes through `roleCheck` per row.

### 4.7 `relay.composePrompt(rt Runtime, b store.Binding, planPath, reportPath string) (string, error)`

Modified. Returns `fmt.Sprintf(builderPrompt, b.Round, planPath, reportPath)`
and nil, unconditionally. `rt` and the candidate lookup are no longer used;
drop the parameter if nothing else in the file needs it, keep the signature
if the call sites make that churn (builder's call).

### 4.8 `relay.Ask` -- consult prompt

Modified. The text sent is `fmt.Sprintf(consultPrompt, askPath, findingsPath)`
with nothing prepended.

### 4.9 `relay.Bind` -- rebind branch

Modified. `b.PreamblePending = true` is removed. Doc comment's postcondition
list drops "PreamblePending is true". Everything else in the branch stays.

### 4.10 `cmd/relay agent print --kind agy --role <r>`

Modified. The agy refusal is removed; the command resolves through `AgentDoc`
like any kind. Usage strings read `--kind <agy|claude|opencode>`.

## 5. High-level pseudocode

### 5.1 Launch

```
bind/add/fork/ask resolve candidate c and role spec r
h := harness.Lookup(c.Harness)
l := h.Launch(c.Provider, c.Model, c.ExtraArgs, r)
herdr.StartAgent(name, l.Kind, pane, l.Args)      // agy now gets --agent
prompt := role's prompt template                   // no preamble on any kind
```

### 5.2 doctor, per known kind with a binary on PATH

```
if h.MinVersion != "":
    v, err := env.BinaryVersion(ctx, binPath)
    emit version row per §4.6
for r in h.Roles:
    emit roleCheck(env, kind, r)                   // §4.5 adds the ExpectModel warn
```

### 5.3 Store load

```
decode binding JSON; unknown key preamble_pending is ignored by encoding/json
```

No migration; the field's absence is its zero value and nothing reads it.

## 6. Error handling strategy

No new error types. `BinaryVersion` errors are absorbed by `doctor` into a
probe-failed row, matching how `HerdrVersion` failures are handled.
`composePrompt` can no longer fail on candidate lookup; its remaining error
return, if kept for signature stability, is always nil. `agent print` for an
unknown role on agy reports `kind "agy" has no role "x" (known: [...])`
through the existing branch.

## 7. Behavioural rules and their rationale

### 7.1 Every known kind selects its role with `--agent`

There is one launch shape. A kind that cannot do this is not a kind relay
runs. Rationale: a role relay cannot select is a role relay cannot verify,
and the harness at the top of the dispatch order was that kind.

### 7.2 agy definitions pin `model: inherit`, and doctor warns on anything else

The candidate token names the model; relay passes it with `--model`. On agy
the definition's `model` is a tier that would override the launch line. The
pin is `inherit` so the token stays true, and `doctor` reports the drift
because a user editing the pin "like the README says to for the other
kinds" would otherwise be running a model `relay status` does not show.

### 7.3 plan-executor is never a sub-agent

`subagent: false` on `plan-executor.agy.md`. A plan-executor invoked as a
sub-agent by another plan-executor is a second writer in the tree. On claude
and opencode this is prose in the definition; on agy it is a switch the
harness enforces. The pin test keeps checking the prose too, on all three.

### 7.4 Every agy definition carries a `tools` allowlist

- Observed on agy 1.2.1 (2026-09-11): a definition in
  `~/.gemini/config/agents/` with no `tools:` gets a read-mostly default with
  no write, no `run_command`, no `invoke_subagent` (#91). One with `tools:`
  gets exactly that list plus `send_message` and `manage_task`. One naming a
  tool the registry does not have fails at `failed to construct executor`,
  so the agent never starts. A definition in a workspace `.agents/agents/`
  directory ignores `tools:` entirely; relay does not install there.
- Therefore all three definitions carry an explicit list. `plan-executor`
  lists the writer's set (read, search, `write_to_file`,
  `replace_file_content`, `multi_replace_file_content`, `run_command`,
  `invoke_subagent`, `manage_subagents`). `researcher` and `reviewer` list
  `view_file grep_search find_by_name list_dir run_command`;
  `view_file_outline`, `view_code_item`, `command_status`, `read_terminal`
  were in the original list and do not resolve.
- `TestAgyAllowlistsResolve` pins every listed name to a set confirmed from
  a live run; the stream-json `init` event's `tools` array is not that set.
- `relay doctor` warns when an installed definition differs from the
  shipped one, so a fix to a definition is not silently absent from the
  machine, on agy only -- a kind whose definition relay owns because
  `model:` must stay `inherit`. On claude and opencode the installed copy
  is the user's to edit (the README tells them to repin `model:`), and
  doctor does not compare it.

### 7.5 No preamble, on any kind, on any round

Round 1 and post-rebind prompts are the plan prompt alone. Rationale: the
preamble existed only for agy; with agy on `--agent`, "re-send the preamble
after rebind" describes nothing. Removing the field removes a state bit the
daemon and `send` had to agree on.

### 7.6 The version floor is a `doctor` fail, not a `bind` refusal

`bind` starts an agy candidate whatever the installed version; `doctor` is
the preflight and reports `1.1.6` as the floor with an upgrade fix.
Rationale: consistent with how herdr's floor is treated; `bind` does not
probe binaries today and this design does not make it start.

## 8. Testing requirements

All in `internal/harness`, `internal/doctor`, `internal/relay`,
`internal/store`; the `cmd/relay` test for `agent print --kind agy` runs a
pure function that reads the embed FS and does not reach herdr.

- `TestLaunch` matrix: agy builder renders `[--model M --agent plan-executor]`;
  agy reviewer/researcher render their definition names; extra args follow.
- `TestAgyCarriesThreeRoles` replaces the test asserting `Roles` is nil.
- `TestAgentDocResolvesEveryTableRole` now covers nine pairs with no change
  to its body; `TestAgentDocRejectsUnknownPairs` loses the agy case.
- `TestPlanExecutorDefinitionsForbidWritingSubAgents` iterates
  `{claude, opencode, agy}`. Mutation: delete the one-writer sentence from
  `plan-executor.agy.md`, this test fails.
- `TestAgyDefinitionsFrontmatter`: for each agy doc, frontmatter has
  `name:` equal to the role, `model: inherit`; plan-executor has
  `subagent: false` and a `tools:` list containing `write_to_file`,
  `replace_file_content`, `run_command`, `invoke_subagent`; researcher and
  reviewer have a non-empty `tools:` list containing no entry matching
  `write|edit|delete|invoke_subagent`. Mutation: add `write_to_file` to
  researcher's list, this test fails.
- `TestAgyAllowlistsResolve`: every name in every agy definition's `tools:`
  list is in `agyKnownTools`. Mutation: add `view_file_outline` to
  researcher's list, this test fails.
- `doctor`: agy with all three files present and `inherit` -> three OK rows;
  one file pinning `pro` -> one Warn row naming the fix; version `1.2.1` ->
  OK; `1.1.5` -> Fail; `garbage` -> Warn; probe error -> Warn ProbeFailed.
  claude and opencode get no version row. An agy role file whose installed
  bytes differ from what relay ships -> one Warn row naming the drift and
  the `agent print` fix; identical bytes, or identical bytes plus a
  trailing newline, -> OK. A claude definition that differs from shipped
  stays OK -- the drift check runs only when `Role.ExpectModel != ""`, so
  it never applies to claude or opencode; drop that guard and this
  subtest fails alongside `TestDoctorClaudeRoleNeverWarnsOnAPin`.
- `composePrompt`: round 1 on an agy binding returns the bare plan prompt;
  a post-rebind binding likewise. The fake candidate set is not consulted
  (assert the fake's lookup count is zero, or drop the fake).
- `Ask`: the prompt sent to an agy consult pane is exactly the consult
  prompt.
- `store`: a binding JSON with `"preamble_pending": true` loads without
  error and round-trips without the key.

## 9. Explicitly out of scope

- Any change to `SameAgent`, `effectiveStatus`, or the #66 session flip.
- Recording agy's sub-agent visibility shape; that belongs to #84.
- A `bind`-time version probe.
- Workspace-local `.agents/agents/` installs. relay installs globally, as it
  does for the other kinds; a user who prefers workspace-local copies runs
  `agent print` into that path themselves.
- Declaring `agents:` on plan-executor. Both definitions are global and
  `subagent: true` is the documented discovery mechanism; if step 1 shows
  agy 1.2.1 does not offer a global sub-agent without the declaration, that
  is a one-line frontmatter addition recorded in the plan, not a design
  change.
