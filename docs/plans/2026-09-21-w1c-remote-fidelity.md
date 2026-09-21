# Wave 1 batch C: remote rounds carry tags and the stream file; relay wait returns at once when nothing is in flight (#242, #240, #253)

Three remote-round fixes in one round. This plan stands alone: everything
you need is in this file and in the tree. If a step is impossible as
written or contradicts the code, **halt and report** -- do not improvise
around it.

You are a headless builder on a server-side worktree of this repo. The
repo there has **no tags** (that is #242, which you are fixing), so never
run `make check`; run the gate commands in §7 exactly as written. Run every
command in the foreground and read its exit code; never as a background
task. Do not spawn sub-agents for the edits. Every throwaway git repo a test
creates must disable signing (`-c commit.gpgsign=false -c tag.gpgsign=false`,
as `internal/git/client_test.go:1491` and `scripts/check-plugin-version_test.sh:26-32` do).

## 1. System overview

- **#242** `relay send` ships the branch to the server as a git bundle of
  exactly one ref, `refs/relay/<name>/out` (`internal/relay/remote.go:347`
  `sendRemote` -> `Transport.Snapshot`; `internal/git/client.go:617`
  `BundleCreate` bundles only the refs it is given; `FetchBundle` at `:748`
  fetches `--no-tags`). Tags never reach the server, so `git describe --tags`
  in a server worktree has nothing to describe and
  `scripts/plugin-build_test.sh` case 3 (a clone whose remote is the
  enclosing repo) fails on every server round. Two halves:
  (a) the client sends its tags as **data** next to the bundle -- a JSON list
  of `{name, sha}` in the `StartRound` multipart request -- and the server
  sets each one as a lightweight tag in the bare repo when it has the commit
  (`update-ref refs/tags/<name> <sha>`), skipping the rest. No bundle format
  change, no new objects: a tag on an ancestor of the shipped branch already
  has its commit on the server. (b) case 3 stops assuming the enclosing repo
  has tags: it clones into an intermediate repo, tags there if needed, and
  clones `--no-tags` from that.
- **#240** the server serves round files by kind (`internal/serve/rounds.go:231`
  `handleRoundFile`: `report|diff|log|plan`), and the client's `catchUp`
  (`internal/relay/remote.go:800`) fetches report, diff and log. The
  harness's own record, `NNN-builder.jsonl` (`store.BuilderStreamPath`),
  never ships, so a remote `[error]` has no detail on the client. Add kind
  `stream`, fetch it at catch-up. Separately, `internal/transcript/claude.go`
  has no `case "error"`, so a claude stream `{"type":"error","message":...}`
  renders as the bare `[error]` from `unknown()`; render its message like
  the codex/opencode renderers do.
- **#253** (1) `catchUp`'s "branch checked out" retry (`remote.go:873-877`)
  logs at Info on every `SyncRemote`, i.e. once per second under
  `relay wait`; log it once per process per binding. (2) `relay.Wait`
  (`internal/relay/wait.go:117`) never asks whether the round it waits for
  was ever sent: after a refused send there is no `plan` entry for
  `b.Round`, and `Wait` sleeps the full timeout and exits 124. Return at
  once with a new code when the round has no plan entry.

## 2. File structure

