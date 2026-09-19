# Pane builders' terminal tab: render the harness's own session record into NNN-builder.log (#184, claude first)

Closes the claude half of #184; the issue stays open for opencode (its
record is a sqlite table mutated in place, which needs a different cursor)
and agy (no record exists). Design in this file; no separate spec.

Decisions taken by the planner:

- **Claude only this round.** The locator returns "no record" for every
  other kind, and those pane builders keep today's screen capture exactly.
- **The daemon renders, the ui reads** -- same as headless (#168): the
  session file is append-only, so the existing byte cursor
  (`Endpoint.StreamRound` / `StreamOffset`) works unchanged; a pane
  builder's round log is cut per round at `Send` (cursor = the record's
  size at send time) so `NNN-builder.log` holds round N alone.
- **Locate by glob**, `~/.claude/projects/*/<SessionID>.jsonl`, never by
  slugging the cwd: the issue verified the slug can be the main repo, and
  this machine also has worktree slugs. The session id is the file name.
- **Session records are a different shape from the stream**: they carry
  ~18 housekeeping record types the stream never has. A new
  `transcript.RenderRecord` renders `assistant`/`user` exactly as the
  stream renderer does, renders the human/planner prompt as `> …`, and
  renders **everything else as nothing** -- the stream rule "unknown
  renders as `[type]`" is wrong for a record file, where unknown is
  housekeeping.
- **The ui falls back to the capture** until the round's log exists (the
  record is not located yet, or the round has not produced a line), so the
  tab is never blank where it was not blank before.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else.

**Scope guard.** Touch only the files listed in §2. Do not run `make e2e`
(the planner runs it). Do not add a CLI verb or flag, a module dependency,
or any sqlite code. Do not touch `internal/usage`. Run every command in the
foreground; dispatch no sub-agents. Every exported signature you add or
change is named in §4; change no other.

**Commits.** ONE `feat(ui):` commit for the whole feature (squash your step
commits before the gate). Subject:
`feat(ui): pane builders' terminal tab renders the claude session record per round (#184)`.

**Commit the plan with the work.** Copy this plan file to
`docs/plans/2026-09-19-pane-transcript.md` in your worktree and include it.

**Before step 1**: `git status --short` and `git branch --show-current`; you
must be on `relay/<binding-name>` under `~/.local/state/relay/.worktrees/`
with a clean tree. Otherwise halt.

## 1. System overview

`relay ui`'s terminal tab for a **pane** builder is one viewport of
`herdr agent read <pane> --source recent-unwrapped` (`internal/ui/fetch.go:211-265`),
re-read every tick: no history, no styling, and for an alternate-screen TUI
mostly nothing. For a **headless** builder the same tab is the round's
`NNN-builder.log`: the daemon's `drainStream` (`internal/relay/headless.go:132`)
renders each new stream line with `transcript.Render` past a byte cursor
and appends it; the ui reads the last 5000 lines, follows the tail, and
colours the `● Tool(arg)` / `⎿ ok:` markers (`internal/ui/detail.go:59`).

A claude pane builder keeps its own record at
`~/.claude/projects/<slug>/<SessionID>.jsonl`, and relay already holds
`Endpoint.SessionID` (recorded at bind, backfilled by `refreshEndpoint`).
Its `assistant`/`user` records carry the same `message.content` blocks the
stream does. So the daemon can drain that file into `NNN-builder.log` the
way it drains a headless stream, and the ui can show the log the way it
does for headless -- which is the whole change:

- `Send` (pane branch) arms the cursor: `StreamRound = Round`,
  `StreamOffset = size of the located record (0 when not located)`,
  `LogPath = BuilderLogPath(name, Round)`.
- Every pane tick, after `refreshEndpoint`, `drainSession` locates the
  record via `rt.Sessions` and appends the rendered new lines.
- `fetchTerminal` reads the round log when it exists, else captures.
- `switchBuilder` re-arms the cursor for the replacement pane (offset 0:
  a fresh session's file starts empty; the round's log keeps appending,
  after the existing `switched to …` marker line).

## 2. File structure

