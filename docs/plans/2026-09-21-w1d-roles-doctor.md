# Wave 1 batch D: gate commands run in the foreground, a half-done tree is verified not redone, opencode roles carry a mode, doctor checks opencode's external_directory allowlist (#241, #236, #252 side note)

Role-definition wording and one doctor check. This plan stands alone:
everything you need is in this file and in the tree. If a step is
impossible as written or contradicts the code, **halt and report** -- do
not improvise around it.

You are a headless builder on a server-side worktree of this repo. The repo
there has **no tags**, so never run `make check`; run the gate commands in
§7 exactly as written. Run every command in the foreground and read its
exit code; never as a background task. Do not spawn sub-agents for the
edits.

## 1. System overview

- **#241.** On 2026-09-20 an agy builder launched the plan's `make check`
  as a background task, wrote "I have launched make check and am waiting",
  idled, and agy's headless runner exited 0 -- no report, no marker. The
  rule exists only in agy's file (`internal/harness/agents/plan-executor.agy.md`
  `# One process, in the foreground`, lines 103-121) and says nothing about
  reading the exit code; the claude, opencode and codex variants have no
  foreground/exit-code rule at all. Same round, second observation: the
  replacement builder started from scratch on a tree already carrying the
  first builder's uncommitted edit. The pre-flight `git status` lives in
  relay's handoff prompt (`internal/relay/send.go:52` `builderPrompt`), not
  in the role files, and no text anywhere names the case "the tree already
  carries part of the plan". Also: agy's `# Workflow` (lines 126-127,
  132-133) still says "dispatch research sub-agents", contradicting its own
  line 115 and #191.
- **#252 side note.** opencode 2.x refused `Agent researcher cannot run as a
  subagent`: `researcher.opencode.md` has no `mode:` key (only
  `architect.opencode.md` `mode: primary` and `plan-executor.opencode.md`
  `mode: all` set one). The researcher is only ever dispatched by the
  plan-executor through the Task tool, so it is `mode: subagent`. The
  reviewer is spawned in its own pane by `relay ask` and is left alone.
- **#236.** A headless opencode builder cannot read its plan under
  `~/.local/state/relay/<name>/` unless `~/.config/opencode/opencode.jsonc`
  allows that directory under `permission.external_directory` (README
  `### opencode permission allowlist`, lines ~1600-1619); without it every
  round dies in seconds with `auto-rejecting`. `relay doctor` has no
  opencode config check at all (`internal/doctor` only knows the sqlite3
  row). Add one: read the config, warn with the README snippet when the
  entry is absent or the file cannot be parsed.

## 2. File structure

```
internal/harness/agents/plan-executor.claude.md    + "Gate commands" paragraph, + "Tree already carries part of the plan" paragraph
internal/harness/agents/plan-executor.opencode.md  same
internal/harness/agents/plan-executor.codex.toml   same (inside developer_instructions)
internal/harness/agents/plan-executor.agy.md       exit-code sentence added to "One process, in the foreground"; same dirty-tree paragraph; Workflow lines 126-127/132-133 reworded
internal/harness/agents/researcher.opencode.md     frontmatter + mode: subagent
internal/harness/agents_test.go                    + TestPlanExecutorGateRunsInForegroundOnEveryKind, TestPlanExecutorVerifiesAHalfDoneTree, TestOpencodeDefinitionsDeclareMode
internal/doctor/opencode.go                        + opencodeAllowlistCheck(env, stateRoot) Check, + jsonc stripper
internal/doctor/opencode_test.go                   + tests
internal/doctor/doctor.go                          RunOption WithStateRoot(string); the opencode branch of the per-kind loop appends the check
cmd/relay/doctor.go                                passes doctor.WithStateRoot(rt.Store.Root()) (or store.DefaultRoot() if Store has no Root(); check)
README.md                                          allowlist section: one sentence that doctor checks it
docs/plans/2026-09-21-w1d-roles-doctor.md          copy of this plan
```

Nothing outside these files. In particular do **not** edit
`architect.*.md` (a separate test pins those identical across kinds) and do
not touch `internal/relay/send.go`'s `builderPrompt`.

## 3. Data structures

None new beyond `RunOption WithStateRoot(root string)` storing `cfg.stateRoot`.

## 4. Interfaces

### Role text (exact sentences; tests pin the quoted fragments)

