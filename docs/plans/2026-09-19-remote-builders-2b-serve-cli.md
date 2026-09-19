# Remote builders, plan 2b of 4: `relay serve` -- TLS, enrollment, admin verbs, unit file (#100)

Spec: `docs/specs/2026-09-19-remote-builders-design.md` (in this tree)
§2.3–2.4, §3 (`/v1/candidates`), §6.1, §6.4. Plan 2a (merged, #207) built
`internal/serve` with handlers, per-owner stores and the tick loop; this
plan puts a listener, a certificate, an enrollment file and a CLI in
front of it. Plan 3 is the client. The surface freeze exception for these
verbs is recorded on #114 (2026-09-19).

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else.

**Scope guard.** Touch only the files in §2. No module dependency (standard
library: `crypto/tls`, `crypto/x509`, `crypto/ecdsa`, `crypto/elliptic`,
`encoding/pem`, `math/big`, `net`, `net/http`). Do not run `make e2e`.
Foreground only; no sub-agents. **CI rule (CLAUDE.md):** no test in
`cmd/relay` may execute a subcommand that reaches herdr; every rule below
is tested as a pure function in `internal/serve` or `internal/doctor`, and
the `cmd/relay` tests are limited to flag parsing and usage text.

**Git in tests.** Same helper discipline as plans 1 and 2a
(`GIT_CONFIG_GLOBAL=/dev/null`, fixed identity); copy, do not import.

**Commit the plan with the work.** Copy this plan file (the path relay gave
you) to `docs/plans/2026-09-19-remote-builders-2b-serve-cli.md` in your
worktree and include it in the first commit.

**Check command.** `make check`; green before every commit.

## 1. System overview

`relay serve` is one process: an HTTPS listener over `Server.Handler()`
and the `Server.Run` tick loop from plan 2a, sharing one `Server`. Its
state root is `<state>/serve` (the same `$XDG_STATE_HOME/relay` as the
local daemon, plus `serve/`). Before it can run, `relay serve init` has
written a server key and a self-signed certificate and printed the
fingerprint clients pin; `relay serve enroll` has added at least one
client. The admin reads the server from the box with `relay serve status`
(every owner, owner column first) and prunes with `relay serve gc
--abandoned`. `relay doctor` gains a "serve" section when a server is
initialised on the machine.

One amendment to the spec, made here and recorded in the report: the
spec's §6.4 put the server-local views on the ordinary verbs (`relay
status` showing all owners; `relay log --owner`). Those verbs are bound to
one `store.Store` and the server has one per owner, so the server-local
views live under `relay serve` instead: `relay serve status` now; `relay
serve log/show/tab --owner` are follow-ups, not in this plan.

## 2. File structure

```
internal/remote/
  proto.go             + CandidateView, CandidatesResponse

internal/serve/
  tls.go               InitTLS, LoadTLS, Fingerprint
  tls_test.go
  candidates.go        GET /v1/candidates handler
  admin.go             AdminStatus, OwnerStatus, GCAbandoned, GCAbandonedResult
  admin_test.go
  listen.go            (*Server).ListenAndServe: TLS or --insecure-http, plus Run, graceful stop
  listen_test.go       one real listener on 127.0.0.1:0 with TLS, whoami round-trip over a pinned client
  routes.go            + candidates route
  serve_test.go        + TestCandidatesView

internal/doctor/
  serve.go             ServeChecks
  serve_test.go

cmd/relay/
  serve.go             cmdServe and its subcommands
  serve_test.go        usage/flag tests only
  main.go              dispatch "serve"; usage text gains the serve lines

dist/relay-serve.service
README.md              "Command surface" gains the serve verbs; new section "Remote builders: the server"
docs/plans/2026-09-19-remote-builders-2b-serve-cli.md
```

## 3. Data structures

### 3.1 `internal/remote/proto.go`

```
type CandidateView struct {
    Token string `json:"token"`     // canonical harness/provider/model token
    Kind  string `json:"kind"`      // harness kind: agy | claude | opencode
    Gated bool   `json:"gated"`     // a live limit gate on the ledger
    Pick  bool   `json:"pick"`      // what the policy order would pick right now for the builder role
}

type CandidatesResponse struct {
    Candidates []CandidateView `json:"candidates"`
}
```

### 3.2 `internal/serve/tls.go`

Files under `<root>/serve`: `server.key` (ECDSA P-256, PKCS#8 PEM, 0600),
`server.crt` (self-signed X.509 PEM, 0644). Certificate: CN `relay serve`,
SANs = the `--host` values given to `init` plus `localhost`, `127.0.0.1`,
and the machine hostname; validity 10 years from `now`; `ExtKeyUsage
ServerAuth`. Fingerprint = `sha256:` + lower-case hex of `sha256(cert
DER)`.

```
InitTLS(dir string, hosts []string, now time.Time) (fingerprint string, err error)
    ErrTLSExists when server.key or server.crt is present (never overwrite)
LoadTLS(dir string) (tls.Certificate, error)
Fingerprint(certPath string) (string, error)
FingerprintOf(der []byte) string
```

### 3.3 `internal/serve/admin.go`

```
type OwnerStatus struct {
    Owner  remote.ClientID
    Label  string               // Clients.LabelOf
    Report relay.Report         // relay.Status over that owner's runtime
}

AdminStatus(ctx, s *Server) ([]OwnerStatus, error)
    walks bindings/<hex>/ as Tick does; one relay.Status per owner; sorted by Label

RenderAdminStatus(owners []OwnerStatus) string
    For each owner: a header line "<label>  (<id>)" then relay.RenderStatus(report)
    indented two spaces; owners with no bindings print "<label>  no bindings".
    Empty input prints "no owners\n".

type GCAbandonedResult struct {
    Owner   remote.ClientID
    Label   string
    Name    string
    LastSeen time.Time
    Archive string     // path, "" on dry run
}

GCAbandoned(ctx, s *Server, olderThan time.Duration, now time.Time, dryRun bool) ([]GCAbandonedResult, error)
    For every owned binding whose Serve.LastSeen (or, when zero, RoundStartedAt) is older
    than now-olderThan AND whose round is not running (RoundStateOf != running):
    dry run -> listed; else relay.Unbind(ctx, rt, name, true) (archive) -> listed with the archive path.
    A running round is never touched, whatever its age.
```

### 3.4 `internal/doctor/serve.go`

```
ServeChecks(env Env, serveRoot string, now time.Time) []Check
    Absent serveRoot/server.key -> no checks at all (this machine is not a server).
    Otherwise:
      "serve: certificate"   ok when server.crt parses and NotAfter > now+30d;
                             warn "expires <date>" within 30d; fail "expired" / "unreadable"
      "serve: clients"       ok "<n> enrolled" when clients.json parses (0 is a warn "none enrolled");
                             fail on a parse error
      "serve: state"         ok when serveRoot/bindings is creatable/writable (env.Stat on serveRoot, then
                             a write probe the Env offers -- add `Probe(dir string) error` to doctor.Env
                             if no such method exists, with realEnv creating and removing a temp file)
```

Match the existing `Check` shape and severities in `doctor.go`; look at
`usageChecks` for the pattern.

## 4. Interfaces

### 4.1 `internal/serve/listen.go`

```
type ListenConfig struct {
    Addr         string   // e.g. ":7777"
    TLS          *tls.Certificate   // nil only with InsecureHTTP
    InsecureHTTP bool
}

func (s *Server) ListenAndServe(ctx context.Context, lc ListenConfig) error
    Pre: lc.TLS != nil || lc.InsecureHTTP, else ErrNoTLS ("no certificate; run relay serve init or pass --insecure-http")
    Starts: net.Listen(tcp, Addr); http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10s};
            go s.Run(ctx) for the tick loop.
    InsecureHTTP: slog.Warn("serving plain HTTP; every client request is readable on the network") once at start
                  and once per tick summary (every 60 ticks) -- the spec says "every tick logs it"; once a minute
                  is the honest reading of that, say so in the doc comment.
    Blocks until ctx is done, then http.Server.Shutdown with a 5s budget and returns.
    Returns the listener's error otherwise.

