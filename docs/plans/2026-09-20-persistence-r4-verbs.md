# Persistence round 4: `relay history` and `relay show`

Spec: `docs/specs/2026-09-20-persistence-design.md` (§5.1 `Filter`/`RoundRow`;
§5.7 verbs; §6 errors). Issues #172 (history, show over the archive) and
#183 (show over live state). Depends on rounds 1-3 being merged -- confirm
`internal/db` and `internal/ingest` exist and `relay db stats` runs; if
not, halt and report.

This plan stands alone: everything you need is in this file and in the
tree. If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

## 1. System overview

The database now holds every binding, round, event, artifact and
transcript relay has recorded, live or archived. Nothing reads it yet.
This round adds the two read verbs: `relay history`, one line per round
across every binding, filtered by any column and newest first; and
`relay show`, one round's plan, report, diff, drift, log or transcript,
read from the files for a live binding and from the db for anything else.
Both are read-only and change no state.

## 2. File structure

```
internal/relay/history.go           HistoryOptions -> db.Filter (resolves --here to a repo key), HistoryLine(RoundRow, now, loc), FormatHistory
internal/relay/history_test.go
internal/relay/show.go              ShowOptions, ShowSection enum, Show(ctx, rt, opts) (ShowResult, error): live-vs-db resolution, section selection
internal/relay/show_test.go
cmd/relay/history.go                cmdHistory: flags -> HistoryOptions; --json prints []db.RoundRow
cmd/relay/show.go                   cmdShow: flags -> ShowOptions
cmd/relay/main.go                   verbs "history", "show"; both open rt.DB (read-only use)
README.md                           `### relay history`, `### relay show` sections; the verbs table
```

## 3. Data structures

```
// internal/relay
type HistoryOptions struct {
    Here      string        // cwd to resolve into a repo key; "" = no repo filter
    Repo      string        // explicit origin url or common dir
    Feature, Binding, Planner, Harness, Provider, Model, Candidate, Outcome string
    Since, Until string     // relay.ParseSince forms: 24h, 7d, YYYY-MM-DD
    Archived   *bool        // --archived true, --live false, nil both
    Limit      int          // default 200; 0 = all
}
func (o HistoryOptions) Filter(ctx, rt Runtime, now time.Time) (db.Filter, error)
    Here: rt.Git.RepoFacts(Here) -> normalised origin if any, else common dir; error when Here is not a repo
    Since/Until: ParseSince; Newest: true

type ShowSection string   // plan | report | diff | drift | log | transcript
type ShowOptions struct { Name string; Round int /* 0 = newest completed */; Section ShowSection; JSON bool }
type ShowResult struct {
    Name string; Round int; Rounds int; Live bool; Archived bool; ArchivedAt time.Time
    Section ShowSection; Text string; Missing bool   // Missing: the section does not exist for that round
    Events []store.LogEntry                           // for Section log
}
```

## 4. Interfaces

```
func HistoryLine(r db.RoundRow, loc *time.Location) string
    "2026-09-15 14:02  api-auth      r3  agy/antigravity/opus         reported   +2 commits  clean  $0.42   (archived)"
    columns: started local time; name padded to 12 (truncate with … when longer); rN; candidate padded to 40; outcome padded to 14;
             commits ("+N commits", "-" when nil); tree ("-" when nil); cost ("$0.42", "~$0.42" when basis estimated, "unknown" when basis unknown, "-" when nil);
             "(archived)" when Archived
func FormatHistory(rows []db.RoundRow, loc *time.Location) string     // one HistoryLine per row; "no rounds" when empty

