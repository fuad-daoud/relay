# Status without herdr: rows render when `herdr agent list` fails

Issue context: `relay ui` on a box with no herdr server showed only
`list agents: herdr agent list: no herdr server is running ...` and
`cannot reach herdr`, even though every builder there was headless and
headless status never touches herdr (`headlessStatus`, `internal/relay/headless.go:527`).

If any step below is impossible as written or contradicts the code, **halt
and report** -- do not improvise a different design.

## 1. System overview

`relay.Status` (`internal/relay/status.go:203`) calls `rt.Herdr.ListAgents`
before building any row and returns its error, so a failed herdr lookup
fails the whole report. Only two things in a row actually need the agent
list: the planner's pane status and a *pane* builder's status. Headless and
remote builders, log-derived fields, gates, usage and waiting all come from
the store. `PlannerStatus` (`internal/relay/statusline.go:217`) already
builds rows with `agents == nil` and no herdr call, which proves nothing
downstream requires agents.

This round makes `Status` degrade instead of fail: when `ListAgents` errors,
the report still carries every row, the report says herdr was unreachable,
and every endpoint relay could not look up reads `unknown` -- **not**
`gone`, because relay did not ask and must not claim absence (spec §7.4,
"what relay acts on is what it shows"). `relay status` prints the herdr
error as a header line; `relay ui` renders the rows and adds a footer note.

Callers of `Status` are all read-only: `cmd/relay/main.go:1384` (`status`),
`internal/ui/fetch.go:85`, `internal/pick/model.go:70`,
`internal/serve/admin.go:44` (stub herdr, never errors). The daemon's own
`ListAgents` in `daemon.go:89` is untouched -- reconcile must still refuse
to act blind.

**Scope guard.** Another round (`persist`, branch `relay/persist`) is in
flight on `internal/db/*`, `internal/store/store.go`, `internal/history/*`,
`internal/relay/herdr.go`, `cmd/relay/main.go`, `cmd/relay/db.go`,
`README.md`. **Do not edit any of those files.** In particular do not touch
`cmd/relay/main.go`: `filterReport` there already drops `Gated` for a
single-binding view and will drop the new field the same way; that is
accepted for this round.

## 2. File structure

```
internal/relay/status.go          Report.HerdrError; agentUnknown; Status degrades; buildReport/statusRow take the absent-status word; RenderStatus header line
internal/relay/statusline.go      PlannerStatus passes nil herdr error (one-line call-site change)
internal/relay/status_test.go     three new tests, statusRow call sites gain the new argument
internal/ui/model.go              footer note when report.HerdrError != ""
internal/ui/split_test.go         one new footer test
docs/plans/2026-09-20-status-without-herdr.md   this plan (already present)
```

No new files besides tests. No changes to `internal/serve`, `internal/pick`,
`cmd/relay`.

## 3. Data structures & type definitions

### `Report` (`internal/relay/status.go`, existing struct) -- add one field

| field | type | JSON | meaning |
|---|---|---|---|
| `HerdrError` | `string` | `herdr_error,omitempty` | The error text herdr returned when `Status` asked for its agents. Empty when herdr answered (including an empty agent list). When set, the report's rows were built from the store alone and every pane endpoint reads `unknown`. |

Constraint: empty string exactly when the lookup succeeded. Never set by
`PlannerStatus`. Absent from JSON when empty so a consumer that never learned
the field sees the document it always did (same rule as `DoneHidden`,
`Gated`).

### Constant (`internal/relay/status.go`, next to `agentGone`)

```
agentUnknown = "unknown"   // status for a pane endpoint relay could not look up because herdr did not answer
```

The word `unknown` is already what `headlessStatus` and the remote path use
for "could not determine"; the meaning is identical.

## 4. Interface definitions & component contracts

### `Status(ctx, rt) (Report, error)` -- contract change

- Precondition: unchanged.
- Postcondition, new: returns a non-nil error **only** for store failures
  (`rt.Store.List`, `ReadLog`) -- never for `rt.Herdr.ListAgents`. On a
  herdr error `err`: `Report.HerdrError = err.Error()`, rows are built with
  `agents == nil` and the absent word `agentUnknown`. On success `HerdrError
  == ""` and the absent word is `agentGone`, exactly as today.