```
internal/git/client.go                 + ListTags(ctx, dir) (map[string]string, error)
internal/git/client_test.go            + TestListTagsPeelsAnnotated
internal/relay/herdr.go                Git interface + ListTags; RemoteClient.StartRound gains tags []remote.TagRef
internal/remote/proto.go               + type TagRef {Name, SHA}
internal/remote/client/client.go       StartRound(...): + tags param, multipart field "tags" (JSON) when non-empty
internal/remote/client/client_test.go  + a test that the field is sent (build on the existing StartRound test)
internal/relay/remote.go               sendRemote: ListTags -> sorted []TagRef -> StartRound; catchUp: fetch "stream"; checked-out log once
internal/relay/fake_test.go            fakeGit + tags map[string]string, ListTags
internal/relay/remote_test.go          fakeRemote.StartRound records tags; + TestSendRemoteShipsTags, TestCatchUpFetchesStream, TestCatchUpStreamMissingIsFine, TestCatchUpBranchCheckedOutLogsOnce
internal/serve/rounds.go               handleStartRound: parse "tags", set refs after Absorb; handleRoundFile: kind "stream"
internal/serve/serve_test.go           + TestRoundStartSetsShippedTags; extend TestRoundCloseServesFilesBundleAck with /files/stream
internal/transcript/claude.go          + case "error"
internal/transcript/transcript_test.go + claude error case
internal/relay/wait.go                 + WaitNotStarted = 6; WaitOutcome returns it when the round has no plan entry
internal/relay/wait_test.go            + TestWaitNotStartedReturnsAtOnce, TestWaitExplicitUnsentRoundReturnsAtOnce
scripts/plugin-build_test.sh           case 3 hermetic (intermediate tagged clone)
README.md                              relay wait exit codes: + 6; remote rounds: tags and stream one sentence each
docs/plans/2026-09-21-w1c-remote-fidelity.md   copy of this plan
```

Every implementer of `relay.Git` and `relay.RemoteClient` must compile:
`grep -rn "var _ Git\|var _ RemoteClient\|StartRound(" internal/ cmd/ --include=*.go`
before you start and list what you find in the report (expected:
`*git.Client`, `fakeGit`, `fakeRemote`, `*client.Client`, plus any
`internal/e2e` fake). Nothing outside the files above unless that grep
names it -- and then only the call-site update.

## 3. Data structures

```
// internal/remote/proto.go
type TagRef struct {
    Name string `json:"name"`  // tag name without refs/tags/, e.g. "v0.4.0"; non-empty
    SHA  string `json:"sha"`   // the COMMIT the tag points at (annotated tags peeled); 40 hex
}
// The wire field: multipart form value "tags" on POST /v1/bindings/{name}/rounds,
// a JSON array of TagRef sorted by Name. Absent or empty = no tags. A server built
// before this ignores the field; a client built before this never sends it.

// internal/relay/wait.go
const WaitNotStarted = 6   // the round waited for has no plan entry: nothing is in flight
// WaitResult.Line for it: "round %d was never sent to %s's builder" (name = binding name)
```

## 4. Interfaces

