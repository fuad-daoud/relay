# Cockpit A2a: the single-source agent format and its per-kind rendering

Spec: `docs/specs/2026-09-24-cockpit-design.md` §3.2 (Agent, Rendering). This round
builds a new pure package only: parse, validate, format and render. Storing agents in
relevo.db, installing them, the TUI and the `actors` section come in A2 proper. Line
numbers are from `ff04f5d`.

**One round. If a step is impossible as written or contradicts what you find, stop and
report. Do not improvise.**

CI has no harness and no network. Everything here is pure Go and golden files. Nothing
spawns a harness, and nothing writes outside `t.TempDir()`.

**Do not modify** `internal/harness/agents/*`, including `shipped.sha256`. Any change
there needs `scripts/agents-shipped.sh --write`, and this round must not change a
shipped agent. Do not touch `internal/ui/**`, `internal/candidate/**`,
`internal/roles/**`, `internal/policy/**` or `internal/config/**`: parallel rounds own
them.

## 1. System overview

A custom agent is one source file: frontmatter (`name`, `description`, `shape`,
`output`, `requires`, `kinds`) plus a prompt body. relevo renders one native file per
harness kind from it:

- claude `.md`
- opencode `.md`
- agy `.md` with its tool allowlist
- codex `.toml`

The rules come from the shipped files in `internal/harness/agents/`, which a research
pass mapped byte for byte. The shipped agents themselves are **not** rendered by this
package. They stay hand-maintained per kind, because their bodies and frontmatter
differ per kind on purpose.

**Shape does not change the rendered tools.** A reader needs write tools to fill its
artifact directory, and the scratch worktree is what keeps it off the binding's tree
(spec D6). Shape is carried in the source for relevo's own use, to decide where the
round runs. It does not affect the rendered bytes.

## 2. File structure

```
internal/agentsrc/source.go        NEW  Source, Shape, Parse, Format, Validate, errors
internal/agentsrc/render.go        NEW  Render, the per-kind templates, yamlScalar
internal/agentsrc/source_test.go   NEW
internal/agentsrc/render_test.go   NEW  goldens + the shipped-keys conformance test
internal/agentsrc/testdata/*.golden  NEW (8 files: 2 fixtures × 4 kinds)
```

The package imports `internal/harness` (for `Lookup`, `All`, `IsShipped` and
`AgentDoc`). Nothing imports the package yet.

## 3. Data structures

```
type Shape string
const (
    ShapeWriter Shape = "writer"
    ShapeReader Shape = "reader"
)

type Source struct {
    Name        string   // ^[a-z0-9][a-z0-9._-]{0,63}$ ; never a shipped agent name
    Description string   // one line, non-empty, <= 300 runes, no newline
    Shape       Shape    // writer | reader
    Output      string   // ^[a-z][a-z0-9-]{0,23}$  (report, plan, findings, notes, design ...)
    Requires    []string // agent names (same pattern), no duplicates, not Name itself; may be empty
    Kinds       []string // known harness kinds, no duplicates; empty = every kind in harness.All()
    Body        string   // the prompt; after Parse it ends with exactly one "\n"; non-blank
}

var ErrBadSource = errors.New("bad agent source")   // every Parse/Validate error wraps it
```

**The source text format** (`Format` writes exactly this, and `Parse` accepts exactly
this):

```
---
name: ui-designer
description: Designs a page as static HTML and CSS from the plan it is given.
shape: reader
output: design
requires: []
kinds: [claude, agy]
---
<body>
```

- The keys appear in this order, one per line, as `key: value`.
- `requires` and `kinds` are flow lists: `[]`, or `[a, b]` with `", "` separators.
  `Format` writes `kinds: []` when `Kinds` is empty.
- `description` is written plain.
- The body is everything after the closing `---\n`.

## 4. Contracts

### 4.1 `Parse(data []byte) (Source, error)`

1. The data must start with `"---\n"`. The frontmatter ends at the first line that is
   exactly `---`.
2. Each frontmatter line must be `key: value`.
   - An unknown key, a duplicate key, or a missing required key (`name`,
     `description`, `shape`, `output`) is an error.
   - `requires` and `kinds` are optional and default to empty.
   - Blank lines inside the frontmatter are an error.