func (s *Server) Addr() net.Addr    // the bound address once listening (for tests and the startup log line)
```

TLS config: `MinVersion: tls.VersionTLS12`, the one certificate, no client
auth (identity is the signed request, spec §2.4).

### 4.2 `internal/serve/candidates.go`

```
GET /v1/candidates   (authenticated like every route)
    gates := relay.Gates(rt) for any owner's runtime (the ledger is server-wide, so the caller's runtime is fine)
    for each candidate in cfg.Candidates (canonical order): CandidateView{Token, Kind, Gated: token in gates}
    Pick: the token relay's builder pick would choose now -- use the same function relay.Add uses for
    a headless add with no --builder (find it in internal/relay/add.go or pick.go; do not re-implement);
    exactly one candidate has Pick true unless every one is gated (then none).
    200 CandidatesResponse
```

### 4.3 `cmd/relay/serve.go`

```
relay serve [--listen :7777] [--state <dir>] [--interval 2s] [--insecure-http] [--max-bundle-bytes N]
relay serve init [--host <name>]... [--state <dir>]
relay serve enroll --label <label> --key "<ed25519 line>" [--state <dir>]
relay serve clients [--state <dir>]
relay serve revoke <id> [--state <dir>]
relay serve fingerprint [--state <dir>]
relay serve status [--state <dir>]
relay serve gc --abandoned <duration> [--dry-run] [--state <dir>]
```

- `--state` defaults to `store.DefaultRoot()`; the serve root is
  `<state>/serve`. Every subcommand resolves it through one helper
  `serveRoot(fs)`.
- `relay serve` (run): builds `serve.Config` the way `newRuntime()` builds
  the local runtime -- candidates and policy from `userConfigRoot()`,
  `git.NewClient`, `proc.New()`, `time.Now` -- then `serve.New`, then
  `LoadTLS` unless `--insecure-http`, then `ListenAndServe` under a
  signal-cancelled context. Prints one line on start: `relay serve
  listening on <addr> (tls sha256:...)` or `(INSECURE http)`.
- `init`: `InitTLS`; prints `fingerprint sha256:...` and `clients: run
  relay serve enroll --label <who> --key "<their relay client init line>"`.
  `ErrTLSExists` -> `already initialised; fingerprint sha256:...` exit 0.
- `enroll`: `Clients.Add`; prints `enrolled <label> <id>`.
  `ErrAlreadyEnrolled` -> message, exit 1.
- `clients`: one line per client: `<id>  <label>  enrolled <date>
  [revoked <date>]`.
- `revoke`: `Clients.Revoke`; `ErrNoSuchClient` -> exit 1.
- `fingerprint`: prints the fingerprint alone (scriptable).
- `status`: `RenderAdminStatus(AdminStatus(...))`.
- `gc`: `--abandoned` required (no default: a gc that guesses an age is
  the wrong kind of helpful); prints one line per result.

Exit codes as the rest of `cmd/relay`: 0 ok, 1 refused/failed, 2 usage.
`cmd/relay/serve_test.go`: usage text on no args (exit 2), `gc` without
`--abandoned` (exit 2), flag defaults. Nothing that opens a listener or
touches herdr.

### 4.4 `main.go`

`case "serve": return cmdServe(args[1:])`. The `usage` const gains, in
its own block after `agent`:

```
  serve                     run the remote-builder server (listener + daemon)
  serve init|enroll|clients|revoke|fingerprint|status|gc
                            server administration, on the server host
