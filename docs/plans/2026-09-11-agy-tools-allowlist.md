# agy tools allowlist: plan-executor can write, researcher/reviewer can start (#91, #94)

**Design spec:** `docs/specs/2026-09-11-agy-role-definitions-design.md` -- read §2
(the frontmatter table) and §7.4; this plan amends §7.4 in step 4.
**Issues:** #91 (agy builder has no write/shell tools), #94 (design.md preamble
paragraph stale). Closes both.
**Depends on:** nothing open. #85/#87 landed.

The spec is in your worktree. This plan tells you what; the "Observed" section
below tells you why, because the spec's §7.4 is wrong and step 4 corrects it.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `agy` yourself and do not touch `~/.gemini`. The live
verification is the planner's, after merge (see "Planner verification").

## Global constraints

- Files touched: `internal/harness/agents/{plan-executor,researcher,reviewer}.agy.md`,
  `internal/harness/agents_test.go`, `internal/doctor/doctor.go`,
  `internal/doctor/doctor_test.go`, `docs/specs/2026-09-11-agy-role-definitions-design.md`,
  `docs/design.md`, `README.md`. Nothing else.
- Do not rewrite passages this plan does not name. Match the surrounding voice.
- No test in `cmd/relay` -- CI has no herdr and no agy. Every test here is a
  pure function over the embed FS or a fake `doctor.Env`.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Observed (agy 1.2.1, 2026-09-11, print mode with `--output-format stream-json`)

Each row is one `agy --agent <name> -p "list every tool available to you"` run
against a definition placed in `~/.gemini/config/agents/` (relay's install
path). This is the ground truth the definitions and tests below encode.

| frontmatter `tools:` | result |
| --- | --- |
| absent (shipped `plan-executor`) | 10-tool default: `view_file grep_search find_by_name list_dir send_message manage_task read_url_content search_web schedule generate_image`. **No write, no `run_command`, no `invoke_subagent`.** This is the #91 halt. |
| present, every name known | exactly the listed tools, plus `send_message` and `manage_task` which agy always adds. The allowlist is enforced. |
| present, any name unknown (shipped `researcher`, `reviewer`) | **the agent does not start**: `error: failed to construct executor: failed to resolve components: unknown component: tool "view_file_outline" not found in registry` (likewise `view_code_item`, `command_status`, `read_terminal`). |
| any of the above in a workspace `.agents/agents/` dir | `tools:` is ignored; a 17-tool default including writes. relay never installs there; recorded so nobody "verifies" from a workspace copy. |

Names confirmed to resolve (each appeared in an enforced list or a default set
in the runs above): `view_file grep_search find_by_name list_dir run_command
write_to_file replace_file_content multi_replace_file_content invoke_subagent
manage_subagents define_subagent send_message manage_task read_url_content
search_web schedule generate_image ask_question`.

Names confirmed **not** to resolve: `view_file_outline view_code_item
command_status read_terminal`. (`command_status` is advertised in the
stream-json `init` event's `tools` array and still refused at construction; the
`init` array is not the registry. Do not use it as a source.)

`relay doctor` reported all three agy rows OK throughout: it checks that the
file exists and that `model:` is `inherit`, nothing else. Step 3 closes that.

---

## Step 1: fix the three agy definitions

**Files:** `internal/harness/agents/plan-executor.agy.md`,
`internal/harness/agents/researcher.agy.md`, `internal/harness/agents/reviewer.agy.md`

### 1a. plan-executor gains an allowlist

In `plan-executor.agy.md`, after the `commandExecutionPolicy: auto` line and
before the closing `---`, add exactly:

```yaml
tools:
  - view_file
  - grep_search
  - find_by_name
  - list_dir
  - run_command
  - write_to_file
  - replace_file_content
  - multi_replace_file_content
  - invoke_subagent
  - manage_subagents
```

Then append a new H1 section at the end of the body (agy delimits sections by
H1; keep the heading exactly):

```markdown
# Why the tools list is this

On agy a definition without `tools:` does not get every tool; it gets a
read-mostly default with no write, no shell and no `invoke_subagent`, which
is a builder that cannot build. The list above is the writer's set: read,
search, edit, write, shell, and `invoke_subagent`/`manage_subagents` for
dispatching and collecting `researcher` sub-agents. Nothing browser-, web-,
or scheduling-shaped is offered because a plan never asks for it. An
unknown name in this list stops the agent from starting at all, so every
entry is one agy 1.2.1 resolves.
```

