# Remote builder permission tier: resolve on the server, carry over the wire

Spec: `docs/specs/2026-09-19-remote-tier-design.md` (read it first; this plan
implements Part A then Part B of it). If any step below is impossible as
written or contradicts the code you find, **stop and report** -- do not
improvise around it. Steps 1-4 (Part A) are complete and shippable on their
own; a halt in steps 5-12 must still leave 1-4 green and say so.

Other builders are working on this repo in parallel. Keep your edits to
`internal/e2e/remote_test.go` inside `newServerWithContext` only; put every
new e2e test in the new file `internal/e2e/tier_test.go`.

## 1. System Overview

`internal/serve` never resolves a permission tier: `handleCreateBinding`
stores `Tier: ""`, so `effectiveTier(b)` is `harness` for every served
builder and the server's `policy.json` `tier.builder` and the candidate's
`tier` are dead config (Part A). Separately, nothing on the wire carries the
planner's `--tier`: `Send` routes to `sendRemote` before it looks at
`opts.Tier`, `StartRound` sends only `round`/`plan`/`bundle`, and
`handleStartRound` calls `relay.Send(..., SendOptions{})` (Part B).

Part A adds `ResolveServedTier` -- the same chain `add.go` uses locally,
capped by the *server's* `max_tier` with no way to lift it from the wire --
and calls it at create and at round start. Part B adds optional, additive
fields: `tier` on `CreateBindingRequest` and the `StartRound` form, `tier`
echoed on `BindingView`, and `features`/`builder_tier`/`max_tier` on
`WhoAmI` so a client can tell a pre-tier server apart and refuse `--tier`
before creating anything. `remote.Version` stays 1.

## 2. File Structure

```
internal/relay/served.go              MODIFY  ResolveServedTier, ServedBuilderTier; ServedView fills Tier
internal/relay/served_test.go         MODIFY  TestResolveServedTier, TestServedBuilderTier (create the file if absent)
internal/remote/proto.go              MODIFY  WhoAmI +Features/BuilderTier/MaxTier; CreateBindingRequest +Tier; BindingView +Tier; CodeTierAboveMax
internal/serve/bindings.go            MODIFY  handleCreateBinding resolves and stores Tier
internal/serve/rounds.go              MODIFY  handleStartRound reads the tier field, maps ErrTierAboveMax
internal/serve/routes.go              MODIFY  handleWhoAmI fills the three new fields
internal/serve/serve_test.go          MODIFY  tests for create/start/whoami tier behaviour
internal/relay/herdr.go               MODIFY  RemoteClient.StartRound gains a tier parameter
internal/remote/client/client.go      MODIFY  StartRound writes the tier form field when non-empty
internal/relay/remote_test.go         MODIFY  fakeRemote.StartRound signature; new addRemote/sendRemote/servers tests
internal/relay/remote.go              MODIFY  addRemote tier probe+request+echo; sendRemote tier; ServerProbe tier fields; RenderServers warning; ServerTierWarning
internal/relay/send.go                MODIFY  sendRemote(ctx, rt, hint, body, opts.Tier)
cmd/relay/main.go                     MODIFY  cmdAdd prints the tier line for a remote binding
cmd/relay/doctor.go                   MODIFY  serverChecks emits the tier warning row
cmd/relay/serve.go                    MODIFY  cmdServeRun logs the resolved builder tier at startup
internal/e2e/remote_test.go           MODIFY  newServerWithContext takes a policy (see §4.9)
internal/e2e/tier_test.go             NEW     Part A argv tests, Part B round-trip test
```

Do not touch `internal/relay/tier.go`, `internal/relay/add.go`,
`internal/relay/headless.go`, `internal/relay/switch.go`, or
`internal/store`.

## 3. Data Structures & Type Definitions

### 3.1 `internal/remote/proto.go` (all additive, `omitempty`)