```
internal/transcript/
  record.go            NEW   RenderRecord(kind, line) []string; claude record table
  record_test.go       NEW   table over record types
internal/relay/
  session.go           NEW   SessionLocator, HomeSessionLocator, armSessionCursor, drainSession
  session_test.go      NEW   locator, arm, drain tests (real temp files, fake locator)
  herdr.go             EDIT  Runtime.Sessions SessionLocator
  headless.go          EDIT  extract drainFile from drainStream; drainStream becomes a wrapper (behaviour identical)
  reconcile.go         EDIT  pane path: b = drainSession(rt, b) after the two refreshEndpoint calls
  send.go              EDIT  pane branch: b = armSessionCursor(rt, b) before RoundStartedAt is stamped
  switch.go            EDIT  pane branch after `b.Builder = ep`: re-arm with offset 0
  send_test.go         EDIT  TestSendArmsSessionCursorForPaneBuilder
  reconcile_test.go    EDIT  TestReconcileDrainsPaneSessionRecord
  switch_test.go       EDIT  TestSwitchRearmsSessionCursor
cmd/relay/
  main.go              EDIT  newRuntime: Sessions: relay.HomeSessionLocator(home)  (home is resolved at :331)
internal/ui/
  fetch.go             EDIT  tabContent.transcript, tabContent.logName; logTab helper; pane branch reads the log when present
  detail.go            EDIT  bodyOf gates transcript colouring on (headless || c.transcript)
  pane.go              EDIT  sourceLine: transcript source line for pane builders
  fetch_test.go        EDIT  TestFetchTerminalPaneReadsRoundLog, TestFetchTerminalPaneFallsBackToCapture
  pane_test.go         EDIT  sourceLine / bodyOf cases for a pane transcript
docs/specs/2026-09-17-relay-ui-split-design.md  EDIT  two sentences amended (§7)
README.md              EDIT  one paragraph (§7)
docs/plans/2026-09-19-pane-transcript.md  NEW  copy of this plan
```

Grep before you start: `grep -rn "drainStream(" --include=*.go .` -- the
only production caller must be `headless.go:273`. `grep -rn "bodyOf("` --
seven test call sites pass a bool; §4 keeps that signature so none change.

## 3. Data structures & type definitions

### `internal/relay/session.go`

```
// SessionLocator finds a pane harness's own session record for a builder,
// or reports that there is none: ok is false for a kind that keeps no
// record relay can read (agy, opencode this round), for an empty
// sessionID, and when the file is not (yet) on disk. Pure apart from the
// filesystem; cmd/relay wires HomeSessionLocator, tests wire a map.
type SessionLocator func(kind, sessionID string) (path string, ok bool)
```

`Runtime` gains, next to `Usage`:

```
// Sessions locates a pane builder's own session record so the daemon can
// render it into the round log the way it renders a headless stream
// (#184). Nil means pane builders keep the screen capture; tests that do
// not set it behave exactly as before.
Sessions SessionLocator
```

