# Plan: #382 round 3: a remote binding runs a server-side writer role

## 1. System overview

Spec: `docs/specs/2026-09-24-writer-roles-design.md` §5.3. Read it first.

Rounds 1–2 (on this branch) made a local binding run a `roles.json` writer role:

- `store.Binding.Role`;
- `bindingRole` / `relevo.BindingRole`;
- `checkWriterRole`;
- `bindingSpec`;
- `AddOptions.Role`;
- `relevo add --role`.

Round 1 also added a temporary refusal in `relevo.Add`, which this round
replaces. When `normRole(opts.Role) != ""` and `opts.Server != ""`, it returns
`server %s does not run custom roles (role %q); not yet available`.

Read `internal/relevo/writer_role.go` and the top of `Add`
(`internal/relevo/add.go` ~105-130) first.

This round, per S1's rule that **a server uses its own roles**:

- **Wire:**
  - `remote.CreateBindingRequest` gains `Role`;
  - a new feature token, `FeatureRoles = "roles"`, is advertised by a server that
    honours it.
- **Client** (`relevo add --server S --role r`):
  - does **not** check `r` against its own `roles.json`, because the client's
    roles never travel;
  - refuses when S does not advertise `roles`;
  - sends `Role`;
  - records `Role` on its local mirror of the binding, so `relevo status` shows
    it.
- **Server:**
  - resolves `r` against **its own** registry, refusing an unknown role or a
    reader;
  - picks the candidate and the tier from that role;
  - stores `Role` on the served binding.

Every served round then runs the role through round 1's `bindingSpec`. That
covers `headless.go`, `switch.go` and `serve/rounds.go`, which already call
`bindingRole`.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 2. Files touched

```
internal/remote/proto.go        CreateBindingRequest (~67-79): Role; FeatureRoles const (next to FeatureBuilder ~234)
internal/serve/routes.go        WhoAmI Features list (~113): add remote.FeatureRoles
internal/serve/bindings.go      handleCreateBinding (~66-165): role check, role-aware pick and tier, Role on the literal (~143)
internal/relevo/served.go       PickServedCandidateFor, ResolveServedTierFor (+ the old names as builder wrappers) (~165-235)
internal/relevo/writer_role.go  export CheckWriterRole(rt Runtime, role string) error (wraps checkWriterRole)
internal/relevo/add.go          Add (~105-130): drop round 1's refusal; skip checkWriterRole on the server path
internal/relevo/remote.go       addRemote (~59-330): feature check, createReq.Role (~230), local mirror Role (~305)
internal/serve/serve_test.go    new tests
internal/relevo/remote_test.go  new tests
README.md                       one sentence in the Roles section
```

## 3. Data structures

- **`remote.CreateBindingRequest`:** add `Role string` with tag
  `json:"role,omitempty" // "" = builder; resolved against the server's own roles.json`.
