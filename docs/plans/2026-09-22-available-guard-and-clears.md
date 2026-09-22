# `relay available`: refuse an unknown subject, and record a clear in the history (#301, #302)

This plan stands alone: everything you need is in this file and in the tree.
If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it. A halt that names a design error is
worth more than a green suite that bent a test to fit.

Run every command in the foreground. Do not start subagents. Do not run
`make e2e` (§8 says why).

## 1. System overview

`relay.Available` (`internal/relay/ledger.go:160`) lifts rate-limit gates
from the ledger. It has three callers:

| caller | path | who clears |
|---|---|---|
| `cmdAvailable` (`cmd/relay/main.go:755`) | `relay available <subject>` on a client, then `ForwardAvailable` to every server the bindings name | a planner |
| `(*Server).handleAvailable` (`internal/serve/bindings.go:390`) | `POST /v1/available`, i.e. a client's forwarded clear | a planner, remotely |
| `serve.AdminAvailable` (`internal/serve/admin.go:364`) | `relay serve available` run on the server host | the server's operator |

Two defects, one function:

- **#301.** `Available` accepts any string. `relay available clinepass`
  (the provider is `cline-pass`) prints `nothing was gating clinepass`,
  which looks exactly like a real no-op, so the gate the user meant to
  clear stays in place. `Unavailable` checks its token against the
  candidates and `Available` does not. The serve handler partly patches
  this with its own pre-check that refuses unknown *tokens* but lets any
  bare provider through.
- **#302.** A clear writes to `ledger.json` but not to `availability.json`.
  The 30-day history records when a block started but never when it ended,
  so it cannot show how long a provider stayed down (a ~5h cline-pass block
  is its session limit; a multi-day one is weekly or monthly). The spec
  records this as deliberate (`docs/specs/2026-09-11-availability-history-design.md`
  §4.1: "a clear is not an observation"). This plan reverses that.

The fix puts both rules **inside `Available`**, so all three callers behave
the same and nothing can bypass them:

1. Under the state lock, `Available` loads and prunes the ledger, then asks
   a pure function, `ResolveClearSubject`, whether the subject names
   something relay knows. A subject is known when it names a configured
   candidate or provider, **or** a provider the ledger currently gates.
   The second case covers a gate left behind after its candidate was
   removed from `candidates.json`, which must stay clearable (#301's open
   question). Unknown means an error and nothing is written.
2. When at least one entry was removed, `Available` appends one history
   event of the new kind `cleared`. The event carries `Since`, the `At` of
   the oldest removed entry, so "blocked for" is `At - Since` and nothing
   has to parse a note.
3. `relay policy`'s history block gains a `cleared` row in the hourly grid,
   plus a "blocked for" summary per provider (clear count, median, longest).

## 2. Files

```
internal/candidate/candidate.go        Set.Providers (new method)
internal/candidate/candidate_test.go   TestSetProviders
internal/history/history.go            Cleared kind; Event.Since; BlockedDurations
internal/history/history_test.go       TestBlockedDurations; TestSinceOmittedWhenZero
internal/relay/available.go     (new)  ErrUnknownProvider; ClearedByPlanner/ClearedByServer;
                                       ResolveClearSubject; suggestProvider; editDistance
internal/relay/available_test.go (new) TestResolveClearSubject; TestSuggestProvider
internal/relay/ledger.go               Available: new signature and body (§5.1)
internal/relay/ledger_test.go          update the Available callers; replace
                                       TestAvailableLeavesHistory (§6)
internal/relay/policy_view.go          formatHistory: cleared row + blocked-for block; blockedText
internal/relay/policy_view_test.go     TestFormatHistoryBlockedFor (add to the existing file)
internal/serve/bindings.go             handleAvailable: drop the pre-check, pass ClearedByPlanner
internal/serve/admin.go                AdminAvailable: pass ClearedByServer
internal/serve/serve_test.go           TestAvailableRefusesUnknownProvider (new)
internal/serve/admin_test.go           TestAdminAvailableRecordsServerClear (new)
cmd/relay/main.go                      cmdAvailable: pass ClearedByPlanner (no other change)
docs/specs/2026-09-11-availability-history-design.md   §4.1 amendment (§7 step 5)
docs/plans/2026-09-22-available-guard-and-clears.md    copy of this plan (last step)
```

