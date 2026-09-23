# Plan: a remote builder commits as the client's git identity (#335)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit.

## Working efficiently

Each model step costs a few seconds of round trip, so:
- Read everything in the "Read first" list below in **one** step (parallel
  reads of the named ranges). Don't grep for what this plan already locates.
- Make every change to a file in one edit call (several hunks).
- Loop with `go build ./... 2>&1 | head -80` and focused
  `go test ./internal/<pkg>/ -run '<names>'`, fixing every reported error
  before the next run. Run `make check` once at the end.

Read first (all paths from the repo root):
- `internal/relay/runtime.go` lines 20-100 (the `Git` interface)
- `internal/git/client.go` lines 55-80 (`gitEnv`, `run`) and 1020-1060 (`RepoFacts`)
- `internal/relay/remote.go` lines 53-260 (`addRemote`)
- `internal/remote/proto.go` lines 45-70 (`CreateBindingRequest`)
- `internal/serve/bindings.go` lines 50-150 (`handleCreateBinding`)
- `internal/store/types.go` lines 465-525 (`Binding.Serve`, `ServeFacts`)
- `internal/relay/headless.go` lines 110-160 (`startRound`)
- `internal/relay/runner.go` lines 1-40 (`ProcSpec`)
- `internal/relay/fake_test.go` lines 90-110 and ~431 (`fakeGit`)
- `internal/relay/remote_test.go` lines 40-110 (`fakeRemote`) and 680-770 (tier request tests, the pattern for new tests)
- `internal/serve/admit_test.go` lines 70-100 (`sendRound`), `internal/serve/serve_test.go` lines 1290-1330 (`scriptRunner`) and 1460-1560 (`setupTestEnv`)
- `cmd/relay/doctor.go` lines 190-290 (`cmdDoctor`) and 470-480 (`plannerCheckInput`)
- `internal/doctor/doctor.go` lines 15-60 (`Severity`, `Check`)

## 1. System Overview

A remote builder runs on the server as the server's OS user. When the repo
has no repo-local `user.name`/`user.email` and that user has no global
identity, the builder's `git commit` fails. The builder then improvises an
identity (seen: `relay <relay@localhost>`), and that identity comes back in
`relay/<name>`.

After this change:
- `relay add --server` resolves the client's effective git identity for the
  repo (`git config --get user.name` / `user.email` in the repo, which
  includes global and system config). If either is missing, `add` refuses
  before anything is created on the server.
- The identity rides in `CreateBindingRequest.Author`, and the server stores
  it on `store.ServeFacts`.
- Every builder process the server starts for that binding (admit, switch,
  repair, relaunch: all go through `startRound`) gets `GIT_AUTHOR_NAME`,
  `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME` and `GIT_COMMITTER_EMAIL` in its
  environment.
- `relay doctor`, run inside a git repo on a machine with remote servers
  configured, shows a `git identity` row. It warns when either value is
  missing.

Out of scope, so leave these untouched:
- relay's own server-side commits (`CommitTree` for the uncommitted-work
  ref, `CommitAll` in pause). They keep the `relay <relay@localhost>`
  identity.
- Feature-token gating. An old server ignores the unknown JSON field (it
  decodes leniently), which is today's behaviour. Do not add a `WhoAmI`
  call to `addRemote`, because `TestAddRemoteNoTierSkipsProbe` pins that
  there is none without `--tier`.
- Local (non-remote) builders. They inherit the user's own git config.

## 2. File Structure

