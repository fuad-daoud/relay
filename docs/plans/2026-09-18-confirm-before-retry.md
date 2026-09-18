# Plan: confirm a stalled prompt from the screen before re-sending; second opinion on `unknown` (#126)

Issue: https://github.com/fuad-daoud/relay/issues/126. This plan is the spec;
the issue body is background. Where they differ, this plan wins.

**Halt rule.** If a step is impossible as written, contradicts the code you
find, or a named test cannot be made to fail by the named mutation, stop,
write what you found to the report path from your round prompt, create the
done marker, and end. Do not improvise around it.

## 1. System overview

`promptWithRetry` (`internal/relay/send.go`) is the one helper every payload
relay types into a pane goes through. When herdr reports the prompt stalled
(`herdr.ErrPromptStalled`, which also absorbs `ErrWaitTimeout`), it re-types
the whole payload once. herdr's own guidance is that a stall does not prove
the prompt was lost, and the e2e work showed `agent list` lags the screen by
~600 ms -- so the retry can land on top of a prompt that already landed. For
the plan that is two builders' worth of edits in one worktree; for a report it
is two planner turns; for a nudge it is two nudges.

This change makes the retry conditional: on the first stall, read the pane's
visible screen and look for a caller-chosen fingerprint of what was typed. If
it is there, the prompt landed and herdr was slow -- record the delivery as
*late* and do not re-send. If it is not there, or the screen cannot be read,
retry exactly as today.

Second, smaller: `relay send` currently types the plan into a builder whose
herdr status is `unknown` as if it were idle. herdr's own blocked gate only
fires when herdr is confident. For `unknown` only, `send` reads the visible
screen and matches a per-harness list of dialog patterns; a match is refused
as `ErrBuilderBlocked` without typing anything, exactly as herdr's gate would
have. Every other status is unchanged. This guard lives in `send` only; the
other five `promptWithRetry` callers do not get it (deliver already refuses
unless the planner is idle/done, the nudge fires only on an idle builder, and
switch/ask/consult prompt into panes relay just spawned).

Nothing here changes what relay does on a confirmed stall, adds a second
retry, or touches deliver's planner-idle rule.

## 2. File structure

```
internal/
  store/
    log.go                    LogEntry gains Late bool
  harness/
    harness.go                Harness gains DialogPatterns; defaultDialogPatterns list
    harness_test.go           TestDialogPatternsSetOnEveryKind
  candidate/
    candidate.go              Candidate gains DialogPatterns (json dialog_patterns); Load validates
    candidate_test.go         Load rejects a non-compiling dialog pattern
  relay/
    dialog.go                 NEW: dialogPatterns, dialogTail, dialogGuard, dialogScanLines
    dialog_test.go            NEW: pure tests for dialogTail and dialogPatterns
    send.go                   promptWithRetry(fingerprint), ErrPromptLate, the unknown guard, Late on the plan entry
    send_test.go              late / retry / read-error / unknown-guard tests
    reconcile.go              nudge passes nudgeFingerprint; Late on the nudge entry
    reconcile_test.go (or the file the existing nudge tests live in)
    deliver.go                report passes pending.Path; late is slog only
    deliver_test.go           late delivery confirms with one Prompt
    switch.go                 passes the plan path; late is slog only
    ask.go                    passes consult.FindingsPath; late counts as running
    consult.go                passes consultNudgeFingerprint; late counts as nudged
    logline.go                LogLine prints " late" when set
    logline_test.go           late suffix test
README.md                     dialog_patterns under candidates; late in the log section
```

