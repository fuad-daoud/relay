# The architect definition tells the planner to hand off through relay (#188)

> **For agentic workers:** execute the tasks in order; each ends green and
> commits. Steps use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #188
**Related:** #174 (install writes the definition), #123 (plugin skill; overlaps
later, not now), #99 (headless builders).

## 1. System overview

`relay agent install` writes an `architect` definition for every harness kind
(`internal/harness/agents/architect.{claude,opencode,agy}.md`, embedded and
served by `harness.AgentDoc`). It is the planner's persona. Its body -- the
text after the YAML frontmatter -- is byte-identical across the three kinds
today and describes only how to *write* a plan. It never says what to do with
one. This round appends a **Handing off** section to that shared body so any
planner started with `--agent architect`, in any repo, on any device, reaches
for `relay send` and a headless builder instead of an in-session subagent;
adds a test that pins the section and the body identity across kinds; and
trims the now-duplicated protocol out of relay's own `CLAUDE.md`, leaving the
repo-specific facts and a pointer.

No Go behaviour changes. No new types, interfaces or files apart from this
plan. The whole round is markdown plus one test.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/handoff` | **the git worktree. Every source edit goes here.** Your shell's cwd, branch `relay/handoff`. |
| `~/.local/state/relay/handoff` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit. In
particular: if the three architect bodies are *not* byte-identical when you
start (Task 1 Step 1 checks), stop -- do not reconcile them yourself.

## Running commands

`make` is intercepted on this machine; run the constituents directly and say
so in the report:

```bash
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do not run `herdr` or any harness. No test under `cmd/relay` (CI has no
herdr; the rule is tested as a pure function in `internal/harness`).

## Global constraints

- The three definition **bodies stay byte-identical**. Edit one, copy to the
  other two; the test in Task 2 enforces it.
- Frontmatter of each kind is **untouched** (the agy one carries
  `model: inherit` and a tools allowlist; opencode carries `mode: primary`).
- No text about Claude Code specifics in the section (no "Agent tool", no
  slash commands): opencode and agy planners read the same body.
- `CLAUDE.md` edits are the ones Task 3 lists. Nothing else in it moves.
- Two commits: Tasks 1+2 together, Task 3 (+ the plan file) separately.

## 2. File structure

```
internal/harness/agents/architect.claude.md    modify: append "## Handing off" to the body
internal/harness/agents/architect.opencode.md  modify: same body
internal/harness/agents/architect.agy.md       modify: same body
internal/harness/harness_test.go               modify: new TestArchitectHandoffIsSharedAcrossKinds
CLAUDE.md                                      modify: collapse the two protocol sections
docs/plans/2026-09-18-architect-handoff.md     add: this file (copied from the drop directory)
```

## 3. Data structures & type definitions

None new. The only contract touched is textual: the body of a definition,
defined as *everything after the second line that is exactly `---`*. That
definition of "body" is what the test in Task 2 uses.

## 4. Interface definitions & component contracts

None new. `harness.AgentDoc(role, kind string) ([]byte, error)` is read by
the new test as it exists today; its signature does not change.

## 5. High-level pseudocode

```
TestArchitectHandoffIsSharedAcrossKinds:
  bodies := map[kind]string
  for each h in All():
    doc := AgentDoc("architect", h.Kind)          // fatal on error
    body := text after the second "---" line     // fatal if fewer than two
    assert body contains "## Handing off"
    assert body contains "relay send"
    assert body contains "--headless"
    assert body contains "relay unavailable"
    assert body does NOT contain "Agent tool" or "slash command"   // harness-neutral
    bodies[h.Kind] = body
  assert every value in bodies equals the "claude" one
    (report the first differing kind and the first differing line)
```

## 6. Error handling strategy

Not applicable: no runtime code path changes. The failure modes are test
failures, each of which names the kind and the missing marker or the first
differing line, so a future edit to one file and not the others is caught
by name.

---

## 7. Ordered implementation steps

### Task 1: append the Handing off section to all three bodies

**Files:**
- Modify: `internal/harness/agents/architect.claude.md`,
  `internal/harness/agents/architect.opencode.md`,
  `internal/harness/agents/architect.agy.md`

**Depends on:** nothing.

- [ ] **Step 1: confirm the precondition.** In the worktree:

```bash
for k in claude opencode agy; do
  awk 'f{print} /^---$/{c++; if(c==2)f=1}' internal/harness/agents/architect.$k.md | md5sum
done
```

All three sums must be equal. If they are not, stop and report.

- [ ] **Step 2: append the section to `architect.claude.md`.** The file
currently ends with the line `If any answer is "no," revise before
presenting the plan.` Append, after one blank line, exactly this:

