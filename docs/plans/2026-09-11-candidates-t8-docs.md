# Candidates T8: docs -- aliases become candidates everywhere a reader looks (#80)

**Design spec:** `docs/specs/2026-09-11-candidates-design.md` -- read §1, §3.2, §3.4, §3.5, §4.3, §4.12, §4.13
**Issue:** #80
**Depends on:** T7 -- merged into this tree.

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

Verification is `make check` (docs cannot break it, but run it anyway so the
report says so). If `make` is intercepted, run its constituents directly:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

## Global constraints

- **Prose only.** This task touches `README.md`, `docs/design.md` and
  `CLAUDE.md`. No `.go` file changes. If a doc claim cannot be made true
  without a code change, stop and report it -- do not describe behaviour
  that is not there.
- Every command and flag you write must exist: check `cmd/relay/main.go`
  before writing a flag name. Every JSON key must match
  `internal/candidate/candidate.go`'s struct tags.
- Keep the README's voice (read a few paragraphs first). No marketing.
- Do **not** edit any file under `docs/specs/` or `docs/plans/`. Older
  specs describe the code as it was; they are history.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: README, design.md, CLAUDE.md

**Files:**
- Modify: `README.md`
- Modify: `docs/design.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: README "Builder aliases" → "Candidates"** (spec §1, §3.2, §3.5)

  Replace the whole `## Builder aliases` section (from its heading to the
  line before `## Consults: asking a reviewer`) with a `## Candidates`
  section carrying, in this order:

  1. One paragraph: a candidate is one way to fill a role, named by the
     token `harness/provider/model`. `harness` and `provider` are single
     segments; `model` is the rest, so `opencode/openrouter/z-ai/glm-5.3-flash`
     is one token. **relay ships no candidates**: which model you are
     entitled to run is a fact about your accounts, not about relay.
  2. The file: `$XDG_CONFIG_HOME/relay/candidates.json` (default
     `~/.config/relay/candidates.json`), a JSON array. Show this example
     verbatim:

     ```json
     [
       {
         "harness":  "claude",
         "provider": "anthropic",
         "model":    "sonnet",
         "roles":    ["builder", "reviewer"]
       },
       {
         "harness":  "opencode",
         "provider": "openrouter",
         "model":    "z-ai/glm-5.3-flash",
         "roles":    ["builder"],
         "extra_args": ["--auto"]
       },
       {
         "harness":    "agy",
         "provider":   "google",
         "model":      "gemini-3.8-flash-high",
         "roles":      ["builder"],
         "extra_args": ["--dangerously-skip-permissions"]
       }
     ]
     ```

  3. A field list: `harness` (a kind relay knows: `agy`, `claude`,
     `opencode`; required), `provider` (who enforces the quota; free text;
     required), `model` (passed to the harness as-is; required), `roles`
     (non-empty; from `builder`, `reviewer`, `researcher`), `tree`
     (`binding`, the default, or `none`, which `relay ask` refuses today),
     `extra_args` (appended verbatim after what relay renders). A file
     that does not validate stops every relay command with a message
     naming the entry; a missing file is zero candidates.
  4. The roles table, copied from spec §3.4 minus the preamble column, plus
     one sentence: a role is relay's name for a job; the harness definition
     it selects is what `relay agent print` emits.
  5. "How relay launches one": the rendering table from spec §3.5
     (`claude` → `--model M --agent DEF`; `opencode` → `--agent DEF -m
     PROVIDER/M`; `agy` → `--model M` and the role's preamble on the first
     prompt), then `extra_args`. One sentence: because relay renders the
     argv, the token in `relay status` is exactly what was started.
  6. The `--dangerously-skip-permissions` note, reworded from today's
     `abuilder` note: it is an `extra_args` entry you add once you have
     watched a few rounds and trust the loop with that tree; relay never
     adds it.
  7. Choosing one: `relay bind --builder claude/anthropic/sonnet`. With
     `--builder` omitted, relay uses the only configured candidate that
     serves `builder`; with several it refuses and lists them; with none
     it names the file. The same rule applies to `relay add`, `relay fork`
     (which first inherits the source's candidate) and `relay ask
     --candidate`. `relay candidates` prints the configured tokens with
     their roles. A `--builder` value containing `:` and no `/` is a herdr
     pane id to adopt.
  8. The adopted-pane paragraph, kept: an adopted pane gets no preamble
     and needs no candidate.
  9. One line: `aliases.json` from earlier versions is no longer read.

- [ ] **Step 2: README consult sections** (spec §4.8)

  In `## Consults: asking a reviewer`: the sentence "a consult on `agy`
  selects its role from the alias preamble" → "relay prepends the role's
  preamble when the candidate is `agy`". "the alias decides where it runs"
  → "the candidate's `tree` decides where it runs".

  Replace `### The reviewer alias` (heading through the "One trap"
  paragraph) with `### Consult candidates`: `relay ask --role reviewer`
  resolves `reviewer` through the role table and then picks a candidate
  whose `roles` include it, by the same rule as `--builder`. Show a
  two-line example: a `candidates.json` entry with `"roles": ["reviewer"]`,
  and `relay ask --role reviewer --candidate claude/anthropic/opus --file
  q.md webshop`. Until a candidate lists `reviewer`, `ask` fails with `no
  configured candidate serves role "reviewer"`. Delete the "One trap"
  paragraph -- there is no layering any more.

- [ ] **Step 3: README everywhere else**

  - Line ~24: "relay ships example aliases for … see [Builder aliases]" →
    "relay knows how to start `opencode`, `claude` and `agy`; you tell it
    which models in [Candidates](#candidates)."
  - "First run on a clean machine": step 4's agy sentence → "`agy` has no
    `--agent` flag; relay prepends the role's preamble on the first prompt
    instead, so it has no definitions to install." Insert a new step
    after it: "Write `~/.config/relay/candidates.json` (see
    [Candidates](#candidates)) and check it with `relay candidates`."
    Step 7's `relay bind --builder cbuilder` → `relay bind --builder
    claude/anthropic/sonnet` (or, with one candidate, `relay bind`).
  - Command surface: `[--builder ALIAS|PANE_ID]` → `[--builder
    CANDIDATE|PANE_ID]`; the sentence "`--builder` is looked up as an alias
    unless it contains `:`" → "`--builder` is a candidate token unless it
    contains `:` and no `/`, in which case …". `relay add --name N
    --builder ALIAS` → `relay add --name N [--builder CANDIDATE]`. `relay
    fork … [--builder ALIAS]` → `[--builder CANDIDATE]`. Add `relay ask
    … [--candidate CANDIDATE]` to the `ask` line if the surface lists its
    flags. Add a line: `relay candidates` — list the configured
    candidates and the roles each serves.
  - Every remaining example using `cbuilder`, `abuilder` or `builder` as
    a `--builder` value (the `relay add` examples near line ~268 and any
    others: `grep -n "cbuilder\|abuilder\|--builder builder" README.md`)
    → a token from the step-1 example.
  - Line ~632: "has not seen the alias preamble" → "has not seen the
    role's preamble".
  - Final check: `grep -n -i "alias" README.md` returns only the one line
    from step 1 item 9.

- [ ] **Step 4: `docs/design.md`**

  - `relay bind --builder <alias|pane>` → `<candidate|pane>`.
  - The `builder.alias` row → `| builder_candidate | string |
    harness/provider/model the builder was started from; empty when adopted |`.
  - Replace `### Alias table (config, derived from the author's shell
    functions)` and its table + paragraph with `### Candidates (config)`:
    two sentences pointing at README "Candidates" and the spec, and the
    rendering table from spec §3.5. Keep the sentence explaining why agy
    needs a preamble (no `--agent` flag; a persistent session cannot bake
    it into startup; prepended to round 1's prompt only).
  - Decision-table row "Builder selection": rationale → "Preserves
    existing cost/model control; the token names exactly what starts, so
    the log and `status` show it without a lookup".
  - `grep -n -i alias docs/design.md` → no hits.

- [ ] **Step 5: `CLAUDE.md`**

  In "Dispatching work to builders": the numbered list becomes

  ```
  1. `agy/google/gemini-3.8-flash-high`
  2. `claude/anthropic/sonnet`
  3. `opencode/openrouter/z-ai/glm-5.3-flash`
  ```

  Delete the paragraph beginning "Note the alias names do not track the
  priority order" entirely. Replace it with: "Candidates are configured in
  `~/.config/relay/candidates.json`; `relay candidates` lists what this
  machine has. Pass the token to `--builder`, or omit it when only one
  candidate serves `builder`."

- [ ] **Step 6: `make check`, commit**

  ```bash
  git add README.md docs/design.md CLAUDE.md
  git commit -m "docs: aliases become candidates; candidates.json, roles, launch rendering (#80)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1` (exactly three
files), and the outputs of `grep -n -i alias README.md docs/design.md
CLAUDE.md`.
