# Remote builders, plan 1 of 4: the `internal/remote` package and its git primitives (#100)

Spec: `docs/specs/2026-09-19-remote-builders-design.md` (in this tree). This
plan implements spec §2.3 (client id, key format), §2.4 (request signing,
nonce window), §3.1–3.2 (wire types, error codes), §4.1 (`TreeTransport`)
and §4.3's git-bundle mechanics, plus the `internal/git` primitives they
need. It produces no CLI, no server, no store change: plans 2–4 build the
server, the client and the e2e on top of exactly the names in §4 below.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. A halt that surfaces a design error is the
wanted outcome; an improvised workaround is not.

**Scope guard.** Touch only the files listed in §2. Do not add a CLI verb or
flag. Do not add a module dependency: everything here is the Go standard
library (`crypto/ed25519`, `crypto/sha256`, `crypto/x509`, `encoding/pem`,
`encoding/base64`, `encoding/json`, `net/http` header constants only) plus
the existing `internal/git`. Do not touch `internal/relay`, `internal/store`
or `cmd/relay`. Run every command in the foreground; dispatch no sub-agents
and start no background work. Do not widen any exported signature outside
§2's files.

**Git in tests.** Every test that runs git must go through a helper that
sets `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_SYSTEM=/dev/null` and a
fixed author/committer identity, exactly as `runGit` in
`internal/git/client_test.go` does -- copy that helper into the new test
file rather than importing it (it is unexported). A global
`commit.gpgsign` otherwise hangs the suite on this machine.

**Commit the plan with the work.** Copy this plan file (the path relay gave
you) to `docs/plans/2026-09-19-remote-builders-1-remote-package.md` in your
worktree and include it in the first commit.

**Check command.** `make check` (gofmt over the tree, `go vet`, `go mod
tidy` check, `go test ./...`). Run it at the end of every step that says
so; it must be green before the next step starts.

## 1. System overview

`internal/remote` is the vocabulary both sides of the wire share: how a
client is named (a key fingerprint), how a request proves who sent it
(an ed25519 signature over a canonical string), what the JSON bodies look
like, and how a tree moves between two repositories (`TreeTransport`, with
git bundles as its only implementation). Nothing in it knows about
bindings, rounds, herdr, or HTTP routing; the server (plan 2) and the
client (plan 3) import it and stay thin. `internal/git` gains the seven
plumbing calls the bundle transport and plan 2's round close need, in the
same style as the existing `Client` methods: one git invocation each, a
bounded timeout, named sentinel errors.

## 2. File structure

```
internal/remote/
  key.go             Keypair: generate, PEM (PKCS#8) private, one-line public; ClientID from a public key
  key_test.go
  auth.go            Canonical string, Sign, Verify, NonceWindow, the four auth errors
  auth_test.go
  proto.go           Version, header names, JSON request/response types, RoundState, error codes, RepoID
  proto_test.go
  transport.go       TreeTransport interface, Snapshot value, content type, transport errors
  bundle.go          BundleTransport over *git.Client
  bundle_test.go

internal/git/
  client.go          + RootCommit, RefSHA, UpdateRef, CommitTree, BundleCreate, BundleHeads, FetchBundle
  types.go           + ErrNotFastForward, ErrBadBundle, ErrRefMissing
  client_test.go     + one test per new method (§7)

docs/plans/2026-09-19-remote-builders-1-remote-package.md   (this file, committed)
```

## 3. Data structures

### 3.1 `internal/remote/key.go`

```
type ClientID string
    // "SHA256:" + base64.RawStdEncoding(sha256(raw 32-byte ed25519 public key)).
    // Same shape as ssh-keygen -l; 7 + 43 = 50 characters. Never empty.

type Keypair struct {
    Private ed25519.PrivateKey   // 64 bytes
    Public  ed25519.PublicKey    // 32 bytes
}
```

Encodings, all exact:

- Private: PKCS#8 DER inside a PEM block of type `PRIVATE KEY`
  (`x509.MarshalPKCS8PrivateKey` / `x509.ParsePKCS8PrivateKey`). Parsing a
  PEM whose key is not ed25519 is `ErrKeyType`.
