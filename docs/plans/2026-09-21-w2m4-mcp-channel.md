# Wave 2 chain M, step 4: `relay mcp` -- the planner channel and four tools (#124)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. Never
run `make check` here (the planner runs it); run the gate commands in §8
exactly as written. Every command in the foreground; no sub-agents for
edits. No git fetch/rebase (this worktree has no origin). Spec:
`docs/specs/2026-09-21-planner-channel-design.md` (read it first; this
plan implements §3-§7 and §9 of it).

## 1. System overview

A Claude Code planner today waits for a builder's report inside its own
turn (`relay wait` loop) because the daemon can only reach it by typing
into its pane. This round adds `relay mcp`: an MCP server over stdio that
Claude Code spawns from a plugin manifest. In **channel mode** it writes a
**claim** for its planner pane, drains that pane's planner-bound mailbox
(`Tx.PendingForPlanner` / `Tx.ConfirmIndex`) into `notifications/claude/channel`
pushes, and pushes one event per transition into `needs_you`, `broken`,
`orphaned`. The daemon's `DeliverPending` yields to a live claim and does
nothing else differently. In either mode the server exposes `status`,
`send`, `answer`, `done` as tools that call the same `internal/relay`
functions the CLI does. The JSON-RPC surface is hand-written on the
standard library: no new go.mod dependency.

## 2. File structure

```
internal/relay/channel.go            Claim, ClaimStore (interface), FileClaims (impl): Live/Write/Remove; ClaimTTL; pane->file name
internal/relay/channel_test.go       live/stale/dead-pid/absent, stale file removed on read, second writer refused
internal/relay/deliver.go            DeliverPending: first check yields to rt.Channels.Live(pane)
internal/relay/deliver_test.go       + TestDeliverYieldsToLiveClaim, TestDeliverIgnoresStaleClaim
internal/relay/herdr.go              Runtime: + Channels ClaimStore (nil == no claims)
internal/relay/drain.go              Drain: one poll over one pane's bindings -> pushes + confirms; StateEdges
internal/relay/drain_test.go         push-then-confirm, push error leaves pending, state edges
internal/mcp/jsonrpc.go              Request/Response/Notification types, line codec, error codes
internal/mcp/server.go               Server: Serve(ctx, in, out) loop; initialize / initialized / ping / tools/list / tools/call dispatch; Push
internal/mcp/tools.go                the four tool schemas (tools/list document) and argument decoding
internal/mcp/verbs.go                Verbs interface (what tools call) + RelayVerbs (adapter over relay.Runtime)
internal/mcp/instructions.go         const Instructions (the model-facing text, <60 lines)
internal/mcp/mode.go                 DetectMode(parentArgv) -> Mode; ParentArgv() (linux /proc, darwin ps)
internal/mcp/mode_test.go
internal/mcp/server_test.go          framing, initialize version pinning, tools/list, tools/call via fake Verbs, -32601/-32602, push encoding
cmd/relay/mcp.go                     cmdMCP: flags --pane, --mode, --interval; wires Runtime, Server, claim, poll loop, signals
cmd/relay/main.go                    dispatch "mcp"; help line; newRuntime sets Channels: relay.FileClaims{Root: st.ChannelsDir()}
internal/store/store.go              + (s *Store) ChannelsDir() string  = <root>/channels
claude-plugin/.claude-plugin/plugin.json   the Claude Code plugin manifest
.claude-plugin/marketplace.json            the repo-as-marketplace manifest
scripts/check-plugin-version.sh      + the two JSON manifests must carry the same version as herdr-plugin.toml
scripts/check-plugin-version_test.sh + one case per new manifest (mismatch fails)
README.md                            "## Claude Code plugin": install, launch line, modes, what arrives
docs/plans/2026-09-21-w2m4-mcp-channel.md   this plan; already committed in your base along with the spec -- do not rewrite either
```

No file outside this list changes. `internal/relay/reconcile.go`,
`held.go`, `internal/serve/*`, `internal/ui/*` are untouched.

## 3. Data structures & type definitions