Add to every plan-executor variant, as its own section after the
"HANDLING PROBLEMS WITHOUT DEVIATING" section (agy: extend
`# One process, in the foreground` instead of adding a second foreground
section, and add the dirty-tree paragraph as its own section):

```
GATE COMMANDS RUN IN THE FOREGROUND

A verification or gate command -- the plan's check line, `make check`, `go test`,
a build -- runs in the foreground: you wait for it to finish and read its exit
code before the next step. Never run it as a background task, never hand it to
a sub-agent, never report it as passed before it has exited. If it fails, that
step failed: report the failing command and its last lines, and halt there.

THE TREE MAY ALREADY CARRY PART OF THE PLAN

If the pre-flight `git status` shows uncommitted changes and they match a step
of the plan (a previous builder was cut off mid-round), do not redo the step:
verify what is there against the step's text, fix only what differs, and say in
the report which steps you found already applied. Uncommitted changes that do
not match any step are a reason to halt and report, not to clean up.
```

Casing follows the file: the claude/opencode/codex files use ALL-CAPS
section titles; agy uses `# Title case` headings. The fragments the tests
pin are the sentences, so keep them verbatim across kinds:
`runs in the foreground: you wait for it to finish and read its exit code`,
`never report it as passed before it has exited`,
`do not redo the step: verify what is there against the step's text`.

agy `# Workflow` lines 126-127 and 132-133: replace "dispatch research
sub-agents ... in parallel" wording with "read the files yourself,
sequentially"; the file must still contain no occurrence of the word
`researcher` (`TestPlanExecutorDispatchesResearcherOnEveryKind`).

`researcher.opencode.md` frontmatter gains `mode: subagent` after `model:`.

### Doctor

```
// internal/doctor/opencode.go
func opencodeAllowlistCheck(env Env, stateRoot string) Check
    // stateRoot: relay's state root, e.g. /home/x/.local/state/relay (never "~")
    // path candidates, in order: ~/.config/opencode/opencode.jsonc, ~/.config/opencode/opencode.json (env.HomePath)
    // first readable file wins; none -> Check{Group: <the opencode kind's group as other opencode rows use>, Name: "external_directory",
    //     Severity: SevWarn, Detail: "no ~/.config/opencode/opencode.jsonc; headless opencode builders auto-reject reading their plan", Fix: snippet(stateRoot)}
    // parse: stripJSONC(body) -> json.Unmarshal into map[string]any; failure -> SevWarn "could not parse <path>: <err>", Fix: snippet
    // walk: permission -> external_directory -> map[string]any; an entry whose key is stateRoot+"/**" or stateRoot+"/*" with value "allow" -> SevOK Detail "<path> allows <stateRoot>/**"
    // also accept a key that equals stateRoot or a glob whose prefix before the first '*' is a parent of stateRoot and whose value is "allow" (e.g. "/home/x/.local/state/**")
    // otherwise SevWarn Detail "<path> has no permission.external_directory allow entry for <stateRoot>/**; headless opencode builders auto-reject reading their plan", Fix: snippet
func snippet(stateRoot string) string   // the README jsonc block with stateRoot substituted, prefixed by `add to ~/.config/opencode/opencode.jsonc:`
func stripJSONC(b []byte) []byte
    // removes // line comments and /* */ block comments outside string literals, and a trailing comma before } or ]
    // string-literal aware: a '/' inside "..." is content; handles \" escapes
```

`doctor.Run`: in the per-kind loop (`doctor.go:427-587`), when
`kind == "opencode"` and `cfg.stateRoot != ""`, append
`opencodeAllowlistCheck(env, cfg.stateRoot)` after the role rows. Not when
`cfg.adopted` (that path is the bind-time warning; keep it quiet).

`cmd/relay/doctor.go` `cmdDoctor`: add `doctor.WithStateRoot(<state root>)`
to the `doctor.Run` call at ~240. The runtime's store knows its root
(`rt.Store` -- find the accessor; if there is none, use
`store.DefaultRoot()`, which is what `newRuntime` used).

## 5. Pseudocode

Covered by §4. Order of operations in `opencodeAllowlistCheck`: locate ->
read -> strip -> parse -> walk -> classify.

## 6. Error handling