- Public, the enrollment line: `ed25519 <base64.StdEncoding(raw 32 bytes)>`
  optionally followed by a space and a free-text comment which is ignored.
  One line, no trailing newline required. Anything else is `ErrKeyFormat`.

### 3.2 `internal/remote/auth.go`

```
const (
    HeaderClient    = "Relay-Client"
    HeaderTimestamp = "Relay-Timestamp"     // unix seconds, decimal
    HeaderNonce     = "Relay-Nonce"         // base64.StdEncoding of 16 random bytes
    HeaderSignature = "Relay-Signature"     // base64.StdEncoding of the 64-byte ed25519 signature
)

const MaxClockSkew = 5 * time.Minute

type KeyStatus int
const (
    KeyUnknown KeyStatus = iota   // not enrolled
    KeyActive
    KeyRevoked
)

type KeyLookup func(id ClientID) (pub ed25519.PublicKey, status KeyStatus)
    // The server's clients.json, abstracted. KeyUnknown -> pub is nil.

type NonceWindow struct { /* unexported: map[nonce]expiry, ttl, mutex */ }
```

Canonical string (the exact bytes that are signed):

```
method + "\n" + target + "\n" + timestamp + "\n" + nonce + "\n" + hex(sha256(body))
```

- `method`: upper-case as sent (`GET`, `POST`).
- `target`: the request path **plus** `"?" + RawQuery` when the query is
  non-empty, exactly as it appears on the request line. The spec §2.4 says
  "path"; this plan pins that to include the query, because
  `?since=<sha>` on the bundle endpoint must be covered by the signature.
  Record this in the doc comment on `Canonical`.
- `timestamp`: the decimal string placed in `Relay-Timestamp`.
- `nonce`: the base64 string placed in `Relay-Nonce`.
- `hex(sha256(body))`: 64 lower-case hex characters; for an empty body it
  is the hash of zero bytes, never omitted.

Errors, each a distinct sentinel and each carrying exactly the wire message
the spec names:

```
ErrUnknownClient   "unknown client"
ErrRevoked         "revoked"
ErrBadSignature    "bad signature"
ErrStale           "stale or replayed"
```

### 3.3 `internal/remote/proto.go`

```
const Version = 1
const ContentTypeGitBundle = "application/x-git-bundle"

type RoundState string
const (
    RoundIdle     RoundState = "idle"
    RoundRunning  RoundState = "running"
    RoundClosed   RoundState = "closed"
    RoundNeedsYou RoundState = "needs_you"
)

type WhoAmI struct {
    ID            ClientID `json:"id"`
    Label         string   `json:"label"`
    ServerVersion int      `json:"server_version"`
    Transports    []string `json:"transports"`      // ["git-bundle"]
}

type CreateBindingRequest struct {
    Name           string `json:"name"`             // required; store name rules apply on the server
    RepoID         string `json:"repo_id"`          // required; RepoID()
    BaseCommit     string `json:"base_commit"`      // required; 40 hex
    Candidate      string `json:"candidate,omitempty"`
    RoundCap       int    `json:"round_cap,omitempty"`
    RoundTimeoutMS int    `json:"round_timeout_ms,omitempty"`
}

type BindingView struct {
    Name           string     `json:"name"`
    State          string     `json:"state"`            // the store's State string
    Round          int        `json:"round"`
    RoundState     RoundState `json:"round_state"`
    Halt           string     `json:"halt,omitempty"`
    ResultCommit   string     `json:"result_commit,omitempty"`
    DirtyCommit    string     `json:"dirty_commit,omitempty"`
    ReportOutcome  string     `json:"report_outcome,omitempty"`   // relay.ReportTail.Status or "unstructured"
    AckedRound     int        `json:"acked_round"`
    Candidate      string     `json:"candidate,omitempty"`
    RoundStartedAt time.Time  `json:"round_started_at,omitempty"`
    RoundCap       int        `json:"round_cap"`
    RoundTimeoutMS int        `json:"round_timeout_ms"`
}

type UnavailableRequest struct {
    Token  string `json:"token"`
    Reason string `json:"reason"`
}

type Code string
const (
    CodeNotEnrolled    Code = "not_enrolled"
    CodeRevoked        Code = "revoked"
    CodeBadSignature   Code = "bad_signature"
    CodeStale          Code = "stale"
    CodeNotFound       Code = "not_found"
    CodeRoundOpen      Code = "round_open"
    CodeRoundStarted   Code = "round_started"
    CodeNotFastForward Code = "not_fast_forward"
    CodeNoRunner       Code = "no_runner"
    CodeSpawnFailed    Code = "spawn_failed"
    CodeTooLarge       Code = "too_large"
    CodeVersion        Code = "version"
)

type ErrorBody struct {
    Error   Code   `json:"error"`
    Message string `json:"message"`
}
```