### 3.1 `internal/relay/channel.go`

```
const ClaimTTL = 10 * time.Second

type Claim struct {
    Pane      string    `json:"pane"`       // required; the herdr pane id, e.g. "wG:pQ"
    PID       int       `json:"pid"`        // required; > 0
    StartedAt time.Time `json:"started_at"` // required
    SeenAt    time.Time `json:"seen_at"`    // required; refreshed each poll
    CWD       string    `json:"cwd"`        // informational
    Version   string    `json:"version"`    // relay version that wrote it; informational
}

// ClaimStore is what the daemon and relay mcp share. nil-safe on the
// Runtime: a nil Channels means "no claims exist".
type ClaimStore interface {
    // Live returns the claim for pane if it is live: parses, pid alive,
    // now - SeenAt <= ClaimTTL. A file that exists but is not live is
    // removed and (nil, nil) returned. Missing file -> (nil, nil).
    Live(pane string, now time.Time) (*Claim, error)
    // Write persists c (atomic: temp file + rename). Returns ErrClaimHeld
    // when a different live claim (other PID) exists for c.Pane.
    Write(c Claim, now time.Time) error
    // Remove deletes the claim file for pane if its PID equals pid. No
    // error when absent.
    Remove(pane string, pid int) error
}

var ErrClaimHeld = errors.New("pane already has a live channel")

type FileClaims struct {
    Root  string                 // <state>/channels
    Alive func(pid int) bool     // nil -> default: syscall.Kill(pid, 0) == nil || EPERM
}

func ClaimFileName(pane string) string   // ":" -> "_", plus ".json"; pane must be non-empty
```

### 3.2 `internal/relay/drain.go`

```
// Pusher is the channel's outbound side; internal/mcp implements it.
type Pusher interface {
    Push(ctx context.Context, content string, meta map[string]string) error
}

type DrainState struct {
    Pane     string
    Last     map[string]store.State // binding name -> last-seen state; nil until first poll
}

type DrainResult struct {
    Pushed    int      // payload events pushed and confirmed
    States    int      // state events pushed
    Failed    []string // binding names whose push returned an error (left pending)
}
```

### 3.3 `internal/mcp`

```
const ProtocolVersion = "2025-06-18"
const ServerName      = "relay"

type Mode int
const (
    ModeTools   Mode = iota // no claim, no pushes
    ModeChannel             // claim + pushes + tools
)

// JSON-RPC 2.0 (jsonrpc.go)
type Request struct {
    JSONRPC string          `json:"jsonrpc"`            // "2.0"
    ID      json.RawMessage `json:"id,omitempty"`       // absent on notifications
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"`
}
type Response struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      json.RawMessage `json:"id"`
    Result  any             `json:"result,omitempty"`
    Error   *RPCError       `json:"error,omitempty"`
}
type RPCError struct { Code int `json:"code"`; Message string `json:"message"`; Data any `json:"data,omitempty"` }
const (
    CodeParse         = -32700
    CodeInvalidReq    = -32600
    CodeMethodMissing = -32601
    CodeInvalidParams = -32602
    CodeInternal      = -32603
)

// tools (tools.go): the tools/list document, verbatim schemas
type ToolSpec struct {
    Name        string         `json:"name"`
    Description string         `json:"description"`
    InputSchema map[string]any `json:"inputSchema"`   // JSON Schema object; "additionalProperties": false everywhere
}
// StatusArgs{Name string; All bool}
// SendArgs{Name, File string; Tier string; Verify *bool; Regate *int; DryRun bool}   // Name, File required
// AnswerArgs{Name string; Text, Keys string; Choice int}                              // Name required; exactly one of Text|Keys|Choice(>0)
// DoneArgs{Name string}                                                                // required

// ToolResult is the MCP tools/call result
type ToolResult struct {
    Content []Content `json:"content"`          // one {type:"text", text:<json or message>}
    IsError bool      `json:"isError,omitempty"`
}
type Content struct { Type string `json:"type"`; Text string `json:"text"` }

