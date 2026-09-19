# harness: codex as a builder kind -- `codex/openai/gpt-5.6-terra:high`

Spec: `docs/specs/2026-09-19-codex-harness-design.md`. It is not on main
yet: read it from the planner's checkout at
`/home/fuad/projects/relay/docs/specs/2026-09-19-codex-harness-design.md`,
copy it byte for byte to the same relative path in your worktree, and
commit it with the work. This plan argues from it and cites its sections
as §N.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. A halt that surfaces a design error is the
wanted outcome; an improvised workaround is not.

**Scope guard.** Touch only the files listed in §2. Do not run `make e2e`.
Do not add a module dependency (no TOML library: `go.mod` must not change).
Do not run `codex` itself -- every fact about its output is in this plan and
the spec; the planner verifies live after the round. Do not modify any
`*.agy.md`, `*.claude.md` or `*.opencode.md` definition. Do not widen an
exported signature that exists today; you may add new exported names listed
in §4. Regenerate `internal/transcript/testdata/codex.log` only with the
test's own `-update` flag and inspect it before committing. Every existing
test must stay green unchanged except the ones §7 names as extended.

**Commits.** Squash to ONE commit before the gate, subject
`feat(harness): codex kind -- profiles per role, :effort suffix, exec stream`.

**Commit the plan with the work.** Copy this plan file to
`docs/plans/2026-09-19-codex-harness.md` in your worktree and include it.

**Before step 1**: `git status --short` and `git branch --show-current`; you
must be on `relay/<binding-name>` under `~/.local/state/relay/.worktrees/`
with a clean tree. Otherwise halt.

## 1. System overview

relay knows three harness kinds (`agy`, `claude`, `opencode`), each a row
in `internal/harness.knownHarnesses` with a launch argv shape, a permission
tier table, shipped Markdown agent definitions selected with `--agent`, a
transcript renderer for its headless JSON stream, and a usage reader. This
round adds a fourth, `codex` (OpenAI Codex CLI 0.155), whose differences
are: roles are selected with `-p <profile>` (a TOML file at
`~/.codex/<role>.config.toml`, not Markdown), reasoning effort is a config
override carried as a `:effort` suffix on the candidate's model, and its
research sub-agents run as a codex `[agents.researcher]` role pinned to
`gpt-5.6-luna` at `medium`. Nothing about the other three kinds changes.

## 2. File structure

```
internal/harness/harness.go                 modify: DocExt field, codex row, Launch case, ErrBadModel, SplitEffort
internal/harness/tier.go                    modify: PermissionArgs and PermissionFlags codex cases
internal/harness/agents.go                  modify: embed pattern, AgentDoc extension
internal/harness/agents/plan-executor.codex.toml   create
internal/harness/agents/researcher.codex.toml      create
internal/harness/agents/reviewer.codex.toml        create
internal/harness/agents/architect.codex.toml       create
internal/harness/harness_test.go            modify: TestTableExactValues, TestLaunchPrintPerKind, definitionBody; add TestSplitEffort, TestLaunchCodex, TestLaunchCodexBadModel
internal/harness/tier_test.go               modify: TestPermissionArgsTable, TestLaunchRefusesExtraArgsPermissionFlag
internal/harness/agents_test.go             modify: one-writer loop adds codex; add TestAgentDocCodexIsToml
internal/transcript/transcript.go           modify: Render case "codex"
internal/transcript/codex.go                create: renderCodex
internal/transcript/testdata/codex.jsonl    create (fixture in §8)
internal/transcript/testdata/codex.log      create via -update
internal/transcript/transcript_test.go      modify: add TestCodexTable
internal/usage/source.go                    modify: Harness comment, readStream and readPane codex cases
internal/usage/codex.go                     create: codexStream
internal/usage/testdata/codex-stream.jsonl  create (fixture in §8 plus one synthetic line)
internal/usage/codex_test.go                create: TestCodexStream, TestCodexStreamEmpty
internal/doctor/doctor.go                   modify: pinnedModel, roleCheck uses it, codex mismatch wording
internal/doctor/doctor_test.go              modify: add TestDoctorCodexResearcherPin, TestPinnedModelToml
README.md                                   modify: launch table row, tier table row, harness lists, first-run paragraph
docs/specs/2026-09-19-codex-harness-design.md  create: copied from the planner checkout, unchanged
docs/plans/2026-09-19-codex-harness.md      create: this file
```

