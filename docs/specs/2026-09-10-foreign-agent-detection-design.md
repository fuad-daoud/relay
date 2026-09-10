# Foreign-agent detection and the read-only researcher role

- Date: 2026-09-10
- Issues: #40 (relay is blind to writers it did not spawn), partially #24
- Status: implemented in #52 (`2b3b0fe`, `internal/relay/foreign.go`)
- Amended 2026-09-10: §7.2 gains a harness-specific caveat after an
  opencode sub-agent was observed NOT surfacing as its own herdr pane

## 1. System overview

relay guarantees one writer per working tree, but the guarantee is enforced
only against *bindings*: `Bind` refuses a second binding on a tree another one
drives (`ErrCWDTaken`). An agent relay did not start is invisible to it.
`docs/design.md` names a second writer on one tree as the failure mode that
destroys work rather than stalling, so this is the one class of failure the
design is shaped to avoid, resting on an assumption relay never checks.

This design does two things.

**It makes the occupancy observable.** `herdr agent list` already reports a
`cwd`, a pane id, a kind, a status and a title for every live agent, and
`relay status` already fetches that list. An agent inside a bound tree that no
binding accounts for is therefore a fact relay can state at zero additional
cost. It surfaces as `foreign` rows on the affected binding.

**It removes relay's own contribution to the problem.** The `plan-executor`
definitions relay ships (`internal/harness/agents/`) instruct the builder to
dispatch parallel sub-agents to execute *implementation* steps. relay ships a
role that encourages the thing relay's model forbids. Those definitions are
amended so sub-agents may only read, and a new read-only `researcher` role is
shipped for them to be dispatched as.

### Scope boundary

relay does not prevent, kill, or adjudicate. It reports occupancy. It cannot
observe writes, so the vocabulary throughout is **foreign agent**, never
"foreign writer" -- a read-only explorer and a rogue implementer are
indistinguishable to `herdr agent list`.

## 2. File structure

```
internal/relay/
  foreign.go                          NEW  detection: pure functions over data
                                           Status already holds
  foreign_test.go                     NEW
  status.go                           MOD  BindingStatus.Foreign; Status computes
                                           the known-endpoint set once; statusRow
                                           populates the field; RenderStatus emits
                                           the rows

internal/harness/
  harness.go                          MOD  Harness.RolePath/RoleDoc -> Roles []Role
  agents.go                           MOD  AgentDoc(role, kind); embed agents/*.md
  agents_test.go                      MOD  guard: shipped definitions carry the
                                           one-writer prohibition
  agents/plan-executor.claude.md      MOD  forbid writing sub-agents
  agents/plan-executor.opencode.md    MOD  ditto
  agents/researcher.claude.md         NEW  read-only sub-agent role, model: haiku
  agents/researcher.opencode.md       NEW  read-only sub-agent role, pinned model

internal/doctor/
  doctor.go                           MOD  one role row per role per kind; the
                                           researcher row reports its pinned model
  doctor_test.go                      MOD

cmd/relay/
  agent.go                            MOD  `relay agent print --role`, default
                                           plan-executor
  agent_test.go                       MOD

docs/design.md                        MOD  state the occupancy guarantee and its
                                           limit
README.md                             MOD  researcher role install line; the model
                                           pin as the first edit a new user makes
```

## 3. Data structures and type definitions

### 3.1 `relay.ForeignAgent` (new)

One live agent occupying a bound working tree that no binding accounts for.

| field | type | json | description |
| --- | --- | --- | --- |
| `PaneID` | `string` | `pane_id` | herdr pane id, as reported. Always present. |
| `Kind` | `string` | `kind` | herdr agent kind (`claude`, `agy`, `opencode`, ...). |
| `Status` | `string` | `status` | herdr lifecycle status, verbatim. Never interpreted. |
| `CWD` | `string` | `cwd` | the agent's cwd as herdr reports it. Equal to or nested under the binding's cwd. |
| `Title` | `string` | `title,omitempty` | `terminal_title_stripped`. Untrusted display text; see 6.3. |

Every field is copied from `herdr.Agent` unmodified. No derived or judged
field exists on this type, deliberately.

### 3.2 `relay.BindingStatus` (modified)

Gains one field:

| field | type | json | description |
| --- | --- | --- | --- |
| `Foreign` | `[]ForeignAgent` | `foreign,omitempty` | Agents inside this binding's tree matching no known endpoint. Sorted by `PaneID`. `omitempty`, so existing JSON consumers are byte-identical when the slice is empty. |

