# A seconds-resolution formatter for a round's CPU time (#299)

This plan stands alone: everything you need is in this file and in the tree.
If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

You are a headless builder. Never run `make check` (the planner runs it);
run the gate in §5 exactly as written. Every command in the foreground; no
sub-agents for edits. Do not touch any file outside your worktree.

## 1. The defect

`internal/relay/logline.go:50-52` renders a closed round's CPU time with the
wall-clock age formatter:

```go
if e.Kind == store.KindReport && e.Rusage != nil {
    first += fmt.Sprintf(" cpu %s peak %s", usage.ShortDuration(e.Rusage.CPUMS), shortBytes(e.Rusage.PeakMemBytes))
}
```

`usage.ShortDuration` (`internal/usage/format.go:42-53`) returns `"<1m"` for
anything under a minute, so a real round reads:

```
report  .../002-report.md  outcome=done cpu <1m peak 341MB
```

where the recorded value was `cpu_ms: 8550`. Measured rounds on the server
burn 8.5 s and 19.7 s of CPU, so minute resolution collapses exactly the
comparison the number exists for (#216: what did this round cost the
machine). `peak 341MB` is fine and stays.

`usage.ShortDuration` itself is **correct for its other callers**
(`usage.Line`, `usage.Parts`, which render a round's wall-clock duration at
`format.go:89` and `format.go:116`) and must not change.

## 2. The fix

Add an unexported `shortCPU(ms int64) string` to `internal/relay/logline.go`,
beside `shortBytes` (logline.go:72-82), and use it in place of
`usage.ShortDuration` on line 51 **only**. Leave the `peak` half alone.

Rounding rule, in this order (the table in §3 is the contract; implement it
so the table passes):

1. `ms <= 0` -> `""` (same as today's empty render).
2. Compute tenths of a second, rounded half-up: `tenths := (ms + 50) / 100`.
3. `tenths < 600` -> `fmt.Sprintf("%d.%ds", tenths/10, tenths%10)` -- e.g.
   `8.5s`, `19.7s`, `0.5s`, `59.9s`.
4. otherwise seconds `s := tenths / 10`; `s < 3600` ->
   `fmt.Sprintf("%dm%02ds", s/60, s%60)` -- e.g. `1m00s`, `1m20s`, `59m59s`.
5. otherwise `fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)` -- e.g.
   `1h00m`, `2h05m`.

Step 2 before the branch in step 3 is deliberate: it stops `59_999` ms
rendering as `60.0s`. Do not reach for `time.Duration.String()` -- it gives
`8.55s`/`1m20.5s`, which is not this table.

## 3. Test (the contract)

`internal/relay/logline_test.go` (create it if absent, otherwise add
beside the existing tests), `TestShortCPU`, a table with exactly these rows:

| ms | want |
|---|---|
| 0 | `""` |
| -1 | `""` |
| 50 | `"0.1s"` |
| 499 | `"0.5s"` |
| 8550 | `"8.6s"` |
| 19723 | `"19.7s"` |
| 59_949 | `"59.9s"` |
| 59_999 | `"1m00s"` |
| 60_000 | `"1m00s"` |
| 80_000 | `"1m20s"` |
| 3_599_000 | `"59m59s"` |
| 3_600_000 | `"1h00m"` |
| 7_500_000 | `"2h05m"` |

(`59_949` -> tenths 600 would be wrong; check your rounding: 59_949+50 =
59_999, /100 = 599 tenths -> `59.9s`. `59_999+50 = 60_049`, /100 = 600 ->
the minutes branch -> `1m00s`.)

Also add `TestLogLineRusageUsesSecondsResolution`: build a
`store.LogEntry{Kind: store.KindReport, Rusage: &store.Rusage{CPUMS: 8550,
PeakMemBytes: 341 << 20}}` (fill whatever other fields `LogLine` needs --
read the function first) and assert the rendered line contains
`cpu 8.6s peak 358MB`. Compute the `peak` half from `shortBytes`'s actual
rule (logline.go:72-82) rather than trusting this sentence: if `341 << 20`
does not render as `358MB`, use what `shortBytes` really returns and say so
in the report.

Mutation-check before reporting: change `shortCPU`'s step 3 to
`return usage.ShortDuration(ms)` and confirm `TestShortCPU` fails; restore
by re-editing (never `git checkout`).

## 4. Files

```
internal/relay/logline.go        shortCPU; line 51 uses it
internal/relay/logline_test.go   TestShortCPU, TestLogLineRusageUsesSecondsResolution
docs/plans/2026-09-22-cpu-formatter.md   copy of this plan (last step)
```

Do **not** touch `internal/usage/format.go`, `internal/ui/dash/render.go`'s
`shortDuration`, or `shortBytes`.

## 5. Gate

```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Commit the fix as `fix(logline): render a round's cpu at seconds resolution
(#299)`, the plan copy as `chore(plans): cpu formatter`.

## Report

The mutation check both ways, the gate tail, commit shas, the actual
`shortBytes(341 << 20)` value, and anything you did that this plan did not
say.