## 3. Data structures

`harness.Harness` gains one field, after `DenialPatterns`:

```go
// DocExt is the extension of this kind's shipped definition files under
// agents/; "" means "md". codex roles are TOML profiles (spec §5).
DocExt string
```

`harness.Role` is unchanged. The codex row (spec §3.3), to be added to
`knownHarnesses` AND, byte-identical, to `TestTableExactValues`'s expected
map:

```go
"codex": {
	Kind:        "codex",
	Binary:      "codex",
	Integration: "codex",
	MinVersion:  "0.155.0",
	// Observed 2026-09-19, codex-cli 0.155.1: a spawned [agents.*] role is
	// a thread inside the same `codex exec` process, reported on the
	// parent's stream as collab_tool_call items; herdr sees one pane.
	SubAgents: SubAgentsHidden,
	// Codex CLI limit text and OpenAI 429 bodies; unverified against a pane; replace with the observed line when one is seen.
	LimitPatterns: []string{
		`(?i)usage limit`,
		`(?i)rate limit`,
		`(?i)quota`,
		`(?i)"status": 429`,
		`(?i)too many requests`,
	},
	DialogPatterns: defaultDialogPatterns,
	// Denial patterns for codex; unverified against a real denied round; replace with the observed line when one is seen.
	DenialPatterns: []string{
		`(?i)(command|operation|write) (was )?(rejected|denied|blocked)`,
		`(?i)sandbox.*(denied|blocked|not permitted)`,
		`(?i)not permitted`,
		`(?i)permission denied`,
	},
	DocExt: "toml",
	Roles: []Role{
		{Name: "plan-executor", Path: ".codex/plan-executor.config.toml", Doc: "plan-executor.codex"},
		{Name: "researcher", Path: ".codex/researcher.config.toml", Doc: "researcher.codex", ExpectModel: "gpt-5.6-luna"},
		{Name: "reviewer", Path: ".codex/reviewer.config.toml", Doc: "reviewer.codex"},
		{Name: "architect", Path: ".codex/architect.config.toml", Doc: "architect.codex"},
	},
},
```

## 4. Interfaces

New exported names, all in `internal/harness`:

```go
// ErrBadModel reports a candidate model relay cannot render for its kind.
var ErrBadModel = errors.New("bad model")

// SplitEffort splits a codex candidate model "<id>[:<effort>]" on its last
// ':' (spec §3.1). No colon: (model, ""). It does not validate the effort
// vocabulary, which is model-dependent. Returns ErrBadModel when the id is
// empty or a colon is present with an empty effort.
func SplitEffort(model string) (id, effort string, err error)
```

Changed behaviour, same signatures:

- `(Harness) Launch(provider, model string, extra []string, role RoleSpec, tier Tier) (Launch, error)`
  gains `case "codex"` and may now return `ErrBadModel` (wrapped:
  `fmt.Errorf("%w: codex model %q", ErrBadModel, model)`).
- `(Harness) PermissionArgs(tier)` and `PermissionFlags()` gain codex cases.
- `AgentDoc(role, kind)` reads `agents/<Doc>.<ext>` with ext = `h.DocExt`
  or `md`.
- `transcript.Render("codex", line)` dispatches to `renderCodex`.
- `usage.reader.Read` handles `Harness == "codex"` in both modes.
- `doctor.roleCheck` reads the pin through `pinnedModel(kind, raw)`.

New unexported names: `transcript.renderCodex(obj map[string]any) []string`,
`usage.codexStream(r io.Reader, provider, model string) []Sample`,
`doctor.pinnedModel(kind string, raw []byte) string`,
`doctor.tomlTopLevelModel(raw []byte) string`.

## 5. Pseudocode

