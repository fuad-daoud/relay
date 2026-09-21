# Wave 1 batch G: server gates can be listed and lifted -- relay available forwards, relay serve gates|available|unavailable (#251)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. The repo
there has **no tags**, so never run `make check`; run the gate commands in
§7 exactly as written. Run every command in the foreground and read its
exit code; never as a background task. Do not spawn sub-agents for the
edits.

## 1. System overview

`relay unavailable <token>` gates a provider in the client's ledger and
forwards to the server (`internal/relay/remote.go:949` `ForwardUnavailable`
-> `POST /v1/unavailable`, handler `internal/serve/bindings.go:332`
`handleUnavailable` -> `relay.Unavailable` on the **server-wide** ledger at
`<serve root>/ledger.json`, `serve.go:112`). Lifting has no counterpart:
`relay available` (`cmd/relay/main.go:552`) edits only the client's ledger,
there is no `/v1/available` route, and `relay serve` has no gate verb, so a
server gate outlives the outage until someone edits `ledger.json` by hand.
Worse, `relay available` run **on the box** reads `~/.local/state/relay/ledger.json`
and prints `nothing was gating`, which reads as "not gated". After this
round: `POST /v1/available` mirrors `/v1/unavailable`; `relay available`
forwards to every server the client's bindings name and prints each
server's answer; `relay serve gates` lists the server ledger's gates,
`relay serve available <subject>` and `relay serve unavailable <token>`
edit it in place (resolving the state dir through the #246 pointer file
like the other admin verbs); and `relay available` on a box that runs a
serve daemon says so.

## 2. File structure

```
internal/remote/proto.go                 + AvailableRequest{Subject}, AvailableResponse{Provider, Removed}
internal/remote/client/client.go         + Available(ctx, server, subject) (remote.AvailableResponse, error)
internal/remote/client/client_test.go    + test (shape of the Unavailable client test if one exists; else a httptest round-trip)
internal/serve/routes.go                 + POST /v1/available -> handleAvailable
internal/serve/bindings.go               + handleAvailable
internal/serve/serve_test.go             + TestAvailableClearsServerWideGate
internal/serve/admin.go                  + AdminGates(srv) ([]ledger.Gate, error), AdminAvailable(srv, subject) (provider string, removed int, err error), AdminUnavailable(srv, token, until, reason) (provider string, err error), RenderGates([]ledger.Gate, now) string
internal/serve/admin_test.go             + TestAdminGatesAvailableUnavailable
internal/serve/serve.go                  Config: Candidates already exists; + a ledger-only runtime helper if runtimeAt does not fit (see §4)
internal/relay/herdr.go                  RemoteClient + Available(ctx, server, subject string) (remote.AvailableResponse, error)
internal/relay/remote.go                 + ForwardAvailable(ctx, rt, subject) []string
internal/relay/remote_test.go            fakeRemote.Available; + TestForwardAvailablePostsToEveryServerOnce
cmd/relay/main.go                        cmdAvailable: forwards; serve-daemon hint; help text
cmd/relay/serve.go                       + gates|available|unavailable verbs; usage text; admin config loads candidates+policy
cmd/relay/serve_test.go                  + TestServeGatesRefusesUninitialisedRoot (shape of TestServeStatusRefusesUninitialisedRoot at 154; reaches no herdr)
README.md                                relay serve verbs list; `relay available` paragraph
docs/plans/2026-09-21-w1g-server-gates.md   copy of this plan
```

Every implementer of `relay.RemoteClient` must compile:
`grep -rn "RemoteClient = \|func (f \*fakeRemote)\|Unavailable(ctx" internal/ cmd/ --include=*.go`
-- list what you find (expected: `*client.Client`, `fakeRemote` in
`internal/relay/remote_test.go`, possibly an `internal/e2e` fake).

## 3. Data structures

```
// internal/remote/proto.go
type AvailableRequest  struct { Subject string `json:"subject"` }   // provider name or candidate token, as relay.Available takes
type AvailableResponse struct { Provider string `json:"provider"`; Removed int `json:"removed"` }
```

## 4. Interfaces

