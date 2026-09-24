# Plan: remote builder parity, round 1 (the live block)

Spec: `docs/specs/2026-09-24-remote-live-parity-design.md` (in this tree), §1–§3.
Read it first. Line numbers below are as of commit fec20b49, the tip of this branch.

**If a step is impossible as written or contradicts the code, stop and report.
Do not bend a test to fit.** Stop in particular if:
(a) `relevo.statusRow` cannot be called from `ServedLive` without holding the
server's `s.mu`, meaning something it calls needs that lock;
(b) `internal/remote` cannot import `internal/usage` without a cycle;
(c) the serve test env cannot produce a running round with a pid.

## 1. Overview

A remote round runs as a local headless round on the server, and the server's
own `statusRow` already computes its live facts. This round sends those facts
to the client in `BindingView.Live`. The client stores them on the binding
(`Endpoint.RemoteLive`), and the client's `statusRow` shows them through the
same `BindingStatus` fields a local row uses. After this, `relevo status`, the
status line and `relevo ui` show live tokens, the live diff, progress, pid and
log tail for a remote builder.

## 2. File structure

```
internal/remote/proto.go         MODIFY  LiveView, DiffStat types; BindingView.Live field
internal/store/types.go          MODIFY  LiveFacts, DiffFacts types; Endpoint.RemoteLive field
internal/relevo/served.go        MODIFY  ServedLive (exported) + liveViewOf (pure)
internal/relevo/remote.go        MODIFY  liveFactsOf (pure); observeRemote stores/clears RemoteLive; line 1280 clear
internal/relevo/status.go        MODIFY  applyRemoteLive (pure); remote branch uses it; guards on peekUsage/liveStat; ProcessWord; human render label
internal/serve/live.go           NEW     liveCache (per caller/binding/round, 2 s TTL)
internal/serve/serve.go          MODIFY  Server gains the cache field, initialised in New
internal/serve/bindings.go       MODIFY  handleGetBinding: build the view under s.mu, the live block after unlocking it
internal/ui/round_pane.go        MODIFY  "headless" words become ProcessWord() (lines ~457-460, ~471-475)
tests: internal/relevo/served_test.go, remote_test.go, status_test.go; internal/serve/serve_test.go (or a new live_test.go)
docs/specs/2026-09-24-remote-live-parity-design.md, docs/plans/2026-09-24-remote-live-r1.md  ALREADY PRESENT, commit them
```

Nothing else changes. Out of scope for this round (round 2 covers them): log
offset tailing, log-entry mirroring, drift.

## 3. Data structures

### `remote.DiffStat` (proto.go, new; place after `QueueView`)
| Field | Type | JSON |
|---|---|---|
| Files | int | `files` |
| Added | int | `added` |
| Removed | int | `removed` |

### `remote.LiveView` (proto.go, new; place after `DiffStat`)
This is the running round as the server's own status row sees it. No field
holds a server path.
| Field | Type | JSON | Source on the server |
|---|---|---|---|
| At | time.Time | `at` | `rt.Now()` when built |
| PID | int | `pid,omitempty` | `row.Headless.PID` |
| StartedAt | time.Time | `started_at,omitzero` | `row.Headless.StartedAt` |
| ExitCode | string | `exit_code,omitempty` | `row.Headless.ExitCode` ("3" or "unknown"; "" while running) |
| Tail | []string | `tail,omitempty` | `row.Headless.Tail` |
| Usage | *usage.Usage | `usage,omitempty` | `row.LiveUsage` |
| Diff | *DiffStat | `diff,omitempty` | `row.Live` (Files/Added/Removed; `Shared` dropped) |
| LastProgressAt | time.Time | `last_progress_at,omitzero` | `row.LastProgressAt` |
| ExploringSince | time.Time | `exploring_since,omitzero` | `b.ExploringSince` |
| GatingSince | time.Time | `gating_since,omitzero` | `time.Unix(b.GateRun.StartedAt, 0)` when `b.GateRun != nil`, else zero |

