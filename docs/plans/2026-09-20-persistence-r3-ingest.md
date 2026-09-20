# Persistence round 3: `internal/ingest`, `relay db backfill`, the daemon fills the database

Spec: `docs/specs/2026-09-20-persistence-design.md` (§3 decisions 3, 5, 8;
§5.2 ingester; §5.3 outcome; §5.5 daemon; §5.6 `backfill`; §6 errors).
Issue #172. Depends on rounds 1 (`internal/db`) and 2 (`bind.json` fields)
being merged -- confirm `internal/db/db.go` and `store.RepoRef` exist
before starting; if either is missing, halt and report.

This plan stands alone: everything you need is in this file and in the
tree. If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

## 1. System overview

Round 1 created `relay.db`; nothing fills it. This round adds one pure
function, `ingest.Ingest`, that reads a binding's directory -- live under
`~/.local/state/relay/<name>/`, or a `gc` tarball under `.archive/` -- and
upserts every fact it holds: the repo, the planner, the binding, each
round with its recorded outcome and builder, every log event, every
plan/report/diff/drift/gate-log/question/answer/consult artifact, and every
transcript record. `relay db backfill` runs it once over every tarball and
live dir on an existing machine (this one has 49 tarballs, 22 MB); the
daemon runs it over live dirs at the end of every tick, with per-file
cursors so an idle tick writes nothing. Files stay the write side; the db
becomes the record.

## 2. File structure

```
internal/ingest/
  source.go            Source interface; DirSource; TarSource (reads members from the gzip tar without extracting)
  ingest.go            Ingest, Deps, Stats; the orchestration in §5
  outcome.go           deriveOutcome, builderForRound, switchesForRound
  cursor.go            append-only and whole-file cursor logic (head sha, whole sha, reset rule)
  facts.go             round facts from entries: usage, gate, commits/tree, report outcome, tier
  transcript.go        transcript rows from NNN-builder.jsonl (stream) or NNN-builder.log (rendered only), and from the planner's record
  errors.go            ErrSource, ErrCursor
  testdata/binding-three-rounds/   golden live dir (see Task 2)
  *_test.go
internal/relay/herdr.go            Runtime.DB already exists (round 1); + IngestDeps(rt) helper
internal/relay/daemon.go           end of Tick: ingest every live binding
internal/relay/daemon_test.go      + tests with a temp db
cmd/relay/db.go                    + backfill [--dry-run] [--archive-only|--live-only]
cmd/relay/main.go                  cmdDaemon opens the db (rt.DB); relay serve does NOT (its state root is its own; out of scope)
README.md                          `relay db backfill`; a paragraph "The database" under the state-directory section
```

## 3. Data structures

```
// internal/ingest
type Source interface {
    Name() string
    Bind() (store.Binding, error)                     // decodes bind.json; ErrSource when missing/invalid
    Open(member string) (io.ReadCloser, int64, error) // member = basename inside the binding dir; size; os.ErrNotExist when absent
    List() ([]string, error)                          // basenames, sorted
    Origin() (kind, path string)                      // "live", dir  |  "archive", tarball path
}
type Deps struct {
    Git      GitFacts                 // RepoFacts(ctx, cwd) (originURL, commonDir string, err error); nil = never resolve
    Sessions relay.SessionLocator     // nil = never locate a planner record
    Now      func() time.Time
    Logger   *slog.Logger             // nil = slog.Default()
}
type GitFacts interface{ RepoFacts(ctx context.Context, dir string) (string, string, error) }
type Stats struct{ Bindings, Rounds, Events, Artifacts, TranscriptRecords, Skipped int }
func (s Stats) Add(o Stats) Stats
```

Member naming: derive every member name from `store.New("/")`'s path
helpers and `filepath.Base` (`PlanPath`, `ReportPath`, `DiffPath`,
`DriftPath`, `GateLogPath`, `QuestionPath`, `DonePath`, `BuilderLogPath`,
`BuilderStreamPath`, `AskPath`, `FindingsPath`); never hard-code `NNN-plan.md`
strings. Consult members are recognised by the pattern the helpers produce
(`%03d-<id>-ask.md`, `%03d-<id>-findings.md`); the id between is the
`consult_id`.