If `internal/relay/policy_view_test.go` does not exist, put the new test in
whichever `_test.go` file in `internal/relay` already tests `formatHistory`
or `FormatPolicy`, and say which one in the report. Touch nothing else. If
another file has to change to compile (a caller of `Available` this plan
missed), halt and report it rather than editing it.

## 3. Probes (first; paste the output verbatim in the report)

```
grep -rn "Available(" --include=*.go . | grep -v _test | grep -v "\.claude/worktrees" | grep -v "ForwardAvailable\|AdminAvailable(s\|func (c"
grep -rn "Cleared\b\|ErrUnknownProvider\|ResolveClearSubject\|ClearedBy" --include=*.go internal cmd
ls internal/relay/policy_view_test.go
```

The first must list exactly three production call sites of
`relay.Available`: `cmd/relay/main.go`, `internal/serve/bindings.go` and
`internal/serve/admin.go`. The second must print nothing, apart from
comments in `internal/store/types.go` that use the English word "Cleared".
If either says otherwise, halt.

## 4. Contracts

### 4.1 `candidate.Set.Providers` (`internal/candidate/candidate.go`)

```
// Providers returns every distinct provider the configured candidates use,
// sorted. A nil *Set returns nil: callers hold a possibly-nil set (a server
// with no candidates.json), and this must not panic on one.
func (s *Set) Providers() []string
```

### 4.2 History (`internal/history/history.go`)

```
// Cleared is the history-only kind a manual clear records (#302). It never
// enters the ledger (ledger validation would reject it there); it exists
// so availability.json can say when a block ended, not only when it began.
const Cleared ledger.Kind = "cleared"
```

`Event` gains one field, added after `Note`:

```
// Since is, on a Cleared event, the At of the oldest ledger entry the clear
// removed: At - Since is how long the provider was blocked. Zero on every
// other kind, and omitted from the JSON when zero.
Since time.Time `json:"since,omitzero"`
```

The module is `go 1.25.0`, so `omitzero` is supported. Do not use
`omitempty`, which never omits a `time.Time`.

```
// BlockedDurations returns At - Since for every Cleared event on provider
// whose Since is non-zero and not after At, in event order. Pure.
func BlockedDurations(h History, provider string) []time.Duration
```

`FromEntry`, `Prune`, `Append`, `HourCounts`, `Load` and `Save` do not
change.

### 4.3 The subject rule (`internal/relay/available.go`, new)

```
// ErrUnknownProvider is returned when a clear names a provider that no
// configured candidate uses and the ledger does not gate.
var ErrUnknownProvider = errors.New("unknown provider")

// Who cleared a gate, as recorded in the history event's Source.
const (
    ClearedByPlanner = "planner" // relay available, local or forwarded over POST /v1/available
    ClearedByServer  = "server"  // relay serve available on the server host
)

// ResolveClearSubject decides what `relay available <subject>` clears, and
// refuses a subject relay knows nothing about (#301). Pure: the caller
// passes the configured set (possibly nil) and the already-pruned ledger.
//
//   subject parses as a candidate token (candidate.ParseRef succeeds):
//     provider := ref.Provider
//     known if set != nil and set.Lookup(ref) succeeds,
//        or if l has a RateLimited entry whose Subject == provider
//     otherwise: return set.Lookup's error unchanged (it wraps
//        candidate.ErrUnknownCandidate); when set is nil, return
//        fmt.Errorf("candidate %q not found (no candidates configured): %w",
//        subject, candidate.ErrUnknownCandidate)
//   otherwise (a bare provider):
//     provider := subject
//     known if provider is in set.Providers(),
//        or if l has a RateLimited entry whose Subject == provider
//     otherwise: return an error wrapping ErrUnknownProvider (§4.4)
func ResolveClearSubject(set *candidate.Set, l ledger.Ledger, subject string) (provider string, err error)
```

Never call `Lookup` on a nil `*Set`: it dereferences the set and panics.