- **`remote.FeatureRoles`:** `const FeatureRoles = "roles"`. Its doc comment:
  the WhoAmI.Features token a server that honours CreateBindingRequest.Role
  advertises (#382); a server without it would ignore the field and run the
  builder.

## 4. Contracts

### `internal/relevo/served.go`

- `PickServedCandidateFor(rt Runtime, role, token string) (string, string)` is
  today's `PickServedCandidate` body, with `role` in place of `"builder"`
  (~219). `PickServedCandidate(rt, token)` becomes a wrapper passing
  `"builder"`.
- `ResolveServedTierFor(rt Runtime, role, token, explicit string) (harness.Tier, error)`
  is today's body with `role` in place of `"builder"` (~195).
  `ResolveServedTier` becomes a wrapper passing `"builder"`. Update the chain
  comment to say `tier.<role>`.
- `ServedBuilderTier` is unchanged. It still reports the builder's tier.

### `relevo.CheckWriterRole(rt Runtime, role string) error`

Returns `checkWriterRole(rt.RoleRegistry(), role)`, with the same errors
(`ErrUnknownRole`, `ErrNotAWriterRole`). Also export
`func NormRole(role string) string`, delegating to `normRole`.

### Server: `handleCreateBinding` (`internal/serve/bindings.go`)

After the `rt, err := s.runtime(caller)` block and before `PickServedCandidate`:

```
role := relevo.NormRole(req.Role)
if role != "" {
    if err := relevo.CheckWriterRole(rt, role); err != nil {
        writeErr(w, http.StatusBadRequest, remote.CodeInvalid, err.Error()); return
    }
}
roleName := role; if roleName == "" { roleName = "builder" }
candidateToken, harnessKind := relevo.PickServedCandidateFor(rt, roleName, req.Candidate)
tier, err := relevo.ResolveServedTierFor(rt, roleName, candidateToken, req.Tier)
```

- The tier-error message
  `format = "policy tier.builder %s exceeds max_tier %s"` names `roleName`
  instead of `builder`. For a builder binding it stays byte-identical.
- The `store.Binding` literal (~143) gains `Role: role`.

The role check must run **before** `InitBare`. Today `InitBare` runs before the
pick (~118), so place the role check right after the `Store.Load` duplicate
check (~108). A refused create must leave no bare repo behind.

### Client: `Add` (`internal/relevo/add.go` ~105-130)

- **Delete** round 1's "not yet available" refusal.
- `checkWriterRole(rt.RoleRegistry(), opts.Role)` runs **only on the local
  path**, after the `if opts.Server != ""` branch has returned into
  `addRemote`. Move the call if round 1 put it first. The local path must behave
  exactly as it does after round 1.

### Client: `addRemote` (`internal/relevo/remote.go`)

1. **Before any branch or worktree work**, where the `--tier` feature check sits
   (~214-224, `who, err := rt.Remote.WhoAmI(...)`):
   - when `NormRole(opts.Role) != ""` and `who.Features` lacks
     `remote.FeatureRoles`, return
     `fmt.Errorf("server %s does not run custom roles (role %q); upgrade it", opts.Server, opts.Role)`;
   - reuse the one WhoAmI call if the tier check already made it. If WhoAmI is
     called only under `opts.Tier != ""` today, restructure so it is called when
     either flag needs it, and **at most once**.
2. `createReq.Role = normRole(opts.Role)` (~230).
3. The local mirror `store.Binding` literal (~305) gains
   `Role: normRole(opts.Role)`.
4. The server's refusal (400 with the role message) surfaces as the HTTP error it
   is. Do not reword it.

## 5. Pseudocode

```
client: relevo add --server S --role ui-builder
  Add -> opts.Server != "" -> addRemote          (no local roles.json check)
  who := WhoAmI(S); "roles" ∉ who.Features -> "server S does not run custom roles (role "ui-builder"); upgrade it"
  CreateBinding(S, {..., Role:"ui-builder"})
server: handleCreateBinding
  CheckWriterRole(serverRT, "ui-builder")        unknown/reader -> 400, no bare repo
  PickServedCandidateFor(serverRT, "ui-builder", req.Candidate)
  ResolveServedTierFor(serverRT, "ui-builder", token, req.Tier)
  Save(Binding{Role:"ui-builder", ...})          -> format 2 on the server's disk
client: local mirror Binding{Role:"ui-builder"}   -> status shows "role ui-builder"
served round: startRound -> bindingSpec(serverRT, b, kind) -> the server's ui-builder definition
```

## 6. Error handling

| condition | where | result |
|---|---|---|
| server lacks `roles` | client `addRemote` | refused before any branch, worktree or create call |
| role unknown on the server | server | 400 `unknown role "r" (known: …)`; no bare repo, no binding |
| role is a reader on the server | server | 400 `--role r: a reader role runs through relevo ask --role r` |
| role later removed from the server's roles.json | served round start | round 1's `bindingSpec` error, on the server |

## 7. Tests

**`internal/serve/serve_test.go`**, modelled on
`TestCreateBindingResolvesTierFromPolicy` (line 775) and its helper
`newTierTestServer` (line 732). The server's `Config.Registry` is how a test
gives it a `roles.json`. Build one with `roles.Build(&roles.File{Rows: …}, cSet, pol)`.

1. **`TestCreateBindingRunsServerRole`**. Build a registry where:
   - `builder` has candidates `[claude/anthropic/haiku]`;
   - `ui-builder` is a writer with the same candidate and the claude agent
     `srv-ui`.

   Create with `Role: "ui-builder"`. Expect:
   - status 201;
   - the stored binding has `Role == "ui-builder"`;
   - its `BuilderCandidate` is from ui-builder's list.
2. **`TestCreateBindingUnknownRoleRefused`**. Create with `Role: "nope"`.
   Expect:
   - status 400, with a body containing `unknown role "nope"`;
   - no binding stored;
   - no bare repo under the caller's repo root. Check the directory the handler
     would have created.
3. **`TestCreateBindingReaderRoleRefused`**: `Role: "reviewer"` gives 400 and a
   body containing `a reader role runs through relevo ask`.
4. **`TestWhoAmIAdvertisesRoles`**: WhoAmI's Features contain `roles`. Extend
   the existing features test instead if one exists
   (`grep -n 'FeatureIdempotentSend\|FeatureAuthor' internal/serve/*_test.go`).

**`internal/relevo/remote_test.go`**, modelled on
`TestAddRemoteTierPreTierServerRefused` (line 666) and
`TestAddRemoteTierWiresRequestAndEchoesBinding` (line 701):

5. **`TestAddRemoteRolePreRolesServerRefused`**. The fake WhoAmI lacks `roles`.
   `Add` with `Server` and `Role: "ui-builder"` returns the `upgrade it` error.
   Expect no CreateBinding call, and no branch or worktree.
6. **`TestAddRemoteRoleWiresRequest`**. The fake WhoAmI has `roles`. The
   recorded CreateBinding request has `Role == "ui-builder"`, and the local
   mirror binding has `Role == "ui-builder"`. The client's registry has **no**
   ui-builder row: this proves the client does not check its own roles for a
   server add.
7. **`TestAddRemoteBuilderUnchanged`**. No role, and a fake WhoAmI lacking
   `roles`: the add still succeeds, and the request has `Role == ""`. An old
   server keeps working for builder bindings.

**Existing tests must pass unedited.** That includes `TestAddCustomRoleOnServerRefused`
from round 1: it now passes Remote as nil or failing, so check what it asserts.
If it pinned the round 1 message `not yet available`, **port it**: change its
assertion to the new `upgrade it` message, with a fake WhoAmI that lacks
`roles`. Cite this plan's §4 in the test comment. It is the only sanctioned
test edit.

**Mutations.** Apply each one, run the named test, confirm it fails, then revert.

- **M1:** the server ignores `req.Role`, keeping the builder pick and no
  `Role`. Test 1 fails.
- **M2:** the role check runs after `InitBare`. Test 2 fails on the bare repo.
- **M3:** the client skips the feature check. Test 5 fails.
- **M4:** the client calls `checkWriterRole` on the server path. Test 6 fails.

## 8. Working efficiently

Each model step costs a full round trip, so:

- Read every file in §2 as parallel tool calls in one step. Line ranges are
  given; do not re-find them.
- Make every change to one file in one edit call.
- **Focused loop:**
  - `go test -count=1 ./internal/serve/ -run 'Create|WhoAmI'`
  - `go test -count=1 ./internal/relevo/ -run 'AddRemote|Role'`
  - `go build ./...`
- **Once at the end:** `make check` and `sh scripts/check-name.sh`. `make e2e` is
  not required.

## 9. Ordered steps

1. **Wire and server helpers.** Deliverable: `Role`, `FeatureRoles`, the WhoAmI
   feature, `PickServedCandidateFor`, `ResolveServedTierFor`, `CheckWriterRole`
   and `NormRole`. Verify: `go build ./...`.
2. **Server create.** Deliverable: the `handleCreateBinding` changes and tests
   1–4. Verify: `go test -count=1 ./internal/serve/`.
3. **Client.** Deliverable: the `Add` reorder, the `addRemote` feature check,
   the request `Role`, the mirror `Role`, tests 5–7, and the port of round 1's
   test if needed. Verify: `go test -count=1 ./internal/relevo/`.
4. **Mutations M1–M4.**
5. **README.** In the Roles section, after the `shape` bullet round 2 wrote,
   add: with `relevo add --server S --role <r>`, the server resolves `<r>`
   against **its own** `roles.json`, and your local `roles.json` does not travel.
   A server too old to run custom roles refuses the add. Verify:
   `sh scripts/check-name.sh`.
6. **Two leftovers from round 2** (`cmd/relevo/main.go`, `cmdBind`, ~1035-1065):
   - **`*rebind` case:** it computes
     `kind = relevo.CandidateKindFor(rt, opts.Candidate, roleName)` with
     `roleName` = builder, because `--role` is refused with `--resume`. A rebind
     replaces the builder of an **existing** binding, so the role must be the
     stored one:
     1. when `*name != ""`, load the binding as the `adopted` case does;
     2. set `specRole = relevo.BindingRole(existing)`;
     3. call `CandidateKindFor(rt, opts.Candidate, specRole)`.

     If the load fails, keep today's behaviour.
   - **The preflight comment** above `Spec(specRole, kind)`: it says "The
     builder's definitions for this kind come from the registry". Reword it to
     "The binding's role's definitions …", so it names a custom writer too.

   No test is required for the comment. For the rebind, add one to
   `cmd/relevo/main_test.go` only if an existing test already drives
   `cmdBind --resume --rebind` without spawning. Otherwise, say so in the report.
7. **Full check and commit.** `make check` passes. Then make one commit:

       feat(serve): a remote binding runs the server's own writer role (#382)

   Do not push. The report lists:
   - the files changed;
   - each mutation and the test that failed for it;
   - whether round 1's server-refusal test was ported;
   - the `make check` result.