```
type WhoAmI struct {
    ... existing ...
    Features    []string `json:"features,omitempty"`     // ["tier"] on a server with this change
    BuilderTier string   `json:"builder_tier,omitempty"` // ServedBuilderTier(rt): the tier a headless builder
                                                          //   launches at when the client sends none
    MaxTier     string   `json:"max_tier,omitempty"`     // policy.MaxTierOrDefault()
}

type CreateBindingRequest struct {
    ... existing ...
    Tier string `json:"tier,omitempty"`   // "" = server's choice; else harness|read|edit|yolo
}

type BindingView struct {
    ... existing ...
    Tier string `json:"tier,omitempty"`   // effectiveTier(b) on the server; "" from a pre-tier server
}

const CodeTierAboveMax Code = "tier_above_max"   // HTTP 422
const FeatureTier = "tier"                       // the Features token
```

### 3.2 `internal/relay/remote.go`

```
type ServerProbe struct {
    ... existing ...
    TierAware   bool   // WhoAmI.Features contains FeatureTier
    BuilderTier string // WhoAmI.BuilderTier; "" when !TierAware
    MaxTier     string // WhoAmI.MaxTier;     "" when !TierAware
}
```

`ServerProbe` fields are filled only in the `err == nil` ("enrolled") arm.

## 4. Interface Definitions & Component Contracts

### 4.1 `internal/relay/served.go`

```
// ResolveServedTier is the served counterpart of add.go's
// resolveTier+checkTierCap. token is the candidate PickServedCandidate
// chose (may be "" or unresolvable: then the candidate contributes no
// tier); explicit is the wire tier ("" = none).
//   chain: explicit > candidate.Tier > rt.Policy.TierFor("builder") > harness
//   cap:   checkTierCap(tier, rt.Policy, allowYolo=false) -- the server's
//          max_tier is a ceiling nothing on the wire lifts
// Errors: harness.ParseTier's error for a malformed explicit;
//         ErrTierAboveMax (wrapped, message names the ceiling) above the cap.
// explicit == "" can still fail: a policy tier.builder above max_tier is a
// policy authoring error and returns ErrTierAboveMax too, so the create
// fails loudly instead of launching at a tier the policy forbids (add.go
// behaves the same locally).
func ResolveServedTier(rt Runtime, token, explicit string) (harness.Tier, error)

// ServedBuilderTier is what WhoAmI reports and relay serve logs at startup:
// ResolveServedTier(rt, PickServedCandidate(rt, ""), ""), falling back to
// harness (not an error) when the chain refuses.
func ServedBuilderTier(rt Runtime) harness.Tier
```

Candidate lookup: `candidate.ParseRef(token)` then `rt.Candidates.Lookup`
(guard `rt.Candidates == nil`); any failure means a zero
`candidate.Candidate`. `resolveTier` and `checkTierCap` are unexported in
the same package -- use them, do not re-implement.

`ServedView` additionally sets `Tier: string(effectiveTier(b))`.

### 4.2 `internal/serve/bindings.go` -- `handleCreateBinding`

After `PickServedCandidate`:

```
tier, err := relay.ResolveServedTier(rt, candidateToken, req.Tier)
if err != nil:
    errors.Is(err, relay.ErrTierAboveMax) -> 422 CodeTierAboveMax,
        message: fmt.Sprintf("tier %s exceeds this server's max_tier %s; raise max_tier in the server's policy.json", req.Tier, rt.Policy.MaxTierOrDefault())
        (when req.Tier == "" the offending tier is the policy's own; word it
         "policy tier.builder %s exceeds max_tier %s")
    otherwise                          -> 400 CodeInvalid, err.Error()
b.Tier = string(tier)
```

Everything else in the handler is unchanged.

### 4.3 `internal/serve/rounds.go` -- `handleStartRound`

After `planText` is read: `tierStr := r.FormValue("tier")`. If non-empty,
`harness.ParseTier` (malformed → 400 `invalid`). Call
`relay.Send(..., relay.SendOptions{Tier: tierStr})`. In the `sendErr`
switch, **before** the generic 500: `errors.Is(sendErr, relay.ErrTierAboveMax)`
→ 422 `CodeTierAboveMax` with `sendErr.Error()` and return; the round must
not have opened (`Send` refuses before taking the lock -- verify that in
`send.go` and stop if it does not).

### 4.4 `internal/serve/routes.go` -- `handleWhoAmI`

Build `rt` exactly as `handleCandidates` does (`s.runtime(caller)`); on
error keep today's response (no new fields) rather than failing whoami.
Otherwise set `Features: []string{remote.FeatureTier}`,
`BuilderTier: string(relay.ServedBuilderTier(rt))`,
`MaxTier: string(rt.Policy.MaxTierOrDefault())`.