Constraint: `Display` is **not** affected by `Foreign`. A foreign agent is an
observation, not a state. Promoting it to `NEEDS YOU` would be relay judging
something it cannot see.

### 3.3 `harness.Role` (new)

| field | type | description |
| --- | --- | --- |
| `Name` | `string` | Role name as the user types it: `plan-executor`, `researcher`. Also the doctor row's `Name` and the `--role` value. |
| `Path` | `string` | Home-relative install path, e.g. `.claude/agents/researcher.md`. Required, non-empty. |
| `Doc` | `string` | Basename stem of the embedded definition, `<role>.<kind>`. Required, non-empty. |

### 3.4 `harness.Harness` (modified)

`RolePath string` and `RoleDoc string` are **replaced** by `Roles []Role`.

An empty `Roles` means the harness selects its role by preamble rather than by
file. `agy` keeps an empty `Roles`, so its existing doctor row -- "selected by
preamble, not a file" -- is unchanged.

Populated table:

| kind | binary | integration | roles |
| --- | --- | --- | --- |
| `agy` | `agy` | `antigravity-cli` | (none) |
| `claude` | `claude` | `claude` | `plan-executor` -> `.claude/agents/plan-executor.md`; `researcher` -> `.claude/agents/researcher.md` |
| `opencode` | `opencode` | `opencode` | `plan-executor` -> `.config/opencode/agents/plan-executor.md`; `researcher` -> `.config/opencode/agents/researcher.md` |

`Roles` is ordered: `plan-executor` first, so doctor output is stable and the
role relay's loop depends on is reported first.

## 4. Interface definitions and component contracts

### 4.1 `internal/relay/foreign.go`

Single responsibility: decide which live agents occupy a bound tree without
being accounted for. Pure -- no I/O, no clock, no filesystem.

```
func knownEndpoints(bindings []store.Binding) []store.Endpoint
```
- Returns every `Planner` and `Builder` endpoint across all bindings.
- Precondition: none. A nil slice yields an empty result.
- Postcondition: length is exactly `2 * len(bindings)`. Endpoints are not
  deduplicated or validated; `SameAgent` tolerates zero values.
- Errors: none.

```
func ForeignAgents(agents []herdr.Agent, known []store.Endpoint, tree string) []ForeignAgent
```
- Returns agents whose cwd is inside `tree` and which match no endpoint in
  `known`, sorted by `PaneID`.
- Matching uses the existing `relay.SameAgent`, so session-durable identity,
  pane moves, and older kind-less bindings behave exactly as they do
  everywhere else in relay. It must not reimplement matching.
- Precondition: `tree` is the binding's stored `CWD`.
- Postcondition: returns `nil` (not an empty slice) when nothing matches, so
  `omitempty` elides the JSON field.
- Errors: none. An empty `tree` yields `nil`.

```
func withinTree(tree, cwd string) bool
```
- Reports whether `cwd` is `tree` or is nested under it.
- Algorithm: return false if either argument is empty; `t := filepath.Clean(tree)`,
  `c := filepath.Clean(cwd)`; return true if `c == t`; return true if
  `strings.HasPrefix(c, t + string(filepath.Separator))`; else false.
- The separator guard is load-bearing: without it `/foo-bar` matches `/foo`.
- Errors: none.

### 4.2 `internal/relay/status.go` (modified)

`Status` computes `known := knownEndpoints(bindings)` **once**, before the row
loop, and passes it to `statusRow`. `statusRow` gains a `known []store.Endpoint`
parameter and sets `row.Foreign = ForeignAgents(agents, known, b.CWD)`.

No new herdr call. No new error path: `Status` already fails if `ListAgents`
fails, and detection adds no I/O.

`RenderStatus` emits one line per foreign agent, after the `builder` line and
before `detail`, reusing the existing column widths:

```
  builder  wM:pV          agy      gone      `abuilder`
  foreign  wM:pW          claude   idle      plan-executor
  foreign  wM:pX          opencode working   researcher      internal/
```

Format: `"  foreign  %-14s %-8s %-9s %s"` over `PaneID`, `Kind`, `Status`,
`Title`. A fifth field carrying the cwd is appended **only when `f.CWD != b.CWD`**,
rendered as `filepath.Rel(b.CWD, f.CWD)` so it reads as a path within the tree
(`internal/ui`) rather than an absolute duplicate of the binding's own cwd. If
`Rel` errors, the absolute cwd is used. A foreign agent sitting at the tree
root appends nothing.