### 4.4 The unknown-provider message

Exactly one of these two forms:

```
no configured candidate uses provider "clinepass" (known: anthropic, cline-pass, openai); did you mean "cline-pass"?
no configured candidate uses provider "zzz" (known: anthropic, cline-pass, openai)
```

- `known:` is `set.Providers()` joined with `", "`. When that is empty,
  the parenthesis reads `(no candidates configured)`.
- The `; did you mean "<p>"?` tail appears only when `suggestProvider`
  returns a non-empty string.
- The error wraps `ErrUnknownProvider`, so `errors.Is(err, ErrUnknownProvider)`
  is true. Build it with `fmt.Errorf("...: %w", ErrUnknownProvider)`, then
  check that the rendered text is exactly the form above. If `%w` forces
  extra text such as `: unknown provider` onto the end, return a small
  unexported error type whose `Error()` gives the exact text and whose
  `Unwrap()` returns `ErrUnknownProvider`. Say which one you used.

```
// suggestProvider returns the known provider closest to subject, or "".
//   norm(x) := lower-case x with every rune outside [a-z0-9] removed
//   a known p qualifies when norm(p) == norm(subject)          -> distance 0
//                       or editDistance(lower(p), lower(subject)) <= 2
//   return the qualifier with the smallest distance, where a norm match
//   counts as 0; on a tie, the alphabetically first. None -> "".
func suggestProvider(known []string, subject string) string

// editDistance is Levenshtein distance over runes. Pure, unexported.
func editDistance(a, b string) int
```

### 4.5 `Available` (`internal/relay/ledger.go`)

```
// Available clears every rate-limit gate on subject's provider. subject may
// be a candidate token or a bare provider name; ResolveClearSubject decides
// what it names and refuses one relay knows nothing about (#301). A clear
// that removed anything is recorded in the availability history as a
// Cleared event whose Source is source (#302). Zero removed is not an error
// and records nothing -- the caller reports that nothing was gating.
//
// source must be ClearedByPlanner or ClearedByServer; anything else is a
// programming error and returns an error before any write.
func Available(rt Runtime, subject, source string) (provider string, removed int, err error)
```

Errors: `ErrUnknownProvider`, `candidate.ErrUnknownCandidate` (both
wrapped), ledger load/save errors, and the bad-source error. A history
write failure is **not** an error: print it to stderr and drop it, exactly
as `appendEntryLocked` does (`ledger.go:64-72`), because the ledger write
already happened.

Postconditions:
- on any error, `ledger.json` and `availability.json` are byte-identical
  to before the call
- on success with `removed == 0`: the ledger is pruned and saved, which is
  what `mutateLedger` did before, and the history is untouched
- on success with `removed > 0`: exactly one `Cleared` event is appended

## 5. Pseudocode

### 5.1 `Available`

```
if source not in {ClearedByPlanner, ClearedByServer}:
    return "", 0, fmt.Errorf("available: unknown clear source %q", source)

var oldest time.Time
err = rt.Store.WithLock(func(*store.Tx) error {
    l := ledger.Load(rt.LedgerPath)             -- error -> return it
    l = l.Prune(rt.Now())
    provider, err = ResolveClearSubject(rt.Candidates, l, subject)
    if err: return err                          -- nothing saved
    for e in l.Entries where e.Kind == RateLimited && e.Subject == provider:
        removed++
        if oldest.IsZero() || e.At.Before(oldest): oldest = e.At
    ledger.Save(rt.LedgerPath, l.Clear(RateLimited, provider))   -- error -> return it
    if removed > 0:
        ev := history.Event{At: rt.Now(), Kind: history.Cleared,
                            Provider: provider, Source: source,
                            Note: fmt.Sprintf("cleared %d entries", removed),
                            Since: oldest}
        h, herr := history.Load(rt.AvailabilityPath)
        if herr == nil: herr = history.Save(rt.AvailabilityPath, h.Prune(rt.Now()).Append(ev))
        if herr != nil: fmt.Fprintf(os.Stderr, "relay: could not record history: %v\n", herr)
    return nil
})
if err: return provider, 0, err
return provider, removed, nil
```

