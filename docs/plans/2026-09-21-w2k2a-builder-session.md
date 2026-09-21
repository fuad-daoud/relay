# Wave 2 chain K, step 2a: every closed round records the builder session that built it (#147 part 1)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. Never
run `make check` here (the planner runs it); run the gate commands in §7
exactly as written. Every command in the foreground; no sub-agents for
edits. If the pre-flight `git status` shows a dirty tree, follow your
definition's rule for a tree that already carries part of the plan.

## 1. System overview

The binding stores one `Builder.SessionID` -- the current pane builder's,
learned from herdr (`reconcile.go:101-110` `refreshEndpoint`); after a
switch or rebind it is gone, and a headless builder records none at all
(`headless.go:83-119` `startRound` keeps PID/StartedAt/LogPath only). A
planner reading round 3's report two rounds later cannot name the session
that wrote it, so nothing can resume it (#147 part 2) and the cost reader
cannot key on it (#142). Every headless harness prints its session id in
the stream's first events: claude `{"type":"system","subtype":"init",...,"session_id":"sess-1"}`,
opencode a top-level `"sessionID"` on every event, agy
`{"event":"init","conversation_id":"conv-1",...}`, codex
`{"type":"thread.started","thread_id":"..."}` (all four are in
`internal/usage/testdata/*-stream.jsonl` line 1). After this round the
daemon reads that id from the stream once per round and stores it on the
builder endpoint; at round close the report log entry carries
`BuilderSession{Kind, ID}` (pane: `Builder.SessionID`; headless: the
stream id; `""` when neither is known -- never guessed); `relay show
--json` and `relay log --json` expose it because they print the entry.
No process is started, no behaviour changes for any harness.

## 2. File structure

```
internal/store/log.go             + type BuilderSession {Kind, ID}; LogEntry + BuilderSession *BuilderSession `json:"builder_session,omitempty"`
internal/store/types.go           Endpoint + StreamSessionID string `json:"stream_session_id,omitempty"` (headless: the id read from the round's stream; reset when a round starts)
internal/transcript/session.go    + SessionID(kind string, line []byte) string  -- pure: the id a stream line carries for that kind, "" otherwise
internal/transcript/session_test.go + table test over the four fixtures' first lines and a non-matching line
internal/relay/headless.go        drainStream: when b.Builder.StreamSessionID == "" try transcript.SessionID on each drained line until one answers; startRound: reset StreamSessionID; clearProcess: keep it (the round is what it belongs to; queueReport reads it before the reset)
internal/relay/reconcile.go       queueReport: entry.BuilderSession = builderSessionOf(b); round close clears Builder.StreamSessionID
internal/relay/session.go         + builderSessionOf(b store.Binding) *store.BuilderSession
internal/relay/headless_test.go   + TestDrainStreamRecordsSessionIDOnce, TestReportEntryCarriesHeadlessSession
internal/relay/reconcile_test.go  + TestReportEntryCarriesPaneSession, TestReportEntryNoSessionIsNil
internal/relay/logline.go         LogLine: report entries append " session=<kind>:<id8>" (first 8 chars) when set
internal/relay/logline_test.go    + case
README.md                         one paragraph under the round log / show section: builder_session on report entries, how it is learned, when it is absent
docs/plans/2026-09-21-w2k2a-builder-session.md   copy of this plan
```

## 3. Data structures

```
// internal/store/log.go
type BuilderSession struct {
    Kind string `json:"kind"` // harness kind: claude | opencode | agy | codex
    ID   string `json:"id"`   // the harness's own session/conversation/thread id, verbatim
}
LogEntry.BuilderSession *BuilderSession `json:"builder_session,omitempty"`   // report entries only; nil when unknown

// internal/store/types.go  Endpoint
StreamSessionID string `json:"stream_session_id,omitempty"`
    // headless only: the session id the round's stream announced. Set once per round by drainStream, cleared by startRound.
    // Pane builders keep using SessionID (from herdr).
```

## 4. Interfaces

```
// internal/transcript/session.go
func SessionID(kind string, line []byte) string
    // json-decodes line into map[string]any (non-JSON -> ""); per kind:
    //   claude:   str(obj["session_id"])                       (any event type carries it; first seen wins at the caller)
    //   opencode: str(obj["sessionID"])
    //   agy:      str(obj["conversation_id"]); if "" and obj["event"]=="init": str(asMap(obj["init"])["conversation_id"])
    //   codex:    obj["type"]=="thread.started" -> str(obj["thread_id"]); else ""
    //   other:    ""
    // never returns the relay-exit trailer or a non-string

// internal/relay/session.go
func builderSessionOf(b store.Binding) *store.BuilderSession
    // headless: if b.Builder.StreamSessionID != "" -> &{Kind: b.Builder.Kind, ID: StreamSessionID}
    // pane:     if b.Builder.SessionID != ""       -> &{Kind: b.Builder.Kind, ID: SessionID}
    // remote / neither: nil

// internal/relay/headless.go drainStream (the per-tick renderer of NNN-builder.jsonl -> NNN-builder.log):
    // for each newly drained raw line, before/after rendering: if b.Builder.StreamSessionID == "" { if id := transcript.SessionID(b.Builder.Kind, line); id != "" { b.Builder.StreamSessionID = id } }
    // drainStream must therefore return the (possibly updated) binding, or take *store.Binding -- pick whichever matches its current signature with the least churn and say which; its caller in reconcileHeadless persists the binding anyway.
// startRound: b.Builder.StreamSessionID = "" before Runner.Start (a new round, a new session)
// queueReport (reconcile.go ~:764): entry.BuilderSession = builderSessionOf(b); in the round-close block: b.Builder.StreamSessionID = ""
// LogLine: for KindReport with BuilderSession != nil: append fmt.Sprintf(" session=%s:%s", s.Kind, short8(s.ID))
```

## 5. Pseudocode

Covered by §4. The stream is read by `drainStream` already (cursor
`StreamRound/StreamOffset`); this adds one string check per new line until
the id is known, then nothing.

## 6. Error handling

- A stream that never announces an id (or a harness relay does not know)
  leaves `StreamSessionID` empty and the report entry's `BuilderSession`
  nil. Never an error, never a guess.
- A mid-round switch to a new headless process: `startRound` resets the id,
  so the report names the builder that finished the round (the switch log
  entry names the earlier one).

## 7. Ordered implementation steps

Commit prefix: exactly **one** `feat(session):` commit for the code
(squash step commits, or `chore:`/`test:` per step); the plan copy may be
its own `chore(plans):` commit.

### Task 1 -- the pure parser

**Files:** `internal/transcript/session.go`, `session_test.go`.

**Test first:** table over `internal/usage/testdata/{claude,opencode,agy,codex}-stream.jsonl` line 1 -> `sess-1`, `ses_1`, `conv-1`, `01a0bb3b-6da3-79d1-a85d-9ea76187d710`; a claude `assistant` line (line 2 of the claude fixture) also yields `sess-1`; an agy `step_update` line yields its nested `conversation_id` if present else `""`; the trailer `relay-exit:0` -> `""`; unknown kind -> `""`. **Mutation check:** return `""` for opencode and the table fails on that row.

**Verify:** `go test -count=1 ./internal/transcript/`.

### Task 2 -- record it

**Files:** `internal/store/log.go`, `types.go`, `internal/relay/headless.go`, `reconcile.go`, `session.go`, `logline.go`, and the tests named in §2.

**Tests first**
- `TestDrainStreamRecordsSessionIDOnce`: a sent headless binding; write the claude fixture's first two lines to `BuilderStreamPath`; tick -> `Builder.StreamSessionID == "sess-1"`; append a line with a different `session_id` (a sub-agent's, say) and tick -> still `sess-1`.
- `TestReportEntryCarriesHeadlessSession`: continue: write the report and marker, exit 0; after close the `KindReport` entry has `BuilderSession == &{claude sess-1}` and the binding's `Builder.StreamSessionID == ""`. **Mutation check:** drop the `entry.BuilderSession =` line and this fails.
- `TestReportEntryCarriesPaneSession` (`reconcile_test.go`, on the `sentBinding` pane fixture whose agent list carries a session value -- see `builderAgent` and `refreshEndpoint`): after a marker close the report entry has `BuilderSession == &{<kind> <that session>}`.
- `TestReportEntryNoSessionIsNil`: pane fixture whose agent has no session value -> nil; JSON of the entry has no `builder_session` key.
- `TestLogLineSessionSuffix` (`logline_test.go`).
- `startRound` reset: extend `TestStartRoundRecordsTheHandleAndTheLogPath` (~:139) with a pre-set `StreamSessionID` that is empty after start.

**Then** the code per §4. **Verify:** `go test -race -count=1 ./internal/relay/ ./internal/store/`.

### Task 3 -- README, plan copy, gate

README: in the section describing the round log entries (grep -n "gate=" README.md for the report-entry vocabulary) add one paragraph: `builder_session` on report entries, learned from the headless stream's first event or herdr's session for a pane, absent when unknown; `relay log --json` / `relay show --json` print it; it is what `ask --round` (coming) resumes.

Copy the plan file to `docs/plans/2026-09-21-w2k2a-builder-session.md` and commit.

Gate, in the foreground, stop at the first failure:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```
`make e2e` is the planner's (this touches `reconcile.go`/`headless.go`); say so.

Final commit message: `feat(session): a closed round's report entry names the builder session that built it, read from the headless stream or herdr (#147)`

## Report

Per task: what was done, test names, verify output, the mutation checks'
failing tests, the drainStream signature decision. Commit shas (one
`feat:`). If any step was impossible as written, say which and stop there.