No new `store` fields: `Endpoint.StreamRound`, `StreamOffset` and
`LogPath` are reused with their documented meaning ("they belong to the
round's file"). Update the `Endpoint` doc comment on `StreamRound` in
`internal/store/types.go`? **No** -- the comment says "the round whose
builder stream … the daemon is rendering into that round's log", which
stays true; leave the file untouched.

### `internal/ui/fetch.go` -- `tabContent`

```
transcript bool   // the body is a rendered round log (headless stream or pane session record): colour markers, show the log source line
logName    string // base name of that log ("003-builder.log"); "" for a capture
```

## 4. Interface definitions & component contracts

### `internal/transcript/record.go`

```
// RenderRecord turns one line of a harness's own session record into the
// lines to append to the log (#184). It differs from Render in what
// "unknown" means: a record file is a superset of the stream with
// housekeeping records the stream never has, so an unknown record type,
// a non-JSON line, or a kind with no record table renders as nothing.
// Never errors, never panics.
func RenderRecord(kind string, line []byte) []string
```

Claude table (`renderClaudeRecord(obj)`):

| record `type` | renders |
|---|---|
| `assistant` | exactly `renderClaude(obj)` (tool_use -> `toolLine`, text -> the text) |
| `user` with a `tool_result` block | exactly `renderClaude(obj)` (`⎿ ok:` / `⎿ error:`) |
| `user` whose `message.content` is a string, or a list whose blocks are all `text` | one line `"> " + first non-empty line of the text, truncated to maxArg runes with "…"` -- the prompt relay (or the human) typed |
| anything else (`attachment`, `permission-mode`, `mode`, `last-prompt`, `atis-latch`, `agent-setting`, `queue-operation`, `file-history-snapshot`, `file-history-delta`, `system`, `summary`, `pr-link`, …) | nil |

Any kind other than `claude` -> nil.

### `internal/relay/session.go`

```
// HomeSessionLocator locates claude's record under home:
// filepath.Glob(home/.claude/projects/*/<sessionID>.jsonl); when several
// match (a session copied between slugs) the newest by mtime wins. A
// sessionID containing a path separator or a glob metacharacter is
// refused (ok false) -- it came from herdr, but it is used in a path.
func HomeSessionLocator(home string) SessionLocator

// armSessionCursor prepares a pane builder's round log (#184): StreamRound
// = b.Round, StreamOffset = the located record's current size (0 when
// rt.Sessions is nil, the id is empty, or the file is not located), and
// LogPath = rt.Store.BuilderLogPath(b.Name, b.Round). Never fails: a stat
// error is offset 0. Not for headless or remote builders (they have
// startRound); the caller guards.
func armSessionCursor(rt Runtime, b store.Binding) store.Binding

// drainSession is drainStream for a pane builder (#184): the located
// record past StreamOffset, rendered with transcript.RenderRecord, appended
// to BuilderLogPath(name, StreamRound). Unchanged binding (and no I/O)
// when rt.Sessions is nil, the builder is headless or remote, SessionID is
// "", StreamRound is 0, or the record is not located. Same cursor rules as
// drainStream: partial trailing line waits; an offset past EOF resets to 0
// with a warning; the cursor advances only after the append succeeded.
func drainSession(rt Runtime, b store.Binding) store.Binding
```

### `internal/relay/headless.go`

```
// drainFile appends render(line) for every complete line of src past off
// to logPath and returns the new offset. It is the shared body of
// drainStream and drainSession (#184): it never fails the tick -- every
// problem is a slog.Warn (with what) and the offset unchanged, except an
// offset past EOF, which resets to 0. what names the source in warnings
// ("stream", "session").
func drainFile(logPath, src string, off int64, render func(line []byte) []string, what string, fields ...any) int64
```

`drainStream` keeps its signature and behaviour exactly: it computes the
paths, calls `drainFile(..., func(l []byte) []string { return transcript.Render(b.Builder.Kind, l) }, "stream", "binding", b.Name, "round", round)`
and stores the offset. Every existing `headless_test.go` drain test must
pass unchanged -- that is the refactor's check.

### `internal/relay/send.go`, `switch.go`, `reconcile.go`

- `Send`, inside the `WithLock` closure, in the **pane** branch (the `else`
  of `if b.Builder.Headless()` that calls `promptWithRetry`), after the
  prompt landed (or landed late) and before the `entry :=` plan log entry:
  `b = armSessionCursor(rt, b)`. Guard: `!b.Builder.Headless() && !b.Builder.Remote()`
  (the branch already implies it; state the guard anyway).
- `switchBuilder`, immediately after `b.Builder = ep` (switch.go:165), when
  `!ep.Headless()`: `b.Builder.StreamRound, b.Builder.StreamOffset, b.Builder.LogPath = b.Round, 0, rt.Store.BuilderLogPath(b.Name, b.Round)`.
  The `appendLogMarker` "switched to …" line currently runs only for
  headless; make it run for both modes (the pane log exists now too).
- `Reconcile`, pane path, right after the planner `refreshEndpoint` block
  (reconcile.go:198-201): `b = drainSession(rt, b)`.

### `cmd/relay/main.go`

In `newRuntime`, where `home` is resolved for the usage reader (`:331`),
add `Sessions: relay.HomeSessionLocator(home)` to the `relay.Runtime`
literal.

### `internal/ui`

```
// logTab reads a round log for the terminal tab: the last headlessLogLines
// lines, transcript true, logName the file's base name. missing is what
// the tab says when the file does not exist yet. (Extracted from the
// headless branch; that branch now calls it.)
func logTab(name, logPath string) (tabMsg, bool)   // ok false when the file cannot be read
```

`fetchTerminal` pane branch: before `ListAgents`, if
`b.Builder.StreamRound != 0` and `logTab(name, rt.Store.BuilderLogPath(name, b.Builder.StreamRound))`
returns ok -> return it. Otherwise the capture path, unchanged, with
`transcript: false`.

`bodyOf(t tab, c tabContent, headless bool) string` -- signature kept;
the gate becomes `t == tabTerminal && (headless || c.transcript)`.

`sourceLine`, `tabTerminal` case: when `c.transcript`:
`src := "headless"` if `m.detail.headless` else `"pane"`; `src += " · " + c.logName`
when `c.logName != ""`; then the existing `"%s · %d lines · %s"` with
following/scrolled. When not transcript: the capture line, unchanged.

## 5. High-level pseudocode

```
Send (pane):
    promptWithRetry(...)
    b = armSessionCursor(rt, b)
        path, ok := rt.Sessions(b.Builder.Kind, b.Builder.SessionID)   (nil Sessions -> ok false)
        off := 0; if ok { if info, err := os.Stat(path); err == nil { off = info.Size() } }
        b.Builder.StreamRound, b.Builder.StreamOffset = b.Round, off
        b.Builder.LogPath = BuilderLogPath(b.Name, b.Round)
    ... plan entry, RoundStartedAt, Save

Reconcile (pane), each tick:
    b.Builder = refreshEndpoint(b.Builder, builder)        // SessionID backfilled here
    b = drainSession(rt, b)
        preconditions (see §4) else return b
        path, ok := rt.Sessions(kind, id); if !ok return b
        b.Builder.StreamOffset = drainFile(BuilderLogPath(name, StreamRound), path, StreamOffset,
                                           RenderRecord(kind, ·), "session", "binding", name, "round", StreamRound)
    ... rest of the tick unchanged

fetchTerminal (pane):
    if b.Builder.StreamRound != 0:
        if msg, ok := logTab(name, BuilderLogPath(name, StreamRound)); ok: return msg
    capture as today
```

Cursor semantics on the record: `StreamOffset` is a byte offset into the
session file; round N's log starts at the file size when round N was sent,
so a session that already held rounds 1..N-1 contributes nothing of them
to round N's log. When the record was not located at send (round 1, file
not yet created; or SessionID still empty), offset 0 means "from the
start of the file", which for round 1 is exact and for a later round
includes earlier turns -- acceptable and documented in the comment.

