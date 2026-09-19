# Remote builders, plan 2a of 4: the server core -- owners, planner-less rounds, handlers (#100)

Spec: `docs/specs/2026-09-19-remote-builders-design.md` (in this tree),
§2.1–2.2, §2.5, §3, §4.3, §6.1–6.3. Plan 1 (merged, #206) supplied
`internal/remote` and the git primitives; this plan builds the server on
them **without TLS, enrollment CLI, or `relay serve` verbs** -- those are
plan 2b. Everything here is testable with `httptest` and real git in a
temp dir. Plan 3 is the client; plan 4 the e2e.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. A halt that surfaces a design error is the
wanted outcome; an improvised workaround is not.

**Scope guard.** Touch only the files in §2. No CLI verb or flag, no module
dependency (standard library + existing internal packages). Do not run
`make e2e`. Do not widen any exported signature outside §2's files, except
the `relay.Git` interface, which §4.1 names. If a test outside §2 stops
compiling, halt and say which. Foreground only; no sub-agents.

**Git in tests.** Every git-running test uses a helper that sets
`GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_SYSTEM=/dev/null` and a fixed
identity, as `runGit` in `internal/git/client_test.go` and
`internal/remote/bundle_test.go` do. Copy it; do not import it.

**Commit the plan with the work.** Copy this plan file (the path relay gave
you) to `docs/plans/2026-09-19-remote-builders-2a-serve-core.md` in your
worktree and include it in the first commit.

**Check command.** `make check`; green before every commit.

## 1. System overview

The server is a set of per-owner `store.Store`s, one `relay.Runtime` per
owner that differs from the local one only in its `Store` and its `Herdr`
(a stub: no panes, no planner, notifications go to the log), a tick loop
that runs the existing `Daemon.Tick` for every owner, and an HTTP handler
that turns the spec's eleven routes into calls on the existing verbs
(`relay.Send`, `relay.Done`, `relay.Unbind`, `relay.Unavailable`) plus
three things that are new: absorbing the client's bundle into a bare repo
and fast-forwarding the worktree, closing a round with the side ref and
recording the result commit, and serving round files and bundles back.

Two seams in `internal/relay` make the existing round machinery run
without a planner: a `Binding.Owner` that, when set, makes
`deliverAndSettle` leave queued payloads queued (the client collects them
over the wire), and a hook in `reconcileHeadless` that, on a closed round
of an owned binding, records the result commit and writes the side ref.
Nothing else in reconcile, send, close, halt, switch, or the ledger
changes.

Layout on disk (server state root `<state>/serve`):

```
<state>/serve/
  bindings/<owner-id>/                a store.Store root per owner
    <name>/binding.json, NNN-plan.md, NNN-report.md, NNN-diff.patch, NNN-builder.log, NNN-builder.stream
    .worktrees/<name>/                the binding's worktree = that store's WorktreePath(name)
    .lock, ledger.json (unused: see §3.3), history.json (unused)
  repos/<owner-id>/<repo-id>.git      bare repo per (owner, repo)
  ledger.json                         server-wide availability ledger (one for all owners)
  history.json                        server-wide availability history
  clients.json                        enrolled keys (plan 2b writes it; this plan reads it)
```

The worktree lives at the owner store's `WorktreePath(name)` so that
`relay.Done` (release), `relay.Unbind` (teardown) and `relay.GC` work
unchanged on a server binding.

### Refs and the outbound name

git refuses to fetch into a branch that a worktree has checked out. So:

- **Outbound** (client -> server) travels as `refs/relay/<name>/out`. The
  client (plan 3) points that ref at its `refs/heads/relay/<name>` before
  bundling. The server absorbs it into the bare repo (never checked out
  there) and then fast-forwards the worktree with `merge --ff-only`.
- **Inbound** (server -> client) travels as `refs/heads/relay/<name>` plus,
  when the round left the tree dirty, `refs/relay/<name>/round-<N>`. The
  client never has `relay/<name>` checked out (spec §4.3), so a plain
  fast-forward fetch works there.

## 2. File structure

```
internal/store/
  types.go             + Binding.Owner, Binding.Serve *ServeFacts, ServeFacts

internal/git/
  client.go            + MergeFF, InitBare
  types.go             + ErrMergeConflict
  client_test.go       + TestMergeFF, TestInitBare

internal/relay/
  herdr.go             Git interface + RefSHA, UpdateRef, CommitTree, MergeFF, RootCommit
  reconcile.go         deliverAndSettle: owned-binding guard
  headless.go          reconcileHeadless: closed-round hook for owned bindings
  served.go            closeServedRound, RoundStateOf, ServedView
  served_test.go       guard, hook, RoundStateOf, closeServedRound against real git
  fake_test.go         fakeGit gains the five methods (recording, no-op)

internal/serve/
  serve.go             Server, Config, New, Handler; per-owner runtimes; the mutex
  clients.go           Clients file: Load, Lookup (KeyLookup), Add, Revoke, List
  auth.go              signed-request middleware -> ClientID in context
  herdr.go             stubHerdr
  routes.go            route table and JSON/error helpers
  bindings.go          create, list, get, done, unbind, resume, unavailable
  rounds.go            start round (absorb, worktree, ff, Send), files, bundle, ack
  daemon.go            Tick over owners; Run loop
  serve_test.go        httptest suite (§7)

docs/plans/2026-09-19-remote-builders-2a-serve-core.md
```

## 3. Data structures

### 3.1 `internal/store/types.go`

```
// Owner is the enrolled client id that created this binding on a relay
// server (remote-builders spec §2.1). Empty on every local binding. When
// set, the binding has no planner: deliverAndSettle leaves payloads queued
// and the owner collects them over the wire.
Owner string `json:"owner,omitempty"`

// Serve is what the server records about the binding beyond the local
// fields; nil on local bindings.
Serve *ServeFacts `json:"serve,omitempty"`

type ServeFacts struct {
    RepoID       string    `json:"repo_id"`                   // remote.RepoID of the client's repo
    BareRepo     string    `json:"bare_repo"`                 // absolute path of the bare repo
    ClosedRound  int       `json:"closed_round,omitempty"`    // last round closed by the daemon; 0 none
    ResultCommit string    `json:"result_commit,omitempty"`   // refs/heads/relay/<name> at that close
    DirtyCommit  string    `json:"dirty_commit,omitempty"`    // refs/relay/<name>/round-<ClosedRound>, "" if clean
    AckedRound   int       `json:"acked_round,omitempty"`     // last round the owner acked; 0 none
    LastSeen     time.Time `json:"last_seen,omitempty"`       // last signed request from the owner about this binding
}
```

Invariant: `AckedRound <= ClosedRound <= Round` (`Round` is the store's
"next round to send" counter after close, so `ClosedRound == Round-1` right
after a close).

### 3.2 `internal/serve/clients.go`

```
type Client struct {
    ID        remote.ClientID `json:"id"`
    Label     string          `json:"label"`
    PubKey    string          `json:"pubkey"`       // the enrollment line, remote.MarshalPublic form
    EnrolledAt time.Time      `json:"enrolled_at"`
    RevokedAt  time.Time      `json:"revoked_at,omitempty"`
}

type Clients struct {
    path string
    mu   sync.Mutex
    list []Client
}
```

File is a JSON array. `Load(path)` on a missing file is an empty set, not
an error (a fresh server has no clients yet).

### 3.3 `internal/serve/serve.go`

```
type Config struct {
    Root       string            // <state>/serve
    Candidates *candidate.Set
    Policy     policy.Policy
    Runner     relay.Runner
    Git        *git.Client       // concrete: the transport needs it too
    Now        func() time.Time
    Interval   time.Duration     // daemon tick, floored by relay.NewDaemon
    MaxBundleBytes int64         // default 512 << 20
}

type Server struct {
    cfg      Config
    clients  *Clients
    nonces   *remote.NonceWindow           // ttl = remote.MaxClockSkew
    transport remote.TreeTransport         // remote.NewBundleTransport(cfg.Git, filepath.Join(cfg.Root, "tmp"))
    mu       sync.Mutex                    // §6.3 of the spec: every store/ledger mutation and every tick
}
```

Per-owner runtime, built on demand and never cached (a `store.Store` is a
root path and a mutex; cheap):

```
func (s *Server) runtime(owner remote.ClientID) relay.Runtime
    st := store.New(filepath.Join(s.cfg.Root, "bindings", string(owner)))
    return relay.Runtime{
        Herdr: stubHerdr{}, Git: s.cfg.Git, Runner: s.cfg.Runner, Store: st,
        Candidates: s.cfg.Candidates, Policy: s.cfg.Policy,
        LedgerPath:  filepath.Join(s.cfg.Root, "ledger.json"),     // server-wide, not st.LedgerPath()
        HistoryPath: filepath.Join(s.cfg.Root, "history.json"),
        Now: s.cfg.Now,
        // Usage, Prices, Hooks: zero -- unknown usage, no hooks (plan 2b may wire usage)
    }
```

### 3.4 Round state (spec §3.1), derived, never stored

```
func RoundStateOf(b store.Binding, entries []store.LogEntry) remote.RoundState
    b.State == store.StateNeedsYou                                  -> RoundNeedsYou
    HasEntry(entries, b.Round, DirToBuilder, KindPlan) &&
      !HasEntry(entries, b.Round, DirToPlanner, KindReport)         -> RoundRunning
    b.Serve != nil && b.Serve.ClosedRound > b.Serve.AckedRound      -> RoundClosed
    otherwise                                                       -> RoundIdle
```

Lives in `internal/relay/served.go` (it needs `HasEntry` and the store
kinds), exported so `serve` can call it.

## 4. Interfaces

### 4.1 `internal/git` additions

```
MergeFF(ctx, dir, ref string) error
    git merge --ff-only <ref>         (run in the worktree dir)
    stderr containing "Not possible to fast-forward" or "not possible to fast-forward"
        -> ErrNotFastForward
    stderr containing "would be overwritten" or "local changes"
        -> ErrMergeConflict (new sentinel: "uncommitted changes conflict with the update")
    Post: on success the worktree and its branch are at <ref>.

InitBare(ctx, path string) error
    git init --bare <path>   (run with dir = filepath.Dir(path); create parents first with os.MkdirAll)
    Post: path is a bare repository; idempotent (an existing bare repo is left as is:
          check for path/HEAD before running init).
```

`relay.Git` interface (`internal/relay/herdr.go`) gains **exactly**:

```
RefSHA(ctx, dir, ref string) (string, bool, error)
UpdateRef(ctx, dir, ref, newSHA, oldSHA string) error
CommitTree(ctx, dir, tree, parent, message string) (string, error)
MergeFF(ctx, dir, ref string) error
RootCommit(ctx, dir string) (string, error)
```

`fakeGit` in `internal/relay/fake_test.go` implements them: each records
its arguments on the fake and returns configurable values (`refSHA
map[string]string`, `commitTreeSHA string`, `mergeFFErr error`); defaults
are "ref present at `refSHA[ref]`", "commit sha `fakecommit`", nil errors.

### 4.2 `internal/relay/served.go`

```
// closeServedRound runs once per closed round on an owned binding, inside
// reconcileHeadless after closeOnMarker reports closed and before
// clearProcess. It never fails the close: any git error is a slog.Warn and
// the facts stay at their previous values, so the owner's next GET shows the
// round still running until the next tick retries.
func closeServedRound(ctx context.Context, rt Runtime, b store.Binding) store.Binding
    Pre:  b.Owner != "", b.Serve != nil, b.Round already incremented by queueReport
          (the closed round is b.Round-1), b.Worktree is the worktree, b.Branch its branch.
    1. closed := b.Round - 1
    2. head, ok := rt.Git.RefSHA(ctx, b.Serve.BareRepo, "refs/heads/"+b.Branch)
       !ok -> warn "branch missing at close", return b
    3. dirty, err := rt.Git.Dirty(ctx, b.Worktree); err -> warn, return b
    4. dirtyCommit := ""
       if dirty:
           tree, err := rt.Git.SnapshotTree(ctx, b.Worktree)
           sha, err := rt.Git.CommitTree(ctx, b.Serve.BareRepo, tree, head,
                          fmt.Sprintf("[relay] %s: round %d, uncommitted work", b.Name, closed))
           rt.Git.UpdateRef(ctx, b.Serve.BareRepo, fmt.Sprintf("refs/relay/%s/round-%d", b.Name, closed), sha, "")
           any err -> warn, return b
           dirtyCommit = sha
    5. b.Serve.ClosedRound = closed; b.Serve.ResultCommit = head; b.Serve.DirtyCommit = dirtyCommit
    6. return b

// ServedView is the wire view of an owned binding (spec §3.1).
func ServedView(b store.Binding, entries []store.LogEntry) remote.BindingView
    Name, State (string(b.State)), Round, RoundState: RoundStateOf, Halt: b.Halt if needs_you,
    ResultCommit/DirtyCommit: from b.Serve when RoundState == closed, else "",
    ReportOutcome: the Outcome field of the newest KindReport entry for round ClosedRound ("" if none),
    AckedRound, Candidate: b.BuilderCandidate, RoundStartedAt, RoundCap, RoundTimeoutMS.
```

### 4.3 `internal/relay` seams

`deliverAndSettle` (reconcile.go), first lines:

```
if b.Owner != "" {
    // Owned by a remote client: there is no planner pane. Payloads stay
    // queued; the owner reads them over the wire (remote-builders spec §6.2).
    return b, nil
}
```

`reconcileHeadless` (headless.go), in the `closed` branch:

```
if closed {
    if next.Owner != "" {
        next = closeServedRound(ctx, rt, next)
    }
    next.Builder = clearProcess(next.Builder)
    return deliverAndSettle(ctx, rt, tx, next, agents)
}
```

Nothing else in `internal/relay` changes. `haltBinding` keeps calling
`rt.Herdr.Notify`; the server's stub logs it.

### 4.4 `internal/serve/herdr.go`

```
type stubHerdr struct{}
ListAgents -> nil, nil
Notify(ctx, message) -> slog.Info("notify", "message", message); nil
every other method -> errNoHerdr ("relay serve has no herdr")
```

### 4.5 `internal/serve/clients.go`

```
LoadClients(path string) (*Clients, error)
(*Clients).Lookup(id remote.ClientID) (ed25519.PublicKey, remote.KeyStatus)   // satisfies remote.KeyLookup
(*Clients).Add(label, pubLine string, now time.Time) (Client, error)           // ErrAlreadyEnrolled if id present and not revoked; re-enrolling a revoked id clears RevokedAt
(*Clients).Revoke(id remote.ClientID, now time.Time) error                     // ErrNoSuchClient
(*Clients).List() []Client
(*Clients).LabelOf(id remote.ClientID) string                                  // label, or the id's first 8 chars after "SHA256:"
save() writes the whole array with 0600 via write-to-temp-and-rename.
```

### 4.6 `internal/serve/auth.go`

```
func (s *Server) authenticate(next http.Handler) http.Handler
    1. Read the body fully into a temp file under cfg.Root/tmp (bundles are big),
       up to cfg.MaxBundleBytes+1; over the cap -> 413 {too_large, "request body exceeds N bytes"}.
       Hash it while copying (sha256). Replace r.Body with the reopened temp file;
       remove the file when the handler returns.
    2. id, err := remote.Verify(r.Header, r.Method, target(r), sum, s.cfg.Now(), s.clients.Lookup, s.nonces)
       target(r) = r.URL.EscapedPath() + ("?"+r.URL.RawQuery if non-empty)  -- the same rule as remote.Canonical's doc
       err -> 401 {remote.CodeOf(err), err.Error()}
    3. ctx = context.WithValue(ctx, callerKey, id); next.ServeHTTP
func callerOf(r *http.Request) remote.ClientID
```

### 4.7 `internal/serve/routes.go`

```
func (s *Server) Handler() http.Handler
    mux := http.NewServeMux()   // Go 1.22 patterns: "GET /v1/whoami", "POST /v1/bindings/{name}/rounds", ...
    every route wrapped in s.authenticate
    unknown /v1/... -> 404 {not_found}; any path not under /v1/ -> 426 {version, "this server speaks v1"}
    GET /v1/candidates is plan 2b's (it renders the candidate/policy view); not wired here

writeJSON(w, status, v)
writeErr(w, status, code remote.Code, msg string)   // body remote.ErrorBody
```

### 4.8 `internal/serve/bindings.go` -- every handler takes `s.mu`

```
allowed(caller remote.ClientID, verb string, b store.Binding) bool     // spec §2.5; verb "create" -> true
    (b.Owner == caller). Exported as Allowed for the test and for #203.

POST /v1/bindings
    decode remote.CreateBindingRequest
    store.ValidName(name) fails, repo_id empty, or base_commit not 40 hex
        -> 400 {invalid, "<what is wrong>"}
```

`invalid` is not in the spec's closed set; this plan adds
`CodeInvalid Code = "invalid"` to `internal/remote/proto.go` (that file is
in scope for that one constant) and the report says so, so the planner
amends the spec.

```
POST /v1/bindings   (continued)
    rt := s.runtime(caller)
    if rt.Store.Load(name) succeeds -> 409 {invalid, "binding exists"}
    bare := filepath.Join(cfg.Root, "repos", caller, req.RepoID+".git"); git.InitBare(bare)
    b := store.Binding{
        Name: name, Owner: caller, CWD: rt.Store.WorktreePath(name), Worktree: same,
        Branch: "relay/"+name, Base: req.BaseCommit, Repo: bare,
        Builder: store.Endpoint{Kind: <candidate's harness kind>, Mode: store.ModeHeadless, AgentName: name},
        BuilderCandidate: <token: req.Candidate if given and known in cfg.Candidates, else first ungated in policy order via the same pick the local add uses>,
        Round: 1, State: store.StateActive, RoundCap/RoundTimeoutMS: req values or the store defaults,
        Serve: &store.ServeFacts{RepoID: req.RepoID, BareRepo: bare, LastSeen: now},
    }
    -- The worktree is NOT created here: there is nothing to check out until the first bundle arrives.
    rt.Store.Save(b); 201 ServedView
    Look at relay.Add (internal/relay/add.go) for how the candidate pick and the Endpoint are
    filled for a headless add, and call the same helpers; do not re-implement the pick.

GET /v1/bindings          -> list of ServedView for rt.Store.List() (owner-scoped by construction)
GET /v1/bindings/{name}   -> load; !allowed -> 404; touch Serve.LastSeen; ServedView
POST .../done             -> relay.Done(ctx, rt, name); ErrRoundOpen-shaped error (Done refuses an open round) -> 409 {round_open}; ok -> 200 view
POST .../unbind           -> relay.Unbind(ctx, rt, name, true)  (archive); 200 {}
POST .../resume           -> load; State must be done; git.CheckoutWorktree(bare, b.Worktree, b.Branch); State=active; Save; 200 view
POST .../unavailable      -> decode; relay.Unavailable(rt, token, now+<the same duration the CLI uses>, reason); 200 {}
```

### 4.9 `internal/serve/rounds.go`

```
POST /v1/bindings/{name}/rounds        multipart: "round" (int), "plan" (text), "bundle" (optional, octet-stream)
    load; !allowed -> 404
    entries := rt.Store.ReadLog(name); RoundStateOf == running -> 409 {round_open}
    if req.round != b.Round:
        if req.round == b.Round-1 && sha256(plan) == sha256(file at PlanPath(name, b.Round-1)) -> 200 view   (idempotent resend)
        else -> 409 {round_started, "round N already started with a different plan"}
    outRef := "refs/relay/"+name+"/out"
    if bundle part present and non-empty:
        moved, err := s.transport.Absorb(ctx, b.Serve.BareRepo, remote.ContentTypeGitBundle, part, []string{outRef})   -- OUTSIDE s.mu
        ErrNotFastForward / ErrBadBundle / ErrUnexpectedRef / ErrUnsupportedType -> 422 {not_fast_forward, err.Error()}
    outSHA, ok := git.RefSHA(bare, outRef); !ok -> 422 {not_fast_forward, "no outbound ref; send a bundle first"}
    if worktree missing (os.Stat(b.Worktree) not exist):
        git.UpdateRef(bare, "refs/heads/"+b.Branch, outSHA, "")     // create or move the branch (no worktree holds it)
        git.CheckoutWorktree(bare, b.Worktree, b.Branch)
    else:
        git.MergeFF(b.Worktree, outRef)
        ErrNotFastForward -> 422 {not_fast_forward, "relay/<name> on the server has moved past your copy"}
        ErrMergeConflict  -> 422 {not_fast_forward, "uncommitted work in the server worktree conflicts with your update"}
    write plan to a temp file; res, err := relay.Send(ctx, rt, name, tmpPlan)      -- under s.mu
        ErrRunnerUnavailable -> 503 {no_runner}; ErrBuilderBusy -> 409 {round_open};
        spawn failure (Send returned an error after putting the binding in NEEDS YOU) -> 201 view anyway
        (the view says needs_you with the halt text; the client shows it -- spec §7)
    201 ServedView

GET .../rounds/{n}/files/{kind}       kind in report|diff|log|plan
    load; !allowed -> 404; n < 1 or n >= b.Round+1 -> 404
    path := Report/Diff/BuilderLog/PlanPath(name, n)
    report/diff/plan: only when n <= Serve.ClosedRound, else 404 {not_found, "round N is not closed"}
    log: any round that was sent
    http.ServeFile-equivalent streaming, Content-Type text/plain; missing file -> 404

GET .../rounds/{n}/bundle?since=<sha>
    load; !allowed -> 404; n != Serve.ClosedRound -> 404
    refs := ["refs/heads/"+b.Branch] + ["refs/relay/<name>/round-<n>" if DirtyCommit != ""]
    snap, err := s.transport.Snapshot(ctx, bare, refs, since)      -- OUTSIDE s.mu
        ErrSinceUnknown -> 422 {not_fast_forward, "since is not an ancestor of the result"}
    snap.Empty -> 204 No Content
    else Content-Type snap.ContentType, stream snap.Body, close it

POST .../rounds/{n}/ack
    load; !allowed -> 404; n > Serve.ClosedRound -> 409 {round_open, "round N is not closed"}
    Serve.AckedRound = max(AckedRound, n); Save; 200 view
```

### 4.10 `internal/serve/daemon.go`

```
func (s *Server) Tick(ctx) error
    s.mu.Lock(); defer Unlock()
    owners := readdir(cfg.Root/bindings)  (directories only)
    for each: rt := s.runtime(owner); relay.NewDaemon(rt, cfg.Interval).Tick(ctx)   -- errors logged per owner, loop continues
func (s *Server) Run(ctx) error     ticker loop like relay.Daemon.Run
```

`Daemon.Tick` returns early when the store has no bindings and calls
`rt.Herdr.ListAgents` otherwise -- the stub returns nil, so every owned
binding reconciles with `agents == nil`, which `reconcileHeadless` accepts.

## 5. High-level pseudocode: one round on the server

```
client POST /v1/bindings {name: api, repo_id, base_commit}
    -> bare repo created, binding saved ACTIVE round 1, no worktree
client POST /v1/bindings/api/rounds {round: 1, plan, bundle carrying refs/relay/api/out}
    -> Absorb into bare; branch relay/api created at out; worktree checked out;
       relay.Send stages 001-plan.md, captures baseline, startRound -> agy runs in the worktree
server Tick, every interval
    -> reconcileHeadless: drainStream, marker? no -> Alive? yes -> timeout? no -> deliverAndSettle (guard: no-op)
builder writes 001-report.md and 001-done
server Tick
    -> closeOnMarker -> queueReport (diff, facts, usage unknown, Round=2) -> closed
    -> closeServedRound: head = relay/api sha; dirty? side ref; Serve{ClosedRound:1, ResultCommit, DirtyCommit}
    -> clearProcess -> deliverAndSettle (guard)
client GET /v1/bindings/api -> round_state closed, result_commit, dirty_commit, acked_round 0
client GET files/report, files/diff, files/log; GET bundle?since= ; POST ack {1}
    -> AckedRound 1 -> round_state idle
```

## 6. Error handling

- Handler errors are exactly the spec's codes plus `invalid`. A store
  `ErrNotFound` and an `allowed == false` are the same `404 not_found`
  body (spec §2.5: a tenant never learns another's names).
- Git and transport failures during a send are `422 not_fast_forward`
  with the git message; the binding is untouched (nothing saved before
  `relay.Send`), so the client can retry after fixing its branch.
- `closeServedRound` never fails a close (§4.2).
- Every handler logs one `slog.Info` line: method, path, owner label,
  status.

## 7. Ordered implementation steps

**Step 1 -- plan, store fields, git additions.** Copy the plan. Add
§3.1 to `internal/store/types.go`. Add `ErrMergeConflict`, `MergeFF`,
`InitBare` to `internal/git` with `TestMergeFF` (ff succeeds and moves the
worktree; a diverged ref -> `ErrNotFastForward`; a dirty file the update
touches -> `ErrMergeConflict`) and `TestInitBare` (creates; second call is
a no-op). Commit: `store, git: served-binding facts; MergeFF, InitBare
(#100)`.

**Step 2 -- Git interface, fakeGit, served.go.** Widen `relay.Git` per
§4.1; add the five fakeGit methods. Add `RoundStateOf`, `ServedView`,
`closeServedRound`. Tests in `served_test.go`: `TestRoundStateOf` (one
case per arm); `TestServedViewReportOutcome`; `TestCloseServedRoundClean`
and `TestCloseServedRoundDirty` against **real git** (bare repo + linked
worktree, as `TestCommitTreeFromLinkedWorktree` in `internal/git` sets
up; a real `*git.Client` satisfies `relay.Git`): dirty -> side ref exists
in the bare repo with parent = branch head, branch unchanged,
`DirtyCommit` set; clean -> no ref, `DirtyCommit == ""`.
`TestCloseServedRoundGitFailureKeepsFacts` with fakeGit returning an
error from `Dirty`: facts unchanged, no panic. Commit: `relay: served
bindings -- round state, view, close with side ref (#100)`.

**Step 3 -- the two seams.** Guard in `deliverAndSettle`; hook in
`reconcileHeadless`. Tests: `TestDeliverAndSettleOwnedLeavesQueued` (an
owned binding with a pending payload and a planner agent present in
`agents` is returned unchanged -- state not Held, payload not confirmed;
mutation target: remove the guard and it fails);
`TestReconcileHeadlessOwnedCloseRecordsFacts` (fakeRunner + fakeGit, a
report and marker on disk, Owner set, Serve set: after Reconcile,
`Serve.ClosedRound == 1`, `ResultCommit == fakeGit.refSHA[...]`;
mutation: remove the hook and it fails). Commit: `relay: owned bindings
reconcile without a planner (#100)`.

**Step 4 -- `internal/serve` skeleton: clients, stub herdr, auth, routes.**
`clients.go`, `herdr.go`, `auth.go`, `routes.go`, `serve.go` with `New`
and `Handler`; only `GET /v1/whoami` wired. Tests: `TestClientsAddRevokeLookup`
(add -> KeyActive; revoke -> KeyRevoked; re-add -> KeyActive; unknown ->
KeyUnknown; file survives a reload); `TestAuthRejects` (one subtest per
`remote` error code, checking status 401 and the body's `error` field);
`TestAuthBodyCap` (a body one byte over `MaxBundleBytes` -> 413
`too_large`); `TestWhoAmI` (signed request -> id, label, `transports:
["git-bundle"]`); `TestNonV1Is426`. Write a test helper `signedRequest(t,
kp, method, target, body)` that sets the four headers via `remote.Sign`
with `remote.NewNonce()`; every later test uses it. Commit: `serve:
server skeleton, enrolled clients, signed-request auth (#100)`.

**Step 5 -- bindings handlers.** §4.8. Tests: `TestCreateBinding`
(201; bare repo exists at the path in `Serve.BareRepo`; binding.json has
Owner); `TestCreateInvalid` (bad name -> 400 `invalid`); `TestCreateDuplicate`
(409); `TestListIsOwnerScoped` (A creates `api`, B lists -> empty, B GETs
`api` -> 404 -- **mutation target: remove the `allowed` call from GET and
this must fail**); `TestGetTouchesLastSeen`; `TestUnavailableGatesServerWide` (A's
`unavailable` on token T writes the gate to the server-wide `ledger.json`
at `cfg.Root`, and no per-owner store dir gains a `ledger.json`). Commit:
`serve: binding create/list/get/done/unbind/resume/unavailable (#100)`.

**Step 6 -- rounds handlers with a scripted runner.** §4.9. Write
`scriptRunner` in `serve_test.go`: `Start` records the ProcSpec and
returns a handle; `Alive` returns a settable bool; `ExitCode` (0, true)
once `alive` is false; `Kill` sets alive false. Tests, each a full
sequence through `httptest.NewServer(s.Handler())` with a real client
repo and real git:
- `TestRoundStartAbsorbsAndChecksOut`: create; bundle `refs/relay/api/out`
  from the client repo; POST rounds -> 201; the worktree exists on branch
  `relay/api` at the client's head; `001-plan.md` staged; `scriptRunner`
  saw one Start with `Dir == worktree`.
- `TestRoundStartWhileRunningIs409`.
- `TestRoundResendSamePlanIs200`, `TestRoundResendDifferentPlanIs409`.
- `TestRoundStartNotFastForward`: server branch moved ahead (commit in
  the worktree via `runGit`), client sends an older `out` -> 422.
- `TestRoundCloseServesFilesBundleAck`: after start, write
  `001-report.md` (with a `relay` block, `status: done`) and `001-done`
  in the store dir, set `scriptRunner.alive=false`, call `s.Tick`; GET
  binding -> `closed`, `result_commit` == branch sha; GET files/report,
  diff, log -> 200; GET bundle?since=<client head> -> a bundle the
  client repo can `Absorb` (use `remote.NewBundleTransport` in the test)
  and after which the client's `refs/heads/relay/api` == result_commit;
  POST ack -> `acked_round: 1`, GET -> `idle`.
- `TestRoundCloseDirtyShipsSideRef`: same, but the test writes an
  untracked file into the worktree before the tick; GET shows
  `dirty_commit`; the bundle carries `refs/relay/api/round-1` and **the
  branch did not move** -- this is the case plan 1 left unpinned: the
  bundle omits the empty-range branch and `Absorb` leaves the client's
  branch as it was; assert the client's branch sha is unchanged and the
  side ref's parent is that sha.
- `TestFilesBeforeCloseIs404`, `TestBundleWrongRoundIs404`,
  `TestAckUnclosedIs409`.
Commit: `serve: rounds -- start, files, bundle, ack (#100)`.

**Step 7 -- daemon loop.** §4.10 with `TestTickWalksEveryOwner` (two
owners, one running binding each, both reconciled: `scriptRunner.Alive`
called for both) and `TestTickSkipsMissingBindingsDir`. Commit: `serve:
tick over owners (#100)`.

**Step 8 -- final.** `make check`. Report: every exported name in
`internal/serve` and `internal/relay/served.go` against §3/§4 with
`matches` or the deviation; the `CodeInvalid` addition; the mutation
targets from steps 3 and 5 and what failed. Done marker.

## Verification the planner runs

`make check`; `git diff --stat` within §2 plus `internal/remote/proto.go`
(one constant); mutation re-runs: the `deliverAndSettle` guard, the
`reconcileHeadless` hook, the `allowed` call in GET.