### 4.3 `internal/harness/agents.go` (modified)

```
func AgentDoc(role, kind string) ([]byte, error)
```
- Returns the embedded definition for `agents/<role>.<kind>.md`.
- Resolution: look up `knownHarnesses[kind]`, scan its `Roles` for an entry
  whose `Name == role`, and read `agents/<entry.Doc>.md`. The filename is built
  from the *table's* `Doc` field, never from the caller's `role` string.
- Precondition: the `(role, kind)` pair must appear in the table. A pair that
  does not returns `ErrNoAgentDoc` **without touching the embed FS**, so a
  caller cannot path-traverse through `role`.
- The embed directive becomes `//go:embed agents/*.md`, and the two-case
  switch is deleted.
- Errors: `ErrNoAgentDoc` (unchanged sentinel).

### 4.4 `cmd/relay/agent.go` (modified)

`relay agent print --kind <kind> [--role <role>]`.

- `--role` defaults to `plan-executor`, so every existing README line and every
  doctor `Fix` command already in the wild keeps working verbatim.
- An unknown `--role` for a known kind is an error naming the roles that kind
  has.
- Preconditions and output are otherwise unchanged.

### 4.5 `internal/doctor` (modified)

The single `plan-executor` check becomes a loop over `h.Roles`, emitting one
`Check` per role with `Name` set to the role name.

- `len(h.Roles) == 0` -> the existing "selected by preamble, not a file" row,
  emitted once, keeping `Name: "plan-executor"`. That is still the role the
  preamble selects, and keeping the name makes agy's doctor output
  byte-identical to today's.
- File missing -> `SevWarn`, `Fix: relay agent print --kind <kind> --role <role> > ~/<path>`.
- File present -> `SevOK`, `Detail: ~/<path>`.
- File present **and** its YAML frontmatter carries a `model:` key -> `Detail`
  becomes `~/<path> (model: <value>)`.

The model is read from the *installed* file, never the embedded one: the user
may have edited it, and the point of the row is to show what will actually
run. Parsing is deliberately shallow -- first `---` fenced block, first line
matching `^model:\s*(.+)$`, value trimmed. A file with no frontmatter, or an
unreadable one, simply omits the suffix; it is never an error, because a
missing model pin is not a fault.

**`doctor.Env` gains one method.** The interface can `Stat` a path but cannot
read one, so the model pin is unreachable as the interface stands:

```
ReadFile(path string) ([]byte, error)
```

`realEnv.ReadFile` delegates to `os.ReadFile`. The test `fakeEnv` gains a
`fileContents map[string]string` beside its existing `existingFiles`; a path
present in `existingFiles` but absent from `fileContents` reads as empty, which
must yield no model suffix and no error.

`Report.UsableBuilder` is unchanged: role rows have never contributed to it and
still do not.

## 5. High-level pseudocode

### 5.1 Detection, per `relay status` invocation

```
Status(ctx, rt):
    bindings <- rt.Store.List()
    agents   <- rt.Herdr.ListAgents(ctx)         // already happens today
    known    <- knownEndpoints(bindings)          // once, not per binding
    for each binding b in bindings:
        row <- statusRow(rt, b, agents, known)
        rows.append(row)
    return Report{rows}

statusRow(rt, b, agents, known):
    ... existing planner/builder/detail/last/pending logic, unchanged ...
    row.Foreign <- ForeignAgents(agents, known, b.CWD)
    return row

ForeignAgents(agents, known, tree):
    if tree is empty: return nil
    out <- nil
    for each agent a in agents:
        if a.CWD is empty:            continue
        if not withinTree(tree, a.CWD): continue
        if any ep in known where SameAgent(a, ep): continue
        out.append(ForeignAgent copied from a)
    sort out by PaneID
    return out
```

Cost: one pass over the agent list per binding, over a list already in memory.
No allocation when nothing is foreign.

### 5.2 Doctor role rows, per kind

```
for each role r in h.Roles:
    full <- env.HomePath(r.Path)
    if HomePath errored:
        emit Check{Name: r.Name, Sev: Warn, ProbeFailed: true, Detail: reason}
        continue
    if env.Stat(full) errored:
        emit Check{Name: r.Name, Sev: Warn,
                   Detail: "missing: ~/" + r.Path,
                   Fix:    "relay agent print --kind K --role " + r.Name + " > ~/" + r.Path}
        continue
    detail <- "~/" + r.Path
    if model <- frontmatterModel(full); model is present:
        detail <- detail + " (model: " + model + ")"
    emit Check{Name: r.Name, Sev: OK, Detail: detail}
```

