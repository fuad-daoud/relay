# Remote builders, plan 3b.3: add --server leaves nothing behind on failure; relay serve unbind (#100)

Round 3 on the same branch. Found on the planner's manual run: an
`add --server` that the server accepted and the client then refused
(round 2's CWD rule, before its fix) left the binding on the server with
no local counterpart. The next `add` with that name got `409` and a hint
(`relay bind --resume`) that cannot work without a local binding, and the
admin had no verb to remove the stale server binding.

**Halt rule for the builder.** If any step below is impossible as written
or contradicts the code you find, stop, write the report saying which step
and why, create the done marker, and do nothing else. Do not rebase or
move this branch.

**Scope guard.** Touch only `internal/relay/remote.go`,
`internal/relay/remote_test.go`, `internal/serve/admin.go`,
`internal/serve/admin_test.go`, `cmd/relay/serve.go`,
`cmd/relay/serve_test.go`, `cmd/relay/main.go` (usage text only),
`README.md` (the serve verb list), and copy this plan to
`docs/plans/2026-09-19-remote-builders-3b3-add-cleanup.md`. Foreground
only, no sub-agents.

## 1. `addRemote` cleanup

After `rt.Remote.CreateBinding` succeeds, every later failure in
`addRemote` (branch exists, `CreateBranch` error, `Save` error including
`store.ErrCWDTaken`, log append error) calls `rt.Remote.Unbind(ctx,
server, name)` best-effort before returning the original error; an unbind
failure is `slog.Warn` and appended to the returned error text as
`; server binding <name> on <server> could not be removed: <err>`. One
`defer` with a `created` flag, not per-branch calls.

The `409` message becomes:
`binding <name> already exists on <server> for this client but not here; relay serve unbind --owner <your label> <name> on the server, or choose another name`
(the label is not known client-side; print `--owner <you>` literally as
`<your label>`).

## 2. `relay serve unbind`

```
relay serve unbind --owner <label|id> <name> [--state <dir>] [--force]
```

`internal/serve/admin.go`:

```
AdminUnbind(ctx, s *Server, owner string, name string, force bool) (UnbindResult, error)
    owner resolves by exact label or exact id via Clients.List(); ambiguous label (two clients share it) -> error
    listing the ids; unknown -> ErrNoSuchClient.
    rt := s.runtime(id); b := rt.Store.Load(name); not found -> store.ErrNotFound.
    RoundStateOf == running && !force -> error "round N is running; wait, or --force"
    relay.Unbind(ctx, rt, name, true)  (archive) -> result
```

`cmd/relay/serve.go`: prints `unbound <label>/<name>` plus `relay.UnbindText`
if that helper fits; exit 1 on refusal. Usage text and README's serve list
gain the verb.

## 3. Steps

**Step 1.** Copy the plan. `remote_test.go`: `TestAddRemoteCleansUpServerOnSaveFailure`
(seed a local binding at the same CWD *without* the remote exemption --
e.g. a local binding whose name differs but whose `CWD` equals `opts.Repo`
and whose builder is a pane; then make the store refuse by using a
`store` seeded so `Save` fails; if that is hard to provoke, use a
`fakeRemote` whose `CreateBinding` succeeds and a `fakeGit.CreateBranch`
returning an error -- the assertion is that `fakeRemote.calls` contains
`Unbind(server, name)` after the failure; mutation target: remove the
deferred cleanup and it fails). Extend `TestAddRemoteServerConflict` for
the new message. Run red, implement §1, run green. Commit: `relay: add
--server removes the server binding when the local half fails (#100)`.

**Step 2.** `admin_test.go`: `TestAdminUnbindByLabelAndId`,
`TestAdminUnbindRefusesRunningUnlessForce`,
`TestAdminUnbindAmbiguousLabel`. Implement §2; CLI; usage; README.
`cmd/relay/serve_test.go`: usage on missing `--owner` (exit 2). Commit:
`serve: relay serve unbind --owner (#100)`.

**Step 3.** `make check` green. Report the mutation result and the
`changed_paths`. Done marker.