### 5.1 Launch, codex case (harness.go, inside the `switch h.Kind`)

```
case "codex":
    id, effort, err := SplitEffort(model)
    if err != nil: return Launch{}, fmt.Errorf("%w: codex model %q", ErrBadModel, model)
    cfg := ["-p", role.Definition, "-m", id, "-c", "model_provider=" + provider]
    if effort != "": cfg += ["-c", "model_reasoning_effort=" + effort]
    base  = cfg
    print = ["exec", PromptPlaceholder] + cfg + ["--json", "-C", DirPlaceholder]
    promptAt = 1
```

The `SplitEffort` error check happens inside the switch, BEFORE
`PermissionArgs`, so a bad model is reported even at a refused tier.
`SplitEffort`:

```
if model == "": return "", "", ErrBadModel
i := strings.LastIndex(model, ":")
if i < 0: return model, "", nil
id, effort = model[:i], model[i+1:]
if id == "" || effort == "": return "", "", ErrBadModel
return id, effort, nil
```

### 5.2 PermissionArgs / PermissionFlags, codex (tier.go)

```
case "codex":
    read -> ["-s", "read-only"]; edit -> ["-s", "workspace-write"]
    yolo -> ["--dangerously-bypass-approvals-and-sandbox"]
    default -> fmt.Errorf("%w: codex cannot honour tier %s", ErrTierUnsupported, tier)
PermissionFlags: -s, --sandbox, -a, --ask-for-approval, --full-auto,
    --approve-for-me, --dangerously-bypass-approvals-and-sandbox
```

Add to the `PermissionFlags` doc comment a `codex:` line listing them and
the sentence: `A -c sandbox_mode=... or -c approval_policy=... pair is not
detected: -c is a generic override and the two-element shape does not fit
the whole-element matcher.` Also add the codex row to the table in the
`PermissionArgs` doc comment if one is written there.

### 5.3 AgentDoc (agents.go)

```
//go:embed agents/*.md agents/*.toml
ext := h.DocExt; if ext == "": ext = "md"
b, err := agentFS.ReadFile("agents/" + r.Doc + "." + ext)
```

### 5.4 renderCodex (transcript/codex.go) -- spec §7 table

```
switch str(obj["type"]):
  "thread.started", "turn.started", "turn.completed", "item.started": nil
  "turn.failed": [errLine(str(asMap(obj["error"])["message"]))]
  "error":       [errLine(str(obj["message"]))]
  "item.completed":
      item := asMap(obj["item"])
      switch str(item["type"]):
        "agent_message": text := str(item["text"]); if text == "" -> nil else [text]
        "reasoning": nil
        "error": [errLine(str(item["message"]))]
        "command_execution":
            call := toolLine("bash", map[string]any{"command": item["command"]})
            out := str(item["aggregated_output"])
            code, _ := item["exit_code"].(float64)      // null or absent -> 0
            if code == 0: [call, okLine(out)]
            else: [call, errLine(fmt.Sprintf("exit %d: %s", int(code), out))]
        "file_change":
            changes := asList(item["changes"]); if empty -> ["[file_change]"]
            one toolLine("edit", map[string]any{"path": c["path"]}) per change (asMap each; skip a non-map)
        "collab_tool_call":
            tool := str(item["tool"])
            "spawn_agent": [toolLine("spawn_agent", map[string]any{"prompt": item["prompt"]})]
            "wait": for each key of asMap(item["agents_states"]) in sorted order: st := asMap(v);
                    if str(st["status"]) == "completed": append okLine(str(st["message"]))
                    if none appended -> ["[collab_tool_call wait]"]
            other: ["[collab_tool_call " + tool + "]"]
        default: [unknown(item)]      // "[<item.type>]"
  default: [unknown(obj)]
```

`toolLine`, `okLine`, `errLine`, `str`, `asMap`, `asList`, `unknown` exist in
transcript.go; `toolLine` picks the main argument by `argKeys`, which
already lists `command`, `path` and `prompt`. Truncation happens inside
those helpers; do not truncate again. Head the file with a comment naming
the 2026-09-19 capture the way `opencode.go` does.

