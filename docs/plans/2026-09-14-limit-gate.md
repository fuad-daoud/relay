# Limit gate: the daemon gates a provider on rate-limit text (#140)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-14-limit-gate-design.md`. Section numbers
below (§) refer to it.
**Issue:** #140. Closes it.
**Depends on:** nothing open.

**Goal:** When a round stops -- a headless process exits without the
marker, a pane builder goes quiescent without it, or either mode runs past
its round budget -- relay scans the builder's last output for the harness's
rate-limit text. On a match it records a `rate_limited` ledger entry
(`source: relay`, the matched line as note, a reset time parsed from the
line or a policy default) and performs the existing mid-round switch, and
that switch does not count toward `max_switches`. Patterns ship per harness
and can be extended per candidate. `relay unavailable` stays as the manual
override.

**Architecture:** `harness.Harness` gains `LimitPatterns`;
`candidate.Candidate` gains `LimitPatterns` (`limit_patterns`, compile-checked
in `Load`); `policy.Policy` gains `LimitGateDefaultMS`. A new
`internal/relay/limit.go` holds `limitPatterns`, `matchLimit`, `parseReset`,
`limitText` and `gateOnLimit`. `switchBuilder` gains a trailing `counted
bool`; every rate-limit switch passes `false`. `checkRoundTimeout` gains a
`tx` parameter and scans on the halting tick. Five decision points call
`gateOnLimit` (§5). No new verb, no prompt change.