No new packages. No CLI changes. No new verbs or flags (the #114 freeze holds).

## 3. Data structures & type definitions

### 3.1 `store.LogEntry` (existing, `internal/store/log.go`)

Add one field after `Note`:

| field | type | json | meaning |
|---|---|---|---|
| `Late` | `bool` | `late,omitempty` | The payload this entry records was typed once, herdr reported a stall, and the screen read showed it had landed. Never set when the retry fired. |

`Note` is **not** given a `late` token. The nudge entry's `Note` must remain
`nudgeNote` (`reconcile.go` `HasEntry`/`nudgeTime` and `wait.go` key on it) and
a report entry's `Note` carries #117's `""`/`unmarked`/`scraped`/`noreport`.
A bool beside it collides with nothing.

### 3.2 `harness.Harness` (existing, `internal/harness/harness.go`)

Add after `LimitPatterns`:

| field | type | meaning |
|---|---|---|
| `DialogPatterns` | `[]string` | Default regexes for the text this harness shows when it is waiting on a yes/no or option dialog. Every default must compile. Case-insensitivity is written into the pattern with `(?i)`. |

Every known kind gets the same shared default slice, declared once as a
package-level `var defaultDialogPatterns = []string{...}` and assigned in each
`knownHarnesses` entry (no per-harness differences yet). The defaults, seeded
from outsourcerer's classifier as the issue asks:

```
(?i)\[y/n\]
(?i)\(y/n\)
(?i)do you want to (proceed|continue|allow)
❯\s*1\.\s*Yes
(?i)press enter to confirm
(?i)esc to cancel
```

### 3.3 `candidate.Candidate` (existing, `internal/candidate/candidate.go`)

Add after `LimitPatterns`:

| field | type | json | meaning |
|---|---|---|---|
| `DialogPatterns` | `[]string` | `dialog_patterns,omitempty` | Extra regexes appended to the harness defaults. Extend-only, as `limit_patterns` is. |

`Load` validates each entry compiles, with the same error shape as
`limit_patterns[%d]`: `candidates %s: candidate %d: dialog_patterns[%d]: %w`.

### 3.4 Constants and sentinels (`internal/relay`)

| name | file | value | purpose |
|---|---|---|---|
| `ErrPromptLate` | `send.go` | `errors.New("prompt landed late")` | Returned by `promptWithRetry` in place of nil when the first stall was contradicted by the screen. Every caller treats it as success and records/logs late. |
| `lateScanLines` | `send.go` | `40` | Lines read from the `visible` source when confirming a fingerprint. |
| `dialogScanLines` | `dialog.go` | `30` | Lines read from the `visible` source for the unknown guard (the issue's "last 30 lines"). |
| `nudgeFingerprint` | `reconcile.go` | `"You went idle without finishing."` | First line of `nudgePrompt`. Declare the const and build `nudgePrompt` so the two cannot drift (e.g. `nudgePrompt = nudgeFingerprint + "\n" + ...`). |
| `consultNudgeFingerprint` | `consult.go` | `"You went idle without writing your findings."` | Same relationship to `consultNudgePrompt`. |

## 4. Interface definitions & component contracts

### 4.1 `promptWithRetry` (modified, `send.go`)

```
promptWithRetry(ctx, rt Runtime, target, text, fingerprint string) error
```

Single responsibility: type one payload into one pane at most twice.

- Precondition: `fingerprint` is either `""` or a substring of `text`.
- Postcondition on return `nil`: the payload was typed once and observed,
  or typed twice after an unconfirmed stall (today's behaviour).
- Postcondition on return `ErrPromptLate`: the payload was typed exactly once;
  the first `Prompt` reported a stall; `ReadAgentSource(target, "visible",
  lateScanLines)` succeeded and contained `fingerprint`. No retry fired.
- Errors: as today (`stalled twice`, `failed on retry`, and any non-stall
  error from the first `Prompt`, including `herdr.ErrAgentBlocked`).
- An empty `fingerprint` disables the screen read entirely: first stall goes
  straight to the retry, identical to the current code.
- A failing screen read never removes the retry -- it is logged at
  `slog.Warn` ("late check: screen unreadable", target, err) and the retry
  proceeds.

### 4.2 Dialog helpers (new, `dialog.go`)

```
dialogPatterns(rt Runtime, kind, token string) []*regexp.Regexp
```
Responsibility: the compiled pattern list for one builder. Harness defaults
come from `harness.Lookup(kind)`; if `token` parses (`candidate.ParseRef`)
and resolves in `rt.Candidates`, that candidate's `DialogPatterns` are
appended. Mirrors `limitPatterns` in `limit.go`, with two differences: it
takes the herdr agent kind so an adopted builder with no candidate token
still gets the harness defaults, and a nil `rt.Candidates` only drops the
extras, not the defaults. Non-compiling entries are skipped, never panicked on.

```
dialogTail(text string, patterns []*regexp.Regexp) bool
```
Responsibility: pure classifier. True when any pattern matches anywhere in
`text`. Empty text or empty patterns → false.

```
dialogGuard(ctx, rt Runtime, paneID string, patterns []*regexp.Regexp) bool
```
Responsibility: the one herdr read the guard makes. Reads
`ReadAgentSource(paneID, "visible", dialogScanLines)`. On a read error:
`slog.Warn("dialog guard: builder screen unreadable", pane, err)` and return
false -- the guard is a second opinion, a broken read must not strand a send.
Otherwise returns `dialogTail(text, patterns)`.

### 4.3 `LogLine` (modified, `logline.go`)

When `e.Late` is true, the first line gains a trailing ` late` after the note
column (so a plan entry reads `... /p/004-plan.md  late`, and a nudge entry
`... /p/004-report.md nudge late`). Format with `%s %s` as today, then append
`" late"` when set; do not add a fixed column, so existing expected strings
stay byte-identical when `Late` is false.

### 4.4 Call-site contracts

| caller | fingerprint | on `ErrPromptLate` |
|---|---|---|
| `send.go` plan prompt | `planPath` | proceed as success; plan `LogEntry.Late = true` |
| `switch.go` hand-off after a builder switch | `rt.Store.PlanPath(b.Name, b.Round)` | proceed as success; `slog.Info("plan handed to switched builder late", binding, round)` |
| `reconcile.go` `nudgeBuilder` | `nudgeFingerprint` | proceed as success; nudge `LogEntry.Late = true`, `Note` stays `nudgeNote` |
| `deliver.go` report to planner | `pending.Path` | proceed as success (`ConfirmIndex` as today); `slog.Info("report delivered late", binding, round)`; `Delivery` struct unchanged |
| `ask.go` consult prompt | `consult.FindingsPath` | `consult.State = store.ConsultRunning`, as on nil |
| `consult.go` consult nudge | `consultNudgeFingerprint` | `NudgedAt = now`, as on nil |

Every site: `errors.Is(err, ErrPromptLate)` is checked **before** any
`errors.Is(err, herdr.ErrAgentBlocked)` or generic failure branch.

### 4.5 The unknown guard in `send` (modified, `send.go`)

Inside the locked section, in the pane (non-headless) branch, immediately
before `promptWithRetry`:

```
IF builder.Status == herdr.StatusUnknown THEN
    patterns := dialogPatterns(rt, builder.Kind, b.BuilderCandidate)
    IF dialogGuard(ctx, rt, builder.PaneID, patterns) THEN
        RETURN fmt.Errorf("binding %q: %w", name, ErrBuilderBlocked)
```

`builder` is the `herdr.Agent` `FindAgent` located earlier in `Send`; it is
in scope. Returning the error from inside the transaction aborts the round
advance the same way today's `ErrAgentBlocked` → `ErrBuilderBlocked` mapping
does, so the staged plan file and un-advanced round behave identically.

## 5. High-level pseudocode

### 5.1 `promptWithRetry`

```
err := Prompt(target, text)
IF err is not ErrPromptStalled: RETURN err

IF fingerprint != "":
    screen, rerr := ReadAgentSource(target, "visible", lateScanLines)
    IF rerr != nil:
        Warn("late check: screen unreadable")
    ELSE IF strings.Contains(screen, fingerprint):
        RETURN ErrPromptLate

retryErr := Prompt(target, text)          -- unchanged from today
IF retryErr == nil: RETURN nil
IF retryErr is ErrPromptStalled: RETURN "stalled twice"
RETURN "failed on retry"
```

### 5.2 `Send`, pane branch (excerpt)

```
IF builder.Status == unknown AND dialogGuard(...): RETURN ErrBuilderBlocked
late := false
err := promptWithRetry(ctx, rt, builder.PaneID, text, planPath)
IF errors.Is(err, ErrPromptLate): late = true; err = nil
IF err != nil:
    IF errors.Is(err, herdr.ErrAgentBlocked): RETURN ErrBuilderBlocked   -- as today
    RETURN "prompt builder: err"
append plan LogEntry{..., Confirmed: true, Late: late}
```

### 5.3 `nudgeBuilder`

```
late := false
err := promptWithRetry(ctx, rt, Target(b.Builder), fmt.Sprintf(nudgePrompt, ...), nudgeFingerprint)
IF errors.Is(err, ErrPromptLate): late = true; err = nil
IF err != nil: RETURN "nudge builder: err"
append nudge LogEntry{..., Note: nudgeNote, Confirmed: true, Late: late}
```

Deliver, switch, ask and consult follow the same three-line shape with the
"late" outcome from the table in 4.4.

## 6. Error handling strategy

- `ErrPromptLate` is a success-shaped sentinel, never propagated out of a
  caller: each site converts it before its error branches. A caller that
  forgets converts a delivered prompt into a halted round, which is why every
  site in 4.4 gets a test that pins the late outcome.
- Screen-read failures in both new reads are recoverable and logged at Warn:
  the late check falls through to the retry; the dialog guard falls through
  to sending. Neither may return an error.
- `ErrBuilderBlocked` from the guard is the existing error with the existing
  message; `cmd/relay` already prints "answer it with relay answer".
- Pattern compilation failures are refused at `candidate.Load` (config error,
  non-recoverable, same as `limit_patterns`) and skipped defensively in
  `dialogPatterns` (unreachable after Load).
- No new hook events, no state changes, no ledger writes.

## 7. Ordered implementation steps

Run `make check` after every step; it must be green before the next step
begins. Where a mutation is named, apply it, confirm the named test fails,
then revert the mutation. Tests in `internal/relay` use `fakeHerdr`
(`fake_test.go`): `stalls` scripts stall count, `readOut`/`readErr` script
`ReadAgentSource`, `reads` records every read with its source, `agents`
carries each agent's `Status`, and the fake records each `Prompt` call --
use whatever field it already has for that; do not add a second recorder.
No test in `cmd/relay` is added; nothing here reaches herdr from a test.

### Step 1 -- `LogEntry.Late` and `LogLine`
Files: `internal/store/log.go`, `internal/relay/logline.go`, `logline_test.go`.
- Add the field (3.1). Add the ` late` suffix (4.3).
- Test `TestLogLineLateSuffix`: an entry with `Late: true` and `Note: "nudge"`
  ends with `nudge late`; the same entry with `Late: false` is byte-identical
  to today's format. `TestLogLineWithoutUsageIsTodaysFormat` must still pass unchanged.
- Verify: `make check` green.

### Step 2 -- harness and candidate `DialogPatterns`
Files: `internal/harness/harness.go`, `harness_test.go`, `internal/candidate/candidate.go`, `candidate_test.go`.
- Add `defaultDialogPatterns` (3.2) and assign it to every entry in `knownHarnesses`.
- Add `TestDialogPatternsSetOnEveryKind`, shaped like `TestLimitPatternsSetOnEveryKind`: non-empty and every pattern compiles, for every kind in `All()`.
- Add `Candidate.DialogPatterns` (3.3) and the `Load` validation.
- Test in `candidate_test.go`: a candidates file with `"dialog_patterns": ["("]` is refused with an error mentioning `dialog_patterns[0]`; a valid one loads and `Lookup` returns the slice.
- Verify: `make check` green; mutation: delete the `Load` validation loop → the refusal test fails.

### Step 3 -- `dialog.go`
Files: `internal/relay/dialog.go`, `dialog_test.go`.
- Implement 4.2 (`dialogPatterns`, `dialogTail`, `dialogGuard`, `dialogScanLines`).
- `TestDialogTailMatchesEveryDefault`: for each string in `harness.Lookup("agy").DialogPatterns`, a one-line fixture that should match does (`[y/N]`, `(Y/n)`, `Do you want to proceed?`, `❯ 1. Yes`, `Press Enter to confirm`, `Esc to cancel`).
- `TestDialogTailIgnoresOrdinaryOutput`: fixtures `compiling main.go`, `error: expected ] at line 3`, `ok  github.com/x 0.4s`, and empty text → false.
- `TestDialogPatternsAdoptedBuilderGetsDefaults`: `token == ""` and nil `rt.Candidates` still yields the harness defaults for kind `agy`; a candidate with `dialog_patterns: ["custom"]` yields defaults + custom.
- `TestDialogGuardReadErrorIsNotADialog`: `readErr` set → false, one read with source `visible` and `dialogScanLines` lines.
- Verify: `make check` green.

### Step 4 -- `promptWithRetry` fingerprint and `ErrPromptLate`
Files: `internal/relay/send.go`, `send_test.go`.
- Change the signature (4.1), add `ErrPromptLate` and `lateScanLines`. Update
  **all six** call sites to compile by passing the fingerprint from the 4.4
  table and converting `ErrPromptLate` per that table (the behaviour for
  deliver/switch/ask/consult is finished here; their tests come in step 6).
  Update the plan `LogEntry` in `Send` to carry `Late`.
- `TestSendStallThenFingerprintOnScreenIsLate`: `stalls: 1`, `readOut` containing the plan path → exactly one `Prompt` call, one read with source `visible`, `Send` returns nil, the plan entry has `Late == true`. Mutation: remove the `strings.Contains` check (always fall through) → fails.
- `TestSendStallWithoutFingerprintRetries`: `stalls: 1`, `readOut: "some other screen"` → two `Prompt` calls, `Late == false`. (The existing stall test at `send_test.go:128` may already be this; extend it to assert `Late == false` rather than duplicate it.)
- `TestSendStallReadErrorStillRetries`: `stalls: 1`, `readErr` set → two `Prompt` calls, nil error. Mutation: return `ErrPromptLate` on read error → fails.
- Existing `stalls: 2` and `promptErr: ErrAgentBlocked` tests must pass unchanged.
- Verify: `make check` green.

### Step 5 -- the unknown guard in `Send`
Files: `internal/relay/send.go`, `send_test.go`.
- Add the guard (4.5) before `promptWithRetry` in the pane branch.
- `TestSendUnknownBuilderAtDialogIsBlocked`: builder agent `Status: herdr.StatusUnknown`, `readOut: "Do you want to proceed?\n❯ 1. Yes"` → `errors.Is(err, ErrBuilderBlocked)`, zero `Prompt` calls, exactly one read with source `visible` and `dialogScanLines` lines, round not advanced. Mutation: drop the `Status == unknown` condition's guard call → fails.
- `TestSendUnknownBuilderWithoutDialogSends`: `Status: unknown`, `readOut: "$ "` → one `Prompt`, nil error.
- `TestSendIdleBuilderNeverScansForDialog`: `Status: idle`, `readOut` containing a dialog line, no stall → one `Prompt`, **zero** reads. Mutation: remove the status condition (always guard) → fails.
- Verify: `make check` green.

### Step 6 -- late tests for nudge and deliver
Files: `internal/relay/reconcile.go` (the `nudgeFingerprint` const and `nudgePrompt` composition from 3.4), `consult.go` (`consultNudgeFingerprint` likewise), the existing nudge test file, `deliver_test.go`.
- `TestNudgeStallLateRecordsLate`: drive the existing idle-builder nudge fixture with `stalls: 1` and `readOut` containing `nudgeFingerprint` → one `Prompt`, nudge entry with `Note == nudgeNote` **and** `Late == true`, and `HasEntry`/`nudgeTime` still treat it as the nudge (assert the round is not considered to have a second plan send -- reuse whatever the existing nudge test asserts about `Note`).
- `TestDeliverStallLateConfirmsOnce`: the existing idle-planner delivery fixture with `stalls: 1` and `readOut` containing the pending entry's `Path` → `Delivered: true`, one `Prompt`, entry confirmed. Mutation: pass `""` as deliver's fingerprint → two `Prompt` calls, fails.
- Verify: `make check` green.

### Step 7 -- README
File: `README.md`.
- Under the candidates field list (beside `limit_patterns`, ~line 567): a `dialog_patterns` bullet in the same voice: extra regexes appended to the harness defaults, matched against the builder's visible screen only when herdr reports its status as `unknown`; a match refuses `send` as blocked; extend-only.
- In the section that describes `relay log` output / notes: one sentence that `late` on an entry means herdr reported the prompt stalled but the screen showed it had landed, so it was not re-sent.
- Verify: `make check` green (gofmt/tidy unaffected; this is the last step).

### Step 8 -- e2e
- Run `make e2e` (local-only; needs a `herdr` binary on PATH). It exercises the nudge path this change touched. If it cannot run on this machine, say so in the report rather than skipping silently; do not modify `e2e_test.go` to make it pass.

### Report
End with: which steps completed, each mutation you ran and the test it failed,
`make check` and `make e2e` results verbatim, `git diff --stat`, and anything
adjacent you deliberately did not do.