3. List values must be `[]` or `[x, y]`. Items are trimmed and must be non-empty.
4. The body is the bytes after the closing fence. Then:
   - Strip **one** leading `"\n"` if present.
   - Normalise to exactly one trailing `"\n"`: `strings.TrimRight(body, "\n") + "\n"`.
5. Run `Validate`.
6. Errors read `agent source: line <n>: <what>: bad agent source` for syntax, and
   `agent source <name>: <field>: <what>: bad agent source` for validation.

### 4.2 `(Source) Validate() error`

Checks each §3 field rule, plus:

- For every kind in `s.Kinds` (or every kind, when `Kinds` is empty),
  `harness.IsShipped(kind, s.Name)` must be false. A match fails with
  `name "<n>" is a shipped agent; duplicate it under another name`.
- If `codex` is among the rendered kinds, `Body` must not contain `'''`. That is the
  codex literal-string delimiter (codex-harness spec, lines 146-148).
- The body must be non-blank after `strings.TrimSpace`.

### 4.3 `Format(s Source) []byte`

- Writes the §3 text. `Parse(Format(s))` must equal `s` for any valid `s` whose
  `Body` already ends in exactly one `"\n"`.
- `Format` puts a blank line after the closing fence (`"---\n\n" + Body`), and
  `Parse` strips it.

### 4.4 `Render(s Source, kind string) ([]byte, error)`

- **Precondition:** `s.Validate() == nil` and `kind` is one of the rendered kinds.
  Otherwise return the error, or
  `agent <name>: kind <k> is not in its kinds: bad agent source`.
- **Output:** the exact bytes below. `<desc>` is `yamlScalar(s.Description)` (§4.5).
  `<body>` is `s.Body`, which already ends in one `"\n"`. Every file ends with exactly
  one `"\n"`.

**claude**
```
---
name: <name>
description: <desc>
---

<body>
```

**opencode**
```
---
name: <name>
description: <desc>
mode: all
---

<body>
```