```
// internal/git/client.go
func (c *Client) ListTags(ctx context.Context, dir string) (map[string]string, error)
    runs: git for-each-ref --format=%(refname:strip=2)%00%(objectname)%00%(*objectname) refs/tags
    value = %(*objectname) when non-empty (annotated tag -> commit), else %(objectname)
    empty map, nil error when there are no tags; error only when git fails

// internal/relay/herdr.go
type Git interface { ...existing...; ListTags(ctx context.Context, dir string) (map[string]string, error) }
type RemoteClient interface {
    StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader, tier string, tags []remote.TagRef) (remote.BindingView, error)
    ...rest unchanged...
}

// internal/remote/client/client.go
func (c *Client) StartRound(ctx, server, name string, round int, plan []byte, bundle io.Reader, tier string, tags []remote.TagRef) (remote.BindingView, error)
    when len(tags) > 0: writes form field "tags" = json.Marshal(tags) before the bundle part

// internal/serve/rounds.go  handleStartRound, after the Absorb succeeds and before the worktree checkout/merge:
    raw := r.FormValue("tags")
    if raw != "" { var tags []remote.TagRef; json.Unmarshal -> 400 CodeBadRequest "tags: <err>" on failure
                   for each t: validate Name (no "/", no "..", not empty, no whitespace) and SHA (40 hex) -> skip invalid silently? NO: 400 on the first invalid entry
                   err := s.cfg.Git.UpdateRef(ctx, bare, "refs/tags/"+t.Name, t.SHA, "")
                   on error: slog.Debug("tag not set", "tag", t.Name, "err", err); continue   // the commit is not on the server; that is expected for unrelated tags
                 }
    // idempotent: a tag already at that sha is a no-op; a tag moved by the client moves here too (old = "" means unconditional)

// internal/serve/rounds.go  handleRoundFile: case "stream": path = rt.Store.BuilderStreamPath(name, n)   (closed-round rule applies, like report/diff)

// internal/relay/remote.go  catchUp: after the "log" fetch and its 404 handling, the same block for "stream" -> writeTempAndRename(rt.Store.BuilderStreamPath(name, n), rc); 404 -> nothing written, no error
// internal/relay/remote.go  sendRemote: tagsMap, err := rt.Git.ListTags(ctx, b.Repo); err -> return the error (a broken repo is a real failure);
//                           tags := sorted []remote.TagRef; passed to StartRound

// internal/transcript/claude.go  renderClaude: case "error": return errLine(firstNonEmpty(str(obj["message"]), str(asMap(obj["error"])["message"])))  -- use the file's existing helpers (str, asMap, errLine); if a helper is missing, add it in transcript.go next to errLine

// internal/relay/wait.go
func WaitOutcome(b, entries, round, questionOf) WaitResult
    order: report entry for round -> 0/2/5 (unchanged)
           b.State == StateDone -> WaitGone (unchanged)
           WaitingOn(...) -> WaitNeedsYou (unchanged)
           NEW: !HasEntry(entries, round, store.DirToBuilder, store.KindPlan) -> WaitResult{Code: WaitNotStarted, Line: fmt.Sprintf("round %d was never sent to %s's builder", round, b.Name), Done: true}
           else zero value (keep waiting)
    Note: HasEntry is the helper remote.go:514 and :976 already use; if it lives in another file, use it from there.
    Note: the nudge entry (Note == nudgeNote) is a plan entry too -- DefaultWaitRound excludes it; WaitOutcome should treat a round whose only plan entry is a nudge as NOT started? No: a nudge is only ever appended to an already-sent round. Count any KindPlan/DirToBuilder entry.
```

## 5. Pseudocode

### checked-out log once (#253 part 1)
```
// internal/relay/remote.go, package level
var checkedOutWarned sync.Map   // binding name -> struct{}; process-local on purpose (daemon and wait are separate processes; each says it once)

in catchUp, the "checked out" branch:
    if _, seen := checkedOutWarned.LoadOrStore(name, struct{}{}); !seen {
        slog.Info("checkout another branch, then relay pull", "binding", name, "branch", b.Branch)
    } else {
        slog.Debug("still checked out", "binding", name, "branch", b.Branch)
    }
    return b, nil
after a successful Absorb in catchUp: checkedOutWarned.Delete(name)
```

### plugin-build_test.sh case 3 (#242 b)
```
# 3. A tagless clone with a reachable, tagged remote fetches tags and describes.
git clone -q "$root" "$work/origin"                       # carries the enclosing repo's tags if any
if ! git -C "$work/origin" describe --tags >/dev/null 2>&1; then
    git -C "$work/origin" -c tag.gpgsign=false tag v0.0.0-fixture     # hermetic: the fixture provides its own tag
fi
git clone -q --no-tags "$work/origin" "$work/withremote"
mkdir -p "$work/withremote/from-source"
... rest of case 3 unchanged (build, expect v[0-9]*) ...
# case 4 keeps using $work/withremote and removing its origin; unchanged.
```
Run `shellcheck scripts/plugin-build_test.sh` after editing (shellcheck is
installed on the server; if `command -v shellcheck` is empty say so).

## 6. Error handling

- `ListTags` failure in `sendRemote` is returned to the caller before
  `StartRound`, so no local state is written (same rule as every other
  pre-send failure, `remote.go:369-390`).
- Malformed `tags` JSON or an invalid entry -> 400 `CodeBadRequest` (use the
  existing bad-request code constant in `internal/remote`; if none exists,
  the closest existing 4xx code the file already uses for a bad form field).
  The bundle was already absorbed at that point; that is fine -- the next
  send is idempotent (`CodeRoundStarted` handling).
