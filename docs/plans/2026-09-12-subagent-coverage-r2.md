# Sub-agent coverage row -- round 2: finish Task 3, then Task 4 (#84)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Continues:** `docs/plans/2026-09-12-subagent-coverage.md` (round 1). Tasks 1
and 2 are committed (`651e228`, `dda7b3a`). Task 3's `status.go` and
`status_test.go` edits are in the worktree, uncommitted and correct; do not
redo them. Your round-1 halt was right: the plan did not anticipate that three
existing tests match the bare word `foreign`, which the coverage sentence
contains.

**Decision:** the three tests mean "a `foreign` *row*". Tighten them to match
the row label with its column padding, `"  foreign "` (two leading spaces,
one trailing), which is how `RenderStatus` prints every row label. The
coverage sentence's "no foreign rows above" has one leading space and cannot
match. The coverage wording is unchanged and the tests' bindings keep their
unset `BuilderKind` -- they now incidentally exercise the unknown-kind row,
which is fine.

Everything in round 1's "Where you are working", "Stop rather than
improvise", "Running commands", and "Global constraints" still applies.

---

### Task 3 (continued): tighten the three foreign-row assertions, finish Steps 4-6

**Files:**
- Modify: `internal/relay/status_test.go` -- three existing tests:
  `TestRenderStatusOmitsForeignLineWhenNone`,
  `TestRenderStatusForeignOmitsCWDAtTreeRoot`,
  `TestRenderStatusShowsEveryForeignAgent`
- Already modified, keep as-is: `internal/relay/status.go`,
  the three new tests at the end of `internal/relay/status_test.go`

- [ ] **Step 1: Tighten the three assertions**

In `TestRenderStatusOmitsForeignLineWhenNone`, change

```go
	if strings.Contains(out, "foreign") {
```

to

```go
	if strings.Contains(out, "  foreign ") {
```

In `TestRenderStatusForeignOmitsCWDAtTreeRoot`, change

```go
		if !strings.Contains(line, "foreign") {
```

to

```go
		if !strings.Contains(line, "  foreign ") {
```

In `TestRenderStatusShowsEveryForeignAgent`, change

```go
	if n := strings.Count(out, "foreign"); n != 2 {
```

to

```go
	if n := strings.Count(out, "  foreign "); n != 2 {
```

Leave `TestRenderStatusShowsForeignLine` alone: its positive assertion is
satisfied by the row itself and is not made wrong by the coverage line.

- [ ] **Step 2: Run the whole package**

Run: `go test -count=1 ./internal/relay/`
Expected: PASS. If anything else fails, stop and report.

- [ ] **Step 3: Mutation checks**

(a) Temporarily change the `SubAgentsForeground` case in
`internal/relay/coverage.go` to `return "", false`. Run
`go test -count=1 ./internal/relay/`. Expected:
`TestSubAgentCoverage/foreground` and
`TestRenderStatusCoverageRowPerKind/agy` fail. Restore; PASS.

(b) Temporarily make `RenderStatus` print the coverage row with the label
`foreign` instead of `coverage` (change the `"coverage"` argument in the
`fmt.Fprintf(&sb, "  %-8s %s\n", "coverage", note)` line to `"foreign"`).
Run `go test -count=1 ./internal/relay/`. Expected:
`TestRenderStatusOmitsForeignLineWhenNone` and
`TestRenderStatusShowsEveryForeignAgent` fail -- this proves the tightened
assertions still pin the row count. Restore; PASS.

- [ ] **Step 4: Commit**

```bash
gofmt -l internal/relay/ ; go vet ./internal/relay/
git add internal/relay/status.go internal/relay/status_test.go
git commit -m "feat(status): coverage row says what no foreign rows proves per builder kind (#84)"
```

---

### Task 4: Docs -- spec §7.2 points at the table; design.md names the row

Exactly as written in round 1's Task 4, Steps 1-4. Repeated here so you
need not open the other file.

**Files:**
- Modify: `docs/specs/2026-09-10-foreign-agent-detection-design.md` §7.2
  (from `### 7.2 Sanctioned read-only panes still show as foreign` up to `### 7.3`)
- Modify: `docs/design.md` -- the paragraph beginning `What it can do is say so.`

- [ ] **Step 1: Amend §7.2 of the foreign-agent spec**

Keep the first two paragraphs of §7.2 (beginning "After the researcher role
ships" and "Suppressing by title"). Replace everything from the paragraph
beginning `**Whether it surfaces at all is harness-specific.**` through the
paragraph ending `...drawing a false conclusion from silence.` (up to but not
including `### 7.3`) with:

```markdown
**Whether it surfaces at all is harness-specific, and relay says so.** The
per-kind record is `harness.Harness.SubAgents` in
`internal/harness/harness.go`: `separate` (claude -- own pane, shows as a
foreign row), `foreground` (agy -- takes over the builder pane's session slot,
#66), `hidden` (opencode -- in-process, herdr lists only the pane). Each
entry's comment carries the date, herdr version, and integration version it
was observed at. That table is the one record; this section does not restate
it.

The opencode case is by design, not chance: herdr's opencode integration
tracks child sessions by parent id and folds them into the pane's root session
so they cannot replace it. herdr's model is one agent per pane, and an
in-process sub-agent has no pane to be listed under.

`relay status` prints a `coverage` row for any binding whose builder kind is
not `separate`, after the foreign rows: "no foreign rows above does not mean
the tree is clear". A claude binding prints none. See
`docs/specs/2026-09-12-subagent-coverage-design.md`. Detection still covers
agents herdr knows about, which is not the same set as agents touching the
tree; the row exists so that the gap is visible from the output instead of
only from this spec.
```

- [ ] **Step 2: Add the coverage sentence to `docs/design.md`**

In the paragraph beginning `What it can do is say so.`, after the sentence
ending `...trusting a string any agent can set.`, append:

```markdown
How much that covers depends on the builder's harness: claude runs sub-agents
in their own panes, which herdr lists; agy and opencode do not, and herdr
lists nothing extra. For those bindings `relay status` prints a `coverage` row
after the foreign rows saying that no foreign rows does not mean the tree is
clear. The per-harness record is `harness.Harness.SubAgents`.
```

- [ ] **Step 3: Verify the whole tree**

Run: `make check` (or its constituents per round 1's "Running commands").
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add docs/specs/2026-09-10-foreign-agent-detection-design.md docs/design.md
git commit -m "docs: foreign-agent spec §7.2 points at the harness table; design.md names the coverage row (#84)"
```

---

## Report

Say that both mutation checks in Task 3 Step 3 failed the named tests and
passed after restore, and the `make check` result. `git diff --stat
ce8be2b..HEAD` should list exactly: `internal/harness/harness.go`,
`internal/harness/harness_test.go`, `internal/relay/coverage.go`,
`internal/relay/coverage_test.go`, `internal/relay/status.go`,
`internal/relay/status_test.go`,
`docs/specs/2026-09-10-foreign-agent-detection-design.md`, `docs/design.md`.
Call out anything else.