### `BindingView.Live` (proto.go, struct at 92–140)
Add after `Queue` (line 139): `Live *LiveView \`json:"live,omitempty"\``. The
doc comment says: non-nil only when RoundState == RoundRunning on a server that
sends it; nil from an older server.

### `store.DiffFacts` and `store.LiveFacts` (types.go, new; place after `QueueFacts`, line ~150)
`DiffFacts` has the same three ints as `remote.DiffStat` (JSON `files`, `added`, `removed`).
`LiveFacts` has exactly `LiveView`'s fields, with `Diff *DiffFacts`, using the
same JSON names. Its doc comment says: copied from BindingView.Live by
observeRemote on each running poll; nil in every other round state.

### `store.Endpoint.RemoteLive` (types.go, after `RemoteQueue`, line 138)
`RemoteLive *LiveFacts \`json:"remote_live,omitempty"\``.

## 4. Function contracts

### relevo (served.go)
- `func liveViewOf(row BindingStatus, b store.Binding, at time.Time) *remote.LiveView`.
  This is pure. It maps each field per the §3 table. `row.Headless` nil means
  the process fields stay zero; `row.LiveUsage` or `row.Live` nil means the
  pointer stays nil. It never returns nil.
- `func ServedLive(ctx context.Context, rt Runtime, b store.Binding) (*remote.LiveView, error)`.
  It calls `statusRow(ctx, rt, b)` and returns `liveViewOf(row, b, rt.Now())`,
  or `(nil, err)`. The caller decides when to call it (only for a running round).

### relevo (remote.go)
- `func liveFactsOf(v *remote.LiveView) *store.LiveFacts`: pure; nil in, nil out; otherwise a field-for-field copy (Tail copied, Usage copied by value into a new pointer).
- `observeRemote`:
  - Line 726, next to `b.Builder.RemoteQueue = nil`: add `b.Builder.RemoteLive = nil`
    and extend the comment. Every state clears it except running.
  - `case remote.RoundRunning:` (line 767): after `b.StalledSince = view.StalledSince`,
    set `b.Builder.RemoteLive = liveFactsOf(view.Live)`. Do it *before* the log
    mirror fetch, so a failed fetch still keeps the facts.
