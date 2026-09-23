# Plan: the architect writes latency-aware plans (#323, guidance half)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test to fit.

## Working efficiently

Each model step costs a few seconds of round trip, so:
- Read the four definition files and `internal/harness/harness_test.go`
  lines 185-245 in **one** step (parallel reads). Don't grep for anything
  this plan already locates.
- Make the text change with **one** scripted edit (`python3`) that inserts
  the same two blocks into all four files, rather than twelve hand edits.
  Then add the test in one edit.
- Loop with `go test ./internal/harness/ -run 'TestArchitect'`, then run
  `make check` once at the end.

## 1. System Overview

relay ships the planner's definition, `architect`, in four copies:
`internal/harness/agents/architect.{claude,opencode,agy}.md` (YAML
frontmatter + markdown body) and `architect.codex.toml` (the body inside
`developer_instructions = '''` … `'''`). `TestArchitectHandoffIsSharedAcrossKinds`
(`internal/harness/harness_test.go` ~line 194) requires the four bodies to be
**byte-identical**.

Measurements on #323 showed three things. A round's wall time on a fast
model behind a high-latency provider is set by its number of model steps.
Scripting the mechanical work (a deletion tool plus a manifest) cut steps
the most. And a scripted deletion without a closed list of what may be
deleted overreached: 692 of 1,853 tests were deleted. Batching instructions
helped only sometimes. This change teaches the architect those lessons by
adding one mandatory plan section, one guidance section, and two checklist
items. No Go production code changes. Measuring steps per round is a
separate plan.

## 2. File Structure

```
internal/harness/agents/architect.claude.md    MODIFY  body: three insertions (§4)
internal/harness/agents/architect.opencode.md  MODIFY  identical insertions
internal/harness/agents/architect.agy.md       MODIFY  identical insertions
internal/harness/agents/architect.codex.toml   MODIFY  identical insertions, inside the ''' literal
internal/harness/harness_test.go               MODIFY  one new test (§7 step 2)
```

Do not touch `plan-executor.*`, `researcher.*`, `reviewer.*`, the
frontmatter of any file, or any other file.

## 3. Data Structures & Type Definitions

None. The contract is the exact text below. It must be inserted verbatim
and identically into all four bodies. It contains no `'''` sequence, which
matters because the codex copy is a TOML literal string. Keep that true.

### Block A: a new mandatory output section

Insert it immediately **after** the `### 7. Ordered Implementation Steps`
subsection's last bullet (`- Include verification criteria (how to know the
step is done correctly)`) and its following blank line, and **before**
`## Quality Standards`:

```
### 8. Working Efficiently
Every plan carries this section, placed before step 1, because a builder pays one
round trip (seconds, on a high-latency provider) for every model step whatever the
step does, so a round's cost is its number of steps. Tell the builder to:
- Batch independent reads, searches and edits as parallel tool calls in one step.
- Read once, from the locations this plan names; not re-find what the plan located.
- Make every change to a file in one edit call, or one scripted edit when the change
  is mechanical.
- Iterate on a focused build and test command, fixing every reported error before the
  next run, and run the full check once at the end.
Name the concrete commands (the focused test invocation, the full check) for this
project.
```

### Block B: a new guidance section

Insert it immediately **before** `## Quality Standards`, which puts it right
after Block A:

```
## Writing for the Builder's Round Trips

Step count, not tokens, sets a round's wall time on a fast model behind a slow
provider, and batching instructions alone move it only for some models. These
levers do move it:

- **Hand over locations, not searches.** Name the file, the function and the line
  range for every change. When the change deletes code, quote what goes, so the
  builder deletes rather than hunts.
- **Script the mechanical work.** A deletion, rename or fixture swap across many files
  ships as a tool plus a manifest the builder runs in one step (AST-based where the
  language allows), followed by one pass that fixes whatever no longer compiles. A
  mechanical-edit tool is the one kind of code a plan may contain: it is the
  instruction, not the implementation.
- **Fence every deletion.** A plan that deletes behaviour lists what is deleted as a
  closed, numbered list. Everything not on the list survives, and a test that
  asserted surviving behaviour through a deleted mechanism is ported (its assertion
  changed), not deleted. Every test the round removes must cite a list item in the
  report.
- **Size the plan for the provider.** A long chain of dependent read-then-edit steps
  is split into rounds, or sent to a lower-latency candidate. Measure steps per
  round for a candidate before relying on a plan style for it.
```

