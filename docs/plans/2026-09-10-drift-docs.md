# Plan: document what the diff trail actually covers

Spec: `docs/specs/2026-09-10-between-rounds-drift-design.md`
Issue: #19
Covers: spec step **8**. Nothing else.

Read sections 1, 8.1, 8.2, 8.3 and 8.4 of the spec before starting.

## What this is

Issue #19's first proposed option was "document the limitation and stop". We did
not stop -- relay now captures the between-rounds window as a patch -- but the
documentation half was always the part that mattered most, because the original
defect was that **`relay diff` reads as a complete history of the tree and is
not one, and nothing said so.**

This is a docs-only round. Write no Go.

## Files you may touch

```
docs/design.md
README.md
```

Touching any other file means the plan is wrong. Halt and report.

## Context you need

The feature works like this, and all of it is already built:

- When a round closes, relay keeps the tree it snapshotted for that round's
  diff, in `Binding.RoundClosedTree`.
- At the next `relay send`, it compares that against the fresh baseline. If they
  differ it writes `NNN-drift.patch`, logs a `drift` entry, and prints a line.
- The patch is keyed to the round **about to open**, not the one that closed.
- `relay diff --drift` reads it back.

## Step 1 -- `docs/design.md`

Add a passage stating plainly what the audit trail covers. It must say:

1. A round's diff covers exactly **send -> report**. That was always true; it
   was never written down, and a reader reasonably assumed otherwise.
2. The window between a report and the next send is now captured separately, as
   drift, keyed to the round that opens.
3. **A commit is not drift.** relay snapshots working-tree *content* (`add -A`
   into a temporary index), so a planner committing, amending, or rebasing the
   builder's work changes nothing relay can see. A merge that brings in new
   content, a checkout, or a builder that kept editing after it reported all do
   register. This narrowness is a property to rely on, and a reader who does not
   know it will misread a quiet send.
4. **relay reports drift; it does not attribute it.** Drift never changes a
   binding's state, never notifies, and never fires a hook. relay cannot observe
   *who* wrote -- `herdr agent list` can say which agents were in the tree
   (that is the foreign-agent feature), and joining the two is the planner's
   judgement, not relay's.
5. One honest gap: a round that produced no baseline also records no drift
   origin, so the next send is silent. It self-heals after one round.

Match the surrounding voice. `docs/design.md` states rules and the reasons
behind them; it does not tutorialise.

## Step 2 -- `README.md`

Document `relay diff --drift` where `relay diff` is already covered. Cover:

- what it prints, and that it composes with `--stat` and `--round`
- that its default round is the **currently open** round, while plain
  `relay diff` defaults to the newest **completed** one -- because drift is
  recorded when a round opens and a round diff when one closes. Both name the
  newest thing of their kind; they are different numbers, and a reader who
  assumes they match will be confused exactly once.

Keep it short. The README documents commands; the reasoning belongs in
`docs/design.md`.

## Definition of done

- `make check` is clean. It covers the whole tree, so run it even for a
  docs-only change.
- A reader of `docs/design.md` can answer, without opening any code, which of
  these produce a drift line: builder kept working after reporting; planner
  committed the work; planner merged main in. (Answers: yes, no, yes.)
- `git diff --stat` shows only the two files above.

## Halt and report rather than improvise

You are documenting behaviour built in an earlier round. If what you read in
`internal/relay/drift.go`, `capture.go`, `reconcile.go` or `send.go`
**contradicts** anything in the Context section above, stop and report the
contradiction rather than documenting either version. A doc that disagrees with
the code is worse than no doc, and the mismatch is a design error worth
surfacing.

Do not document `relay diff --drift` beyond the flags described here, and do not
invent examples of output you have not verified against the code.