`ErrorBody` implements `error` (returns `Message`) so a client can wrap it.
`CodeOf(err error) Code` maps the four auth sentinels to their codes
(`ErrUnknownClient -> not_enrolled`, `ErrRevoked -> revoked`,
`ErrBadSignature -> bad_signature`, `ErrStale -> stale`) and anything else
to `""`.

`RepoID(rootCommit string) string` = lower-case hex of `sha256(rootCommit)`
where `rootCommit` is the 40-hex sha as a string (the ASCII bytes, not the
binary sha). Empty input is an error `ErrNoRoot`.

### 3.4 `internal/remote/transport.go`

```
type Snapshot struct {
    ContentType string
    Body        io.ReadCloser        // nil when Empty; caller closes otherwise
    Heads       map[string]string    // ref -> sha the snapshot carries, always filled
    Empty       bool                 // nothing newer than `since`; Body is nil
}

type TreeTransport interface {
    // Snapshot packages every ref in refs, as it stands in repo, relative to
    // since. since "" means "everything reachable". since must be a commit
    // that is an ancestor of every ref; otherwise ErrSinceUnknown.
    Snapshot(ctx context.Context, repo string, refs []string, since string) (Snapshot, error)

    // Absorb applies a body of contentType to repo. Every ref the body
    // carries must be in refs (else ErrUnexpectedRef); every ref in refs the
    // body carries is fast-forwarded (else git.ErrNotFastForward); a ref in
    // refs the body does not carry is left as it is. Returns ref -> new sha
    // for the refs it moved. A zero-length body is a no-op returning an
    // empty map.
    Absorb(ctx context.Context, repo, contentType string, body io.Reader, refs []string) (map[string]string, error)
}

ErrUnsupportedType   contentType is not one this transport speaks
ErrUnexpectedRef     the body carries a ref outside refs
ErrSinceUnknown      since is not a commit in repo, or not an ancestor of a ref
```

`refs` are full ref names (`refs/heads/relay/api`,
`refs/relay/api/round-3`), never short names.

### 3.5 `internal/git` additions (`types.go`)

```
ErrNotFastForward   "ref update is not a fast-forward"
ErrBadBundle        "bundle is malformed or its prerequisites are missing"
ErrRefMissing       "ref does not exist"
```

## 4. Interfaces

### 4.1 `internal/git/client.go` -- seven new methods on `*Client`

Each is one git invocation through the existing `run`, bounded by the
client's timeout, returning `ErrNotRepo`/`ErrGitUnavailable` exactly as
the existing methods do. Doc comments follow the existing
Preconditions/Postconditions/Errors shape.

