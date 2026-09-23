# Plan: a consult's pick is not the round's builder; one `relay tab` row per model

These are two small attribution bugs, found while building `relay stats`
(#366). They are in separate packages and do not depend on each other, so
they run as two steps in one round.

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. Halt and
report if any of the following is false.

- **`ask` writes its pick into the binding's log under the consult's round**,
  as `KindPick` with note `"picked <tok> for <role>: …"`, where role is the
  consult role, never `builder` (`internal/relay/ask.go` ~222,
  `pickEntry(rt.Now(), consult.Round, role.Name, res)`).
- **`ingest.builderForRound` (`internal/ingest/outcome.go` ~75) takes the
  last `KindPick` or `KindSwitch` in round n and does not check the role.**
  Its only production caller is `ingest.go` ~427, which writes the result
  into the round's `builder_candidate`/`builder_harness`/`builder_provider`/
  `builder_model` through `tx.UpsertRound`. `UpsertRound` overwrites those
  columns on an existing row (`internal/db/write.go` ~314).
- **The builder pick shapes relay writes** are
  `"picked <tok> for builder: …"` (`candidate.go` `ExplainResolution`) and
  `"picked <tok> on <server>: <how>"` (`remote.go` `remotePickEntry`). The
  existing ingest tests also use `"picked <tok> on host1: …"`.
- **`tabKey` (`internal/relay/tab.go` ~94) builds the `--by model` group as
  `Provider + "/" + Model` from the entry's usage, and keeps a `#effort`
  suffix on the model.** `statsUsageKey` and `refKey` (`internal/relay/stats.go`)
  each cut the model at its first `#` with their own inline code.

## 1. System Overview

**Bug 1: `relay.db` can name a reviewer as a round's builder.**
`relay ask` records its candidate pick in the binding's log as a `KindPick`
entry in the current round. It is the same kind a builder pick uses; only
the role in the note differs (`for reviewer:`). When the daemon ingests the
log, `builderForRound` takes the round's **last** pick or switch, whichever
role it names. So a consult asked after the round's builder pick overwrites
that round's `builder_*` columns with the reviewer's candidate. Then
`relay history`, `relay show` and anything else reading `relay.db` names the
wrong builder.

**Fix:** `builderForRound` skips a `KindPick` whose note names a role other
than `builder`. It uses a negative rule: skip only a pick whose note reads
`"picked <tok> for <role>:"` with a role that is not `builder`. The
`" on <server>:"` remote shape and any older shape are kept exactly as they
are parsed today. `KindSwitch` entries are always builder events and are
unchanged.

**Bug 2: `relay tab --by model` can show one model as two rows.**
`Usage.Model` sometimes carries the candidate's effort suffix
(`cline-pass/deepseek-v4.1-flash#high`) and sometimes does not. `tabKey`
groups on it verbatim, so the same model splits into two rows. `relay stats`
fixed the same thing in `statsUsageKey` (#366 r2).

**Fix:** one unexported helper, `modelSansEffort`, cuts a model at its first
`#`. `tabKey`'s model branch, `statsUsageKey` and `refKey` all use it. A
`:effort` suffix belongs to the model and stays, as it does in ingest's
`stripEffortHash`.

**Out of scope:**
- the `relay.db` schema and migrations
- `relay db backfill`'s behaviour
- `parsePickNote`/`parseSwitchNote` themselves
- the fallback to `b.BuilderCandidate` when a round has no builder pick
- `tabKey`'s `binding`, `owner` and `provider` branches
- any `relay stats` output change beyond the refactor, which must be
  byte-identical
- the README

## 2. File Structure

```
internal/ingest/
  outcome.go       # builderForRound skips non-builder picks; + isRolePick
  outcome_test.go  # + rows
internal/relay/
  tab.go           # + modelSansEffort; tabKey's model branch uses it
  tab_test.go      # + rows
  stats.go         # statsUsageKey and refKey call modelSansEffort (no behaviour change)
```

No other file changes.

## 3. Data Structures & Type Definitions

None change.

## 4. Interface Definitions & Component Contracts

### `isRolePick(note string) bool` (new, unexported, `internal/ingest/outcome.go`)

Pure. It returns true when the note has the form `"picked <tok> for <role>:"`
with `role != "builder"`:

- The note starts with `"picked "`.
- The token is the text after that, up to the next space.
- The text after the token starts with `" for "`.
- The role is the text between that and the next `":"`. It must be
  non-empty, contain no space, and not be `builder`.

Everything else returns false: builder picks, remote `" on "` picks, and
unrecognised or older shapes. The doc comment says it recognises the
consult pick `ask` writes, and deliberately nothing else.

### `builderForRound` (changed)

In the loop, a `KindPick` entry for which `isRolePick(e.Note)` is true is
skipped. It does not become `lastKind`/`lastNote`. Nothing else changes:
- the fallback to `b.BuilderCandidate`
- the effort strip
- the return contract
- `KindSwitch` handling

Update the doc comment to say that a consult's pick is not the builder.

### `modelSansEffort(model string) string` (new, unexported, `internal/relay/tab.go`)

Pure. Returns `model` up to its first `#`, or `model` unchanged when it has
none. A `:` suffix is untouched.

### `tabKey` (changed, model branch only)

It becomes `strings.Trim(u.Provider+"/"+modelSansEffort(u.Model), "/")`.
The `"unknown"` guard stays as it is: it tests the raw `u.Provider` and
`u.Model`.

### `statsUsageKey`, `refKey` (`internal/relay/stats.go`, refactor only)

Replace each inline `#` cut with `modelSansEffort`. The output must be
identical. The existing `TestStatsUsageKey`, `TestRefKeyAndProviderOfToken`
and `TestBuildStatsOneKeyPerModel` pin that, and none of them may be edited.

## 5. High-Level Pseudocode

```
builderForRound(events, n, b):
  for e in events where e.Round == n:
     if e.Kind == Pick and isRolePick(e.Note): continue      # new
     if e.Kind in {Pick, Switch}: remember e
  tok := parse(remembered) or b.BuilderCandidate             # unchanged

tabKey(e, "model"):
  if provider == "" and model == "": "unknown"               # unchanged
  trim(provider + "/" + modelSansEffort(model), "/")
```

## 6. Error Handling Strategy

No new errors. An unrecognised pick note is not a role pick, so it keeps
today's behaviour. A round whose only pick was a consult's falls back to
`b.BuilderCandidate`, exactly as a round with no pick does today.

## 7. Ordered Implementation Steps

After every step, run `make check` and `gofmt -l $(git ls-files '*.go')`.
The second must print nothing. All tests are in-memory, in
`internal/ingest` and `internal/relay`. Add no `cmd/relay` test: CI runners
have no harness binary and no network. No test opens the real state root.

1. **Consult picks in ingest.** Add `isRolePick`, and make `builderForRound`
   skip role picks.
   *Verify:*
   - A `TestIsRolePick` table:
     - `"picked claude/anthropic/sonnet for reviewer: order #1"` gives true.
     - `"picked a/b/c for verify: sole candidate"` gives true.
     - `"picked a/b/c for builder: order #1"` gives false.
     - `"picked a/b/c on host1: spawn"` gives false.
     - `"picked a/b/c"` gives false.
     - `"picked a/b/c for : x"` gives false.
     - `"switched builder (exited (code 1) without a report): picked a/b/c for builder: order #5"`
       gives false, because it does not start with `"picked "`.
   - A `builderForRound` test. Round 1 has
     `{Kind: Pick, Note: "picked opencode/cline-pass/cline-pass/glm-5.3-flash#high for builder: order #1"}`
     and then `{Kind: Pick, Note: "picked claude/anthropic/sonnet for reviewer: order #1"}`.
     The result is the opencode token, with the ref's harness `opencode`.
   - A test where round 1's only pick is the reviewer's: the result is
     `b.BuilderCandidate`.
   - Every existing `outcome_test.go` test passes unedited.

   *Mutation (required):* remove the `isRolePick` skip from
   `builderForRound`, and confirm the builder-then-reviewer test fails.
   Restore it.

2. **One key per model.** Add `modelSansEffort`, switch `tabKey`'s model
   branch to it, and refactor `statsUsageKey` and `refKey` onto it.
   *Verify:*
   - A `TabRows` test with `by = "model"` and two report entries of the same
     provider, one with model `cline-pass/deepseek-v4.1-flash#high` and one
     with `cline-pass/deepseek-v4.1-flash`, each carrying usage. The result
     is exactly one row, `cline-pass/cline-pass/deepseek-v4.1-flash`, with
     both entries summed.
   - A `modelSansEffort` table:
     - `"m#high"` gives `"m"`.
     - `"gpt-5.6-terra:high"` is unchanged.
     - `"a/b#x#y"` gives `"a/b"`.
     - `""` gives `""`.
   - Every existing `tab_test.go` and `stats_test.go` test passes unedited.

   *Mutation (required):* make `tabKey`'s model branch use `u.Model` again,
   and confirm the one-row test fails. Restore it.

## Report

- `git diff --stat main...HEAD`. It must be exactly the five files in §2.
- Both required mutation checks: what you broke, and which named test
  failed.
- What you found for each halt condition.
- **Read-only finding, no change:** when `relay db backfill --archive-only`
  meets an archive it has already ingested (an unchanged cursor), does it
  re-derive that binding's round rows (the `-- rounds` block in
  `internal/ingest/ingest.go` ~388) or skip them? Cite the lines. This
  decides whether rows written wrongly before the fix can be corrected
  without deleting `relay.db`.