```

### 4.5 `dist/relay-serve.service`

Same shape as `dist/relay.service` (user unit, PATH line, no MemoryMax,
`OOMPolicy=continue` with the same comment about headless builders),
`ExecStart=%h/.local/bin/relay serve --listen :7777`,
`Description=relay serve - remote builders for enrolled relay clients`.
Not installed by any script in this plan; documented in the README.

### 4.6 README

"Command surface": the two lines from §4.4. New section "Remote builders:
the server" (after "Headless builders"): what `relay serve` is, the first
run (`init` -> copy the fingerprint -> `enroll` each client's public line
-> `systemctl --user enable --now relay-serve` with the unit copied to
`~/.config/systemd/user/`), what the admin sees (`serve status`,
`serve clients`), what it does not do (no planner, no panes, no admin over
the network), and the threat-model paragraph from spec §2.5 in two
sentences with a pointer to #204.

## 5. High-level pseudocode: first run on a server

```
relay serve init --host zen --host zen.lan
    -> serve/server.key, serve/server.crt; prints sha256:...
relay serve enroll --label alice@laptop --key "ed25519 AAAA..."
    -> serve/clients.json
cp dist/relay-serve.service ~/.config/systemd/user/ && systemctl --user enable --now relay-serve
    -> relay serve listening on [::]:7777 (tls sha256:...)
