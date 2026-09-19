# Remote builders, plan 3b of 4: surfaces -- diff facts, candidate refresh, status row, doctor, sync-on-read (#100)

Spec: `docs/specs/2026-09-19-remote-builders-design.md` (in this tree)
§2.2 (sync-on-read), §5.5 (UI/status), §7. Plans 1–3a are merged (#206,
#207, #208, #210): a laptop drives a round on a server. This plan closes
the gaps the first real round over the wire showed (PR #210's "known
gaps"): the client's diff entry says `unavailable: no baseline`; the local
`BuilderCandidate` stays at the original token after a server-side
switch; `status`/`ui` print nothing useful in the builder column; `relay
doctor` knows nothing about servers; `status`/`pull`/`wait` need the
daemon to see a closed round; the server's request log prints `owner=""`;
`add --server`'s pick line says "explicit, policy bypassed". Plan 4 is the
scripted end-to-end test.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. Do not rebase, merge or move this branch.

**Scope guard.** Touch only the files in §2. No module dependency. Do not
run `make e2e`. Foreground only; no sub-agents. **CI rule:** nothing in
`cmd/relay` tests reaches herdr or the network.

**Git in tests.** Same helper discipline as before; copy, do not import.

**Commit the plan with the work.** Copy this plan file (the path relay gave
you) to `docs/plans/2026-09-19-remote-builders-3b-surfaces.md` in your
worktree and include it in the first commit.

**Check command.** `make check`; green before every commit.

**Report discipline.** `changed_paths` names every file the diff touches.

## 1. System overview

Six small changes, each one seam:

1. **Diff facts travel in the view.** The server already records a diff
   log entry at close (`DiffSummary` note, commits, clean/dirty). The view
   carries those three facts; `catchUp` writes the client's diff entry
   from them (with the downloaded patch as `Path`) before `queueReport`,
   which then skips its own capture because the entry exists. No patch
   parsing, no baseline.
2. **Candidate refresh.** Every tick that gets a view compares
   `view.Candidate` with `BuilderCandidate`; a difference is a server-side
   switch: update the token and the endpoint kind, append a `switch` log
   entry naming the server, so usage and status name the builder that
   actually ran.
3. **Status row.** A remote binding's `BuilderPane` is the server name and
   `BuilderStatus` is the last `RemoteStatus` (`running`, `idle`, `closed`,
   `needs_you`, `unreachable`, `cert`). Rendered in `status` and `ui` from
   the store; no network on the read path.
4. **Doctor.** `relay doctor` gains one check per configured server:
   reachable and enrolled (label), not enrolled (with the line to give the
   admin), unreachable, certificate changed. Built from the same probe
   `relay servers` uses, moved into `internal/relay` so both share it.
5. **Sync-on-read.** `relay status`, `relay pull` and each iteration of
   `relay wait` first run a one-shot remote reconcile over the store's
   remote bindings, so a closed round is collected without the daemon.
6. **Two cosmetics:** the server's request log names the caller's label;
   `add --server`'s pick entry says the server picked.

## 2. File structure

```
internal/remote/
  proto.go             + BindingView.DiffNote, DiffCommits, DiffTree

internal/relay/
  served.go            ServedView fills the three diff facts from the closed round's diff entry
  remote.go            catchUp diff entry; candidate refresh; SyncRemote; ProbeServers; addRemote pick entry
  remote_test.go       + tests in §7
  status.go            remote row
  status_test.go       + TestStatusRemoteRow

internal/doctor/
  doctor.go            WithExtraChecks option (appends caller-built checks after the last group)
  doctor_test.go       + TestExtraChecksAppended

internal/serve/
  routes.go            request log: owner label
  serve_test.go        + assertion on the log line (or a LabelOf call in the middleware test)

cmd/relay/
  main.go              status/pull/wait call relay.SyncRemote; doctor appends relay.ProbeServers checks
  client.go            relay servers renders relay.ProbeServers
  doctor.go            (if that is where doctor is assembled) extra checks

README.md              "Remote builders: the client": one paragraph on what status/doctor show
docs/plans/2026-09-19-remote-builders-3b-surfaces.md
```

## 3. Data structures

### 3.1 `internal/remote/proto.go`

```
on BindingView (closed only, "" / 0 otherwise):
    DiffNote    string `json:"diff_note,omitempty"`     // the server's diff entry Note, e.g. "1 file, +1 -0; 1 commit, clean"
    DiffCommits int    `json:"diff_commits,omitempty"`  // LogEntry.Commits
    DiffTree    string `json:"diff_tree,omitempty"`     // LogEntry.Tree: "clean" | "dirty" | ""
```

`ServedView` fills them from the newest `KindDiff` entry for
`Serve.ClosedRound`, the same way it fills `ReportOutcome` from the report
entry.

### 3.2 `internal/relay/remote.go`

```
type ServerProbe struct {
    Name   string
    URL    string
    State  string   // "enrolled" | "not enrolled" | "unreachable" | "cert changed" | "no key" | "error"
    Label  string   // enrolled as
    Detail string   // the enrollment line for "not enrolled"; the error text otherwise
}
```

## 4. Interfaces

### 4.1 `internal/relay/remote.go`

```
catchUp, between the file downloads (step 1) and the bundle (step 2), or anywhere before queueReport:
    if view.DiffNote != "" && !HasEntry(entries, n, DirToPlanner, KindDiff):
        AppendLog(name, LogEntry{TS: now, Round: n, Direction: DirToPlanner, Kind: KindDiff,
                                 Path: DiffPath(name, n) if the diff file was downloaded else "",
                                 Note: view.DiffNote, Commits: view.DiffCommits, Tree: view.DiffTree, Confirmed: true})
    -- queueReport then sees the entry and skips CaptureRoundDiff; DiffLine for the payload comes from...
       queueReport only appends DiffLine inside the "no entry yet" branch, so the payload would lose its
       "Diff:" line. Add it here instead: payload passed to queueReport already contains the report text;
       append "\n" + a line built by DiffLineFromNote(view.DiffNote, view.DiffCommits, view.DiffTree, b.Branch)
       -- a new pure helper next to DiffLine that produces the same sentence shape DiffLine does from the
       stored facts rather than a DiffResult. Test it against DiffLine's output for the same facts.

reconcileRemote, right after a view is obtained (any RoundState):
    if view.Candidate != "" && view.Candidate != b.BuilderCandidate:
        prev := b.BuilderCandidate
        b.BuilderCandidate = view.Candidate
        b.Builder.Kind = harness kind of the token (candidate.Parse or the existing helper the pick uses; "" if unparseable)
        AppendLog(name, LogEntry{Round: b.Round, Direction: DirToPlanner, Kind: store.KindSwitch (the kind the mid-round
                                 switch entry uses -- find it in switch.go), Note: fmt.Sprintf("switched on %s: %s -> %s",
                                 server, prev, view.Candidate), Confirmed: true})

SyncRemote(ctx, rt Runtime) (synced int, err error)
    if rt.Remote == nil -> (0, nil)
    bindings := rt.Store.List(); for each with Builder.Remote() and State != done:
        rt.Store.WithLock(func(tx) { fresh := tx.Load; next := reconcileRemote(ctx, rt, tx, fresh, nil); save if changed })
        -- agents nil: reconcileRemote's planner refresh tolerates it (FindAgent on nil is "not found", which only
           leaves the planner endpoint as it was). deliverAndSettle with agents nil marks PlannerGone -> Orphaned:
           NOT wanted on a read path. So SyncRemote passes a flag or calls a variant: split reconcileRemote into
           observeRemote (view -> state/log/catchUp, no delivery) and the tick wrapper that adds deliverAndSettle.
           SyncRemote calls observeRemote. Name them exactly so.
    errors per binding are collected into one joined error; synced counts bindings that changed.

ProbeServers(ctx, rt Runtime, servers map[string]client.ServerEntry, enrollLine string) []ServerProbe
    -- rt.Remote nil (no key): every entry State "no key", Detail "run relay client init"
    -- per server, in name order: WhoAmI -> "enrolled"/Label; HTTPError 401 not_enrolled -> "not enrolled", Detail enrollLine;
       ErrCertChanged -> "cert changed"; ErrUnreachable -> "unreachable", Detail cause; other -> "error"
    Pure over the RemoteClient; tested with fakeRemote. `internal/relay` may import internal/remote/client for the
    ServerEntry type only if that does not create a cycle (client imports remote and serve? check: client must not
    import relay). If it would cycle, take `map[string]string` name->url instead and let cmd pass it.

RenderServers(probes []ServerProbe) string      // the table `relay servers` prints today, moved here

addRemote's pick entry: Note "picked <token> on <server>: server's pick" when opts.Candidate == "",
    "picked <token> on <server>: explicit" otherwise. Look at pickEntry's fields and write the same shape.
```

### 4.2 `internal/relay/status.go`

```
in the row builder, beside the Headless() branch:
    if b.Builder.Remote():
        row.BuilderPane = b.Builder.Server
        row.BuilderStatus = b.Builder.RemoteStatus; "" -> "unknown"
        -- "unreachable" and "cert" are shown as such; the ui's stateStyle already colours unknown statuses neutrally
```

`RenderStatus` needs no change if it prints `BuilderPane` and
`BuilderStatus` generically; confirm by test. In `internal/ui/pane.go:50`
the pane column is `%-4s`; a server name is longer -- widen that format
to `%-9s` only if the golden tests in `internal/ui` still pass with the
existing fixtures (they use 4-character pane ids; check). If widening
breaks a golden, leave the width and let the name overflow; say which in
the report.

### 4.3 `internal/doctor/doctor.go`

```
WithExtraChecks(checks []Check) RunOption   -- appended verbatim at the end of Report.Checks
```

`cmd/relay` builds the checks from `ProbeServers`: Group `""`, Name
`servers`, Detail `<name>: enrolled as <label>` (ok) / `<name>: not
enrolled` with Fix `give the admin: <line>` (warn) / `<name>: unreachable:
<cause>` (warn, ProbeFailed) / `<name>: certificate changed` with Fix
`relay client add-server <name> <url> --fingerprint <new>` (fail). No
servers configured -> no check.

### 4.4 `internal/serve/routes.go`

The request log's `owner` is `s.clients.LabelOf(caller)` (falls back to the
id prefix), `-` when unauthenticated.

### 4.5 `cmd/relay`

- `cmdStatus`: `relay.SyncRemote` before `relay.Status` when `rt.Remote != nil`;
  a sync error is one stderr line, never fatal.
- `cmdPull`: same, before the claim.
- `cmdWait`: same, at the top of each poll iteration.
- `cmdServers`: `RenderServers(ProbeServers(...))`.
- doctor: `WithExtraChecks(serverChecks(ProbeServers(...)))`.

## 5. Pseudocode

```
tick / sync on a remote binding:
    view := GetBinding
    refreshCandidate(view)
    switch view.RoundState ... closed -> catchUp:
        files; diff entry from view; bundle; LastKnown; ack; queueReport(payload + DiffLineFromNote)
relay status (no daemon running):
    SyncRemote -> the closed round is collected -> rows show it -> pending delivery waits for pull
```

## 6. Error handling

Unchanged rules: sync failures are advisory on read paths; the daemon
path keeps its halts. `WithExtraChecks` never changes the doctor verdict
except through the severities it carries.

## 7. Ordered implementation steps

**Step 1 -- view facts and the client's diff entry.** §3.1, `ServedView`,
`catchUp`, `DiffLineFromNote`. Tests: `TestServedViewDiffFacts` (server
side, extend the existing close test); `TestCatchUpWritesDiffEntryFromView`
(entry has Note/Commits/Tree/Path; `queueReport` did not call
`fakeGit.SnapshotTree` -- assert the call count; mutation target: skip the
entry and the count goes up / the note reads "no baseline");
`TestDiffLineFromNoteMatchesDiffLine`. Commit: `relay: remote rounds carry
the server's diff facts (#100)`.

**Step 2 -- candidate refresh.** Tests: `TestReconcileRemoteRefreshesCandidate`
(token and kind change; one switch entry naming the server; a second
identical view adds nothing). Commit: `relay: refresh the candidate from
the server's view (#100)`.

**Step 3 -- status row.** §4.2 with `TestStatusRemoteRow` (pane = server
name, status = RemoteStatus; `RenderStatus` output contains both).
Commit: `status: remote builder column (#100)`.

**Step 4 -- observe/sync split and sync-on-read.** `observeRemote` /
`reconcileRemote` split; `SyncRemote`; `cmd` wiring. Tests:
`TestSyncRemoteCollectsClosedRoundWithoutDelivery` (after SyncRemote the
report entry exists, pending, and State is not Orphaned; mutation target:
have SyncRemote call reconcileRemote instead and the Orphaned assertion
fails); `TestSyncRemoteSkipsDoneAndLocal`. Commit: `relay: sync remote
bindings on status, pull and wait (#100)`.

**Step 5 -- probes, servers, doctor.** `ProbeServers`, `RenderServers`,
`WithExtraChecks`, cmd wiring. Tests: `TestProbeServersStates` (one
subtest per State via fakeRemote); `TestExtraChecksAppended`. Commit:
`doctor, servers: server probes (#100)`.

**Step 6 -- cosmetics.** §4.4 and the pick entry, each with a one-line
test assertion added to an existing test. Commit: `serve, relay: owner
label in the request log; server pick line (#100)`.

**Step 7 -- README and final.** `make check`. Report: exported names vs
§3/§4; the ui width decision; mutation results from steps 1 and 4;
`changed_paths` complete. Done marker.

## Verification the planner runs

`make check`; scope; mutations from steps 1 and 4; then the same manual
run as plan 3a's (serve + client on this machine) with the client daemon
**not** running: `relay status` alone must collect the closed round and
show the diff facts and the switched candidate.