```
// internal/serve/bindings.go
func (s *Server) handleAvailable(w, r)
    s.mu.Lock; rt := s.runtime(caller) (same as handleUnavailable :337); decode AvailableRequest; Subject required (400 like Token at :365-368)
    provider, removed, err := relay.Available(rt, req.Subject); err -> 422 with err.Error() (an unknown token is a client mistake)
    200 AvailableResponse{provider, removed}
    // server-wide only (no /v1/bindings/{name}/available): the ledger is server-wide, so there is nothing binding-scoped to check

// internal/remote/client/client.go
func (c *Client) Available(ctx, server, subject string) (remote.AvailableResponse, error)   // POST /v1/available, same signing as Unavailable (:389-407)

// internal/relay/herdr.go  RemoteClient
Available(ctx context.Context, server, subject string) (remote.AvailableResponse, error)

// internal/relay/remote.go
func ForwardAvailable(ctx, rt Runtime, subject string) []string
    rt.Remote == nil -> nil
    servers := distinct b.Builder.Server over rt.Store.List() where b.Builder.Remote() (ANY state, open round or not -- a gate matters most when nothing runs), sorted
    for each: resp, err := rt.Remote.Available(ctx, server, subject)
        err  -> "<server>: <err>"
        resp.Removed == 0 -> "<server>: nothing was gating <resp.Provider>"
        else -> "<server>: cleared <resp.Provider> (<n> entries)"
    returns one line per server, in order (the CLI prints them all, to stdout -- these are answers, not warnings)

// internal/serve/admin.go
func ledgerRuntime(s *Server) relay.Runtime
    // relay.Runtime{Candidates: s.cfg.Candidates, Policy: s.cfg.Policy, Store: store.New(s.cfg.Root), LedgerPath: <Root>/ledger.json, AvailabilityPath: <Root>/availability.json, Now: s.cfg.Now, Herdr: stubHerdr{}}
    // Store on the serve root exists only for WithLock (relay.mutateLedger locks through rt.Store); it never lists bindings from there.
    // If store.New(root) creates files eagerly that would confuse Initialised()/AdminStatus (check store.New and initialisedMarkers pointer.go:16), use a subdirectory <Root>/ledger-lock instead and say so in the report.
func AdminGates(s *Server) []ledger.Gate                       // relay.Gates(ledgerRuntime(s)); nil Candidates -> nil (relay.Gates already does this)
func AdminAvailable(s *Server, subject string) (provider string, removed int, err error)      // relay.Available(ledgerRuntime(s), subject)
func AdminUnavailable(s *Server, token string, until time.Time, reason string) (provider string, err error)  // relay.Unavailable(...)
func RenderGates(gates []ledger.Gate, now time.Time) string
    // "no gates\n" when empty; else one line per gate: "<token>  <GateKindText>  <GateUntilText>  <note>" -- reuse relay.GateKindText/GateUntilText (ledger.go:201,216) and match how `relay candidates` prints its gates section (cmd/relay/main.go cmdCandidates); if that renderer is a function you can call, call it.

// cmd/relay/serve.go
cmdServeGates(args)        flags: --state; adminRoot(fs); srv := serve.New(serveAdminConfigWithCandidates(root)); fmt.Print(serve.RenderGates(serve.AdminGates(srv), time.Now()))
cmdServeAvailable(args)    flags: --state; positional <subject> (usage exit 2 when missing); prints "cleared %s (%d entries)" or "nothing was gating %s" (same strings as cmdAvailable :574-579)
cmdServeUnavailable(args)  flags: --state, --for D, --reason S; positional <token>; prints "gated %s" like cmdUnavailable :536 (no daemon-switch line, no forwarding)
func serveAdminConfigWithCandidates(root string) (serve.Config, error)
    // serveAdminConfig(root) + Candidates and Policy loaded the way cmdServeRun does at serve.go:202-206 (factor that loading into one helper both call); a missing candidates.json is an error for these three verbs ("relay serve gates: no candidates at <path>")
usage text (serve.go:119-128): + "gates [--state DIR]", "available <provider|token> [--state DIR]", "unavailable <token> [--for D] [--reason S] [--state DIR]"

// cmd/relay/main.go cmdAvailable
    after the local clear: for _, line := range relay.ForwardAvailable(ctx, rt, subject) { fmt.Println(line) }
    then: if p, ok, _ := serve.ReadPointer(store.DefaultRoot()); ok && pidAlive(p.PID) {   // pidAlive is in cmd/relay/serve.go:79
              fmt.Printf("note: a relay serve daemon runs here with its own ledger (%s); use relay serve gates / relay serve available\n", filepath.Join(p.Root, "ledger.json")) }
    help text line 77: "available    clear a recorded rate limit locally and on every server your bindings name: relay available <provider|token>"
```

## 5. Pseudocode

Covered by §4. `handleAvailable` is `handleUnavailable` minus the
binding-scoped branch, with `relay.Available` in place of `relay.Unavailable`.

## 6. Error handling

- `/v1/available` with an unknown token -> 422 with `relay.Available`'s
  error text; a bare provider that gates nothing -> 200 `Removed: 0` (not
  an error, matching the local verb).
