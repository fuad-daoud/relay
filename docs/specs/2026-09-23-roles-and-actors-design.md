# Roles × candidates, and actors

Issue: #374 (slice S1). Slice S2 is #382 (the actor model).
Status: design approved 2026-09-23 as a direction; the user expects to rework
S2 before it is planned.

## 1. Why

relay can choose the *candidate* that runs a role (harness/provider/model, #80),
but not the *agent definition*, and not the set of roles:

- `roleTable` (`internal/harness/harness.go:39-58`) hard-wires builder →
  `plan-executor` (+ `researcher`), reviewer → `reviewer`, researcher →
  `researcher`. The launch line passes `role.Definition` (`harness.go:300-329`).
  The only way to run your own builder is to overwrite the shipped file, and
  #371's refresh then never updates it again.
- The role ↔ candidate relation is written twice: every candidate lists the
  `roles` it serves (`candidates.json`), and `policy.json` lists the candidate
  `order` per role. Tier is written twice too: `candidates[].tier` and
  `policy.tier.<role>`.
- The handoff rules a planner needs live only in the shipped `architect`
  definition ("Handing off"), so a planner running any other agent never
  receives them.

## 2. The model

Two tables and their product:

- **Roles** (`roles.json`, new): what a role *is* (its shape, whether it is
  gated, the agent definition it runs on each harness kind) and *its policy*:
  the candidates it uses, in order, and its tier.
- **Candidates** (`candidates.json`, as today, minus `roles` and `tier`): the
  harness/provider/model triples that can run anything.
- **Possibilities = roles × candidates.** Role R can run on candidate C when C
  is in R's `candidates` list and R has a definition for C's harness kind
  (shipped or custom). Whether that definition's file is on disk is the #238
  gate's question, not the loader's.

In S1, a round is still what it is today: `relay send` runs the builder role,
and `relay ask` runs a reader role beside it. S2 (§10) lets a round name any role.

## 3. `roles.json` (S1)

Path: `<userConfigRoot()>/relay/roles.json`, composed through
`userConfigRoot()` (`cmd/relay/main.go:381`) like every other config path.

```jsonc
{
  "builder": {
    "definitions": {
      "claude": { "agent": "my-executor", "requires": ["my-scout"] }
    },
    "candidates": [
      "opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high",
      "codex/openai/gpt-5.6-terra:high",
      "claude/anthropic/sonnet"
    ],
    "tier": "yolo"
  },
  "reviewer": {
    "candidates": ["claude/anthropic/sonnet", "codex/openai/gpt-5.6-terra:high"],
    "tier": "yolo"
  },
  "security-reviewer": {
    "shape": "reader",
    "definitions": { "claude": { "agent": "sec-review" } },
    "candidates": ["claude/anthropic/sonnet"]
  }
}
```

The file is an object keyed by role name. Each row:

| Field | Type | Rule |
|---|---|---|
| `shape` | `"writer"` \| `"reader"` | Built-in rows: optional, and it must equal the built-in shape if given. New rows: required. In S1, a new row must be `reader` (writer rows need `send --role`, S2): `roles.json: <name>: a new writer role needs relay send --role, not yet available`. |
| `gate` | bool | Optional. Writer only: `true` is refused on a reader. Built-in builder defaults to `true`. Nothing in S1 reads it; S2's writer rounds do, and it is validated now so that a file written today stays valid. |
| `definitions` | object, harness kind → `{agent, requires}` | Optional. A kind must be known (`harness.Lookup`). `agent` is required, and it must match `^[a-z0-9][a-z0-9._-]{0,63}$`, because it becomes a file name. `requires` is optional, a list of names with the same rule. For a built-in role, a kind not listed keeps the shipped definition. For a new role, a kind not listed has no definition, so candidates of that kind cannot run it. |
| `candidates` | list of candidate tokens | Optional. Every token must parse (`candidate.ParseRef`). Duplicates are refused. The order is the preference order (today's `order.<role>`). A token naming no configured candidate is tolerated at load and reported by `relay policy` (as `order` is today, `policy_view.go:72`). |
| `tier` | tier name | Optional. Parsed by `harness.ParseTier`, and it must not exceed `policy.json`'s `max_tier`. It replaces `policy.tier.<role>` and `candidates[].tier`. |

- **Built-in rows** (defaults, used when `roles.json` omits them): builder
  (writer, gate true, shipped `plan-executor` + `researcher` on every kind),
  reviewer (reader, shipped `reviewer`) and researcher (reader, shipped
  `researcher`), each with no candidates and no tier.
- **Overrides are field by field.** A row in the file replaces only the fields
  it sets, and `definitions` merges per kind.
- **Name collisions:** a custom `agent` equal to a shipped name
  (`plan-executor`, `researcher`, `reviewer`, `architect`) is allowed. It is
  then the shipped file, and relay keeps refreshing it. `Custom` (§4) is false
  exactly when `agent` and every `requires` name are shipped names for that
  kind.
- **Unknown fields** in a row or at the top level are **kept, not refused**.
  This is the #372 direction: `roles.json` is decoded without
  `DisallowUnknownFields`, and `relay doctor` warns about each unknown key.

## 4. Registry and resolution (S1)

New package `internal/roles`. It imports `harness`, `candidate` and `policy`.
None of them may import it: `policy` keeps validating its legacy `order` and
`tier` keys against `harness.RoleByName`, the built-in names.

- `Load(path) (*File, error)`: shape validation only (§3). A missing file
  gives `nil, nil`. It wraps `ErrBadRoles`.
- `Build(f *File, set *candidate.Set, pol policy.Policy) (*Registry, error)`:
  - `f != nil`: built-ins merged with the file (`Source = "roles.json"`).
    Cross-file checks run here: `tier` against `max_tier`.
  - `f == nil`: **legacy derivation** (`Source = "legacy"`). R *serves* on
    exactly the candidates whose `roles` contain R (today's
    `Candidate.Serves`).
    - R's `Ranked` list reproduces `rankedList` (`internal/relay/candidate.go:108-139`)
      exactly:
      1. first, each `pol.order[R]` token that parses, is configured, serves R
         and is not a repeat, with `Position = its 1-based index in
         order[R]`. The index counts skipped tokens too, as today's
         "order #N" does;
      2. then every other serving candidate, in `Set.ForRole` order (sorted
         by ref), with `Position = 0` ("unlisted, after order").
    - `Ordered = len(pol.order[R]) > 0`. When it is false and more than one
      candidate serves R, the resolver keeps today's `ErrAmbiguousCandidate`.
    - A token that doesn't serve R stays a `relay policy` warning
      (`policy_view.go:72`), computed from the legacy files as today.
    - R's tier stays today's chain (§4.1). Nothing about today's behaviour
      changes.
  - With `roles.json`, `Ranked` is the role's `candidates` tokens that are
    configured and have a definition for their kind, each with `Position =
    its 1-based index`. There are no unlisted entries, and `Ordered = true`.
- `Registry.Names() []string`: built-ins first in table order, then new roles
  sorted.
- `Registry.Role(name) (Role, bool)`.
- `Registry.Spec(name, kind string) (harness.RoleSpec, error)`: the launch
  spec for one role on one kind. `Definition` is the resolved `agent`, and
  `Definitions` is `agent` followed by `requires` (for shipped builder:
  `plan-executor, researcher`). Error `ErrNoDefinition` when the role has no
  definition for that kind.
- `Registry.Serves(name string, ref candidate.Ref) bool`: `Spec(name,
  ref.Harness)` succeeds, and in addition:
  - with `roles.json`: the ref is in the role's `candidates`;
  - in legacy mode: the candidate's `roles` contain the name. That is
    today's rule, so an explicit `--builder` token that is absent from
    `order` but serves the role stays allowed.
- `Registry.Source() string`: `"roles.json"` or `"legacy"`.

`Role` holds `Name`, `Shape` (`harness.ShapeBuilder` for writer,
`harness.ShapeConsult` for reader, in S1), `Gate bool`, `Candidates []string`
(the tokens as written: `order[R]` in legacy mode, `candidates` in the file),
`Ranked []Ranked{Token string; Position int}`, `Ordered bool`,
`Tier (harness.Tier, bool)`, `Builtin bool`, and
`Definitions map[kind]Definition{Agent string; Requires []string; Custom bool}`,
where the map holds the resolved definition for every kind the role runs on.

### 4.1 Tier chain

- With `roles.json`: explicit `--tier`, then the role's `tier`, then
  `harness`. Verify keeps its own rule (`verify.go:140`): the reviewer role's
  tier, else `yolo`.
- In legacy mode: unchanged (`internal/relay/tier.go:20`): explicit, then
  `candidate.Tier`, then `policy.TierFor(role)`, then `harness`.

`resolveTier` and `verifyTier` take the registry instead of reading `c.Tier`
and `pol.TierFor` directly.

### 4.2 Wiring

- `relay.Runtime` gains `Roles *roles.Registry`. The existing `Roles` field
  is the `harness.RoleChecker`, so the old field is renamed `RoleFiles`, and
  its ~6 uses are renamed with it.
- Every place that builds a Runtime from config loads `roles.json` after
  `candidates.json` and `policy.json`, then calls `Build`: `cmd/relay/main.go`
  near `:533`, and `cmd/relay/serve.go:269`.
- **Remote rounds:** a server resolves through *its own* `roles.json` or
  legacy config. The client's registry never travels to it.

### 4.3 Call sites that move to the registry

| Site | Today | After |
|---|---|---|
| `internal/relay/send.go:245`, `headless.go:202,287`, `bind.go:606` | `harness.RoleByName("builder")` | `rt.Roles.Spec("builder", c.Harness)` |
| `internal/relay/verify.go:231` | `RoleByName("reviewer")` | `rt.Roles.Spec("reviewer", c.Harness)` |
| `internal/relay/ask.go:132` | `RoleByName(opts.Role)`, and a shape check | `rt.Roles.Role` + `Spec`. A writer role returns `ErrNotAConsultRole`, whose text is generalised to "a writer role; bind it / send it, not relay ask" |
| `internal/relay/names.go:20` | built-in consult roles | every reader role in the registry |
| `internal/relay/probe.go:79` | first known role in `c.Roles` | the first role in `rt.Roles.Names()` that `Serves` this candidate; none returns `"serves no role"` |
| `internal/relay/candidate.go:108,186` `rankedList`, `resolveCandidate` | `set.ForRole` + `pol.OrderFor` | `rt.Roles.Role(role).Ranked` (`Position > 0` → `HowOrder`, `0` → `HowUnlisted`). Today's rules stay: one candidate → `HowSole`; several and `!Ordered` → ambiguous. An explicit token must satisfy `Serves`, or it is `ErrRoleNotServed` |
| `internal/relay/policy_view.go:39,72,115,184` | `harness.RoleNames()`, `c.Roles` | `rt.Roles.Names()`, `Serves` |
| `internal/relay/candidates_list.go:50` | the `roles` column from `c.Roles` | the roles that `Serves` the candidate, from the registry |
| `internal/harness/roles.go:54` `osRoleChecker.Missing(kind)` | the builder's shipped `Definitions` | `Missing(kind string, defs []string)`: the caller passes the resolved `Spec(...).Definitions`. `MissingDefinitions` resolves a non-shipped name to that kind's path convention (§5) |
| `cmd/relay/doctor.go:76,102` | `RoleByName` | the registry: definitions per kind for every role some candidate on that kind serves |
| `internal/candidate/candidate.go:203-212` | `roles` required, checked by `CanServe` | `roles` optional. If present, each entry must be a built-in name (legacy only). The `CanServe` check is removed: every kind ships every built-in definition, and a custom definition is the gate's job. `tier` is still parsed, but read only in legacy mode |
| `internal/harness/harness.go:246` `CanServe` | used by the candidate loader | deleted (its only caller is gone) |

## 5. Definition files, gate and doctor (S1)

- **Path convention for a name N on a kind**, home-relative, where a shipped
  row's `Path` wins for shipped names:
  - claude: `.claude/agents/N.md`
  - opencode: `.config/opencode/agents/N.md`
  - agy: `.gemini/config/agents/N.md`
  - codex: `.codex/N.config.toml`

  One function, `harness.DefinitionPath(kind, name) string`, owns this.
  Home-dir only; repo-local agent dirs are out of scope.
- **Gate (#238):** a candidate is gated for role R when any of
  `Spec(R, kind).Definitions` is missing on disk. That is the same `RolesMissing`
  gate as today (`internal/relay/ledger.go:236-260`), now per role and not only
  for the builder.
- **Install and refresh (#371)** iterate only `harness.Harness.Roles`, the
  shipped rows. They never write, refresh or overwrite a custom name. S1
  asserts this with a test; the code stays as it is.
- **Doctor:**
  - a `roles` row per role and kind, e.g. `builder  claude: my-executor
    (custom) ok` or `… missing ~/.claude/agents/my-executor.md`;
  - a row with the registry source (`roles.json` / `legacy`);
  - when `roles.json` exists: one warning per legacy field still set
    (`candidates[i].roles`, `candidates[i].tier`, `policy.order`,
    `policy.tier`), saying it is ignored;
  - a warning per unknown key in `roles.json`.

## 6. Visibility (S1)

- `relay policy` prints, per role: its source, its candidate order, its tier,
  and its definition per kind, with `(custom)` marked.
- `relay status` (text and JSON) adds the builder's resolved definition, as
  `builder_definition` in JSON, with `(custom)` marked in text.
- The round's pick/send log entry records `definition` and
  `definition_custom`, and `relay show` / `relay history` print them. The
  rendered `NNN-builder.log` is left alone, because the limit scan reads its
  tail.

## 7. Planner handoff rules (S1)

- **One source:** the "Handing off" section moves to
  `internal/planner/handoff.md` (embedded).
- `relay planner init --hook claude` appends it to the hook context after the
  planner sentence, in both `HookOutput` and `HookOutputNoEnv`. `HookNote`
  (failure) does not append it.
- The four `architect.*` definitions keep their copies. A test holds each copy
  byte-equal to `handoff.md`:
  - Markdown kinds: the text from `## Handing off` to the end of file.
  - codex TOML: the same, up to the closing `'''`.
  - Today the three Markdown copies are identical, and codex differs by its
    delimiter line only.
- Changing an `agents/` file needs `sh scripts/agents-shipped.sh --write`.
- An architect session receives the rules twice, which costs about 2.7 KB. That
  is accepted.

## 8. `relay roles` (S1)

- **`relay roles`** prints the registry: role, shape, source, candidates in
  order, tier and definitions per kind. It is the same data as the `relay
  policy` roles block, without the gates.
- **`relay roles init [--dry-run] [--force]`** writes `roles.json` from the
  legacy fields. It is pure translation:
  - `candidates` come from `order.<role>`, else from the candidates whose
    `roles` name the role.
  - `tier` comes from `policy.tier.<role>`, else from the candidates' `tier`
    when every candidate in the role's list has the same one.
  - If candidate tiers differ within one role and there is no policy tier,
    init refuses and names the role and the tokens (`ErrAmbiguousTier`).
    The user decides.
  - It refuses to overwrite an existing `roles.json` without `--force`.
  - It never edits `candidates.json` or `policy.json`. Doctor then lists the
    legacy fields to delete.

Against today's config on this machine, init produces:
- builder: its six-token order, tier `yolo`;
- reviewer: two tokens, tier `yolo` (both candidates are yolo, and there is
  no policy tier);
- researcher: `claude/anthropic/haiku`, no tier.

## 9. Errors (S1)

| Error | Package | When |
|---|---|---|
| `ErrBadRoles` | roles | `roles.json` fails the §3 validation. The message names the file, the row and the field, and follows `policy.Load`'s style |
| `ErrNoDefinition` | roles | `Spec(role, kind)` for a kind the role has no definition on |
| `ErrUnknownRole` | relay (exists) | now means a name absent from the registry |
| `ErrRoleNotServed` | relay (exists) | the candidate is not in the role's list, or the role has no definition for the candidate's kind |
| `ErrAmbiguousTier` | roles | `roles init` cannot translate one tier |

- A bad `roles.json` is fatal wherever a bad `policy.json` is fatal today.
- A missing definition file is never fatal: it gates the candidate.

**Compatibility, related to #372:** an older relay binary ignores `roles.json`
(it doesn't know the file), and so keeps running on legacy config while newer
processes read `roles.json`. After `relay roles init`, delete the legacy
fields only once every relay process on the machine is upgraded. The docs say
so. Nothing in S1 adds a key to `policy.json` or `candidates.json`, so S1 does
not trip #372's `DisallowUnknownFields` problem.

## 10. S2 outline: the actor model (separate issue; to be reworked)

- A binding is a worktree and a branch. `relay send <name> <file> [--role R]`
  (default `builder`) runs one **actor** for one round: R's definition on a
  candidate from R's list.
- **Writer rounds:** as today, with tree check, diff and `gate` if set.
- **Reader rounds:** run in the binding's tree, produce `NNN-<role>.md` as the
  artifact, have no gate and no diff, and refuse to close if the tree changed.
- At most one round is open per binding. `relay ask` stays the concurrent side
  channel for reader roles, and verify stays an ask.
- A built-in `planner` reader role, backed by the shipped `architect`.
- New **writer** roles in `roles.json` are allowed; #202's frontend roles
  become rows.
- Vocabulary: builder → actor across state and wire names (`bind.json`,
  status JSON, `--builder`, `NNN-builder.*` → `NNN-<role>.*`). There is a
  doctor-guided state migration, which reuses #292's migration machinery and
  #372's format version, and lands after #372.

## 11. Testing (S1)

All tests are pure, in `internal/roles`, `internal/relay`, `internal/harness`
and `internal/planner`. No CLI test spawns a harness or reaches the network,
and `cmd/relay` tests use the TestMain temp root (#235).

- `roles.Load`: every §3 rule, one case each, including a new writer row
  refused, an unknown field kept, and a bad agent name.
- `roles.Build` legacy derivation: against a fixture equal to this machine's
  config, the registry gives the same candidate order and tier per role as
  today's `resolveCandidate` and `resolveTier`. This is the "no behaviour
  change" pin.
- `roles.Build` with a file: the per-kind merge (a claude override leaves
  opencode on `plan-executor`), and `Custom` set correctly, including for a
  shipped-name collision.
- Gate: a custom definition missing on claude gates the claude candidates
  only.
  - Mutation check: make `MissingDefinitions` ignore non-shipped names, and
    that test must fail.
- Install/refresh: a custom `.claude/agents/my-executor.md` is untouched by
  `Install` with `--force`.
- `roles init`: this machine's config gives the §8 output, and mixed
  candidate tiers give `ErrAmbiguousTier`.
- Handoff: `handoff.md` is byte-equal to each architect copy, and the hook
  output contains it.
  - Existing hook tests that pin the exact context are ported: their expected
    text gains the appended rules.

## 12. Out of scope

- Repo-local agent directories.
- The client's roles travelling to a remote server.
- The read-tier cap for reader roles: verify's yolo reviewer is deliberate.
- Per-candidate tier overrides inside a role's list.
- Everything in §10 until its own issue is planned.
