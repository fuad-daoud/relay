# Remote builder permission tier

Status: design note, not yet implemented. Written before the change so the
protocol shape and the v1-server story are agreed first.

## Problem

Two layers, and the first one alone explains the hang.

**A. The server never resolves a tier.** `internal/serve` contains no
`Tier` at all. `handleCreateBinding` (`bindings.go`) stores the binding
with `Tier: ""`, and every launch (`headless.go:95`) and every mid-round
switch (`switch.go:149`) reads `effectiveTier(b)`, which is `RoundTier`,
else `Tier`, else `harness`. Locally, `add`/`bind`/`fork` fill `b.Tier`
from `resolveTier(explicit, candidate, policy, "builder")` -- explicit,
then the candidate's `tier`, then policy `tier.builder`, then `harness`.
The server skips that step entirely, so on a server `policy.json`
`tier.builder` and a candidate's `tier` field are dead config. Reproduced
on v0.3.0-9-g722e715 with the box policy
`{"tier":{"builder":"yolo"},"max_tier":"yolo"}`: `claude -p` launched
with no `--dangerously-skip-permissions`, every tool call denied, round
hung until unbound. Only the candidate's `extra_args` reach the launch.

**B. Nothing on the wire carries the planner's tier.** `relay send --tier
yolo --allow-yolo` against a `--server` binding is accepted by the client
(it passes `checkTierCap` against the client's policy) and then dropped:
`Send` routes to `sendRemote` before it looks at `opts.Tier`; `StartRound`
sends only `round`, `plan`, `bundle`; `handleStartRound` calls
`relay.Send(..., SendOptions{})`. `CreateBindingRequest` has no tier
either. And nothing tells the planner what tier the server will use.

A fixes the reported hang by itself and needs no protocol change. B is
what makes `--tier` mean something across a server and is the part that
needs a compatibility story. They are independent; A can ship first.

## Goals

0. The server resolves a tier for every binding it creates exactly as
   `add.go` does locally, from *its* candidates and policy, and stores it
   in `b.Tier` so launch and switch honour it. `relay serve` logs the
   resolved builder tier at startup.
1. A tier chosen on the client reaches the server and is what the builder
   launches at, or the client is told -- before anything starts -- that it
   cannot be.
2. The server's `max_tier` is a hard ceiling. A request above it is refused
   with a code and message that name the ceiling; there is no wire flag
   that lifts it. (`--allow-yolo` stays a client-side gate on the client's
   own policy.)
3. `relay add --server` prints the tier the server will actually use.
4. `relay servers` and `relay doctor` warn when a server would launch a
   headless builder at `harness`.
5. A new client works against a v1 server that predates this note, and an
   old client works against a new server, without a protocol version bump.

## Non-goals

- Changing how a *local* headless binding resolves its tier. The local
  chain and its `harness` default are unchanged.
- Detecting a hung permission-denied round on the server. #192-style
  denial detection (`headless.go`, "permission denial") fires on process
  exit; a `claude -p` that blocks instead of exiting is a separate bug.
- Pane builders on the server. Served builders are headless only.

## Part A: server-side resolution (no wire change)

```
// ResolveServedTier is what handleCreateBinding and handleStartRound use.
// token is the candidate PickServedCandidate chose ("" -> policy pick);
// explicit is the wire tier ("" -> none). Chain and cap are resolveTier and
// checkTierCap with allowYolo=false: the server's max_tier is the ceiling
// and nothing on the wire lifts it.
// Errors: ErrTierAboveMax (explicit above max_tier); a malformed explicit
// tier is harness.ParseTier's error.
func ResolveServedTier(rt Runtime, token, explicit string) (harness.Tier, error)   // internal/relay/served.go
```

`handleCreateBinding`: after `PickServedCandidate`, `tier :=
ResolveServedTier(rt, candidateToken, req.Tier)`; store `Tier:
string(tier)`. With `req.Tier == ""` (every client today) this is the
server's own chain -- candidate `tier`, then policy `tier.builder`, then
`harness` -- which is the bug fix on its own. Policy reload (#209) does
not retroactively change an existing binding's `Tier`, same as locally.

"Per round" is the local semantics too: `Tier` is fixed at create,
`RoundTier` is the one-round override `Send --tier` writes and
`queueReport` clears. The server gets the same two, nothing more.

Test for A (`internal/e2e`): a server whose `policy.json` has
`tier.builder: yolo` and a `claude` candidate; `relay.Add --server`, then
`relay.Send`; assert `runner.specs[0].Argv` contains
`--dangerously-skip-permissions` (`harness.PermissionArgs(claude, yolo)`).
The same test with the policy tier unset asserts the flag is absent, so
the default is pinned as `harness`, not silently `yolo`. `scriptRunner`
already records the `ProcSpec`.

## Part B: wire changes (all additive; `remote.Version` stays 1)

### `WhoAmI` (GET /v1/whoami)

```
Features    []string  // new; contains "tier" when the server honours the fields below
BuilderTier string    // new; the tier the server resolves for a headless builder
                      //      with no explicit tier: policy tier.builder, else "harness"
