# Sub-agent coverage row in `relay status` (#84)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #84. **Spec:** `docs/specs/2026-09-12-subagent-coverage-design.md`
-- read it whole, it is short. **Depends on:** nothing open.

**Goal:** `relay status` prints a `coverage` row for any binding whose builder
harness cannot show sub-agents as separate herdr agents, so "no foreign rows"
on agy or opencode reads as unverified rather than clear; and the per-harness
sub-agent visibility, with the observation behind each value, lives in one
place in the repo (`internal/harness/harness.go`).

**Architecture:** A `SubAgentVisibility` tri-state on `harness.Harness`
(`separate` / `foreground` / `hidden`). A pure `SubAgentCoverage(kind, vis)`
in `internal/relay/coverage.go` turns it into row text or nothing.
`statusRow` copies the harness value into `BindingStatus.SubAgents`;
`RenderStatus` prints the row after `foreign` rows and before `detail`.
Nothing else changes: `ForeignAgents`, doctor, the TUI, and the ledger are
untouched.

**Tech stack:** Go, `make check`.

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

**Do not run `relay`, `herdr`, `agy`, or `opencode` for any reason.** `relay`
is on your PATH and its subcommands reach herdr. Live verification is the
planner's, after merge. The only commands you need are `go`, `gofmt`, `grep`,
`git`. CI runners have no `herdr` binary: every test in this plan is a pure
function test in `internal/harness` or `internal/relay`.

## Global constraints

- The three constants are exactly `"separate"`, `"foreground"`, `"hidden"`.
  These are the JSON values scripts will branch on.
- The zero value `""` of `SubAgentVisibility` is not a state. Every entry in
  `knownHarnesses` sets the field. Only an unknown kind (`Lookup` fails)
  yields `""` downstream.
- `SubAgentCoverage` returns `("", false)` for `separate` and `(text, true)`
  for everything else including `""`. Row texts are copied verbatim from
  Task 2; do not reword them.
- The row label is `coverage`, printed with the same `"  %-8s "` column as
  `planner`, `builder`, `foreign`, `detail`. A claude binding's rendered
  output is byte-for-byte unchanged.
- `ForeignAgents` and `foreign.go` are not modified.
- One commit per task.

---

### Task 1: `SubAgentVisibility` on the harness table

**Files:**
- Modify: `internal/harness/harness.go` (type + constants after `RoleSpec`;
  field on `Harness`; value on each of the three `knownHarnesses` entries)
- Modify: `internal/harness/harness_test.go` (`TestTableExactValues`
  expected map gains the field; new `TestSubAgentsSetOnEveryKind`)

**Interfaces:**
- Produces: `type SubAgentVisibility string`; constants
  `SubAgentsSeparate`, `SubAgentsForeground`, `SubAgentsHidden`; field
  `Harness.SubAgents SubAgentVisibility`. Task 2 and Task 3 consume these.

- [ ] **Step 1: Extend the exact-values test and add the every-kind test**

In `internal/harness/harness_test.go`, inside `TestTableExactValues`, add
`SubAgents:` to each of the three expected entries, directly after
`Integration:` (and after `MinVersion:` on agy):

```go
		"agy": {
			Kind:        "agy",
			Binary:      "agy",
			Integration: "antigravity-cli",
			MinVersion:  "1.1.6",
			SubAgents:   SubAgentsForeground,
			Roles: []Role{
```

```go
		"claude": {
			Kind:        "claude",
			Binary:      "claude",
			Integration: "claude",
			SubAgents:   SubAgentsSeparate,
			Roles: []Role{
```

```go
		"opencode": {
			Kind:        "opencode",
			Binary:      "opencode",
			Integration: "opencode",
			SubAgents:   SubAgentsHidden,
			Roles: []Role{
```

Then append this test at the end of the file:

```go
// TestSubAgentsSetOnEveryKind pins the rule that "" is not a visibility
// state: an unknown kind yields "" downstream, and a known kind never may.
func TestSubAgentsSetOnEveryKind(t *testing.T) {
	valid := map[SubAgentVisibility]bool{
		SubAgentsSeparate:   true,
		SubAgentsForeground: true,
		SubAgentsHidden:     true,
	}
	for _, h := range All() {
		if !valid[h.SubAgents] {
			t.Errorf("harness %q: SubAgents = %q, want one of separate/foreground/hidden", h.Kind, h.SubAgents)
		}
	}
}
```