func Show(ctx context.Context, rt Runtime, opts ShowOptions) (ShowResult, error)
    pre:  opts.Name non-empty; Section valid
    resolution:
        if rt.Store.Load(name) succeeds: Live = true; rounds from the store (b.Round); "newest completed" = the highest round with a report entry, else b.Round-1, else error ErrNoCompletedRound
            Text from the files: PlanPath/ReportPath/DiffPath/DriftPath; log = ReadLog filtered to the round; transcript = BuilderLogPath contents
        else if rt.DB != nil and db.Binding(name) found: Live = false; Rounds from db.Rounds; newest completed = highest round with outcome != open
            Text from db.Artifact(roundID, kind) (plan|report|diff|drift); log = db.Events(bindingID, round) decoded from EntryJSON; transcript = db.Transcript(round, roundID, 0, 0) Rendered lines joined by "\n"
        else: store.ErrNotFound
    Missing = true (Text "", no error) when the file or row is absent
```

Both verbs open the db read-only in the sense of never writing; `db.Open`
still migrates, which is acceptable. If `db.Open` fails, `history` exits 1
with the error; `show` on a live binding still works (it needs no db) and
on a non-live one exits 1 with the error.

## 5. Pseudocode

```
cmdHistory:
    flags: --here (bool; sets Here = cwd) --repo --feature --binding --planner --harness --provider --model --candidate --outcome
           --since --until --archived (bool) --live (bool) --limit (int, 200) --json
    --archived and --live together: usage error
    --outcome not in db's enum: usage error listing the six values
    rt := newRuntime(); rt.DB = db.Open(rt.Store.DBPath())
    f := opts.Filter(ctx, rt, now)
    rows := rt.DB.Query(f)
    --json: json.NewEncoder(os.Stdout).Encode(rows)   (empty -> "[]")
    else: fmt.Print(FormatHistory(rows, time.Local))

cmdShow:
    usage: relay show <name> [--round N] [--plan|--report|--diff|--drift|--log|--transcript] [--json]
    more than one section flag: usage error; none: plan
    res := Show(ctx, rt, opts)
    --json: encode ShowResult (Events included for log)
    else:
        header line to stderr: "<name> round <N> of <Rounds> · <section>" + " · archived <date>" when Archived, so stdout is the section only
        Missing: stdout "no <section> for round N", exit 0
        log: one relay.LogLine per event
        else: stdout Text (ensure trailing newline)
```

## 6. Error handling

- `ErrNoCompletedRound` (`show` with no `--round` on a binding whose
  every round is open): exit 1, message `no completed round yet; --round N to read an open round's plan`.
- `--round` out of range: exit 1 `round N: binding has M rounds`.
- Unknown binding: exit 1 `binding "x" not found (live or in the database)`.
- Bad `--since`/`--until`/`--outcome`/section combination: exit 2 with usage.
- db open failure: as stated in §4.

## 7. Ordered implementation steps

### Task 0 -- two ingester fixes found by the planner's backfill (do this first)

The real backfill over 49 archives showed two rule errors in round 3's
plan. Both live in `internal/ingest/ingest.go`; fix them here so `history`
reads correct rows.

