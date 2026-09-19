# Remote builders, plan 4 of 4: the scripted end-to-end test (#100)

Spec: `docs/specs/2026-09-19-remote-builders-design.md` (in this tree)
§8 (testing: "End-to-end, local-only"). Plans 1–3b are merged (#206,
#207, #208, #210, #212). Everything the planner verified by hand -- a
server and a client on one machine, a round through the wire, a closed
round collected while the client was away -- becomes one Go test package
that runs in `make check` with no herdr, no network beyond 127.0.0.1, and
no real harness.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. Do not rebase or move this branch.

**Scope guard.** Touch only the files in §2. No module dependency. Do not
run `make e2e`. Foreground only; no sub-agents. The new package must not
import `internal/relay`'s test files (they are unexported); it defines its
own fakes.

**Git in tests.** Same helper discipline as before (`GIT_CONFIG_GLOBAL=/dev/null`,
fixed identity); copy, do not import.

**Commit the plan with the work.** Copy this plan file (the path relay gave
you) to `docs/plans/2026-09-19-remote-builders-4-e2e.md` in your worktree
and include it in the first commit.

**Check command.** `make check`; green before every commit. The new tests
are part of it (no build tag): they take real git and a loopback TLS
listener, nothing else.

**Report discipline.** `changed_paths` names every file the diff touches.

## 1. System overview

`internal/e2e` is an external test package (`package e2e`, test files
only) so it can import `internal/relay`, `internal/serve`,
`internal/remote/client` and `internal/store` together without the import
cycle an in-package test in `relay` would hit. It builds:

- a **server**: `serve.InitTLS` in a temp root, one enrolled client,
  `serve.New` with a `scriptRunner` whose `Start` records the `ProcSpec`
  and whose "process" is the test itself writing the round's report and
  marker into the server's store and committing to the server worktree;
  `ListenAndServe` on `127.0.0.1:0` in a goroutine, cancelled at the end;
- a **client**: a `relay.Runtime` over a temp `store.Store`, a real
  `git.Client`, a real `client.Client` pinned to the server's fingerprint,
  `remote.NewBundleTransport`, and a `fakeHerdr` whose `ListAgents`
  returns one idle planner agent in pane `p1` and whose `Prompt` records
  what was typed into it;
- a **repo**: a temp git repo with one commit, the client's `Repo`.

Ticks are explicit: the test calls `server.Tick(ctx)` and
`relay.NewDaemon(clientRT, interval).Tick(ctx)` itself, in the order each
scenario needs. Nothing sleeps except for a bounded poll where the
server's process state has to settle.

## 2. File structure

```
internal/e2e/
  remote_test.go       harness (newServer, newClient, newRepo, tickUntil) and the three scenarios
  fakes_test.go        fakeHerdr (relay.Herdr), scriptRunner (relay.Runner), runGit helper

docs/plans/2026-09-19-remote-builders-4-e2e.md
```

Nothing outside `internal/e2e` changes. If a scenario cannot be written
without touching production code, that is a halt with the reason -- it
means a seam is missing, and the planner wants to know which.

## 3. Fakes

```
type fakeHerdr struct {
    mu      sync.Mutex
    agents  []herdr.Agent          // one planner: {PaneID: "p1", Name: "planner", Status: idle, Kind: "claude"}
    prompts []struct{ Target, Text string }
}
ListAgents -> agents; Prompt -> append; Notify -> append to a notices slice; every other method -> error "not in e2e"
    (look at internal/relay/herdr.go's Herdr interface for the exact method set)

type scriptRunner struct {
    mu     sync.Mutex
    specs  []relay.ProcSpec
    alive  bool
}
Start -> record spec, alive = true, return ProcHandle{PID: 4242, StartedAt: time.Now()}
Alive -> alive; ExitCode -> (0, !alive); Kill -> alive = false
```

The "builder" is `finishRound(t, serverRT, name, round, commitMsg)`: writes
a report file (with a `relay` block: `status: done`, `changed_paths:
[hello.txt]`) at the server store's `ReportPath`, commits `hello.txt` in
the server worktree via `runGit` (`git add`, `git commit -m <msg>`), sets
`runner.alive = false`, then writes the done marker at `DonePath`. Marker
last, as a real builder does.

## 4. Harness

```
newServer(t) (srv *serve.Server, url, fingerprint string, enroll func(pub string) remote.ClientID, srvStore func(owner) *store.Store, runner *scriptRunner)
    root := t.TempDir()/serve; serve.InitTLS(root, ["localhost","127.0.0.1"], now)
    clients file at root/clients.json (serve.LoadClients then Add)
    cfg := serve.Config{Root: root, Candidates: a one-candidate set built in the test (look at internal/candidate for the
           constructor the serve tests use), Policy: zero, Runner: runner, Git: git.NewClient("git", 10*time.Second, 0), Now: time.Now,
           Interval: time.Second, MaxBundleBytes: 64 << 20}
    srv := serve.New(cfg); go srv.ListenAndServe(ctx, {Addr: "127.0.0.1:0", TLS: loaded cert}); wait for srv.Addr() non-nil
    t.Cleanup cancels ctx and waits for ListenAndServe to return.

newClient(t, url, fingerprint) (rt relay.Runtime, hd *fakeHerdr, kp remote.Keypair)
    cfgDir := t.TempDir(); kp via client.InitKey(client.KeyPaths(cfgDir)); servers := {"zen": {URL: url, Fingerprint: fp}}
    rt := relay.Runtime{Herdr: hd, Git: gitClient, Store: store.New(t.TempDir()), Candidates: same set, Now: time.Now,
                        Remote: client.New(servers, kp, time.Now), Transport: remote.NewBundleTransport(gitClient, "")}
    -- look at cmd/relay/main.go's newRuntime for any other field a round needs (LedgerPath, HistoryPath: temp files)

newRepo(t) string        // git init, one commit "init" with README.md

tickUntil(t, deadline time.Duration, step func() bool)   // calls step every 50ms until it returns true or the deadline; t.Fatal on deadline
```

## 5. Scenarios

**`TestRemoteRoundEndToEnd`**
1. server, client (enrolled), repo. `relay.Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: repo, PlannerPane: "p1"})`.
   Assert: local branch `relay/api` exists at the repo's HEAD; server store for the owner has `api`.
2. `relay.Send(ctx, rt, "api", planFile)` with a one-line plan.
   Assert: `runner.specs` has one entry whose `Dir` is the server worktree; the worktree has `README.md`;
   client binding `Builder.LastShipped` == repo HEAD.
3. `finishRound(...)`; `srv.Tick`; then `tickUntil` the client daemon's `Tick` leaves a `KindReport` entry for round 1.
   Assert: `hd.prompts` has exactly one entry for `p1` whose text contains `Report:` and `Diff: 1 file` and
   `1 commit on relay/api`; the client repo's `relay/api` is one commit ahead of `init` with `hello.txt`;
   the server view (via `rt.Remote.GetBinding`) has `acked_round == 1` and `round_state == idle`;
   client `Builder.RemoteStatus == "idle"`; `BuilderCandidate` equals the server's candidate token.
4. `relay.Done(ctx, rt, "api")`. Assert: server view state `done`; client state done.

**`TestRemoteRoundCollectedAfterClientWasAway`**
Same as above through `Send`, then `finishRound` and **several** `srv.Tick`s with **no** client ticks
(the laptop is asleep). Assert the server view is `closed` with `acked_round 0`. Then client ticks:
exactly one prompt to the planner, `acked_round == 1`, and a second batch of client ticks adds no
second report entry and no second prompt (idempotence).

**`TestRemoteServerUnreachableIsNotAHalt`**
Through `Send`; cancel the server's context (listener down); client `Tick`.
Assert: state stays active, `Builder.RemoteStatus == "unreachable"`, `RemoteUnreachableSince` set,
no notice from `fakeHerdr.Notify`. Restart a server? Not needed: the assertion is the non-halt.

**`TestRemoteSyncOnReadWithoutDaemon`**
Through `Send` and `finishRound` and `srv.Tick`; then `relay.SyncRemote(ctx, rt)` (no daemon tick).
Assert: report entry exists and is pending (not delivered: `hd.prompts` empty), state is not orphaned;
then one daemon `Tick` delivers it (one prompt).

## 6. Error handling

Every assertion names the scenario step in its message (`step 3: prompts = %d, want 1`).
`tickUntil` deadlines are 10s; the tests must finish in well under a minute together.

## 7. Ordered implementation steps

**Step 1 -- plan, fakes, harness, first scenario.** §2–§4 and
`TestRemoteRoundEndToEnd`. Commit: `e2e: remote round end to end, in
process (#100)`.

**Step 2 -- the other three scenarios.** Commit: `e2e: away, unreachable,
sync-on-read (#100)`.

**Step 3 -- final.** `make check` (the package runs inside it; confirm in
the output that `internal/e2e` ran). Report: the four test names with
their wall time, any seam you needed and did not have, `changed_paths`.
Done marker.

## Verification the planner runs

`make check`; `go test ./internal/e2e -count=3` (no flake); mutation:
comment out the `Ack` call in `catchUp` and `TestRemoteRoundCollectedAfterClientWasAway`
must fail on `acked_round`.