- A tag whose commit the server lacks: skipped, Debug log, never an error.
- `stream` 404 at catch-up: not an error (mirrors the `log` rule at `remote.go:851`).
- `WaitNotStarted` is a normal outcome, exit code 6, message on stdout like
  the other codes (`cmd/relay/main.go:1533-1542` needs no change: it prints
  `res.Line` and returns `exitCodeErr{res.Code}`).

## 7. Ordered implementation steps

Commit prefix for every commit in this round: `fix(...)`. Never `feat:`.

### Task 1 -- tags travel with the send (#242 a)

**Files:** `internal/git/client.go`, `client_test.go`, `internal/remote/proto.go`,
`internal/remote/client/client.go`, `client_test.go`, `internal/relay/herdr.go`,
`remote.go`, `fake_test.go`, `remote_test.go`, `internal/serve/rounds.go`,
`serve_test.go`, plus call-site updates the §2 grep names.

**Tests first**
- `TestListTagsPeelsAnnotated` (`internal/git`): temp repo (signing off), two
  commits, lightweight tag `lw` on the first, annotated tag `an` (`tag -a -m x`)
  on the second; `ListTags` returns `{lw: sha1, an: sha2}` (the commit, not
  the tag object); an untagged repo returns an empty map.
- `TestSendRemoteShipsTags` (`internal/relay`): build on
  `TestSendRemoteFirstSendFullBundle` (~1048): `fakeGit.tags = {"v1": "aaaa...", "v0": "bbbb..."}`;
  after `sendRemote`, `fakeRemote` recorded a StartRound with tags
  `[{v0 bbbb..} {v1 aaaa..}]` (sorted by name). **Mutation check:** drop the
  `tags` argument (pass nil) in `sendRemote` and this must fail.
- `TestRoundStartSetsShippedTags` (`internal/serve`): build on
  `TestRoundStartAbsorbsAndChecksOut` (~1291): tag the client repo's base
  commit `v1.2.3` (signing off) and include `tags` in the request (through
  the real `client.Client.StartRound`, or the raw multipart the test already
  builds -- whichever that test uses); after the round starts,
  `git -C <bare> rev-parse refs/tags/v1.2.3` equals the base sha and
  `git -C <worktree> describe --tags` prints `v1.2.3` (the worktree is a
  linked worktree of the bare repo; if describe there prints nothing, halt
  and report -- that means the tag has to be set on the worktree instead,
  which is a design change). Also send a tag for an all-`f` sha: the round
  still starts (200) and that tag does not exist in the bare repo.
- Client test: the multipart request carries field `tags` with the JSON
  when tags are given and no such field when they are nil.

**Then** the code per §4. **Verify:** `go test -count=1 ./internal/git/ ./internal/remote/... ./internal/relay/ ./internal/serve/`.
Commit: `fix(remote): a send ships the client's tags as data and the server sets the ones it can (#242)`.

### Task 2 -- hermetic plugin-build case 3 (#242 b)

**Files:** `scripts/plugin-build_test.sh`.

Per §5. **Verify:** `sh scripts/plugin-build_test.sh` passes **in this
worktree, which has no tags** (`git tag` prints nothing -- confirm and quote
it in the report). Before the change it fails case 3; after, all four cases
pass. `shellcheck scripts/plugin-build_test.sh` clean.
Commit: `fix(scripts): plugin-build case 3 provides its own tag instead of assuming the enclosing repo has one (#242)`.

### Task 3 -- the stream file ships; claude error events render (#240)

**Files:** `internal/serve/rounds.go`, `serve_test.go`, `internal/relay/remote.go`,
`remote_test.go`, `internal/transcript/claude.go`, `transcript_test.go`.

