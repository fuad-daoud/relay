# Writer roles: a binding runs your own writer role

**Issue:** #382, S2 of `docs/specs/2026-09-23-roles-and-actors-design.md`, narrowed on
2026-09-24.
**Status:** design approved 2026-09-24.

This replaces §10 of the roles-and-actors spec for #382. The rest of §10 moved
elsewhere:

| was in §10 | now |
|---|---|
| reader rounds (`send --role <reader>`, `NNN-<role>.md`, refuse a changed tree), chaining roles on one binding | #388 |
| a built-in `planner` role; relevo driving plan → build → next plan | #389 |
| the builder → actor vocabulary rename, and its state migration | deferred, no issue |

## 1. Goal

A user can run their own roles, readers and writers alike, on their own custom
agents.

S1 (#374) already covers most of it:

- A custom **reader** role is a `roles.json` row with `"shape": "reader"`. It runs
  through `relevo ask --role <name>`, which resolves it through the registry
  (`internal/relevo/ask.go`, `Ask`).
- A custom **agent for the built-in builder** is `definitions.<kind>.agent` on the
  `builder` row.

What is missing is a **new writer role**, such as `ui-builder`, whose rounds change
the tree the way the builder's do. `internal/roles/file.go` refuses one today:
"a new writer role needs `relevo send --role`, not yet available". S2 lifts that
refusal and gives such a role a way to run.

## 2. The model

- **A binding runs exactly one writer role, chosen when it is created.**
  - It is set with `relevo add --role <r>` or `relevo bind --role <r>`. The default
    is `builder`.
  - `relevo fork` inherits the source binding's role.
  - The role cannot be changed later. A different role means a different binding
    (#388 is where one binding alternates roles).
- **Every round of that binding runs the role:** its definition for the
  candidate's harness kind, on a candidate from the role's own `candidates` list,
  at the role's tier.
- **There is no `send --role`.** `relevo send` runs the binding's role.
- **Reader roles are unchanged.** `relevo ask --role <reader>` stays the only way
  to run one.

## 3. `roles.json`

A new row may give `"shape": "writer"`. Such a row follows the S1 rules for
`definitions`, `candidates` and `tier`, plus one more:

- **`gate`** is optional. For a **new** writer row it **defaults to `true`**, the
  same as the built-in builder, because a writer changes the tree and the
  project's own check applies to its work. `"gate": false` opts out.

A new writer row with no `definitions` has no definition on any kind. It can run
nowhere, and `relevo roles` and doctor say so. This is the same rule S1 applies to
a new reader row.

A shipped name used as `agent` (`plan-executor`, `researcher`, `reviewer`,
`architect`) is allowed, as in S1. A `ui-builder` row whose claude definition is
`plan-executor` is a builder under another name, with its own candidates and tier.

## 4. CLI surface

| command | change |
|---|---|
| `relevo add`, `relevo bind` | New `--role <r>` flag, default `builder`. Refused when `r` is unknown (`unknown role "x" (known: …)`) or a reader (`--role reviewer: a reader role runs through relevo ask --role reviewer`). |
| `relevo fork` | No flag. The fork inherits the source binding's role. |
| `--builder <token>` (on add, bind, fork and send) | The token must be able to serve the binding's role: it has a definition for its kind. The same check `resolveRole` makes today, against the binding's role instead of `"builder"`. |
| `relevo status` | The builder line ends with `role <r>` when the role is not `builder`. `status --json` gets `"role"` on the row, omitted for `builder`. |
| `relevo ask --role` | Help text only: `consult role (a reader role in roles.json; default ones: reviewer, researcher)`. |
| `relevo roles` | Unchanged. It already lists every row with its shape. |

The flag and the state keep the word `builder` (`--builder`, the `Builder` endpoint,
`NNN-builder.*`). It names the binding's **writer slot**, not the role. The rename
is deferred.

## 5. Internals

### 5.1 One accessor

`func bindingRole(b store.Binding) string` returns `b.Role`, or `"builder"` when
that is empty. Every place in `internal/relevo` that passes the literal
`"builder"` as a **role** asks it instead:

- `resolveRole(…, "builder")` and `resolveRoleTier(…, "builder")` in `add.go`,
  `bind.go`, `fork.go`, `served.go`, `switch.go`, `builder_change.go` and
  `candidate.go`;
- `rt.RoleRegistry().Spec("builder", kind)` in `send.go`, `headless.go` and
  `bind.go`;
- the custom-definition lookup in `status.go` (`Role("builder")`), which then
  reports the binding's role's definition;
- the gate decision. A binding's gate command is resolved when the binding is
  created (`resolveGate` in add, bind and fork). With neither `--gate` nor
  `--no-gate`, the role's `gate` decides: true takes policy.json's
  `gate.default`, false takes no gate. An explicit `--gate` still wins.

These do **not** change, because they name the slot and not the role:

- `pickEntry(…, "builder", …)` log lines (`picked <tok> for builder: …`). Keeping
  them matters: `internal/ingest/outcome.go` treats a pick for any other role name
  as a consult pick.
- `RunningProcRef.Kind == "builder"`.
- `store` round file names.
- `remote.FeatureBuilder`.

### 5.2 State: additive, at the lowest format that holds it

`store.Binding` gains `Role string` with tag `json:"role,omitempty"`.

#372's rule is that an older relevo must refuse to rewrite a shape it does not
know. An older relevo that ignored `role` would run `plan-executor` on a
ui-builder binding without a word. The rule here is **the lowest format that can
hold the record**:

- A binding whose `Role` is empty or `builder` is written at format 1, as today,
  so it is byte-identical.
- A binding with any other role is written at **format 2**.
- `BindingFormat` (the highest format this binary knows) becomes 2.
- The format written is chosen per record, from `Role`.
- Reading accepts 1 and 2. A format-1 binding reads with an empty role, meaning
  builder.

The effect is that an older relevo refuses to save exactly the bindings it would
get wrong, and keeps working on every other one. Bumping every binding to 2 would
lock a downgrade out of all bindings. That was rejected.

### 5.3 Remote bindings

S1 settled that a server uses its own roles.

- `remote.CreateBindingRequest` gains `Role string` with tag
  `json:"role,omitempty"`.
- A new `WhoAmI.Features` token, `FeatureRoles = "roles"`, is advertised by a
  server that honours `Role`.
- **Client:** `relevo add --server S --role r`, with `r != builder`, is refused
  when S does not advertise `roles`: `server S does not run custom roles; upgrade
  it`. An old server would otherwise ignore the field and run the builder.
- **Server:** it resolves `r` against **its own** registry. An unknown `r`, or a
  reader, is refused with the same errors as §4. It stores the role on the served
  binding, and every served round runs that role (`served.go` goes through
  `bindingRole`).
- The client's `roles.json` never travels. The same role name may mean different
  definitions on the client and on the server, and that is intended.

### 5.4 Doctor

A custom writer role's definitions get the same on-disk check (#238's gate) that
custom reader roles get today. If a binding names a role that `roles.json` no
longer defines, a doctor row names the binding and the role. At round start that
binding fails with the `unknown role` error, not a fallback to builder.

## 6. Errors

| when | error |
|---|---|
| `--role` names no row | `unknown role "x" (known: builder, reviewer, …)`, wrapping `ErrUnknownRole` |
| `--role` names a reader | `--role reviewer: a reader role runs through relevo ask --role reviewer`, wrapping a new `ErrNotAWriterRole` |
| the role has no definition for the picked candidate's kind | the existing `ErrRoleNotServed` message, naming the role |
| a binding's role disappears from `roles.json` | at round start: `binding b runs role "x", which roles.json no longer defines`, wrapping `ErrUnknownRole`. No fallback. |
| `--server` without the `roles` feature | `server S does not run custom roles (role "x"); upgrade it` |
| an older relevo meets a format-2 binding | the existing `ErrNewerFormat` (#372) |

## 7. Testing

The rule tests go in `internal/relevo` and `internal/roles` as pure functions or
temp-store tests. A cmd/relevo test spawns nothing and reaches no network; it
checks flag parsing and error text only.

- **`roles`:**
  - a new writer row loads;
  - its `gate` defaults to true;
  - `"gate": false` is kept;
  - the S1 refusal test is ported: its assertion flips from "refused" to
    "loads".
- **`bindingRole`:** an empty role and `"builder"` both give `builder`.
- **add/bind:** `--role ui-builder` picks from ui-builder's candidates and
  persists `Role`. `--role reviewer` gives `ErrNotAWriterRole`, and an unknown
  role gives `ErrUnknownRole`.
- **Send:** the launch spec is the role's definition, not `plan-executor`.
  Assert it on the spec `Launch` receives, with a fake harness.
- **Gate:**
  - a writer role with `gate: false` closes without a gate;
  - the builder still gates;
  - a new writer with no `gate` key gates.
- **Switch on a usage limit:** the next candidate comes from the role's list.
- **Fork:** the fork inherits `Role`.
- **Format:**
  - a builder binding saves with no `format` key, byte-identical to a golden;
  - a ui-builder binding saves with `"format": 2`;
  - a relevo whose `BindingFormat` is 1 refuses to save the latter (the #372
    test pattern).
- **Remote:**
  - with the feature missing, a create with `Role` is refused client-side;
  - the server resolves the role against its own registry and refuses a reader.
- **Status:** the `role <r>` suffix appears only for a non-builder role, and the
  JSON `role` is omitted for builder.
- **Mutations:** hard-code `"builder"` back in `send.go`'s `Spec` call; write
  format 1 for a role binding; default a new writer's gate to false. Each one
  must fail a named test.

## 8. Out of scope

- Reader rounds, `send --role` and chains (#388).
- A `planner` role and unattended runs (#389).
- The builder → actor rename and any state migration.
- Changing a binding's role after it is created.
- The client's roles travelling to a server.