// verbs.go
type Verbs interface {
    Status(ctx context.Context, a StatusArgs) (any, error)   // returns relay.Report (filtered)
    Send(ctx context.Context, a SendArgs) (any, error)       // relay.SendResult or relay.DryRun
    Answer(ctx context.Context, a AnswerArgs) (any, error)   // {"ok":true,"text":...}
    Done(ctx context.Context, a DoneArgs) (any, error)       // DoneResult + "text"
}
type RelayVerbs struct { RT relay.Runtime; Pane string }    // the adapter; implements Verbs

// server.go
type Server struct {
    Verbs        Verbs
    Version      string        // serverInfo.version
    Instructions string        // defaults to Instructions
    Log          io.Writer     // stderr; nil -> discard
    OnInitialized func()       // called once after notifications/initialized (cmd wires the poll loop start)
}
```

`Server` implements `relay.Pusher` via `Push`, which writes a
`notifications/claude/channel` line to `out`; writes are serialised by a
mutex because the request loop and the poll goroutine both write.

### 3.4 Manifests

`claude-plugin/.claude-plugin/plugin.json`:

```json
{
  "name": "relay",
  "displayName": "relay",
  "description": "Planner/builder handoff: builder reports and NEEDS YOU pushed into this session; status/send/answer/done as tools",
  "version": "<same as herdr-plugin.toml>",
  "author": {"name": "Fuad Daoud"},
  "repository": "https://github.com/fuad-daoud/relay",
  "license": "MIT",
  "mcpServers": {
    "relay": {"command": "relay", "args": ["mcp"]}
  }
}
```

`.claude-plugin/marketplace.json`:

```json
{
  "name": "relay",
  "owner": {"name": "Fuad Daoud"},
  "plugins": [
    {"name": "relay", "source": "./claude-plugin", "description": "<same>", "version": "<same>"}
  ]
}
```

Check the LICENSE file for the actual license string; use what it says.

## 4. Interface definitions & component contracts

### 4.1 `FileClaims` (ClaimStore)

- `Live(pane, now)`: read `Root/ClaimFileName(pane)`. Missing -> `(nil,nil)`.
  Parse error, `PID<=0`, `!Alive(PID)`, or `now.Sub(SeenAt) > ClaimTTL` ->
  remove the file (ignore remove errors), return `(nil, nil)`. Otherwise
  the claim. I/O errors other than not-exist -> `(nil, err)`.
- `Write(c, now)`: `Live(c.Pane, now)`; if a live claim exists with
  `PID != c.PID` -> `ErrClaimHeld`. Else `MkdirAll(Root, 0o755)`, write
  `c` as JSON to a temp file in `Root`, rename over the target.
- `Remove(pane, pid)`: read; if present and `PID == pid`, delete. Else
  nothing. Never returns not-exist errors.
- Precondition for all: `pane != ""`, else an error `"empty pane"`.

### 4.2 `DeliverPending` (extended)

Signature unchanged. New first statement:

```
if rt.Channels != nil:
    c, err := rt.Channels.Live(b.Planner.PaneID, rt.Now())
    if err != nil: return b, Delivery{}, fmt.Errorf("channel claim: %w", err)
    if c != nil:   return clearPlannerScreen(b), Delivery{Reason: "planner has a channel"}, nil