```
RootCommit(ctx, dir string) (string, error)
    git rev-list --max-parents=0 HEAD
    Post: the single root sha. A repo with several roots (a grafted history)
          returns the lexicographically smallest, so the id is deterministic;
          document this. Empty repo (no HEAD) -> ErrRefMissing.

RefSHA(ctx, dir, ref string) (sha string, ok bool, err error)
    git rev-parse --verify --quiet <ref>^{commit}
    Post: (sha, true, nil) when ref resolves; ("", false, nil) when it does
          not (exit 1 with empty output is "missing", not an error).

UpdateRef(ctx, dir, ref, newSHA, oldSHA string) error
    git update-ref <ref> <newSHA> [<oldSHA>]
    oldSHA "" means "create or overwrite"; oldSHA given means CAS, and a
    mismatch is a wrapped git error (not a sentinel: callers here never race).

CommitTree(ctx, dir, tree, parent, message string) (sha string, err error)
    git commit-tree <tree> [-p <parent>] -m <message>
    with env GIT_AUTHOR_NAME=relay GIT_AUTHOR_EMAIL=relay@localhost
             GIT_COMMITTER_NAME=relay GIT_COMMITTER_EMAIL=relay@localhost
    parent "" omits -p. Post: the commit exists, unreferenced (caller
    UpdateRefs it). The env is passed through run's env parameter, which
    appends to os.Environ -- so the caller's identity is overridden, and a
    global commit.gpgsign does NOT apply because commit-tree never signs
    unless -S is given. Document that.

BundleCreate(ctx, dir, path string, refs []string, since string) (heads map[string]string, empty bool, err error)
    Pre:  every ref resolves (else ErrRefMissing); since is "" or a commit sha.
    1. heads := RefSHA of every ref.
    2. If since != "" and every head == since -> return (heads, true, nil)
       without invoking bundle (git refuses an empty bundle).
    3. If since != "": for every ref, `git merge-base --is-ancestor <since> <ref>`;
       a non-ancestor is ErrRefMissing wrapped with "since is not an ancestor
       of <ref>" (the transport maps this to ErrSinceUnknown).
    4. git bundle create <path> [<since>..]<ref>... -- one argument per ref;
       with since, each argument is "<since>..<ref>" AND a final bare
       "<ref>" is NOT added (the range form already records the ref name in
       the bundle header). Verify this against `git bundle list-heads` in the
       test: the bundle must list every ref by its full name.
    Post: file at path is a complete bundle; (heads, false, nil).

BundleHeads(ctx, dir, path string) (map[string]string, error)
    git bundle list-heads <path>
    Output lines are "<sha> <ref>". A malformed file is ErrBadBundle.

FetchBundle(ctx, dir, path string, refs []string) (map[string]string, error)
    1. git bundle verify <path>   -- non-zero -> ErrBadBundle wrapped with stderr
    2. heads := BundleHeads
    3. for every ref in refs that heads carries:
          git fetch --no-tags <path> <ref>:<ref>
          stderr containing "non-fast-forward" or "[rejected]" -> ErrNotFastForward
          (no leading "+" on the refspec, so git enforces the fast-forward)
       Do one fetch per ref, in refs order, stopping at the first error; refs
       already moved stay moved (document: callers treat a partial absorb as
       retryable, and a re-run is idempotent because a ref at its target sha
       is a no-op fetch).
    Post: map of ref -> sha for the refs fetched.
```

`internal/relay`'s `Git` interface is **not** widened in this plan; plan 2
adds what the server needs there.

### 4.2 `internal/remote/key.go`

```
Generate() (Keypair, error)                            crypto/rand
IDOf(pub ed25519.PublicKey) ClientID
MarshalPrivate(k Keypair) ([]byte, error)              PEM bytes
ParsePrivate(pem []byte) (Keypair, error)              ErrKeyFormat, ErrKeyType
MarshalPublic(pub ed25519.PublicKey, comment string) string    "ed25519 <b64>[ <comment>]"
ParsePublic(line string) (ed25519.PublicKey, error)   ErrKeyFormat; a wrong-length key is ErrKeyFormat
```

Pre/post: `ParsePrivate(MarshalPrivate(k)) == k` byte for byte;
`ParsePublic(MarshalPublic(p, c)) == p` for any comment including ones with
spaces; `IDOf` of a parsed public key equals `IDOf` of the original.

### 4.3 `internal/remote/auth.go`

