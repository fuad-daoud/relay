# Status line redesign: a round clock, round tokens, no money, the server

**Amends:** docs/specs/2026-09-13-statusline-design.md, decision 4 ("age is
time since the last relayed message") and the row anatomy of §1; and
docs/specs/2026-09-14-statusline-edges-design.md wherever it pins the right
cell as `age · STATE`.
**Scope:** `relevo status --line` only. `relevo status` (human), `--json`
(except four additive fields), and the `relevo ui` rail are unchanged.
**Status:** approved 2026-09-24; plan at `docs/plans/2026-09-24-statusline-redesign.md`.

## 1. What was wrong

Observed on a planner with seven bindings:

```
○ ck-w1   r1 · agy · plan sent · live unknown · 11.1M tok       15m · ACTIVE
○ ck-b1   r4 · opencode · report in · $0.35 · 36.7M tok         22m · ACTIVE
```

1. **The clock kept ticking after the report came in.** It was
   `now − LastPayload.TS`: during a round that is the round's elapsed time,
   but after the report it becomes "how long the report has sat", which reads
   as a round that is still running.
2. **ACTIVE said nothing.** It is `displayState(store.StateActive)`: the
   binding is open. A working builder ("plan sent") and an idle one whose
   report is in ("report in") both read ACTIVE.
3. **Money is not wanted**, and for a harness with no price (agy) the live
   figure rendered as the word `unknown` ("live unknown").
4. **Tokens meant two things.** A running row showed this round's live
   tokens; a closed row showed `Spend`, the sum over every round.
5. **A remote builder's server was not shown.**

## 2. The row

```
○ ck-w1   r1 · agy@contabo · plan sent · 11.8M tok          16m
○ ck-b1   r4 · opencode · report in · 8.2M tok              22m
● ck-x    r2 · opencode · builder gone                        4m · NEEDS YOU
○ docs    r2 · agy · report in                               23s · PAUSED
```

- **Harness cell:** the candidate's harness segment, as today; for a remote
  binding, `harness@server`, where server is the binding's configured server
  name (`Builder.Server`).
- **Waiting cell:** unchanged (`waiting()`).
- **Token cell:** this round's tokens only, `N tok` via `usage.ShortTokens`.
  While the round is open: `LiveUsage` (when it has samples). Once it has
  closed: the usage on *that round's* report entry. No usage for the round →
  no cell; an older round's figure is never shown. No dollar figure, no
  `live` word.
- **Right cell:** the round clock, then ` · STATE` only when the display
  state is not ACTIVE (NEEDS YOU, PAUSED, or any other non-ACTIVE word).
  NEEDS YOU keeps its colour; ACTIVE and its green colour are gone.

## 3. The round clock

The current round is the round of the newest `plan` entry sent to the
builder in the binding's log.

- **start** = TS of the *earliest* `plan` → builder entry of that round. A
  nudge, a switch or a repair may add later plan entries or reset
  `RoundStartedAt`; neither moves the start, so a builder switch does not
  restart the clock.
- **end** = TS of the newest `report` → planner entry of that round, or zero
  when there is none (the round is open).
- clock = `end − start` when end is set (frozen), else `now − start`
  (ticking), formatted by `AgeText`. No plan entry → `--`.

The clock is derived from the log, not from `Usage.DurationMS`, so it works
for rounds whose report carries no usage and agrees across harnesses.

## 4. Data

`BindingStatus` gains four additive fields, filled in `buildRow`:

| field | JSON | meaning |
|---|---|---|
| `RoundStart time.Time` | `round_start,omitzero` | §3 start; zero when the log has no plan entry |
| `RoundEnd time.Time` | `round_end,omitzero` | §3 end; zero while the round is open |
| `RoundUsage *usage.Usage` | `round_usage,omitempty` | usage on the report that set RoundEnd; nil when open or when that report has none |
| `Server string` | `server,omitempty` | `Builder.Server` for a remote binding; "" otherwise |

`RenderStatusLine` stays a pure function of `Report` and `now`.

## 5. Not changed

The dot, the planner line, width and truncation rules, `RELEVO_STATUSLINE_MARGIN`,
row order, `waiting()`'s words, `LastUsage`/`Spend`/`LiveUsage` and every
other surface that reads them (rail card, `relevo status`).
