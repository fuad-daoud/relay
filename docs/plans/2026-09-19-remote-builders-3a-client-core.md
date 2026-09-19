# Remote builders, plan 3a of 4: the client core -- config, signed client, remote bindings (#100)

Spec: `docs/specs/2026-09-19-remote-builders-design.md` (in this tree)
§2.1–2.4, §4.3, §5, §7. Plans 1, 2a, 2b are merged (#206, #207, #208):
`internal/remote` (keys, signing, wire types, `TreeTransport`),
`internal/serve` (the server, `httptest`-able, with TLS and enrollment).
This plan makes a laptop drive a round on that server. It leaves the
status/ui columns, doctor lines and local diff stats to plan 3b, and the
scripted end-to-end run to plan 4. Surface-freeze exception for the new
verbs is on #114.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. Do **not** rebase, merge or otherwise move
this branch; if the base is wrong, that is a halt with the reason.

**Scope guard.** Touch only the files in §2. No module dependency. Do not
run `make e2e`. Do not widen an exported signature outside §2 except the
`relay.Git` interface (§4.1). If a test outside §2 stops compiling, halt.
Foreground only; no sub-agents. **CI rule:** no test in `cmd/relay`
executes a subcommand that reaches herdr or the network; every rule is
tested in `internal/...` with a fake or an in-process `serve.Server`.

**Git in tests.** Same helper discipline as before (`GIT_CONFIG_GLOBAL=/dev/null`,
fixed identity); copy, do not import.

**Commit the plan with the work.** Copy this plan file (the path relay gave
you) to `docs/plans/2026-09-19-remote-builders-3a-client-core.md` in your
worktree and include it in the first commit.

**Check command.** `make check`; green before every commit.

**Report discipline.** The `changed_paths` list in the report's trailing
`relay` block must name every file the diff touches; an empty list against
a non-empty diff is a report defect.

## 1. System overview

A remote binding on the client is an ordinary binding whose builder
endpoint says `Mode: remote` and names a server from
`~/.config/relay/servers.json`. `relay add --name api --server zen`
creates the local branch `relay/api` (no worktree), tells the server to
create the binding, and saves it. `relay send` stages the plan locally as
today, ships the branch as a bundle of `refs/relay/api/out`, and asks the
server to start the round. The daemon's reconcile gets a third branch:
instead of a pane or a process it reads the server's binding view; when
the view says a round closed, it pulls the report, diff and log into the
binding's dir, fetches the result bundle into `relay/api`, acks, and hands
the report to the existing `queueReport` path -- so delivery, `pull`,
`wait`, `status` and the statusline see exactly what they see for a
headless builder. `done`, `unbind`, `bind --resume` and `unavailable` are
forwarded to the server first and recorded locally only when the server
agreed.

The signed HTTP client lives in `internal/remote/client` and is tested
against a real in-process `serve.Server` over `httptest`, pinned by
fingerprint, so the wire is exercised end to end without any network.

## 2. File structure

```
internal/remote/
  proto.go             + BindingView.ClosedRound

internal/serve/
  bindings.go (or wherever ServedView is filled)   ClosedRound in the view -- one line

internal/remote/client/
  config.go            Servers file, ServerEntry, Load/Save, key file helpers
  config_test.go
  client.go            Client: one method per endpoint, signed, pinned; error types
  client_test.go       against serve.Server via httptest (real TLS, pinned)

internal/git/
  client.go            + CreateBranch
  client_test.go       + TestCreateBranch

internal/store/
  types.go             ModeRemote; Endpoint.Server/LastShipped/LastKnown/RemoteStatus; Binding.RemoteUnreachableSince

internal/relay/
  herdr.go             RemoteClient interface; Runtime.Remote, Runtime.Transport; Git + CreateBranch
  remote.go            addRemote, sendRemote, reconcileRemote, catchUp, forward helpers
  remote_test.go       fakeRemote; every §7 row
  add.go               AddOptions.Server -> addRemote
  send.go              remote branch -> sendRemote
  reconcile.go         dispatch to reconcileRemote
  bind.go              --resume on a remote binding forwards; --rebind refused
  status.go            Done: forward first (see §4.6)
  gc.go                remote binding: skip unless DONE and server confirmed
  fake_test.go         fakeGit + CreateBranch

cmd/relay/
  client.go            relay client init|add-server|rm-server; relay servers
  client_test.go       usage/flags only
  main.go              dispatch; add --server; Runtime.Remote/Transport wiring; usage lines
  README.md            "Command surface" + "Remote builders: the client"

docs/plans/2026-09-19-remote-builders-3a-client-core.md
```

## 3. Data structures

### 3.1 `internal/remote/client/config.go`

```
type ServerEntry struct {
    URL         string `json:"url"`                     // https://zen:7777 (or http:// only with Insecure)
    Fingerprint string `json:"fingerprint,omitempty"`   // sha256:<hex>; required unless CA == "system"
    CA          string `json:"ca,omitempty"`            // "" (pin) | "system"
    Insecure    bool   `json:"insecure,omitempty"`      // plain http allowed
}
type Servers map[string]ServerEntry          // key: the server's short name

LoadServers(path string) (Servers, error)    // missing file -> empty, nil
SaveServers(path string, s Servers) error    // 0600, write-temp-rename
ValidateEntry(e ServerEntry) error           // url parses; https unless Insecure; fingerprint or CA present unless Insecure

KeyPaths(configDir string) (priv, pub string)          // <configDir>/relay/client.key, client.pub
ServersPath(configDir string) string                   // <configDir>/relay/servers.json
LoadKey(privPath string) (remote.Keypair, error)       // ErrNoKey when absent: "no client key; run relay client init"
InitKey(privPath, pubPath string) (remote.Keypair, error)   // ErrKeyExists when present; 0600/0644
```

`configDir` is what `userConfigRoot()` returns in `cmd/relay`; the package
never resolves it itself.

### 3.2 `internal/store/types.go`

```
ModeRemote Mode = "remote"
func (e Endpoint) Remote() bool { return e.Mode == ModeRemote }

on Endpoint (builder side of a remote binding):
    Server       string `json:"server,omitempty"`         // key into servers.json
    LastShipped  string `json:"last_shipped,omitempty"`   // sha of relay/<name> last bundled out
    LastKnown    string `json:"last_known,omitempty"`     // result sha last fetched in
    RemoteStatus string `json:"remote_status,omitempty"`  // last view: running|idle|closed|needs_you|unreachable (for status/ui, plan 3b)

on Binding:
    RemoteUnreachableSince time.Time `json:"remote_unreachable_since,omitempty"`
    RemoteAbsorbFailures   int       `json:"remote_absorb_failures,omitempty"`
```

`Round`, `Branch`, `Base`, `Repo` (the client repo root) are reused as-is.
`CWD` = `Repo` for a remote binding (the planner's repo; nothing is ever
written there but refs).

### 3.3 `internal/remote/proto.go`

`BindingView` gains `ClosedRound int json:"closed_round"` -- the server's
`Serve.ClosedRound`; `ServedView` fills it.

## 4. Interfaces

### 4.1 `internal/git`

```
CreateBranch(ctx, dir, branch, commit string) error
    git branch <branch> <commit>; ErrBranchExists when it exists.
```

`relay.Git` gains `CreateBranch`; `fakeGit` records it.

### 4.2 `internal/remote/client/client.go`

```
type Client struct { /* servers Servers, key remote.Keypair, http per server (pinned transport), now func() time.Time */ }
New(servers Servers, key remote.Keypair, now func() time.Time) *Client

Errors:
  *remote.ErrorBody          -- any non-2xx with a JSON body; carries Status int (add an unexported field or wrap: type HTTPError struct{Status int; Body remote.ErrorBody}; HTTPError implements error with Body.Message)
  ErrUnreachable             -- dial/timeout/connection reset, wrapped with the cause
  ErrCertChanged             -- pinned fingerprint mismatch
  ErrUnknownServer           -- name not in servers.json
  ErrVersion                 -- 426

Pinning: tls.Config{InsecureSkipVerify: true, VerifyPeerCertificate: compares serve.FingerprintOf(rawCerts[0])
         with the entry's Fingerprint} when CA == ""; system roots when CA == "system"; plain http when Insecure.
Every request: body spooled to a temp file (bundles) or bytes, sha256 computed, headers from remote.Sign with
remote.NewNonce(), target = path + "?" + query as remote.Canonical documents. Timeouts: 30s for control calls,
none for bundle/file streams (ctx governs).

Methods (server is the short name; every method resolves it or returns ErrUnknownServer):
  WhoAmI(ctx, server) (remote.WhoAmI, error)
  Candidates(ctx, server) (remote.CandidatesResponse, error)
  CreateBinding(ctx, server, req remote.CreateBindingRequest) (remote.BindingView, error)
  GetBinding(ctx, server, name) (remote.BindingView, error)
  StartRound(ctx, server, name string, round int, plan []byte, bundle io.Reader) (remote.BindingView, error)   // bundle nil = no part
  RoundFile(ctx, server, name string, round int, kind string) (io.ReadCloser, error)
  RoundBundle(ctx, server, name string, round int, since string) (io.ReadCloser, error)   // (nil, nil) on 204
  Ack(ctx, server, name string, round int) (remote.BindingView, error)
  Unavailable(ctx, server, name, token, reason string) error
  Done(ctx, server, name string) error
  Unbind(ctx, server, name string) error
  Resume(ctx, server, name string) (remote.BindingView, error)
```

### 4.3 `internal/relay/herdr.go`

```
type RemoteClient interface { the twelve methods above, same signatures }
Runtime.Remote    RemoteClient          // nil: no servers configured; every remote path returns ErrRemoteUnavailable
Runtime.Transport remote.TreeTransport  // nil with Remote nil; cmd wires remote.NewBundleTransport(gitClient, "")
ErrRemoteUnavailable = errors.New("no remote client configured; run relay client init and relay client add-server")
```

### 4.4 `internal/relay/remote.go` -- add and send

```
AddOptions gains Server string.

addRemote(ctx, rt, opts) (AddResult, error)      called by Add when opts.Server != ""
    Pre: opts.CWD == "" (else "remote builders are add-only: --cwd and --server cannot be combined");
         opts.Headless ignored (a remote builder is headless by construction);
         rt.Remote != nil; rt.Git != nil.
    1. store.ValidName; binding must not exist locally.
    2. base := opts.Base if given (AddOptions gains Base string, a ref or sha) else rt.Git.HeadCommit(opts.Repo)
       resolve to a full sha with RefSHA(opts.Repo, base) when it is not 40-hex; missing -> error
    3. root := rt.Git.RootCommit(opts.Repo); repoID := remote.RepoID(root)
    4. candidate := opts.Candidate; if non-empty, rt.Remote.Candidates(server) must list it (else error naming the
       server's tokens); if empty, leave "" and let the server pick.
    5. view := rt.Remote.CreateBinding(ctx, server, {Name, RepoID, BaseCommit: base, Candidate, RoundCap, RoundTimeoutMS})
       HTTPError 409 -> "binding api already exists on zen for this client; relay bind --resume --name api"
    6. rt.Git.CreateBranch(opts.Repo, "relay/"+name, base)   -- after the server agreed, so a refused create leaves no branch
       ErrBranchExists -> error "branch relay/api exists; delete it or pick another name" AND rt.Remote.Unbind the
       server binding just created (best effort, logged).
    7. b := Binding{Name, CWD: opts.Repo, Repo: opts.Repo, Branch, Base: base, Planner: <as Add fills it>,
                    Builder: Endpoint{Mode: ModeRemote, Server: server, Kind: <kind from view.Candidate's harness, "" if unknown>,
                                      AgentName: name},
                    BuilderCandidate: view.Candidate, Round: 1, State: active, RoundCap/Timeout as Add}
       Save under lock; append the same pick log entry Add writes, with the candidate the server reported.
    AddResult as Add returns (Worktree "").

sendRemote(ctx, rt, b, planBody []byte) (SendResult, error)      called by Send when the pre-lock hint is Remote()
    OUTSIDE the lock:
    1. rt.Git.UpdateRef(b.Repo, "refs/relay/"+b.Name+"/out", RefSHA("refs/heads/"+b.Branch), "")
    2. snap := rt.Transport.Snapshot(b.Repo, ["refs/relay/<name>/out"], b.Builder.LastShipped)
       ErrSinceUnknown -> treat as first send: Snapshot with since ""
    3. view, err := rt.Remote.StartRound(ctx, server, name, b.Round, planBody, snap.Body or nil when Empty)
       HTTPError 409 round_started -> proceed as success (the server has this round with this plan; the client's
                                       own idempotency check is the plan hash compare the server did)
       HTTPError 409 round_open   -> error "round N is running on zen"
       HTTPError 422              -> error "<server>: <message>"  (the pull hint is the server's wording)
       ErrUnreachable            -> error "zen unreachable: <cause>"; nothing recorded
    UNDER the lock:
    4. reload b; if b.Round != the round sent -> error "round advanced during send; run relay status" (nothing recorded;
       the next send hits round_started and records)
    5. write PlanPath(name, round) = planBody; AppendLog plan to_builder (as Send does); b.RoundStartedAt = now;
       b.State = active; b.Halt = ""; b.Builder.LastShipped = snap.Heads["refs/relay/<name>/out"];
       b.Builder.RemoteStatus = string(view.RoundState); Save.
    SendResult as Send returns.
```

`Send` itself: after the pre-lock `Load(name)` hint, `if hint.Builder.Remote() { return sendRemote(ctx, rt, hint, body) }`
-- before the pane/headless liveness checks and before `CaptureBaseline` (no local tree to baseline).

### 4.5 `internal/relay/remote.go` -- reconcile

```
Reconcile: right after the DONE early-return and before the Headless() dispatch:
    if b.Builder.Remote() { return reconcileRemote(ctx, rt, tx, b, agents) }

reconcileRemote(ctx, rt, tx, b, agents) (store.Binding, error)
    planner refresh as reconcileHeadless does (FindAgent on agents).
    if rt.Remote == nil -> once per binding slog.Warn; return b
    view, err := rt.Remote.GetBinding(ctx, b.Builder.Server, b.Name)
    switch:
      ErrUnreachable:
          if b.RemoteUnreachableSince.IsZero(): set now; slog.Warn once ("zen unreachable")
          b.Builder.RemoteStatus = "unreachable"
          if round open locally (HasEntry plan && !report for b.Round) &&
             now - b.RemoteUnreachableSince > roundBudget(b) + unreachableGrace (const 30m):
                 return haltBinding(ctx, rt, b, fmt.Sprintf("%s: %s unreachable for %s; round %d may still be running there",
                                    b.Name, server, dur, b.Round))
          return b, nil
      HTTPError 401 (any auth code): return haltBinding("<name>: <server>: <message>")
      HTTPError 404:                 return haltBinding("<name>: <server>: binding removed by the server admin")
      ErrCertChanged:                b.Builder.RemoteStatus = "cert"; slog.Warn once; return b, nil   (hard refuse lives in the client; status shows it)
      other err:                     slog.Warn; return b, nil
    b.RemoteUnreachableSince = zero; b.Builder.RemoteStatus = string(view.RoundState)
    switch view.RoundState:
      running:   mirror the builder log: rt.Remote.RoundFile(log) -> write to BuilderLogPath(name, b.Round) (whole file,
                 O_TRUNC; it is the terminal tab, never parsed); errors are slog.Warn; return b, nil
                 (no local checkRoundTimeout: the server enforces the budget)
      needs_you: return haltBinding("<name>: "+view.Halt)  (haltBinding dedups per round)
      closed:
          if view.ClosedRound >= b.Round:            // the round we sent has closed and we have not recorded it
              return catchUp(ctx, rt, tx, b, view)
          fallthrough to deliverAndSettle             // closed and already recorded: pending delivery to the planner
      idle:      if b.State == broken -> active; return deliverAndSettle(...)

catchUp(ctx, rt, tx, b, view) (store.Binding, error)    -- each step idempotent; any failure returns b unchanged
    n := view.ClosedRound   (== b.Round)
    1. for kind in report, diff, log: rc := rt.Remote.RoundFile(server, name, n, kind); write to the store path
       (ReportPath / DiffPath / BuilderLogPath) via temp-and-rename. A 404 on diff is fine (no diff); a 404 on
       report is a halt: haltBinding("<name>: <server> closed round N without a report file").
    2. rc := rt.Remote.RoundBundle(server, name, n, b.Builder.LastKnown)
       nil (204) -> nothing to absorb (result == LastKnown)
       else refs := ["refs/heads/"+b.Branch] + ["refs/relay/<name>/round-<n>" if view.DirtyCommit != ""]
            rt.Transport.Absorb(b.Repo, ContentTypeGitBundle, rc, refs)
            git.ErrNotFastForward with the branch checked out (message contains "checked out") ->
                slog.Info("checkout another branch, then relay pull"); return b, nil   (retried next tick, not a halt)
            other err -> b.RemoteAbsorbFailures++; Save via return; >= 10 -> haltBinding("<name>: cannot absorb round N from <server>: <err>")
    3. b.Builder.LastKnown = view.ResultCommit; b.RemoteAbsorbFailures = 0
    4. rt.Remote.Ack(server, name, n)  -- failure: slog.Warn, return b with LastKnown set (re-acked next tick; the
       server's ack is idempotent)
    5. entries := tx.ReadLog(name); payload := fmt.Sprintf("Builder finished round %d on %s. Report: %s", n, server, ReportPath)
       note := "" ; if view.DirtyCommit != "" note = "uncommitted work at refs/relay/<name>/round-<n>"
       next, err := queueReport(ctx, rt, tx, b, entries, ReportPath, payload, note)
       -- queueReport captures a diff from RoundBaselineTree; it is empty for a remote binding, so no local diff
          entry is produced this plan (plan 3b reads the downloaded patch). The report tail is parsed as usual.
    6. next.Builder.RemoteStatus = "idle"; return deliverAndSettle(ctx, rt, tx, next, agents)
```

### 4.6 Lifecycle forwarding

```
Done (status.go):   if b.Builder.Remote(): rt.Remote.Done(server, name) first; HTTPError 409 round_open -> error
                    "round N is running on zen; wait or relay unbind --force"; ErrUnreachable -> error, nothing local.
                    Then the existing local Done path (which for a binding with Worktree "" releases nothing).
Unbind (bind.go):   same shape with rt.Remote.Unbind; a 404 from the server is treated as already gone (proceed locally).
Bind --resume:      on a remote binding: --rebind/--builder/--headless -> "cannot change a remote builder; unbind and add";
                    rt.Remote.Resume(server, name) then State active locally; the local branch must still exist
                    (BranchExists) else error naming it.
GC (gc.go):         a remote binding is eligible only when State == done (the server was told at Done); no server call.
Unavailable (cmd):  `relay unavailable <token> [--reason]` keeps its local behaviour and additionally, for every binding in the
                    store with Builder.Remote() and an open round, calls rt.Remote.Unavailable(server, name, token, reason);
                    failures are printed per binding, never fatal. Implement as relay.ForwardUnavailable(ctx, rt, token, reason) []string
                    (lines to print) in remote.go, called from cmdUnavailable.
Answer, Ask, Fork:  a remote binding is refused with "remote builders take no dialogs" / "consults are local-only" /
                    "fork across servers is not supported" -- one guard each at the top of the verb, tested.
```

### 4.7 `cmd/relay`

```
relay client init                         -> InitKey; prints "client id <id>" and the enrollment line "ed25519 <b64> <user>@<host>"
relay client add-server <name> <url> (--fingerprint sha256:... | --ca system | --insecure)
                                          -> ValidateEntry; on --fingerprint, call WhoAmI once and print "enrolled as <label>" or
                                             "not enrolled on <name>: give the admin: <enrollment line>" (a 401 is not an error here)
relay client rm-server <name>             -> refused while any binding in the store names it
relay servers                             -> table: name  url  enrolled-as|not enrolled|unreachable|cert changed
relay add --server <name> [--base <ref>]  -> AddOptions.Server/Base
```

`newRuntime()`: when `servers.json` is non-empty and `client.key` exists,
`Remote = client.New(...)`, `Transport = remote.NewBundleTransport(gitClient, "")`;
otherwise both nil. A missing key with servers configured prints one
stderr line (`relay: servers.json present but no client key; run relay
client init`) and leaves them nil.

Usage text and README: "Command surface" gains the four lines; new README
section "Remote builders: the client" (init, add-server with the
fingerprint from the admin, add --server, what comes back as `relay/<name>`,
the side ref for uncommitted work, what is refused).

## 5. Pseudocode: a round from the laptop

```
relay client init                     -> ~/.config/relay/client.key; prints the line the admin enrolls
relay client add-server zen https://zen:7777 --fingerprint sha256:...   -> whoami: enrolled as alice@laptop
relay add --name api --server zen     -> server binding created; local branch relay/api at HEAD; binding Mode=remote
relay send --name api --file plan.md  -> out ref, bundle (full history first time), StartRound; plan staged; log entry
daemon tick                           -> GetBinding: running -> log mirrored
... builder finishes on zen ...
daemon tick                           -> closed, ClosedRound 1 -> catchUp: files, bundle into relay/api, ack, queueReport
                                      -> deliverAndSettle types the report into the planner pane as today
relay wait / relay pull               -> unchanged
```

## 6. Error handling

- A send that did not reach the server records nothing (§4.4 step 3).
- Unreachable is never NEEDS YOU until it outlasts the round budget plus
  30 minutes (§4.5).
- catchUp is ordered files -> bundle -> LastKnown -> ack -> queueReport;
  each failure leaves the binding as it was.
- Every error message that comes from the server is printed as
  `<server>: <message>`, never a status code.

## 7. Ordered implementation steps

**Step 1 -- plan, proto, store, git.** Copy the plan. `ClosedRound` on
the view (server fills it; extend the existing `TestRoundCloseServesFilesBundleAck`
assertion to check it). §3.2. `CreateBranch` + `TestCreateBranch`.
`relay.Git` + `fakeGit`. Commit: `store, git, remote: remote-binding
fields, CreateBranch, closed_round (#100)`.

**Step 2 -- client config and key files.** §3.1 with `config_test.go`:
round-trip, `ValidateEntry` refusals (http without insecure; https without
pin or CA), `InitKey` refuses overwrite, `LoadKey` `ErrNoKey`. Commit:
`client: servers.json and client key files (#100)`.

**Step 3 -- the signed client against a real server.** §4.2 with
`client_test.go`: a helper starts `serve.Server` (TLS from `serve.InitTLS`
in a temp root, one enrolled key, `scriptRunner`-style fake runner copied
from `internal/serve/serve_test.go`'s shape, on `127.0.0.1:0`) and returns
a `Client` pinned to its fingerprint. Tests: `TestWhoAmIPinned`;
`TestCertChangedRefused` (a client pinned to a different fingerprint ->
`ErrCertChanged`); `TestUnknownServer`; `TestUnreachable` (a closed port);
`TestNotEnrolledIs401Body` (unenrolled key -> `HTTPError{401, not_enrolled}`);
`TestCreateStartFilesBundleAck` (create; StartRound with a real bundle from
a temp client repo; the server's worktree exists; write report+marker on
the server side and tick it; GetBinding closed with `ClosedRound == 1`;
RoundFile report; RoundBundle absorbed into the client repo; Ack). Commit:
`client: signed, pinned HTTP client over the serve API (#100)`.

**Step 4 -- add and send.** §4.4 with `remote_test.go` and a `fakeRemote`
(records calls; scripted responses per method; can return `HTTPError` or
`ErrUnreachable`). Tests: `TestAddRemoteCreatesBranchAfterServerAgrees`
(order: CreateBinding before CreateBranch -- assert on the fake's call
log); `TestAddRemoteRefusesCWD`; `TestAddRemoteServerConflict` (409 -> the
message; no branch created); `TestAddRemoteBranchExistsUnbindsServer`;
`TestSendRemoteRecordsOnlyOnSuccess` (unreachable -> no plan file, no log
entry, Round unchanged); `TestSendRemoteRoundStartedIsSuccess`;
`TestSendRemoteFirstSendFullBundle` (LastShipped "" -> Snapshot since "");
`TestSendRemoteSetsLastShipped`. Commit: `relay: add --server and send
over the wire (#100)`.

**Step 5 -- reconcile and catch-up.** §4.5. Tests, one per row of the
spec's §7 client column that this plan owns: `TestReconcileRemoteRunningMirrorsLog`;
`TestReconcileRemoteNeedsYouHalts`; `TestReconcileRemoteUnreachableIsNotHalt`;
`TestReconcileRemoteUnreachablePastBudgetHalts` (mutation target: drop the
grace and the "not halt" test fails); `TestReconcileRemote401Halts`;
`TestReconcileRemote404Halts`; `TestCatchUpOrderAndIdempotence` (fake
fails at bundle: no LastKnown, no ack, no report entry; next tick
succeeds; exactly one report entry; mutation target: move Ack before
Absorb and the "no ack" assertion fails); `TestCatchUpDirtyNote`;
`TestCatchUpBranchCheckedOutRetries`; `TestCatchUpAbsorbFailuresHaltAtTen`.
Real git for the Absorb paths (a client repo + a bundle built with
`remote.NewBundleTransport` from a second repo), `fakeRemote` for the
wire. Commit: `relay: remote reconcile and catch-up (#100)`.

**Step 6 -- lifecycle forwarding and refusals.** §4.6 with tests:
`TestDoneRemoteForwardsFirst` (server 409 -> local state unchanged);
`TestUnbindRemote404Proceeds`; `TestResumeRemoteRefusesRebind`;
`TestGCRemoteOnlyWhenDone`; `TestForwardUnavailable` (two remote bindings,
one with an open round -> exactly one server call);
`TestAnswerAskForkRefuseRemote`. Commit: `relay: remote lifecycle
forwarding, refusals (#100)`.

**Step 7 -- CLI, wiring, README.** §4.7. `cmd/relay/client_test.go`:
usage on no args, `add-server` flag exclusivity (`--fingerprint` with
`--ca` -> exit 2), `rm-server` refusal message shape via a pure helper in
`internal/relay` (`ServerInUse(bindings, name) []string`). Commit: `cli:
relay client, relay servers, add --server (#100)`.

**Step 8 -- final.** `make check`. Report: exported names vs §3/§4; the
mutation targets from steps 5 and what failed; `changed_paths` complete.
Done marker.

## Verification the planner runs

`make check`; scope per §2; mutations: the unreachable grace, the ack
ordering, the `Remote()` dispatch in `Reconcile` (remove it and
`TestReconcileRemoteRunningMirrorsLog` must fail); then a manual run:
`relay serve` on this machine in a temp state root, `relay client init`,
`add-server` with the printed fingerprint, `serve enroll`, `relay add
--server`, `relay send` with a one-line plan, watch `relay status`.