### `buildReport(ctx, rt, bindings, agents, herdrErr error) (Report, error)` -- signature change

- `herdrErr` nil: today's behaviour. Non-nil: sets `rep.HerdrError` and
  passes `agentUnknown` to every `statusRow`.
- Callers: `Status` (passes the ListAgents error or nil), `PlannerStatus`
  (passes `nil`).

### `statusRow(ctx, rt, b, agents, known, absent string) (BindingStatus, error)` -- signature change

- `absent` is the initial `PlannerStatus` and `BuilderStatus` for the row,
  replacing the hard-coded `agentGone` at `status.go:251-252`. Everything
  found in `agents`, or headless, or remote, or gated, overrides it exactly
  as today. `absent` must be one of `agentGone`, `agentUnknown`.
- All five test call sites in `status_test.go` pass `agentGone`.

### `RenderStatus(r Report) string` -- output change

When `r.HerdrError != ""`, the very first line of the output is

```
herdr unreachable: <HerdrError>; pane statuses unknown
```

followed by one blank line, then today's output unchanged (including the
`no bindings` case). When empty, output is byte-identical to today.

### UI footer (`internal/ui/model.go`, the `notes` block at ~line 446)

When `m.report.HerdrError != ""`, append `errorStyle.Render("! herdr unreachable")`
to `notes`, placed immediately after the `! refresh failed (retrying)`
slot (i.e. after the `m.err != nil` check, before the NEEDS YOU loop). The
error text itself is not shown in the footer -- same rule the refresh
marker follows (`list_test.go:463`). Nothing else in the UI changes: rows
render, the rail shows `unknown` where it showed `gone`.

## 5. High-level pseudocode

```
Status(ctx, rt):
    bindings <- rt.Store.List()             ; error -> return
    agents, herdrErr <- rt.Herdr.ListAgents(ctx)
    if herdrErr != nil: agents <- nil       ; do NOT return
    return buildReport(ctx, rt, bindings, agents, herdrErr)

buildReport(ctx, rt, bindings, agents, herdrErr):
    absent <- agentGone
    if herdrErr != nil: absent <- agentUnknown
    known <- knownEndpoints(bindings)
    for b in bindings:
        row <- statusRow(ctx, rt, b, agents, known, absent) ; error -> return
        rows += row
    sort rows by Name
    rep <- Report{Bindings: rows, Gated: Gates(rt)}
    if herdrErr != nil: rep.HerdrError <- herdrErr.Error()
    return rep

statusRow(..., absent):
    row.PlannerStatus <- absent ; row.BuilderStatus <- absent
    (rest unchanged)

RenderStatus(r):
    if r.HerdrError != "": write "herdr unreachable: " + r.HerdrError + "; pane statuses unknown\n\n"
    (rest unchanged)
```

## 6. Error handling strategy

- Store errors: non-recoverable for the report, propagate as today.
- herdr `ListAgents` error: recoverable -- degraded report, carried as data
  in `HerdrError`, surfaced once as a header line (`relay status`) or a
  footer marker (`relay ui`). Never logged separately; the report is the
  log.
- No change to how the UI handles a non-nil `statusMsg.err` (that path
  still covers store errors and still renders `cannot reach herdr — see the
  error above` before the first good report; the existing test at
  `list_test.go:365` stays as is, since it drives `statusMsg{err}` directly).

## 7. Ordered implementation steps

Run from the worktree root. Do not commit until step 6.

### Step 1 -- failing tests first (`internal/relay/status_test.go`)

Add, using the existing `fakeHerdr` and `sentBinding` helpers (note the
fake's `listErr` fails every `ListAgents` call after the first; `sentBinding`
already makes several calls, so setting `f.listErr` *after* `sentBinding`
makes the next `Status` call fail):

1. `TestStatusDegradesWhenHerdrUnreachable`: after `sentBinding`, set
   `f.listErr = errors.New("no herdr server")`. `Status` returns `err == nil`,
   `len(rep.Bindings) == 1`, `rep.HerdrError == "no herdr server"`,
   `rep.Bindings[0].PlannerStatus == "unknown"`,
   `rep.Bindings[0].BuilderStatus == "unknown"`.