## 6. Error handling strategy

- Nothing in this plan returns a new error. `drainSession` and
  `armSessionCursor` are best-effort like `drainStream`: warn and leave the
  binding unchanged; the cursor advances only after a successful append.
- A locator must never panic on odd ids: the metacharacter guard in
  `HomeSessionLocator` returns ok false.
- The ui's `logTab` returns ok false on any read error and the pane branch
  falls back to the capture; the headless branch keeps its existing
  "log not written yet" prose (pass that through `logTab`'s second return
  or keep its own `os.ReadFile` error branch -- either, but the headless
  tab's observable text must not change).

## 7. Documentation

`docs/specs/2026-09-17-relay-ui-split-design.md`:
- lines 246-247: replace `A pane builder's terminal tab stays a
  viewport-sized live screen capture: there is nothing above it to scroll
  to.` with `A pane builder whose harness keeps a session record relay can
  read (claude, #184) gets the same log view, cut per round at send; any
  other pane builder's terminal tab stays a viewport-sized live screen
  capture: there is nothing above it to scroll to.`
- line 254: replace `A pane builder's capture is never restyled.` with
  `A screen capture is never restyled; a pane builder's rendered round log
  is styled like a headless one.`

`README.md`, under `### Interactive reader: relay ui` (find the sentence
describing the terminal tab), add:

> For a claude pane builder the terminal tab is the round's own transcript:
> the daemon renders the harness's session record
> (`~/.claude/projects/*/<session>.jsonl`) into `NNN-builder.log` from the
> moment the round was sent, so the tab scrolls, follows the tail and is
> styled exactly as a headless builder's. opencode and agy pane builders
> keep the live screen capture (#184).

## 8. Tests

`internal/transcript/record_test.go` -- `TestRenderRecord`, table:
- assistant with `tool_use{name: Read, input: {file_path: x}}` -> one line
  equal to `Render("claude", sameLine)` (assert equality with the stream
  renderer, not a literal).
- user with `tool_result{content: "ok text"}` -> `Render` equality.
- user with `content: "please do X\nsecond line"` -> `["> please do X"]`.
- user with `content: [{type: text, text: "hi"}]` -> `["> hi"]`.
- a 300-rune prompt -> truncated to maxArg with a trailing "…".
- `{"type":"attachment", ...}`, `{"type":"file-history-snapshot"}`,
  `{"type":"system"}`, `{"type":"summary"}` -> nil each.
- non-JSON line `relay-exit: 0` -> nil (unlike `Render`).
- kind `opencode` with an assistant record -> nil.

`internal/relay/session_test.go`:
- `TestHomeSessionLocatorGlobsAnySlug`: temp home with
  `.claude/projects/-a-slug/S.jsonl` -> found; a second slug with an older
  file -> the newest wins; missing -> ok false; kind `opencode` -> ok
  false; id `../x` and `*` -> ok false without touching the fs.
- `TestArmSessionCursorUsesRecordSize`: locator maps to a 120-byte temp
  file -> StreamOffset 120, StreamRound = Round, LogPath set; not located
  -> offset 0; nil `Sessions` -> offset 0 and still StreamRound/LogPath set.
- `TestDrainSessionAppendsRenderedRecords`: temp record with two complete
  assistant/user lines and one partial line; binding pane/claude with
  SessionID and StreamRound 1, offset 0 -> log has the two rendered lines,
  offset = bytes through the second newline; append the rest of the
  partial line + "\n" -> next call appends one more line. A `mode`
  housekeeping record in between -> no line for it.
- `TestDrainSessionPreconditions`: nil Sessions / empty SessionID /
  StreamRound 0 / headless endpoint -> binding unchanged and no log file.
- `TestDrainSessionOffsetPastEOFResets`: offset 999 on a 50-byte file ->
  renders from 0, offset = file size.
  **Mutation check (run and report):** in `drainSession`, skip the
  `StreamOffset` write-back; `TestDrainSessionAppendsRenderedRecords` fails
  on the second call (lines rendered twice).

`internal/relay/headless_test.go`: no edits; every existing `drainStream`
test passes after the `drainFile` extraction (name the ones you ran).

`internal/relay/send_test.go` -- `TestSendArmsSessionCursorForPaneBuilder`:
an existing pane `Send` fixture plus `rt.Sessions` mapping to a temp file of
N bytes -> after send `Builder.StreamRound == Round`, `StreamOffset == N`,
`LogPath == BuilderLogPath`. Headless send: `startRound` still owns the
cursor (offset 0, existing behaviour; assert one existing headless test
still holds).

`internal/relay/reconcile_test.go` -- `TestReconcileDrainsPaneSessionRecord`:
`sentBindingWithBuilderSession(t, f, "S")` with `rt.Sessions` mapping
`("claude","S")` to a temp file; write one assistant record; set
`Builder.StreamRound = 1` on the binding (as Send would); one tick ->
`BuilderLogPath(name,1)` holds the rendered line and the saved binding's
offset advanced. With `rt.Sessions` nil -> no log file (existing tests
already cover this implicitly; assert it once).

`internal/relay/switch_test.go` -- `TestSwitchRearmsSessionCursor`: after a
pane switch, `Builder.StreamRound == Round`, `StreamOffset == 0`, `LogPath`
set, and the round log contains the `switched to` marker line.

`internal/ui/fetch_test.go`:
- `TestFetchTerminalPaneReadsRoundLog`: binding with `Builder.StreamRound
  = 2`, log file written at `st.BuilderLogPath(name, 2)` -> body is the
  log, `transcript == true`, `logName == "002-builder.log"`, and the fake
  herdr's `ReadAgent` was NOT called (the ui fake errors on unexpected
  calls, or count reads).
- `TestFetchTerminalPaneFallsBackToCapture`: `StreamRound = 2` but no log
  file -> today's capture path (`ReadAgent` called once), `transcript ==
  false`.
- The two existing terminal tests still pass unchanged.

`internal/ui/pane_test.go`: `bodyOf(tabTerminal, c{transcript:true}, false)`
colours markers; `sourceLine` for a pane transcript reads
`pane · 002-builder.log · N lines · following`.

## 9. Ordered implementation steps

1. `internal/transcript/record.go` + `record_test.go`.
   `go test ./internal/transcript`.
2. `headless.go`: extract `drainFile`; `go test ./internal/relay -run
   'Drain|Stream|Headless' -count=1` green with no test edits.
3. `session.go` + `herdr.go` field + `session_test.go`; mutation check.
4. `send.go`, `switch.go`, `reconcile.go` call sites + their three tests.
5. `cmd/relay/main.go` wiring; `go build ./... && go vet ./cmd/relay`.
6. `internal/ui` fetch/detail/pane + tests.
7. Docs per §7.
8. Copy the plan to `docs/plans/2026-09-19-pane-transcript.md`. Squash to
   the one `feat(ui):` commit. Gate, exactly:
   ```
   test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
   make check
   ```
   Report the tail verbatim, the mutation outcome, and
   `git diff --stat main..HEAD`.

## 10. Report

End `NNN-report.md` with the ```relay block (`status`, `halted_at`,
`changed_paths`, `commands_run`, `not_done`). State that the daemon-path
change is not live until the planner runs `make service`, and that
`make e2e` is owed by the planner (the pane tick changed).
