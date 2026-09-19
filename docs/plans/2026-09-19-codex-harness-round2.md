# codex harness -- round 2: resolve the round 1 halt, then finish steps 4-10

This round continues `docs/plans/2026-09-19-codex-harness.md` (the "base
plan"; a copy is already in your worktree at that path) from the state
round 1 left. Everything in the base plan still applies -- halt rule, scope
guard, commit rule -- except as amended here.

**Before step 1**: `git branch --show-current` must print
`relay/codex-harness`; `git status --short` must show exactly the round 1
state: modified `internal/harness/agents.go`, `agents_test.go`,
`harness.go`, `harness_test.go`; untracked
`docs/plans/2026-09-19-codex-harness.md`,
`docs/specs/2026-09-19-codex-harness-design.md` and the four
`internal/harness/agents/*.codex.toml`. Anything else: halt. Do not
recreate what round 1 built; read it and continue.

## Amendments to the base plan

A. **§2 file list** gains nothing; `internal/harness/harness_test.go` is
   already listed. **§7 step 3** is extended: modify
   `TestArchitectShipsOnEveryKindAndIsNotARole` so that a kind whose
   `DocExt == "toml"` is checked for its own identity marker instead of a
   frontmatter key. The `name: architect` assertion becomes:

   ```go
   if h.DocExt == "toml" {
       // A profile has no frontmatter: its identity is the file relay
       // installs it at (architect.config.toml, selected with -p
       // architect) and the literal that carries the role text.
       if !strings.Contains(string(doc), "developer_instructions = '''") {
           t.Errorf("%s architect definition lacks the developer_instructions literal", h.Kind)
       }
   } else if !strings.Contains(string(doc), "name: architect") {
       t.Errorf("%s architect definition does not carry name: architect", h.Kind)
   }
   ```

   The `Ordered Implementation Steps` check stays for every kind (the codex
   body contains it because it is claude's body verbatim). The agy
   `model: inherit` check is untouched.

B. **§6.2 header comment**: round 1's wording -- the comment says
   `` The `agents.researcher` table at the end `` rather than
   `The [agents.researcher] table at the end` -- is accepted. Keep it.
   The ordering assertion in `TestAgentDocCodexIsToml` stays as written
   (index of `developer_instructions` < index of `[agents.researcher]`).

C. **§7 step 10 (gate)**: before the squash, append this section verbatim
   to the END of the plan copy in your worktree
   (`docs/plans/2026-09-19-codex-harness.md`), so the committed plan
   records what changed and why:

   ```
   ## 9. Amendments (round 2)

   - §7 step 3 also extends `TestArchitectShipsOnEveryKindAndIsNotARole`:
     a kind with `DocExt == "toml"` is checked for its
     `developer_instructions = '''` literal instead of `name: architect`,
     which is a frontmatter key a profile cannot carry. Round 1 halted on
     this test, correctly: the base plan had not listed it.
   - §6.2's header comment refers to the table as `agents.researcher` in
     backticks, so the comment does not precede the real key in the
     `TestAgentDocCodexIsToml` ordering check.
   ```

   Also commit `docs/plans/2026-09-19-codex-harness-round2.md` (this
   file; copy it from the round's plan path in
   `~/.local/state/relay/codex-harness/002-plan.md`).

## Steps

1. Apply amendment A. Run `go test ./internal/harness/...`: everything
   green, including `TestArchitectShipsOnEveryKindAndIsNotARole` and
   `TestArchitectHandoffIsSharedAcrossKinds`. If any other pre-existing
   test fails here, halt and name it.
2. Base plan step 4 (Launch).
3. Base plan step 5 (Tiers).
4. Base plan step 6 (Transcript).
5. Base plan step 7 (Usage).
6. Base plan step 8 (Doctor).
7. Base plan step 9 (README).
8. Base plan step 10 (Gate) with amendment C: `make check` clean;
   `git diff --stat` and untracked files cover only the base plan's §2 list
   plus the two plan files and the spec; `go.mod`/`go.sum` unchanged; ONE
   squashed commit with the base plan's subject; the report names every
   test written and the three mutation checks with the failure each
   produced.
