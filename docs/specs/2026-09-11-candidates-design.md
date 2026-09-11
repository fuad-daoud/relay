# Candidates replace aliases: harness/provider/model triples, no shipped defaults

**Issue:** #80
**Closes:** #24 (items 2 and 3); unblocks #61
**Amends:** `docs/design.md` §"Builder aliases"; README "Builder aliases" and
"The reviewer alias"; `docs/specs/2026-09-10-consults-design.md` wherever it
says a consult role is an alias

## 1. System overview

Today the thing relay starts is an *alias*: a human-invented name
(`abuilder`) mapped to a herdr kind plus a verbatim argv
(`internal/alias/alias.go`). The name is opaque. The model, the provider and
the role are buried in the argv, so nothing in relay can compare two of
them, and a consult role is whatever alias happens to carry `role:
"consult"` -- `relay ask --role reviewer` looks up an alias *named*
`reviewer`.

This design replaces aliases with **candidates** and ships none.

A candidate is one concrete way to fill a role, identified by the token

```
<harness>/<provider>/<model>
```

`harness` and `provider` are single path segments; `model` is everything
after the second `/`, so opencode's `z-ai/glm-5.3-flash` survives. The
token is the reference everywhere: on the CLI, in `bind.json`, in the log.
There are no short names.

Three things move as a consequence.

1. **Roles become relay's.** `builder`, `reviewer` and `researcher` are a
   fixed table in `internal/harness` with a shape (persistent writer vs
   one-shot read-only) and the harness definition each selects
   (`plan-executor`, `reviewer`, `researcher`). A candidate lists the roles
   it may serve; the role decides whether it is a consult, not the
   candidate.
2. **Relay renders the argv.** Because `model` and the role's definition are
   now fields, `internal/harness` knows how each kind takes them. The user's
   file carries `model`, `provider`, `roles`, and an optional `extra_args`
   appended verbatim. `--dangerously-skip-permissions` is an `extra_args`
   entry the user writes, never a default.
3. **Zero candidates is a valid state.** `DefaultTable` goes.
   `~/.config/relay/aliases.json` is never opened again -- no import, no
   warning. `bind` and `ask` on a machine with no candidates say so and
   name the file to write.

Relay still picks nothing. The only rule this design adds for an omitted
token is: exactly one configured candidate lists the role → use it; none →
error; more than one → refuse and list them. That refusal is the seam #61
step 4 later fills with a scorer.

### Scope boundary

In scope: the `candidate` package, the role table and launch rendering in
`harness`, every call site that today takes an alias name, the `bind.json`
field, `doctor`'s scope, `relay candidates`, docs.

Out of scope, unchanged:

- Scoring, ranking, availability, cooldowns -- #61.
- The reconciler, held delivery, consult lifecycle, reap. They read
  `BuilderCandidate` only where they read `BuilderAlias` today (error text
  and status rows).
- `tree: "none"`. Still forward-declared, still refused by `ask`.
- Migration of `aliases.json` or of existing bindings. An old binding is
  treated as adopted (§4.7).
- Any role beyond the three that ship. Adding one is a table entry plus an
  embedded definition, not a design change.

## 2. File structure

```
internal/candidate/candidate.go        Candidate, Ref, ParseRef, Set, Load, ForRole, Lookup   (new package)
internal/candidate/candidate_test.go   parse/format round-trip, validation matrix, ForRole rule
internal/harness/harness.go            RoleSpec, roleTable, RoleByName, Harness.CanServe, Harness.Launch
internal/harness/harness_test.go       launch rendering per kind, CanServe matrix
internal/relay/herdr.go                Runtime.Aliases -> Runtime.Candidates
internal/relay/bind.go                 BindOptions.Candidate, resolveBuilder via candidate
internal/relay/add.go                  AddOptions.Candidate, ErrCandidateRequired
internal/relay/fork.go                 ForkOptions.Candidate, inherit BuilderCandidate
internal/relay/ask.go                  role resolution via harness role table + candidate.ForRole
internal/relay/send.go                 composePrompt via Launch(...).Preamble
internal/relay/names.go                ConsultRolesTooLong over roles, not aliases
internal/relay/status.go               BuilderCandidate field
internal/relay/answer.go               error text
internal/relay/errors (wherever)       ErrNoCandidates, ErrAmbiguousCandidate, ErrRoleNotServed, ErrUnknownRole
internal/store/types.go                Binding.BuilderCandidate replaces BuilderAlias
internal/ui/fetch.go, list.go          BuilderCandidate
cmd/relay/main.go                      LoadTable -> candidate.Load; flag help; cmdCandidates
cmd/relay/doctor.go                    assembleKinds over candidates; "no candidates" row
cmd/relay/agent.go                     agy hint text no longer mentions aliases
internal/alias/                        DELETED
docs/design.md, README.md              aliases -> candidates
```