### 5.5 codexStream (usage/codex.go) -- spec §8

```
type codexUsage struct { Input `input_tokens`; Cached `cached_input_tokens`; CacheWrite `cache_write_input_tokens`; Output `output_tokens` }  // all int64
type codexEvent struct { Type string `type`; Usage *codexUsage `usage` }
scanLines(r, line -> unmarshal; if err != nil || Type != "turn.completed" || Usage == nil: return
    t := Tokens{In: Input - Cached, CacheRead: Cached, CacheWrite: CacheWrite, Out: Output}
    append Sample{Provider: provider, Model: model, Tokens: t})   // HasCost false
```

Comment on the reader: OpenAI's `input_tokens` includes the cached part
(probe 2026-09-19: 34933 input, 26112 cached), hence the subtraction;
`reasoning_output_tokens` is a subset of `output_tokens` and is not added;
a sub-agent's tokens are folded into the parent turn by codex and priced
as the candidate's model. In `readStream`:

```
case "codex":
    model := src.Model
    if id, _, err := harness.SplitEffort(src.Model); err == nil { model = id }
    samples = codexStream(f, src.Provider, model)
```

In `readPane`, the first switch gains `case "codex": return nil, "codex
pane usage not read"`. Update the `Source.Harness` comment to
`"claude" | "agy" | "opencode" | "codex"`. Before importing
`internal/harness` from `internal/usage`, run
`go list -deps ./internal/harness | grep internal/usage`; it must print
nothing (no cycle). If it prints anything, halt.

### 5.6 pinnedModel (doctor.go)

```
func pinnedModel(kind string, raw []byte) string:
    if h, ok := harness.Lookup(kind); ok && h.DocExt == "toml": return tomlTopLevelModel(raw)
    return frontmatterModel(raw)

func tomlTopLevelModel(raw []byte) string:
    for each line of raw (strings.TrimSpace):
        if strings.HasPrefix(line, "["): return ""          // first table header ends the top level
        rest, ok := strings.CutPrefix(line, "model"); if !ok: continue
        rest = strings.TrimSpace(rest)
        rest, ok = strings.CutPrefix(rest, "="); if !ok: continue
        v := strings.TrimSpace(rest)
        if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"': return v[1:len(v)-1]
    return ""
```

`model_reasoning_effort = "medium"` must NOT match: after
`CutPrefix(line, "model")` the rest is `_reasoning_effort = "medium"`, and
`CutPrefix(rest, "=")` fails on `_`, so the loop continues. Pin that in
the test. In `roleCheck`, replace the `frontmatterModel(raw)` call with
`pinnedModel(kind, raw)`. The mismatch branch keeps its structure; only
the strings become kind-aware:

```
if r.ExpectModel != "" && model != r.ExpectModel:
    if h, ok := harness.Lookup(kind); ok && h.DocExt == "toml":
        Detail: fmt.Sprintf("%s -- pins %s; relay ships %s", detail, model, r.ExpectModel)
        Fix:    fmt.Sprintf("relay agent install --kind %s --role %s --force", kind, r.Name)
    else: (the existing agy text, unchanged)
```

### 5.7 definitionBody (harness_test.go)

Extend so the cross-kind architect test can read a TOML body:

```
func definitionBody(t, kind, doc):
    if h, _ := Lookup(kind); h.DocExt == "toml":
        _, after, ok := strings.Cut(doc, "developer_instructions = '''\n")
        body, _, ok2 := strings.Cut(after, "'''")
        if !ok || !ok2: t.Fatalf("%s definition has no developer_instructions literal", kind)
        return body
    (existing frontmatter split, unchanged)
```

With §6.1's assembly the literal's content is exactly the md body
including its trailing newline, so `body` equals claude's md body byte for
byte and the comparison passes without any trimming. If it does not, fix
the assembly, never the comparison.

## 6. The four profiles

### 6.1 Rule

For each role, `developer_instructions` is the body of the claude
definition (`<role>.claude.md`, everything after the closing `---` fence,
byte-exact), inside a TOML multi-line literal string. Verify first with
`grep -c "'''" internal/harness/agents/*.claude.md` -- every count must be
0 (it is today); if not, halt.

