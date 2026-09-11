# Policy T5: docs -- README "Policy", CLAUDE.md dispatch, design.md (#61 step 2)

**Design spec:** `docs/specs/2026-09-11-policy-order-design.md` -- read §1, §4.2, §4.5, §4.7, §7 step 5
**Issue:** #61 (step 2)
**Depends on:** T3 and T4 -- both already in this tree. Run `relay policy --help 2>&1 | head -3` and `grep -n KindPick internal/store/log.go` to confirm before you start; stop if either is missing.

The spec is in your worktree. This task is prose only; no Go changes.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every edit goes here.** It is your shell's cwd. |
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

- **No `.go` file changes.** `git diff --stat` at the end shows only
  `README.md`, `CLAUDE.md`, `docs/design.md`.
- Use the replacement text below verbatim where it is given in a block;
  where a step says "rewrite", keep the surrounding section's voice
  (second person, present tense, short paragraphs, the "why" after the
  "what").
- Every command and flag you mention must exist: check `relay help` and
  the flag help in `cmd/relay/main.go` before writing it down.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: the docs

**Files:**
- Modify: `README.md` ("Command surface" list; "Choosing a candidate"; "Availability"; new "Policy" subsection)
- Modify: `CLAUDE.md` ("Dispatching work to builders")
- Modify: `docs/design.md` ("Candidates (config)")

- [ ] **Step 1: README "Command surface"** 

  In the command list (around line 196), after the `relay candidates`
  bullet, add:

  ```
  - `relay policy` — show, per role, the candidates in the order relay would
    try them, which one it would pick right now, and any gap between
    `policy.json` and `candidates.json`.
  ```

- [ ] **Step 2: README "Choosing a candidate"** (spec §4.2)

  Replace the one paragraph under `### Choosing a candidate` (around line
  413) with:

  ```
  Pass the token to `relay bind --builder claude/anthropic/sonnet` and relay
  starts exactly that, gated or not (with a `note:` on stderr if it is).

  With `--builder` omitted, relay decides, by one rule:

  - exactly one configured candidate serves the role → that one, unless it
    is gated;
  - several serve it and `policy.json` orders them (see [Policy](#policy))
    → the first in that order that is not gated, then any serving
    candidate the order does not list, in token order;
  - several serve it and nothing is ordered → relay refuses and lists
    them. Name one, or write the order.

  When every candidate serving the role is gated, relay refuses and says
  why each one is; an explicit `--builder` still bypasses that. The same
  rule applies to `relay add`, `relay fork` (which first inherits the
  source's candidate -- an inherited token counts as explicit) and `relay
  ask --candidate`.

  Every choice is written down. `bind`, `add`, `fork` and `ask` print one
  line saying what was picked and why, and the same line lands in the
  binding's log as a `pick` entry, so `relay log` shows it later:

  ```
  picked claude/anthropic/sonnet for builder: order #2; skipped agy/google/gemini-3.8-flash-high (rate-limited until 20:28)
  ```

  `relay candidates` prints the configured tokens with their roles. A
  `--builder` value containing `:` and no `/` is a herdr pane id to adopt.
  ```

- [ ] **Step 3: README "Policy" subsection** (spec §3.1, §4.7)

  Insert a new `### Policy` subsection directly after "Choosing a
  candidate" and before "Availability":

  ```
  ### Policy

  `~/.config/relay/policy.json` is where you tell relay the order to try
  candidates in, per role:

  ```json
  {
    "order": {
      "builder": ["agy/google/gemini-3.8-flash-high",
                  "claude/anthropic/sonnet",
                  "opencode/openrouter/z-ai/glm-5.3-flash"]
    }
  }
  ```

  Roles you leave out are unordered, and an omitted `--builder` keeps
  refusing for them when several candidates serve the role. A candidate
  you add to `candidates.json` without adding it here is tried last, after
  everything listed. An entry here that names a candidate that is not
  configured, or one that does not serve the role, is skipped -- never an
  error, because removing a candidate must not stop every command -- and
  `relay policy` and `relay doctor` warn about it.

  `relay policy` shows what relay would do right now:

  ```
  builder  (order set in ~/.config/relay/policy.json)
    1  agy/google/gemini-3.8-flash-high        order     rate-limited until 20:28
    2  claude/anthropic/sonnet                 order     <- would pick
    3  opencode/openrouter/z-ai/glm-5.3-flash  unlisted
  reviewer  (no order set)
    1  claude/anthropic/opus                   sole      <- would pick
  ```

  The marker is computed by the same code `bind` runs, so it cannot
  disagree with what `bind` does next. There is no `relay policy set`:
  edit the file. The rest of #61 -- scoring for unordered roles, peak
  windows, mid-round switching -- will add keys to this file as it
  lands.
  ```

  (Nest the inner fences by using four backticks for the outer block or
  indenting -- check the rendered result with `grep -n '```' README.md`
  around the section: every fence you opened is closed.)