alice: relay client add-server zen https://zen:7777 --fingerprint sha256:...   (plan 3)
alice: relay add --name api --server zen                                         (plan 3)
admin: relay serve status
    alice@laptop  (SHA256:...)
      api  round 1  ACTIVE  builder headless agy ...
```

## 6. Error handling

- `init` never overwrites; `enroll` never duplicates; `revoke` never
  deletes. All three say what exists.
- The listener refuses to start without a certificate unless told
  `--insecure-http`, and then says so in the start line and the log.
- `gc --abandoned` never touches a running round and never deletes: it
  archives through `relay.Unbind(..., archive=true)`.
- Doctor checks never fail the doctor run on a machine that is not a
  server (no `server.key` -> no section).

## 7. Ordered implementation steps

**Step 1 -- plan, proto, TLS.** Copy the plan. Add §3.1. `tls.go` with
`TestInitTLSAndLoad` (files exist with the stated modes; `LoadTLS` returns
a certificate whose leaf has the SANs and a 10-year validity;
`Fingerprint(crt) == FingerprintOf(leaf.Raw)`), `TestInitTLSRefusesOverwrite`.
Commit: `serve: self-signed TLS, fingerprint (#100)`.

**Step 2 -- candidates route.** §4.2 with `TestCandidatesView` (two
candidates, one gated via a gate written to the server-wide ledger:
`Gated` true for it, `Pick` true for exactly the other). Commit: `serve:
GET /v1/candidates (#100)`.

**Step 3 -- listener.** §4.1 with `listen_test.go`: `TestListenTLSWhoAmI`
(init TLS in a temp root, start `ListenAndServe` on `127.0.0.1:0` in a
goroutine, build an `http.Client` whose `tls.Config` has
`InsecureSkipVerify: true` and a `VerifyPeerCertificate` that checks
`FingerprintOf(rawCerts[0])` equals the init fingerprint -- this is the
pinning rule plan 3's client will use; call `/v1/whoami` signed; cancel
ctx; `ListenAndServe` returns nil), `TestListenRefusesWithoutTLS`,
`TestListenInsecureHTTP`. Commit: `serve: HTTPS listener with the tick
loop (#100)`.

**Step 4 -- admin.** §3.3 with `admin_test.go`: `TestAdminStatusAllOwners`
(two owners, one binding each, both rendered under their labels, sorted);
`TestGCAbandonedArchivesOnlyIdleOld` (three bindings: old+idle ->
archived, old+running -> kept, new+idle -> kept; dry run archives
nothing and lists one). Commit: `serve: admin status, gc --abandoned
(#100)`.

**Step 5 -- doctor.** §3.4 with `serve_test.go` in `internal/doctor`:
no section without `server.key`; the three checks with a fake `Env`
(follow the existing doctor tests' fake). Commit: `doctor: serve section
(#100)`.

**Step 6 -- CLI, unit, README.** §4.3–4.6. Commit: `cli: relay serve
verb family, unit file, README (#100)`.

**Step 7 -- final.** `make check`. Report: exported names against §3/§4;
the exact `usage` lines added; the README section as written; the
`Probe` addition to `doctor.Env` if it was needed. Done marker.

## Verification the planner runs

`make check`; scope per §2; `relay serve init` / `enroll` / `clients` /
`fingerprint` / `status` in a temp `--state` on this machine; `relay serve
--state <tmp> --listen 127.0.0.1:0` starts and stops on SIGINT; `relay
doctor` on this machine shows no serve section (no server here) and shows
one with `XDG_STATE_HOME` pointed at the temp root.
