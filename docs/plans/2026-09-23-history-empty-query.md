# Plan: `relay history` without `-q` prints its rows

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. Halt and
report if any of the following is false.

- **`histq.AxisNone` is `Axis("none")`** (`internal/histq/histq.go` ~57), not
  the zero value. **`histq.ParseAt` sets `By: AxisNone`** (~151).
- **`HistoryOptions.Filter` (`internal/relay/history.go` ~137) starts from
  `var q histq.Query`**, leaves it as the zero value (`By == ""`) when
  `o.Query` is blank, and stores it with `o.parsed = q`.
- **`cmdHistory` (`cmd/relay/history.go` ~193–212) regroups whenever
  `axis != histq.AxisNone`**, where `axis` is `opts.ParsedQuery().By`,
  overridden by a valid `--by`.
- **`histq.Group` returns nil for `""`**, and `relay.FormatGroups` of an
  empty group list prints `no rounds`.
- **`cmdHistory` spawns no harness and makes no network call.** It builds
  a runtime, opens `relay.db` and prints. If `newRuntime` or `cmdHistory`
  reaches a harness or the network, do not add the step-3 test. Halt and
  report instead: CI runners have neither.

## 1. System Overview

`relay history` prints `no rounds` for any invocation without a `-q` query:
plain `relay history`, `--since 30d`, `--binding x`, `--limit 0`, even when
`relay.db` holds hundreds of rounds. Only `-q …` works. It has been broken
since #254 (2026-09-21), which added `-q`/`--by`.

The planner traced it with debug prints on a copy of a real database. The
SQL returned 200 rows, and `Apply` kept all 200. Then:

1. Without `-q`, `Filter` leaves the parsed query's `By` as `""`.
   `ParseAt` would have set `AxisNone` (`"none"`).
2. `cmdHistory` compares `axis != histq.AxisNone`, which is true for `""`,
   so it takes the regroup path.
3. `histq.Group(rows, "")` returns nil, and `FormatGroups(nil)` prints
   `no rounds`.

The same zero value gives a second symptom. `--by builder` without `-q`
reaches `Filter`'s override check, `q.By != histq.AxisNone && string(q.By) != o.By`
(~180), which is true for `""`. So it prints a spurious
`note: --by overrides by: from -q` although there is no query.

`internal/ui/dash` already guards against both values
(`model.go` ~369: `m.query.By != histq.AxisNone && m.query.By != ""`).

**Fix:**
- At the source: `Filter` starts from `histq.Query{By: histq.AxisNone}`, so
  a parsed query always names an axis.
- Defensively in `cmdHistory`: a small pure helper resolves the axis and
  treats `""` as none. A future path that bypasses `Filter` then cannot
  regress this.

**Out of scope:**
- `histq` itself; do not change `AxisNone`'s value
- `internal/ui/dash`
- `FormatGroups`/`FormatHistory`
- the SQL
- any flag or output change beyond the bug

## 2. File Structure

```
internal/relay/history.go       # Filter starts from Query{By: AxisNone}
internal/relay/history_test.go  # + Filter rows
cmd/relay/history.go            # + historyAxis helper; cmdHistory uses it
cmd/relay/history_test.go       # + historyAxis table + one end-to-end history test
```

No other file changes.

## 3. Data Structures & Type Definitions

None change.

## 4. Interface Definitions & Component Contracts

### `HistoryOptions.Filter` (changed)

The initial query is `histq.Query{By: histq.AxisNone}` instead of the zero
value. When `o.Query` is non-blank, `ParseAt`'s result replaces it, as
today. Nothing else changes.

Postcondition: `o.ParsedQuery().By` is never `""` after `Filter` returns
without error.

### `historyAxis(parsed histq.Query, by string) histq.Axis` (new, pure, `cmd/relay/history.go`)

- If `by` is non-empty and `histq.ParseAxis(by)` is ok, return that axis.
  `validateHistoryBy` has already rejected a bad `--by`, as today.
- Otherwise, if `parsed.By == ""`, return `histq.AxisNone`.
- Otherwise return `parsed.By`.

`cmdHistory` replaces its inline `axis := parsed.By; if *by != "" {…}` block
with `axis := historyAxis(parsed, *by)`. The `if axis != histq.AxisNone`
branch that follows is unchanged.

## 5. High-Level Pseudocode

```
Filter:
  q := Query{By: AxisNone}
  if o.Query not blank: q = ParseAt(o.Query)        # unchanged
  ... flag overrides unchanged; the --by note now only fires against a real by: ...

cmdHistory:
  rows := DB.Query(f); rows = parsed.Apply(rows)    # unchanged
  axis := historyAxis(parsed, *by)                  # "" -> AxisNone
  axis != AxisNone ? grouped : FormatHistory(rows)  # unchanged
```

## 6. Error Handling Strategy

No new errors or messages. The only visible changes:
- rows print where `no rounds` was printed wrongly
- the spurious `--by` note is gone

## 7. Ordered Implementation Steps

After every step, run `make check` and `gofmt -l $(git ls-files '*.go')`.
The second must print nothing.

1. **Source fix.** Change `Filter`'s initial query.
   *Verify:* new `internal/relay/history_test.go` tests:
   - `HistoryOptions{}` with `Filter` then `ParsedQuery().By` gives
     `histq.AxisNone`, with no notes.
   - `HistoryOptions{By: "builder"}` gives no notes. Before the fix it gave
     the spurious `--by` note.
   - `HistoryOptions{Query: "by:binding", By: "builder"}` still gives the
     override note. The real conflict is kept.

   Every existing test passes unedited.

   *Mutation (required):* revert to the zero-value `var q histq.Query`, and
   confirm the first two new tests fail. Restore it.

2. **Defensive axis helper.** Add `historyAxis`, and use it in
   `cmdHistory`.
   *Verify:* a pure `TestHistoryAxis` table in `cmd/relay/history_test.go`:
   - `(Query{}, "")` gives `AxisNone`.
   - `(Query{By: AxisNone}, "")` gives `AxisNone`.
   - `(Query{By: AxisBuilder}, "")` gives `AxisBuilder`.
   - `(Query{}, "binding")` gives `AxisBinding`.
   - `(Query{By: AxisBuilder}, "binding")` gives `AxisBinding`.

3. **End to end.** Add `TestHistoryPlainListsRounds` in
   `cmd/relay/history_test.go`. It runs the real subcommand, which is
   allowed only because `cmdHistory` spawns no harness and makes no network
   call; confirm that in the report, per the halt list.
   - Set `XDG_STATE_HOME` to a `t.TempDir()` with `t.Setenv`, so the
     package's `TestMain` root and the user's state are never touched
     (#235).
   - Open `<that dir>/relay/relay.db` with `db.Open`. Upsert one binding
     (name `histcase`) and one round (number 1, outcome `reported`),
     filling only the fields the write API requires. Close it.
   - `captureOutput(t, func() error { return run([]string{"history"}) })`:
     stdout contains `histcase`, and not `no rounds`.
   - The same with `--since 3650d`, and with `--binding histcase`.

   *Mutation (required):* revert both the step-1 and the step-2 changes
   together, and confirm `TestHistoryPlainListsRounds` fails. Then restore
   only step 2's helper and confirm it passes. That shows the helper alone
   also closes the bug. Restore everything.

## Report

- `git diff --stat main...HEAD`. It must be exactly the four files in §2.
- Both required mutation checks, with the named tests that failed and
  passed.
- What you found for each halt condition, including what `newRuntime`
  touches, since that decides whether step 3 is allowed.