- `ForwardAvailable` never fails the local clear: it returns lines, the CLI
  prints them, exit 0. An unreachable server is one line (`<server>: <err>`).
- Admin verbs on an uninitialised root: the existing `adminRoot` error
  (`no serve state at ...`), exit non-zero, no ledger touched.

## 7. Ordered implementation steps

Commit prefix: this round is one feature; make **one** `feat(serve):`
commit at the end (squash your step commits before the gate, or use
`chore:`/`test:` per step and one `feat:` for the code). Never more than
one `feat:` commit on the branch.

### Task 1 -- wire and route

**Files:** `internal/remote/proto.go`, `internal/remote/client/client.go`, `client_test.go`,
`internal/serve/routes.go`, `bindings.go`, `serve_test.go`, `internal/relay/herdr.go`,
`remote.go`, `remote_test.go`, plus the implementers the §2 grep names.

**Tests first**
- `TestAvailableClearsServerWideGate` (`internal/serve`): build on
  `TestUnavailableGatesServerWide` (~1015): gate via `POST /v1/unavailable`
  `{token, reason}`, then `POST /v1/available` `{"subject": "<provider>"}`
  -> 200, body `removed == 1`, `provider` = the provider; `<root>/ledger.json`
  has no `rate_limited` entry left (`ledger.Load`); a second call ->
  `removed == 0`; `{"subject": ""}` -> 400; an unknown token -> 422.
- `TestForwardAvailablePostsToEveryServerOnce` (`internal/relay`): build on
  `TestForwardUnavailable` (~2269): three bindings -- two remote on server
  `alpha` (one with an open round, one idle), one remote on `beta` (DONE),
  one local. `fakeRemote.Available` records `"Available:<server>:<subject>"`;
  expect exactly two calls, `alpha` and `beta`, sorted, and two lines.
  **Mutation check:** filter to open rounds (as `ForwardUnavailable` does)
  and this must fail (alpha still called, beta not -- assert on both lines).

**Then** the code. **Verify:** `go test -count=1 ./internal/serve/ ./internal/relay/ ./internal/remote/...`.

### Task 2 -- admin verbs and CLI

**Files:** `internal/serve/admin.go`, `admin_test.go`, `cmd/relay/serve.go`, `serve_test.go`, `cmd/relay/main.go`.

**Tests first**
- `TestAdminGatesAvailableUnavailable` (`internal/serve/admin_test.go`, in
  the shape of `TestFlatStatusStampsOwnersAndDedupsGates` at 36 which
  already writes a gate through `relay.Unavailable`): `AdminGates` empty ->
  `RenderGates` prints `no gates`; `AdminUnavailable(srv, "claude/t/m", zero, "quota")`
  -> one gate, kind rate-limited, `RenderGates` contains `claude/t/m` and
  `quota`; `AdminAvailable(srv, "t")` -> removed 1; `AdminGates` empty
  again. Also: after these calls `Initialised(root)` still reports what it
  did before (the lock store did not fake an init).
- `TestServeGatesRefusesUninitialisedRoot` (`cmd/relay/serve_test.go`, shape
  of `TestServeStatusRefusesUninitialisedRoot` at 154). This test executes
  `relay serve gates --state <empty tmp>` and must fail at `adminRoot`
  before anything else; it reaches no herdr (CI runners have none).
- `TestServeAvailableWithoutSubjectExits2` (shape of
  `TestServeUnbindWithoutOwnerExits2` at 50).

**Then** the code per §4. **Verify:** `go test -count=1 ./internal/serve/ ./cmd/relay/ && go build ./...`.

### Task 3 -- README, plan copy, gate

README: in the `relay serve` admin verbs list add the three verbs with
one line each; in the rate-limit / `relay unavailable` section add: `relay
available` also clears the gate on every server your bindings name and
prints each server's answer; on a box running `relay serve`, use `relay
serve available`. Find them with `grep -n "relay serve unbind\|relay available" README.md`.

Copy the plan file you were handed to `docs/plans/2026-09-21-w1g-server-gates.md`
and commit it.

Gate, in the foreground, in this order; stop at the first failure and report it:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```
Do not run `make check` or `make e2e` (planner runs them).

Final commit message (the one `feat:`):
`feat(serve): gates can be listed and lifted on the server -- POST /v1/available, relay available forwards, relay serve gates|available|unavailable (#251)`

## Report

Per task: what was done, the test names, the verify result, the mutation
check's outcome (name the failing test). The §2 grep output. Then the
commit shas (confirm exactly one `feat:` on `main..HEAD`) and the gate
output's last lines. If any step was impossible as written, say which and
stop there.
