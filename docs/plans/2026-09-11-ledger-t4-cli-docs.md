# Ledger T4: `relay unavailable`, `relay available`, the spawn-time note, docs (#61 step 1)

**Design spec:** `docs/specs/2026-09-11-availability-ledger-design.md` -- read §4.4, §4.9, §5.2, §6
**Issue:** #61 (step 1)
**Depends on:** T1, T2, T3 -- already in this tree.

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- **No test may execute a `cmd/relay` subcommand that reaches herdr.**
  `newRuntime` constructs a herdr client, so `cmdUnavailable` and
  `cmdAvailable` are not tested through `main`; their logic lives in
  `relay.Unavailable` / `relay.Available` (T2, tested). The only
  `cmd/relay` test here is the pure `--for` parsing helper.
- The note after spawn goes to **stderr** and never changes the exit code.
- Docs: every command and flag you write must exist in `main.go`; check
  before writing. Do not edit anything under `docs/specs/` or `docs/plans/`.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: two commands, four notes, two docs

**Files:**
- Modify: `cmd/relay/main.go`, `cmd/relay/main_test.go`
- Modify: `README.md`, `CLAUDE.md`

**Interfaces consumed** (T2, T3): `relay.Unavailable(rt, token, until,
reason) (provider, err)`, `relay.Available(rt, subject) (provider, removed,
err)`, `relay.GatedNote(rt, token) string`, `relay.GateUntilText`;
`Binding.BuilderCandidate`; `AskResult.Candidate`; `rt.Now`,
`rt.Candidates`.

**Interfaces produced:**
- `relay unavailable <token> [--for D] [--reason S]`
- `relay available <provider|token>`
- `parseFor(s string, now time.Time) (time.Time, error)` (unexported, `cmd/relay`)

- [ ] **Step 1: `parseFor`** (spec §4.9)

  In `main.go`:

  ```go
  // parseFor turns --for into an absolute expiry. Empty means "until cleared"
  // (a zero time); anything else must be a positive Go duration -- relay does
  // not know a provider's reset schedule, so it never invents one (spec §1).
  func parseFor(s string, now time.Time) (time.Time, error)
  ```

  `""` → `time.Time{}, nil`; `time.ParseDuration` error → wrapped
  `fmt.Errorf("--for %q: %w", s, err)`; `d <= 0` → `fmt.Errorf("--for must
  be a positive duration, got %q", s)`; else `now.Add(d)`.

  Test (`main_test.go`) `TestParseFor`: `""` → zero; `"2h"` → `now+2h`;
  `"90m"` → `now+90m`; `"0"` → error; `"-5m"` → error; `"soon"` → error.

  Run: `go test ./cmd/relay/ -run ParseFor`. Expected: green.

- [ ] **Step 2: `cmdUnavailable` and `cmdAvailable`** (spec §4.9, §5.2)

  `cmdUnavailable(args)`: flags `--for` (string, default `""`) and
  `--reason` (string); exactly one positional, the token; usage error
  `relay unavailable <harness/provider/model> [--for DURATION] [--reason TEXT]`
  when missing. Build the runtime; `until, err := parseFor(*forFlag,
  rt.Now())`; `provider, err := relay.Unavailable(rt, token, until,
  *reason)`. Count configured candidates with that provider (iterate
  `rt.Candidates.Refs()`, `candidate.ParseRef`, compare `Provider`). Print
  `gated <provider> (<n> candidates) <until text>` using
  `relay.GateUntilText(until)`.

  `cmdAvailable(args)`: no flags; one positional, a provider or token;
  usage error `relay available <provider|harness/provider/model>`. `provider,
  removed, err := relay.Available(rt, subject)`. Print `cleared <provider>
  (<removed> entries)` or, when `removed == 0`, `nothing was gating
  <provider>`.

  Dispatch: `case "unavailable"` and `case "available"` beside
  `candidates`. Help text, two lines under `candidates`:

  ```
    unavailable  record a provider rate limit: relay unavailable <token> [--for D] [--reason S]
    available    clear a recorded rate limit: relay available <provider|token>
  ```

  Run: `go build ./...`. Expected: clean.

- [ ] **Step 3: the note after a successful spawn** (spec §4.4)

  In `cmdBind`, after the `bound %s: …` printf: `if n :=
  relay.GatedNote(rt, b.BuilderCandidate); n != "" { fmt.Fprintln(os.Stderr,
  n) }`. Same in `cmdAdd` (`res.Binding.BuilderCandidate`), `cmdFork`
  (`res.Binding.BuilderCandidate` -- check the result type's field name),
  and `cmdAsk` (`res.Candidate`). For an adopted builder
  `BuilderCandidate` is `""` and `GatedNote` returns `""`, so nothing
  prints.

  Run: `go build ./...`. Expected: clean.

- [ ] **Step 4: README** 

  Under `## Candidates`, after "Choosing a candidate" and before the
  adopted-pane paragraph, add `### Availability`:

  1. One paragraph: relay keeps a ledger of when a candidate could not be
     used -- spawn failures it observed, rate limits you report. It shows
     the ledger; it does not (yet) act on it. `~/.local/state/relay/ledger.json`.
  2. Reporting a limit, with the two commands verbatim:
     ```
     relay unavailable claude/anthropic/sonnet --reason "5-hour window"
     relay unavailable claude/anthropic/sonnet --for 2h
     relay available anthropic
     ```
     with one sentence each: a limit gates the **provider** (every
     candidate with `provider: anthropic`), because that is who enforces
     the quota; without `--for` it stays until `relay available`, because
     relay does not know your provider's reset schedule.
  3. Spawn failures: recorded by relay when starting an agent fails, gate
     that one candidate for ten minutes, expire on their own.
  4. Where it shows: `relay status` gains a `candidates` block only while
     something is gated; `relay candidates` marks gated rows
     `unavailable:`; `relay doctor` warns per gated candidate with the
     command that clears it. `bind`/`add`/`fork`/`ask` print a `note:` on
     stderr when they start a gated candidate and **proceed** -- refusing
     is a later step of #61.

  Command surface: add `relay unavailable …` and `relay available …` lines
  after `relay candidates`.

- [ ] **Step 5: `CLAUDE.md`**

  In "Dispatching work to builders", after "Move down the list only when
  the current harness is unavailable -- usage limits as much as a crash.",
  add: "When a builder reports a usage limit, run `relay unavailable
  <token> --reason '<what it said>'` before moving down, so `relay status`
  and `relay doctor` show why, and `relay available <provider>` when it
  lifts."

- [ ] **Step 6: `make check`, commit**

  ```bash
  git add cmd/relay/main.go cmd/relay/main_test.go README.md CLAUDE.md
  git commit -m "feat(relay): relay unavailable / available; note when spawning a gated candidate; docs (#61)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1` (four files), and
the output of `go run ./cmd/relay help | grep -A1 -i 'unavailable'` (help
text only; this does not reach herdr).
