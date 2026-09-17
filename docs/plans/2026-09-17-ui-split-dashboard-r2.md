# `relay ui` split dashboard, round 2: three test corrections (#180)

> **For agentic workers:** execute the tasks in order; each ends green and
> commits. Steps use checkbox (`- [ ]`) syntax for tracking.

**Design spec:** `docs/specs/2026-09-17-relay-ui-split-design.md`
**Round 1 plan:** `docs/plans/2026-09-17-ui-split-dashboard.md`
**Issue:** #180

Round 1 landed all six tasks. Its report (kept in the drop directory)
identified three places where the plan's own tests were wrong and the
implementation was right; the planner has verified each. This round fixes
the tests. **No non-test file changes.**

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/ui-split` | **the git worktree. Every source edit goes here.** It is your shell's cwd, on branch `relay/ui-split`. |
| `~/.local/state/relay/ui-split` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find
in the code, **stop and say so in your report**.

## Running commands

`make` is intercepted on this machine; run the constituents directly:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do not run `herdr` or any harness. No test under `cmd/relay`.

## Global constraints

- Only `internal/ui/split_test.go` and `internal/ui/layout_test.go`
  change. If making a test pass seems to need a change elsewhere, stop.
- One commit for the whole round, `test(ui): ...`.

---

### Task 1: `TestPaneFollowsCursor` -- order the stale reply after the in-flight move

**Files:** `internal/ui/split_test.go`

Round 1's finding: a `tabMsg` clears `tabInFlight` unconditionally before
its name check (pinned by `TestTabMsgMismatchedBindingDiscarded`), so
delivering the stale reply *before* the second move released the guard the
test then expected to be held. The fetch for the previous binding is done
when its reply arrives, so clearing the guard is correct; the test's
ordering was not.

- [ ] **Step 1:** In `TestPaneFollowsCursor`, move the "With a fetch in
flight, a second move issues none" block (the second `j` and its two
assertions) **above** the "A stale reply for webshop is discarded" block,
and change the stale-reply assertion to also check the guard was released:

```go
	// With a fetch in flight, a second move issues none.
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	if cmd != nil {
		t.Error("a move while tabInFlight must not issue a second fetch")
	}
	if m.detail.name != "docs" {
		t.Errorf("pane must still re-point: %q", m.detail.name)
	}
	// The reply for the binding the cursor left arrives: discarded, and
	// the guard is released so the next tick can fetch for docs.
	res, _ = m.Update(tabMsg{name: "webshop", round: 3, t: tabDiff, content: tabContent{loaded: true, body: "old"}})
	m = res.(Model)
	if m.detail.cache[tabDiff].loaded {
		t.Error("stale tabMsg for a previous binding must be discarded")
	}
	if m.tabInFlight {
		t.Error("a stale reply still completes the fetch; the guard must be released")
	}
```

- [ ] **Step 2:** `go test ./internal/ui -run TestPaneFollowsCursor` -- PASS.

- [ ] **Step 3: Mutation check.** Temporarily remove
`if m.tabInFlight { return m, nil }` from `pointDetailAt`: the test must
fail on "a move while tabInFlight must not issue a second fetch". Revert.
Record the outcome.

---

### Task 2: `TestFooterNoticesAndRefreshAge` -- squeeze at a split width

**Files:** `internal/ui/split_test.go`

Round 1's finding: width 60 is stack layout on the list screen, where
`paneVisible()` is false and the other-binding notice is (correctly) not
produced -- the rail shows the state itself. The squeeze rule must be
tested where the notice exists: the narrowest split width.

- [ ] **Step 1:** Replace the last block of the test:

```go
	// The right side wins when they would overlap: at the narrowest split
	// width the six keys plus the notice do not fit on one line.
	m.width = splitMinWidth
	f = stripANSI(m.footerView())
	if lipgloss.Width(f) > splitMinWidth {
		t.Errorf("footer wider than the terminal: %q", f)
	}
	if !strings.Contains(f, "webshop NEEDS YOU") || !strings.Contains(f, "refreshed 2s ago") {
		t.Errorf("the notice and the age must survive the squeeze: %q", f)
	}
	if strings.Contains(f, "q quit") {
		t.Errorf("the key list must be the side that gives way: %q", f)
	}
```

If the last assertion fails because the keys still fit at 110 columns,
measure: `lipgloss.Width` of the left side plus 1 plus the right side must
exceed 110 for the squeeze to fire. If it does not, report the two widths
and stop -- do not lower the width below `splitMinWidth`.

- [ ] **Step 2:** `go test ./internal/ui -run TestFooterNoticesAndRefreshAge` -- PASS.

---

### Task 3: `TestLayoutThreshold` -- pin the number, not the symbol

**Files:** `internal/ui/layout_test.go`

Round 1's finding: the test derived its inputs from `splitMinWidth`, so
changing the constant could not fail it. Spec §3.1 says 110; the test
must say 110.

- [ ] **Step 1:** Replace the body:

```go
// TestLayoutThreshold pins spec §3.1's number with literals on purpose:
// a test that reads splitMinWidth cannot notice it changing.
func TestLayoutThreshold(t *testing.T) {
	m := Model{height: 40}
	m.width = 109
	if m.layout() != layoutStack {
		t.Errorf("109 columns: want stack")
	}
	m.width = 110
	if m.layout() != layoutSplit {
		t.Errorf("110 columns: want split")
	}
}
```

- [ ] **Step 2:** `go test ./internal/ui -run TestLayoutThreshold` -- PASS.

- [ ] **Step 3: Mutation check.** Set `splitMinWidth = 111` temporarily:
the test must fail on "110 columns: want split". Revert. Record the
outcome.

---

- [ ] **Finish:** all four `make check` constituents green, tree clean.
Commit:

```
test(ui): correct three round-1 assertions the implementation was right about (#180)
```

## Report

`NNN-report.md`: the constituents' tails, the two mutation outcomes, and
confirmation that only the two test files changed (`git diff --stat
HEAD~1`).