Cursor sources: `"<abs dir>/<member>"` for live; `"<tarball>::<member>"`
for archive.

Outcome enum and artifact kinds: use the constants exported by
`internal/db` (round 1).

## 4. Interfaces

```
func DirSource(dir string) Source
func TarSource(path string) (Source, error)      // opens once to index members (name -> offset is NOT possible in tar; read sequentially and cache small members in memory; members > 64 MB stream on demand by re-reading)
func Ingest(ctx context.Context, src Source, d *db.DB, deps Deps) (Stats, error)
    pre:  d != nil
    post: one db.Tx committed with every row the source yields beyond its cursors; cursors saved in the same Tx
    err:  ErrSource (bind.json missing/invalid; tarball unreadable) -- nothing written; db errors wrapped
func deriveOutcome(events []store.LogEntry, round int, b store.Binding, members map[string]bool) string
func builderForRound(events []store.LogEntry, round int, b store.Binding) (candidate string, ref candidate.Ref, ok bool)
func switchesForRound(events []store.LogEntry, round int) int
func roundFacts(events []store.LogEntry, round int) (db.Round fields: started, closed, tier, commits, tree, gate*, tokens, cost*, reportOutcome)
```

## 5. Pseudocode

```
Ingest(src):
    b := src.Bind()                                    -- ErrSource on failure
    members := set(src.List())
    kind, origin := src.Origin()
    d.Tx(func(tx):
        -- repo
        ref := b.RepoRef                                   -- the *RepoRef added in round 2, NOT the pre-existing Binding.Repo string (the add/fork source checkout)
        if ref == nil and kind == "live" and deps.Git != nil: ref = from deps.Git.RepoFacts(b.CWD) (normalised via git.NormalizeOriginURL), nil on error
        repoID := tx.UpsertRepo(...) if ref has either field, else nil
        -- planner
        plannerID := tx.UpsertPlanner{HarnessKind: b.Planner.Kind, SessionID: b.Planner.SessionID, Locator: b.Planner.TranscriptLocator} if SessionID != "" else nil
        -- events (append-only, cursored)
        cur := tx.Cursor(source(log.jsonl)); entries, newOffset := readAppendOnly(log.jsonl, cur)   -- §cursor
        all := entries already in db (tx.Events(bindingID, 0)) + new      -- need the full list to derive rounds; read the whole file once when the cursor is at 0, else db rows + new lines
        -- binding
        createdAt := b.CreatedAt; if zero: first entry ts; if none: deps.Now()
        bindingID := tx.UpsertBinding{Name, RepoID, PlannerID, Feature, ForkedFrom*, CWD, Worktree, Branch, Base, Tier, Gate, BuilderMode: b.Builder.Mode or "pane", Server, CreatedAt, FinalState: b.State, ArchivedAt/ArchivePath when kind=="archive" (ArchivedAt = tarball stamp), IngestSource: kind}
        tx.AppendEvents(bindingID, new entries with Seq = line index (0-based from file start), EntryJSON = the raw line)
        -- rounds
        rounds := union of entry.Round for all entries, and every NNN parsed from members, and b.Round when b.Round > 0
        for n in sorted(rounds):
            r := db.Round{BindingID, Number: n, ...roundFacts(all, n), Outcome: deriveOutcome(all, n, b, members), Switches: switchesForRound(all, n)}
            cand, ref, ok := builderForRound(all, n, b); if ok: fill BuilderCandidate/Harness/Provider/Model; BuilderMode = b.Builder.Mode
            roundID := tx.UpsertRound(r)
            for (member, kind) in {plan, report, diff, drift, gate_log, question}: if member in members: whole-file cursor check; tx.UpsertArtifact{RoundID, Kind, Text, Bytes, SHA256, CapturedAt: now}
            for each consult member of round n: UpsertArtifact with Kind ask|findings and ConsultID
            answer: an `answer` entry's Payload for round n, when present, becomes an artifact of kind answer (Text = Payload)
            transcript: if NNN-builder.jsonl in members: readAppendOnly; for each new line i: TranscriptRecord{OwnerKind: round, OwnerID: roundID, Seq: lineIndex, TS: parsed from the record when present, RecordJSON: line, Rendered: strings.Join(transcript.Render(b.Builder.Kind, line), "\n")}
                        else if NNN-builder.log in members: readAppendOnly; each line -> {RecordJSON: "", Rendered: line}
        -- planner transcript
        if plannerID != nil and locator != "" and kind == "live" and file exists: readAppendOnly(locator); rows with OwnerKind planner, OwnerID plannerID, Rendered = RenderRecord(b.Planner.Kind, line)
        save every cursor touched
    )
    return stats

readAppendOnly(source, cur):
    open; size
    if cur exists:
        if size < cur.ByteOffset or sha256(first min(4096,size) bytes) != cur.HeadSHA: log Info "cursor reset"; cur = 0     -- ErrCursor is internal; never returned
    seek cur.ByteOffset; read complete lines only (a trailing partial line is left for next time)
    return lines, newOffset, newHeadSHA
whole-file check: sha256 of the member; skip when equal to cur.WholeSHA

deriveOutcome(events, n, b, members):  first match wins
    any event kind==report and round==n                       -> reported
    members has NNN-done and no report                        -> done_no_report
    any exit and n == b.Round and b.State != active           -> exited
    (b.State == needs_you and n == b.Round) or (b.Halt != "" and n == b.Round) -> halted
    any switch                                                -> switched
    otherwise                                                 -> open

builderForRound(events, n, b):
    last pick/switch entry for round n: parse the note --
        pick:   "picked <token> on <server>: ..." or "picked <token>: ..." -> token
        switch: "... -> <token>)" or "... -> <token>" -> the token after the last "->", trimmed of ")" and whitespace
    else b.BuilderCandidate
    token may carry "#high" or ":high" suffixes (effort) -- keep BuilderCandidate verbatim; parse Harness/Provider/Model with candidate.ParseRef on the token with any "#..." suffix removed (":effort" belongs to the model part and stays)

roundFacts(events, n):
    started := ts of the plan entry for n (else the earliest entry of n)
    closed  := ts of the report entry for n, else the exit/switch entry when the outcome is exited, else nil
    tier    := plan entry .Tier
    commits, tree := diff entry .Commits/.Tree when Tree != ""
    gate*   := report entry .Gate
    tokens/cost := report entry .Usage: In, CacheRead -> cache_tokens, CacheWrite -> write_tokens, Out, Cost.USD, Cost.Basis
    reportOutcome := report entry .Outcome ("" -> "unstructured")
```

