# Plan r2: `relay stats`, two attribution fixes (#322 stage 1)

This is round 2 on branch `relay/stats`. Round 1
(`00df6ef feat(stats): relay stats, local usage analytics (#322 stage 1)`)
is correct as far as it goes. The planner ran it against a copy of a real
state root (300 rounds, 176 archives), and it read two things wrong. Both
are attribution bugs in `internal/relay/stats.go`. Nothing else changes.

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. Halt and
report if any of the following is false.

- **`remotePickEntry` (`internal/relay/remote.go` ~372–382) writes a
  `KindPick` with `Note: fmt.Sprintf("picked %s on %s: %s", token, server, how)`,**
  where `how` is `"server's pick"` or `"explicit"`. It is the only note shape
  of the form `"picked <tok> on "`. A consult's pick is always
  `"picked <tok> for <role>: …"`.
- **`statsUsageKey` (`internal/relay/stats.go` ~360) is the only function
  that builds a key from a `usage.Usage`**, and its callers are the builder
  key rule 1 (~180) and `ConsultModels` (~230).
- **`refKey` cuts the model at its first `#`**, and `statsUsageKey` does not.

## 1. System Overview

These are the two defects from the real-state run:

1. **Remote picks are not builder tokens.** 16 archived picks read
   `picked opencode/… on contabo: server's pick`. `builderTokenFromNote` only
   accepts `" for builder:"` after the token, so a remote binding keeps an
   empty `cur`. Its rounds with no usage count as `unknown`, even though the
   pick names the builder. A remote binding has only a builder, so this
   shape is a builder pick.
2. **One model can get two keys.** Rule 1 uses the report's usage verbatim,
   and in real logs `Usage.Model` can carry the effort suffix
   (`cline-pass/deepseek-v4.1-flash#high`). Rules 2–3 go through `refKey`,
   which strips it. The same builder then splits into two `builders` rows,
   one with `#high` and one without, depending on whether a round recorded
   usage.

**Out of scope:** everything else in the file; `RenderStats`; the README;
`cmd/relay`; `internal/ingest`; any `remote.go` change.

## 2. File Structure

```
internal/relay/stats.go       # builderTokenFromNote, statsUsageKey
internal/relay/stats_test.go  # new rows
```

No other file changes.

## 3. Data Structures & Type Definitions

None change.

## 4. Interface Definitions & Component Contracts

### `builderTokenFromNote(note string) (string, bool)` (widened)

After the existing `" for builder:"` acceptance, also accept a token whose
following text starts with `" on "` **and** has a `": "` somewhere after it.
That is the remote pick shape, and it returns the token.

Everything else is unchanged:
- `" for reviewer:"` and every other role still return `"", false`.
- The `"switched on "` branch still comes first.
- A relaunch still gives its token.

Update the doc comment to name the remote pick shape.

### `statsUsageKey(u *usage.Usage) string` (normalised)

Cut `u.Model` at its first `#` before joining, the same as `refKey`. Empty
parts are still dropped. A model with a `:effort` suffix is left alone, as
`refKey` and ingest leave it. The doc comment says it matches `refKey`, so
both paths agree on one builder key.

## 5. High-Level Pseudocode

```
builderTokenFromNote(note):
  "switched on " prefix      -> text after last " -> "         (unchanged)
  find "picked "; tok = text up to next space
  rest after tok starts " for builder:"          -> tok, ok     (unchanged)
  rest after tok starts " on " and contains ": " -> tok, ok     (new: remote pick)
  otherwise                                       -> "", false

statsUsageKey(u): join non-empty [Harness, Provider, Model cut at first '#']
```

## 6. Error Handling Strategy

No new errors. A note that matches neither shape still returns not ok, and
the round falls back to `"unknown"` exactly as before.

## 7. Ordered Implementation Steps

After every step, run `make check` and `gofmt -l $(git ls-files '*.go')`.
The second must print nothing. Tests go in `internal/relay/stats_test.go`,
in memory. Add no `cmd/relay` test, since CI runners have no harness binary
and no network.

1. **Remote pick.** Widen `builderTokenFromNote`.
   *Verify:* new `TestBuilderTokenFromNote` rows:
   - `"picked opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high on contabo: server's pick"`
     gives that token, ok.
   - `"picked claude/anthropic/sonnet on contabo: explicit"` gives that
     token, ok.
   - `"picked claude/anthropic/sonnet on"` (no `": "`) gives not ok.
   - The existing reviewer and verify pick rows still give not ok.

   Also a `BuildStats` row: a segment whose only builder evidence is a
   remote pick, with one round that has a report without usage, gives a
   `builders` key of `opencode/cline-pass/cline-pass/deepseek-v4.1-flash`,
   not `unknown`.

   *Mutation (required):* remove the new `" on "` branch and confirm the
   `BuildStats` remote row fails. Restore it.

2. **One key per model.** Normalise `statsUsageKey`.
   *Verify:*
   - A `BuildStats` row with two rounds in one segment, both after the same
     pick of `opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high`.
     Round 1's report carries usage `{Harness: "opencode", Provider:
     "cline-pass", Model: "cline-pass/deepseek-v4.1-flash#high"}`, and round
     2's report has no usage. Result: `Builders` is exactly one entry,
     `opencode/cline-pass/cline-pass/deepseek-v4.1-flash` with count 2.
   - A `statsUsageKey` table:
     - a `#high` model is cut
     - a `:high` model is kept
     - an empty provider is dropped
   - `ConsultModels` for a findings entry whose model has `#high` gives the
     cut key.

   *Mutation (required):* remove the `#` cut and confirm the two-round row
   fails. Restore it.

## Report

- `git diff --stat` for this round only (`git diff --stat HEAD~1` after you
  commit). It must be the two files in §2.
- Both required mutation checks: what you broke, and which named test
  failed.
- What you found for each halt condition.
