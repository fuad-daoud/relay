# A scraped report is never tail-parsed (#221)

Closes #221. Design in this file; no separate spec. Decision taken from the
issue's "Decide first": option **(b)** -- a scraped body is a screen capture,
never the builder's structured report, so the report-tail parser does not run
on it at all. The e2e assertions (`note == "scraped"`, exactly) stand.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else.

**Scope guard.** Touch only the files listed in §2. Do **not** run
`make e2e` (it spawns a private herdr session; the planner runs it after the
round). Do not edit `internal/relay/e2e_test.go` or `reporttail.go`. Do not
add a CLI verb, flag, or module dependency. Run every command in the
foreground; dispatch no sub-agents. Do not widen any exported signature.

**Commits.** ONE commit, subject `fix(relay): never tail-parse a scraped report (#221)`.
Not `feat:`.

**Commit the plan with the work.** Copy this plan file to
`docs/plans/2026-09-19-scrape-no-tail.md` in your worktree and include it in
the commit.

**Before step 1**: `git status --short` and `git branch --show-current`; you
must be on `relay/<binding-name>` under `~/.local/state/relay/.worktrees/`
with a clean tree. Otherwise halt.

## 1. System overview

`queueReport` (`internal/relay/reconcile.go:648`) closes a round: it reads
the report body at `path`, runs `parseReportTail` on it, and when a
```` ```relay ```` fence is present but unreadable it appends the reject
reason to the entry's `Note` (#133: "the builder wrote a block we could not
read" must not look like "the builder omitted the block").

`scrapeReport` (`reconcile.go:622`) is the last resort when a pane builder
went quiet without a report: it writes the terminal scrollback to the report
path and calls `queueReport(..., note = "scraped", ...)`. That scrollback
contains the *prompt* relay typed into the builder -- which since #133 ends
with a ```` ```relay ```` block skeleton -- and herdr's scrollback capture
truncates or wraps it, so since #201 (the YAML block-list parser) every
scraped round's note becomes `scraped tail: unclosed fence`. `make e2e`
asserts the exact string `scraped` at `e2e_test.go:391` and `:432` and has
been red on `main` since #201, unseen because e2e is local-only.

The fix is a precondition, not a parser change: **when the note is
`scraped`, `queueReport` does not call `parseReportTail`.** The outcome is
`unstructured`, the tail fields stay empty, the note stays exactly
`scraped`. The injection scan still runs on the scraped text (the planner
will read it). Nothing else in `queueReport` changes.

## 2. File structure

```
internal/relay/
  reconcile.go          EDIT  noteScraped const; scrapeReport passes it; queueReport skips the tail parse on it
  reconcile_test.go     EDIT  TestReconcileScrapedBodyIsNeverTailParsed (two cases)
CLAUDE.md               EDIT  one sentence in "Verifying a builder's work" (§7)
docs/plans/2026-09-19-scrape-no-tail.md   NEW  copy of this plan
```

No other file. In particular `internal/relay/e2e_test.go` is unchanged and
`internal/relay/reporttail.go` is unchanged.

## 3. Data structures & type definitions

One unexported constant in `internal/relay/reconcile.go`, placed next to
`joinNotes` (`reconcile.go:384`):

```
// noteScraped marks a report entry whose body is a terminal capture, not
// the builder's own file. A scraped body is never tail-parsed (#221).
const noteScraped = "scraped"
```

No struct changes. `store.LogEntry` is untouched.

## 4. Interface definitions & component contracts

`queueReport` keeps its signature exactly:

```
func queueReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding,
    entries []store.LogEntry, path, payload, note string, gate *store.GateRecord) (store.Binding, error)
```

New contract on the `note` parameter: when `note == noteScraped`, the body
at `path` is treated as unstructured prose -- `parseReportTail` is not
called, `outcome` is `OutcomeUnstructured`, and `tail` is the zero
`ReportTail`. All other notes behave as today.

`scrapeReport` replaces its literal `"scraped"` with `noteScraped`. No other
caller changes (`headless.go:395`, `reconcile.go:456/464/530`,
`remote.go:838`, `send_test.go:914` pass other notes and are untouched).

## 5. High-level pseudocode

In `queueReport`, replace the lines

```
body, _ := os.ReadFile(path)
tail, ok, reject := parseReportTail(body)
outcome := OutcomeUnstructured
if ok { ... } else if reject != "" { note = joinNotes(note, reject) }
```

with