### 4.5 `RemoteClient.StartRound` (`internal/relay/herdr.go`, `internal/remote/client/client.go`, `fakeRemote`)

```
StartRound(ctx, server, name string, round int, plan []byte, bundle io.Reader, tier string) (remote.BindingView, error)
```

Client writes form field `tier` only when `tier != ""`, after `plan` and
before `bundle`. All three implementations change in one step so the
package compiles.

### 4.6 `internal/relay/remote.go` -- `addRemote`

Insert between step 4 (candidate check) and step 5 (CreateBinding):

```
wireTier := ""
if opts.Tier != "":
    t := harness.ParseTier(opts.Tier)            // error -> return it
    checkTierCap(t, rt.Policy, opts.AllowYolo)   // error -> return it (client's own policy, as Add does)
    who := rt.Remote.WhoAmI(ctx, opts.Server)     // error -> return it
    if !slices.Contains(who.Features, remote.FeatureTier):
        return ErrServerPreTier wrapped: "server %s does not carry a permission tier (pre-tier server); upgrade it or drop --tier"
    wireTier = string(t)
createReq.Tier = wireTier
```

`var ErrServerPreTier = errors.New("server does not carry a permission tier")`
in `remote.go`. After the create, store `Tier: view.Tier` on the local
binding. Nothing else in `addRemote` changes; the deferred unbind-on-failure
logic is untouched because the probe happens before the create.

### 4.7 `sendRemote(ctx, rt, b, planBody, tier string)`

Same probe as 4.6 when `tier != ""` (the cap check already ran in `Send`;
do not repeat it), then pass `tier` to `StartRound`. A 422
`CodeTierAboveMax` `HTTPError` from `StartRound` is returned as
`fmt.Errorf("%w: %s", ErrTierAboveMax, httpErr.Body.Message)` so the CLI
wording matches the local refusal.

### 4.8 `ServerTierWarning`, `RenderServers`, `serverChecks`

```
// ServerTierWarning is the one-line warning for a server that would launch
// headless builders at tier harness, "" otherwise (including !TierAware).
//   "headless builders at tier harness deny every tool unless the server host's harness settings allow them; set tier.builder in the server's policy.json or pass --tier"
func ServerTierWarning(p ServerProbe) string
```

`RenderServers`: for an enrolled probe append `  builder tier: <BuilderTier> (max <MaxTier>)`
to the row when `TierAware`, or `  builder tier: unknown (pre-tier server)`
when not; when `ServerTierWarning(p) != ""` add a second line
`  !! <warning>` under the row. `serverChecks` (`cmd/relay/doctor.go`):
after the existing "enrolled" ok row, append a `SevWarn` row
`Name: "servers"`, `Detail: "<name>: " + warning`, `Fix: "set tier.builder in the server's policy.json"`
when the warning is non-empty; a `SevInfo`/ok row `"<name>: builder tier unknown (pre-tier server)"`
when `!TierAware` (use whichever informational severity `doctor` already
has; if none, `SevWarn` with `ProbeFailed: true`). No test in `cmd/relay`
for this -- the rule lives in `ServerTierWarning`, tested in `internal/relay`
(CI runners have no herdr; `cmd/relay` tests must not run subcommands).

### 4.9 `cmd/relay/main.go` `cmdAdd`, `cmd/relay/serve.go` `cmdServeRun`, e2e helper

`cmdAdd`, remote branch: after the `added … on <server>` line print
`  tier %s (server)` when `res.Binding.Tier != ""`, else
`  tier server's choice (pre-tier server)`.

`cmdServeRun`: after both config files load, build
`rt := relay.Runtime{Candidates: candidates, Policy: pol, LedgerPath: <root>/ledger.json}`
and `slog.Info("builder tier", "tier", relay.ServedBuilderTier(rt))`; when
it is `harness`, `slog.Warn` the `ServerTierWarning` text instead.

`internal/e2e/remote_test.go`: change `newServerWithContext` to
`newServerWithContext(t, ctx, cancel, pol policy.Policy)` and have
`newServer` pass `policy.Policy{}`; `cfg.Policy = pol`. That is the only
edit to this file.

## 5. High-Level Pseudocode