## 6. Error handling strategy

### 6.1 Detection introduces no error path

Every function in `foreign.go` is total. There is nothing to fail: the inputs
are already-fetched data, and every branch has a defined result. `Status`'s
existing failure modes -- store read, `ListAgents`, log read, pending scan --
are untouched.

This is the point of computing at read time rather than in the daemon. A
daemon-side detector would need dedupe state on `Binding` (the trap
`HaltNotifiedRound` exists to document), a definition of when a foreign agent
is "new", and a persistence story. None of that is needed to answer the
question a human actually asks, which is "what is in this tree right now".

### 6.2 Doctor frontmatter parsing never fails the check

An unreadable or malformed role file omits the model suffix and stays `SevOK`.
Absence of a model pin is not a fault, so it cannot produce a `SevWarn`, and a
parse failure must never mask the fact the file exists.

### 6.3 Titles are untrusted

`ForeignAgent.Title` is display-only. Nothing branches on it. In particular,
foreign rows are **not** filtered by title -- see 7.2.

### 6.4 Observability

No new logging. `relay status` is the surface; the daemon is unchanged and
emits nothing new.

## 7. Behavioural rules and their rationale

### 7.1 Foreign means "referenced by no binding at all"

Not "not this binding's builder". A second binding's planner may legitimately
sit in the same tree -- #19 records exactly that happening -- and relay knows
about it, so it must not be reported as foreign. The rule matches the issue's
own phrasing: the observed pane was *known to nothing*.

### 7.2 Sanctioned read-only panes still show as foreign

After the researcher role ships, a correctly-behaving builder may produce
`foreign` rows titled `researcher`, because on at least one observed machine a
claude sub-agent surfaced as its own herdr pane. This is expected and is not
suppressed.

Suppressing by title would mean relay trusting a string any agent can set, to
decide whether to report a fact. relay cannot distinguish a reader from a
writer, so it reports occupancy and shows the title; the *human* reads the
title and draws the conclusion. That division is the whole design.

**Whether it surfaces at all is harness-specific.** Observed 2026-09-10 against
herdr 0.9.0, opencode integration v11: two `builder`-alias builders were run in
relay worktrees, and one dispatched a `researcher` sub-agent. It rendered as a
labelled card *inside* the builder's own opencode TUI. `herdr agent list`
reported three agents -- the planner and the two builders -- and no researcher.
So on opencode the sub-agent produced **no** `foreign` row.

Do not read that as the quieter, better case. It is the worse one. A row you did
not want is noise you can dismiss; a sub-agent herdr cannot see is occupancy
relay cannot report, and `ForeignAgents` will stay silent about it whether it
reads or writes. Detection covers agents herdr knows about, which is not the
same set as agents touching the tree -- and the gap is invisible from
`relay status` by construction.

Two consequences for the reader of this spec:

- A user on the `builder` (opencode) alias who sees no `researcher` rows has not
  verified that nothing else is in the tree. They have verified that herdr
  reported nothing else.
- The claude observation above and this opencode one are both single
  observations, on one machine, at one integration version. Neither generalises
  to "this harness always does X". Treat the vocabulary as: relay reports what
  herdr reports, and how much that covers varies by harness.

This changes nothing in the implementation. It is recorded because §7.2
previously read as though noisy `researcher` rows were the universal outcome,
which would leave an opencode user drawing a false conclusion from silence.

### 7.3 Containment, one direction

An agent in a subdirectory of the tree writes to the same tree and lands in the
same round diff, so it must be caught. An agent in a *parent* directory is not
flagged: a planner sitting in `~/projects` would otherwise flag every binding
beneath it.

### 7.4 Known limitation: symlinks

Paths compare literally after `filepath.Clean`. A binding whose stored `CWD`
and an agent's reported cwd differ only by a symlink will not match, and the
agent will not be reported. `EvalSymlinks` would make `withinTree` impure and
hit the filesystem on every status call for every agent. Documented in
`docs/design.md` rather than fixed.

## 8. Role definition changes

### 8.1 `plan-executor.{claude,opencode}.md`

Both files stay twins. Changes:

1. **PARALLELIZATION PROTOCOL** -- delete item 1 ("independent implementation
   steps ... dispatched as parallel sub-agents"). Keep items 2 (file reads) and
   3 (codebase research). Reframe the opening from "maximize throughput by
   delegating work" to delegating **reads only**.
2. **EXECUTION ALGORITHM** -- steps 2-5 currently describe waves of parallel
   implementation sub-agents. Replaced with sequential step execution; research
   may still fan out in parallel alongside it.
3. **SUB-AGENT PROMPTING STANDARDS** -- retitled to research sub-agents, with an
   explicit prohibition: a sub-agent must not create, edit, or delete files, or
   run any command that modifies the tree.
4. **Front-matter `<example>` blocks** -- all three currently advertise parallel
   implementation to the model. They must be rewritten, or they undo the body.
5. **Final report** -- "Which steps ran in parallel via sub-agents" becomes
   "Which research was delegated".
6. **New load-bearing sentence**, verbatim in both files, asserted by test:
   `Exactly one agent writes to this working tree, and it is you.`
7. Research sub-agents are dispatched as the `researcher` role. This clause
   lands with step 7, not step 6, so no shipped file ever names a role that has
   no definition beside it.

### 8.2 `researcher.{claude,opencode}.md` (new)

Read-only investigator dispatched by `plan-executor`. Returns findings in-band
to its parent; it never writes a file. (This is what distinguishes it from
#36's proposed `explorer` consult, which runs in its own relay-spawned pane and
hands findings back through a file path. Same shape, different contract --
they are deliberately not the same definition.)

Contents:
- Read-only tool set; explicit prohibition on edits, creates, deletes, and any
  tree-modifying command.
- Instruction to report findings with file paths and line references, not
  summaries.
- Instruction to state plainly when it did not find something, rather than
  guessing.
- A `model:` frontmatter pin.

**Model pins.** Framed the way the alias table is: worked examples, not a
supported set. Chosen so that **neither introduces a provider the user does not
already need**:

| file | pin | provider already assumed by |
| --- | --- | --- |
| `researcher.claude.md` | `haiku` | `cbuilder` alias (`sonnet`) |
| `researcher.opencode.md` | `openrouter/z-ai/glm-5.3-flash` -- the model the `builder` alias already names | `builder` alias |

The README documents the `model:` line as the first edit a new user makes, and
doctor reports the installed pin (4.5) so the edit is discoverable without
reading the README.

### 8.3 Open item the implementer must confirm, not guess

The claude definitions dispatch sub-agents via the `Agent` tool with a
`subagent_type` parameter. **The opencode equivalent parameter name is not
known** and must be verified against opencode's actual agent/Task tool schema
before `plan-executor.opencode.md` is written.

Per `CLAUDE.md`: if this cannot be established, the builder halts and reports
rather than improvising a plausible-looking parameter name. A wrong name here
fails silently -- the sub-agent runs in the default role, which is the writing
role, which is the exact hazard this work removes.

## 9. Testing requirements

Unit, table-driven where the shape allows.

**`withinTree`** -- exact match; nested one level; nested deep; the `/foo-bar`
vs `/foo` sibling trap; trailing slash on either argument; empty tree; empty
cwd; parent directory (must be false).

**`ForeignAgents`** --
- this binding's builder in the tree -> excluded
- this binding's planner in the tree -> excluded
- **another binding's planner in the tree -> excluded** (the false positive the
  rule exists to prevent)
- unknown pane, cwd equal to tree -> included
- unknown pane, cwd nested under tree -> included
- unknown pane, cwd elsewhere -> excluded
- unknown pane, empty cwd -> excluded
- endpoint matched by session id on a moved pane -> excluded (proves
  `SameAgent` is used rather than reimplemented)
- nothing foreign -> returns nil, not an empty slice
- ordering is by pane id

**`RenderStatus`** -- golden output with zero, one, and two foreign agents;
one case where the foreign cwd is nested, asserting the relative cwd suffix
appears; one where it equals the binding cwd, asserting it does not.

**`Status` JSON** -- a report with no foreign agents serialises without a
`foreign` key.

**`harness.AgentDoc`** -- every `(role, kind)` pair in the table resolves; an
unknown role returns `ErrNoAgentDoc`; a role containing path separators or
`..` returns `ErrNoAgentDoc` without reading the FS.

**`harness` definition guard** -- both `plan-executor` definitions contain the
verbatim sentence from 8.1 item 6. This is the mutation-testable guard: delete
the sentence, this named test fails.