**Tech stack:** Go 1.22. Verification is the `make check` constituent set
(runs `-race`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/limit-gate` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/limit-gate`, cut from `main`. |
| `~/.local/state/relay/limit-gate` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Resuming a round another builder started

The branch may already carry commits from an earlier builder on this same
round. Before Task 1, run `git log --oneline main..HEAD`. A task whose
commit message is already there is **done**: run its Step 4 command to
confirm it is green, tick it, and continue with the next task. Do not redo
it, do not amend it. An untracked or modified file from an unfinished task
is yours to finish or replace as that task's steps say.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` directly, in this order, and say so in your
report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` or `make e2e` yourself; the planner runs `make e2e`
after the round. **No test is added under `cmd/relay`** (CI runners have no
`herdr`; CLAUDE.md). Every new test is a pure function or a fake-backed
reconcile in `internal/relay`, `internal/harness`, `internal/candidate` or
`internal/policy`.

## Global constraints

- Scanning happens **only** inside `gateOnLimit`, and `gateOnLimit` is
  called **only** at the five points in §5. No decision point is added to a
  running builder's tick.
- `gateOnLimit` checks the report **before** switching (§4.4): a report on
  disk means "record the gate, do not switch".
- A rate-limit switch never advances `RoundSwitches` (§4.5); the `>= limit`
  check at the top of `switchBuilder` is untouched.
- The budget points scan only when `b.HaltNotifiedRound != b.Round` (§5).
- `matchLimit` returns the **last** matching line of the text (§4.2).
- `parseReset` results outside `(now, now+7d]` are unparsed (§4.3).
- Ledger entries use `Source: "relay"` and `Binding: b.Name` (§3.5); a
  ledger write failure is printed to stderr and dropped, never returned.
- Read failures at a pane point are `slog.Warn`ed and treated as no text.
- `internal/relay/e2e_test.go` is not edited.
- The builder prompt (`builderPrompt`, `send.go`) is not edited.
- One commit per task, on the worktree's branch.

---

### Task 1: `Harness.LimitPatterns` defaults

**Files:**
- Modify: `internal/harness/harness.go` (struct field after `SubAgents`; a value on each of the three `knownHarnesses` entries)
- Modify: `internal/harness/harness_test.go` (new test after `TestSubAgentsSetOnEveryKind`, line 274)

**Interfaces:**
- Produces: `Harness.LimitPatterns []string` -- default regexes for this kind's rate-limit text (§3.1). Doc comment records provenance per the §3.1 table: the agy first pattern is observed (2026-09-12, `history.json`), every other pattern is "unverified against a pane; replace with the observed line when one is seen".

- [ ] **Step 1: Write `TestLimitPatternsSetOnEveryKind`**, modelled on `TestSubAgentsSetOnEveryKind`: for every `All()` kind, `len(LimitPatterns) >= 1`, and every pattern compiles with `regexp.Compile`. Add a second assertion that the agy list matches the fixture line `Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.` via at least one compiled pattern's `MatchString`.

- [ ] **Step 2: Run to verify it fails** -- `go test -count=1 ./internal/harness -run TestLimitPatterns`: compile error, `LimitPatterns` undefined.

- [ ] **Step 3: Implement.** Add the field with a doc comment; set the three lists exactly as §3.1's table (the `(?i)` prefix on every pattern; the opencode alternations as written there). Do not touch `TestTableExactValues` unless it compares whole `Harness` values -- if it does, extend its expected values with the same lists rather than weakening the comparison.

- [ ] **Step 4: Run** `go test -count=1 ./internal/harness`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harness/harness.go internal/harness/harness_test.go
git commit -m "feat(harness): default rate-limit patterns per kind (#140)"
```

---

### Task 2: `Candidate.LimitPatterns` and `Policy.LimitGateDefaultMS`

**Files:**
- Modify: `internal/candidate/candidate.go` (struct field after `ExtraArgs`; validation in `Load` after the `Tree` check)
- Modify: `internal/candidate/candidate_test.go` (extend `TestLoadValidation` with a bad-regex case; extend `TestLoadAcceptsExtraArgsAndTree` -- or add a sibling -- so a good `limit_patterns` list round-trips)
- Modify: `internal/policy/policy.go` (field after `MaxSwitches`; `DefaultLimitGate`; `LimitGateDefault()`; validation in `Load` after the `MaxSwitches` check)
- Modify: `internal/policy/policy_test.go` (new `TestLimitGateDefault`, modelled on `TestSwitchLimit`)

**Interfaces:**
- Produces: `Candidate.LimitPatterns []string` `json:"limit_patterns,omitempty"` (§3.2). `Load` refuses with `candidates <path>: candidate <i>: limit_patterns[<j>]: <regexp error>` on the first pattern that does not compile.
- Produces: `Policy.LimitGateDefaultMS *int` `json:"limit_gate_default_ms,omitempty"`; `const DefaultLimitGate = time.Hour`; `func (p Policy) LimitGateDefault() time.Duration` (§3.3). `Load` refuses `<= 0` with `<path>: limit_gate_default_ms: must be > 0, got <n>: ErrBadPolicy`.

- [ ] **Step 1: Write the tests.** Candidate: a body with `"limit_patterns": ["(unclosed"]` fails `Load` with an error containing `limit_patterns[0]`; a body with `"limit_patterns": ["(?i)quota"]` loads and `Lookup` returns the list. Policy: table rows `absent -> DefaultLimitGate`, `{"limit_gate_default_ms":1800000} -> 30m`, `0 -> error containing "limit_gate_default_ms" and "must be > 0"`, `-5 -> error`.

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/candidate ./internal/policy`: compile errors on the new fields.

- [ ] **Step 3: Implement** both fields, the accessor and the two validations. `policy.Load` uses `DisallowUnknownFields`, so the new key must be on the struct for existing policy files with it to load.

- [ ] **Step 4: Run** `go test -count=1 ./internal/candidate ./internal/policy && go build ./...`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/candidate internal/policy
git commit -m "feat(config): limit_patterns per candidate, limit_gate_default_ms in policy (#140)"
```

---

### Task 3: `matchLimit` and `parseReset`

**Files:**
- Create: `internal/relay/limit.go`
- Create: `internal/relay/limit_test.go`

**Interfaces:**
- Produces: `type LimitMatch struct { Line string; Until time.Time; Parsed bool }` (§3.4).
- Produces: `func matchLimit(text string, patterns []*regexp.Regexp, now time.Time, fallback time.Duration) (LimitMatch, bool)` (§4.2): last matching line wins; `Line` is `strings.TrimSpace`d and capped at 200 runes; `Until` is UTC.
- Produces: `func parseReset(line string, now time.Time) (time.Time, bool)` (§4.3): duration form first, then clock form; result must be in `(now, now+7d]`; returned in UTC.
- Produces: `const limitScanLines = 40`.

- [ ] **Step 1: Write the tests** as one table each for `parseReset` and `matchLimit`, with `now := time.Date(2026, 9, 13, 23, 13, 0, 0, time.FixedZone("EEST", 3*3600))`:

  `parseReset` rows (want is in the same zone; compare with `Equal`):
  | line | want | ok |
  |---|---|---|
  | `Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.` | now+2h48m52s | true |
  | `limit · resets 7pm` | 2026-09-14 19:00 EEST | true |
  | `individual quota reached (resets ~00:26)` | 2026-09-14 00:26 EEST | true |
  | `Resets at 23:30` | 2026-09-13 23:30 EEST | true |
  | `error: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 95h4m16s.` | now+95h4m16s | true |
  | `try again in 5 min` | now+5m | true |
  | `retry after 30s` | now+30s | true |
  | `You've hit your limit` | -- | false |
  | `try again in 400h` | -- | false |
  | `resets 99:99` | -- | false |

  `matchLimit` rows, with `patterns` = the compiled agy defaults and `fallback = time.Hour`:
  - text of three lines where lines 1 and 3 both match -> `Line` is line 3.
  - the agy fixture line -> `Parsed=true`, `Until = now+2h48m52s`.
  - `individual quota reached` alone -> `Parsed=false`, `Until = now+1h`.
  - `starting\nboom: out of tokens\nrelay-exit:3` -> `ok=false`.
  - any text with `patterns == nil` -> `ok=false`.
  - a 300-rune matching line -> `len([]rune(Line)) == 200`.

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/relay -run 'TestParseReset|TestMatchLimit'`: compile errors.

- [ ] **Step 3: Implement** in `limit.go`, package `relay`, with a file header comment naming the issue and the two rules (decision points only; last line wins). The duration and clock regexes are package-level compiled vars. Duration components sum `h|hr|hours?`, `m|min|minutes?`, `s|sec|seconds?`. Clock: hour 0-23 (12 with am/pm: `12am`=0, `12pm`=12; `pm` adds 12 for 1-11), minute 0-59, else unparsed; build in `now.Location()` on `now`'s date; if `!t.After(now)` add 24h. Apply the `(now, now+7d]` bound to both forms.

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay -run 'TestParseReset|TestMatchLimit'`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/limit.go internal/relay/limit_test.go
git commit -m "feat(relay): rate-limit matcher and reset-time parser (#140)"
```

---

### Task 4: `switchBuilder` gains `counted`; gated switches stop counting

**Files:**
- Modify: `internal/relay/switch.go` (signature; the two `b.RoundSwitches++` lines become conditional on `counted`; doc comment gains one paragraph on why a gated switch is uncounted, §1 decision 5)
- Modify: `internal/relay/reconcile.go` (gone trigger passes `true`; gated trigger passes `false`)
- Modify: `internal/relay/headless.go` (gated trigger passes `false`; exit path passes `true`)
- Modify: `internal/relay/headless_test.go` (`switchHeadless` helper passes `true`; `TestReconcileHeadlessGatedKillsAndSwitches` expects `RoundSwitches == 0`)
- Modify: `internal/relay/switch_test.go` (`TestGatedSwitchesAtOnce` expects `RoundSwitches == 0`; add `TestGatedSwitchDoesNotCount`)

**Interfaces:**
- Changes: `switchBuilder(ctx, rt, tx, b, reason string, closeOld, counted bool)` (§4.5).

- [ ] **Step 1: Update the two expectations** (`RoundSwitches != 1` -> `!= 0` with the message "a gated switch is uncounted") and write `TestGatedSwitchDoesNotCount`: `sentSwitchable`, set `b.RoundSwitches = rt.Policy.SwitchLimit() - 1` and save, `Unavailable` on `agy/other/m`, reconcile -> a switch happened (one start, one switch entry) and `got.RoundSwitches == limit-1` still. Then a second assertion in the same test: set `b.RoundSwitches = rt.Policy.SwitchLimit()` on a fresh `sentSwitchable`, gate, reconcile -> halted (`StateNeedsYou`, no start): the limit check still applies.

- [ ] **Step 2: Run to verify** -- `go test -count=1 ./internal/relay -run 'TestGatedSwitchesAtOnce|TestReconcileHeadlessGatedKillsAndSwitches|TestGatedSwitchDoesNotCount'`: the first two fail on `RoundSwitches`, the third fails to compile or fails on the count.

- [ ] **Step 3: Implement** the parameter and the four call sites plus the test helper. `TestMaxSwitchesZeroHalts` must still pass unchanged.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/relay`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/switch.go internal/relay/reconcile.go internal/relay/headless.go internal/relay/headless_test.go internal/relay/switch_test.go
git commit -m "feat(relay): a rate-limit switch does not count toward max_switches (#140)"
```

---

### Task 5: `limitPatterns`, `limitText`, `gateOnLimit`

**Files:**
- Modify: `internal/relay/limit.go`
- Modify: `internal/relay/limit_test.go`

**Interfaces:**
- Produces: `func limitPatterns(rt Runtime, token string) []*regexp.Regexp` (§4.1): `candidate.ParseRef(token)` -> `rt.Candidates.Lookup` -> `harness.Lookup(c.Harness).LimitPatterns` then `c.LimitPatterns`, each compiled; nil when the token does not resolve or `rt.Candidates == nil`. A candidate pattern that fails to compile here cannot happen (`Load` refused it) -- skip it defensively rather than panic.
- Produces: `func limitText(ctx context.Context, rt Runtime, b store.Binding) string`: headless -> `logTail(b.Builder.LogPath, limitScanLines)`; pane -> `rt.Herdr.ReadAgent(ctx, Target(b.Builder), scrapeLines)`, `slog.Warn("limit scan: builder screen unreadable", ...)` and `""` on error.
- Produces: `func gateOnLimit(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, text string, closeOld bool) (next store.Binding, m LimitMatch, handled bool, err error)` (§4.4), exactly the flow written there: switchable guard; match; `appendEntryLocked` with the §3.5 entry (stderr on failure, continue); `slog.Warn("provider rate-limited", "binding", "round", "provider", "until", "parsed", "line")`; headless log marker `rate-limited: <line>` via `appendLogMarker`; report check via `os.Stat(rt.Store.ReportPath(b.Name, b.Round))` -> `(b, m, false, nil)`; else `switchBuilder(..., "rate-limited: "+m.Line, closeOld, false)` -> `(next, m, true, err)`. `m` is the zero value (`Line == ""`) on no match and when not switchable.

- [ ] **Step 1: Write the tests**, running `gateOnLimit` inside `rt.Store.WithLock` the way `switchHeadless` runs `switchBuilder`. Setup as in `TestReconcileHeadlessGatedKillsAndSwitches` (two-provider set, headless bind on `agy/other/m`, `orderOf("builder", "agy/other/m", testClaudeRef, testOpencodeRef)`, one `Send`), clock via `at(rt, time.Minute)`:
  - **match, no report** with the agy fixture line as `text`, `closeOld=false` -> `handled=true`; `loadLedger` has one `RateLimited` entry with `Subject=="other"`, `Source=="relay"`, `Binding=="webshop"`, `Note==` the fixture line, `Until.Equal(now+2h48m52s)`; one switch entry whose note starts `switched builder (rate-limited: Individual quota reached`; `got.RoundSwitches == 0`; `got.BuilderCandidate == testClaudeRef`; the builder log file's last line contains `rate-limited: Individual quota reached`.
  - **no match** (`text = "boom: out of tokens"`) -> `handled=false`, `m.Line == ""`, ledger has zero entries, no switch entry, `got` equals `b`.
  - **match with a report on disk** (write `rt.Store.ReportPath("webshop", 1)` first) -> `handled=false`, `m.Line` is the fixture line, ledger has the entry, no switch entry, zero `fr.specs` beyond the original one.
  - **not switchable** (`b.BuilderCandidate = ""`) with the fixture line -> `handled=false`, ledger empty.
  - **ledger write failure**: point `rt.LedgerPath` at a directory path (e.g. `t.TempDir()`) so `ledger.Save` fails -> `handled=true`, the switch still happened.

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/relay -run TestGateOnLimit`: compile errors.

- [ ] **Step 3: Implement** the three functions.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/relay -run 'TestGateOnLimit|TestMatchLimit|TestParseReset'`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/limit.go internal/relay/limit_test.go
git commit -m "feat(relay): gateOnLimit records the gate and switches uncounted (#140)"
```

---

### Task 6: headless decision points

**Files:**
- Modify: `internal/relay/headless.go` (the "Exited after writing a report" block; the "Exited without a report" block; the `checkRoundTimeout` call)
- Modify: `internal/relay/reconcile.go` (`checkRoundTimeout` gains `tx *store.Tx` as its third parameter and scans on the halting tick; its pane caller passes `tx`)
- Modify: `internal/relay/headless_test.go` (three new tests after `TestReconcileHeadlessGatedKillsAndSwitches`)

**Interfaces:**
- Changes: `checkRoundTimeout(ctx, rt, tx, b) (store.Binding, bool, error)`. New behaviour, before `haltBinding`: when `b.HaltNotifiedRound != b.Round`, `text := limitText(ctx, rt, b)`; `next, _, handled, err := gateOnLimit(ctx, rt, tx, b, text, true)`; when `handled`, return `(next, true, err)` -- the caller already treats `true` as "do not deliver". Otherwise halt as today.
- Headless exit without report: today the block appends the exit entry, logs, resets `b.Builder.PID, b.Builder.StartedAt` (keeping `LogPath`), then halts or switches. Insert the scan **after the PID reset and before the `!switchable` halt**: `next, _, handled, err := gateOnLimit(ctx, rt, tx, b, logTail(b.Builder.LogPath, limitScanLines), false)`; `if handled { return next, err }`. On no match, continue exactly as today (`switchable` halt or counted switch). `gateOnLimit` reads `LogPath`, which the reset leaves in place.
- Headless exit with report, no marker: before building the payload, `_, m, _, err := gateOnLimit(ctx, rt, tx, b, logTail(...), false)`; it returns `handled=false` because the report is present. When `m.Line != ""`, append to the payload after `Report: <path>.` the sentence ` Provider rate-limited: <line>; gated until <HH:MM local>.` (`m.Until.Local().Format("15:04")`). Then `queueReport` as today.

- [ ] **Step 1: Write the tests**, each set up like `TestReconcileHeadlessGatedKillsAndSwitches` (two-provider set, headless `agy/other/m`), with the log file written the way `TestReconcileHeadlessExitWithoutReportLogsAndSwitches` writes it:
  - `TestReconcileHeadlessExitOnLimitGatesAndSwitchesUncounted`: `fr.script(pid,false)`, `fr.exit(pid,1)`, log content `starting\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.\nrelay-exit:1\n` -> one exit entry (still written), one switch entry with the `rate-limited:` reason, `got.RoundSwitches == 0`, replacement is claude, ledger has the `other` gate with `Source "relay"`, `fr.kills` empty.
  - `TestReconcileHeadlessBudgetOnLimitKillsAndSwitches`: `b.RoundTimeoutMS = 1000`, process alive, log content with the fixture line, reconcile `at(rt, 2*time.Second)` -> `fr.kills` has the old handle, replacement started, `got.State == StateActive`, `f.notices` has exactly one notice containing `switched builder` and none containing `run past`, `got.HaltNotifiedRound == 0`.
  - `TestReconcileHeadlessBudgetWithoutLimitStillHalts`: same but log content `working hard\n` -> `TestReconcileHeadlessBudgetHaltsButNeverKills`'s assertions (halt, no kill), plus the ledger is empty.
  - `TestReconcileHeadlessExitWithReportOnLimitGatesAndClosesUnmarked`: write the report file, exit code 1, fixture line in the log -> `PendingForPlanner` note `unmarked`, payload contains `Provider rate-limited: Individual quota reached`, ledger has the gate, no switch entry, `fr.specs` has one spec.
  - Existing `TestReconcileHeadlessExitWithoutReportLogsAndSwitches` must pass unchanged (its log text matches nothing; its switch stays counted).

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/relay -run 'TestReconcileHeadless(ExitOnLimit|BudgetOnLimit|BudgetWithoutLimit|ExitWithReportOnLimit)'`.

- [ ] **Step 3: Implement** the signature change, the three headless points and the `checkRoundTimeout` scan. Keep `checkRoundTimeout`'s doc comment true: add one sentence that the halting tick scans for a rate limit first.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/relay`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/headless.go internal/relay/reconcile.go internal/relay/headless_test.go internal/relay/limit.go internal/relay/limit_test.go
git commit -m "feat(relay): headless exit and budget scan for rate-limit text (#140)"
```

---

### Task 7: pane decision points

**Files:**
- Modify: `internal/relay/reconcile.go` (`handleIdleBuilder`: after `quiescent` is established and before the `os.Stat(reportPath)` branch)
- Modify: `internal/relay/reconcile_test.go` (two new tests after `TestReconcileQuiescentWithoutReportStillScrapes`)
- Modify: `internal/relay/reconcile_blocked_test.go` (one new test after `TestReconcileFlagsRoundTimeout`; `TestReconcileFlagsRoundTimeout` itself gains one assertion)

**Interfaces:**
- `handleIdleBuilder`, once quiescent: `text := limitText(ctx, rt, next)`; `gated, m, handled, err := gateOnLimit(ctx, rt, tx, next, text, true)`; `if handled { return gated, err }`. Then the existing report branch, whose `unmarked` payload gains the same ` Provider rate-limited: <line>; gated until <HH:MM local>.` sentence when `m.Line != ""`; then `scrapeReport` as today. The pane budget point needs no edit here: Task 6's `checkRoundTimeout` already scans, and the pane caller now passes `tx`.

- [ ] **Step 1: Write the tests.** Pane setup: `sentSwitchable(t, f)` (agy on provider `other`, claude replacement), `f.readOut` set to the screen text, clock via `withClock`/`fakeClock` as `TestReconcileQuiescentWithoutReportStillScrapes` does, agents `plannerWith(StatusWorking,false)` + `builderAgent(StatusIdle)`:
  - `TestReconcileQuiescentOnLimitSwitchesInsteadOfScraping`: `readOut = "…\nIndividual quota reached. Resets in 2h48m52s.\n"`; first reconcile nudges, advance past `nudgeGrace`, second reconcile -> `f.closed == ["w2:p4"]`, one claude start, `got.Round == 1`, `got.RoundSwitches == 0`, no pending payload (`PendingForPlanner` not found), ledger has the `other` gate, one switch entry.
  - `TestReconcileQuiescentWithReportOnLimitGatesAndClosesUnmarked`: same screen, report file written before the second tick -> pending note `unmarked`, payload contains `Provider rate-limited:`, ledger gated, no pane closed, no start.
  - `TestReconcileTimeoutOnLimitSwitchesInsteadOfHalting` in `reconcile_blocked_test.go`: `sentSwitchable`, `b.RoundTimeoutMS = 30m`, `RoundStartedAt` 31m ago, `f.readOut` = fixture line, builder `StatusWorking` -> `got.State == StateActive`, `f.closed == ["w2:p4"]`, one start, notices contain `switched builder` and not `run past`.
  - `TestReconcileFlagsRoundTimeout` (existing; its `readOut` is empty so it halts as before) gains, after its second tick, `if len(f.reads) != 1 { t.Errorf("reads = %d, want exactly one limit scan, on the halting tick", len(f.reads)) }` -- the halting tick scans once, the already-halted tick not at all (§5 "once per transition").

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/relay -run 'OnLimit'`.

- [ ] **Step 3: Implement** the `handleIdleBuilder` point and the payload sentence.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/relay`. Expected: PASS, including every existing quiescence, scrape, nudge and timeout test unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/reconcile.go internal/relay/reconcile_test.go internal/relay/reconcile_blocked_test.go
git commit -m "feat(relay): pane quiescence and budget scan for rate-limit text (#140)"
```

---

### Task 8: README

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Candidates section** (after the `extra_args` bullet, line ~514): add
  `- \`limit_patterns\` — extra regexes, appended to the harness defaults, for the text this candidate's provider prints when it closes a session on quota. Extend-only; a default that misfires is a bug to report.`

- [ ] **Step 2: Policy section** (the JSON example gains `"limit_gate_default_ms": 3600000`; the paragraph after it gains one sentence): `\`limit_gate_default_ms\` is how long a rate limit relay detects itself gates the provider when the matched line names no reset time; absent defaults to one hour.`

- [ ] **Step 3: Headless section**: replace the two-line paragraph `Rate limits are still yours to declare: relay shows the log, it never reads it for meaning.` with a paragraph of at most six lines: when a round stops -- exit without the marker, a quiescent pane, or the round budget -- relay scans the builder's last output for the harness's rate-limit text; a match records a `rate_limited` gate with `source relay` and the matched line, parses the reset time when the line names one (`Resets in 2h48m52s`, `resets 7pm`) and otherwise uses `limit_gate_default_ms`, and switches the builder without counting toward `max_switches`; `relay unavailable` remains the override and `relay available` undoes a false positive. Also amend the `Exit without a report` bullet: `up to \`max_switches\`` -> `up to \`max_switches\` (a switch caused by a rate-limit gate is not counted)`.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: limit_patterns, limit_gate_default_ms and the daemon's limit gate (#140)"
```

---

### Task 9: mutation checks

Run each mutation, confirm the named test fails, revert with `git checkout -- <file>`, and record all three in the report.

- [ ] **Mutation 1:** in `switch.go`, make `RoundSwitches++` unconditional again. Expected failures: `TestGatedSwitchesAtOnce`, `TestReconcileHeadlessGatedKillsAndSwitches`, `TestGatedSwitchDoesNotCount`, `TestReconcileHeadlessExitOnLimitGatesAndSwitchesUncounted`.
- [ ] **Mutation 2:** in `gateOnLimit`, move the `os.Stat(report)` check after the `switchBuilder` call (i.e. always switch). Expected failures: the report-present `TestGateOnLimit` subtest, `TestReconcileHeadlessExitWithReportOnLimitGatesAndClosesUnmarked`, `TestReconcileQuiescentWithReportOnLimitGatesAndClosesUnmarked`.
- [ ] **Mutation 3:** in `matchLimit`, return the first matching line instead of the last. Expected failure: the last-line `TestMatchLimit` subtest.
- [ ] **Mutation 4:** in `checkRoundTimeout`, drop the `HaltNotifiedRound != Round` guard around the scan. Expected failure: `TestReconcileFlagsRoundTimeout` on its `reads` assertion (two ticks, two reads).

---

### Task 10: full constituent set

- [ ] **Step 1: Run the four constituents** from "Running commands", in order. Expected: green. `gofmt -l .` prints nothing.

- [ ] **Step 2: `git diff --stat main..HEAD`** and compare with §2's file list plus the test files named in this plan. Anything outside it goes in the report.

- [ ] **Step 3: No commit** (nothing to commit unless a constituent found something; if `gofmt` reformatted a file, commit that as `style: gofmt (#140)`).

---

## Report

Your report is `NNN-report.md` in the drop directory, then the empty
`NNN-done`. It lists: each task's commit hash; the Task 9 mutation checks
and which tests failed under each; the final constituent-set output; the
`git diff --stat`; and any step where you stopped, with the reason.