**agy** (the shipped plan-executor's agy keys and tools, exactly, `plan-executor.agy.md`)
```
---
name: <name>
description: <desc>
mainAgent: true
subagent: false
model: inherit
commandExecutionPolicy: auto
tools:
  - view_file
  - grep_search
  - find_by_name
  - list_dir
  - run_command
  - write_to_file
  - replace_file_content
  - multi_replace_file_content
---

<body>
```

**codex**
```
# relevo agent <name> for codex, rendered by relevo from its source.
# Edit the source (relevo config agents), not this file: relevo rewrites it.

developer_instructions = '''
<body>'''
```

After that, for each name `r` in `s.Requires`, in order, it appends:

```

[agents.<r>]
config_file = "<r>.config.toml"
description = "relevo agent <r>"
```

- The `'''` closes directly after the body's final `"\n"`, as in the shipped files.
- The file ends `'''\n` when there are no requires, otherwise the last table's
  `description` line + `"\n"`.
- No `sandbox_mode` is written: the tier comes from the launch line.

### 4.5 `yamlScalar(s string) string`

- It returns `s` unchanged when `s` is a safe YAML plain scalar:
  - non-empty;
  - no leading or trailing space;
  - the first rune is not one of `-?:,[]{}#&*!|>'"%@` or a backtick;
  - it contains neither `": "` nor `" #"`;
  - it is not one of `true false yes no null ~` (case-insensitive);
  - it does not parse as a number.
- Otherwise it returns a double-quoted string: `json.Marshal` with HTML escaping off
  (use a `json.Encoder` with `SetEscapeHTML(false)` and trim its trailing newline).
  YAML reads a JSON string correctly.

## 5. Pseudocode

```
Render(s, kind):
  s.Validate() or fail; kind in kinds(s) or fail
  switch kind:
    claude:   fm = [name, description]
    opencode: fm = [name, description, "mode: all"]
    agy:      fm = [name, description, mainAgent/subagent/model/commandExecutionPolicy, tools list]
    codex:    return header + "developer_instructions = '''\n" + body + "'''" + "\n" + requires tables
  return "---\n" + fm lines + "---\n\n" + body
```

## 6. Error handling

Every error wraps `ErrBadSource`, and nothing panics. `Render` never returns partial
bytes with a nil error.

## 7. Tests

### `source_test.go`

- `TestParseFormatRoundTrip`: two fixtures, a writer with `requires: [researcher]` and
  `kinds: []`, and a reader with `kinds: [claude, agy]`. `Parse(Format(s)) == s`, and
  `Format(Parse(text)) == text` for the canonical text.
- `TestParseErrors` covers each of these, and checks that each error message names
  the line or the field:
  - missing opening fence;
  - missing closing fence;
  - unknown key;
  - duplicate key;
  - missing name, description, shape and output (four cases);
  - a bad list;
  - a blank frontmatter line;
  - a bad name;
  - a bad output;
  - a bad shape;
  - an unknown kind;
  - a duplicate require;
  - self-require;
  - a shipped name (`plan-executor`);
  - `'''` in the body with codex among the kinds;
  - the same body with `kinds: [claude]` (valid);
  - a blank body.
- `TestParseBodyNormalisation`: a leading blank line is stripped once, and trailing
  newlines collapse to one.

### `render_test.go`

- `TestRenderGolden`: both fixtures × four kinds. The reader fixture has no codex kind,
  so the golden set is writer×4 plus reader×2, for 6 goldens; the §2 count of 8 is an
  upper bound. Use the repo's convention:
  `var updateGolden = flag.Bool("update", false, ...)` and
  `testdata/<fixture>-<kind>.golden`, as in `internal/ui/golden_test.go:23`.
  Regenerate with `go test ./internal/agentsrc/ -run TestRenderGolden -update`, then
  read each golden and confirm it matches §4.4 by eye before committing. Paste one of
  each kind into the report.
- `TestRenderedKeysMatchShipped`: for each kind, read `harness.AgentDoc("plan-executor",
  kind)`. Take the set of top-level frontmatter keys for the `.md` kinds (split the
  lines between the fences, keep each line with no leading space that contains `:`,
  and take the text before the first `:`). Compare with the writer fixture's rendered
  set:
  - **claude:** equal.
  - **agy:** equal, and the `tools:` item list is equal and in the same order.
  - **opencode:** the rendered set is the shipped set plus `name`. The shipped
    plan-executor opencode file has no `name:`, and the shipped researcher opencode
    file has one, so `name` is allowed.
  - **codex:** the rendered file contains `developer_instructions = '''\n`, exactly
    one closing `'''` after it, and `[agents.researcher]` with
    `config_file = "researcher.config.toml"`.
- `TestRenderShapeDoesNotChangeTools`: the same source with each shape renders
  identical bytes for every kind.
- `TestYamlScalar`: plain cases; quoted cases (a leading `-`, containing `": "`,
  `"yes"`, `"42"`, a leading space, a backtick, a quote); a description containing
  `—` stays plain.
- `TestRenderRefusesKindNotListed`.

### Mutation check

In the agy template, drop `multi_replace_file_content` from the tools list.
`TestRenderedKeysMatchShipped` must fail. Report it, then revert.

## 8. Working efficiently

A builder pays one round trip per step whatever the step does, so:

- Batch independent reads and edits as parallel tool calls in one step.
- Read the four `plan-executor.*` shipped files once, for §4.4's agy block and
  codex's table.
- Iterate on `go test ./internal/agentsrc/...`.
- Run `make check` once at the end.

## 9. Ordered steps

**1. `source.go`.**
- Deliverable: §3, §4.1–§4.3 and `source_test.go`.
- Verify: `go test ./internal/agentsrc/ -run 'Parse|Format'`.

**2. `render.go`.**
- Deliverable: §4.4, §4.5 and `render_test.go`, with the goldens generated,
  inspected and committed.
- Verify: `go test ./internal/agentsrc/...`.
- Depends on 1.

**3. Check and report.**
- Run the mutation check, then `make check`.
  `scripts/agents-shipped.sh --check` must pass untouched.
- Report:
  - the functions with their line ranges;
  - one golden per kind, pasted;
  - `git diff --stat`, which must be only `internal/agentsrc/**`.
- Depends on 2.