MaxTier     string    // new; policy.MaxTierOrDefault()
```

`Features` is the capability signal. A server that omits it is a v1 server
from before this note.

### `CreateBindingRequest` (POST /v1/bindings)

```
Tier string `json:"tier,omitempty"`  // new; "" means "server's choice"
```

Server: `ResolveServedTier(rt, candidateToken, req.Tier)` (Part A).
Malformed → 400 `invalid`. Above the ceiling →
**422 `tier_above_max`** with message
`tier yolo exceeds this server's max_tier edit; raise max_tier in the
server's policy.json`. Otherwise `b.Tier = string(tier)` is stored exactly
as `Add` stores it locally. `Tier == ""` keeps today's chain, so old
clients see no change.

### `BindingView` (every binding response)

```
Tier string `json:"tier,omitempty"`  // new; effectiveTier(b) on the server
```

This is what `relay add --server` prints ("tier: edit (server)") and what
`relay status` shows on a remote binding. It is also the post-hoc check:
a client that asked for a tier and gets a view without one knows the
server ignored it.

### `StartRound` multipart (POST /v1/bindings/{name}/rounds)

New optional form field `tier`. Server: parse as above, then
`relay.Send(..., SendOptions{Tier: tier})`. `Send` already refuses above
the cap with `ErrTierAboveMax`; `handleStartRound` maps it to
**422 `tier_above_max`** and does not open the round. The
one-round-override semantics (`RoundTier`, cleared by `queueReport`) come
for free because served bindings are headless.

### New error code

```
CodeTierAboveMax Code = "tier_above_max"   // 422
```

## Client changes

- `AddOptions.Tier` / `AllowYolo` already exist. `addRemote` keeps the
  client-side `checkTierCap` (client policy, `--allow-yolo`) and then
  puts `string(tier)` in `CreateBindingRequest.Tier` **only when the user
  passed `--tier`**; otherwise `""` so the server's chain applies. After
  the create, print `tier: <view.Tier> (server)`, or
  `tier: server's choice (pre-tier server)` when the view has none.
- `SendOptions.Tier` for a remote binding: forwarded as the `tier` form
  field. `sendRemote` gains the tier parameter.
- **Capability check before side effects.** When `--tier` is given on
  `add --server` or `send` to a remote binding, the client calls `WhoAmI`
  first and refuses if `Features` lacks `"tier"`:
  `server contabo does not carry a permission tier (pre-tier server);
  upgrade it or drop --tier`. This is the only extra request and it only
  happens when a tier is requested. Without `--tier` no probe is added.
- `ServerProbe` gains `BuilderTier`, `MaxTier`, `TierAware bool` from
  `WhoAmI`. `RenderServers` appends `builder tier: harness` and, when it
  is `harness`, the warning line
  `!! headless builders at tier harness deny every tool unless the server
  host's harness settings allow them; set tier.builder in the server's
  policy.json or pass --tier`.
- `relay doctor` (`internal/doctor`, servers section): one `warn` check
  per configured server whose `BuilderTier == harness`, same text; `info`
  when the server is pre-tier ("cannot see the server's tier").

## Compatibility matrix

| client | server | `--tier` given | behaviour |
|---|---|---|---|
| new | new | yes | tier on the wire, capped by server `max_tier`, echoed in view |
| new | new | no | server chain (unchanged); view shows what it picked |
| new | v1 (pre-tier) | yes | refused before create/start: pre-tier server |
| new | v1 (pre-tier) | no | works as today; `add` prints "server's choice (pre-tier server)"; `servers`/`doctor` say the tier is unknown |
| old | new | -- | no tier fields sent; server chain; extra JSON fields ignored by the old client |

No version bump: every field is optional and ignored-if-absent on both
sides, and the one dangerous case (new client asks, old server ignores)
is caught by `Features` before anything is created. Bumping to v2 would
lock old clients out of servers that still serve them correctly.

## Default on the server: leave `harness`, warn loudly

The hang in the report happened at the server's default. Changing the
served default to `edit` was considered and rejected: it would make a
served builder more permissive than a local one with the same
`policy.json`, and the operator who mirrored the client's config would
not expect the two to differ. Instead the default stays and three
surfaces say so: `relay add --server` output, `relay servers`, and
`relay doctor`. `relay serve` itself logs one Warn at startup when its
resolved builder tier is `harness` (`serve.go`, after policy load).

## Tests (to be planned when this is implemented)

- Part A's two e2e cases above, plus `ResolveServedTier` as a table test
  in `internal/relay/served_test.go` (explicit > candidate > policy >
  harness; above-cap refused; allowYolo never consulted).
- `internal/serve`: create with tier below/at/above the ceiling; start
  round with tier; view echoes `Tier`; `WhoAmI` carries `Features`,
  `BuilderTier`, `MaxTier`.
- `internal/relay`: `addRemote`/`sendRemote` refuse on a `WhoAmI` without
  `"tier"` and send nothing; `RenderServers` warning line; doctor check
  as a pure function (no herdr, CI has none).
- `internal/e2e`: one round with `--tier edit` and a scripted runner that
  asserts the spec's argv carries the `edit` permission flags of the
  candidate's harness.

## Open questions

- Should `tier_above_max` be 403 rather than 422? 422 matches
  `not_fast_forward` (a well-formed request the server's state refuses);
  403 would read as an auth failure in the request log. Leaning 422.
- Whether `BuilderTier` in `WhoAmI` should be per-candidate (candidate
  `tier` field can differ). First cut: resolve for the policy's pick;
  `relay candidates --server` can show per-candidate later.
