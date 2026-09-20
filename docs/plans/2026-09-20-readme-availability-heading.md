# README: rename the `#### History` heading to `#### Availability history`

## 1. System overview

`README.md` has a section headed `#### History` (line 922) that documents
`~/.local/state/relay/history.json`, which holds provider availability events
for `relay policy` (#61). relay is about to gain a real binding history (#172),
so the heading must say what the section is about. This is a one-line
documentation change; no code moves.

## 2. File structure

```
README.md    -- the only file touched: one heading line changes
```

## 3. Data structures

None.

## 4. Interfaces

None.

## 5. High-level pseudocode

```
open README.md
find the exact line `#### History` (there is exactly one; it precedes the
  paragraph starting "Every gate relay records")
replace that line with `#### Availability history`
change nothing else
```

## 6. Error handling

If `#### History` occurs zero times or more than once, or the paragraph after
it does not start with "Every gate relay records", STOP and report -- do not
guess which heading was meant.

## 7. Ordered implementation steps

1. Change line 922 of `README.md` from `#### History` to
   `#### Availability history`. Deliverable: that single-line diff.
   Verify: `grep -c '^#### Availability history$' README.md` prints `1` and
   `grep -c '^#### History$' README.md` prints `0`.
2. Run `make check`. Verify: it passes (it is docs-only, so it must).
3. Commit with the message
   `docs(readme): head the availability section "Availability history" (#172)`.
   Verify: `git diff --stat HEAD~1` shows `README.md | 2 +-` and nothing else.

## Scope

Only `README.md`, only that line. If any step is impossible as written or
contradicts what is in the tree, halt and report rather than improvise.