**(a) Phantom rounds.** `bind.json`'s `round` is the *next* round number
after `finishRound`'s `Round++`, so adding `b.Round` to the round set
creates an empty `open` round on every finished binding (55 of 156 rounds
in the planner's db). Rule: a round exists only if it has at least one
event or at least one `NNN-*` member. Remove `b.Round` from the union.

**(b) Repo fallback.** For an archived binding, `b.CWD` is the gc'd
worktree path, so `RepoFacts(b.CWD)` fails and 57/57 archives have a null
repo. `bind.json` also carries the pre-existing `Repo` string (the source
checkout at add/fork time, `internal/store/types.go` ~line 237). Rule, in
order: `b.RepoRef` when set; else `deps.Git.RepoFacts(b.CWD)`; else
`deps.Git.RepoFacts(b.Repo)` when `b.Repo != ""`; else null. Apply for
archive sources too (drop the `kind == "live"` condition).

**Files:** `internal/ingest/ingest.go`, `ingest_test.go`.

**Tests**
- `TestIngestNoPhantomRoundFromBindRound`: fixture with `round: 4` in
  `bind.json` and files/events only for rounds 1-3 -> exactly 3 rounds.
  **Mutation check:** re-add `b.Round` to the union and this must fail.
- `TestIngestRepoFromSourceCheckoutWhenCWDGone`: `cwd` pointing at a
  missing dir, `repo` pointing at a dir the fake `GitFacts` answers for ->
  repo row created and linked; also with an archive source.

**Verify:** `go test ./internal/ingest/`.

### Task 1 -- history formatting and filter

**Files:** `internal/relay/history.go`, `history_test.go`.

**Tests**
- `TestHistoryLineColumns`: one RoundRow with every field set -> the exact line (fix a UTC location in the test); variants: nil cost -> `-`, estimated -> `~$`, unknown basis -> `unknown`, long name -> truncated with `…`, archived suffix.
- `TestFormatHistoryEmpty` -> `no rounds`.
- `TestHistoryOptionsFilterHere`: fakeGit returning an origin -> `Filter.Repo` is the normalised url, `Here` cleared, `Newest` true.
- `TestHistoryOptionsFilterHereNoRemote` -> common dir.
- `TestHistoryOptionsSinceUntil` -> parsed times.

**Verify:** `go test ./internal/relay/ -run History`.

### Task 2 -- Show

**Files:** `internal/relay/show.go`, `show_test.go`.

**Tests** (seed a temp store for the live case, a temp db via `ingest.Ingest` over `internal/ingest/testdata/binding-three-rounds` for the db case -- copy the fixture into a temp dir first, do not write into testdata):
- `TestShowLiveDefaultsToNewestCompletedPlan`.
- `TestShowLiveRoundReport`, `TestShowLiveMissingDiffIsMissingNotError`, `TestShowLiveLogFiltersRound`, `TestShowLiveTranscriptReadsBuilderLog`.
- `TestShowLiveNoCompletedRound` -> `ErrNoCompletedRound`.
- `TestShowDBFallsBackWhenNotLive`: the fixture binding is not in the store -> `Live false`, plan text from the artifact row.
- `TestShowDBTranscriptFromRows`, `TestShowDBLogFromEvents`, `TestShowDBArchivedHeaderFacts` (ingest from a tarball -> `Archived true`, `ArchivedAt` from the stamp).
- `TestShowRoundOutOfRange`, `TestShowUnknownBinding`.

**Verify:** `go test ./internal/relay/ -run Show`.

### Task 3 -- CLI, README

**Files:** `cmd/relay/history.go`, `cmd/relay/show.go`, `cmd/relay/main.go`, `README.md`.

README: two sections after `### relay log` (find the existing verb
sections and match their shape): what each verb prints, every flag, the
live-vs-database rule for `show`, and one example line each.

**Tests:** none that execute a subcommand (CI has no herdr; the rules are covered in `internal/relay`). A pure `TestHistoryUsageFlagsConflict` on the flag-validation helper is fine if you factor one out.

**Verify (planner's machine; the db is backfilled from round 3):**
`relay history --limit 5` prints five lines newest first; `relay history --here --json | head -c 200` is a JSON array; `relay history --outcome reported --harness agy --since 30d` runs; `relay show <an archived name from history> --report` prints that report; `relay show <a live binding> --log --round 1` prints round-1 lines only. Paste the first line of each into the report.

**Remote builder note:** the machine-specific check above (this planner's state directory / interactive terminal) is done by the planner after the round, not by you. Run the automated verifies only, and say in the report that the machine check was left to the planner.

### Task 4 -- full check and commit

Run: `gofmt -l .` (nothing), `go vet ./...`, `go test -race -count=1 ./...`,
`go mod tidy && git diff --exit-code go.mod go.sum`, then `make check`.
Known: `scripts/plugin-build_test.sh` case 3 fails on a clone without tags
(#242) -- if that is the **only** failure and `git tag` prints nothing, say
so and treat the check as passed.

**Commit** (one for the round):
`feat(relay): history and show; ingest: no phantom round from bind round, repo from the source checkout -- every round across live and archived bindings, one round's files from files or rows (#172, #183)`

## Report

Per task: what was done, the test names, the verify result, the pasted
example lines from Task 3. Then the commit sha and the `make check` result.
If any step was impossible as written, say which and stop there.