Doctor checks never fail the run: every failure mode is a `SevWarn` row
with a `Fix`. `stripJSONC` on malformed input still returns bytes; the
`json.Unmarshal` error is what the row reports.

## 7. Ordered implementation steps

Commit prefix for every commit in this round: `fix(...)`. Never `feat:`.

### Task 1 -- role text (#241, #252 side note)

**Files:** the four `plan-executor.*` files, `researcher.opencode.md`, `internal/harness/agents_test.go`.

**Tests first** (`agents_test.go`, in the shape of
`TestPlanExecutorDefinitionsForbidWritingSubAgents` at 55-70):
- `TestPlanExecutorGateRunsInForegroundOnEveryKind`: every kind's
  plan-executor contains the two foreground fragments from §4.
- `TestPlanExecutorVerifiesAHalfDoneTree`: every kind contains the
  dirty-tree fragment.
- `TestOpencodeDefinitionsDeclareMode`: `architect.opencode.md` ->
  `mode: primary`, `plan-executor.opencode.md` -> `mode: all`,
  `researcher.opencode.md` -> `mode: subagent` (use the `frontmatter()`
  helper at 169-181 or a plain `strings.Contains` on the frontmatter block).
- Existing: `TestPlanExecutorDispatchesResearcherOnEveryKind` (agy must not
  say `researcher`), `TestAgyDefinitionsFrontmatter`, `TestAgentDocCodexIsToml`
  must stay green -- the codex text goes inside the `'''` block, before the
  closing `'''` at line 95, and must not add `subagent_type` or `Agent tool`.

**Then** the edits. **Verify:** `go test -count=1 ./internal/harness/`.
Commit: `fix(agents): gate commands run in the foreground and read their exit code; a half-done tree is verified, not redone; opencode researcher is mode subagent (#241)`.

### Task 2 -- doctor allowlist check (#236)

**Files:** `internal/doctor/opencode.go`, `opencode_test.go`, `doctor.go`, `cmd/relay/doctor.go`, `README.md`.

**Tests first** (`opencode_test.go`, using `fakeEnv` from `doctor_test.go:28-43`
with `fileContents` / `existingFiles` and `homeDir`; stateRoot `/fake/home/.local/state/relay`):
- file with `"permission": {"external_directory": {"/fake/home/.local/state/relay/**": "allow"}}` -> `SevOK`.
- same content with `// a comment`, a `/* block */` and a trailing comma -> `SevOK` (pins `stripJSONC`).
- file present, no `permission` key -> `SevWarn`, `Fix` contains
  `/fake/home/.local/state/relay/**` and `"allow"`.
- entry present with value `"ask"` -> `SevWarn`.
- no file -> `SevWarn` with `no ~/.config/opencode/opencode.jsonc`.
- `opencode.json` (no `c`) with the entry, no `.jsonc` -> `SevOK`.
- `stripJSONC` unit test: a string containing `"http://x"` survives intact.
- In `doctor_test.go`, a `Run` test in the shape of `TestUsageChecks`
  (~1026): kinds `["opencode"]` with `WithStateRoot(...)` -> the report has
  a row named `external_directory`; kinds `["claude"]` -> it does not.
  **Mutation check:** drop the `kind == "opencode"` condition and the
  second assertion must fail.

**Then** the code per §4. `cmd/relay/doctor.go`: one added option; no
`cmd/relay` test (the doctor command probes herdr; the rule is tested in
`internal/doctor`).

README `### opencode permission allowlist`: append the sentence
`relay doctor warns when this entry is missing (row external_directory under opencode).`

**Verify:** `go test -count=1 ./internal/doctor/ && go build ./...`.
Commit: `fix(doctor): warn when opencode's external_directory allowlist does not cover relay's state dir (#236)`.

### Task 3 -- plan copy and gate

Copy the plan file you were handed to `docs/plans/2026-09-21-w1d-roles-doctor.md`
and commit it (`chore(plans): record wave 1 batch D plan`).

Gate, in the foreground, in this order; stop at the first failure and report it:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```
Do not run `make check` or `make e2e` (planner runs them). After merge the
planner runs `relay agent install --force` locally and the serve unit
reinstalls on restart -- say in the report that the installed definitions
are stale until then.

## Report

Per task: what was done, the test names, the verify result, the mutation
check's outcome (name the failing test). Then the commit shas and the gate
output's last lines. If any step was impossible as written, say which and
stop there.