Daemon (`daemon.go` end of `Tick`, after `notifyFinished`):
```
if d.rt.DB != nil:
    for b in fresh:
        stats, err := ingest.Ingest(ctx, ingest.DirSource(d.rt.Store.Dir(b.Name)), d.rt.DB, relay.IngestDeps(d.rt))
        if err: slog.Warn("ingest", "binding", b.Name, "err", err); continue
        if stats has any non-zero count: slog.Info("ingest", "binding", b.Name, "rounds", ..., "events", ..., "artifacts", ..., "transcript", ...)
```
`IngestDeps(rt)` returns `Deps{Git: rt.Git (nil-safe), Sessions: rt.Sessions, Now: time.Now}`.

`relay db backfill`:
```
open db
if not --live-only: for a in store.ListArchives() (oldest first): src := TarSource(a.Path); Ingest; print "<name>  <stamp>  rounds N events N artifacts N transcript N" or "<name>  FAILED: <err>"
if not --archive-only: for b in store.List(): DirSource; same line
--dry-run: open the sources and count members/lines without writing (Ingest with a db opened on a temp copy? NO -- simpler: a `Plan(src)` function that returns what Ingest would touch; implement as Ingest against an in-memory db opened at ":memory:"); print the same lines prefixed "would"
exit 1 if any source failed, after finishing the rest
```

## 6. Error handling

- `ErrSource`: per source; backfill reports and continues; the daemon
  logs `Warn` and skips the binding this tick.
- Cursor reset is not an error: `Info` log, offset 0, and `UNIQUE` keys
  make the re-append a no-op.
- A transcript line that `Render` cannot parse is stored with `Rendered`
  empty and counted in `Stats.Skipped`.
- `db.ErrBusy` from the Tx: the daemon skips the binding this tick (no
  retry loop inside the tick); backfill retries once after 1 s.