Assemble with a script so the bytes are exact, from the repo root:

```
body() { awk 'f{print} /^---$/{c++; if(c==2)f=1}' "internal/harness/agents/$1.claude.md"; }
```

(`f` turns on after the second `---` line; the printed text is the body
including its trailing newline.) Each file is then: header comment lines,
top-level keys, a blank line, `developer_instructions = '''`, newline, the
body, `'''`, newline, and for plan-executor ONLY, after that, a blank line
and the `[agents.researcher]` table. Because the body ends with `\n`, the
closing `'''` starts a fresh line.

TOML detail that decides the order: `developer_instructions` is a top-level
key and MUST appear BEFORE any `[table]` header, or TOML assigns it to the
table. `TestAgentDocCodexIsToml` pins this.

### 6.2 plan-executor.codex.toml

```toml
# relay plan-executor role for codex (docs/specs/2026-09-19-codex-harness-design.md §5).
# Installed by `relay agent install --kind codex`; relay selects it with
# `codex -p plan-executor`. The [agents.researcher] table at the end is the
# sub-agent role this builder spawns for read-only research; that role's
# own profile pins its model.

developer_instructions = '''
<body of plan-executor.claude.md, with the one edit below>
'''

[agents.researcher]
config_file = "researcher.config.toml"
description = "Read-only codebase research: locates code, traces conventions, answers questions about the existing codebase. Never edits anything."
```

The one edit inside the body: find the rule with
`grep -n "subagent_type: researcher" internal/harness/agents/plan-executor.claude.md`
and read the full sentence around it. The clause `pass subagent_type:
researcher to the Agent tool` becomes `call the spawn_agent tool with that
role, then wait on it`. Keep every other byte, including line breaks
before and after the changed clause; rewrap only the lines the edit
touches. The result must contain `spawn_agent` and must not contain
`subagent_type` or `Agent tool`.

### 6.3 researcher.codex.toml

```toml
# relay researcher role for codex (spec §5.1). Two uses, one file: the
# `-p researcher` consult profile, and the [agents.researcher] layer the
# plan-executor profile names. The model pin is deliberate: every codex
# builder's research runs gpt-5.6-luna at medium regardless of the
# candidate; `relay doctor` warns if it drifts.
model = "gpt-5.6-luna"
model_reasoning_effort = "medium"
sandbox_mode = "read-only"

developer_instructions = '''
<body of researcher.claude.md>
'''
```

### 6.4 reviewer.codex.toml and architect.codex.toml

reviewer: a comment line (`# relay reviewer role for codex; a read-only
consult started by relay ask with -p reviewer.`), `sandbox_mode =
"read-only"`, blank line, the literal with reviewer's body.
architect: a comment line (`# relay architect (planner) definition for
codex; never launched by relay. Start a planner with: codex -p architect`),
blank line, the literal with architect's body verbatim --
`TestArchitectHandoffIsSharedAcrossKinds` compares it to claude's body
byte for byte through `definitionBody` (§5.7).

## 7. Ordered steps

Run `go test ./internal/harness/ ./internal/transcript/ ./internal/usage/ ./internal/doctor/`
after every step. Where a test is written first, run it, see it fail (or
fail to compile), implement, run again, see it pass.

1. **SplitEffort + ErrBadModel.** Write `TestSplitEffort` in
   harness_test.go, cases: `gpt-5.6-terra:high` -> (`gpt-5.6-terra`, `high`);
   `gpt-5.6-terra` -> (`gpt-5.6-terra`, ``); `a:b:c` -> (`a:b`, `c`);
   `:high`, `gpt-5.6-terra:` and `` -> `errors.Is(err, ErrBadModel)`.
   Implement per §4/§5.1.