```
internal/git/client.go            MODIFY  new method Identity
internal/git/client_test.go       MODIFY  (or the file holding RepoFacts tests) Identity tests
internal/relay/runtime.go         MODIFY  Git interface gains Identity
internal/relay/fake_test.go       MODIFY  fakeGit implements Identity (configurable; default set)
internal/relay/remote.go          MODIFY  addRemote resolves identity, refuses without it, sends Author
internal/relay/errors.go          MODIFY  (wherever ErrServerPreTier / ErrGitRequired live) new ErrNoGitIdentity
internal/relay/headless.go        MODIFY  startRound sets ProcSpec.Env from builderEnv(b); new pure builderEnv
internal/relay/remote_test.go     MODIFY  two addRemote tests
internal/relay/headless_test.go   MODIFY  builderEnv tests; startRound passes Env
internal/remote/proto.go          MODIFY  GitIdentity type; CreateBindingRequest.Author
internal/store/types.go           MODIFY  ServeFacts.AuthorName / AuthorEmail
internal/serve/bindings.go        MODIFY  validate and store Author
internal/serve/admit_test.go      MODIFY  (beside sendRound) wire tests
internal/doctor/identity.go       CREATE  pure GitIdentityCheck
internal/doctor/identity_test.go  CREATE
cmd/relay/doctor.go               MODIFY  append the row
```

If the relay sentinel errors do not live in `internal/relay/errors.go`, put
`ErrNoGitIdentity` next to `ErrServerPreTier`, wherever that is, and say so.

## 3. Data Structures & Type Definitions

### `remote.GitIdentity` (new, `internal/remote/proto.go`)

| Field | Type | JSON | Constraint |
|-------|------|------|------------|
| `Name`  | string | `name`  | required when the struct is present; 1-256 bytes; no `\n`, `\r`, NUL, `<` or `>` |
| `Email` | string | `email` | same constraints |

### `remote.CreateBindingRequest` (changed)

New field `Author *GitIdentity \`json:"author,omitempty"\``. nil means an
old client that sent none. Comment: "the client's git identity; the server
runs this binding's builders as it (#335)."

### `store.ServeFacts` (changed)

