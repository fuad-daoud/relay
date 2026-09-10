# Consults Task 8, round 2: make `reviewer` a first-class shipped role

Round 1 is correct and committed (`7fc0f7c`). I verified it independently:
`make check` green on zen, scope exactly the seven declared files, both
mutations killed their named test (`Consults = 3, want 2 running` and
`rendered a zero consult count`), and both doc amendments read correctly.

Your Note 1 found a real gap, and this round closes it. It is a defect in the
plan you were given, not in your work.

## Why round 2 exists

The reviewer definitions ship in the embed FS, but nothing can reach them:

```
$ relay doctor
claude
  binary                  ok       /usr/bin/claude
  integration             ok       current (v9)
  plan-executor           ok       ~/.claude/agents/plan-executor.md (model: sonnet)
  researcher              ok       ~/.claude/agents/researcher.md (model: haiku)
```

No `reviewer` row. `harness.AgentDoc` resolves roles only through the hard-coded
`Roles` slices in `internal/harness/harness.go`, and `reviewer` is not in them —
exactly as you reported.

That leaves `reviewer` worse off than every other role relay ships. The README
already tells users to install the other two with `relay agent print`
(README:112-117); yours has to be copied out of the source tree by hand, and
`relay doctor` cannot tell the user whether it landed. #24's whole argument for
`doctor` is that an install should be verifiable without reading the README.

You were right not to do this in round 1 — `harness.go` was outside the declared
scope and `TestTableExactValues` pins the tables deliberately. It is in scope
now.

## Task

- [ ] **Step 1: Register the role**

In `internal/harness/harness.go`, add a third entry to each `Roles` slice,
**after** `researcher`. The doc comment at `:19-22` says the ordering is
deliberate — plan-executor first, because doctor should report the role relay's
loop depends on before the rest — so append rather than insert.

claude:

```go
			{Name: "reviewer", Path: ".claude/agents/reviewer.md", Doc: "reviewer.claude"},
```

opencode:

```go
			{Name: "reviewer", Path: ".config/opencode/agents/reviewer.md", Doc: "reviewer.opencode"},
```

`agy` keeps `Roles: nil` — it has no `--agent` flag and selects a role by
preamble.

- [ ] **Step 2: Update the pinned table test**

`TestTableExactValues` (`internal/harness/harness_test.go:41`) asserts the exact
tables and will now fail. Add the same two entries to its `expected` map, in the
same order. Run it and watch it go from FAIL to PASS:

```bash
dev run go test ./internal/harness/ -run TestTableExactValues -v
```

This test is doing its job — it is meant to make a role addition a deliberate,
visible act. Do not weaken it.

- [ ] **Step 3: Update the `relay agent print` usage strings**

`cmd/relay/agent.go` names the roles in four places (`:13`, `:20`, `:32`, `:38`).
Change every `<plan-executor|researcher>` to
`<plan-executor|researcher|reviewer>` and the `--role` flag's help text at `:32`
from `(plan-executor, researcher)` to `(plan-executor, researcher, reviewer)`.

- [ ] **Step 4: Verify it is actually reachable**

```bash
dev run make check
go build -o /tmp/relay ./cmd/relay
/tmp/relay agent print --kind claude --role reviewer | head -5
/tmp/relay agent print --kind opencode --role reviewer | head -5
/tmp/relay doctor 2>&1 | sed -n '/^claude/,/^$/p'
```

The two `agent print` calls must emit the frontmatter of the definitions you
wrote in round 1. The doctor block must now carry a `reviewer` row. It will
report the role as missing on this machine, which is correct — nobody has
installed it yet.

- [ ] **Step 5: Point the README at `relay agent print`**

Your README section tells the user to copy the definitions out of
`internal/harness/agents/`. Now that the role is registered, replace those
instructions with the same form the other roles use (README:112-117):

```
   relay agent print --kind claude   --role reviewer > ~/.claude/agents/reviewer.md
   relay agent print --kind opencode --role reviewer > ~/.config/opencode/agents/reviewer.md
```

Keep everything else in that section — the alias JSON, the `LoadTable`
wholesale-replacement trap, the `ErrUnknownAlias` note, and the statement that
read-only is a property of the role's configuration rather than something relay
enforces.

- [ ] **Step 6: Commit**

```bash
git add internal/harness/ cmd/relay/agent.go README.md
git commit -m "feat(harness): register reviewer so doctor and agent print serve it

The reviewer definitions shipped in the embed FS but nothing could reach them:
AgentDoc resolves roles only through the hard-coded Roles tables, so relay
agent print could not emit them and relay doctor had no row for them.

That left reviewer the only shipped role a user had to copy out of the source
tree by hand, and the only one whose install doctor could not verify."
```

## Constraints

- Do not change the role definitions themselves, `status.go`, or the tests from
  round 1. They are verified.
- Do not touch `internal/alias/alias.go`. The reviewer alias still ships as a
  README example only.
- If a step is impossible as written, stop and say so in your report.