### 1b. researcher and reviewer drop the four names agy refuses

In both `researcher.agy.md` and `reviewer.agy.md`, the `tools:` block becomes
exactly:

```yaml
tools:
  - view_file
  - grep_search
  - find_by_name
  - list_dir
  - run_command
```

(remove `view_file_outline`, `view_code_item`, `command_status`,
`read_terminal`; keep order as shown.)

In both files, replace the paragraph under `# Why the tools list is short`
with:

```markdown
The `tools:` allowlist above is the read-only tools agy 1.2.1 exposes and none
of the writing ones. On this harness read-only is not a request to you, it is a
refusal by the harness: a write tool is not listed, so it is not offered.
`run_command` is present under `commandExecutionPolicy: sandbox` so `git diff`
and `git log` work; a command that writes to the tree is refused by the
sandbox. Every name here is one agy resolves: an unknown name in this list
stops the agent from starting at all.
```

**Verify:** `go test ./internal/harness/` fails at this point on
`TestAgyDefinitionsFrontmatter` (`plan-executor must not restrict tools`).
That is expected; step 2 replaces that assertion.

## Step 2: tests pin the allowlists to the registry

**File:** `internal/harness/agents_test.go`

### 2a. Rewrite the plan-executor branch of `TestAgyDefinitionsFrontmatter`

Replace the `case "plan-executor":` block. It must still require
`subagent: false`. Delete the `must not restrict tools` check. Add: the
frontmatter contains `\ntools:\n`, and the tools block contains each of
`write_to_file`, `replace_file_content`, `run_command`, `invoke_subagent`
as a `  - name` line (use the same line-anchored regexp style as
`forbidden`). Error text: `plan-executor allowlist must include %s; without it
the builder cannot build`.

The `default:` branch (researcher, reviewer) is unchanged, except extend the
`forbidden` regexp's alternation with `multi_replace_file_content`, `sed_file`,
`manage_subagents`, `define_subagent`.

### 2b. New test `TestAgyAllowlistsResolve`

Add a package-level `var agyKnownTools = map[string]bool{...}` holding exactly
the "confirmed to resolve" names from the Observed section (18 names), with a
comment: `// Tool names agy 1.2.1 resolves for a definition in
~/.gemini/config/agents. An unknown name stops the agent from starting
(#91). Extend only from a live agy run, never from the stream-json init
event's tools array, which advertises names the registry refuses.`

The test: for each role in `{plan-executor, researcher, reviewer}`, read
`AgentDoc(role, "agy")`, take the `frontmatter(t, doc)` text, collect every
line matching `^\s*-\s*([a-z_]+)\s*$` that appears after a `tools:` line and
before the next non-list line, and `t.Errorf` for any name not in
`agyKnownTools`: `%s allowlist names %q, which agy 1.2.1 does not resolve;
the agent would not start`. Also require at least one name was collected per
role.

Write a small helper `agyAllowlist(fm string) []string` for the extraction;
keep it in the test file.

**Verify:** `go test ./internal/harness/` passes. Mutations, each must fail
the named test, then revert:
- add `  - view_file_outline` to `researcher.agy.md` -> `TestAgyAllowlistsResolve`
- delete the `  - write_to_file` line from `plan-executor.agy.md` -> `TestAgyDefinitionsFrontmatter`
- add `  - write_to_file` to `reviewer.agy.md` -> `TestAgyDefinitionsFrontmatter`

## Step 3: `relay doctor` warns when an installed definition differs from shipped

**Files:** `internal/doctor/doctor.go`, `internal/doctor/doctor_test.go`

The stale-copy problem in #91 was invisible: doctor's definition row checks
existence and the `model:` pin. Add one more condition to the per-definition
check (the function containing the `missing: %s` and `pins a tier` returns).

**Interface:** the check already reads the installed file via
`env.ReadFile(fullPath)` and knows `r.Doc` (the embedded name, e.g.
`plan-executor.agy`). Locate how `harness.AgentDoc(role, kind)` (or the
existing accessor the doctor package uses for `r.Doc`) returns the shipped
bytes; use that, do not re-implement the embed lookup.

**Rule:** after the model-pin branch and before the final `SevOK` return, if
the shipped bytes and installed bytes differ, return