````markdown
## Handing off

A finished plan is a file, and a builder runs it -- not you. Write the plan to
disk and send it with `relay send`. Never dispatch a plan to a subagent in
your own session: that skips the worktree, the round log, the diff capture and
the report handoff, and nothing done inline appears in `relay status`.

- **Bind before you send.** `relay bind` puts one builder on the current
  tree; `relay add --name <name>` puts another builder on its own git
  worktree. `relay status` shows what is already bound.
- **Headless by default.** Pass `--headless` unless a human will watch the
  pane or a step is expected to raise a dialog. A headless builder takes no
  dialogs (`relay answer` is refused) and keeps no memory across rounds, so
  every plan you send must stand alone -- which the Output Structure above
  already guarantees. A step that needs a mid-round decision is a reason to
  split the plan, not to use a pane.
- **Parallelism is instances, not harnesses.** Several builders are several
  `relay add` bindings of one harness, each on its own worktree. Never bind
  two harness kinds to two tasks as a way of parallelising. Omit `--builder`
  and let the configured order pick; `relay policy` explains the current
  pick and why.
- **A usage limit gates the provider.** When a builder reports one, run
  `relay unavailable <token> --reason '<what it said>'`; relay switches the
  binding to the next ungated candidate and resends the round. Do not work
  around a gated provider by naming another token. `relay available
  <provider>` when it lifts.
- **Tell the builder to stop rather than improvise.** Every plan says so: if
  a step is impossible as written or contradicts the code, halt and report.
  A halt that surfaces a design error is worth more than a green suite that
  bent a test to fit.
- **Do not trust the report.** When relay delivers it, run the project's own
  check command yourself and compare the diff against the plan's declared
  scope before calling the round done.
````

The file ends with a single trailing newline after `done.`.

- [ ] **Step 3: copy the body to the other two kinds.** Replace everything
after the second `---` line in `architect.opencode.md` and `architect.agy.md`
with the body of `architect.claude.md`. Do not touch either frontmatter.
Re-run the Step 1 loop: three equal sums, different from the Step 1 value.

- [ ] **Step 4: verify by eye.** `git diff --stat` shows exactly the three
definition files, each `+33` or thereabouts and `-0`. `head -12` of each
still shows its own frontmatter.

Do not commit yet; Task 2 commits with this.

### Task 2: pin the section and the body identity

**Files:**
- Test: `internal/harness/harness_test.go`

**Depends on:** Task 1 (the test must pass against it).

- [ ] **Step 1: write the failing shape first.** Add, after
`TestArchitectShipsOnEveryKindAndIsNotARole`:

```go
// The architect body -- everything after the frontmatter -- is one text
// shipped three times. #188 added the Handing off section that makes the
// planner reach for relay; this pins both the section and the identity, so
// an edit to one kind cannot drift from the others.
func TestArchitectHandoffIsSharedAcrossKinds(t *testing.T) {
	bodies := map[string]string{}
	for _, h := range All() {
		doc, err := AgentDoc("architect", h.Kind)
		if err != nil {
			t.Fatalf("AgentDoc(architect, %s): %v", h.Kind, err)
		}
		body := definitionBody(t, h.Kind, string(doc))
		for _, want := range []string{"## Handing off", "relay send", "--headless", "relay unavailable"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s architect body lacks %q", h.Kind, want)
			}
		}
		for _, banned := range []string{"Agent tool", "slash command"} {
			if strings.Contains(body, banned) {
				t.Errorf("%s architect body is not harness-neutral: contains %q", h.Kind, banned)
			}
		}
		bodies[h.Kind] = body
	}
	ref := bodies["claude"]
	for kind, body := range bodies {
		if body != ref {
			t.Errorf("%s architect body differs from claude's:\n%s", kind, firstDifferingLine(ref, body))
		}
	}
}

// definitionBody returns the text after the closing --- of the frontmatter.
func definitionBody(t *testing.T, kind, doc string) string {
	t.Helper()
	parts := strings.SplitN(doc, "\n---\n", 2)
	if len(parts) != 2 || !strings.HasPrefix(doc, "---\n") {
		t.Fatalf("%s architect definition has no frontmatter fence", kind)
	}
	return parts[1]
}

func firstDifferingLine(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return fmt.Sprintf("line %d:\n  claude: %q\n  other:  %q", i+1, al[i], bl[i])
		}
	}
	return fmt.Sprintf("lengths differ: %d vs %d lines", len(al), len(bl))
}
```