Everything happens inside one `WithLock`. Do **not** call `mutateLedger`
or `mutateLedgerLocked` from `Available`: their callback cannot return an
error, and the refusal has to stop the save. Both functions stay (other
callers use them).

### 5.2 Callers

- `cmdAvailable`: `relay.Available(rt, subject, relay.ClearedByPlanner)`.
  Its existing `if err != nil { return err }` already returns before
  `ForwardAvailable`, so an unknown subject exits non-zero and is **not**
  forwarded to any server. Do not change that order. The CLI prints the
  error through the existing `relay: <err>` path. Add nothing else.
- `handleAvailable`: delete the whole `candidate.ParseRef` / `Lookup`
  pre-check block and its comment (`bindings.go`, the block starting
  `// relay.Available resolves a token to its provider but`). Call
  `relay.Available(rt, req.Subject, relay.ClearedByPlanner)`. The existing
  error branch already answers `422 CodeInvalid` with `err.Error()`, so an
  unknown subject now gets a 422 carrying the same message as the local
  refusal. Update the function's doc comment: a bare provider that gates
  nothing is still a 200 with `Removed 0` **when it is known**, and an
  unknown one is a 422. If removing the block leaves the `candidate`
  import unused, remove the import.
- `AdminAvailable`: `relay.Available(ledgerRuntime(s), subject, relay.ClearedByServer)`.
  `cmdServeAvailable` already returns the error, so `relay serve available
  zzz` exits non-zero. No change there.

### 5.3 `formatHistory` (`internal/relay/policy_view.go`)

- `kinds` becomes `[]ledger.Kind{ledger.RateLimited, ledger.SpawnFailed, history.Cleared}`,
  so a provider with clears gets a third grid row. Its label is
  `GateKindText(history.Cleared)`, which already falls through to
  `string(k)` = `cleared`. Do not add a case to `GateKindText`.
- Update the doc comment's ordering sentence to name the third kind.
- After the last grid row, when at least one provider has a non-empty
  `history.BlockedDurations(hist, p)`, append:

```
blocked for (30d, gates cleared by hand)
  cline-pass  3 clears  median 5h00m  longest 3d00h
```

  - header line exactly `blocked for (30d, gates cleared by hand)`, with no
    blank line before it
  - one row per such provider, providers sorted, formatted
    `fmt.Sprintf("  %-10s %s  median %s  longest %s\n", p, count, blockedText(med), blockedText(max))`
  - `count` is `"1 clear"` or `"<n> clears"`
  - median: sort ascending; odd n takes the middle element; even n takes
    index `n/2-1` (the lower middle, so no averaging)
  - `peakText` and every other line of `FormatPolicy` are unchanged

```
// blockedText renders a blocked duration compactly, flooring:
//   d < 1h   -> "<m>m"          45m -> "45m"; 0 -> "0m"
//   d < 24h  -> "<h>h<mm>m"     5h2m -> "5h02m"
//   else     -> "<d>d<hh>h"     52h -> "2d04h"; 72h -> "3d00h"
func blockedText(d time.Duration) string
```

## 6. Tests

Every bullet names a mutation. Make it, run the named test, confirm it
**fails**, then undo the mutation **by editing it back**. Never undo with
`git checkout <file>`, which also throws away your uncommitted work. If a
sed-based mutation is used, confirm with `grep -c` that it actually changed
the file before trusting the result. A test that passes both ways pins
nothing.

In `internal/relay`, build candidate sets with the existing
`candidateSet(t, json)` helper that `newRuntime` (`bind_test.go:25`) uses.
In `internal/candidate` and `internal/serve`, write a `candidates.json`
under `t.TempDir()` and call `candidate.Load`. Use providers `test`,
`cline-pass` and `openai` where a case needs them. `newRuntime`'s clock is
the field `rt.Now` (fixed at `baseTime`). Advance it by reassigning that
field.

1. `TestSetProviders` (`internal/candidate`): duplicates collapse, output
   sorted, nil `*Set` returns nil without panicking.
   Mutate: drop the sort. The test must fail.
