# Plan: `relay send --builder <token>` changes a live binding's builder (#318)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. Halt in
particular if any of these turns out to be false:

- `startRound` (`internal/relay/headless.go` ~136) launches whatever
  `b.BuilderCandidate` names, via `candidate.ParseRef` + `rt.Candidates.Lookup`,
  and reads nothing else that pins the old candidate. `Builder.Kind` is the only
  other field that names the harness.
- `ingest.builderForRound` (`internal/ingest/outcome.go`) attributes a round to
  the token in the **last `KindPick` or `KindSwitch` entry filed under that
  round**, and a `KindSwitch` entry marks the round `OutcomeSwitched` and sets
  its `ClosedAt`. (This is why step 3 logs a `KindPick`, not a `KindSwitch`.)
- The server's `handleStartRound` (`internal/serve/rounds.go`) starts a served
  round through `relay.Send(..., SendOptions{Tier, Defer: true})`, and the
  admitted round is later started by `startRound` on the same binding. So a
  `SendOptions.Builder` applied inside `Send` also takes effect on the server.

Another batch (#352) is editing a comment in `internal/relay/send.go` at the
same time: the `Where` pane form, around line 418. Do not touch that comment.
If you get a merge conflict there, keep their wording.

## 1. System Overview

A planner chooses a builder when a binding is created (`relay add --builder`),
and relay switches builders mid-round on its own when a provider is gated. What
a human cannot do today is say "this binding's **next** round goes to a
different builder". The only way is to unbind and re-add under a new name,
which loses the round history, the one thing you want when you switch because
something went wrong.

This change adds `relay send --builder <token>` (plus a `builder` argument on
the MCP `send` tool).

**The change persists.** The named candidate becomes the binding's builder
(`b.BuilderCandidate`) for this round **and every later one**, until another
`--builder` or a mid-round switch changes it. It is deliberately not a
one-round override like `--tier`:

- The issue's failure was a plain `relay send` silently going to the builder
  the binding was bound to. A one-round override would bring that trap back on
  the round after.
- `b.BuilderCandidate` is the single field that `startRound`, `switchBuilder`,
  `relay status`, `relay policy` and usage attribution all read. A second
  "round builder" field would need a revert at round close and would make a
  mid-round switch ambiguous (switch away from which one?). `RoundTier` needed
  that machinery, and a builder would need more.

Rules:

- **Refused while a round is open.** A round is open when the binding's
  current round has a plan entry and no report entry. The refusal names
  `relay stop <name>` first; since #344 that works for remote bindings too.
- **The token must resolve** to a configured candidate that serves the
  `builder` role. It is resolved with `resolveCandidate` as an explicit
  token, which is exactly `relay add --builder`'s rule.
- **Gated candidates, matching `relay add --builder`:** an explicit pick of a
  rate-limited or spawn-failed candidate **proceeds**. It is recorded as
  "explicit, policy bypassed" with the live gates named in the pick entry.
  `roles_missing` is **refused** (resolveCandidate already does this, #238).
  The CLI prints the pick line, so a bypassed gate is visible at once.
- **Tier:** the binding's stored tier `b.Tier` is re-derived for the new
  candidate with the normal chain, `resolveTier("", newCandidate, policy, "builder")`,
  and checked with `checkTierCap`. If `--tier` is also passed, it stays the
  one-round override (`RoundTier`) exactly as today. Consequence to document:
  a tier set explicitly at `relay add --tier` time is replaced by the new
  candidate's chain. Pass `--tier` again if you want it for this round.
- **Same builder:** `--builder` naming the binding's current candidate (after
  canonicalising the ref) is a no-op. There is no pick entry and no tier
  change, and the send proceeds normally.
- **Remote bindings:** the token travels as a new optional multipart field
  `candidate` on `StartRound`. The server advertises a new
  `FeatureBuilder = "builder"` token. The client refuses against a server
  without it **before uploading anything**. The server validates and applies
  the change through the same `Send` code, so its ledger and its candidates
  decide. The client records the pick and its own `BuilderCandidate`, so the
  next `observeRemote` sees `view.Candidate == b.BuilderCandidate` and writes
  no spurious "switched on <server>" entry.

Out of scope: a `relay rebuilder` verb; changing the builder of a round that
is already running (that is `relay unavailable` + switch, or `relay stop`
then `send --builder`); `relay bind --resume --rebind`; any change to
`relay policy`'s `<- would pick` wording.

## 2. File Structure

```
internal/relay/send.go            MODIFY  SendOptions.Builder; preflight resolution; in-lock apply; SendResult.Pick
internal/relay/builder_change.go  CREATE  ErrBadBuilder, ResolveSendBuilder, applyBuilder, roundOpenIn
internal/relay/builder_change_test.go CREATE  pure-function tests
internal/relay/remote.go          MODIFY  sendRemote: feature probe, candidate field, client-side record; remotePickEntry gains round
internal/relay/runtime.go         MODIFY  RemoteClient.StartRound gains candidate param
internal/remote/proto.go          MODIFY  FeatureBuilder
internal/remote/client/client.go  MODIFY  StartRound writes the "candidate" field
internal/serve/rounds.go          MODIFY  read "candidate", validate before absorb, pass SendOptions.Builder, map ErrBadBuilder
internal/serve/routes.go          MODIFY  FeatureBuilder in WhoAmI
internal/mcp/tools.go             MODIFY  SendArgs.Builder + schema property
internal/mcp/verbs.go             MODIFY  pass Builder into SendOptions
cmd/relay/main.go                 MODIFY  --builder flag on send; print res.Pick
internal/relay/send_test.go       MODIFY  local send --builder tests
internal/relay/remote_test.go     MODIFY  fake StartRound signature; remote --builder tests
internal/serve/serve_test.go      MODIFY  wire tests; WhoAmI features now 4
internal/mcp/*_test.go            MODIFY  only if a test pins the send schema's property set
README.md                         MODIFY  send section (~1494): --builder paragraph
```

Every other implementation of `relay.RemoteClient.StartRound` (search for
`) StartRound(ctx context.Context`) gets the new parameter.

## 3. Data Structures & Type Definitions

### `internal/relay/send.go`

- `SendOptions.Builder string`. "" means the binding's current builder.
  Otherwise it is a candidate token that **persists** as the binding's builder
  from this round on. Doc comment says "persists", in contrast to `Tier`.
- `SendResult.Pick string`: the pick line when `Builder` changed the builder
  (the `KindPick` note), or "" otherwise. For the CLI to print.
- `preflight` gains `pick *Resolution`: set when `opts.Builder` names a
  different candidate than the binding's current one, nil otherwise. Local
  bindings only. For a remote binding the server resolves.

### `internal/relay/builder_change.go` (new)

- `var ErrBadBuilder = errors.New("send --builder")`. It wraps every
  refusal of the token itself (unknown, bad ref, role not served,
  roles_missing, tier above the cap). It exists so the server can map these to
  422.

### `internal/remote/proto.go`

- `const FeatureBuilder = "builder"`: the `WhoAmI.Features` token a server
  that accepts the `candidate` field on `POST …/rounds` advertises (#318).
- The wire field is the multipart form value `"candidate"` on
  `POST /v1/bindings/{name}/rounds`: a canonical candidate token. Absent or ""
  means keep the binding's builder. Document it on the `TagRef` comment's
  sibling spot, or on `FeatureBuilder`.

### Exact strings (the contract)

| Where | Text (fmt verbs in order) |
|---|---|
| round open, local and remote | `"binding %q has round %d open; relay stop %s ends it, then send again with --builder"` with `name, b.Round, name` |
| bad token (wraps) | `fmt.Errorf("%w %s: %w", ErrBadBuilder, token, cause)` → e.g. `send --builder x/y/z: candidate "x/y/z" not found (configured: …): unknown candidate` |
| pre-builder server | `"server %s cannot change a binding's builder (no %q feature); upgrade it, or send without --builder"` with `server, remote.FeatureBuilder` |
| local pick entry note | `ExplainResolution("builder", res)`, unchanged. It reads `picked <tok> for builder: explicit, policy bypassed[; gates…]` |
| remote pick entry note | `remotePickEntry`'s existing `"picked %s on %s: %s"` with how = `"explicit"` |
| builder log marker (local) | `appendLogMarker(logPath, now, "builder changed to "+tok+" (send --builder)")` before `startRound` |
| README | one paragraph, see step 7 |

## 4. Interface Definitions & Component Contracts

### `ResolveSendBuilder(rt Runtime, current, token string) (*Resolution, error)` (exported, `builder_change.go`)

- Responsibility: turn `--builder`'s token into the resolution to apply, or
  nil for a no-op.
- It calls `resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), token, "builder")`.
  Any error is returned as `fmt.Errorf("%w %s: %w", ErrBadBuilder, token, err)`.
- If `res.Token()` equals `current` (the canonical form), it returns `nil, nil`.
- Otherwise it returns `&res`, which has `How == HowExplicit` and any live
  gates in `res.Gates`.
- Precondition: `token != ""`. It is a pure read of the ledger and candidates.
  It is used by `sendPreflight`, and by `handleStartRound` before absorbing a
  bundle.

### `applyBuilder(b store.Binding, res Resolution, pol policy.Policy, allowYolo bool) (store.Binding, error)` (unexported, pure)

- It sets `b.BuilderCandidate = res.Token()` and `b.Builder.Kind = res.Candidate.Harness`.
  It leaves `Builder.Mode`, `AgentName` and `Server` as they are.
- It sets `b.Tier = string(resolveTier("", res.Candidate, pol, "builder"))`.
- If `checkTierCap(thatTier, pol, allowYolo)` fails, it returns that error
  wrapped with `ErrBadBuilder` (same format as above) and leaves the binding
  unchanged.
- It clears `b.RoundExcluded`. An exclusion list belongs to the old builder's
  round, and a fresh round would clear it anyway.

### `roundOpenIn(entries []store.LogEntry, round int) bool` (unexported, pure)

`HasEntry(entries, round, DirToBuilder, KindPlan) && !HasEntry(entries, round, DirToPlanner, KindReport)`.
Use it for the `--builder` refusal only. Do not refactor the existing inline
copies of this condition.

### `RemoteClient.StartRound` (changed)

```
StartRound(ctx, server, name string, round int, plan []byte, bundle io.Reader, tier, candidate string, tags []remote.TagRef) (remote.BindingView, error)
```

`(*client.Client).StartRound` writes `mw.WriteField("candidate", candidate)`
only when `candidate != ""`, right after `tier`.

### `handleStartRound` (changed)

- Read `candidate := r.FormValue("candidate")`.
- Under the first `s.mu` section, after the `RoundRunning` 409 and before
  absorbing the bundle: if `candidate != ""`, call
  `relay.ResolveSendBuilder(rt, b.BuilderCandidate, candidate)`. On error,
  respond 422 with `CodeInvalid` and `err.Error()`. This refuses before any
  ref moves.
- Pass `relay.SendOptions{Tier: tierStr, Builder: candidate, Defer: true}`.
- In the `sendErr` mapping, add
  `errors.Is(sendErr, relay.ErrBadBuilder) → 422 CodeInvalid` before the
  generic 500. It is a backstop if the ledger changed between the two checks.
- `routes.go` features: `[FeatureTier, FeatureQueue, FeatureStop, FeatureBuilder]`.

### CLI and MCP

- `cmd/relay/main.go` `cmdSend`:
  `builder := fs.String("builder", "", "candidate harness/provider/model to run this round and later ones on; refused while a round is open")`.
  Pass it as `SendOptions.Builder`. After a successful send, print `res.Pick`
  on its own line before `sent round …` when it is not "". The dry run needs
  no change, because it prints `pf.b.BuilderCandidate`, which preflight
  substitutes (§5.1).
- MCP: add `SendArgs.Builder string \`json:"builder,omitempty"\`` and the
  schema property
  `"builder": {"type":"string","description":"candidate token to run this round and later ones on (persists); refused while a round is open"}`.
  `RelayVerbs.Send` passes it through. This is a direct mirror of `tier`.

## 5. High-Level Pseudocode

### 5.1 `sendPreflight` (local and remote), after the binding loads and the paused check

```
if opts.Builder != "":
    entries = rt.Store.ReadLog(name)                 // read-only
    if roundOpenIn(entries, b.Round): return round-open refusal
    if b.Builder.Remote():
        candidate.ParseRef(opts.Builder) must succeed, else ErrBadBuilder-wrapped error
        // no local resolution: the server's candidates decide
    else:
        pick = ResolveSendBuilder(rt, b.BuilderCandidate, opts.Builder)   // error → return it
        if pick != nil:
            b2 = applyBuilder(b, *pick, rt.Policy, opts.AllowYolo)       // error → return it
            b = b2                    // in-memory only: preflight never saves
            if opts.Tier == "": tier = effectiveTier(b)   // re-derived tier drives argv
            pf.pick = pick
```

After this block the existing preflight runs unchanged against the
substituted `b`: `composePrompt`, the candidate lookup, `headlessLaunch` argv
and the advisory gate lookup. The dry run therefore shows the new candidate,
its argv and its gate note with no other change. Order matters: the tier
computed earlier from `opts.Tier` still wins when set.

### 5.2 `Send`, local, inside `WithLock`, after the existing broken/paused/cap/busy checks

```
if pf.pick != nil:
    re-check: entries = tx.ReadLog(name); roundOpenIn → round-open refusal (nothing written)
    b = applyBuilder(b, *pf.pick, rt.Policy, opts.AllowYolo)       // error → return it
(existing: RoundTier from opts.Tier, stage plan)
if pf.pick != nil and not deferred:
    appendLogMarker(BuilderLogPath(name, b.Round), now, "builder changed to <tok> (send --builder)")
(existing: startRound unless deferred; ErrTierUnsupported/ErrExtraArgsPermission → return, nothing saved or logged;
 other spawn errors → the existing NEEDS YOU save)
if pf.pick != nil:
    tx.AppendLog(name, pickEntry(now, b.Round, "builder", *pf.pick))   // before the plan entry
    result.Pick = ExplainResolution("builder", *pf.pick)
(existing: plan entry, drift, …)
```

On the NEEDS YOU spawn-failure path the binding is saved inside the existing
branch and `Send` returns the error. Append the pick entry there too, before
that save, so the log explains why the builder changed.

- If `startRound` fails, the existing NEEDS YOU path saves the binding **with
  the new builder**. That is intended: the human asked for it, and the ledger's
  spawn_failed gate explains it.
- The `ErrTierUnsupported` / `ErrExtraArgsPermission` early return saves
  nothing, so the builder change is not persisted and no pick entry is
  written. That is why the pick entry comes after `startRound`.
- The pick entry is filed under the new round, before its plan entry, so
  `builderForRound` attributes the round to the new builder.

### 5.3 `sendRemote`

```
if builder != "":
    who = rt.Remote.WhoAmI(server)        // one call shared with the tier probe when both are set
    FeatureBuilder not in who.Features → pre-builder-server refusal (nothing shipped)
… existing snapshot, tags …
view = rt.Remote.StartRound(..., tier, builder, tags)
  (existing error mapping; the 422 branch already renders "<server>: <message>")
under the lock, in the existing success block:
    if builder != "" and view.Candidate != "" and view.Candidate != cur.BuilderCandidate:
        AppendLog(remotePickEntry(now, server, view.Candidate, true, cur.Round))
        cur.BuilderCandidate = view.Candidate
        cur.Builder.Kind = harness of ParseRef(view.Candidate) ("" on a parse error)
```

- `sendRemote` gains a `builder string` parameter. `Send` passes
  `opts.Builder`.
- `remotePickEntry` gains a trailing `round int` parameter. `addRemote`
  passes `1`.
- Use `view.Candidate`, not the typed token: the server canonicalises it.
- `SendResult.Pick` is set to that entry's note.

### 5.4 Server `handleStartRound`

```
parse form; candidate = FormValue("candidate")
lock; load; owner check; RoundRunning → 409 (unchanged)
if candidate != "": ResolveSendBuilder(rt, b.BuilderCandidate, candidate) error → 422 invalid (unlock first)
… unchanged absorb / tags / worktree …
relay.Send(..., SendOptions{Tier, Builder: candidate, Defer: true})
  ErrBadBuilder → 422 invalid
… unchanged queue entry, admit, 201 with ServedView (Candidate = new token) …
```

The server's own `Send` writes the server-side pick entry (the §5.2 path,
since the served binding is local to the server). With `Defer: true` there is
no `startRound` and no builder-log marker. The pick entry is still appended
before the plan entry.

## 6. Error Handling Strategy

- **Refusals change nothing:** round open, a bad token, roles_missing, a tier
  above the cap, a pre-builder server. Each returns before any plan is staged,
  any log entry is written or any bundle is shipped. The re-check under the
  lock covers a round that opened between preflight and the lock.
- **Server-side:** a bad token is 422 `invalid` with the resolver's message,
  and the client renders it as `<server>: <message>` (the existing 422
  branch).
- **Gated candidates are not errors:** they are recorded in the pick note
  and printed.
- **Logging:** only the pick entry and the builder-log marker. No new slog
  lines.

## 7. Ordered Implementation Steps

Run `make check` after each step. Every test goes in `internal/relay`,
`internal/serve`, `internal/remote/client` or `internal/mcp`. **Add no test
in `cmd/relay`.** CI runners have no harness binary and no network, and no
`cmd/relay` test may execute a send.

1. **Pure helpers.** Create `builder_change.go` with `ErrBadBuilder`,
   `ResolveSendBuilder`, `applyBuilder` and `roundOpenIn`.
   *Verify* with `builder_change_test.go`:
   - An unknown token gives an error wrapping both `ErrBadBuilder` and
     `candidate.ErrUnknownCandidate`.
   - A roles_missing gate is refused.
   - A rate-limited gate resolves, with `res.Gates` non-empty.
   - The current token (including a non-canonical spelling of it) gives nil.
   - `applyBuilder` sets the candidate, Kind and re-derived Tier, and clears
     `RoundExcluded`.
   - `applyBuilder` with a candidate tier above `max_tier` and no allowYolo
     gives an error, with the binding unchanged.
   - `roundOpenIn` covers all four plan/report combinations.

2. **Local send.** Add `SendOptions.Builder`, `SendResult.Pick`,
   `preflight.pick`, and §5.1 / §5.2 in `send.go`.
   *Verify* in `send_test.go`, using the existing fake runner/candidates
   setup of `TestSendHeadlessTierYoloOverrideAndRoundClose`:
   - (a) A send with `Builder: other` starts the process with the **other**
     candidate's argv. The saved binding has `BuilderCandidate == other`. A
     `KindPick` entry for the round precedes the plan entry. The **next**
     plain send also uses `other`, which pins the persistence.
   - (b) An open round gives the round-open refusal, with no plan staged
     and no entries added.
   - (c) An unknown token gives `ErrBadBuilder`, with nothing written.
   - (d) A gated token proceeds, and `res.Pick` contains `policy bypassed`.
   - (e) `SendDryRun` with `Builder: other` reports `Candidate == other` and
     makes no writes.
   - (f) `Builder` equal to the current token behaves exactly like a plain
     send, with no pick entry.

   *Mutation (required):* make `applyBuilder`'s result stay in preflight only,
   i.e. drop the in-lock `applyBuilder`. Confirm (a) fails on the
   persistence assertion. Restore it.

3. **Wire vocabulary.** Add `FeatureBuilder` and the `StartRound` `candidate`
   parameter on the interface, the client and every fake.
   *Verify:* `make check` is green, and all existing remote tests pass
   unchanged.

4. **Server.** Change `handleStartRound` per §5.4, and add `FeatureBuilder`
   to WhoAmI.
   *Verify* in `serve_test.go`:
   - A `rounds` POST with `candidate` set to a second configured candidate
     returns 201, and the view's `candidate` is that token. The server
     binding's `BuilderCandidate` changed, and its log has a `KindPick` for
     the round.
   - An unknown candidate returns 422 `invalid`, and the server's
     `refs/relay/<name>/out` did **not** move. That pins validation before
     absorb.
   - `TestWhoAmI` now expects four features.

5. **Client remote send.** Implement §5.3.
   *Verify* in `remote_test.go` with the fake client:
   - (a) The fake advertises `builder` and returns a view with the new
     candidate. The fake saw `candidate == token`. The client binding's
     `BuilderCandidate` and `Builder.Kind` updated. A `KindPick` entry was
     filed under the sent round. A following `observeRemote` with the same
     view writes **no** `KindSwitch`.
   - (b) The fake lacks `builder`: the pre-builder refusal, and `StartRound`
     was never called and no snapshot was taken.
   - (c) The client binding has an open round: the round-open refusal, with
     no server call.

6. **CLI and MCP.** Add the `--builder` flag and print `res.Pick`. Add the MCP
   `SendArgs.Builder` and the schema property.
   *Verify:* if an `internal/mcp` test pins the send schema's properties,
   extend it. Otherwise add a small test that `Tools()`'s send schema has a
   `builder` property and that `RelayVerbs.Send` passes it into
   `SendOptions`, through a fake if one exists. If neither is feasible
   without executing a send, skip it and say so.

7. **README.** Next to the `--tier` override paragraph (~line 1494), add:
   `relay send --builder <token>` moves the binding to another configured
   candidate from this round on. It is refused while a round is open (stop it
   first with `relay stop`). An explicit pick of a gated candidate is recorded
   and proceeds, as with `relay add --builder`. The binding's tier is
   re-derived for the new candidate. On a remote binding the server must
   advertise the `builder` feature.
   *Verify:* `make check` is green.

Report: the files touched per step, the mutation check from step 2 (what you
broke and which assertion failed), any "skip and say so" you used in step 6,
and whether the §5.2 ordering (pick entry after `startRound`) needed any
adjustment.