```
Canonical(method, target, timestamp, nonce string, bodySHA256 []byte) []byte
    The bytes of §3.2's canonical string. Pure.

NewNonce() (string, error)
    16 bytes from crypto/rand, base64.StdEncoding.

Sign(k Keypair, method, target string, bodySHA256 []byte, now time.Time, nonce string) http.Header
    Returns exactly the four headers set. timestamp = strconv of now.Unix().
    Pure given nonce; the caller passes NewNonce().

NewNonceWindow(ttl time.Duration) *NonceWindow
(*NonceWindow).Seen(nonce string, now time.Time) bool
    Records nonce with expiry now+ttl and returns whether it was already
    present and unexpired. Prunes expired entries on every call (the set is
    small: one entry per request in the window). Safe for concurrent use.

Verify(h http.Header, method, target string, bodySHA256 []byte, now time.Time, lookup KeyLookup, nonces *NonceWindow) (ClientID, error)
    Order is fixed and each failure returns without evaluating later checks:
    1. id := h.Get(HeaderClient); "" -> ErrUnknownClient
    2. pub, status := lookup(id)
       KeyUnknown -> ErrUnknownClient; KeyRevoked -> ErrRevoked
    3. sig := base64 decode HeaderSignature; decode failure or len != 64 -> ErrBadSignature
    4. ed25519.Verify(pub, Canonical(method, target, h.Get(HeaderTimestamp), h.Get(HeaderNonce), bodySHA256), sig)
       false -> ErrBadSignature
    5. ts := parse HeaderTimestamp as int64; parse failure -> ErrStale;
       |now.Unix() - ts| > MaxClockSkew seconds -> ErrStale
    6. nonces.Seen(h.Get(HeaderNonce), now) -> ErrStale
       (Seen is called only here, after the signature verified, so an
       attacker cannot burn nonces with unsigned requests.)
    7. return id, nil
```

Note step 4 verifies the signature over whatever timestamp/nonce strings
arrived, and only then checks their values -- a tampered timestamp fails
as `bad signature`, not `stale`. Pin this in a test.

### 4.4 `internal/remote/bundle.go`

```
type BundleTransport struct { git *git.Client; tmp string }
NewBundleTransport(g *git.Client, tmpDir string) *BundleTransport
    tmpDir "" means os.TempDir(). Every bundle file this type writes is
    created with os.CreateTemp(tmpDir, "relay-bundle-*.bundle") and removed
    when the Snapshot body is closed or Absorb returns.

Snapshot(ctx, repo, refs, since) (Snapshot, error)
    heads, empty, err := git.BundleCreate(ctx, repo, tmpfile, refs, since)
    ErrRefMissing wrapping "since is not an ancestor" -> ErrSinceUnknown
    empty -> Snapshot{ContentType: ContentTypeGitBundle, Heads: heads, Empty: true}
    else  -> Body is the opened tmpfile wrapped so Close() also removes it.

Absorb(ctx, repo, contentType, body, refs) (map[string]string, error)
    contentType != ContentTypeGitBundle -> ErrUnsupportedType
    copy body to tmpfile; 0 bytes copied -> return empty map, nil
    heads := git.BundleHeads(tmpfile)
    any ref in heads not in refs -> ErrUnexpectedRef (name the ref)
    return git.FetchBundle(ctx, repo, tmpfile, refs)
```

`BundleTransport` satisfies `TreeTransport`; assert it with a compile-time
`var _ TreeTransport = (*BundleTransport)(nil)`.

## 5. High-level pseudocode

The two flows plans 2 and 3 will run, written here so the primitives are
proven against them in this plan's tests:

```
OUTBOUND (client -> server), round N:
    snap := transport.Snapshot(clientRepo, ["refs/heads/relay/api"], lastShipped)
    if snap.Empty: send no body (Content-Length 0)
    else: stream snap.Body as the multipart "bundle" part; close it after
    server: transport.Absorb(bareRepo, ContentTypeGitBundle, part, ["refs/heads/relay/api"])
        ErrNotFastForward -> 422 not_fast_forward
        ErrBadBundle / ErrUnexpectedRef -> 422 not_fast_forward (same client hint)
    lastShipped = snap.Heads["refs/heads/relay/api"]

INBOUND (server -> client), round N closed:
    refs := ["refs/heads/relay/api"] + ["refs/relay/api/round-N" if dirty]
    snap := transport.Snapshot(bareRepo, refs, lastKnown)
    client: transport.Absorb(clientRepo, ..., refs)
    lastKnown = snap.Heads["refs/heads/relay/api"]

SIDE REF at close (server, plan 2 -- the primitives are here):
    tree := git.SnapshotTree(worktree)               (existing)
    head, _, _ := git.RefSHA(bare, "refs/heads/relay/api")
    sha := git.CommitTree(bare, tree, head, "[relay] api: round N, uncommitted work")
    git.UpdateRef(bare, "refs/relay/api/round-N", sha, "")
```