**Tests first**
- Extend `TestRoundCloseServesFilesBundleAck` (~1673): also GET
  `/files/stream` -> 200 with the bytes the fake builder's stream file holds
  (check what `scriptRunner` writes to `BuilderStreamPath`; if it writes
  nothing, have the test write two JSON lines there before closing the
  round). `/files/stream` before close -> 404 like `TestFilesBeforeCloseIs404`.
- `TestCatchUpFetchesStream` (`internal/relay`): build on
  `TestCatchUpWritesDiffEntryFromView` (~1835): `roundFileFunc` returns a
  body for kind `stream`; after catch-up the file at
  `rt.Store.BuilderStreamPath(name, n)` holds it, and the recorded calls
  include `RoundFile:...:stream`. **Mutation check:** remove the stream fetch
  and this must fail.
- `TestCatchUpStreamMissingIsFine`: kind `stream` -> 404; catch-up completes,
  report entry present, no stream file, no halt.
- transcript: a claude line `{"type":"error","message":"Unexpected server error","ref":"err_a1d49da9"}`
  renders `  ⎿ error: Unexpected server error` (exact shape of `errLine`);
  keep the existing `[brand_new]` unknown-type test green.

**Then** the code. **Verify:** `go test -count=1 ./internal/serve/ ./internal/relay/ ./internal/transcript/`.
Commit: `fix(remote): catch-up ships NNN-builder.jsonl; claude error events render their message (#240)`.

### Task 4 -- wait returns at once when nothing is in flight; log once (#253)

**Files:** `internal/relay/wait.go`, `wait_test.go`, `internal/relay/remote.go`, `remote_test.go`, `README.md`.

**Tests first**
- `TestWaitNotStartedReturnsAtOnce`: a binding with **no** log entries,
  `Round: 1`, `StateActive`; `Wait` with `Timeout: time.Hour`,
  `Interval: time.Millisecond` and the fake `Now` from `TestWaitTimesOut`
  (~328) returns `Code == WaitNotStarted`, `Done`, `Line` containing
  `never sent`, and the fake clock advanced at most once (so it did not
  loop to the timeout). **Mutation check:** remove the new branch in
  `WaitOutcome` and this must fail with 124.
- `TestWaitExplicitUnsentRoundReturnsAtOnce`: the `sentBinding` fixture
  (round 1 planned, not reported) with `Round: 3` in `WaitOptions` ->
  `WaitNotStarted`; with `Round: 1` -> still waits (keep the existing
  behaviour: use a short timeout and expect 124).
- `TestWaitOutcome` (~54): add a row for the not-started case if it is
  table-driven; otherwise the two tests above suffice.
- `TestCatchUpBranchCheckedOutLogsOnce`: build on
  `TestCatchUpBranchCheckedOutRetries` (~2055): install a capturing
  `slog.Handler` via `slog.SetDefault` (restore with `t.Cleanup`), run
  catch-up three times, count Info records whose message is
  `checkout another branch, then relay pull`: exactly 1. Reset
  `checkedOutWarned` for the binding name at the start of the test so
  ordering between tests cannot leak.

**Then** the code per §4/§5. README: find the `relay wait` exit-code list
(`grep -n "124" README.md`) and add `6` with its meaning; in the remote
builders section add one sentence each for tags and the stream file.

**Verify:** `go test -count=1 ./internal/relay/`. Commit:
`fix(wait): exit 6 at once when the round was never sent; the checked-out hint logs once (#253)`.

### Task 5 -- plan copy and gate

Copy the plan file you were handed to `docs/plans/2026-09-21-w1c-remote-fidelity.md`
and commit it (`chore(plans): record wave 1 batch C plan`).

Gate, in the foreground, in this order; stop at the first failure and
report it:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
sh scripts/plugin-build_test.sh
```
Do not run `make check` or `make e2e` (planner runs them; `make e2e` covers
`internal/e2e/remote_test.go`, which exercises catch-up with a real server).

## Report

Per task: what was done, the test names, the verify result, the mutation
check's outcome (name the failing test). The §2 grep output. Then the
commit shas and the gate output's last lines. If any step was impossible
as written, say which and stop there.