If `fmt` is not already imported in `harness_test.go`, add it. If a helper
with either name already exists in the package's tests, stop and report
rather than rename.

`doc` begins with `---\n` and the frontmatter closes with `\n---\n`, so
`SplitN(doc, "\n---\n", 2)` splits on the *closing* fence only: the opening
fence has no preceding newline. Confirm this by reading the first line of
each file; if any file starts differently, stop.

- [ ] **Step 2: run it.** `go test -race -count=1 ./internal/harness/
-run TestArchitect -v`. Both architect tests pass.

- [ ] **Step 3: mutation-test it, then revert by re-editing (never with
`git checkout`).**
  1. Delete the `## Handing off` heading line from `architect.agy.md` only.
     Run the test. It must fail naming `agy` for `"## Handing off"` *and*
     for differing from claude. Re-add the line.
  2. Change one word in the section in `architect.opencode.md` only. It
     must fail with `opencode architect body differs from claude's` and a
     `line N:` pair. Restore the word.
  3. Re-run: green.

- [ ] **Step 4: full check** (the four commands under Running commands),
then commit:

```
feat(agent): architect definition hands off through relay -- relay send, headless by default, one harness many worktrees (#188)
```

### Task 3: one source for the protocol -- trim CLAUDE.md, land the plan

**Files:**
- Modify: `CLAUDE.md`
- Add: `docs/plans/2026-09-18-architect-handoff.md`

**Depends on:** Task 2 committed.

- [ ] **Step 1: replace the two sections.** In `CLAUDE.md`, the section
`## Dispatching work to builders` and the section `## Working with builders`
(everything from the first heading up to, not including,
`## Verifying a builder's work`) are replaced by this single section:

````markdown
## Working with builders

The dispatch protocol -- `relay send` not in-session subagents, headless by
default, one harness many worktrees, `relay unavailable` on a usage limit,
stop rather than improvise -- is in the shipped `architect` definition
(`internal/harness/agents/architect.*.md`, "Handing off"), not here. What
follows is what is specific to this machine and this repo.

- Candidates are in `~/.config/relay/candidates.json` (`relay candidates`
  lists them); the builder order is `order.builder` in
  `~/.config/relay/policy.json`. `relay policy` shows the current pick.
- relay closes a pane in exactly two places: `relay reap` (a terminal
  consult pane it spawned) and a mid-round builder switch (the replaced
  builder's pane, when it is still open). It stops a *process* in exactly
  three: `relay done` and `relay unbind` on a headless binding whose
  round is running, and a mid-round switch of a headless builder whose
  provider you gated with `relay unavailable`. After an `unbind`, a
  mis-bind, or any `--assume-dead` rebind of a pane builder, close the
  orphaned builder pane yourself with `herdr pane close <id>` or it holds
  memory indefinitely (an idle opencode builder is roughly 800 MB).
- `relay done` releases a clean worktree (the branch survives) so you can
  `gh pr checkout` in the main repo without `gc`; a dirty tree or an open
  pane round is kept and `gc` retries. `relay bind --resume` restores a
  released worktree; rebind a DONE binding only after that restore.
- A headless round's log is at `~/.local/state/relay/<name>/NNN-builder.log`.
- When a builder reports a usage limit mid-round, `relay unavailable
  <token>` is enough: the daemon switches and resends. Do not rebind by
  hand unless `relay status` says `NEEDS YOU`.
````

The sections `## Verifying a builder's work`, `## Conventions` and
`## Merging and CI`, and the two-paragraph intro above the first heading,
are untouched.

- [ ] **Step 2: land the plan file.** Copy the plan relay delivered
(`~/.local/state/relay/handoff/001-plan.md`) to
`docs/plans/2026-09-18-architect-handoff.md` in the worktree, verbatim.

- [ ] **Step 3: verify.** `git diff --stat HEAD~1` shows `CLAUDE.md` with
roughly `-53 +30` and the new plan file. `grep -c 'Handing off' CLAUDE.md`
is 1. Full check again (markdown-only, but run it). Commit:

```
docs: CLAUDE.md points at the architect definition for the dispatch protocol; land the #188 plan
```

## Report

End with:

- the two commit hashes and `git diff --stat main..HEAD`;
- the output of the Task 1 Step 1 loop *after* the edit (three equal sums);
- the exact failure lines from both mutations in Task 2 Step 3;
- the four check commands, verbatim, with PASS/FAIL each;
- any deviation, or "no deviations".

Not in this round: the README, the plugin skill (#123), any change to
`relay agent install` or to the builder-side roles, and reinstalling the
definition on this machine (the human runs `relay agent install` after the
merge).
