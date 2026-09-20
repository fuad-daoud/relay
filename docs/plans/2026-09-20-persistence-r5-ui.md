# Persistence round 5: `relay ui` shows every binding ever -- `all` scope, plan tab, round stepping, db-backed tabs

Spec: `docs/specs/2026-09-20-persistence-design.md` (§3 decision 7; §5.8
ui; §6 errors). Issues #172 (a year-old binding in the ui) and #183 (plan
tab, `[`/`]` round stepping). Depends on rounds 1-4 -- confirm
`relay.Show` exists in `internal/relay/show.go` and `relay history` runs;
if not, halt and report.

This plan stands alone: everything you need is in this file and in the
tree. If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

## 1. System overview

`relay ui` lists live bindings in a rail and shows one binding's
`report / terminal / diff / log` tabs for its newest completed round. This
round gives the rail a scope -- `live` (today) or `all` (every binding in
the database, archived ones dim) -- adds a `plan` tab first in the order,
and lets `[` / `]` step the detail pane through a binding's rounds. A
live binding's tabs keep reading files; a non-live binding's tabs read the
database through `relay.Show` from round 4. No filters beyond the current
repo; the dashboard/grid is a later spec.

## 2. File structure

```
internal/ui/fetch.go            tab enum: tabPlan first; fetchFor takes a source (live|db); fetchShow(ctx, rt, name, round, section) for db-backed tabs
internal/ui/scope.go            scope enum (live|all), scopeRows(report, dbRows, here) merge rule, archived row rendering facts
internal/ui/scope_test.go
internal/ui/model.go            Model.scope, Model.dbRows []relay.HistoryBinding; statusMsg merge; detail.round stepping; header text
internal/ui/detail.go           detailModel.live bool, .rounds int, .archivedAt time.Time; header "round N of M · archived <date>"
internal/ui/keys.go             "a" toggles scope; "[" / "]" step rounds; "1".."5" for five tabs
internal/ui/rail.go             dim style + "archived <date>" in place of state for non-live rows; never attention-sorted
internal/ui/prefs.go            Prefs.Scope string ("live"|"all"), persisted like Sort
internal/ui/styles.go           archivedStyle
internal/ui/testdata/*.golden   new goldens (see Task 5)
internal/relay/history.go       + HistoryBinding (a ui-shaped row) and Bindings(ctx, rt, here string) ([]HistoryBinding, error) over db.Bindings
internal/relay/history_test.go  + tests
cmd/relay/main.go               cmdUI opens rt.DB (nil on failure with a notice, ui still runs)
README.md                       `### relay ui`: the a key, the plan tab, [ ] stepping, what an archived row looks like
```

## 3. Data structures

```
// internal/relay
type HistoryBinding struct {
    Name string; ID string; Rounds int; LastActivity time.Time
    Feature, Repo string; FinalState string
    Archived bool; ArchivedAt time.Time
    Live bool            // set by the ui when the name is also in the live report
}

// internal/ui
type scope int  (scopeLive, scopeAll)
tab order: tabPlan, tabReport, tabTerminal, tabDiff, tabLog, tabCount ; tabTitles = {"plan","report","terminal","diff","log"}
detailModel gains: live bool; rounds int; archivedAt time.Time; bindingID string
Prefs gains: Scope string `json:"scope,omitempty"`   // "" reads as live
```

Rail row for scope `all`: the union of the live report's rows (rendered
exactly as today, attention-sorted when `sort` is on) followed by every
`HistoryBinding` not in the live set, newest `LastActivity` first, rendered
dim, with `archived <YYYY-MM-DD>` (or `done` when not archived) where the
state word goes and `rN` + `LastActivity` age in the facts line. When the
ui was started inside a git repo, `all` passes `here = cwd`; otherwise
every binding.

## 4. Interfaces

```
// internal/relay
func Bindings(ctx context.Context, rt Runtime, here string) ([]HistoryBinding, error)
    rt.DB == nil -> error ErrNoDatabase
    here != "" -> resolve through rt.Git.RepoFacts as HistoryOptions.Filter does; a non-repo cwd means no filter (not an error)

// internal/ui
func scopeRows(live []relay.BindingStatus, hist []relay.HistoryBinding) (rows []railRow)
    railRow{live *relay.BindingStatus; hist *relay.HistoryBinding}   -- exactly one non-nil
func fetchShow(ctx, rt, name string, round int, section relay.ShowSection) tea.Cmd
    wraps relay.Show; Missing -> tabContent.empty prose ("no <section> for round N"); err -> tabContent.err
func (m Model) detailHeader() string
    "<name> · round N of M" + " · archived 2026-08-30" when archivedAt set + " · live" when live and N == current open round