New fields `AuthorName string \`json:"author_name,omitempty"\`` and
`AuthorEmail string \`json:"author_email,omitempty"\``. Both empty means no
identity was carried (an old client, or a binding created before #335).

### `relay.ErrNoGitIdentity` (new sentinel)

`errors.New("no git identity")`. addRemote wraps it (see §4) so
`errors.Is` works.

### `doctor.GitIdentityInput` (new, `internal/doctor/identity.go`)

| Field | Type | Meaning |
|-------|------|---------|
| `HasServers` | bool | at least one remote server is configured |
| `InRepo` | bool | the cwd is inside a git work tree |
| `Name`, `Email` | string | the resolved values; "" when unset |

## 4. Interface Definitions & Component Contracts

### `(*git.Client) Identity(ctx context.Context, dir string) (name, email string, err error)`

- Runs `git config --get user.name` and `git config --get user.email` in
  `dir` through the existing `run` helper, with the output trimmed.
- When a key is unset (`git config --get` exits 1 with empty output), its
  value is "" and it is **not** an error.
- Any other failure (not a repo, git missing, exit code other than 1) is
  returned as err.
- Add `Identity` to the `relay.Git` interface with the same signature and
  doc. `fakeGit` gains fields `identityName`, `identityEmail` and
  `identityErr`. Its constructor (or zero-value handling) must default to
  `"Test User"` / `"test@example.com"`, so that every existing remote-add
  test keeps passing without edits. If `fakeGit` is built as a bare struct
  literal in many places, implement the default inside the method: when
  both fields are "" and a separate `identityUnset bool` is false, return
  the defaults.

### `addRemote` (changed)

After the repo-id resolution (step 6, ~line 156-163) and **before** the
candidate check and anything that touches the server:
- Call `rt.Git.Identity(ctx, opts.Repo)`. An err is returned as
  `fmt.Errorf("git identity for %s: %w", opts.Repo, err)`.
- If name or email is "", return
  `fmt.Errorf("%w for %s: a remote builder commits as you; set git config user.name and git config user.email (in the repo or --global)", ErrNoGitIdentity, opts.Repo)`.
  No `CreateBinding` call, no local binding and no branch may exist
  afterwards.
- Set `createReq.Author = &remote.GitIdentity{Name: name, Email: email}`.

### `builderEnv(b store.Binding) []string` (new, pure, `internal/relay/headless.go`)

- Returns nil unless `b.Serve != nil && b.Serve.AuthorName != "" && b.Serve.AuthorEmail != ""`.
- Otherwise it returns exactly, in this order:
  `GIT_AUTHOR_NAME=<n>`, `GIT_AUTHOR_EMAIL=<e>`, `GIT_COMMITTER_NAME=<n>`, `GIT_COMMITTER_EMAIL=<e>`.
- `startRound` sets `Env: builderEnv(b)` in its `ProcSpec` literal (~line
  142). No other ProcSpec site changes. `proc.ChildEnv` appends the extras
  after the parent environment, so they win over inherited values.

### `handleCreateBinding` (changed)

- After the existing field validation (~line 60-70): if `req.Author != nil`
  and either field violates §3's constraints, respond 400 `CodeInvalid`
  with the message `"author: name and email must be 1-256 bytes with no newline, NUL, < or >"`.
  Validate with a small pure helper `validAuthor(a remote.GitIdentity) bool`
  in the same file.
- In the binding literal (~line 117-137), set `Serve.AuthorName` and
  `Serve.AuthorEmail` from `req.Author` when it is non-nil.

### `doctor.GitIdentityCheck(in GitIdentityInput) (Check, bool)`

- Returns `(_, false)`, meaning no row, unless `in.HasServers && in.InRepo`.
- Both values set: `Check{Name: "git identity", Severity: SevOK, Detail: "<name> <<email>>"}`.
- Either missing: `Check{Name: "git identity", Severity: SevWarn, Detail: "user.name/user.email not set: relay add --server will refuse", Fix: "git config --global user.name '<your name>' && git config --global user.email '<you@example.com>'"}`.
  Detail names only the missing key(s): "user.name not set", "user.email
  not set", or both joined as above.
- `Group` is "" (a global row).

### `cmdDoctor` (changed)

- `HasServers`: use the same server list the existing `serverChecks(relay.ProbeServers(...))`
  call is built from (len > 0). Do not probe the network again.
- `InRepo`, `Name`, `Email`: from `rt.Git.Identity(ctx, cwd)`. An err
  (not a repo) means `InRepo=false`. A nil `rt.Git` also means `InRepo=false`.
- If the check returns ok, add it with `insertGlobalCheck`.

## 5. High-Level Pseudocode

```
client: relay add --server S
    ... existing preconditions, base, repoID ...
    name, email, err = rt.Git.Identity(ctx, opts.Repo)
    if err: fail "git identity for <repo>: err"
    if name == "" or email == "": fail ErrNoGitIdentity (nothing created)
    ... candidate check, tier probe (unchanged) ...
    createReq = {..., Author: {name, email}}
    CreateBinding(S, createReq)            # unchanged from here on

server: POST /v1/bindings
    decode; validate existing fields
    if req.Author != nil and not validAuthor(*req.Author): 400 invalid
    b.Serve = {..., AuthorName, AuthorEmail from req.Author or ""}
    save; 201

server: any spawn of b's builder
    startRound(b): spec.Env = builderEnv(b)   # four GIT_* vars, or nil
```

## 6. Error Handling Strategy

- `ErrNoGitIdentity`: a user error, recoverable by setting config. It is
  returned before any side effect.
- A `git config` failure other than "unset": wrapped and returned from add.
- A malformed author on the wire: 400 `CodeInvalid`. The client surfaces the
  server's message the way it already surfaces other 400s.
- No new logging.

## 7. Ordered Implementation Steps

### Step 1: git identity read

- Deliverable: `Identity` on `*git.Client`, on the `relay.Git` interface,
  and on `fakeGit` (with the defaults in §4).
- Tests, in the file that tests `RepoFacts` (find it with
  `grep -ln "RepoFacts" internal/git/*_test.go`):
  - `TestIdentityReadsConfig`: a temp repo with repo-local `user.name` and
    `user.email` returns both.
  - `TestIdentityUnsetIsEmptyNotError`: a temp repo with `HOME` and
    `XDG_CONFIG_HOME` set to an empty temp dir (`t.Setenv`) and
    `GIT_CONFIG_NOSYSTEM=1` returns `"", "", nil`.
- Depends on: nothing.
- Verify: `go build ./...` and the two tests pass.

### Step 2: wire type and stored facts

- Deliverable: `remote.GitIdentity`, `CreateBindingRequest.Author`,
  `ServeFacts.AuthorName/AuthorEmail`, `validAuthor`, and the handler
  changes in §4.
- Tests in `internal/serve/admit_test.go`. Add a helper
  `sendRoundAs(t, env, kp, clientDir, repoID, headSHA, name, plan string, author *remote.GitIdentity)`,
  which is `sendRound` with the author set on the request. Make `sendRound`
  call it with nil, so existing callers are unchanged.
  - `TestCreateBindingStoresAuthor`: send with
    `{Name: "Ada Lovelace", Email: "ada@example.com"}`, then load the
    owner's binding and assert both ServeFacts fields.
  - `TestCreateBindingRejectsBadAuthor`: a table of `{"", "a@b"}`,
    `{"Ada", ""}`, `{"Ada\nX", "a@b"}` and `{"Ada", "<a@b>"}`. Each gets a
    400 with code `invalid`, and no binding is stored.
- Depends on: nothing (it can run in parallel with step 1 in your
  head, but do it second).
- Verify: the tests pass.

### Step 3: the builder runs as the author

- Deliverable: `builderEnv` and the `startRound` `Env` field.
- Tests:
  - `TestBuilderEnv` (`internal/relay/headless_test.go`): nil Serve gives
    nil; Serve with an empty email gives nil; a full identity gives exactly
    the four strings in order.
  - `TestRoundSpawnCarriesAuthorEnv` (`internal/serve/admit_test.go`):
    `sendRoundAs` with an author, then read `env.runner.specs` under
    `env.runner.mu`. `specs[0].Env` contains all four `GIT_*=` values.
    Also add a sibling assertion that a round sent with nil author has a
    nil or empty `Env`.
- Depends on: step 2.
- Verify: the tests pass. Mutation (report it): make `builderEnv` return
  nil always. `TestRoundSpawnCarriesAuthorEnv` fails. Restore.

### Step 4: add refuses without identity, sends it with

- Deliverable: the `addRemote` change in §4, plus `ErrNoGitIdentity`.
- Tests in `internal/relay/remote_test.go`, modelled on
  `TestAddRemoteTierWiresRequestAndEchoesBinding`:
  - `TestAddRemoteSendsGitAuthor`: fakeGit identity
    `"Ada Lovelace"/"ada@example.com"`, so `fr.createBindingReq.Author`
    equals it.
  - `TestAddRemoteRefusesWithoutGitIdentity`: fakeGit email unset. The
    error satisfies `errors.Is(err, ErrNoGitIdentity)` and mentions
    `git config user.email`. `fr.calls` has no `CreateBinding:` entry, and
    no local binding was saved (`st.Load` returns `store.ErrNotFound`).
- Depends on: steps 1 and 2.
- Verify: the two new tests pass, and **every existing test in
  `internal/relay/remote_test.go` passes unmodified**. If one needs an edit
  beyond the fakeGit default, STOP and report which one and why.

### Step 5: doctor row

- Deliverable: `internal/doctor/identity.go` (`GitIdentityInput`,
  `GitIdentityCheck`) and the `cmdDoctor` wiring.
- Tests in `internal/doctor/identity_test.go`, a table over
  `GitIdentityCheck`:
  - no servers gives no row;
  - not in a repo gives no row;
  - both set gives SevOK with the Detail `Ada Lovelace <ada@example.com>`;
  - email missing gives SevWarn, the Detail names `user.email` and not
    `user.name`, and Fix is non-empty;
  - both missing gives SevWarn and names both.
- Do **not** add a `cmd/relay` test that runs `relay doctor`: it probes
  harness binaries and servers, and CI runners have no harness or network.
- Depends on: step 1.
- Verify: the tests pass.

### Step 6: full check

- `make check` passes.
- `git diff --stat` touches only the files in §2 (report any substitution
  you made for `errors.go` or the git test file).
- Report: each step's verify output, the step 3 mutation result, and the
  diff stat. Commit on the binding's branch with the message
  `feat(remote): a remote builder commits as the client's git identity; add refuses without one (#335)`.