```

Postconditions with a live claim: no `Herdr.Prompt`, no `Herdr.Notify`,
no `ConfirmIndex`, `b.State` unchanged, `Delivery` all-false. Everything
after that statement is byte-for-byte as today.

### 4.3 `Drain`

```
func Drain(ctx context.Context, rt Runtime, st *DrainState, p Pusher) (DrainResult, error)
```

- Precondition: `st.Pane != ""`, `rt.Store != nil`.
- `rt.Store.List()`; keep `b.Planner.PaneID == st.Pane && b.Owner == ""`.
- For each kept binding, in name order:
  1. `rt.Store.WithLock(func(tx) { e, idx, found, err = tx.PendingForPlanner(b.Name) })`.
     Not found -> skip to state edge.
  2. `p.Push(ctx, e.Payload, meta)` with meta per spec §3.5: `binding`,
     `round`, `kind`, `seq` (strconv), and `path` when `e.Path != ""`.
     Push error -> append name to `Failed`, log, continue (entry stays
     pending).
  3. `rt.Store.WithLock(func(tx) { tx.ConfirmIndex(b.Name, idx) })`; count `Pushed`.
     The lock is dropped between 1 and 3 on purpose: the push may take a
     moment and the daemon must not stall on it; the daemon cannot take the
     entry meanwhile because the claim is live.
  4. State edge: `prev, seen := st.Last[b.Name]`; if `!seen || prev != b.State`,
     and `b.State` is one of `needs_you|broken|orphaned`, push a state event
     (content per spec §3.5, meta `binding, round, kind:"state", state, old_state`
     where `old_state` is `prev` or `""`); count `States`. Then
     `st.Last[b.Name] = b.State` regardless of whether a push happened.
- Bindings that disappeared from the store are dropped from `st.Last`.
- Returns `error` only for store-level failures (List, lock); push
  failures are reported in `Failed`.

### 4.4 `mcp.Server`

`Serve(ctx, in io.Reader, out io.Writer) error` -- reads one JSON object per
line from `in` until EOF or ctx done; for each:

| method | behaviour |
|---|---|
| `initialize` | result `{protocolVersion: ProtocolVersion, capabilities: {tools: {}, experimental: {"claude/channel": {}}}, serverInfo: {name: ServerName, version}, instructions}`. Ignores the client's offered version. |
| `notifications/initialized` | call `OnInitialized` once; no reply |
| `ping` | result `{}` |
| `tools/list` | result `{tools: [4 ToolSpec]}` in the order status, send, answer, done |
| `tools/call` | params `{name, arguments}`; unknown name -> `-32602`; decode arguments into the args struct (unknown fields rejected -> `-32602`; validation failures -> `-32602` with the reason); call the verb; verb error -> `ToolResult{IsError:true, Content:[text: err.Error()]}`; success -> `ToolResult{Content:[text: json.MarshalIndent(result)]}` |
| any other request with an ID | `-32601` |
| any other notification | ignored |
| unparseable line | `-32700` with null id |

`Push(ctx, content, meta)`: writes `{"jsonrpc":"2.0","method":"notifications/claude/channel","params":{"content":...,"meta":...}}` + `\n`. Meta keys that are not identifiers (`^[A-Za-z_][A-Za-z0-9_]*$`) are dropped with a log line -- Claude Code would drop them silently otherwise. Error only when the write fails.

### 4.5 `RelayVerbs`

- `Status`: `relay.Status(ctx, RT)`; unless `a.All`, keep `Bindings` where
  `PlannerPane == Pane`; if `a.Name != ""`, keep only that name (error
  `"no binding named X"` when the filter empties a non-empty list).
  Apply the same DONE-hiding the CLI applies by default (find how
  `cmdStatus` does it -- `HideDone` -- and call the same function).
- `Send`: `relay.SendDryRun` when `a.DryRun`, else `relay.Send`, with
  `SendOptions{Tier, Regate, Verify}`. `AllowYolo` is always false.
- `Answer`: `relay.Answer(ctx, RT, a.Name, AnswerInput{Keys, Text, Choice})`;
  result `map[string]any{"ok": true, "text": relay.AnswerText(a.Name)}`.
- `Done`: `relay.Done(ctx, RT, a.Name)`; result is `DoneResult`'s fields plus
  `"text": relay.DoneText(a.Name, r)` (marshal a small struct that embeds
  DoneResult and adds Text).
- The CLI's `HERDR_PANE_ID` is read by the verbs from `RT` today via
  `os.Getenv` in `cmd/relay` -- check each of the four `cmd*` functions;
  the only one that needs the pane is none of these four (send/answer/done
  resolve the binding by name; status reads all). If one of them does need
  `PlannerPane`, pass `Pane`. Halt if a verb needs an argument this plan
  does not carry.

### 4.6 `DetectMode`

```
func DetectMode(parentArgv []string) Mode   // pure
func ParentArgv() ([]string, error)          // os.Getppid(); linux: /proc/<ppid>/cmdline split on NUL; darwin: exec `ps -o args= -p <ppid>` split on spaces; other: error
```

`DetectMode` returns `ModeChannel` iff argv contains an element equal to
`--channels` or `--dangerously-load-development-channels`, or an element
that starts with either followed by `=`. Anything else -> `ModeTools`.

### 4.7 `cmdMCP` (`cmd/relay/mcp.go`)

Flags: `--pane <id>` (default `$HERDR_PANE_ID`), `--mode channel|tools|auto`
(default `auto`), `--interval <dur>` (default `1s`, min `200ms`).

Exit codes: 2 for usage / no pane; 1 for a refused claim (`ErrClaimHeld`,
message `pane %s already has a live channel (pid %d)`) or a runtime error.

## 5. High-level pseudocode

```
cmdMCP:
    parse flags; pane := flag or env; if "" -> stderr usage line, exit 2
    rt := newRuntime()                       // same as every other verb
    mode := flag; if auto: argv, err := ParentArgv(); mode = DetectMode(argv); if err: mode = tools, log why
    log "relay mcp: pane %s mode %s"
    srv := &mcp.Server{Verbs: &mcp.RelayVerbs{RT: rt, Pane: pane}, Version: version, Log: stderr}
    ctx, cancel := signal.NotifyContext(SIGTERM, SIGINT)
    if mode == channel:
        srv.OnInitialized = func():
            claim := Claim{Pane, PID: os.Getpid(), StartedAt: now, SeenAt: now, CWD: cwd, Version: version}
            if err := rt.Channels.Write(claim, now); err != nil:
                log err; if ErrClaimHeld: os.Exit(1)   // Claude Code shows the server failed; pane delivery continues
            go pollLoop(ctx, rt, pane, srv, interval)
    err := srv.Serve(ctx, os.Stdin, os.Stdout)   // returns on EOF or ctx done
    if mode == channel: rt.Channels.Remove(pane, os.Getpid())
    return err