2. **DocExt, AgentDoc, profiles.** Add the field (§3), the `AgentDoc`
   change and embed pattern (§5.3), and create the four TOML files per §6
   in the same step (an embed pattern with no matching `.toml` file fails
   `go build`). Add `TestAgentDocCodexIsToml` (agents_test.go): for each of
   the four roles `AgentDoc(role, "codex")` succeeds; the doc contains
   `developer_instructions = '''\n`; the text after that marker contains
   exactly one `'''` (`strings.Count(after, "'''") == 1`); plan-executor
   contains `[agents.researcher]`, `config_file = "researcher.config.toml"`
   and `spawn_agent`, does not contain `subagent_type` or `Agent tool`, and
   `strings.Index(doc, "developer_instructions")` <
   `strings.Index(doc, "[agents.researcher]")`; researcher contains
   `model = "gpt-5.6-luna"` and `model_reasoning_effort = "medium"`.
   Extend the one-writer loop in agents_test.go
   (`for _, kind := range []string{"claude", "opencode", "agy"}`) with
   `"codex"`. Extend `definitionBody` per §5.7. (The package will not
   compile until step 3 adds the row, because `Lookup("codex")` must
   succeed for `AgentDoc`; write steps 2 and 3 together and run the tests
   once.)
3. **knownHarnesses row.** Add the codex entry (§3) and the same literal to
   `TestTableExactValues`. Run the whole harness package: the four
   `*SetOnEveryKind` tests, `TestPlanExecutorDispatchesResearcherOnEveryKind`,
   `TestArchitectHandoffIsSharedAcrossKinds`, `TestCanServe`,
   `TestAgentDocCodexIsToml` must pass.
4. **Launch.** Add a codex case to `TestLaunchPrintPerKind` (model `m/x`,
   no colon): print
   `["exec", PromptPlaceholder, "-p", "plan-executor", "-m", "m/x", "-c", "model_provider=prov", "--json", "-C", DirPlaceholder]`,
   prompt index 1. Write `TestLaunchCodex`:
   `Launch("openai", "gpt-5.6-terra:high", nil, builder, TierHarness)` gives
   Args `["-p","plan-executor","-m","gpt-5.6-terra","-c","model_provider=openai","-c","model_reasoning_effort=high"]`
   and Print `["exec", PromptPlaceholder, "-p","plan-executor","-m","gpt-5.6-terra","-c","model_provider=openai","-c","model_reasoning_effort=high","--json","-C",DirPlaceholder]`;
   at `TierEdit` the same with `"-s","workspace-write"` appended to both;
   with extra `["--foo"]` at `TierHarness`, `--foo` is last in both;
   `PrintArgs("hi", time.Hour, "/w")` yields `exec hi ... -C /w` and leaves
   `Print` untouched. Write `TestLaunchCodexBadModel`: `":high"` and
   `"gpt-5.6-terra:"` return an error with `errors.Is(err, ErrBadModel)`.
   Implement §5.1.
5. **Tiers.** Extend `TestPermissionArgsTable` with the four codex cells
   (§5.2) and `TestLaunchRefusesExtraArgsPermissionFlag` with codex +
   extra `["-s", "read-only"]` at `TierEdit` -> `ErrExtraArgsPermission`,
   and at `TierHarness` -> no error, extra passed through. Implement §5.2.