```
ResolveServedTier(rt, token, explicit):
    c := zero candidate
    if rt.Candidates != nil && token != "":
        if ref ok := ParseRef(token); c2 ok := rt.Candidates.Lookup(ref): c = c2
    if explicit != "": ParseTier(explicit) -> err? return err
    tier := resolveTier(explicit, c, rt.Policy, "builder")
    if err := checkTierCap(tier, rt.Policy, false): return tier, err
    return tier, nil

ServedBuilderTier(rt):
    token, _ := PickServedCandidate(rt, "")
    tier, err := ResolveServedTier(rt, token, "")
    if err != nil: return TierHarness
    return tier
```

Part A e2e (`tier_test.go`):
```
TestServedBuilderLaunchesAtPolicyTier:
    server with policy {Tier: {"builder": "yolo"}, MaxTier: "yolo"}   // candidate is claude (existing candJSON)
    Add --server; Send(plan)
    spec := runner.specs[0]
    assert slices.Contains(spec.Argv, "--dangerously-skip-permissions")
    sb := serverStore.Load; assert sb.Tier == "yolo"
TestServedBuilderDefaultsToHarness:
    same with policy.Policy{}
    assert !Contains(spec.Argv, "--dangerously-skip-permissions") && !Contains(spec.Argv, "--permission-mode")
    assert sb.Tier == "harness"
```

Part B e2e:
```
TestRemoteTierOverWire:
    server policy {MaxTier: "yolo"} (no tier.builder); client rt.Policy = {MaxTier: "yolo"}
    who := rt.Remote.WhoAmI; assert Features contains "tier", BuilderTier == "harness"
    Add --server with Tier "edit"; assert view/binding Tier == "edit" on both stores
    Send with SendOptions{Tier: "yolo", AllowYolo: true}; assert specs[0].Argv contains --dangerously-skip-permissions
    Send round 2 (after finishRound + ticks as TestRemoteRoundEndToEnd does) with no tier;
        assert specs[1].Argv contains "--permission-mode" "acceptEdits"   // RoundTier cleared, Tier=edit persists
TestRemoteTierAboveServerMax:
    server policy {MaxTier: "edit"}; client policy {MaxTier: "yolo"}
    Add --server with Tier "yolo", AllowYolo true -> error wraps ErrTierAboveMax; no server binding exists; no local binding, no local branch
```

## 6. Error Handling Strategy

- `ErrTierAboveMax` (existing, `internal/relay/tier.go`) is the one
  sentinel for "above the ceiling" on both sides; the server maps it to
  422 `tier_above_max`, the client maps that back to the same sentinel.
- `ErrServerPreTier` (new) is a client-side refusal that happens before any
  server state changes. It is not retried and not a halt.
- Malformed tier anywhere → `harness.ParseTier`'s error; 400 `invalid` on
  the server.
- `handleWhoAmI` never fails because of tier resolution; it omits the
  fields instead (a v1 client ignores them anyway).
- No change to round-close, switch, or halt behaviour.

## 7. Ordered Implementation Steps

Run `make check` after every step; by hand the gofmt line is
`test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }`.

### Step 1 -- `ResolveServedTier` / `ServedBuilderTier` (test first)

`internal/relay/served_test.go`: table for `ResolveServedTier` -- explicit
beats candidate beats policy beats harness; unresolvable token contributes
nothing; explicit above `max_tier` → `ErrTierAboveMax`; policy tier above
`max_tier` with explicit "" → `ErrTierAboveMax`; `explicit=yolo`,
`MaxTier=yolo` → ok (proves allowYolo is not consulted). Build a
`candidate.Set` the way `tier_test.go`/`served_test.go` already do.
`TestServedBuilderTier`: policy tier set → that tier; refused chain →
harness. Then implement per §4.1. Verify: tests pass; `make check`.

### Step 2 -- proto fields (Part A needs `BindingView.Tier` only, add all now)

§3.1. `ServedView` sets `Tier`. Verify: `go build ./...`; existing
`internal/relay` and `internal/serve` tests pass.

### Step 3 -- server create resolves the tier (test first)