- Nothing here changes a binding, a round file, or a state.

## 7. Ordered implementation steps

### Task 1 -- sources

**Files:** `internal/ingest/source.go`, `errors.go`, `source_test.go`.

**Tests**
- `TestDirSourceListsAndOpens`: temp dir with `bind.json`, `log.jsonl`, `001-plan.md`.
- `TestDirSourceMissingBindIsErrSource`.
- `TestTarSourceReadsMembersWithoutExtracting`: build a tarball in the test the way `store.archive()` does (a top-level `<name>/` directory; read `internal/store/store.go`'s archive code to match the layout exactly), list and open members; no file is written outside `t.TempDir()`.
- `TestTarSourceOriginStamp`: `Origin()` returns `"archive"`, and the stamp parsed from `<name>-YYYYMMDD-HHMMSS.tar.gz` is exposed via an `ArchivedAt() (time.Time, bool)` method on the concrete type.

**Verify:** `go test ./internal/ingest/`.

### Task 2 -- golden fixture

**Files:** `internal/ingest/testdata/binding-three-rounds/` -- a complete live binding directory built by hand:
- `bind.json`: name `fixture`, `repo_ref` with origin `https://github.com/o/r` and a common dir, `feature: "auth"`, planner kind `claude` session `S1`, builder headless `opencode`, `builder_candidate: "opencode/openrouter/z-ai/glm-5.3-flash"`, `round: 3`, `state: "needs_you"`, `halt: "builder exited (code 1) without a report"`.
- `log.jsonl` with: r1 `pick`, `plan` (tier edit), `diff` (commits 2, tree clean), `report` (usage in 1000 / cache 5000 / write 0 / out 200, cost 0.12 measured, gate pass 0 900ms, outcome done); r2 `plan`, `switch` (note `switched ... -> agy/google/gemini-3.8-flash-high`), `report` (outcome halted, usage basis unknown); r3 `plan`, `exit`.
- `001-plan.md`, `001-report.md`, `001-diff.patch`, `001-builder.jsonl` (5 valid opencode stream lines + 1 garbage line), `001-builder.log`; `002-plan.md`, `002-report.md`, `002-drift.patch`, `002-builder.log` only (no jsonl -- the pane case); `003-plan.md`, `003-done`.

Also a `bind.json` copy without any round-2 fields for the legacy test.

**Verify:** the directory exists and `go vet ./...` still passes (testdata is ignored by the toolchain).

### Task 3 -- outcome, builder and facts

**Files:** `internal/ingest/outcome.go`, `facts.go`, `outcome_test.go`, `facts_test.go`.

**Tests** (table tests, one row per §5.3 line of the spec):
- `TestOutcomeReportedBeatsDone` (report + done marker -> reported) -- **mutation check**: swap the first two rules and this must fail.
- `TestOutcomeDoneNoReport`, `TestOutcomeExited`, `TestOutcomeHaltedByState`, `TestOutcomeHaltedByHaltText`, `TestOutcomeSwitched`, `TestOutcomeOpen`.
- `TestBuilderForRoundFromPick`, `...FromSwitch`, `...FallsBackToBinding`, `...StripsEffortSuffix` (`opencode/cline-pass/cline-pass/glm-5.3-flash#high` -> harness opencode, provider cline-pass, model `cline-pass/glm-5.3-flash`, candidate verbatim).
- `TestSwitchesForRound`.
- `TestRoundFactsFromFixtureRound1`: every column of round 1 as listed in Task 2.

**Verify:** `go test ./internal/ingest/`.

### Task 4 -- cursors and transcript rows

**Files:** `internal/ingest/cursor.go`, `transcript.go`, tests.

**Tests**
- `TestReadAppendOnlyFromZero`, `TestReadAppendOnlyResumes` (write 3 lines, read; append 2, read -> 2), `TestReadAppendOnlyLeavesPartialLine`.
- `TestIngestCursorReset` (rewrite the file with different first bytes -> reset, all lines returned) -- **mutation check**: remove the head-sha comparison and this must fail.
- `TestTranscriptFromStream` (jsonl -> rows with Rendered from `transcript.Render`, garbage line -> Rendered "" and Skipped 1).
- `TestTranscriptFromLogOnly` (no jsonl -> rows with RecordJSON "").

**Verify:** `go test ./internal/ingest/`.

### Task 5 -- Ingest

**Files:** `internal/ingest/ingest.go`, `ingest_test.go`.

**Tests** (each opens a fresh `db.Open` in `t.TempDir()`):
- `TestIngestFixtureLive`: rows match exactly -- 1 repo (origin), 1 planner, 1 binding (feature auth, final_state needs_you, ingest_source live), 3 rounds (reported / reported-with-halted-report-outcome / exited -- check §5.3: round 2 has a report so it is `reported` with `report_outcome halted`; round 3 is `exited`), the right builder columns per round (r2 = agy/google), 9 events, artifacts: r1 plan/report/diff, r2 plan/report/drift, r3 plan; transcript rows: r1 5 rendered + 1 skipped, r2 N log lines, r3 none; `Stats` equal to those counts.
- `TestIngestFixtureArchiveEqualsLive`: pack the fixture as a tarball, ingest into a second db, compare every table to the live ingest except `archived_at`/`archive_path`/`ingest_source`.
- `TestIngestTwiceIsNoop`: second call returns zero Stats and row counts are unchanged.
- `TestIngestAppendsAfterNewRound`: ingest, append r4 plan entry + `004-plan.md`, ingest -> +1 round, +1 event, +1 artifact.
- `TestIngestLegacyBindJSON`: the copy without round-2 fields -> repo null (no Git dep), feature null, still 3 rounds.
- `TestIngestResolvesRepoWhenMissing`: legacy bind + a fake `GitFacts` -> repo row created.
- `TestIngestPlannerTranscript`: a fake `Sessions` pointing at a small claude jsonl -> planner transcript rows with `Rendered` from `transcript.RenderRecord`.
- `TestIngestBadBindIsErrSourceAndWritesNothing`.

**Verify:** `go test -race ./internal/ingest/`.

### Task 6 -- daemon and `relay db backfill`

**Files:** `internal/relay/herdr.go` (`IngestDeps`), `daemon.go`, `daemon_test.go`, `cmd/relay/main.go` (`cmdDaemon` opens `rt.Store.DBPath()` with `db.Open`; on error log `Warn` once and continue with `DB == nil`), `cmd/relay/db.go` (`backfill`), `README.md`.

**Tests**
- `TestTickIngestsLiveBindings` in `internal/relay`: a Runtime with a temp db and one binding through the existing fake herdr; after `Tick`, `db.Binding(name)` is found with one round.
- `TestTickWithoutDBIsUnchanged`: `DB == nil` -> no panic, no file `relay.db`.
- `cmd/relay`: `TestFormatBackfillLine` pure formatter test only (no subcommand execution -- CI has no herdr).

**Verify (planner's machine):** `relay db backfill --dry-run` on this machine lists 49 archives and the live bindings without writing; `relay db backfill` then `relay db stats` shows non-zero rows in every table; `relay db backfill` a second time prints all-zero counts. Record the three outputs' first and last lines in the report.

**Remote builder note:** the machine-specific check above (this planner's state directory / interactive terminal) is done by the planner after the round, not by you. Run the automated verifies only, and say in the report that the machine check was left to the planner.

### Task 7 -- full check and commit

Run: `gofmt -l .` (nothing), `go vet ./...`, `go test -race -count=1 ./...`,
`go mod tidy && git diff --exit-code go.mod go.sum`, then `make check`.
Known: `scripts/plugin-build_test.sh` case 3 fails on a clone without tags
(#242) -- if that is the **only** failure and `git tag` prints nothing, say
so and treat the check as passed. Tests that create git repos pass
`-c commit.gpgsign=false -c tag.gpgsign=false`.

**Commit** (one for the round):
`feat(ingest): files to rows -- Ingest over live dirs and archives, relay db backfill, daemon ingest per tick (#172)`

## Report

Per task: what was done, the test names, the verify result. Then the commit
sha, the `make check` result, the two mutation checks' outcomes (name the
test that failed when the rule was broken), and the backfill outputs from
Task 6. If any step was impossible as written, say which and stop there.