Note: `SnapshotTree` runs in the worktree, and the worktree's object
database is the bare repo's (a linked worktree shares it), so `CommitTree`
in `bare` can see the tree object. The test in §7 must prove that with a
real `git worktree add` from a bare repo.

## 6. Error handling

- Auth errors are the only errors that carry wire text; `CodeOf` is the
  single mapping to `Code`. Nothing else in this package formats a message
  for the wire -- plan 2's handlers do.
- Transport errors are sentinels wrapped with context (`%w`); callers use
  `errors.Is`. `git.ErrNotFastForward` passes through `Absorb` unwrapped in
  identity (still `errors.Is`-able).
- No error in this package is logged; the package has no logger.
- Temp files: every path that creates one removes it on every return path,
  including error returns -- a test must leave `tmpDir` empty.

## 7. Ordered implementation steps

Each step: write the test named, run it and see it fail for the stated
reason, implement, run it green, run `make check`, commit with the message
given. Commits are small and in this order.

**Step 1 -- plan file and `internal/git` sentinels.**
Copy this plan to `docs/plans/2026-09-19-remote-builders-1-remote-package.md`.
Add `ErrNotFastForward`, `ErrBadBundle`, `ErrRefMissing` to
`internal/git/types.go`. Verify: `go build ./...`. Commit:
`docs(plans): remote builders plan 1; git: transport sentinels (#100)`.

