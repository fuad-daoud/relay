# Status without herdr, round 2: `HideDone` must carry `HerdrError`

Standalone follow-up to the commit already on this branch
(`fix(status): rows render when herdr is unreachable ...`). If any step is
impossible as written or contradicts the code, **halt and report**.

## 1. System overview

Round 1 added `Report.HerdrError` (`internal/relay/status.go`) and a
`RenderStatus` header line `herdr unreachable: <err>; pane statuses unknown`.
But the default `relay status` (no binding name, no `--all`) routes its
report through `relay.HideDone` (`cmd/relay/main.go:1346`), and `HideDone`
builds a fresh `Report` copying only `Bindings`, `DoneHidden`, `Gated`
(`status.go:475`). `HerdrError` is dropped, so the header never prints in
the common case. This is the #61 bug class again (`Gated` was dropped the
same way). Fix `HideDone`; nothing else.

**Scope guard.** Do not edit `cmd/relay/main.go` or any file outside
`internal/relay/status.go` and `internal/relay/status_test.go`. Another
round owns `cmd/relay/main.go`.

## 2. File structure

```
internal/relay/status.go        HideDone copies HerdrError
internal/relay/status_test.go   one new test
```

## 3. Data structures

None new.

## 4. Contract change

`HideDone(r Report) Report`: postcondition gains `out.HerdrError == r.HerdrError`.
Everything else unchanged. Extend the existing comment on the `Gated`
copy to say `HerdrError` is likewise report-wide, not per binding.

## 5. Pseudocode

```
HideDone(r):
    out <- Report{Bindings: [], DoneHidden: 0, Gated: r.Gated, HerdrError: r.HerdrError}
    (rest unchanged)
```

## 6. Ordered steps

### Step 1 -- failing test

Add `TestHideDoneKeepsHerdrError` to `internal/relay/status_test.go`:
`HideDone(Report{HerdrError: "no herdr server", Bindings: [one row with State "done", one with State "active"]})`
returns `HerdrError == "no herdr server"`, `DoneHidden == 1`,
`len(Bindings) == 1`. Use `string(store.StateDone)` / `string(store.StateActive)`
for the states (check the exact constant names in `internal/store`).

**Verify:** `go test ./internal/relay/ -run TestHideDoneKeepsHerdrError` fails.

### Step 2 -- fix

The one-field addition in `HideDone` plus the comment extension.

**Verify:** the test passes; `go test ./internal/relay/` green.

### Step 3 -- mutation

Re-edit `HideDone` to drop the `HerdrError` copy; `TestHideDoneKeepsHerdrError`
must fail. Restore by re-editing (never `git checkout`).

### Step 4 -- full check and commit

`make check` green. `git diff --stat` (against the previous commit on this
branch) lists only `internal/relay/status.go` and
`internal/relay/status_test.go`; otherwise halt and report.

Commit message:

```
fix(status): HideDone carries HerdrError -- the default relay status view lost the herdr header

Same #61 class: HideDone rebuilt the Report and copied Gated but not the
new HerdrError, so relay status without --all never printed the
"herdr unreachable" line.
```

## Report

Per-step state, `make check` tail, `git diff --stat`, the mutation line.