### Block C: two checklist items

In `## Self-Verification Checklist`, after item `7. Could a developer
unfamiliar with the system implement this plan?`, append:

```
8. Does the plan carry a Working Efficiently section, and does every change name its file, function and line range?
9. If the plan deletes behaviour, is there a closed list of what is deleted, and is any mechanical edit scripted?
```

The line `If any answer is "no," revise before presenting the plan.` stays
after the list, unchanged.

## 4. Interface Definitions & Component Contracts

- After the change, all four bodies are still byte-identical:
  `TestArchitectHandoffIsSharedAcrossKinds` passes unchanged.
- The frontmatter and the TOML wrapper are untouched. In the `.toml` file the
  insertions land inside the `developer_instructions = '''` literal, which
  must still parse as TOML. `TestArchitectShipsOnEveryKindAndIsNotARole`
  and any TOML-parsing test in the package must pass.

## 5. High-Level Pseudocode

```
for f in the four architect files:
    text = read(f)
    anchorQS = "## Quality Standards"                      # exactly once per file; assert it
    insert (Block A + blank line + Block B + blank line) immediately before anchorQS
    anchor7 = the line "7. Could a developer unfamiliar with the system implement this plan?"
    insert "\n" + Block C lines immediately after anchor7   # before the blank line and "If any answer"
    write(f)
```

Because Block A ends up immediately before Block B, which is immediately
before `## Quality Standards`, the whole insertion is one contiguous
insertion before that heading. Assert each anchor occurs exactly once; if
not, STOP.

## 6. Error Handling Strategy

Not applicable (text change). If an anchor is missing or appears more than
once in any file, halt and report the file and the count.

## 7. Ordered Implementation Steps

### Step 1: insert the three blocks into all four files

- Deliverable: the four files per §3/§5, via one script.
- Depends on: nothing.
- Verify: `go test ./internal/harness/ -run 'TestArchitect' -count=1` passes,
  which proves the bodies are identical and the definitions still ship.
  `grep -c "### 8. Working Efficiently" internal/harness/agents/architect.*`
  prints 1 for each of the four files. `grep -c "'''" internal/harness/agents/architect.codex.toml`
  prints 2.

### Step 2: pin the new guidance

- New test `TestArchitectCarriesLatencyGuidance` in
  `internal/harness/harness_test.go`, placed after
  `TestArchitectHandoffIsSharedAcrossKinds`. For each `h` in `All()`, get
  `AgentDoc("architect", h.Kind)` and its `definitionBody`, then assert the
  body contains each of `"### 8. Working Efficiently"`,
  `"## Writing for the Builder's Round Trips"`, `"Fence every deletion"`,
  `"Script the mechanical work"` and `"8. Does the plan carry a Working Efficiently section"`.
  Also assert that `"### 8. Working Efficiently"` appears **before**
  `"## Quality Standards"` in the body (compare `strings.Index`).
  The doc comment should say: #323. The measured levers (scripted mechanical
  work, fenced deletions, locations not searches) are part of the shipped
  planner on every kind.
- Depends on: step 1.
- Verify: the test passes. Mutation (report it): remove Block B from the
  claude file only. Both `TestArchitectCarriesLatencyGuidance` and
  `TestArchitectHandoffIsSharedAcrossKinds` fail. Restore.

### Step 3: full check

- `make check` passes.
- `git diff --stat` shows exactly the five files in §2.
- Report: each verify output, the mutation result, and the diff stat. Commit
  on the binding's branch with the message
  `feat(architect): plans carry a Working Efficiently section; script and fence mechanical deletions (#323)`.