**Step 2 -- `RootCommit`, `RefSHA`, `UpdateRef`.**
Tests in `internal/git/client_test.go`: `TestRootCommit` (two commits ->
the first's sha; an empty `git init` -> `ErrRefMissing`);
`TestRefSHAMissingIsOkFalse` (`refs/heads/nope` -> `"", false, nil`);
`TestUpdateRefCreatesAndCAS` (create with old "", then CAS with the wrong
old sha errors and the ref is unchanged). Commit: `git: RootCommit, RefSHA,
UpdateRef (#100)`.

**Step 3 -- `CommitTree`.**
Test `TestCommitTreeFromLinkedWorktree`: `git init --bare` a repo; seed it
by cloning, committing one file and pushing `refs/heads/relay/api`; `git
worktree add` from the bare repo (git allows worktrees on a bare repo) at
that branch; write an untracked file in the worktree; `SnapshotTree` on
the worktree; `CommitTree(bare, tree, head, msg)`; `UpdateRef(bare,
"refs/relay/api/round-1", sha, "")`; assert `git cat-file -p <sha>` in the
bare repo shows the tree and the parent, the author is `relay
<relay@localhost>`, and `refs/heads/relay/api` still equals `head`.
Mutation target: drop the `GIT_AUTHOR_*` env and the author assertion
fails. Commit: `git: CommitTree with relay identity (#100)`.

**Step 4 -- `BundleCreate`, `BundleHeads`.**
Tests: `TestBundleCreateFullThenIncremental` (full bundle of one ref lists
that ref with its sha; add a commit; bundle `since=<first sha>` lists the
ref at the new sha; the incremental file is smaller than the full one);
`TestBundleCreateEmptyWhenNothingNew` (since == head -> `empty == true`,
no file created); `TestBundleCreateSinceNotAncestor` (a since sha from an
unrelated branch -> `ErrRefMissing`); `TestBundleCreateTwoRefs` (branch +
`refs/relay/api/round-1` -> `list-heads` shows both full names).
Commit: `git: BundleCreate, BundleHeads (#100)`.

**Step 5 -- `FetchBundle`.**
Tests: `TestFetchBundleFastForwards` (bundle from repo A absorbed into bare
B moves `refs/heads/relay/api`; a second incremental bundle moves it
again; the map has the new sha); `TestFetchBundleRefusesNonFastForward`
(B's ref is moved ahead by a local commit; the client's bundle ->
`ErrNotFastForward`, B's ref unchanged); `TestFetchBundleMissingPrereq`
(an incremental bundle whose `since` B never received -> `ErrBadBundle`);
`TestFetchBundleIgnoresRefsNotAsked` (bundle carries two refs, `refs` names
one -> only that one moves). Mutation target: add `+` to the refspec and
`TestFetchBundleRefusesNonFastForward` fails. Commit: `git: FetchBundle,
fast-forward only (#100)`.

**Step 6 -- `internal/remote/key.go`.**
Tests `TestKeyRoundTrip` (private PEM and public line round-trip; ID
stable), `TestParsePublicRejects` (`rsa AAAA`, wrong-length base64, empty
-> `ErrKeyFormat`), `TestParsePrivateRejectsRSA` (a PKCS#8 RSA key ->
`ErrKeyType`; generate it with `rsa.GenerateKey` in the test),
`TestIDShape` (starts with `SHA256:`, length 50, no `=`). Commit:
`remote: ed25519 keypair, client id (#100)`.

**Step 7 -- `internal/remote/auth.go`.**
Tests, one per §4.3 branch, each named for the failure it pins:
`TestSignVerifyRoundTrip`; `TestVerifyUnknownClient`; `TestVerifyRevoked`;
`TestVerifyBodyTamper` (flip one byte of the body hash -> `ErrBadSignature`);
`TestVerifyTimestampTamperIsBadSignature` (change the header after signing ->
`ErrBadSignature`, not `ErrStale`); `TestVerifyStaleClock` (signed with
now-6m -> `ErrStale`; now-4m59s -> ok); `TestVerifyReplay` (same headers
twice -> second is `ErrStale`); `TestVerifyReplayNotBurnedByBadSig` (a
request with a bad signature does not consume its nonce: the same nonce
signed correctly afterwards verifies); `TestNonceWindowPrunes` (after ttl a
nonce is unseen again); `TestCanonicalIncludesQuery` (two targets differing
only in `?since=` produce different bytes). Commit: `remote: signed
requests, nonce window (#100)`.

**Step 8 -- `internal/remote/proto.go`.**
Tests `TestErrorBodyJSON` (marshals to exactly `{"error":"stale","message":"..."}`
-- field order and names), `TestCodeOf` (four sentinels map; `errors.New("x")`
-> `""`), `TestRepoID` (a known sha -> the sha256 hex you compute in the test
with `crypto/sha256` over the ASCII bytes; empty -> `ErrNoRoot`),
`TestBindingViewJSONNames` (marshal a filled value and assert the keys in
§3.3 are present verbatim, `dirty_commit` absent when empty). Commit:
`remote: wire types, error codes, RepoID (#100)`.

**Step 9 -- `transport.go` and `bundle.go`.**
Tests in `bundle_test.go` using real git:
`TestBundleTransportOutboundThenInbound` (the §5 flows end to end between
a client repo and a bare server repo, two rounds, with a dirty side ref on
the second inbound; assert the client's `refs/relay/api/round-2` exists and
its parent is the client's `refs/heads/relay/api`);
`TestBundleTransportEmptySnapshot` (`Empty` true, `Body` nil, `Absorb` of a
zero-length reader returns an empty map);
`TestBundleTransportUnexpectedRef` (a bundle carrying `refs/heads/main` when
`refs` names only `relay/api` -> `ErrUnexpectedRef` and nothing moved);
`TestBundleTransportSinceUnknown`; `TestBundleTransportUnsupportedType`;
`TestBundleTransportLeavesNoTempFiles` (after every call above, `tmpDir`
is empty -- run with `NewBundleTransport(g, t.TempDir())`).
Commit: `remote: TreeTransport, git-bundle implementation (#100)`.

**Step 10 -- final check and report.**
`make check` green. Report: list every new exported name against §3/§4 and
say `matches` or name the deviation; list the mutation targets you ran
(steps 3 and 5) and the test each one broke. Write the report, create the
done marker.

## Verification the planner runs

`make check`; `git diff --stat` must touch only §2's files; mutation
re-runs of steps 3, 5 and 7's `TestVerifyReplayNotBurnedByBadSig` (move
`Seen` before the signature check and it must fail).