2. `TestStatusHerdrErrorEmptyOnSuccess`: after `sentBinding` with
   `f.agents = nil` (no listErr), `rep.HerdrError == ""` and
   `BuilderStatus == "gone"` -- pins that an *answered* empty list is still
   `gone`, not `unknown`. (Extend `TestStatusMarksMissingAgentsAsGone` with
   the `HerdrError == ""` assertion instead of a new test if you prefer;
   either is acceptable.)
3. `TestRenderStatusHerdrHeader`: `RenderStatus(Report{HerdrError: "boom"})`
   starts with exactly `"herdr unreachable: boom; pane statuses unknown\n\n"`
   and then contains `"no bindings\n"`; `RenderStatus(Report{})` does **not**
   contain `"herdr unreachable"`.
4. `TestStatusJSONCarriesStructuredFields` (existing, line 294): add an
   assertion that the marshalled JSON of a report with `HerdrError == ""`
   does not contain the key `"herdr_error"`.

Also update the five `statusRow(...)` call sites in this file to pass
`agentGone` as the new last argument.

**Verify:** `go test ./internal/relay/ -run 'TestStatusDegrades|TestRenderStatusHerdrHeader'`
fails to compile / fails. That is the expected state at the end of step 1.

### Step 2 -- `internal/relay/status.go`

Implement §3/§4/§5 in `status.go` only: the constant, the field, the
`Status` change, the `buildReport` and `statusRow` signatures, the
`RenderStatus` header. Update the doc comment on `Status` ("derives every
row live from herdr, so it cannot disagree with reality") to say herdr
failing to answer is reported, not fatal, and that unlooked-up endpoints
read `unknown`.

**Verify:** `go build ./...` fails only in `statusline.go` (one call site).

### Step 3 -- `internal/relay/statusline.go`

Change `PlannerStatus`'s call to `buildReport(ctx, rt, kept, nil, nil)`.
No other change; its doc comment ("never probes herdr") stays true.

**Verify:** `go build ./... && go test ./internal/relay/` green, including
every test from step 1.

### Step 4 -- UI footer (`internal/ui/model.go`, `internal/ui/split_test.go`)

Test first: in `split_test.go` next to `TestFooterNoticesAndRefreshAge`
(line 169), add `TestFooterMarksHerdrUnreachable`: a model that received a
successful `statusMsg{report: relay.Report{HerdrError: "no herdr server", Bindings: [one ACTIVE row]}}`
renders a footer containing `! herdr unreachable`, does **not** contain the
text `no herdr server`, does **not** contain `refresh failed`, and the view
still contains the row's name; the same model given a report with
`HerdrError == ""` has no `herdr unreachable` in the footer. Follow the
model-construction pattern already used in that file.

Then add the one `notes = append(...)` line described in §4.

**Verify:** `go test ./internal/ui/` green.

### Step 5 -- mutation checks

- Revert (by re-editing, not `git checkout`) the `if herdrErr != nil { absent = agentUnknown }`
  branch so `absent` is always `agentGone`: `TestStatusDegradesWhenHerdrUnreachable`
  must fail on `PlannerStatus`. Restore.
- Make `Status` return the ListAgents error again: the same test must fail
  on `err != nil`. Restore.
- Remove the footer append: `TestFooterMarksHerdrUnreachable` must fail.
  Restore.

Report each of the three as "mutation X: <test> failed as expected".

### Step 6 -- full check and commit

```
make check
```

(`make check` runs `gofmt -l .` with a failing exit, `go vet`, `go test
./...`, and a `go mod tidy` check.) Then `git diff --stat` must list only:
`internal/relay/status.go`, `internal/relay/statusline.go`,
`internal/relay/status_test.go`, `internal/ui/model.go`,
`internal/ui/split_test.go`. If anything else appears, halt and report.

Commit message:

```
fix(status): rows render when herdr is unreachable; unlooked-up panes read unknown, not gone

relay ui/status on a host without a herdr server failed outright even
when every builder was headless. Status now carries the herdr error as
Report.HerdrError, builds rows from the store, and marks pane endpoints it
could not look up as unknown. relay status prints a header line, relay ui
a footer marker.
```

## Report

State per step: done / halted (with the contradiction). Paste the
`make check` tail, the `git diff --stat`, and the three mutation lines.