```
body, _ := os.ReadFile(path)
var (
    tail   ReportTail
    ok     bool
    reject string
)
outcome := OutcomeUnstructured
if note != noteScraped {
    // A scraped body is the terminal, which holds the prompt's own
    // ```relay skeleton, truncated by the capture. The tail contract is
    // for the file the builder writes, not for what herdr had on screen.
    tail, ok, reject = parseReportTail(body)
}
if ok {
    outcome = tail.Status
} else if reject != "" {
    note = joinNotes(note, reject)
}
```

Everything after (injection scan, payload rewrite, diff capture, entry
construction using `tail.*`, `Queue`, round advance) is byte-for-byte as it
is now.

## 6. Error handling strategy

No new error paths. The parse is skipped, not error-suppressed: a scraped
round with a perfectly well-formed block on screen also stays
`unstructured` -- that is the point, and the second test case pins it.

## 7. Documentation

`CLAUDE.md`, section "Verifying a builder's work", the sentence that ends
`Run it after any change to `reconcile.go`'s nudge, fingerprint or scrape
path.` becomes:

> Run it after any change to `reconcile.go`'s nudge, fingerprint or scrape
> path, and after any change to `reporttail.go` or `queueReport`: the e2e
> suite asserts the scraped round's note exactly, and #201 changed the tail
> parser without running it, which left e2e red on `main` for a week (#221).

Keep the surrounding paragraph otherwise as it is.

## 8. Tests (`internal/relay/reconcile_test.go`)

Add one test after `TestReconcileQuiescentWithoutReportStillScrapes`
(`reconcile_test.go:~1513`), using the same helpers (`sentBinding`,
`withClock`, `fakeClock`, `plannerWith`, `builderAgent`, `reconcile`,
`rt.Store.PendingForPlanner`), driven the same way: first `reconcile` nudges,
`clock.Advance(nudgeGrace + time.Second)`, second `reconcile` scrapes.

`TestReconcileScrapedBodyIsNeverTailParsed` -- table of two cases over
`fakeHerdr.readOut`:

1. `unclosed`: readOut is
   `"I implemented the guard clause\n```relay\nstatus: done\nchanged_paths:\n  - internal/x.go\n"`
   (an opening fence, never closed -- the #221 shape).
   Expect: round closed (`got.Round == 2`), `pending.Note == "scraped"`
   exactly (not `"scraped tail: unclosed fence"`), `pending.Outcome ==
   OutcomeUnstructured`, `pending.ChangedPaths == nil`.
2. `wellformed`: readOut is
   `"I stopped.\n```relay\nstatus: halted\nhalted_at: step 3\nchanged_paths:\n  - internal/x.go\n```\n"`
   (a block the parser *would* accept).
   Expect: `pending.Note == "scraped"`, `pending.Outcome ==
   OutcomeUnstructured`, `pending.HaltedAt == ""`, `pending.ChangedPaths ==
   nil` -- proving the parse is skipped, not merely its error hidden.

Also assert in both cases that the report file at
`rt.Store.ReportPath("webshop", 1)` starts with `<!-- SCRAPED` and contains
the readOut text (the scrape itself is unchanged).

**Mutation check (run and report both outcomes):** change
`if note != noteScraped {` to `if true {` in `queueReport`; case 1 must fail
on the note and case 2 must fail on the outcome; restore by re-editing the
line (never `git checkout` the file); both pass. If either case passes under
the mutation, halt: the test is not pinning the change.

Existing tests that must still pass unchanged:
`TestReconcileQuiescentWithoutReportStillScrapes`,
`TestReconcileScrapesAfterNudgeFails`,
`TestReconcileQuiescenceTerminalUnchangedScrapes`,
`TestReconcileQuiescenceResetThenUnchangedScrapes`, `TestParseReportTail`,
and every test in `send_test.go` that calls `queueReport` directly.

## 9. Ordered implementation steps

1. `reconcile.go`: add `noteScraped`; use it in `scrapeReport`; apply §5 in
   `queueReport`. `go build ./... && go vet ./internal/relay`.
2. `reconcile_test.go`: add the test per §8. `go test ./internal/relay -run
   'TestReconcileScrapedBodyIsNeverTailParsed|Scrape|ParseReportTail' -count=1`.
   Run the mutation check; record both outcomes for the report.
3. `CLAUDE.md` per §7.
4. Copy the plan to `docs/plans/2026-09-19-scrape-no-tail.md`. One commit,
   subject from the header. Gate, exactly:
   ```
   test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
   make check
   ```
   Report the tail verbatim, the mutation outcomes, and
   `git diff --stat main..HEAD`.

## 10. Report

End `NNN-report.md` with the ```relay block (`status`, `halted_at`,
`changed_paths`, `commands_run`, `not_done`). Say explicitly that `make e2e`
was **not** run and is owed by the planner.