2. `TestBlockedDurations` (`internal/history`): a Cleared event with
   `Since` 5h before `At` gives `[5h]`; a Cleared event with zero `Since`,
   a RateLimited event and another provider's Cleared event are skipped.
   Mutate: drop the zero-`Since` guard. The test must fail.
3. `TestSinceOmittedWhenZero` (`internal/history`): a RateLimited event
   marshals without a `"since"` key, and a Cleared event with `Since` set
   round-trips through `Save`/`Load` equal.
   Mutate: `omitzero` -> `omitempty`. The test must fail.
4. `TestResolveClearSubject` (`internal/relay`), a table:

   | set providers | ledger gates | subject | want |
   |---|---|---|---|
   | test, cline-pass | -- | `claude/test/m` (configured) | `test`, nil |
   | test, cline-pass | -- | `cline-pass` | `cline-pass`, nil |
   | test, cline-pass | -- | `clinepass` | `errors.Is(ErrUnknownProvider)`; message exactly `no configured candidate uses provider "clinepass" (known: cline-pass, test); did you mean "cline-pass"?` |
   | test, cline-pass | -- | `zzz` | ErrUnknownProvider; message has no `did you mean` |
   | test | `gone` | `gone` | `gone`, nil (gate left behind by a removed candidate) |
   | test | `gone` | `claude/gone/m` (not configured) | `gone`, nil |
   | test | -- | `claude/nope/m` | `errors.Is(candidate.ErrUnknownCandidate)` |
   | nil set | -- | `anything` | ErrUnknownProvider; message contains `(no candidates configured)` |
   | nil set | -- | `claude/x/m` | ErrUnknownCandidate, no panic |

   Mutate: drop the ledger clause of the bare-provider branch. The `gone`
   row must fail.
5. `TestSuggestProvider`: `clinepass` -> `cline-pass` (norm match);
   `openia` -> `openai` (distance 2); `zzz` -> `""`; a tie between two
   distance-1 candidates returns the alphabetically first.
   Mutate: `<= 2` -> `< 2`. The `openia` case must fail.
6. `TestAvailableRecordsClear` (replaces `TestAvailableLeavesHistory` in
   `ledger_test.go`): `Unavailable` at T0, advance `rt.Now` by 5h,
   `Available(rt, "test", ClearedByPlanner)`. The ledger now has no
   entries. The history has 2 events, and the second has Kind
   `history.Cleared`, Provider `test`, Source `planner`, `Since == T0` and
   `At == T0+5h`, where T0 is `baseTime`.
   Mutate: set `Since` to `rt.Now()`. The test must fail.
7. `TestAvailableNothingClearedRecordsNothing`: a configured provider with
   no gate gives `removed == 0`, a nil error, and no history events.
   Mutate: drop the `removed > 0` guard. The test must fail.
8. `TestAvailableRefusesUnknownWritesNothing`: `Unavailable` on
   `claude/test/m`, then `Available(rt, "tset", ClearedByPlanner)` returns
   ErrUnknownProvider. `ledger.json` and `availability.json` are
   byte-identical to before the second call (read them before and after).
   Mutate: move the `ResolveClearSubject` call after the `Save`. The test
   must fail.
9. `TestAvailableRejectsBadSource`: `Available(rt, "test", "bogus")`
   returns an error and writes nothing.
10. The existing `TestAvailableByTokenAndByProvider` and
    `TestAvailableLeavesSpawnFailures` keep their assertions. Only the call
    gains `ClearedByPlanner`.
11. `TestAvailableRefusesUnknownProvider` (`internal/serve/serve_test.go`,
    shaped like `TestAvailableClearsServerWideGate` at line 1114):
    `POST /v1/available` with subject `anthropc` answers 422, and the body
    contains `no configured candidate uses provider "anthropc"`.
    Mutate: restore the old handler behaviour by passing a bare provider
    through unchecked (have `ResolveClearSubject` return `subject, nil` for
    the bare branch). The test must fail.
12. `TestAdminAvailableRecordsServerClear` (`internal/serve/admin_test.go`):
    `AdminUnavailable` then `AdminAvailable`. The server's
    `availability.json` ends with a Cleared event whose Source is `server`.
    Mutate: pass `ClearedByPlanner` in `AdminAvailable`. The test must fail.