```
Check{Group: kind, Name: r.Name, Severity: SevWarn,
      Detail: fmt.Sprintf("%s -- differs from the definition this relay ships", detail),
      Fix:    fmt.Sprintf("relay agent print --kind %s --role %s > %s", kind, r.Name, homeRel)}
```

Compare after trimming trailing whitespace on both sides only (a missing
final newline from a `>` redirect must not warn). This applies to every
kind, not just agy; it is a byte comparison, nothing kind-specific.

**Tests** (fake env, same style as the existing doctor tests):
- installed bytes == shipped bytes -> `SevOK`, detail unchanged.
- installed bytes == shipped bytes + `"\n"` -> `SevOK`.
- installed bytes = shipped with one extra line -> `SevWarn`, `Detail`
  contains `differs from the definition this relay ships`, `Fix` is the
  print command.
- the existing `pins a tier` case still produces its Warn (the pin check
  runs first; a pinned file necessarily differs, and the pin message is the
  more specific one).

**Verify:** `go test ./internal/doctor/` passes. Mutation: make the comparison
always report equal -> the "one extra line" test fails.

## Step 4: docs -- spec §7.4, `docs/design.md` (#94), README

**Files:** `docs/specs/2026-09-11-agy-role-definitions-design.md`,
`docs/design.md`, `README.md`

### 4a. Spec §7.4

Replace the section body of `### 7.4 researcher and reviewer carry a
`tools` allowlist` with a section titled `### 7.4 Every agy definition carries
a `tools` allowlist` and this content (adapt formatting to the surrounding
spec; keep the facts verbatim):

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
  machine.

Also, in the §8 testing list, update the `TestAgyDefinitionsFrontmatter`
bullet: plan-executor's line now reads "plan-executor has `subagent: false`
and a `tools:` list containing `write_to_file`, `replace_file_content`,
`run_command`, `invoke_subagent`". Add a bullet for `TestAgyAllowlistsResolve`
with its mutation ("add `view_file_outline` to researcher's list, this test
fails") and one for the doctor drift row.

### 4b. `docs/design.md` -- #94

In the "Candidates (config)" section, the launch table currently has a
`preamble` column with `role.Preamble` on the `agy` row, and the paragraph
after it begins `` `agy` needs the preamble because it has no `--agent` flag ``.
Drop the `preamble` column from the table entirely; the agy row becomes
`--model <model> --agent <role.Definition>` (matching
`internal/harness/harness.go` `Launch`, case `"agy"`). Delete the paragraph
sentence about agy needing the preamble; keep the sentence about `extra_args`
being appended. If the intro sentence says "launch arguments and preambles
per kind", make it "launch arguments per kind".

### 4c. README

In the passage that reads `On agy the definition's `tools:` allowlist makes it
a property the harness enforces: a write tool that is not listed is not
offered.`, append one sentence: `The list is also load-bearing the other way:
a definition with no `tools:` gets no write or shell tool at all, and one
naming a tool agy does not have does not start -- so every agy definition
relay ships carries an explicit, verified list, and `relay doctor` warns when
the installed copy differs from it.`

**Verify:** `grep -n "role.Preamble\|needs the preamble" docs/design.md` is
empty. `grep -c "TestAgyAllowlistsResolve" docs/specs/2026-09-11-agy-role-definitions-design.md`
is at least 1. `make check` passes.

## Step 5: commit

One commit. Message subject:
`fix: agy definitions carry verified tools allowlists -- plan-executor can write, researcher/reviewer start; doctor warns on drift (#91, #94)`.
Body: two lines on the observed cause (no `tools:` = read-mostly default;
unknown name = agent does not start) and a `Closes #91` / `Closes #94` line.

---

## Planner verification (after merge, not the builder's job)

```bash
for r in plan-executor researcher reviewer; do
  relay agent print --kind agy --role $r > ~/.gemini/config/agents/$r.md
done
relay doctor                       # three agy rows OK, no "differs" warn
cd "$(mktemp -d)"
agy --agent researcher -p "Reply OK" --output-format stream-json | tail -1   # status SUCCESS, not construct error
agy --agent plan-executor -p "Call no tools. List every tool available to you, comma separated." --output-format stream-json | tail -1
#   expect write_to_file, replace_file_content, run_command, invoke_subagent in the response
```

Then one throwaway binding whose plan writes a file and runs `go test` on a
trivial package, and confirm the report shows the edit and the test run.