pollLoop:
    st := &DrainState{Pane: pane}
    ticker every interval:
        claim.SeenAt = now; rt.Channels.Write(claim, now)  // refresh; ErrClaimHeld here means someone stole it: log, stop pushing, keep tools
        res, err := relay.Drain(ctx, rt, st, srv)
        log non-zero counts and errors at info; never exit on a drain error

Server.Serve:
    scanner over in (buffer 16 MiB: a report payload can be large)
    for each line: decode Request
        if Method == "" or JSONRPC != "2.0": reply -32600 (if ID) else ignore
        switch Method ... (table in §4.4)
    on EOF: return nil

DeliverPending (daemon):
    if live claim for b.Planner.PaneID: return not-yet          // NEW
    ...existing...

Drain: see §4.3
```

## 6. Error handling strategy

- **Claim I/O** (`FileClaims`): not-exist is never an error. Other I/O
  errors propagate: in the daemon they fail the tick for that binding
  (`deliverAndSettle` already returns errors up); in `relay mcp` they are
  logged and the poll continues.
- **Push failures** (stdout write): the entry stays pending and is retried
  next poll; after stdout is closed `Serve` has returned anyway and the
  process is exiting.
- **Verb errors**: tool result `isError: true`, message verbatim -- never a
  JSON-RPC error, so the model reads it as data and can act.
- **Protocol errors**: JSON-RPC codes per §4.4. Never crash on a bad line.
- **Second claim**: `ErrClaimHeld` at startup -> exit 1 with the message;
  mid-run (refresh) -> log and stop pushing, tools continue.
- **Logging**: stderr only, `log`-style lines prefixed `relay mcp:`;
  never stdout (it is the transport). The daemon logs the yield nowhere
  per tick (it is the ordinary path once a channel exists), but
  `deliverAndSettle`'s existing branches stay as they are.

## 7. Ordered implementation steps

1. **Claims** -- `internal/relay/channel.go` + `_test.go`; `store.ChannelsDir()`;
   `Runtime.Channels`. Verify: `go test ./internal/relay -run Claim` green;
   a test that writes a claim with a dead pid and asserts `Live` returns
   nil *and* the file is gone.
2. **Daemon guard** -- `DeliverPending` first check; two tests in
   `deliver_test.go` using the existing `fakeHerdr` and a fake ClaimStore
   (a map). Verify: `TestDeliverYieldsToLiveClaim` asserts zero prompts,
   zero notifies, entry still pending, state unchanged; comment out the
   guard locally, confirm that test fails on the prompt count, restore.
   Depends on 1.
3. **Drain** -- `drain.go` + tests with a fake Pusher recording
   `(content, meta)` and a real temp `store.Store`. Verify: push-then-confirm
   order (fake pusher fails on first call -> entry still pending; second
   poll succeeds -> confirmed); state edges: `active->needs_you` pushes
   once, staying in `needs_you` pushes nothing, first sight in `broken`
   pushes once, `needs_you->active` pushes nothing. Depends on 1.
4. **JSON-RPC + Server** -- `internal/mcp/{jsonrpc,server,tools,instructions}.go`
   + `server_test.go` driving `Serve` with an `io.Pipe`. Verify:
   initialize offered `2026-07-28` -> answered `2025-06-18` and the
   capabilities object contains `experimental["claude/channel"]`;
   `tools/list` has exactly four tools with `additionalProperties:false`;
   `tools/call answer` with both `text` and `keys` -> `-32602`;
   `tools/call done` with a fake Verbs returning an error -> `isError:true`;
   `Push` emits the notification line and drops a `bad-key` meta entry.
   No dependency on steps 1-3 (fake Verbs).
5. **Verbs adapter** -- `verbs.go` `RelayVerbs`; one test per verb against
   a temp store where feasible (status filtering by pane and name is the
   one that must be tested; send/answer/done may be covered by asserting
   they forward arguments to `relay.*` through a thin seam if the
   functions cannot run without herdr -- if so, say which in the report).
   Depends on 4.
6. **Mode detection** -- `mode.go` + tests for `DetectMode` (pure) with
   argv fixtures: `[claude --agent architect --dangerously-load-development-channels plugin:relay@relay]` -> channel;
   `[claude --channels=plugin:x]` -> channel; `[claude -p hi]` -> tools;
   empty -> tools. `ParentArgv` is untested (platform I/O).
7. **`relay mcp` verb** -- `cmd/relay/mcp.go`, dispatch + help line in
   `main.go`, `newRuntime` sets `Channels`. Verify: `go build ./...`;
   `echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' | HERDR_PANE_ID=t:1 go run ./cmd/relay mcp --mode tools`
   prints one initialize result and exits 0 on EOF; the same with
   `--mode channel` writes and then removes `<XDG_STATE_HOME>/relay/channels/t_1.json`
   (run with `XDG_STATE_HOME=$(mktemp -d)`). No cmd/relay test that
   reaches herdr (CI has none); the cmd test, if any, only checks usage
   exit 2 on a missing pane. Depends on 1-6.
8. **Manifests + version check + README** -- the two JSON files,
   `check-plugin-version.sh` extended (a JSON `"version": "x"` line read
   with `sed`, same style as the toml), shell test cases, README section
   with the exact launch line and the two modes. Verify:
   `sh scripts/check-plugin-version.sh` passes; change the plugin.json
   version, it fails naming the file, restore. `sh scripts/check-plugin-version_test.sh` green.

## 8. Gate

Run, in the worktree root, in this order, and paste the tail of each into
the report:

```
gofmt -l .            # must print nothing
go vet ./...
go test ./... 2>&1 | tail -40
sh scripts/check-plugin-version.sh
sh scripts/check-plugin-version_test.sh
```

Do not run `make check` or `make e2e`. Commit on the current branch with
the message
`feat(mcp): relay mcp -- the planner channel pushes reports and NEEDS YOU into a Claude Code session, and status/send/answer/done as tools (#124)`
(one commit is fine; more are fine if each builds). Report: what landed
per step, the gate output, and anything you had to leave out and why.