13. `TestFormatHistoryBlockedFor`: a history with RateLimited and Cleared
    events for `cline-pass` (blocked 1h, 5h and 72h) and none cleared for
    `test`. Assert the grid has a `cleared` row for cline-pass, and the
    output ends with exactly:
    ```
    blocked for (30d, gates cleared by hand)
      cline-pass  3 clears  median 5h00m  longest 3d00h
    ```
    Add a second case with durations 1h and 5h (even n), where the median
    is `1h00m`. Add a table test for `blockedText` covering 0, 45m, 5h2m,
    52h and 72h.
    Mutate: take index `n/2` for even n. The even case must fail.

No test in `cmd/relay`: `cmdAvailable` builds a real runtime, and CI
runners have no herdr. The rule it relies on is `ResolveClearSubject`,
tested as a pure function in `internal/relay`. A cmd/relay test must never
read the user's real config (#235). You are adding none, so this is only a
reminder not to.

## 7. Steps (one commit each, in order)

1. `candidate.Set.Providers` and the history changes (§4.1, §4.2) with
   tests 1-3.
   `feat(history): a cleared kind with the time the block began (#302)`
   Done when `go test ./internal/candidate/ ./internal/history/` passes and
   mutations 1-3 each failed.
2. `internal/relay/available.go` (§4.3, §4.4) with tests 4-5. No callers
   yet.
   `feat(available): know what a clear names before clearing it (#301)`
   Done when mutations 4-5 each failed.
3. `Available`'s new signature and body (§4.5, §5.1), plus every caller
   (§5.2): `cmd/relay/main.go`, `internal/serve/bindings.go`,
   `internal/serve/admin.go`. Tests 6-12.
   `fix(available): refuse an unknown provider and record every clear (#301, #302)`
   Done when `go build ./...` succeeds, tests 6-12 pass and mutations
   6-8, 11 and 12 each failed.
4. `formatHistory` and `blockedText` (§5.3) with test 13.
   `feat(policy): show how long a provider stayed blocked (#302)`
   Done when mutation 13 failed.
5. The spec amendment. In `docs/specs/2026-09-11-availability-history-design.md`
   §4.1, replace the sentence ending `stay for` + `` `Available` (a clear is not an observation). ``
   with: `` `mutateLedger` / `mutateLedgerLocked` stay for other callers.
   `Available` no longer uses them: since #302 it takes the lock itself,
   refuses an unknown subject (#301), and records a manual clear as a
   `cleared` history event whose `since` is the oldest cleared entry's
   `at`, because how long a block lasted is the observation the history
   was missing. `` Change no other line of the spec.
   `docs(specs): a clear is an observation after all (#302)`
6. Copy the plan file you were handed, verbatim, to
   `docs/plans/2026-09-22-available-guard-and-clears.md`.
   `chore(plans): relay available guard and clears`

## 8. Gate

```
make check
```

`make check` runs gofmt over `git ls-files '*.go'`, `go vet`, the full test
suite and the `go mod tidy` check. It must exit 0. Paste its last 15 lines.

Also run `go test -race -count=1 ./internal/relay/ ./internal/serve/ ./internal/history/ ./internal/candidate/`
and paste its tail.

`make e2e` is **not** in this gate and you must not run it: nothing here
touches `reconcile.go`'s nudge, fingerprint or scrape path,
`reporttail.go` or `queueReport`. Say so in the report instead of running
it.

## Report

1. The §3 probe output, verbatim.
2. For each step: its commit sha, the tests it added, and for each
   mutation in §6 the exact change you made and the failing test's name
   and failure line.
3. The error-construction choice from §4.4 (plain `%w`, or an unexported
   type) and the exact text `relay available clinepass` would print. Run
   the rule through a test or a scratch `go run`, not the real CLI against
   your own state.
4. Every exported identifier you added or changed, with its signature.
5. `git diff --stat main...HEAD` in full. It must name only §2's files.
6. The `make check` tail and the `-race` tail.
7. Anything you did that this plan did not say, and anywhere you halted.