**`doctor`** -- a kind with two roles emits two role rows in table order; a
missing role file yields the `--role`-bearing fix command; an installed file
with a `model:` pin yields the suffix; one without yields no suffix and stays
`SevOK`; agy still emits its single preamble row.

**`relay agent print`** -- no `--role` prints plan-executor (backward
compatibility); `--role researcher` prints the researcher definition; an
unknown role errors naming the kind's available roles.

## 10. Ordered implementation steps

Each step is independently verifiable and leaves the tree green
(`make check`).

**Step 1 -- `withinTree` and `ForeignAgents`.**
Deliverable: `internal/relay/foreign.go` plus `foreign_test.go`.
Depends on: nothing.
Implements: 3.1, 4.1.
Verify: the full `ForeignAgents` and `withinTree` case lists in section 9 pass;
`make check` clean. Nothing calls the new code yet.

**Step 2 -- wire detection into status.**
Deliverable: `BindingStatus.Foreign`, `Status`/`statusRow` threading,
`RenderStatus` rows.
Depends on: step 1.
Implements: 3.2, 4.2.
Verify: render goldens and the JSON-omission test pass; existing status tests
pass unmodified except where a golden legitimately gains a line.

**Step 3 -- `harness.Roles`.**
Deliverable: `Role` type, `Harness.Roles` replacing `RolePath`/`RoleDoc`,
populated table, `AgentDoc(role, kind)` with `//go:embed agents/*.md`.
Depends on: nothing (parallel with steps 1-2, different files).
Implements: 3.3, 3.4, 4.3.
Verify: `AgentDoc` tests pass including the traversal case; doctor and
`cmd/relay/agent.go` compile against the new shape with minimal edits.

**Step 4 -- `relay agent print --role`.**
Depends on: step 3.
Implements: 4.4.
Verify: the three `agent print` cases in section 9; the no-flag case is
byte-identical to today's output.

**Step 5 -- doctor role rows and the model suffix.**
Depends on: step 3.
Implements: 4.5, 5.2, 6.2.
Verify: the five doctor cases in section 9; agy's row is unchanged.

**Step 6 -- amend the plan-executor definitions.**
Deliverable: items 1-6 of 8.1 in both files. Item 7 (naming the `researcher`
role) is deliberately held back to step 7.
Depends on: step 3, for ordering only, so both files are edited once.
Implements: 8.1 items 1-6.
Verify: the verbatim-sentence guard passes; no occurrence of implementation-step
delegation survives in either file or its front-matter examples; neither file
yet names a role that has no definition.

**Step 7 -- write the researcher definitions, then point plan-executor at them.**
Deliverable: both `researcher.*.md` files, plus item 7 of 8.1 added to both
`plan-executor.*.md`.
Depends on: step 3 (table entries) and step 6 (the files it amends).
**Blocked on 8.3** -- confirm opencode's sub-agent selector before writing the
opencode twin. Halt and report if it cannot be confirmed.
Implements: 8.2, 8.1 item 7.
Verify: `relay agent print --kind claude --role researcher` emits the file;
doctor reports the pin once installed.

**Step 8 -- documentation.**
Deliverable: `docs/design.md` gains the occupancy guarantee and its symlink
limit (7.4); README gains the researcher install line and names the `model:`
pin as the first edit.
Depends on: steps 2, 5, 7.
Verify: a reader following the README on a clean box installs both roles.

## 11. Explicitly out of scope

- **Preventing a foreign agent.** relay does not own the harness and cannot
  stop an agent from starting another.
- **Killing or reaping panes.** relay only ever closes a pane it spawned, and
  there is no reaping today. Tracked as #36's open `relay reap` question.
- **Doctor and hook surfaces for foreign agents.** `relay status` only.
  A `state_changed` event needs per-binding dedupe state and a definition of
  "new"; a doctor row needs doctor to become store-aware, which it is not.
- **Annotating the round diff** as unreliably attributed (#40's second open
  question). That requires observing during the round, which is daemon work.
- **Closing #19's between-rounds gap.** Sibling issue, separate design.
- **#36 consults and #37 triggers.** The `researcher` role is a harness-level
  sub-agent, not a relay-spawned consult, and shares no code with them.
- **The remaining #24 items** -- whether relay ships an alias table at all,
  moving `--dangerously-skip-permissions` behind an opt-in, and the clean-machine
  README walkthrough. Chores, not design.
