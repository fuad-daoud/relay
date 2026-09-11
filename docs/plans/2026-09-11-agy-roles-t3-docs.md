# agy roles T3: README and the candidates-spec amendment (#85)

**Design spec:** `docs/specs/2026-09-11-agy-role-definitions-design.md` -- read §1, §3.6, §4.1, §7.1, §7.2, §7.5
**Issue:** #85
**Depends on:** T1 (the definitions, `--agent` launch, preamble removal). T2
may or may not be in this tree; nothing here depends on it. This task can
run in parallel with T2 -- the files are disjoint.

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

- This task touches `README.md` and
  `docs/specs/2026-09-11-candidates-design.md` only. No Go.
- Line numbers below are from the tree at the time of writing; grep for
  the quoted text rather than trusting the number.
- Do not rewrite passages this plan does not name. The README is long
  and has its own voice; match it.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: every place the docs say agy has no `--agent` flag

**Files:**
- Modify: `README.md`, `docs/specs/2026-09-11-candidates-design.md`

**Interfaces consumed** (from T1): agy's install paths
`~/.gemini/config/agents/{plan-executor,researcher,reviewer}.md`; the
launch line `--model <model> --agent <role.Definition>`; `model: inherit`
on every agy definition.

- [ ] **Step 1: the install block** (README ~L110–130, spec §3.6)

  In the numbered setup list, the step "Emit the builder's role
  definitions directly into your harness's config directory" currently
  shows `# claude` and `# opencode` blocks and then the paragraph
  beginning "`researcher` is the read-only role…". Add a third block
  first, so the order matches CLAUDE.md's dispatch order:

  ```
  # agy
  relay agent print --kind agy --role plan-executor > ~/.gemini/config/agents/plan-executor.md
  relay agent print --kind agy --role researcher    > ~/.gemini/config/agents/researcher.md
  ```

  Replace the paragraph's last two sentences -- from "Both shipped
  definitions pin a `model:`…" through "…the pin each installed
  definition carries." -- with:

  > The claude and opencode definitions pin a `model:` in their front
  > matter as a worked example, chosen so neither needs a provider the
  > rest of relay does not already assume; that line is the first thing to
  > change for your own setup. The agy definitions pin `model: inherit`
  > and that is not an example: on agy the key is a tier (`inherit`,
  > `flash`, `pro`) that would override the `--model` relay passes at
  > launch. `relay doctor` reports the pin each installed definition
  > carries, and warns when an agy copy pins a tier.

  Delete the paragraph that follows it: "`agy` has no `--agent` flag;
  relay prepends the role's preamble on the first prompt instead, so it
  has no definitions to install."

- [ ] **Step 2: the launch table** (README ~L399–406, spec §4.1)

  The table under "How relay launches one" has a `preamble` column.
  Remove the column and make the agy row match claude's:

  ```
  | kind | args |
  | --- | --- |
  | `agy` | `--model <model> --agent <role.Definition>` |
  | `claude` | `--model <model> --agent <role.Definition>` |
  | `opencode` | `--agent <role.Definition> -m <provider>/<model>` |
  ```

  (Rows sorted by kind, as `harness.All()` sorts.) The sentence after the
  table, "Any `extra_args` are appended verbatim…", stays.

- [ ] **Step 3: adopted panes** (README ~L444, spec §7.5)

  "An **adopted** pane (bind by pane id, or `--resume`) gets no preamble
  and needs no candidate: you launched that agent yourself, so it is
  already in whatever role you put it in." becomes:

  > An **adopted** pane (bind by pane id, or `--resume`) needs no
  > candidate: you launched that agent yourself, so it is already in
  > whatever role you put it in. relay selects a role only for agents it
  > starts, with `--agent` on the launch line.

- [ ] **Step 4: the consult-roles install block** (README ~L476–487)

  The block showing `relay agent print --kind claude --role reviewer` and
  the opencode equivalent gains an agy line first:

  ```
  # agy
  relay agent print --kind agy      --role reviewer > ~/.gemini/config/agents/reviewer.md
  ```

  The paragraph "`relay doctor` reports whether the definition landed.
  `agy` has no `--agent` flag; relay prepends the role's preamble when the
  candidate is `agy`, so it needs no definition file." becomes:

  > `relay doctor` reports whether the definition landed, on every kind.

  In the paragraph that follows ("Read-only is a property of the role's
  configuration…"), after "not something relay enforces." add one
  sentence:

  > On agy the definition's `tools:` allowlist makes it a property the
  > harness enforces: a write tool that is not listed is not offered.

- [ ] **Step 5: the rebind note** (README ~L666–670, spec §7.5)

  "Because the replacement builder is a new session that has not seen the
  role's preamble, relay re-sends the preamble on the next prompt even
  after round 1. Relay does not automatically re-send the current plan:
  …" becomes:

  > The replacement builder is started with its role on the launch line,
  > like any builder relay spawns. Relay does not automatically re-send
  > the current plan: …

  (keep the rest of that sentence and paragraph.)

- [ ] **Step 6: the `agent print` usage line**

  Grep the README for `--kind <claude|opencode>` and `known with
  definitions`; change each to `--kind <agy|claude|opencode>` and
  `known: agy, claude, opencode` respectively. If there are none, say so
  in your report.

- [ ] **Step 7: candidates-spec amendment** (spec §2 file list)

  In `docs/specs/2026-09-11-candidates-design.md`, add to the header
  block (after the `**Amends:**` lines, before `## 1. System overview`):

  ```
  **Amended 2026-09-11 by `2026-09-11-agy-role-definitions-design.md` (#85):**
  agy selects its role with `--agent` like every other kind. `RoleSpec.Preamble`
  (§3.4), `Harness.SelectsRoleByPreamble` and `Launch.Preamble` (§3.5), the
  preamble branch of `composePrompt` (§4.7) and the `l.Preamble +` prefix in
  `Ask` (§4.8) no longer exist. The sections below describe the design as
  shipped in #83; read them as history.
  ```

  Do not edit §3.4, §3.5, §4.7 or §4.8 themselves.

- [ ] **Step 8: check, commit**

  `grep -n -i 'preamble' README.md` must return nothing.
  `grep -n 'no --agent' README.md` must return nothing. `make check`
  (it will pass trivially; run it anyway so the report has the line).

  ```bash
  git add README.md docs/specs/2026-09-11-candidates-design.md
  git commit -m "docs: agy installs role definitions and launches with --agent; preamble references removed (#85)"
  ```

## Report

Include the two grep results, the `make check` line, and
`git diff --stat HEAD~1` (two files).
