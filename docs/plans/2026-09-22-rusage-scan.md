# Fix: the rusage trailer is emitted but never recorded (#216 follow-up)

This plan stands alone: everything you need is in this file and in the tree.
If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. The
worktree has **no `origin`**: never fetch, pull or rebase. Never run `make
check` here; run the gate in §5 exactly as written. Every command in the
foreground; no sub-agents for edits.

## 1. The defect (observed on the live server, 2026-09-22)

`internal/proc`'s `supervisorScript` (in `proc.go`) ends a round with two
`printf`s, each of which starts with `\n`:

```
printf '\nrelay-rusage:%s%s\n' ...
printf '\nrelay-exit:%s\n' "$rc"
```

so a real stream file ends with, byte for byte:

```
<builder output>\n
\n
relay-rusage:cpu_usec=19071588 mem_peak=403206144\n
\n
relay-exit:0\n
```

`(*Runner).Rusage` reads `lastLines(streamPath, 2)` and parses `lines[0]`.
After `lastLines` trims only the *trailing* newlines, the last two lines are
`["", "relay-exit:0"]`, so `lines[0]` is the **blank line** between the two
trailers and `ParseRusageTrailer` fails. `ExitCode` is unaffected because it
takes the last line.

Live evidence from binding `q4`, round 1: the stream's tail is exactly
`relay-rusage:cpu_usec=19071588 mem_peak=403206144` followed by a blank line
and `relay-exit:0`, while the round's report log entry has no `rusage` field.

The existing unit test passes because it writes a stream file with no blank
line between the trailers -- a shape the supervisor never produces.

## 2. The fix

Make `Rusage` **scan** the tail for the trailer instead of assuming a fixed
offset, so it is robust to the blank line and to any trailing builder output:

- `(*Runner).Rusage` reads `lastLines(streamPath, 6)` and walks the returned
  lines from the **last to the first**, returning `ParseRusageTrailer(line)`
  for the first line whose prefix is `RusageTrailer`. No match in those lines
  -> `(relay.ProcRusage{}, false)`, as today.
- Change nothing else: `ParseRusageTrailer`, `lastLines`, `lastLine`,
  `ExitCode` and `supervisorScript` all stay exactly as they are. Do **not**
  "fix" the script by dropping the leading `\n` -- the blank lines separate
  the trailers from builder output that may lack a final newline, and a
  scanning reader is correct for both shapes.

## 3. Tests (`internal/proc/scope_test.go` or `proc_test.go`, wherever the
existing `Rusage` test lives -- keep it beside that one)

1. **`TestRusageRealSupervisorLayout`** (the regression that would have caught
   this): build the stream content by the same `printf` semantics the script
   uses, i.e. the exact string
   `"builder said hi\n" + "\nrelay-rusage:cpu_usec=19071588 mem_peak=403206144\n" + "\nrelay-exit:0\n"`,
   write it to a temp file, and assert `Rusage` returns
   `{CPUMS: 19071, PeakMemBytes: 403206144}, true`. Also assert `ExitCode`
   still reports `0, true` from the same file.
2. Keep the existing test that pins the no-trailer case (`ok == false`).
3. **`TestRusageIgnoresLaterOutput`**: same content as (1) plus a line of
   builder output appended *after* the exit trailer; `Rusage` still finds it.
4. Mutation-check each new test yourself before reporting: revert `Rusage` to
   `lines[0]` of `lastLines(path, 2)` and confirm
   `TestRusageRealSupervisorLayout` fails; restore by re-editing the file,
   never with `git checkout` (that would drop the fix too).

## 4. Files

```
internal/proc/proc.go          (*Runner).Rusage: scan the tail; no other change
internal/proc/<the file that holds the current Rusage test>   the three tests above
docs/plans/2026-09-22-rusage-scan.md   copy of this plan (last step)
```

## 5. Gate

```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/proc/ ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Commit the fix as `fix(proc): scan the stream tail for the rusage trailer
(#216)`, the plan copy as `chore(plans): rusage scan`.

## Report

The mutation check's output (the named test failing, then passing again),
the gate tail, commit shas, and anything you did that this plan did not say.