`internal/serve/serve_test.go`, using `newTestServer` plus a `Config` with
`Candidates` and `Policy` as the existing policy-bearing test near line
1857 does: (a) policy `tier.builder=edit` → stored binding `Tier == "edit"`
and the create response view has `tier: "edit"`; (b) no policy tier →
`"harness"`; (c) request `tier: "yolo"`, `max_tier` default → 422 with
`error: "tier_above_max"` and no binding stored; (d) request
`tier: "bogus"` → 400 `invalid`. Then §4.2. Verify: tests pass.

### Step 4 -- Part A e2e

§4.9's helper change, then `tier_test.go` with the two Part A tests.
Verify: both pass; `TestServedBuilderLaunchesAtPolicyTier` **fails** if you
temporarily revert step 3's `b.Tier = string(tier)` line (re-edit to
restore; never `git checkout` an uncommitted file). Report which test
failed under the mutation. **Part A is complete here.**

### Step 5 -- `StartRound` carries a tier (all implementations at once)

§4.5: interface, client, fake. Update the one call in `sendRemote` to pass
`""` for now. Verify: `go build ./... && go vet ./...`; all tests pass.

### Step 6 -- server start-round honours the tier (test first)

`serve_test.go`: start a round with form field `tier=edit` on a binding
stored at `harness` → the runner's `ProcSpec.Argv` contains
`--permission-mode acceptEdits` (see how `TestRoundStartAbsorbsAndChecksOut`
reaches the spec) and the stored binding has `RoundTier == "edit"`; with
`tier=yolo` above `max_tier` → 422 `tier_above_max`, round state stays
idle, no `ProcSpec` recorded. Then §4.3. Verify: tests pass.

### Step 7 -- whoami advertises the feature (test first)

Extend `TestWhoAmI`: response has `features: ["tier"]`, `builder_tier`
equal to the policy's tier (or `harness`), `max_tier` equal to
`MaxTierOrDefault()`. Then §4.4. Verify: passes.

### Step 8 -- client `addRemote` and `sendRemote` (test first)

`internal/relay/remote_test.go` with `fakeRemote`: (a) `--tier edit`
against a fake whose `whoAmIResp.Features` is nil → `ErrServerPreTier`,
`calls` contains no `CreateBinding`; (b) features `["tier"]` →
`CreateBinding` request carries `Tier: "edit"` (capture the request on the
fake) and the saved local binding has `Tier` from the fake's view; (c) no
`--tier` → no `WhoAmI` call, request `Tier == ""`; (d) `sendRemote` with a
tier → `StartRound` called with that tier (extend the fake to record it);
(e) `StartRound` returning `HTTPError{422, tier_above_max}` →
`errors.Is(err, ErrTierAboveMax)`. Then §4.6, §4.7, and the `send.go`
call-site change. Verify: passes; `make check`.

### Step 9 -- probes, `RenderServers`, `ServerTierWarning` (test first)

`remote_test.go`: `ProbeServers` fills `TierAware/BuilderTier/MaxTier`
from the fake's `WhoAmI`; `ServerTierWarning` is non-empty only for
`TierAware && BuilderTier == "harness"`; `RenderServers` output contains
`builder tier: harness (max edit)` and the `!!` line for that probe, and
`unknown (pre-tier server)` for a non-aware one. Then §4.8 in
`internal/relay`. Verify: passes.

### Step 10 -- CLI surfaces

§4.8 `serverChecks` in `cmd/relay/doctor.go`; §4.9 `cmdAdd` line and
`cmdServeRun` startup log. No `cmd/relay` tests. Verify: `go build ./...`;
`go run ./cmd/relay serve --state "$(mktemp -d)" --insecure-http --listen 127.0.0.1:0`
is **not** required -- CI has no herdr and neither does this step; just
`make check`.

### Step 11 -- Part B e2e

`tier_test.go`: `TestRemoteTierOverWire` and `TestRemoteTierAboveServerMax`
per §5. The client `rt` from `newClient` has `Policy` -- set
`rt.Policy = policy.Policy{MaxTier: "yolo"}` on the returned value before
calling `Add`. Verify: both pass; `go test ./internal/e2e -count=1` green.

### Step 12 -- report

List: which test failed under the step 4 mutation; the four e2e test names
and their pass status; `git diff --stat`, which must touch only the files in
§2. Any step you could not do as written: which, and why, with Part A's
status stated explicitly.