- [ ] **Step 2: Run the harness tests to verify they fail to compile**

Run: `go test -count=1 ./internal/harness/`
Expected: FAIL -- `undefined: SubAgentsForeground` (and the sibling names).

- [ ] **Step 3: Add the type, constants, field, and per-kind values**

In `internal/harness/harness.go`, after the `RoleSpec` type (before
`roleTable`), add:

```go
// SubAgentVisibility records what `herdr agent list` shows while a builder
// of this kind is running a sub-agent. It is a property of the harness and
// herdr's integration for it, observed live; the observation behind each
// value is recorded on the knownHarnesses entry that carries it. relay
// reports what herdr reports, and this says how much that covers.
type SubAgentVisibility string

const (
	// SubAgentsSeparate: a sub-agent is a separate herdr agent in its own
	// pane. ForeignAgents reports it as a foreign row.
	SubAgentsSeparate SubAgentVisibility = "separate"
	// SubAgentsForeground: a sub-agent takes over the builder pane's session
	// slot. herdr lists no extra agent; relay's name match keeps the builder
	// known (#66) but has nothing to report for the sub-agent.
	SubAgentsForeground SubAgentVisibility = "foreground"
	// SubAgentsHidden: a sub-agent runs inside the builder's process and
	// herdr lists only the pane. Nothing observable.
	SubAgentsHidden SubAgentVisibility = "hidden"
)
```

On the `Harness` struct, after `MinVersion`, add:

```go
	// SubAgents is what herdr shows for this kind's sub-agents. Never "" on
	// a known kind; TestSubAgentsSetOnEveryKind enforces it. The status layer
	// prints a coverage row for anything but SubAgentsSeparate.
	SubAgents SubAgentVisibility
```

On each `knownHarnesses` entry, add the field with its observation comment.
These comments are the record #84's acceptance criterion asks for; keep the
dates and versions exactly.

agy, after `MinVersion:  "1.1.6",`:

```go
		// Observed 2026-09-11, herdr 0.9.0, antigravity-cli integration (#66):
		// while a builder waits on a sub-agent, `herdr agent list` returns the
		// sub-agent's session id on the builder's pane and no extra agent.
		// The integration reports whichever agy session is in the foreground.
		SubAgents: SubAgentsForeground,
```

claude, after `Integration: "claude",`:

```go
		// Observed 2026-09-10, herdr 0.9.0, claude integration: a researcher
		// dispatched by a plan-executor surfaced as its own herdr pane and was
		// reported as a foreign row (foreign-agent spec §7.2).
		SubAgents: SubAgentsSeparate,
```

opencode, after `Integration: "opencode",`:

```go
		// Observed 2026-09-10, herdr 0.9.0, opencode integration v11: a
		// researcher dispatched by a plan-executor rendered as a card inside
		// the builder's TUI; `herdr agent list` showed no extra agent. Stable
		// by design: the integration (herdr-agent-state.js) tracks child
		// sessions by parentID and folds them into the pane's root session so
		// they "cannot replace the pane's root session"; only a child's
		// permission/question prompt bubbles up, as the root's blocked state.
		SubAgents: SubAgentsHidden,
```

- [ ] **Step 4: Run the harness tests to verify they pass**

Run: `go test -count=1 ./internal/harness/`
Expected: PASS.

- [ ] **Step 5: Mutation check**

Temporarily delete the `SubAgents: SubAgentsHidden,` line from the opencode
entry. Run `go test -count=1 ./internal/harness/`. Expected: FAIL in both
`TestTableExactValues` (`Lookup("opencode")` mismatch) and
`TestSubAgentsSetOnEveryKind` (`harness "opencode": SubAgents = ""`). Restore
the line; re-run; PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l internal/harness/ ; go vet ./internal/harness/
git add internal/harness/harness.go internal/harness/harness_test.go
git commit -m "feat(harness): record per-kind sub-agent visibility with its observation (#84)"
```

---

### Task 2: `SubAgentCoverage` -- row text from visibility

**Files:**
- Create: `internal/relay/coverage.go`
- Create: `internal/relay/coverage_test.go`

**Interfaces:**
- Consumes: `harness.SubAgentVisibility` and the three constants (Task 1).
- Produces: `func SubAgentCoverage(kind string, vis harness.SubAgentVisibility) (string, bool)`.
  Task 3 consumes it.

- [ ] **Step 1: Write the failing test**

Create `internal/relay/coverage_test.go`:

```go
package relay

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/harness"
)