- [ ] **Step 4: README "Availability"** (spec §1, §7 step 5)

  In `### Availability`:

  - First paragraph: replace `It shows the ledger; it does not (yet) act
    on it.` with `It shows the ledger, and an omitted `--builder` skips
    what the ledger gates (see [Choosing a candidate](#choosing-a-candidate)).`
  - Last paragraph ("Where it shows: ..."): replace the final sentence
    (`bind`/`add`/`fork`/`ask` print a `note:` ... refusing is a later
    step of #61.`) with:

    ```
    `bind`/`add`/`fork`/`ask` with an explicit token print a `note:` on
    stderr when the candidate is gated and **proceed** -- you named it. With
    the token omitted they skip gated candidates and refuse when nothing
    ungated serves the role.
    ```

- [ ] **Step 5: CLAUDE.md "Dispatching work to builders"** 

  Replace the numbered list and the two paragraphs around it -- from `Use
  harnesses in this order, exhausting each before moving to the next:`
  through `Do not assign different harnesses to different tasks as a way
  of parallelising.` -- with:

  ```
  The harness order lives in `~/.config/relay/policy.json` under
  `order.builder`, not here. Omit `--builder` and relay takes the first
  candidate in that order the ledger does not gate; `relay policy` shows
  which one that is right now and why. Name a token only to override the
  order for one binding.

  When a builder reports a usage limit, run `relay unavailable <token>
  --reason '<what it said>'` before the next bind, so the next pick skips
  that provider, and `relay available <provider>` when it lifts. Do not
  bind different harnesses to different tasks as a way of parallelising;
  do not work around a gated provider by naming a token on it.
  ```

  Keep the paragraph after it (`Candidates are configured in ...`), but
  change its last sentence to: `Pass the token to `--builder` only to
  override the order.`

  Check the result reads as one section: `sed -n '/## Dispatching/,/## Working/p' CLAUDE.md`.

- [ ] **Step 6: docs/design.md "Candidates (config)"**

  After the paragraph beginning `Candidates are configured in
  ~/.config/relay/candidates.json` (around line 159), add one paragraph:

  ```
  Which candidate an omitted token resolves to is decided by
  `~/.config/relay/policy.json` (`order[role]`) together with the
  availability ledger: the first ungated candidate in the order, refusing
  when nothing ungated serves the role or when several serve it and
  nothing is ordered. Each resolution is a `pick` entry in the binding's
  `log.jsonl`. See `docs/specs/2026-09-11-policy-order-design.md`.
  ```

  In the `log.jsonl` entry section (around line 169), if it enumerates
  `kind` values, add `pick` with the description `relay -> log only: which
  candidate a spawn resolved to and why`. If it does not enumerate them,
  leave it.

- [ ] **Step 7: `make check`, commit**

  `make check` (or its constituents) must still be green: prose only, but
  run it anyway. Then:

  ```bash
  git add README.md CLAUDE.md docs/design.md
  git commit -m "docs: policy.json order, relay policy, pick entries; CLAUDE.md dispatch reads the policy (#61 step 2)"
  ```

## Report

Include `git diff --stat HEAD~1` (three files only), and paste the final
"Dispatching work to builders" section of CLAUDE.md so the planner can
read it as the next planner will.