## 3. Data structures and type definitions

### 3.1 `candidate.Ref`

```
type Ref struct {
    Harness  string   // single segment, non-empty
    Provider string   // single segment, non-empty
    Model    string   // non-empty; may contain '/'
}

func ParseRef(s string) (Ref, error)   // ErrBadRef on fewer than three segments or an empty one
func (r Ref) String() string           // Harness + "/" + Provider + "/" + Model
```

`ParseRef` splits on the first two `/` only. `ParseRef(r.String()) == r` for
every valid `r`. A segment is empty if it has zero bytes; no other
character rules -- herdr and the harness validate what they receive.

### 3.2 `candidate.Candidate`

```
type Candidate struct {
    Harness   string   `json:"harness"`
    Provider  string   `json:"provider"`
    Model     string   `json:"model"`
    Roles     []string `json:"roles"`
    Tree      string   `json:"tree,omitempty"`        // "" | "binding" | "none"
    ExtraArgs []string `json:"extra_args,omitempty"`
}

func (c Candidate) Ref() Ref
func (c Candidate) Serves(role string) bool
```

| field       | required | constraint                                                       |
|-------------|----------|------------------------------------------------------------------|
| `harness`   | yes      | `harness.Lookup` must succeed                                    |
| `provider`  | yes      | non-empty, no `/`                                                |
| `model`     | yes      | non-empty                                                        |
| `roles`     | yes      | non-empty; each in the role table; each `Harness.CanServe`       |
| `tree`      | no       | `""`, `"binding"` or `"none"`; anything else is rejected at load |
| `extra_args`| no       | passed through untouched                                         |

### 3.3 `candidate.Set`

```
type Set struct { byRef map[string]Candidate }   // key: Ref.String()

func Load(path string) (*Set, error)
func (s *Set) Lookup(ref Ref) (Candidate, error)   // ErrUnknownCandidate
func (s *Set) ForRole(role string) []Candidate     // sorted by Ref.String(); may be empty
func (s *Set) Refs() []string                      // sorted, for error text and `relay candidates`
func (s *Set) Len() int
```

`Load` on a missing file returns an empty, non-nil `Set` and no error. A
present file is a JSON array of `Candidate`; validation errors name the
offending index and field. Two entries with the same `Ref` are an error
(`duplicate candidate <ref> at index i and j`).

### 3.4 `harness.RoleSpec`

```
type RoleShape string
const (
    ShapeBuilder RoleShape = "builder"   // persistent writer in the binding tree
    ShapeConsult RoleShape = "consult"   // one-shot read-only beside the builder
)

type RoleSpec struct {
    Name       string     // "builder", "reviewer", "researcher"
    Shape      RoleShape
    Definition string     // harness role file it selects: "plan-executor", "reviewer", "researcher"
    Preamble   string     // first-prompt text for kinds with no --agent flag
}

func RoleByName(name string) (RoleSpec, bool)
func RoleNames() []string   // table order: builder, reviewer, researcher
```

The table, fixed:

| Name         | Shape   | Definition      | Preamble |
|--------------|---------|-----------------|----------|
| `builder`    | builder | `plan-executor` | `Activate your 'plan-executor' skill and act as the Plan Execution Specialist. Execute exactly as specified in the skill.` (today's text, verbatim) |
| `reviewer`   | consult | `reviewer`      | `Activate your 'reviewer' skill and act exactly as it specifies.` |
| `researcher` | consult | `researcher`    | `Activate your 'researcher' skill and act exactly as it specifies.` |

### 3.5 `harness.Harness` (extended)

```
type Harness struct {
    …unchanged: Kind, Binary, Integration, Roles…
    // SelectsRoleByPreamble is true for a kind with no --agent flag. Its
    // Roles slice is nil and every role in the table is servable.
    SelectsRoleByPreamble bool
}

type Launch struct {
    Kind     string
    Args     []string   // rendered args followed by ExtraArgs
    Preamble string     // "" unless SelectsRoleByPreamble
}

func (h Harness) CanServe(role string) bool
func (h Harness) Launch(c candidate-like, role RoleSpec) Launch
```

To avoid an import cycle (`candidate` imports `harness` for validation),
`Launch` takes the fields, not the type:

```
func (h Harness) Launch(provider, model string, extra []string, role RoleSpec) Launch
```

Rendering, per kind:

| kind       | Args                                                      | Preamble        |
|------------|-----------------------------------------------------------|-----------------|
| `claude`   | `--model <model> --agent <role.Definition>`               | `""`            |
| `opencode` | `--agent <role.Definition> -m <provider>/<model>`         | `""`            |
| `agy`      | `--model <model>`                                         | `role.Preamble` |

then `extra` appended. `CanServe(role)` is `h.Role(def)` found, or
`SelectsRoleByPreamble`. `knownHarnesses["agy"].SelectsRoleByPreamble = true`.

### 3.6 `store.Binding` (modified)

```
BuilderCandidate string `json:"builder_candidate,omitempty"`   // Ref.String(); "" for an adopted builder
```

`BuilderAlias` is removed. A `bind.json` written before this change carries
`builder_alias`, which the decoder ignores, so `BuilderCandidate` reads
`""` and the binding behaves as adopted. This is the whole migration.

### 3.7 `store.Consult.Role` (unchanged type, changed meaning)

Now a role-table name (`reviewer`), not an alias name. The comment is
rewritten to say so.

### 3.8 `relay.Runtime` (modified)

```
Candidates *candidate.Set   // replaces Aliases *alias.Table
```

### 3.9 Options structs (modified)

```
BindOptions.Candidate string   // Ref token; "" means resolve by role (§4.4)
AddOptions.Candidate  string
ForkOptions.Candidate string   // "" inherits source.BuilderCandidate
AskOptions.Role       string   // unchanged: a role-table name
AskOptions.Candidate  string   // new; "" means resolve by role
```

### 3.10 `relay.StatusRow` (modified)

`BuilderAlias` → `BuilderCandidate string json:"builder_candidate"`.

## 4. Interface definitions and component contracts

### 4.1 `candidate.Load`

```
func Load(path string) (*Set, error)
```

Pre: `path` composed by the caller through `userConfigRoot()`.
Post: a `Set` whose every member passed §3.2 validation, or an error that
names index and field. Missing file → empty set, nil error. Read or decode
error → wrapped, with the path.

### 4.2 `candidate.Set.ForRole`

```
func (s *Set) ForRole(role string) []Candidate
```

Pure. Returns every candidate whose `Roles` contains `role`, sorted by
`Ref.String()`. Does not consult the role table -- an unknown role simply
matches nothing; the caller validates the role first.

### 4.3 `relay.resolveCandidate` (new, `internal/relay/candidate.go`)

The one rule for an omitted token, shared by bind, add, fork (after inherit)
and ask:

```
func resolveCandidate(set *candidate.Set, token, role string) (candidate.Candidate, error)
```

| `token` | `set.ForRole(role)` | result |
|---|---|---|
| non-empty | — | `ParseRef` then `Lookup`; `ErrBadRef` or `ErrUnknownCandidate` (message lists `set.Refs()`) |
| `""` | 0 and `set.Len()==0` | `ErrNoCandidates`: "no candidates configured; write ~/.config/relay/candidates.json" |
| `""` | 0 and `set.Len()>0` | `ErrRoleNotServed`: "no configured candidate serves role %q (configured: %v)" |
| `""` | 1 | that candidate |
| `""` | ≥2 | `ErrAmbiguousCandidate`: "%d candidates serve %q: %v; name one with --builder / --candidate" |

Post: the returned candidate `Serves(role)`. A named token that does not
serve the role is `ErrRoleNotServed` naming both. Pure; unit-tested in
`internal/relay`, never through a subcommand (CI has no herdr).

### 4.4 `relay.Bind` (`resolveBuilder`)

```
if opts.BuilderPane != "": adopt, unchanged
c, err := resolveCandidate(rt.Candidates, opts.Candidate, "builder")
role := harness.RoleByName("builder")
h := harness.Lookup(c.Harness)          // cannot miss: Load validated it
l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)
StartAgent(ctx, agentName, l.Kind, paneID, l.Args)
b.BuilderCandidate = c.Ref().String()
b.PreamblePending = true                // unchanged: composePrompt decides whether there is anything to prepend
```

The `ErrConsultAlias` branch ("is a consult role; ask it") disappears: a
candidate is not a role, and `resolveCandidate(..., "builder")` already
refuses one that does not serve `builder`.

### 4.5 `relay.Add`

As bind, with `ErrCandidateRequired` replaced by the §4.3 rule: `add` with
no `--builder` on a one-builder-candidate machine works. `ErrAliasRequired`
is deleted.

### 4.6 `relay.Fork`

```
token := opts.Candidate
if token == "": token = src.BuilderCandidate
c, err := resolveCandidate(rt.Candidates, token, "builder")
```

`ErrNoBuilderAlias` becomes `ErrNoBuilderCandidate`, raised only when the
source is adopted **and** `resolveCandidate` with `""` fails -- i.e. a
fork of an adopted binding on a one-candidate machine now succeeds.

### 4.7 `relay.composePrompt` (send)

```
if !(b.Round == 1 || b.PreamblePending): return text
if b.BuilderCandidate == "": return text            // adopted, or pre-#80 binding
c := rt.Candidates.Lookup(ParseRef(b.BuilderCandidate))
   on ErrUnknownCandidate: return text, nil          // the file changed under the binding; send without a preamble rather than fail the round
l := harness.Lookup(c.Harness).Launch(c.Provider, c.Model, c.ExtraArgs, RoleByName("builder"))
if l.Preamble == "": return text
return l.Preamble + "\n\n" + text
```

The `ErrUnknownCandidate` tolerance is new and deliberate: today a removed
alias fails `send`; a removed candidate must not, because the builder is
already running and the preamble is a courtesy for round 1 only.

### 4.8 `relay.Ask`

```
role, ok := harness.RoleByName(opts.Role)
   !ok → ErrUnknownRole (lists RoleNames())
role.Shape != ShapeConsult → ErrNotAConsultRole ("%q is the builder role; bind it")
c, err := resolveCandidate(rt.Candidates, opts.Candidate, opts.Role)
c.Tree == "none" → ErrTreelessUnsupported (unchanged)
agentName := opts.Name + "-" + role.Name + "-" + id
l := Launch(...); StartAgent(..., l.Kind, pane, l.Args); prompt = l.Preamble + consultPrompt
c.Role = role.Name
```

Error order: unknown role, wrong shape, candidate resolution, tree, name
length -- all before the lock, as today.

### 4.9 `relay.ConsultRolesTooLong`

```
func ConsultRolesTooLong(bindingName string) []string
```

Iterates `harness.RoleNames()` filtered to `ShapeConsult`; the candidate
set is no longer an input. Same arithmetic. `noteConsultRolesTooLong` in
`main.go` drops its table argument.

### 4.10 `doctor.assembleKinds`

```
func assembleKinds(set *candidate.Set, st *store.Store) (kinds []string, storeErr error)
```

Kinds are the distinct `Harness` values across the set plus every binding's
`Builder.Kind`, as today. New: when `set.Len() == 0`, `cmdDoctor` inserts a
global `SevWarn` row `candidates: none configured` with fix `write
~/.config/relay/candidates.json; see README "Candidates"`. Per-kind role
checks are unchanged -- they already read `harness.Roles`.

### 4.11 `bind` preflight kind (`cmdBind`)

The spawn branch resolves the kind through `resolveCandidate` (errors
swallowed: preflight is advisory) instead of `Aliases.Lookup`.

### 4.12 `relay candidates` (new subcommand)

```
relay candidates
```

Prints one line per configured candidate, sorted by ref:

```
claude/anthropic/sonnet                        builder, reviewer
opencode/openrouter/z-ai/glm-5.3-flash         builder
agy/google/gemini-3.8-flash-high               builder   [+--dangerously-skip-permissions]
```

Extra args are shown in brackets. Zero candidates prints the
`ErrNoCandidates` text and exits 0 -- it is a listing, not a check. No
flags. Not tested through the subcommand in `cmd/relay`; the formatter is a
pure function in `internal/relay` (or `internal/candidate`) with a test.

### 4.13 CLI flags

| command | flag | help |
|---|---|---|
| `bind`  | `--builder` | "candidate `harness/provider/model` to spawn, or a pane id to adopt; omit when exactly one candidate serves builder" |
| `add`   | `--builder` | same, minus adopt |
| `fork`  | `--builder` | same; "default: inherits source" |
| `ask`   | `--role`    | unchanged; "consult role: reviewer, researcher" |
| `ask`   | `--candidate` | new; "candidate `harness/provider/model`; omit when exactly one serves the role" |

The `:`-means-pane-id rule in `cmdBind` is unchanged; a ref never contains
`:`... except a model that does. **Rule:** a value is a pane id if it
contains `:` and does **not** contain `/`. Documented in the flag help.

## 5. High-level pseudocode

### 5.1 `candidate.Load`

```
raw := read(path); if not-exist: return empty Set
entries := json decode []Candidate; on error: wrap with path
for i, c in entries:
    if c.Harness == "" or c.Provider == "" or c.Model == "": error "candidate %d: harness, provider and model are required"
    if contains(c.Provider, "/"): error "candidate %d: provider must be a single segment"
    h, ok := harness.Lookup(c.Harness); if !ok: error "candidate %d: unknown harness %q (known: %v)"
    if len(c.Roles) == 0: error "candidate %d: roles must not be empty"
    for r in c.Roles:
        if _, ok := harness.RoleByName(r); !ok: error "candidate %d: unknown role %q (known: %v)"
        if !h.CanServe(r): error "candidate %d: harness %q has no definition for role %q"
    if c.Tree not in {"", "binding", "none"}: error "candidate %d: tree must be binding or none"
    key := c.Ref().String()
    if seen[key]: error "duplicate candidate %s at index %d and %d"
    set.byRef[key] = c
return set
```

### 5.2 Bind, spawn path

```
if opts.BuilderPane != "": adopt (unchanged)
c   := resolveCandidate(rt.Candidates, opts.Candidate, "builder")   -- errors propagate
role := RoleByName("builder")
l   := Lookup(c.Harness).Launch(c.Provider, c.Model, c.ExtraArgs, role)
agentName := builderAgentName(name); paneID := builderPane(...)
StartAgent(agentName, l.Kind, paneID, l.Args)
ep := Endpoint{AgentName, PaneID, Kind: l.Kind}; best-effort session lookup (unchanged)
… in Bind: b.BuilderCandidate = c.Ref().String(); b.PreamblePending = true (unchanged semantics: composePrompt decides whether there is anything to prepend)
```

### 5.3 `relay candidates`

```
set := rt.Candidates
if set.Len() == 0: print ErrNoCandidates text; return nil
for ref in set.Refs():
    c := set.Lookup(ref)
    print pad(ref) + join(c.Roles, ", ") + (extra_args? "   [" + join(ExtraArgs, " ") + "]" : "")
```

### 5.4 Error text the planner will read

```
relay bind needs --builder: a candidate harness/provider/model (configured: [a, b]), or a herdr pane id to adopt
no candidates configured; write ~/.config/relay/candidates.json (see README "Candidates")
2 candidates serve builder: [a, b]; name one with --builder
candidate "x/y/z" not found (configured: [a, b])
candidate "a" does not serve role "reviewer" (its roles: [builder])
unknown role "reviwer" (known: [builder reviewer researcher])
"builder" is the builder role; bind it with relay bind, not relay ask
```

## 6. Error handling strategy

New sentinel errors in `internal/candidate`:

| error | when | recoverable |
|---|---|---|
| `ErrBadRef` | token has fewer than three segments or an empty one | yes: retype |
| `ErrUnknownCandidate` | ref not in the set | yes: fix the file or the token |

New sentinel errors in `internal/relay`:

| error | when | recoverable |
|---|---|---|
| `ErrNoCandidates` | empty set and no token | yes: write the file |
| `ErrRoleNotServed` | no candidate (or the named one) serves the role | yes |
| `ErrAmbiguousCandidate` | ≥2 serve the role and no token | yes: name one |
| `ErrUnknownRole` | `ask --role` not in the table | yes |
| `ErrNoBuilderCandidate` | fork of an adopted source, no token, resolution failed | yes: `--builder` |

Deleted: `alias.ErrUnknownAlias`, `relay.ErrConsultAlias`,
`relay.ErrAliasRequired`, `relay.ErrNoBuilderAlias`. `ErrNotAConsultRole`
stays with new text.

All of these are raised before any lock is taken or pane created, as the
alias errors are today. `Load` errors surface at `newRuntime`, so every
subcommand fails fast on a malformed file -- including `status` and
`daemon`. That matches `LoadTable` today and is kept: a daemon running on a
config it cannot parse is worse than one that refuses to start.

The one softened error: `composePrompt` sends without a preamble on
`ErrUnknownCandidate` (§4.7).

No new log event kinds. `bind`, `add`, `fork` and `ask` already log what
they started; the recorded value changes from an alias name to a ref.

## 7. Ordered implementation steps

Each step is one plan / one builder session. `make check` green at every
step. A builder that finds a step impossible as written halts.

1. **`harness` role table and launch rendering.** Add `RoleShape`,
   `RoleSpec`, `roleTable`, `RoleByName`, `RoleNames`,
   `Harness.SelectsRoleByPreamble` (agy), `CanServe`, `Launch`. Tests:
   `Launch` output per kind for a fixed input, with and without extra args;
   `CanServe` for every (kind, role) pair; `RoleNames` order. No caller
   changes yet. Verify: `alias` package still compiles untouched.

2. **`candidate` package.** `Ref`, `ParseRef`, `String`, `Candidate`,
   `Serves`, `Set`, `Load`, `Lookup`, `ForRole`, `Refs`, `Len`, the two
   sentinels. Tests: round-trip on refs with slashes in the model; every
   validation branch in §5.1 with the expected message substring; missing
   file → empty set; `ForRole` ordering. Depends on 1.

3. **`store.Binding.BuilderCandidate`** replaces `BuilderAlias`; `status.go`,
   `ui/fetch.go`, `ui/list.go`, `answer.go` follow the rename. Test: a
   fixture `bind.json` with `builder_alias` decodes with
   `BuilderCandidate == ""`. Depends on nothing but is ordered here so
   step 4 compiles in one move.

4. **`Runtime.Candidates`, `resolveCandidate`, bind/add/fork.** Swap the
   runtime field, add `internal/relay/candidate.go` with the §4.3 rule and
   its table test, rewrite `resolveBuilder`, `Add`, `Fork` per §4.4–4.6,
   `composePrompt` per §4.7 with the unknown-candidate tolerance test.
   `main.go`: `candidate.Load` at
   `filepath.Join(configDir, "relay", "candidates.json")`, the pane-id rule
   in §4.13, flag help. `noteConsultRolesTooLong` temporarily takes no set
   (step 5 finishes it). Depends on 1–3. Verify: `go build ./...` with
   `internal/alias` still present but unreferenced by these files.

5. **`ask` and consult naming.** §4.8 and §4.9. `Consult.Role` comment.
   `AskOptions.Candidate`, `--candidate` flag. Tests in
   `internal/relay/ask_test.go` for the error order; `names_test.go` for the
   role-table version. Depends on 4.

6. **`doctor` and `relay candidates`.** §4.10–4.12. `cmd/relay/agent.go`
   hint text. Formatter test in `internal/relay`; no subcommand test.
   Depends on 4.

7. **Delete `internal/alias`.** `go build ./...` must pass with the
   directory gone; `grep -r alias` over `internal cmd` returns only prose
   hits, if any. Depends on 4–6.

8. **Docs.** README: "Builder aliases" → "Candidates" (file format, the
   three roles, launch rendering table, `extra_args` with the
   `--dangerously-skip-permissions` note as the edit you make once you trust
   the loop, the pane-id rule); "The reviewer alias" → "Consult candidates";
   "First run on a clean machine" gains "write `candidates.json`" as the
   step after installing role files. `docs/design.md` §"Builder aliases"
   rewritten; the `builder.alias` enum row becomes `builder_candidate`.
   `CLAUDE.md` "Dispatching work" paragraph: replace the three alias names
   with tokens and drop the "alias names do not track the priority order"
   note. Depends on 7.

Verification across the whole change, by the planner, not the builder:
`make check`; `git diff --stat` shows no file outside §2; mutation test
`resolveCandidate` by making the ≥2 branch return the first candidate and
confirming the ambiguity test fails; run `relay candidates` and `relay
doctor` on this machine with an empty `candidates.json` and with the three
entries from #80.