func TestSubAgentCoverage(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		vis     harness.SubAgentVisibility
		printed bool
		want    string
	}{
		{
			name: "separate prints nothing", kind: "claude", vis: harness.SubAgentsSeparate,
			printed: false, want: "",
		},
		{
			name: "foreground", kind: "agy", vis: harness.SubAgentsForeground,
			printed: true,
			want:    "sub-agents hidden: agy runs them in the builder pane; no foreign rows above does not mean the tree is clear",
		},
		{
			name: "hidden", kind: "opencode", vis: harness.SubAgentsHidden,
			printed: true,
			want:    "sub-agents hidden: opencode runs them in-process, herdr lists only the pane; no foreign rows above does not mean the tree is clear",
		},
		{
			name: "unknown kind is unverified", kind: "gemini", vis: "",
			printed: true,
			want:    `sub-agents unverified for kind "gemini"; no foreign rows above does not mean the tree is clear`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SubAgentCoverage(tc.kind, tc.vis)
			if ok != tc.printed {
				t.Fatalf("printed = %v, want %v (text %q)", ok, tc.printed, got)
			}
			if got != tc.want {
				t.Errorf("text =\n  %q\nwant\n  %q", got, tc.want)
			}
			if tc.printed && !strings.Contains(got, tc.kind) {
				t.Errorf("text must name the kind %q: %q", tc.kind, got)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 ./internal/relay/ -run TestSubAgentCoverage`
Expected: FAIL -- `undefined: SubAgentCoverage`.

- [ ] **Step 3: Implement**

Create `internal/relay/coverage.go`:

```go
package relay

import (
	"fmt"

	"github.com/fuad-daoud/relay/internal/harness"
)

// SubAgentCoverage returns the `coverage` row for a builder of the given
// kind, and false when no row is warranted.
//
// The row says what an empty foreign list proves. It is a fact about the
// harness (harness.Harness.SubAgents), not about the current round, so it
// prints whether or not the builder is doing anything: making it conditional
// on activity would need relay to see activity it cannot. Only
// SubAgentsSeparate prints nothing, because there herdr lists sub-agents and
// ForeignAgents reports them. An unknown kind ("" from a failed Lookup) is
// reported as unverified rather than assumed either way.
func SubAgentCoverage(kind string, vis harness.SubAgentVisibility) (string, bool) {
	const tail = "; no foreign rows above does not mean the tree is clear"
	switch vis {
	case harness.SubAgentsSeparate:
		return "", false
	case harness.SubAgentsForeground:
		return fmt.Sprintf("sub-agents hidden: %s runs them in the builder pane%s", kind, tail), true
	case harness.SubAgentsHidden:
		return fmt.Sprintf("sub-agents hidden: %s runs them in-process, herdr lists only the pane%s", kind, tail), true
	default:
		return fmt.Sprintf("sub-agents unverified for kind %q%s", kind, tail), true
	}
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test -count=1 ./internal/relay/ -run TestSubAgentCoverage`
Expected: PASS, four subtests.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal/relay/ ; go vet ./internal/relay/
git add internal/relay/coverage.go internal/relay/coverage_test.go
git commit -m "feat(relay): SubAgentCoverage turns harness visibility into a status row (#84)"
```

---

### Task 3: `BindingStatus.SubAgents` and the rendered row

**Files:**
- Modify: `internal/relay/status.go` -- imports; `BindingStatus` struct
  (after `Foreign`); `statusRow` (next to `row.Foreign = ...`);
  `RenderStatus` (after the `for _, fa := range b.Foreign` loop, before the
  `if b.Detail != ""` block)
- Modify: `internal/relay/status_test.go` -- three new tests appended

**Interfaces:**
- Consumes: `harness.Lookup(kind) (harness.Harness, bool)`,
  `harness.Harness.SubAgents` (Task 1); `SubAgentCoverage` (Task 2).
- Produces: `BindingStatus.SubAgents string` with JSON key `sub_agents`.

- [ ] **Step 1: Write the failing render tests**

Append to `internal/relay/status_test.go`:

```go
func TestRenderStatusCoverageRowPerKind(t *testing.T) {
	base := func(kind, vis string) BindingStatus {
		return BindingStatus{
			Name: "b", CWD: "/repo", Round: 1, Display: "ACTIVE",
			PlannerPane: "wM:p1", PlannerKind: "claude", PlannerStatus: "idle",
			BuilderPane: "wM:p2", BuilderKind: kind, BuilderStatus: "working",
			BuilderCandidate: testAgyRef,
			SubAgents:        vis,
			Foreign: []ForeignAgent{{
				PaneID: "wM:p9", Kind: "claude", Status: "working", CWD: "/repo", Title: "researcher",
			}},
			Detail: "something to say",
		}
	}

	t.Run("claude prints no coverage row", func(t *testing.T) {
		out := RenderStatus(Report{Bindings: []BindingStatus{base("claude", "separate")}})
		if strings.Contains(out, "coverage") {
			t.Errorf("claude binding must not print a coverage row:\n%s", out)
		}
	})

	for _, tc := range []struct{ kind, vis, want string }{
		{"agy", "foreground", "  coverage sub-agents hidden: agy runs them in the builder pane; no foreign rows above does not mean the tree is clear\n"},
		{"opencode", "hidden", "  coverage sub-agents hidden: opencode runs them in-process, herdr lists only the pane; no foreign rows above does not mean the tree is clear\n"},
		{"gemini", "", "  coverage sub-agents unverified for kind \"gemini\"; no foreign rows above does not mean the tree is clear\n"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			out := RenderStatus(Report{Bindings: []BindingStatus{base(tc.kind, tc.vis)}})
			if !strings.Contains(out, tc.want) {
				t.Fatalf("coverage row missing or reworded; want %q in:\n%s", tc.want, out)
			}
			// It sits after the last foreign row and before detail: the
			// reader has just scanned the foreign rows and this is the
			// sentence about what that scan proved.
			foreignAt := strings.Index(out, "  foreign ")
			coverageAt := strings.Index(out, "  coverage ")
			detailAt := strings.Index(out, "  detail ")
			if !(foreignAt < coverageAt && coverageAt < detailAt) {
				t.Errorf("coverage must follow foreign and precede detail, got:\n%s", out)
			}
		})
	}
}

func TestStatusRowCopiesHarnessSubAgents(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		{"claude", "separate"},
		{"agy", "foreground"},
		{"opencode", "hidden"},
		{"gemini", ""},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			f := &fakeHerdr{}
			rt, b := sentBinding(t, f)
			b.Builder.Kind = tc.kind
			if err := rt.Store.Save(b); err != nil {
				t.Fatal(err)
			}
			rep, err := Status(context.Background(), rt)
			if err != nil {
				t.Fatal(err)
			}
			if got := rep.Bindings[0].SubAgents; got != tc.want {
				t.Errorf("SubAgents = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBindingStatusSubAgentsJSON(t *testing.T) {
	withValue, _ := json.Marshal(BindingStatus{Name: "b", SubAgents: "hidden"})
	if !strings.Contains(string(withValue), `"sub_agents":"hidden"`) {
		t.Errorf("sub_agents missing from %s", withValue)
	}
	empty, _ := json.Marshal(BindingStatus{Name: "b"})
	if strings.Contains(string(empty), "sub_agents") {
		t.Errorf("empty SubAgents must be omitted, got %s", empty)
	}
}
```

If `status_test.go` does not already import `encoding/json`, add it. Check
how `sentBinding` returns the binding and how the test suite persists a
modified binding (`rt.Store.Save` is the name used elsewhere in this file for
that; if the helper in this file is named differently, use that name and say
so in your report -- do not add a new helper). If `sentBinding` sets the
builder's kind in a way that the fake herdr's agent list must match for the
builder to be found, mirror the kind on the fake agent too; the assertion is
on `SubAgents`, which does not depend on whether the builder is found.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/relay/ -run 'TestRenderStatusCoverageRowPerKind|TestStatusRowCopiesHarnessSubAgents|TestBindingStatusSubAgentsJSON'`
Expected: FAIL to compile -- `unknown field SubAgents in struct literal`.

- [ ] **Step 3: Add the field, populate it, render it**

In `internal/relay/status.go`, add to the imports:

```go
	"github.com/fuad-daoud/relay/internal/harness"
```

In `BindingStatus`, directly after the `Foreign` field:

```go
	// SubAgents is the builder harness's sub-agent visibility
	// (harness.Harness.SubAgents): "separate", "foreground", or "hidden".
	// Empty when the builder kind is not in the harness table. It is the
	// fact, not the rendered coverage row, so the TUI or a script can branch
	// on it. Deliberately does not affect Display.
	SubAgents string `json:"sub_agents,omitempty"`
```

In `statusRow`, directly after `row.Foreign = ForeignAgents(agents, known, b.CWD)`:

```go
	if h, ok := harness.Lookup(b.Builder.Kind); ok {
		row.SubAgents = string(h.SubAgents)
	}
```

In `RenderStatus`, directly after the `for _, fa := range b.Foreign { ... }`
loop and before `if b.Detail != "" {`:

```go
		if note, ok := SubAgentCoverage(b.BuilderKind, harness.SubAgentVisibility(b.SubAgents)); ok {
			fmt.Fprintf(&sb, "  %-8s %s\n", "coverage", note)
		}
```

- [ ] **Step 4: Run the three tests, then the whole package**

Run: `go test -count=1 ./internal/relay/ -run 'TestRenderStatusCoverageRowPerKind|TestStatusRowCopiesHarnessSubAgents|TestBindingStatusSubAgentsJSON'`
Expected: PASS.

Run: `go test -count=1 ./internal/relay/`
Expected: PASS. If an existing render test breaks because it asserts exact
whole-output text for an agy or opencode binding, that test now needs the
coverage line in its expected text -- add the line verbatim from Task 2's
table; do not weaken the assertion. If an existing test breaks for any
other reason, stop and report.

- [ ] **Step 5: Mutation check**

Temporarily change the `SubAgentsForeground` case in `SubAgentCoverage` to
`return "", false`. Run `go test -count=1 ./internal/relay/`. Expected:
`TestSubAgentCoverage/foreground` and
`TestRenderStatusCoverageRowPerKind/agy` fail. Restore; PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l internal/relay/ ; go vet ./internal/relay/
git add internal/relay/status.go internal/relay/status_test.go
git commit -m "feat(status): coverage row says what no foreign rows proves per builder kind (#84)"
```

---

### Task 4: Docs -- spec §7.2 points at the table; design.md names the row

**Files:**
- Modify: `docs/specs/2026-09-10-foreign-agent-detection-design.md` §7.2
  (the section starting `### 7.2 Sanctioned read-only panes still show as foreign`, up to `### 7.3`)
- Modify: `docs/design.md` -- the foreign-rows paragraph (the one beginning
  `What it can do is say so.`, around line 326)

**Interfaces:** none; prose only.

- [ ] **Step 1: Amend §7.2 of the foreign-agent spec**

Keep the first two paragraphs of §7.2 (the one beginning "After the
researcher role ships" and the one beginning "Suppressing by title"). Replace
everything from the paragraph beginning `**Whether it surfaces at all is
harness-specific.**` through the paragraph ending `...drawing a false
conclusion from silence.` (i.e. the rest of §7.2, up to but not including
`### 7.3`) with:

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

Run: `make check` (or its constituents, see "Running commands").
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add docs/specs/2026-09-10-foreign-agent-detection-design.md docs/design.md
git commit -m "docs: foreign-agent spec §7.2 points at the harness table; design.md names the coverage row (#84)"
```

---

## Report

Say which tests you added, that both mutation checks failed the named tests
and passed after restore, and the `make check` result. If Task 3 Step 1
needed a different persistence helper than `rt.Store.Save`, name what you
used. Compare `git diff --stat` against the file list above and call out
anything outside it.