- Line 1280 (`next.Builder.RemoteQueue = nil` in catch-up's idle marking): add `next.Builder.RemoteLive = nil`.

### relevo (status.go)
- `func (b BindingStatus) ProcessWord() string`: returns "remote" when `b.Server != ""`, else "headless".
- `func applyRemoteLive(row *BindingStatus, lf *store.LiveFacts, stalled bool, logPath string, now time.Time)`.
  This is pure. Preconditions: `lf != nil` and the endpoint's RemoteStatus is "running". It sets:
  - `row.Headless = &HeadlessInfo{PID, StartedAt, LogPath: logPath, ExitCode, Tail}`
  - `row.LiveUsage` = a copy of `lf.Usage` (nil when nil)
  - `row.Live` = `&LiveDiff{Files, Added, Removed}` when `lf.Diff != nil`
  - `row.LastProgressAt = lf.LastProgressAt`
  - The builder word, in `headlessStatus`'s precedence, *excluding* stalled (the caller applies that):
    ExploringSince set → `"exploring " + AgeText(now-ExploringSince)`, and `row.Exploring` gets the same
    string unless `stalled`. Else GatingSince set → `"gating " + AgeText(now-GatingSince)`.
    Else ExitCode "unknown" → `"exited"`. Else another non-empty ExitCode → `"exited " + code`. Else `"working"`.
- `statusRow`, remote branch (lines 400–412), in this order:
  1. `row.Server = ...` (already there).
  2. The RemoteStatus / "unknown" / queueText lines are unchanged.
  3. New: when `b.Builder.RemoteStatus == string(remote.RoundRunning) && b.Builder.RemoteLive != nil`, call
     `applyRemoteLive(&row, b.Builder.RemoteLive, !b.StalledSince.IsZero(), rt.Store.BuilderLogPath(b.Name, b.Round), rt.Now())`.
  4. The stall override's condition changes from `row.BuilderStatus == "running"` to
     `b.Builder.RemoteStatus == string(remote.RoundRunning)`, because the word may now be
     "working" or "exploring …". The stalled word always wins.
- Line 510, `row.LiveUsage = peekUsage(...)`: run it only when `!b.Builder.Remote()`.
- Line 517, `row.Live = liveStat(...)`: run it only when `!b.Builder.Remote()`.
- The human render at line 803: replace the literal `"headless"` with `b.ProcessWord()`.
  The `else` branch (Headless nil, which prints "remote") is unchanged.
- The QuietFor computation (line ~521) is unchanged. It already works from
  `row.LastProgressAt`, and the client sets `RoundStartedAt` for a remote round
  (remote.go ~586).

### serve
- `internal/serve/live.go` (new): a small cache type. It maps key
  `caller + "\x00" + name` to `{round int, at time.Time, view *remote.LiveView}`,
  guarded by its own `sync.Mutex` (not `s.mu`), with `const liveViewTTL = 2 * time.Second`.
  - `get(key string, round int, now time.Time) (*remote.LiveView, bool)`: a hit only when the round matches and `now - at < TTL`.
  - `put(key string, round int, now time.Time, v *remote.LiveView)`.
  - Entries for other rounds are overwritten. No background eviction is needed.
- `Server` (serve.go:64): add the cache field and initialise it in `New` next to `stores:` (line 121).
- `handleGetBinding` (bindings.go:226): restructure so that `s.mu` covers
  exactly what it covers today: load, the LastSeen save, ReadLog, ServedView and
  queuePositionView. Capture `rt`, `b` and the view, then unlock. Do not use a
  `defer` across the live computation; an inner func that returns under the
  lock is fine. After unlocking, when `view.RoundState == remote.RoundRunning`:
  take the view from the cache on a hit, or else call
  `relevo.ServedLive(r.Context(), rt, b)` and put the result on success. On
  error, log `slog.Warn` and leave `Live` nil. The GET still succeeds. Then
  `writeJSON`. Error responses and statuses are unchanged.

### ui (round_pane.go)
- Lines ~457–460: `src = "headless"` becomes `src = "headless"`, or `"remote"` when `r != nil && r.Server != ""`.
- Lines ~471–475: `pane = "headless"` becomes `pane = r.ProcessWord()`.

## 5. Deletions (closed list)

1. The condition `row.BuilderStatus == "running"` in the remote stall override (status.go ~409). It is replaced as stated in §4.
2. The literal `"headless"` at status.go:803 and round_pane.go ~459 and ~474. Each is replaced by the ProcessWord logic.

Nothing else is deleted. No test is deleted. A test that asserted the old word for a remote row is ported, and the report cites it.

## 6. Error handling

- `ServedLive` fails → the GET succeeds without `live` and the server logs a warning.
- An old server → `live` is absent → `RemoteLive` stays nil → the row looks as it does today.
- The client cannot reach the server → observeRemote returns before line 726 → the last `RemoteLive` stays; the row shows `unreachable` as today.
- None of this adds a new halt or NEEDS YOU path.

## 7. Working efficiently

Each tool call is a round trip, so:
- Read the files in §2 once, in parallel, at the line ranges given. Don't re-search for what this plan already located.
- Make each file's edits in one call.
- Focused loop: `go test ./internal/relevo ./internal/serve ./internal/remote ./internal/store ./internal/ui -count=1 -run 'Live|Remote|Served|Status|GetBinding|Process'`.
  Fix everything it reports before the next run.
- Run the full check once at the end, by its parts: `gofmt -l $(git ls-files '*.go')` (it must print nothing),
  then `go vet ./...` and `go test -race -count=1 ./...`. (`make check` itself is blocked on this machine's laptop hook; its parts are not.)
- CI has no harness binary and no network. Add no test in `cmd/relevo`. The serve tests use the package's own test env (`setupTestEnv`, `sendRound`, `decodeView`), which uses fakes.

## 8. Ordered steps

### Step 1: types
Add `remote.DiffStat` and `remote.LiveView`, the `BindingView.Live` field,
`store.DiffFacts` and `store.LiveFacts`, and the `Endpoint.RemoteLive` field.
Verify: `go build ./...`.

### Step 2: the server side of relevo
Add `liveViewOf` and `ServedLive` in served.go. Add a test in `served_test.go`:
`TestLiveViewOf` builds a `BindingStatus` with Headless, LiveUsage, Live and
LastProgressAt set, plus a binding with ExploringSince and GateRun set. It
asserts every §3 field, and that nil Headless, LiveUsage and Live give zero
values and nil pointers.
Verify: the focused command.

### Step 3: the client side of relevo
Add `liveFactsOf`, the observeRemote changes and the line 1280 clear. Add
`applyRemoteLive`, the remote-branch changes, the two guards, `ProcessWord` and
the render label. Tests:
- `remote_test.go`, modelled on `TestObserveRemoteRunningClearsQueue` (line 2482):
  - `TestObserveRemoteRunningStoresLive`: a running view with Live set → `b.Builder.RemoteLive` matches field for field.
  - `TestObserveRemoteClosedClearsLive`: a binding with RemoteLive set, and a view in another round state (queued is simplest) → nil.
- `status_test.go`:
  - `TestApplyRemoteLive`, table-driven. Cases: working (no exploring, gating or exit) → "working"; exploring → the word and `row.Exploring`;
    exploring with stalled=true → the word stays exploring and `row.Exploring` stays ""; gating; exit "3" → "exited 3"; exit "unknown" → "exited".
    Every case asserts Headless.PID, LogPath, LiveUsage tokens and Live.
  - `TestStatusRowRemoteUsesLiveNotLocalReaders`: a remote binding saved with RemoteStatus "running" and RemoteLive set (usage 41k tokens,
    diff 2/+10/-3, pid 4242), and a runtime whose `Usage` is a `*fakeUsage` (fake_test.go:690) with non-empty `peekSamples`.
    Build the row through `Status` or `statusRow`. Assert `LiveUsage` is RemoteLive's (41k, not the fake's), `Live` is 2/+10/-3,
    `Headless.PID` is 4242, and `len(fake.peeks) == 0`.
  - `TestProcessWord`: "remote" when Server is set, "headless" otherwise.
- Port any existing test that fails because a remote row's word changed ("running" → "working"). Name each one in the report.
Verify: the focused command.

### Step 4: serve
Add `live.go` (the cache), the Server field, and the `handleGetBinding` restructure. Tests:
- `TestLiveCache` (pure): a put then get within the TTL is a hit; after the TTL, a miss; a different round, a miss.
- In `serve_test.go`, next to `TestStopRunningRoundKeepsBinding` (~line 2835):
  `TestGetBindingRunningHasLive`: `sendRound` gives a running round, then GET `/v1/bindings/api` → `view.Live != nil` and `view.Live.PID != 0`
  (or whatever the test env's fake runner reports; assert that it matches the stored `Builder.PID`).
  Then close or stop the round (reuse the stop route as that test does) and GET again → `view.Live == nil`.
Verify: the focused command.

### Step 5: ui labels
Make the two round_pane.go edits. If a golden file under `internal/ui/testdata`
changes, regenerate it only when the diff is exactly the headless→remote word
for a remote row. Otherwise halt.
Verify: `go test ./internal/ui -count=1`.

### Step 6: mutation checks (do them, report the results, revert)
1. Remove the `!b.Builder.Remote()` guard on peekUsage → `TestStatusRowRemoteUsesLiveNotLocalReaders` must fail.
2. Remove the `b.Builder.RemoteLive = nil` at line 726 → `TestObserveRemoteClosedClearsLive` must fail.
3. Drop the `!stalled` condition in applyRemoteLive → the stalled-exploring case must fail.

### Step 7: full check and commit
Run the full check by its parts (§7). All of it must pass. `git diff --stat`
must touch only the §2 files and the tests. Make one commit:
`feat(remote): a running remote round shows the server's live facts (tokens, diff, progress, pid, tail)`.
The report lists every test added or ported, the mutation results, and any deviation.