```

Fetch routing (`fetchFor`): when `m.detail.live` is false, every tab goes
through `fetchShow` with the section mapped from the tab (`terminal` ->
`transcript`). When live: `plan` reads `rt.Store.PlanPath(name, round)`
(new, small); `report`/`diff`/`log`/`terminal` keep today's fetchers but
all take `m.detail.round` (today only `diff` does -- `fetchReport`,
`fetchLog` and `fetchTerminal` gain the round argument; `fetchLog` filters
entries to the round; `fetchTerminal` for a headless or transcript-backed
round reads `BuilderLogPath(name, round)`; for a pane builder's non-current
round it returns the empty prose `terminal is live; round N left no log`).

## 5. Pseudocode

```
key "a":
    m.scope = toggle; save prefs; if all and m.rt.DB == nil: notice "no database: <err>" and stay live
    trigger fetchStatus (which now also fetches Bindings when scope == all)

statusMsg (extended): carries report + dbRows (nil when scope live)
    m.list rows = scopeRows(report.Bindings, dbRows)
    cursor re-resolves by name as today (resolveSticky) across the union

enter / re-point on a row:
    live row: detail.live = true; detail.rounds = row.Round; detail.round = newest completed (row.Round-1, min 1... keep today's rule); headless as today
    hist row: detail.live = false; detail.bindingID; detail.rounds = row.Rounds; detail.round = rounds (the newest, all closed); archivedAt
    invalidate every tab cache; fetch active tab

key "[":  if detail.round > 1: detail.round--; invalidate caches; fetch active
key "]":  if detail.round < detail.rounds (live: row.Round; hist: Rounds): detail.round++; same
    stepping onto a live binding's open round: report/diff fetch return the empty prose (#183): "round N is open; report arrives when it closes" / "diff is captured when round N closes"
keys "1".."5": tabs in the new order

rail line for hist row: archivedStyle.Render(fit(name)) ... "archived 2026-08-30" | "done" ; second line (non-compact): "r3 · 2mo ago · feature auth" (feature only when set)
```

## 6. Error handling

- `ErrNoDatabase` or `db.Open` failure: the ui runs in `live` scope; `a`
  shows a sticky notice and does nothing else. Never an exit.
- A `fetchShow` error renders in the tab as `tabContent.err` exactly like
  a failed file read today; `Missing` renders as `empty` prose, never as
  an error.
- Round stepping never leaves `[1, rounds]`; at the edges the key is a
  no-op with no notice.

## 7. Ordered implementation steps

### Task 0 -- two fixes from round 4's verification (do this first)

**(a) `event.round_id` is never set.** `internal/ingest/ingest.go` appends
events before the round rows exist and never links them, so
`db.Events(bindingID, round)` with `round > 0` returns nothing and
`relay show --log` filters client-side. Fix in the ingester: after the
rounds loop has upserted every round (so each number has an id), update
`event.round_id` for every event of this binding whose `round_id` is null
and whose decoded `LogEntry.Round` matches a round number (one `UPDATE`
per round: `UPDATE event SET round_id = ? WHERE binding_id = ? AND round_id
IS NULL AND seq IN (...)`, or per event -- either is fine; add a
`(*Tx).LinkEvents(bindingID, roundID string, seqs []int) error` to
`internal/db/write.go` for it). Then make `relay.Show`'s log section use
`db.Events(bindingID, round)` directly and drop the client-side filter.
Existing dbs are rebuilt by `relay db backfill` after deleting `relay.db`;
say so in the README's `relay db` section (one sentence).

**(b) Live rounds count.** `relay.Show` reports `Rounds` as `b.Round` for a
live binding, but `b.Round` is the *next* round once a round has closed
(`finishRound` does `Round++`); the ui already uses "newest completed =
`row.Round - 1`". Rule: for a live binding, `Rounds` = the highest round
number that has a `plan` entry in the log (0 when none). `round N of M`
then reads correctly for idle and in-flight bindings alike.

**Files:** `internal/db/write.go` (+test), `internal/ingest/ingest.go`
(+test), `internal/relay/show.go` (+test), `README.md`.

**Tests**
- `TestLinkEventsSetsRoundID` (`internal/db`).
- `TestIngestLinksEventsToRounds` (`internal/ingest`): after ingesting the
  fixture, `db.Events(bindingID, 2)` returns exactly round 2's entries.
  **Mutation check:** skip the link step and this must fail.
- `TestShowLiveRoundsIsHighestPlanned` (`internal/relay`): a live binding
  with `Round: 4` and plan entries for 1..3 -> `Rounds == 3`.

**Verify:** `go test ./internal/db/ ./internal/ingest/ ./internal/relay/`.

### Task 1 -- `relay.Bindings` and `HistoryBinding`

**Files:** `internal/relay/history.go`, `history_test.go`.

**Tests**
- `TestBindingsNoDatabase` -> `ErrNoDatabase`.
- `TestBindingsHereResolvesRepo` (fakeGit origin -> only that repo's rows, newest first).
- `TestBindingsHereNotARepoMeansAll`.
- `TestHistoryBindingArchivedFacts` (ingested-from-tarball row -> `Archived true`, `ArchivedAt` set).

**Verify:** `go test ./internal/relay/ -run Bindings`.

### Task 2 -- tab order, plan tab, round-aware fetchers (live)

**Files:** `internal/ui/fetch.go`, `model.go`, `keys.go`, `detail.go`, existing tests that index `tabReport` etc. (they must keep passing after the enum shift -- update any golden that renders the tab bar).

**Tests**
- `TestTabOrderStartsWithPlan` (titles array).
- `TestFetchPlanLive` (temp store with `001-plan.md` -> body).
- `TestFetchReportTakesRound`, `TestFetchLogFiltersRound`, `TestFetchTerminalNonCurrentPaneRoundIsEmptyProse`.
- `TestStepRoundBackRefetchesEveryTab`: point at a 3-round live binding, press `[` twice -> `detail.round == 1` and every cache invalidated; press `]` three times -> `detail.round == 3` (the open round), report/diff show the open-round prose.
- `TestStepRoundEdgesNoop`.

**Verify:** `go test ./internal/ui/`.

### Task 3 -- scope, prefs, rail

**Files:** `internal/ui/scope.go`, `scope_test.go`, `prefs.go`, `prefs_test.go`, `rail.go`, `rail_test.go`, `styles.go`, `model.go` (statusMsg carries dbRows; `fetchStatus` fetches `relay.Bindings` when scope is all), `keys.go` (`a`).

**Tests**
- `TestScopeRowsUnionOrder`: live rows first in given order, then hist rows not named in live, newest first; a name in both appears once, as live.
- `TestPrefsScopeRoundTrip`, `TestPrefsScopeEmptyIsLive`.
- `TestRailArchivedRowFacts`: the state slot reads `archived 2026-08-30`; the facts line reads `r3 · <age> · feature auth`; the style is `archivedStyle`.
- `TestKeyAToggleWithoutDBNotices`.

**Verify:** `go test ./internal/ui/`.

### Task 4 -- db-backed detail

**Files:** `internal/ui/fetch.go` (`fetchShow`), `model.go` (re-point on a hist row; routing), `detail.go` (header).

**Tests** (seed a db by `ingest.Ingest` over a copy of `internal/ingest/testdata/binding-three-rounds` packed as a tarball, so the binding is archived and not live):
- `TestPointAtArchivedRowLoadsPlanFromDB`.
- `TestArchivedTerminalTabShowsTranscriptRows` (not tail-following: `follow` false).
- `TestArchivedMissingDiffIsEmptyProse`.
- `TestDetailHeaderArchived` -> `fixture · round 3 of 3 · archived 2026-...`.
- `TestArchivedStepRoundRefetches`.

**Verify:** `go test ./internal/ui/`.

### Task 5 -- goldens, cmdUI, README

**Files:** `internal/ui/golden_test.go` + `testdata/` (add `split-all-scope.golden`, `stack-all-scope.golden`, `split-archived-detail.golden`; regenerate any existing golden whose tab bar changed and list them in the report), `cmd/relay/main.go` (`cmdUI` opens the db; failure -> `rt.DB = nil` and the initial notice), `README.md`.

**Verify:** `go test ./internal/ui/` with the goldens. Planner's machine only -- `relay ui` on this machine: press `a`, see the archived rows dim below the live ones, select one, see plan/report/terminal/diff/log, step with `[`/`]`. Describe what you saw in the report in three lines.

**Remote builder note:** the machine-specific check above (this planner's state directory / interactive terminal) is done by the planner after the round, not by you. Run the automated verifies only, and say in the report that the machine check was left to the planner.

### Task 6 -- full check and commit

Run: `gofmt -l .` (nothing), `go vet ./...`, `go test -race -count=1 ./...`,
`go mod tidy && git diff --exit-code go.mod go.sum`, then `make check`.
Known: `scripts/plugin-build_test.sh` case 3 fails on a clone without tags
(#242) -- if that is the **only** failure and `git tag` prints nothing, say
so and treat the check as passed.

**Commit** (one for the round):
`feat(ui): all scope over the database, plan tab, [ ] round stepping, archived bindings render from rows; ingest links events to rounds (#172, #183)`

## Report

Per task: what was done, the test names, the verify result, which goldens
were regenerated and why each changed. Then the commit sha and the
`make check` result. If any step was impossible as written, say which and
stop there.