6. **Transcript.** Create `testdata/codex.jsonl` from §8 exactly (one JSON
   object per line, a single trailing newline). Create `codex.go` per §5.4
   and the `Render` case. Run
   `go test ./internal/transcript/ -run TestFixtures -update`, open
   `testdata/codex.log` and check it reads, in order: an error line for
   the skills budget, an agent message, `bash` call then an ok line with
   the listing, an agent message, a `spawn_agent` call, one ok line
   `LUNA-RESEARCHER PONG`, the final agent message, and nothing for the
   thread/turn/item.started events. Then add `TestCodexTable` (mirror
   `TestClaudeTable`'s shape) with one case per row of spec §7 including:
   `file_change` with two changes -> two `edit` lines; `file_change` with
   `"changes": []` -> `["[file_change]"]`; `command_execution` with
   `"exit_code": 2` -> call + `errLine("exit 2: ...")`; `turn.failed`;
   top-level `error`; `item.completed` with item type `foo` -> `["[foo]"]`;
   `wait` with no completed states -> `["[collab_tool_call wait]"]`.
7. **Usage.** Create `testdata/codex-stream.jsonl` = the §8 fixture plus
   one line appended:
   `{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":400,"cache_write_input_tokens":50,"output_tokens":20,"reasoning_output_tokens":5}}`.
   Write `TestCodexStream` (usage/codex_test.go):
   `codexStream(f, "openai", "gpt-5.6-terra")` returns exactly two samples;
   `[0].Tokens == Tokens{In: 8541, CacheRead: 97024, CacheWrite: 0, Out: 419}`;
   `[1].Tokens == Tokens{In: 600, CacheRead: 400, CacheWrite: 50, Out: 20}`;
   every sample has Provider `openai`, Model `gpt-5.6-terra`, HasCost false.
   `TestCodexStreamEmpty`: an empty reader yields zero samples. Implement
   §5.5, then the `readStream`/`readPane` cases and the comment.
8. **Doctor.** Write `TestPinnedModelToml` (doctor_test.go): raw
   `"# c\nmodel = \"gpt-5.6-luna\"\nmodel_reasoning_effort = \"medium\"\n[agents.x]\nmodel = \"other\"\n"`
   with kind `codex` -> `gpt-5.6-luna`; raw
   `"model_reasoning_effort = \"medium\"\n"` -> ``; raw
   `"[agents.x]\nmodel = \"other\"\n"` -> ``; kind `claude` with
   `"---\nmodel: opus\n---\n"` -> `opus`. Write `TestDoctorCodexResearcherPin`:
   a `fakeEnv` shaped like `TestDoctorClaudeRoleNeverWarnsOnAPin`'s with
   `lookPaths {"codex": "/usr/bin/codex"}`, a version for it of `0.155.1`
   in whatever map the fake uses for `--version` (see `agyEnv`),
   `intStatus {"codex": {Installed: true}}`, all four
   `/home/u/.codex/<role>.config.toml` in `existingFiles`, and three
   sub-cases: researcher contents = `harness.AgentDoc("researcher", "codex")`
   -> SevOK, Detail `~/.codex/researcher.config.toml (model: gpt-5.6-luna)`;
   researcher contents = that doc with `gpt-5.6-luna` replaced by
   `gpt-5.6-terra` -> SevWarn, Detail
   `~/.codex/researcher.config.toml (model: gpt-5.6-terra) -- pins gpt-5.6-terra; relay ships gpt-5.6-luna`,
   Fix `relay agent install --kind codex --role researcher --force`;
   plan-executor contents = its shipped doc -> SevOK, Detail
   `~/.codex/plan-executor.config.toml` (no pin suffix). Implement §5.6.
9. **README.** (a) "How relay launches one" table: add
   `| \`codex\` | \`-p <role.Definition> -m <id> -c model_provider=<provider> [-c model_reasoning_effort=<effort>]\` |`
   and, after the table, the sentence: "For `codex` the candidate's
   `model` is `<id>[:<effort>]`: `gpt-5.6-terra:high` runs
   `-m gpt-5.6-terra -c model_reasoning_effort=high`, and the suffix stays
   in the token so two efforts are two candidates."
   (b) Permission tiers table: add
   `| codex | (none) | \`-s read-only\` | \`-s workspace-write\` | \`--dangerously-bypass-approvals-and-sandbox\` |`
   and extend the "verified" sentence with `codex 0.155.1`.
   (c) The two lists of kinds -- line 24's "relay knows how to start ..."
   and the candidates `harness` bullet ("a kind relay knows: ...") -- gain
   `codex`.
   (d) In "First run" step 4, after the agy paragraph, one paragraph:
   codex roles are TOML profiles at `~/.codex/<role>.config.toml`
   selected with `-p`; the researcher profile pins `gpt-5.6-luna` at
   `medium` for every codex builder's research sub-agents and `relay
   doctor` warns when that pin drifts; pane builders also need
   `herdr integration install codex`.
10. **Gate.** `make check` clean. `git diff --stat` touches only §2 files
    and `go.mod`/`go.sum` are unchanged. Copy this plan to
    `docs/plans/2026-09-19-codex-harness.md`. Squash to one commit. In the
    report list every test written, and for the three mutation checks
    state that you ran each, saw the named failure, and reverted:
    remove `"-C"` from the codex print form -> `TestLaunchCodex` fails;
    remove the `- Cached` subtraction -> `TestCodexStream` fails; make
    `pinnedModel` always return `frontmatterModel(raw)` ->
    `TestDoctorCodexResearcherPin` fails.

## 8. Fixture: `internal/transcript/testdata/codex.jsonl`

Captured 2026-09-19 from `codex exec --json`, codex-cli 0.155.1 (luna/low,
one shell command, one spawned researcher role). Copy verbatim, one object
per line:

```
{"type":"thread.started","thread_id":"01a0bb3b-6da3-79d1-a85d-9ea76187d710"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Exceeded skills context budget. All skill descriptions were removed and 1281 additional skills were not included in the model-visible skills list."}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"I’ll run `ls -a` first, then use the available agent capability to request the exact one-word reply."}}
{"type":"item.started","item":{"id":"item_2","type":"command_execution","command":"/usr/bin/bash -lc 'ls -a'","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"/usr/bin/bash -lc 'ls -a'","aggregated_output":".\n..\n.git\ncodex.jsonl\nresearcher.toml\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"The directory listing is complete. I’m now spawning the researcher-role sub-agent with the exact instruction to return one word."}}
{"type":"item.started","item":{"id":"item_4","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"01a0bb3b-6da3-79d1-a85d-9ea76187d710","receiver_thread_ids":[],"prompt":"Reply with the single word PONG.","agents_states":{},"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_4","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"01a0bb3b-6da3-79d1-a85d-9ea76187d710","receiver_thread_ids":["01a0bb3b-9c37-79f3-82e2-02c7a1a20ea9"],"prompt":"Reply with the single word PONG.","agents_states":{"01a0bb3b-9c37-79f3-82e2-02c7a1a20ea9":{"status":"pending_init","message":null}},"status":"completed"}}
{"type":"item.started","item":{"id":"item_5","type":"collab_tool_call","tool":"wait","sender_thread_id":"01a0bb3b-6da3-79d1-a85d-9ea76187d710","receiver_thread_ids":["01a0bb3b-9c37-79f3-82e2-02c7a1a20ea9"],"prompt":null,"agents_states":{},"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_5","type":"collab_tool_call","tool":"wait","sender_thread_id":"01a0bb3b-6da3-79d1-a85d-9ea76187d710","receiver_thread_ids":["01a0bb3b-9c37-79f3-82e2-02c7a1a20ea9"],"prompt":null,"agents_states":{"01a0bb3b-9c37-79f3-82e2-02c7a1a20ea9":{"status":"completed","message":"LUNA-RESEARCHER PONG"}},"status":"completed"}}
{"type":"item.completed","item":{"id":"item_6","type":"agent_message","text":"LUNA-RESEARCHER PONG"}}
{"type":"turn.completed","usage":{"input_tokens":105565,"cached_input_tokens":97024,"cache_write_input_tokens":0,"output_tokens":419,"reasoning_output_tokens":92}}
```

## 9. Amendments (rounds 2-3)

- §7 step 3 also extends `TestArchitectShipsOnEveryKindAndIsNotARole`:
  a kind with `DocExt == "toml"` is checked for its
  `developer_instructions = '''` literal instead of `name: architect`,
  which is a frontmatter key a profile cannot carry. Round 1 halted on
  this test, correctly: the base plan had not listed it.
- §6.2's header comment refers to the table as `agents.researcher` in
  backticks, so the comment does not precede the real key in the
  `TestAgentDocCodexIsToml` ordering check.
- Round 3: `TestDoctorUnknownKindDegradesWithoutFailing` and the
  `"unknown kind"` case of `TestRenderRules` used `codex` as the stand-in
  for a kind relay does not know; both now use `droid`. Round 2 halted on
  the doctor one, correctly.
