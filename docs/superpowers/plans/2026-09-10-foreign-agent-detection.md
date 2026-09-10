# Foreign-Agent Detection and the Researcher Role — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Report agents occupying a bound working tree that no binding accounts for, and stop relay's own shipped role text from causing them.

**Architecture:** Detection is a pure filter over the agent list `relay.Status` already fetches — no new herdr call, no new error path, no daemon state. Separately, `internal/harness` grows plural roles so relay can ship a second, read-only `researcher` definition for the builder's sub-agents to run as, and the `plan-executor` definitions stop telling builders to dispatch sub-agents that write.

**Tech Stack:** Go 1.x, standard library only. No new dependencies — `go mod tidy` must stay clean (`make check` enforces it).

**Spec:** `docs/specs/2026-09-10-foreign-agent-detection-design.md`

## Global Constraints

- **Verification command is `make check`, not `go test ./...`.** It adds `gofmt -l .` over the whole tree, `go vet ./...`, a `go mod tidy` check, `sh scripts/check-plugin-version.sh`, shellcheck, and `scripts/*_test.sh`. A task is not done until `make check` is clean.
- **No new module dependencies.** Standard library only.
- **Vocabulary: "foreign agent", never "foreign writer".** relay cannot observe writes; `herdr agent list` reports kind, status, cwd and title only. This wording rule applies to code comments, doc strings, rendered output, and docs.
- **relay makes no judgements.** Nothing added here may change a binding's `State` or `Display`, filter on a pane title, or infer intent.
- **Existing human-readable output must not change** except where a task explicitly says so. `TestStatusJSONForkProvenance` asserts `RenderStatus` output is byte-identical across a change; treat that as the standing bar.
- **Commit after every task.** Message bodies end with:
  ```
  Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1
  ```
- **If a step is impossible as written, or conflicts with existing code, STOP and report.** Do not improvise. A halt that surfaces a design error is worth more than a green suite that bent a test to fit. This applies with special force to Task 9.

## Task Dependency Graph

```
Task 1 ──> Task 2                     (detection; internal/relay)
Task 3 ──┬─> Task 4                   (agent print --role)
         ├─> Task 5 ──> Task 6        (doctor Env.ReadFile, then role rows)
         └─> Task 7 ──> Task 8 ──> Task 9 ──> Task 10
```

Tasks 1–2 and Tasks 3–9 touch **disjoint files** and may run as two parallel
builders in separate git worktrees. Task 10 (docs) needs both.

---

### Task 1: Foreign-agent detection primitives

**Files:**
- Create: `internal/relay/foreign.go`
- Create: `internal/relay/foreign_test.go`

**Interfaces:**
- Consumes: `herdr.Agent` (fields `Kind`, `Status`, `CWD`, `PaneID`, `Title`) from `internal/herdr/types.go`; `store.Endpoint` and `store.Binding` from `internal/store/types.go`; the existing `relay.SameAgent(a herdr.Agent, ep store.Endpoint) bool` from `internal/relay/herdr.go`.
- Produces:
  - `type ForeignAgent struct { PaneID, Kind, Status, CWD, Title string }`
  - `func knownEndpoints(bindings []store.Binding) []store.Endpoint`
  - `func ForeignAgents(agents []herdr.Agent, known []store.Endpoint, tree string) []ForeignAgent`
  - `func withinTree(tree, cwd string) bool`

**Context you need:** `SameAgent` already encodes relay's whole identity story — it prefers a session id when the endpoint has one, falls back to pane+kind, and finally to pane alone for older bindings. Do **not** write a new comparison; call it. Getting this wrong means a builder whose pane moved gets reported as foreign.

- [ ] **Step 1: Write the failing test for `withinTree`**

Create `internal/relay/foreign_test.go`:

```go
package relay

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestWithinTree(t *testing.T) {
	tests := []struct {
		name      string
		tree, cwd string
		want      bool
	}{
		{name: "exact match", tree: "/repo", cwd: "/repo", want: true},
		{name: "nested one level", tree: "/repo", cwd: "/repo/internal", want: true},
		{name: "nested deep", tree: "/repo", cwd: "/repo/internal/ui/x", want: true},
		{name: "sibling sharing a prefix", tree: "/repo", cwd: "/repo-doctor", want: false},
		{name: "sibling sharing a longer prefix", tree: "/foo", cwd: "/foo-bar/baz", want: false},
		{name: "trailing slash on tree", tree: "/repo/", cwd: "/repo/internal", want: true},
		{name: "trailing slash on cwd", tree: "/repo", cwd: "/repo/internal/", want: true},
		{name: "parent directory is not inside", tree: "/repo/worktrees/a", cwd: "/repo", want: false},
		{name: "unrelated", tree: "/repo", cwd: "/somewhere/else", want: false},
		{name: "empty tree", tree: "", cwd: "/repo", want: false},
		{name: "empty cwd", tree: "/repo", cwd: "", want: false},
		{name: "both empty", tree: "", cwd: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinTree(tc.tree, tc.cwd); got != tc.want {
				t.Errorf("withinTree(%q, %q) = %v, want %v", tc.tree, tc.cwd, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/relay/ -run TestWithinTree -v`
Expected: FAIL to build — `undefined: withinTree`.

- [ ] **Step 3: Implement `withinTree`**

Create `internal/relay/foreign.go`:

```go
package relay

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// withinTree reports whether cwd is tree itself or is nested under it.
//
// The separator guard is load-bearing: a plain HasPrefix would make
// /repo-doctor look like it is inside /repo, and relay would report a
// second binding's builder as foreign to the first.
//
// Paths are compared literally after Clean. A tree reached through a
// symlink under one name and reported by herdr under another will not
// match, and the agent goes unreported. Resolving that would mean hitting
// the filesystem for every agent on every status call; see the spec.
func withinTree(tree, cwd string) bool {
	if tree == "" || cwd == "" {
		return false
	}
	t := filepath.Clean(tree)
	c := filepath.Clean(cwd)
	if c == t {
		return true
	}
	return strings.HasPrefix(c, t+string(filepath.Separator))
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/relay/ -run TestWithinTree -v`
Expected: PASS, 12 subtests.

- [ ] **Step 5: Write the failing test for `knownEndpoints` and `ForeignAgents`**

Append to `internal/relay/foreign_test.go`:

```go
func TestKnownEndpointsCollectsBothSidesOfEveryBinding(t *testing.T) {
	bindings := []store.Binding{
		{Name: "a", Planner: store.Endpoint{PaneID: "w1:p1"}, Builder: store.Endpoint{PaneID: "w1:p2"}},
		{Name: "b", Planner: store.Endpoint{PaneID: "w1:p3"}, Builder: store.Endpoint{PaneID: "w1:p4"}},
	}
	got := knownEndpoints(bindings)
	if len(got) != 4 {
		t.Fatalf("got %d endpoints, want 4", len(got))
	}
	seen := map[string]bool{}
	for _, ep := range got {
		seen[ep.PaneID] = true
	}
	for _, want := range []string{"w1:p1", "w1:p2", "w1:p3", "w1:p4"} {
		if !seen[want] {
			t.Errorf("endpoint %q missing from %v", want, got)
		}
	}
}

func TestKnownEndpointsHandlesNoBindings(t *testing.T) {
	if got := knownEndpoints(nil); len(got) != 0 {
		t.Errorf("knownEndpoints(nil) = %v, want empty", got)
	}
}

// agentAt is a live agent in a directory, with no recorded session.
func agentAt(pane, kind, cwd string) herdr.Agent {
	return herdr.Agent{PaneID: pane, Kind: kind, Status: herdr.StatusIdle, CWD: cwd}
}

func TestForeignAgentsExcludesThisBindingsOwnEndpoints(t *testing.T) {
	binding := store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w1:p1", Kind: "claude"},
		Builder: store.Endpoint{PaneID: "w1:p2", Kind: "agy"},
	}
	agents := []herdr.Agent{
		agentAt("w1:p1", "claude", "/repo"),
		agentAt("w1:p2", "agy", "/repo"),
	}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{binding}), binding.CWD)
	if got != nil {
		t.Errorf("own planner and builder must never be foreign, got %+v", got)
	}
}

// This is the false positive the "referenced by no binding at all" rule
// exists to prevent: a second binding's planner legitimately sharing a tree.
func TestForeignAgentsExcludesAnotherBindingsPlanner(t *testing.T) {
	subject := store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w1:p1", Kind: "claude"},
		Builder: store.Endpoint{PaneID: "w1:p2", Kind: "agy"},
	}
	other := store.Binding{
		Name: "other", CWD: "/other",
		Planner: store.Endpoint{PaneID: "w1:p9", Kind: "claude"},
		Builder: store.Endpoint{PaneID: "w1:p8", Kind: "agy"},
	}
	agents := []herdr.Agent{
		agentAt("w1:p1", "claude", "/repo"),
		agentAt("w1:p2", "agy", "/repo"),
		agentAt("w1:p9", "claude", "/repo"), // other's planner, in subject's tree
	}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{subject, other}), subject.CWD)
	if got != nil {
		t.Errorf("an agent known to another binding must not be foreign, got %+v", got)
	}
}

func TestForeignAgentsReportsUnknownAgentInTree(t *testing.T) {
	binding := store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w1:p1", Kind: "claude"},
		Builder: store.Endpoint{PaneID: "w1:p2", Kind: "agy"},
	}
	stranger := herdr.Agent{
		PaneID: "w1:p7", Kind: "claude", Status: herdr.StatusIdle,
		CWD: "/repo", Title: "plan-executor",
	}
	agents := []herdr.Agent{agentAt("w1:p1", "claude", "/repo"), stranger}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{binding}), binding.CWD)
	if len(got) != 1 {
		t.Fatalf("got %d foreign agents, want 1: %+v", len(got), got)
	}
	want := ForeignAgent{
		PaneID: "w1:p7", Kind: "claude", Status: herdr.StatusIdle,
		CWD: "/repo", Title: "plan-executor",
	}
	if got[0] != want {
		t.Errorf("got %+v, want %+v", got[0], want)
	}
}

func TestForeignAgentsReportsNestedAgent(t *testing.T) {
	binding := store.Binding{Name: "webshop", CWD: "/repo"}
	agents := []herdr.Agent{agentAt("w1:p7", "opencode", "/repo/internal/ui")}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{binding}), binding.CWD)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1: %+v", len(got), got)
	}
	if got[0].CWD != "/repo/internal/ui" {
		t.Errorf("cwd = %q, want /repo/internal/ui", got[0].CWD)
	}
}

func TestForeignAgentsIgnoresAgentsOutsideTree(t *testing.T) {
	binding := store.Binding{Name: "webshop", CWD: "/repo"}
	agents := []herdr.Agent{
		agentAt("w1:p7", "claude", "/somewhere/else"),
		agentAt("w1:p8", "claude", "/repo-doctor"),
		agentAt("w1:p9", "claude", ""),
	}

	if got := ForeignAgents(agents, knownEndpoints([]store.Binding{binding}), binding.CWD); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

// Proves ForeignAgents uses SameAgent rather than reimplementing matching:
// the builder's pane id has changed, but its session id still identifies it.
func TestForeignAgentsMatchesByStoredSessionAcrossPaneMove(t *testing.T) {
	binding := store.Binding{
		Name: "webshop", CWD: "/repo",
		Builder: store.Endpoint{PaneID: "w1:p2", Kind: "agy", SessionID: "sess-abc"},
	}
	moved := herdr.Agent{
		PaneID: "w9:p99", Kind: "agy", Status: herdr.StatusWorking, CWD: "/repo",
		Session: herdr.Session{Value: "sess-abc"},
	}

	got := ForeignAgents([]herdr.Agent{moved}, knownEndpoints([]store.Binding{binding}), binding.CWD)
	if got != nil {
		t.Errorf("a builder identified by session must not be foreign after a pane move, got %+v", got)
	}
}

func TestForeignAgentsReturnsNilNotEmptySlice(t *testing.T) {
	got := ForeignAgents(nil, nil, "/repo")
	if got != nil {
		t.Errorf("got %#v, want nil so omitempty elides the JSON field", got)
	}
}

func TestForeignAgentsIgnoresEmptyTree(t *testing.T) {
	agents := []herdr.Agent{agentAt("w1:p7", "claude", "/repo")}
	if got := ForeignAgents(agents, nil, ""); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestForeignAgentsSortsByPaneID(t *testing.T) {
	binding := store.Binding{Name: "webshop", CWD: "/repo"}
	agents := []herdr.Agent{
		agentAt("w1:p9", "claude", "/repo"),
		agentAt("w1:p3", "agy", "/repo"),
		agentAt("w1:p6", "opencode", "/repo"),
	}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{binding}), binding.CWD)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].PaneID != "w1:p3" || got[1].PaneID != "w1:p6" || got[2].PaneID != "w1:p9" {
		t.Errorf("not sorted by pane id: %+v", got)
	}
}
```

- [ ] **Step 6: Run the tests to verify they fail**

Run: `go test ./internal/relay/ -run 'TestKnownEndpoints|TestForeignAgents' -v`
Expected: FAIL to build — `undefined: knownEndpoints`, `undefined: ForeignAgents`, `undefined: ForeignAgent`.

- [ ] **Step 7: Implement the type and both functions**

Append to `internal/relay/foreign.go`:

```go
// ForeignAgent is a live agent occupying a bound working tree that no binding
// accounts for.
//
// relay cannot observe writes -- `herdr agent list` reports a kind, a status,
// a cwd and a title, and nothing more -- so this type reports OCCUPANCY only.
// A sanctioned read-only researcher and a rogue implementer are identical from
// here. Every field is copied from herdr unmodified; nothing on this type is
// derived, and nothing branches on Title.
type ForeignAgent struct {
	PaneID string `json:"pane_id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	CWD    string `json:"cwd"`
	Title  string `json:"title,omitempty"`
}

// knownEndpoints returns every endpoint relay accounts for, across ALL
// bindings rather than just the one being examined.
//
// The breadth is the point. A second binding's planner may legitimately sit in
// this binding's tree, and a nested worktree's builder is inside its parent's
// path; both are known to relay and neither is foreign. Narrowing this to one
// binding's own two endpoints would report both as findings the user cannot
// act on.
func knownEndpoints(bindings []store.Binding) []store.Endpoint {
	eps := make([]store.Endpoint, 0, len(bindings)*2)
	for _, b := range bindings {
		eps = append(eps, b.Planner, b.Builder)
	}
	return eps
}

// ForeignAgents returns the agents inside tree that match no known endpoint,
// sorted by pane id. It returns nil rather than an empty slice so that
// BindingStatus.Foreign is elided by omitempty.
//
// Identity comparison goes through SameAgent, which is relay's single
// definition of "is this the agent I recorded". Reimplementing it here would
// drift: SameAgent prefers a durable session id, falls back to pane+kind, and
// finally to pane alone for bindings written before sessions were recorded.
func ForeignAgents(agents []herdr.Agent, known []store.Endpoint, tree string) []ForeignAgent {
	if tree == "" {
		return nil
	}

	var out []ForeignAgent
	for _, a := range agents {
		if a.CWD == "" || !withinTree(tree, a.CWD) {
			continue
		}
		if isKnownAgent(a, known) {
			continue
		}
		out = append(out, ForeignAgent{
			PaneID: a.PaneID,
			Kind:   a.Kind,
			Status: a.Status,
			CWD:    a.CWD,
			Title:  a.Title,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].PaneID < out[j].PaneID })
	return out
}

func isKnownAgent(a herdr.Agent, known []store.Endpoint) bool {
	for _, ep := range known {
		if SameAgent(a, ep) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/relay/ -run 'TestWithinTree|TestKnownEndpoints|TestForeignAgents' -v`
Expected: PASS, all subtests.

**If `TestForeignAgentsExcludesThisBindingsOwnEndpoints` fails** with the planner reported as foreign, check `SameAgent`: an endpoint with an empty `SessionID` and a set `Kind` requires BOTH pane and kind to match. The test fixture sets both; make sure you passed the agent and endpoint in that argument order.

- [ ] **Step 9: Run the full check**

Run: `make check`
Expected: clean. Nothing calls the new code yet, so no other package can regress.

- [ ] **Step 10: Commit**

```bash
git add internal/relay/foreign.go internal/relay/foreign_test.go
git commit -m "feat(relay): detect agents occupying a bound tree

ForeignAgents filters the live agent list down to agents inside a binding's
working tree that match no endpoint of any binding. Identity goes through
the existing SameAgent, so session-durable matching and pane moves behave as
they do everywhere else.

withinTree guards on the path separator: a plain prefix test would make
/repo-doctor look nested inside /repo.

Nothing calls this yet.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1"
```

---

### Task 2: Surface foreign agents in `relay status`

**Files:**
- Modify: `internal/relay/status.go` (`BindingStatus` struct ~line 19; `Status` ~line 67; `statusRow` ~line 95; `RenderStatus` ~line 163)
- Modify: `internal/relay/status_test.go`

**Interfaces:**
- Consumes: `ForeignAgent`, `knownEndpoints`, `ForeignAgents` from Task 1.
- Produces: `BindingStatus.Foreign []ForeignAgent` with JSON tag `foreign,omitempty`; `statusRow(rt Runtime, b store.Binding, agents []herdr.Agent, known []store.Endpoint) (BindingStatus, error)` — note the new fourth parameter.

**Context you need:** `Status` already calls `rt.Store.List()` and `rt.Herdr.ListAgents(ctx)`. Compute `known` **once**, before the row loop — not inside `statusRow`, which runs per binding and would rebuild it N times.

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/status_test.go`:

```go
func TestStatusReportsForeignAgentInBoundTree(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	stranger := herdr.Agent{
		PaneID: "w9:p9", Kind: "claude", Status: herdr.StatusIdle,
		CWD: b.CWD, Title: "plan-executor",
	}
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking), stranger}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0].Foreign
	if len(got) != 1 {
		t.Fatalf("got %d foreign agents, want 1: %+v", len(got), got)
	}
	if got[0].PaneID != "w9:p9" || got[0].Title != "plan-executor" {
		t.Errorf("foreign = %+v", got[0])
	}
}

func TestStatusReportsNoForeignAgentsForHealthyBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep.Bindings[0].Foreign; got != nil {
		t.Errorf("foreign = %+v, want nil", got)
	}
}

func TestStatusForeignDoesNotChangeDisplay(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusWorking, false),
		builderAgent(herdr.StatusWorking),
		{PaneID: "w9:p9", Kind: "claude", Status: herdr.StatusIdle, CWD: b.CWD},
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].Display != "ACTIVE" {
		t.Errorf("display = %q, want ACTIVE: a foreign agent is an observation, not a state",
			rep.Bindings[0].Display)
	}
}

func TestStatusJSONOmitsForeignWhenEmpty(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "foreign") {
		t.Errorf("empty Foreign must be omitted from JSON, got %s", raw)
	}
}

func TestRenderStatusShowsForeignLine(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p9", Kind: "claude", Status: "idle", CWD: "/repo", Title: "plan-executor"},
		},
	}}})
	if !strings.Contains(out, "foreign") {
		t.Errorf("missing foreign line:\n%s", out)
	}
	if !strings.Contains(out, "w9:p9") || !strings.Contains(out, "plan-executor") {
		t.Errorf("foreign line missing pane or title:\n%s", out)
	}
}

func TestRenderStatusOmitsForeignLineWhenNone(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
	}}})
	if strings.Contains(out, "foreign") {
		t.Errorf("unexpected foreign line:\n%s", out)
	}
}

func TestRenderStatusForeignShowsRelativeCWDWhenNested(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p9", Kind: "opencode", Status: "working", CWD: "/repo/internal/ui", Title: "researcher"},
		},
	}}})
	if !strings.Contains(out, "internal/ui") {
		t.Errorf("nested foreign agent must show its location:\n%s", out)
	}
	if strings.Contains(out, "/repo/internal/ui") {
		t.Errorf("location must be relative to the binding cwd, not absolute:\n%s", out)
	}
}

func TestRenderStatusForeignOmitsCWDAtTreeRoot(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p9", Kind: "claude", Status: "idle", CWD: "/repo", Title: "plan-executor"},
		},
	}}})
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "foreign") {
			continue
		}
		if strings.Contains(line, "/repo") {
			t.Errorf("an agent at the tree root must not repeat the cwd: %q", line)
		}
	}
}

func TestRenderStatusShowsEveryForeignAgent(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
		Foreign: []ForeignAgent{
			{PaneID: "w9:p1", Kind: "claude", Status: "idle", CWD: "/repo"},
			{PaneID: "w9:p2", Kind: "agy", Status: "working", CWD: "/repo"},
		},
	}}})
	if n := strings.Count(out, "foreign"); n != 2 {
		t.Errorf("got %d foreign lines, want 2:\n%s", n, out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/relay/ -run 'Foreign' -v`
Expected: FAIL to build — `unknown field Foreign in struct literal`.

- [ ] **Step 3: Add the `Foreign` field**

In `internal/relay/status.go`, in the `BindingStatus` struct, immediately after the `Pending *PendingInfo` field:

```go
	// Foreign lists live agents occupying this binding's working tree that no
	// binding accounts for. It is an observation, never a judgement: relay
	// cannot see writes, so a sanctioned read-only researcher and a rogue
	// implementer both land here and the human reads the title to tell them
	// apart. Deliberately does not affect Display.
	Foreign []ForeignAgent `json:"foreign,omitempty"`
```

- [ ] **Step 4: Thread `known` through `Status` and `statusRow`**

In `Status`, after `agents, err := rt.Herdr.ListAgents(ctx)` and its error check, before the `for` loop:

```go
	// Computed once, not per binding: it spans every binding, so it does not
	// vary across rows.
	known := knownEndpoints(bindings)
```

Change the loop body call from `statusRow(rt, b, agents)` to `statusRow(rt, b, agents, known)`.

Change the signature:

```go
func statusRow(rt Runtime, b store.Binding, agents []herdr.Agent, known []store.Endpoint) (BindingStatus, error) {
```

and immediately before the final `return row, nil`:

```go
	row.Foreign = ForeignAgents(agents, known, b.CWD)
```

- [ ] **Step 5: Render the foreign lines**

In `RenderStatus`, insert between the `builder` line and the `if b.Detail != ""` block:

```go
		for _, fa := range b.Foreign {
			loc := ""
			if fa.CWD != b.CWD {
				loc = fa.CWD
				if rel, err := filepath.Rel(b.CWD, fa.CWD); err == nil {
					loc = rel
				}
			}
			fmt.Fprintf(&sb, "  foreign  %-14s %-8s %-9s %s",
				fa.PaneID, fa.Kind, fa.Status, fa.Title)
			if loc != "" {
				fmt.Fprintf(&sb, "  %s", loc)
			}
			fmt.Fprint(&sb, "\n")
		}
```

Add `"path/filepath"` to the import block in `status.go`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/relay/ -run 'Foreign' -v`
Expected: PASS.

- [ ] **Step 7: Run the whole relay package**

Run: `go test ./internal/relay/ -count=1`
Expected: PASS. In particular `TestStatusJSONForkProvenance` must still pass — it asserts `RenderStatus` output is byte-identical across a change, and bindings with no foreign agents render exactly as before.

**If it fails,** you have emitted a foreign line unconditionally. The loop must produce nothing for a nil slice.

- [ ] **Step 8: Run the full check**

Run: `make check`
Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add internal/relay/status.go internal/relay/status_test.go
git commit -m "feat(relay): report foreign agents on relay status

BindingStatus gains Foreign, populated from the agent list Status already
fetches, so detection costs no new herdr call and adds no error path. The
known-endpoint set is built once per Status rather than per row.

Display is deliberately unaffected: an agent in a tree is an observation,
not a state, and promoting it to NEEDS YOU would be relay judging something
it cannot see.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1"
```

---

### Task 3: Make harness roles plural

**Files:**
- Modify: `internal/harness/harness.go`
- Modify: `internal/harness/agents.go`
- Modify: `internal/harness/agents_test.go`
- Create: `internal/harness/harness_role_test.go`

**Interfaces:**
- Produces:
  - `type Role struct { Name, Path, Doc string }`
  - `Harness.Roles []Role` — **replaces** `Harness.RolePath` and `Harness.RoleDoc`
  - `func AgentDoc(role, kind string) ([]byte, error)` — **signature change**, was `AgentDoc(key string)`
- Breaks: `internal/doctor/doctor.go` (reads `h.RolePath`) and `cmd/relay/agent.go` (calls `AgentDoc(*kind)`). Both are repaired in this task with the minimum edit that compiles; Tasks 4–6 then build on them.

**Context you need:** `Harness.RoleDoc` currently holds `"claude"` / `"opencode"` and `AgentDoc` switches on it. The new `Role.Doc` holds the filename **stem** — `"plan-executor.claude"` — and the file read is `agents/<Doc>.md`. The filename must be built from the table's `Doc`, never from the caller's `role` argument, or `AgentDoc("../../etc/passwd", ...)` becomes a file read.

- [ ] **Step 1: Write the failing tests**

Replace the whole body of `internal/harness/agents_test.go` with:

```go
package harness

import (
	"errors"
	"strings"
	"testing"
)

func TestAgentDocResolvesEveryTableRole(t *testing.T) {
	for _, h := range All() {
		for _, r := range h.Roles {
			doc, err := AgentDoc(r.Name, h.Kind)
			if err != nil {
				t.Errorf("AgentDoc(%q, %q): %v", r.Name, h.Kind, err)
				continue
			}
			if len(doc) == 0 {
				t.Errorf("AgentDoc(%q, %q) returned empty bytes", r.Name, h.Kind)
			}
		}
	}
}

func TestAgentDocRejectsUnknownPairs(t *testing.T) {
	tests := []struct{ role, kind string }{
		{role: "plan-executor", kind: "agy"},       // agy selects by preamble
		{role: "plan-executor", kind: "nosuch"},
		{role: "nosuch", kind: "claude"},
		{role: "", kind: "claude"},
		{role: "plan-executor", kind: ""},
		{role: "", kind: ""},
	}
	for _, tc := range tests {
		if _, err := AgentDoc(tc.role, tc.kind); !errors.Is(err, ErrNoAgentDoc) {
			t.Errorf("AgentDoc(%q, %q) error = %v, want ErrNoAgentDoc", tc.role, tc.kind, err)
		}
	}
}

// The filename is built from the table's Doc field, so a caller cannot steer
// the read with path syntax in the role argument.
func TestAgentDocRejectsPathTraversal(t *testing.T) {
	for _, role := range []string{
		"../../etc/passwd",
		"../plan-executor",
		"plan-executor/../plan-executor",
		"plan-executor.claude",
	} {
		if _, err := AgentDoc(role, "claude"); !errors.Is(err, ErrNoAgentDoc) {
			t.Errorf("AgentDoc(%q, \"claude\") error = %v, want ErrNoAgentDoc", role, err)
		}
	}
}

func TestPlanExecutorDefinitionsForbidWritingSubAgents(t *testing.T) {
	// The load-bearing sentence. Deleting it from either shipped definition
	// must fail this test: relay ships the role its own loop depends on, and
	// a second writer in one tree destroys work rather than stalling.
	const oneWriter = "Exactly one agent writes to this working tree, and it is you."

	for _, kind := range []string{"claude", "opencode"} {
		doc, err := AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		if !strings.Contains(string(doc), oneWriter) {
			t.Errorf("plan-executor.%s.md must contain %q", kind, oneWriter)
		}
	}
}
```

Create `internal/harness/harness_role_test.go`:

```go
package harness

import "testing"

func TestRolesAreOrderedPlanExecutorFirst(t *testing.T) {
	for _, h := range All() {
		if len(h.Roles) == 0 {
			continue
		}
		if h.Roles[0].Name != "plan-executor" {
			t.Errorf("%s: first role = %q, want plan-executor -- the role relay's loop depends on is reported first",
				h.Kind, h.Roles[0].Name)
		}
	}
}

func TestEveryRoleIsFullyPopulated(t *testing.T) {
	for _, h := range All() {
		for i, r := range h.Roles {
			if r.Name == "" {
				t.Errorf("%s role %d: empty Name", h.Kind, i)
			}
			if r.Path == "" {
				t.Errorf("%s role %q: empty Path", h.Kind, r.Name)
			}
			if r.Doc == "" {
				t.Errorf("%s role %q: empty Doc", h.Kind, r.Name)
			}
		}
	}
}

func TestAgySelectsItsRoleByPreamble(t *testing.T) {
	h, ok := Lookup("agy")
	if !ok {
		t.Fatal("agy missing from the table")
	}
	if len(h.Roles) != 0 {
		t.Errorf("agy has no --agent flag, so it must carry no role files; got %+v", h.Roles)
	}
}

func TestClaudeAndOpencodeCarryBothRoles(t *testing.T) {
	for _, kind := range []string{"claude", "opencode"} {
		h, ok := Lookup(kind)
		if !ok {
			t.Fatalf("%s missing from the table", kind)
		}
		names := map[string]bool{}
		for _, r := range h.Roles {
			names[r.Name] = true
		}
		for _, want := range []string{"plan-executor", "researcher"} {
			if !names[want] {
				t.Errorf("%s missing role %q", kind, want)
			}
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/harness/ -v`
Expected: FAIL to build — `h.Roles undefined`, and `AgentDoc` called with 2 args.

- [ ] **Step 3: Add `Role` and replace `RolePath`/`RoleDoc`**

Replace the `Harness` struct and `knownHarnesses` in `internal/harness/harness.go`:

```go
// Role is one agent definition relay ships for a harness.
type Role struct {
	Name string // as the user types it: "plan-executor", "researcher"
	Path string // home-relative install path
	Doc  string // basename stem of the embedded definition, "<role>.<kind>"
}

// Harness describes how one herdr agent kind appears on the local machine.
type Harness struct {
	Kind        string // herdr agent kind, as passed to `herdr agent start --kind`
	Binary      string // executable name looked up on PATH
	Integration string // herdr integration target; "" when the harness has none
	// Roles are the definitions relay ships for this kind, ordered with
	// plan-executor first so doctor reports the role relay's loop depends on
	// before the rest. Empty means the harness selects its role with a
	// preamble on the first prompt rather than with a file, which is agy.
	Roles []Role
}

var knownHarnesses = map[string]Harness{
	"agy": {
		Kind:        "agy",
		Binary:      "agy",
		Integration: "antigravity-cli",
		Roles:       nil, // no --agent flag; the alias preamble selects the role
	},
	"claude": {
		Kind:        "claude",
		Binary:      "claude",
		Integration: "claude",
		Roles: []Role{
			{Name: "plan-executor", Path: ".claude/agents/plan-executor.md", Doc: "plan-executor.claude"},
			{Name: "researcher", Path: ".claude/agents/researcher.md", Doc: "researcher.claude"},
		},
	},
	"opencode": {
		Kind:        "opencode",
		Binary:      "opencode",
		Integration: "opencode",
		Roles: []Role{
			{Name: "plan-executor", Path: ".config/opencode/agents/plan-executor.md", Doc: "plan-executor.opencode"},
			{Name: "researcher", Path: ".config/opencode/agents/researcher.md", Doc: "researcher.opencode"},
		},
	},
}

// Role returns the named role for this harness.
func (h Harness) Role(name string) (Role, bool) {
	for _, r := range h.Roles {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

// RoleNames returns this harness's role names in table order, for error text.
func (h Harness) RoleNames() []string {
	names := make([]string, 0, len(h.Roles))
	for _, r := range h.Roles {
		names = append(names, r.Name)
	}
	return names
}
```

Leave `Lookup` and `All` exactly as they are.

- [ ] **Step 4: Change `AgentDoc` to take a role and a kind**

Replace `internal/harness/agents.go` in full:

```go
package harness

import (
	"embed"
	"errors"
)

//go:embed agents/*.md
var agentFS embed.FS

// ErrNoAgentDoc reports a role/kind pair with no embedded definition.
var ErrNoAgentDoc = errors.New("no embedded agent definition")

// AgentDoc returns the embedded definition for one role of one harness kind.
//
// The filename is built from the TABLE's Doc field, never from the caller's
// role string, so a caller cannot steer the read with path syntax. A pair
// absent from the table returns ErrNoAgentDoc without touching the embed FS.
func AgentDoc(role, kind string) ([]byte, error) {
	h, ok := Lookup(kind)
	if !ok {
		return nil, ErrNoAgentDoc
	}
	r, ok := h.Role(role)
	if !ok {
		return nil, ErrNoAgentDoc
	}
	b, err := agentFS.ReadFile("agents/" + r.Doc + ".md")
	if err != nil {
		return nil, ErrNoAgentDoc
	}
	return b, nil
}
```

- [ ] **Step 5: Create placeholder researcher definitions so the embed resolves**

The table now names `researcher.claude` and `researcher.opencode`, but those files do not exist, so `TestAgentDocResolvesEveryTableRole` will fail. Task 7 writes their real content. Create minimal valid files now:

```bash
cat > internal/harness/agents/researcher.claude.md <<'EOF'
---
name: researcher
description: Read-only investigator dispatched by plan-executor.
---

Placeholder. Replaced in full by Task 7.
EOF
cp internal/harness/agents/researcher.claude.md internal/harness/agents/researcher.opencode.md
```

- [ ] **Step 6: Add the one-writer sentence to both plan-executor definitions**

`TestPlanExecutorDefinitionsForbidWritingSubAgents` requires it now. Task 6 does the full rewrite; this step only lands the sentence so the guard is live from here on.

Append to **both** `internal/harness/agents/plan-executor.claude.md` and `internal/harness/agents/plan-executor.opencode.md`:

```bash
for f in internal/harness/agents/plan-executor.claude.md internal/harness/agents/plan-executor.opencode.md; do
  printf '\nExactly one agent writes to this working tree, and it is you.\n' >> "$f"
done
```

- [ ] **Step 7: Repair the two broken callers**

In `internal/doctor/doctor.go`, the role check reads `h.RolePath` and `h.RoleDoc`. Make it compile with the smallest possible edit — Task 6 rewrites this block properly:

Replace `} else if h.RolePath == "" {` with `} else if len(h.Roles) == 0 {`, and replace every remaining `h.RolePath` in that block with `h.Roles[0].Path`.

In `cmd/relay/agent.go`, replace `harness.AgentDoc(*kind)` with `harness.AgentDoc("plan-executor", *kind)`. Task 4 adds the `--role` flag.

In `cmd/relay/agent_test.go`, two existing tests call the old one-argument
form. Update both call sites:

```bash
sed -i 's|harness.AgentDoc("claude")|harness.AgentDoc("plan-executor", "claude")|; s|harness.AgentDoc("opencode")|harness.AgentDoc("plan-executor", "opencode")|' cmd/relay/agent_test.go
```

Verify none remain: `grep -n 'AgentDoc(' cmd/relay/agent_test.go` — every hit must pass two arguments.

- [ ] **Step 8: Run the harness tests**

Run: `go test ./internal/harness/ -v`
Expected: PASS, all tests.

- [ ] **Step 9: Run the full check**

Run: `make check`
Expected: clean, including `./internal/doctor` and `./cmd/relay`.

**If `cmd/relay` fails to build,** you missed an `AgentDoc` call site in
`agent_test.go`. Step 7 lists both.

**If doctor tests fail,** you changed the check's `Name` field. Do not — `internal/doctor/doctor_test.go` looks rows up with `findCheck(report, "agy", "plan-executor")`. Task 6 is where role rows change shape.

- [ ] **Step 10: Commit**

```bash
git add internal/harness cmd/relay/agent.go internal/doctor/doctor.go
git commit -m "refactor(harness): make shipped roles plural

Harness.RolePath/RoleDoc become Roles []Role so relay can ship more than one
definition per kind. AgentDoc takes (role, kind) and builds its filename from
the table's Doc field rather than the caller's string, so a role argument
cannot steer the read; the two-case switch is gone in favour of embedding
agents/*.md.

Adds the one-writer sentence to both plan-executor definitions and a test
that fails if either loses it. Placeholder researcher files land here so the
table resolves; their content follows.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1"
```

---

### Task 4: `relay agent print --role`

**Files:**
- Modify: `cmd/relay/agent.go`
- Modify: `cmd/relay/agent_test.go`

**Interfaces:**
- Consumes: `harness.AgentDoc(role, kind)`, `harness.Lookup`, `harness.Harness.RoleNames()` from Task 3.
- Produces: no Go API; the CLI contract `relay agent print --kind <kind> [--role <role>]`.

**Context you need:** `--role` defaults to `plan-executor` so every README line and every doctor `Fix` string already in the wild keeps working byte-for-byte. Do not make it required.

- [ ] **Step 1: Write the failing tests**

`cmd/relay/agent_test.go` already has `captureOutput(t, fn) (stdout, stderr []byte, err error)` and drives the CLI through `run([]string{...})`. Use them — do not add a second capture helper. Append:

```go
func TestAgentPrintDefaultsToPlanExecutor(t *testing.T) {
	expected, err := harness.AgentDoc("plan-executor", "claude")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}

	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print", "--kind", "claude"})
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr, got %q", string(stderr))
	}
	if !bytes.Equal(stdout, expected) {
		t.Error("no --role must print the plan-executor definition unchanged")
	}
}

func TestAgentPrintSelectsResearcher(t *testing.T) {
	expected, err := harness.AgentDoc("researcher", "claude")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}

	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print", "--kind", "claude", "--role", "researcher"})
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr, got %q", string(stderr))
	}
	if !bytes.Equal(stdout, expected) {
		t.Error("--role researcher must print the researcher definition")
	}
}

func TestAgentPrintUnknownRoleExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print", "--kind", "claude", "--role", "nosuch"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "researcher") {
		t.Errorf("stderr must name the roles the kind has, got %q", string(stderr))
	}
}
```

`bytes`, `errors` and `strings` are already imported in this file.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/relay/ -run TestAgentPrint -v`
Expected: FAIL — `--role` is not a defined flag, so `fs.Parse` errors.

- [ ] **Step 3: Add the flag and role resolution**

In `cmd/relay/agent.go`, in `cmdAgentPrint`, after the `kind` flag declaration:

```go
	role := fs.String("role", "plan-executor", "role definition to print (plan-executor, researcher)")
```

Replace the `harness.AgentDoc(*kind)` call and its error branch with:

```go
	doc, err := harness.AgentDoc(*role, *kind)
	if err != nil {
		if h, ok := harness.Lookup(*kind); ok {
			fmt.Fprintf(os.Stderr, "relay: kind %q has no role %q (known: %v)\n",
				*kind, *role, h.RoleNames())
		} else {
			fmt.Fprintf(os.Stderr, "relay: unknown kind %q (known with definitions: claude, opencode)\n", *kind)
		}
		return exitCodeErr{code: 2}
	}
```

Update the three usage strings in `cmdAgent` and `cmdAgentPrint` to:

```
usage: relay agent print --kind <claude|opencode> [--role <plan-executor|researcher>]
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/relay/ -run TestAgentPrint -v`
Expected: PASS.

- [ ] **Step 5: Verify the no-flag output by hand**

Run: `go run ./cmd/relay agent print --kind claude | head -5`
Expected: the plan-executor front matter, exactly as before this task.

- [ ] **Step 6: Run the full check**

Run: `make check`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add cmd/relay/agent.go cmd/relay/agent_test.go
git commit -m "feat(relay): agent print --role selects a shipped definition

Defaults to plan-executor, so every README line and doctor fix command
already in the wild keeps working unchanged. An unknown role names the roles
the kind actually has.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1"
```

---

### Task 5: `doctor.Env` gains `ReadFile`

**Files:**
- Modify: `internal/doctor/env.go`
- Modify: `internal/doctor/doctor_test.go` (the `fakeEnv` type near line 14)

**Interfaces:**
- Produces: `Env.ReadFile(path string) ([]byte, error)`; `realEnv.ReadFile` delegating to `os.ReadFile`; `fakeEnv.fileContents map[string]string`.

**Context you need:** `Env` can `Stat` a path but not read one, so Task 6's model-pin parsing is unreachable without this. This task adds the capability and nothing else — no caller changes, no behaviour change. It is separate from Task 6 so the interface change can be reviewed on its own.

- [ ] **Step 1: Add the method to the interface and the real implementation**

In `internal/doctor/env.go`, add to the `Env` interface, after `Stat`:

```go
	// ReadFile reads a file whose existence Stat has already established.
	// Used to report facts about an installed role definition; a read error
	// is never itself a check failure.
	ReadFile(path string) ([]byte, error)
```

And the implementation:

```go
func (e *realEnv) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
```

- [ ] **Step 2: Run the tests to verify `fakeEnv` no longer satisfies `Env`**

Run: `go test ./internal/doctor/ 2>&1 | head -20`
Expected: FAIL to build — `*fakeEnv does not implement Env (missing method ReadFile)`.

- [ ] **Step 3: Add `fileContents` to `fakeEnv`**

In `internal/doctor/doctor_test.go`, add the field to the `fakeEnv` struct after `existingFiles`:

```go
	fileContents  map[string]string // path -> content; absent reads as empty
```

And the method, beside the other `fakeEnv` methods:

```go
// ReadFile returns recorded content. A path in existingFiles but absent from
// fileContents reads as empty, which must produce no model suffix and no error.
func (f *fakeEnv) ReadFile(path string) ([]byte, error) {
	return []byte(f.fileContents[path]), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/doctor/ -count=1`
Expected: PASS, unchanged behaviour.

- [ ] **Step 5: Run the full check**

Run: `make check`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/doctor/env.go internal/doctor/doctor_test.go
git commit -m "refactor(doctor): let Env read a file it can already stat

Needed to report the model an installed role definition pins. No caller
changes and no behaviour change.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1"
```

---

### Task 6: One doctor row per role, with the pinned model

**Files:**
- Modify: `internal/doctor/doctor.go` (the role block inside the per-kind loop)
- Modify: `internal/doctor/doctor_test.go`

**Interfaces:**
- Consumes: `harness.Harness.Roles` (Task 3); `Env.ReadFile` (Task 5).
- Produces: `func frontmatterModel(raw []byte) string` (unexported, in `internal/doctor/doctor.go`); one `Check` per role, `Check.Name` set to the role name.

**Context you need — do not break this:** `internal/doctor/doctor_test.go` finds rows with `findCheck(report, "agy", "plan-executor")`. agy has no roles, and its "selected by preamble" row must keep `Name: "plan-executor"` so those tests pass untouched. That is not a compatibility hack: the preamble does select plan-executor.

- [ ] **Step 1: Write the failing test for `frontmatterModel`**

Append to `internal/doctor/doctor_test.go`:

```go
func TestFrontmatterModel(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "pinned model",
			raw:  "---\nname: researcher\nmodel: haiku\n---\n\nbody\n",
			want: "haiku",
		},
		{
			name: "model with a slash",
			raw:  "---\nmodel: openrouter/z-ai/glm-5.3-flash\n---\n",
			want: "openrouter/z-ai/glm-5.3-flash",
		},
		{
			name: "trailing whitespace trimmed",
			raw:  "---\nmodel:   haiku   \n---\n",
			want: "haiku",
		},
		{name: "no model key", raw: "---\nname: researcher\n---\n", want: ""},
		{name: "no frontmatter", raw: "just a body\n", want: ""},
		{name: "empty file", raw: "", want: ""},
		{
			name: "model after the frontmatter is not a pin",
			raw:  "---\nname: x\n---\n\nmodel: not-a-pin\n",
			want: "",
		},
		{
			name: "unterminated frontmatter",
			raw:  "---\nmodel: haiku\n",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := frontmatterModel([]byte(tc.raw)); got != tc.want {
				t.Errorf("frontmatterModel(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/doctor/ -run TestFrontmatterModel -v`
Expected: FAIL to build — `undefined: frontmatterModel`.

- [ ] **Step 3: Implement `frontmatterModel`**

Add to `internal/doctor/doctor.go`:

```go
// frontmatterModel returns the value of a `model:` key in the leading `---`
// fenced block, or "" when there is none.
//
// Deliberately shallow: this reports a fact about an installed file for a
// human to read, so a malformed file yields "" rather than an error. An
// absent model pin is not a fault, and a parse failure must never mask the
// fact that the file exists.
func frontmatterModel(raw []byte) string {
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return "" // frontmatter ended with no model key
		}
		rest, ok := strings.CutPrefix(line, "model:")
		if !ok {
			continue
		}
		return strings.TrimSpace(rest)
	}
	return "" // unterminated frontmatter
}
```

`strings` is already imported in this file.

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/doctor/ -run TestFrontmatterModel -v`
Expected: PASS, 8 subtests.

- [ ] **Step 5: Write the failing tests for per-role rows**

Append to `internal/doctor/doctor_test.go`. Match the existing `fakeEnv` construction style in this file for `homeDir`, `lookPaths` and `intStatus`; the fields below show only what this behaviour needs.

```go
func TestDoctorEmitsOneRowPerRole(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
		"/fake/home/.claude/agents/researcher.md":    true,
	}

	report := Run(context.Background(), env, []string{"claude"})

	for _, role := range []string{"plan-executor", "researcher"} {
		c := findCheck(report, "claude", role)
		if c == nil {
			t.Fatalf("no %q row for claude", role)
		}
		if c.Severity != SevOK {
			t.Errorf("%s severity = %v, want ok", role, c.Severity)
		}
	}
}

func TestDoctorMissingRoleFileNamesTheRoleInTheFix(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
	}

	report := Run(context.Background(), env, []string{"claude"})

	c := findCheck(report, "claude", "researcher")
	if c == nil {
		t.Fatal("no researcher row for claude")
	}
	if c.Severity != SevWarn {
		t.Errorf("severity = %v, want warn", c.Severity)
	}
	if !strings.Contains(c.Fix, "--role researcher") {
		t.Errorf("fix must name the role, got %q", c.Fix)
	}
}

func TestDoctorReportsTheInstalledModelPin(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
		"/fake/home/.claude/agents/researcher.md":    true,
	}
	env.fileContents = map[string]string{
		"/fake/home/.claude/agents/researcher.md": "---\nname: researcher\nmodel: haiku\n---\n",
	}

	report := Run(context.Background(), env, []string{"claude"})

	c := findCheck(report, "claude", "researcher")
	if c == nil {
		t.Fatal("no researcher row")
	}
	if !strings.Contains(c.Detail, "model: haiku") {
		t.Errorf("detail = %q, want it to report the pinned model", c.Detail)
	}
}

func TestDoctorOmitsModelSuffixWhenUnpinned(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{
		"/fake/home/.claude/agents/plan-executor.md": true,
		"/fake/home/.claude/agents/researcher.md":    true,
	}
	env.fileContents = map[string]string{
		"/fake/home/.claude/agents/researcher.md": "---\nname: researcher\n---\n",
	}

	report := Run(context.Background(), env, []string{"claude"})

	c := findCheck(report, "claude", "researcher")
	if c == nil {
		t.Fatal("no researcher row")
	}
	if strings.Contains(c.Detail, "model:") {
		t.Errorf("detail = %q, want no model suffix", c.Detail)
	}
	if c.Severity != SevOK {
		t.Errorf("severity = %v, want ok -- an unpinned model is not a fault", c.Severity)
	}
}
```

Add this helper beside the tests above. The values match the existing
literals in this file:

```go
// newFakeEnvForKind is a fakeEnv where everything except the role files is
// healthy, so a test can vary existingFiles and fileContents alone.
func newFakeEnvForKind(t *testing.T, kind string) *fakeEnv {
	t.Helper()
	return &fakeEnv{
		herdrVer:      herdr.MinVersion,
		daemonRunning: true,
		lookPaths:     map[string]string{kind: "/usr/bin/" + kind},
		intStatus: map[string]herdr.IntegrationState{
			kind: {Installed: true, Detail: "current (v9)"},
		},
		homeDir: "/fake/home",
	}
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `go test ./internal/doctor/ -run 'TestDoctorEmitsOneRow|TestDoctorMissingRole|TestDoctorReportsTheInstalled|TestDoctorOmitsModel' -v`
Expected: FAIL — only one role row exists, and its name comes from `h.Roles[0]`.

- [ ] **Step 7: Replace the role block with a loop**

In `internal/doctor/doctor.go`, inside `if !cfg.adopted {`, replace the whole `// Role (plan-executor) check` block with:

```go
			// Role checks: one row per shipped role. A harness with no roles
			// selects its role with a preamble on the first prompt instead of
			// a file, which is agy; its row keeps the name plan-executor,
			// because that is still the role the preamble selects.
			switch {
			case !known:
				checks = append(checks, Check{
					Group:    kind,
					Name:     "plan-executor",
					Severity: SevOK,
					Detail:   fmt.Sprintf("not checked -- relay has no role path for kind %q", kind),
					Fix:      "",
				})
			case len(h.Roles) == 0:
				checks = append(checks, Check{
					Group:    kind,
					Name:     "plan-executor",
					Severity: SevOK,
					Detail:   "selected by preamble, not a file",
					Fix:      "",
				})
			default:
				for _, r := range h.Roles {
					checks = append(checks, roleCheck(env, kind, r))
				}
			}
```

And add, beside `frontmatterModel`:

```go
// roleCheck probes one shipped role definition on disk.
func roleCheck(env Env, kind string, r harness.Role) Check {
	homeRel := "~/" + r.Path
	fullPath, err := env.HomePath(r.Path)
	if err != nil {
		return Check{
			Group: kind, Name: r.Name, Severity: SevWarn,
			Detail:      fmt.Sprintf("could not resolve home directory: %v", err),
			ProbeFailed: true,
		}
	}
	if env.Stat(fullPath) != nil {
		return Check{
			Group: kind, Name: r.Name, Severity: SevWarn,
			Detail: fmt.Sprintf("missing: %s", homeRel),
			Fix: fmt.Sprintf("relay agent print --kind %s --role %s > %s",
				kind, r.Name, homeRel),
		}
	}

	detail := homeRel
	// A read error is deliberately swallowed: the file exists, which is what
	// this row reports, and the model pin is a courtesy on top of that.
	if raw, err := env.ReadFile(fullPath); err == nil {
		if model := frontmatterModel(raw); model != "" {
			detail = fmt.Sprintf("%s (model: %s)", homeRel, model)
		}
	}
	return Check{Group: kind, Name: r.Name, Severity: SevOK, Detail: detail}
}
```

- [ ] **Step 8: Run the doctor tests**

Run: `go test ./internal/doctor/ -count=1 -v`
Expected: PASS, including the pre-existing `TestDoctorAgyRoleSelectedByPreamble` and `TestDoctorUsableBuilderMissingRoleFileDoesNotBreakCompleteness` **unmodified**.

**If either pre-existing test fails,** you renamed agy's row or changed `UsableBuilder`. Role rows have never contributed to `UsableBuilder` and still must not.

- [ ] **Step 9: Run the full check**

Run: `make check`
Expected: clean.

- [ ] **Step 10: Commit**

```bash
git add internal/doctor/doctor.go internal/doctor/doctor_test.go
git commit -m "feat(doctor): one row per shipped role, with the pinned model

Each role a harness ships gets its own doctor row, and an installed
definition's Detail reports the model its frontmatter pins -- read from the
installed file, not the embedded one, since the user may have edited it and
what runs is the point.

An absent or unparseable pin is never a fault: the row stays ok and simply
omits the suffix. agy keeps its single preamble row under the name
plan-executor, which is still the role the preamble selects.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1"
```

---

### Task 7: Amend the plan-executor definitions

**Files:**
- Modify: `internal/harness/agents/plan-executor.claude.md`
- Modify: `internal/harness/agents/plan-executor.opencode.md`

**Interfaces:** none — content only. `TestPlanExecutorDefinitionsForbidWritingSubAgents` (Task 3) guards the outcome.

**Context you need:** These two files are twins. The only intended differences are the sub-agent tool's name (`Agent` for claude, `Task` for opencode, per the existing text) and the `name:`/`description:` front matter. Every edit below applies to both.

**Do NOT add the `researcher` role name yet.** Task 9 does that, once the definitions it names actually exist.

- [ ] **Step 1: Read both files end to end**

Run: `cat internal/harness/agents/plan-executor.claude.md`
Then: `cat internal/harness/agents/plan-executor.opencode.md`

Note the six regions you are about to change: the `<example>` blocks in the front matter, the opening "You are a Plan Execution Specialist" line, `PARALLELIZATION PROTOCOL`, the `EXECUTION ALGORITHM` steps, `SUB-AGENT PROMPTING STANDARDS`, and the final-report bullet list.

- [ ] **Step 2: Rewrite the front-matter examples**

All three `<example>` blocks currently advertise parallel implementation — e.g. *"dispatching sub-agents to run steps 1-3 in parallel"*. These are what the harness matches against when deciding to invoke the role, and they will undo the body if left.

Rewrite each so the `assistant:` line and `<commentary>` describe **sequential implementation with parallel research**. For example, the first block's assistant line becomes:

```
assistant: "I'll use the plan-executor agent to implement this plan exactly as
written, researching the affected files in parallel and then making every edit
itself in order."
```

Apply the same transformation to the other two blocks. Keep the `Context:` and `user:` lines untouched.

- [ ] **Step 3: Fix the opening line**

Replace:

```
combined with aggressive parallelization through sub-agents.
```

with:

```
combined with parallel read-only research.
```

- [ ] **Step 4: Rewrite `PARALLELIZATION PROTOCOL`**

Replace the section heading and its five numbered items with:

```
RESEARCH DELEGATION PROTOCOL (CRITICAL)

Exactly one agent writes to this working tree, and it is you.

A second writer in one working tree does not stall, it destroys work: two
processes contend on one git index and one HEAD, and two concurrent edits to a
file resolve as last-write-wins with no conflict, no error, and no record. This
holds even for steps that touch different files, because the race is at the git
layer rather than the logical one. Parallelism comes from several builders in
several worktrees, which is arranged above you, not from sub-agents inside this
one.

You therefore delegate READS ONLY:

1. File reads: when a step or the plan overall requires reading multiple files,
   dispatch parallel read-only sub-agents instead of reading sequentially.
2. Codebase research: when a step requires understanding existing code,
   patterns, or conventions, launch research sub-agents in parallel with your
   own implementation work.
3. NEVER dispatch a sub-agent to execute an implementation step, apply an edit,
   create or delete a file, or run any command that modifies the tree, the
   index, or HEAD. You make every change yourself.
```

- [ ] **Step 5: Rewrite the `EXECUTION ALGORITHM` steps**

Replace the wave-dispatch steps (currently 2 through 5) with:

```
2. Identify what you need to understand before editing, and dispatch research
   sub-agents for it in parallel.
3. Execute the plan's steps yourself, in order, honouring every stated
   dependency. A step that consumes another's output, edits the same files, or
   assumes prior changes exist must run after it.
4. Verify each step's deliverable against its text before moving on.
5. Fold in research results as they arrive; never block an edit you can already
   make on a research sub-agent that has not returned.
```

- [ ] **Step 6: Rewrite `SUB-AGENT PROMPTING STANDARDS`**

Retitle to `RESEARCH SUB-AGENT PROMPTING STANDARDS`, keep the self-containment guidance, and add as its first bullet:

```
- State in every prompt that the sub-agent is read-only: it must not create,
  edit, or delete files, and must not run any tree-modifying command. If it
  believes a change is needed, it reports that back to you and you make it.
```

Change the bullet reading *"Instruct the sub-agent to follow the step literally and report exactly what it did, including all files changed"* to:

```
- Instruct the sub-agent to report what it found, with file paths and line
  numbers, and to say plainly when it did not find something rather than
  guessing.
```

- [ ] **Step 7: Fix the final-report bullet**

Replace `- Which steps ran in parallel via sub-agents.` with:

```
- Which research was delegated, and what it returned.
```

- [ ] **Step 8: Place the one-writer sentence properly**

Task 3 appended `Exactly one agent writes to this working tree, and it is you.` to the end of both files. Step 4 above put it inside the protocol section where it belongs. Delete the trailing appended copy from the end of both files so it appears exactly once.

Verify: `grep -c 'Exactly one agent writes' internal/harness/agents/plan-executor.*.md`
Expected: `1` for each file.

- [ ] **Step 9: Verify no delegation-to-write language survives**

Run:

```bash
grep -n -i 'parallel sub-agents in a single message\|one sub-agent per step\|dispatch wave\|maximize throughput' internal/harness/agents/plan-executor.*.md
```

Expected: no output.

- [ ] **Step 10: Run the harness tests and the full check**

Run: `go test ./internal/harness/ -v && make check`
Expected: PASS and clean. `TestPlanExecutorDefinitionsForbidWritingSubAgents` must still pass after the sentence moved.

- [ ] **Step 11: Commit**

```bash
git add internal/harness/agents/plan-executor.claude.md internal/harness/agents/plan-executor.opencode.md
git commit -m "fix(harness): stop telling builders to dispatch writing sub-agents

The shipped plan-executor definitions instructed the builder to dispatch
parallel sub-agents to execute implementation steps -- relay shipping a role
that encourages the one thing its own model forbids. A second writer in one
tree destroys work rather than stalling, and it is the failure class
docs/design.md is shaped to avoid.

Read-only research delegation is kept, since it neither touches the index nor
moves HEAD. The front-matter examples are rewritten too: they advertise the
behaviour to the harness and would have undone the body.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1"
```

---

### Task 8: Write the researcher definitions

**Files:**
- Modify: `internal/harness/agents/researcher.claude.md` (replacing the Task 3 placeholder)
- Modify: `internal/harness/agents/researcher.opencode.md` (replacing the Task 3 placeholder)

**Interfaces:** none — content only.

**Context you need:** These are the roles the builder's read-only sub-agents run as. They are **not** #36's proposed `explorer` consult: a consult runs in its own relay-spawned pane and hands findings back through a file path, whereas a researcher returns findings in-band to its parent and never writes a file. Same shape, different contract; do not merge them.

The model pins are worked examples, framed exactly as the alias table is. They are chosen so that **neither introduces a provider the user does not already need**: `haiku` rides the same Claude subscription `cbuilder`'s `sonnet` assumes, and `openrouter/z-ai/glm-5.3-flash` is the model the `builder` alias already names.

- [ ] **Step 1: Read a shipped definition for the house format**

Run: `head -20 internal/harness/agents/plan-executor.claude.md`

Match its front-matter shape — the key order and the `description:` style — rather than inventing one.

- [ ] **Step 2: Write `researcher.claude.md`**

```bash
cat > internal/harness/agents/researcher.claude.md <<'EOF'
---
name: researcher
description: Read-only investigator dispatched by plan-executor to locate code, trace conventions, and answer questions about an existing codebase. Never edits anything.
model: haiku
---

You are a Researcher. You answer questions about a codebase for a
plan-executor that is implementing against it. You find things; you never
change them.

READ-ONLY, WITHOUT EXCEPTION

You must not create, edit, or delete a file, and must not run any command that
modifies the working tree, the git index, or HEAD. That includes `git add`,
`git commit`, `git checkout`, `git stash`, formatters, code generators, and
anything that installs or updates dependencies.

This is not a stylistic preference. Exactly one agent writes to this working
tree, and it is not you -- it is the plan-executor that dispatched you. Two
writers in one tree contend on one git index, and two concurrent edits to a
file resolve as last-write-wins with no conflict and no error. Your edit would
not merely be wrong, it could silently erase work.

If your findings imply a change is needed, say so in your report. The
plan-executor makes it.

WHAT A GOOD REPORT LOOKS LIKE

- Cite file paths with line numbers, so the caller can go straight there.
- Quote the few lines that actually matter rather than summarising them away.
- Answer the question you were asked first, then add context you found on the
  way if it bears on the caller's task.
- Report what the code does, not what it should do.

WHEN YOU DO NOT FIND SOMETHING

Say so plainly, name where you looked, and stop. Do not infer that a thing
probably exists somewhere, and do not offer a plausible-looking path you have
not opened. A confident wrong answer costs the caller more than "not found in
internal/, cmd/, or docs/".

CHOOSING THE MODEL

The `model:` line above is a worked example, not a supported set. It selects a
cheap fast model because searching does not need an expensive one. Change it to
whatever your provider offers, or delete the line to inherit the caller's
model.
EOF
```

- [ ] **Step 3: Write `researcher.opencode.md`**

Same content, with the model pin changed. Generate it from the claude twin so the bodies cannot drift:

```bash
sed 's|^model: haiku$|model: openrouter/z-ai/glm-5.3-flash|' \
  internal/harness/agents/researcher.claude.md \
  > internal/harness/agents/researcher.opencode.md
```

- [ ] **Step 4: Verify both files and their pins**

Run:

```bash
grep -n '^model:' internal/harness/agents/researcher.*.md
diff <(sed '/^model:/d' internal/harness/agents/researcher.claude.md) \
     <(sed '/^model:/d' internal/harness/agents/researcher.opencode.md) && echo "bodies identical"
```

Expected: `haiku` for claude, `openrouter/z-ai/glm-5.3-flash` for opencode, and `bodies identical`.

- [ ] **Step 5: Verify the definitions are reachable through the CLI**

Run: `go run ./cmd/relay agent print --kind opencode --role researcher | head -6`
Expected: the front matter with the OpenRouter pin.

- [ ] **Step 6: Run the full check**

Run: `make check`
Expected: clean. `TestAgentDocResolvesEveryTableRole` now exercises real content.

- [ ] **Step 7: Commit**

```bash
git add internal/harness/agents/researcher.claude.md internal/harness/agents/researcher.opencode.md
git commit -m "feat(harness): ship a read-only researcher role

The role the builder's sub-agents run as now that they may only read. Both
definitions forbid every tree-modifying operation and explain why: exactly one
agent writes to a working tree, and a second one erases work silently rather
than failing.

The model pins are worked examples, chosen so neither adds a provider the user
does not already need -- haiku rides the same subscription cbuilder assumes,
and the opencode pin is the model the builder alias already names.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1"
```

---

### Task 9: Point plan-executor at the researcher role — **VERIFICATION REQUIRED**

**Files:**
- Modify: `internal/harness/agents/plan-executor.claude.md`
- Modify: `internal/harness/agents/plan-executor.opencode.md`

**Interfaces:** none — content only.

> **STOP AND READ.** This task contains the one thing in this plan you must not
> guess. The claude definition dispatches sub-agents via the `Agent` tool with a
> `subagent_type` parameter. **The opencode equivalent parameter name is not
> known and has not been verified.**
>
> A wrong parameter name here fails **silently**: opencode ignores the unknown
> field, the sub-agent runs in its default role, and the default role is a
> writing role. That is the exact hazard this entire plan exists to remove, and
> it would be reintroduced invisibly with every test still green.
>
> If you cannot establish opencode's actual parameter name from its own
> documentation or its tool schema, **do not write the opencode half of this
> task.** Complete the claude half, commit it, and report the blocker. Per
> CLAUDE.md: a halt that surfaces a design error is worth more than a green
> suite that bent to fit.

- [ ] **Step 1: Establish how claude selects a sub-agent role**

Confirm from the existing text in `plan-executor.claude.md` that sub-agents are dispatched with the `Agent` tool, and that the role is selected by a `subagent_type` parameter. Record the exact spelling you find.

- [ ] **Step 2: Establish how opencode selects a sub-agent role**

Check, in this order:
1. opencode's own documentation for its sub-agent/Task tool.
2. The tool's schema as opencode reports it.
3. `~/.config/opencode/` on this machine for an existing agent configuration that demonstrates the call shape.

Record the exact parameter name and an example call.

**If none of the three yields a definite answer, stop here.** Do Step 3, skip Step 4, and report at Step 6.

- [ ] **Step 3: Add the dispatch instruction to `plan-executor.claude.md`**

In the `RESEARCH DELEGATION PROTOCOL` section, after item 3, add:

```
4. Dispatch every research sub-agent as the `researcher` role -- pass
   subagent_type: researcher to the Agent tool. That role is read-only by
   definition, which is what makes delegating to it safe. Do not dispatch
   research to the default role.
```

- [ ] **Step 4: Add the equivalent to `plan-executor.opencode.md`** *(only if Step 2 succeeded)*

Add the same item 4, with the tool name and parameter name **exactly as verified in Step 2**. Do not transliterate the claude spelling.

- [ ] **Step 5: Run the full check**

Run: `make check`
Expected: clean.

- [ ] **Step 6: Commit, and report any blocker**

```bash
git add internal/harness/agents/plan-executor.claude.md internal/harness/agents/plan-executor.opencode.md
git commit -m "feat(harness): dispatch research to the researcher role

plan-executor now names the read-only researcher role when delegating, so
research sub-agents cannot land in the default writing role.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1"
```

If Step 2 failed, say so explicitly in your report: which of the three sources
you checked, what each returned, and that `plan-executor.opencode.md` still
does not name the researcher role. Do not close the task as complete.

---

### Task 10: Documentation

**Files:**
- Modify: `docs/design.md`
- Modify: `README.md`

**Interfaces:** none.

- [ ] **Step 1: Find where `docs/design.md` states the one-writer guarantee**

Run: `grep -n -i 'writer\|destroy\|working tree' docs/design.md`

Read the surrounding section so the addition sits with the existing argument rather than beside it.

- [ ] **Step 2: Document detection and its limit in `docs/design.md`**

Add to that section:

```markdown
relay enforces one writer per working tree only for bindings: `Bind` refuses a
second binding on a tree another one drives. An agent relay did not start is
outside that guarantee entirely, and relay cannot prevent one -- it does not
own the harness.

What it can do is say so. `relay status` reports, per binding, every live agent
whose cwd is inside that binding's working tree and which no binding accounts
for, as `foreign` rows. The rule is occupancy, not authorship: `herdr agent
list` reports a kind, a status, a cwd and a title, and nothing about writes, so
a sanctioned read-only researcher and a rogue implementer look identical from
here. relay reports that something is there and shows its title; the reader
draws the conclusion. Rows are never filtered by title, which would mean relay
trusting a string any agent can set.

An agent is foreign when no binding references it, not merely when it is not
this binding's builder -- a second binding's planner may legitimately share a
tree, and relay knows about it. Agents in subdirectories of the tree count;
agents in parent directories do not.

Known limit: paths are compared literally. A tree reached through a symlink
under one name and reported by herdr under another will not match, and the
agent goes unreported.
```

- [ ] **Step 3: Document the researcher role in `README.md`**

Find the existing section that tells the reader to install the `plan-executor`
definition (`grep -n 'agent print' README.md`) and extend it so both roles are
installed:

```bash
# claude
relay agent print --kind claude --role plan-executor > ~/.claude/agents/plan-executor.md
relay agent print --kind claude --role researcher    > ~/.claude/agents/researcher.md

# opencode
relay agent print --kind opencode --role plan-executor > ~/.config/opencode/agents/plan-executor.md
relay agent print --kind opencode --role researcher    > ~/.config/opencode/agents/researcher.md
```

Add beneath it:

```markdown
`researcher` is the read-only role the builder's own sub-agents run as. It
exists because exactly one agent may write to a working tree: research can fan
out safely, implementation cannot. Both shipped definitions pin a `model:` in
their front matter as a worked example, chosen so neither needs a provider the
rest of relay does not already assume. That line is the first thing to change
for your own setup -- `relay doctor` reports the pin each installed definition
carries.

`agy` has no `--agent` flag and selects its role from the alias preamble
instead, so it has no definitions to install.
```

- [ ] **Step 4: Verify every command in the README section runs**

Run each of the four `relay agent print` commands above with `> /dev/null` and confirm all exit 0:

```bash
for k in claude opencode; do for r in plan-executor researcher; do
  go run ./cmd/relay agent print --kind $k --role $r > /dev/null || echo "FAILED: $k $r"
done; done
```

Expected: no output.

- [ ] **Step 5: Run the full check**

Run: `make check`
Expected: clean. `scripts/check-plugin-version.sh` runs here; if it fails, read it before touching anything.

- [ ] **Step 6: Commit**

```bash
git add docs/design.md README.md
git commit -m "docs(relay): document foreign-agent rows and the researcher role

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019ra83DtaVmCPmPXu4mLGS1"
```

---

## Final Verification

- [ ] `make check` is clean from a fresh `git status`.
- [ ] `git diff --stat main` touches only the files named in this plan.
- [ ] **Mutation test the one-writer guard.** Delete the sentence `Exactly one agent writes to this working tree, and it is you.` from `internal/harness/agents/plan-executor.claude.md`, run `go test ./internal/harness/ -run TestPlanExecutorDefinitionsForbidWritingSubAgents`, and confirm it FAILS. Restore the sentence and confirm it passes. A guard that passes both ways is pinning nothing.
- [ ] **Mutation test the separator guard.** In `withinTree`, change `strings.HasPrefix(c, t+string(filepath.Separator))` to `strings.HasPrefix(c, t)`, run `go test ./internal/relay/ -run TestWithinTree`, and confirm `sibling sharing a prefix` FAILS. Restore.
- [ ] **Mutation test the known-endpoint breadth.** In `knownEndpoints`, drop `b.Planner` from the appended endpoints, run `go test ./internal/relay/ -run TestForeignAgents`, and confirm `TestForeignAgentsExcludesAnotherBindingsPlanner` FAILS. Restore.
- [ ] `relay status` on a real binding with a hand-started agent in its tree shows a `foreign` row; with none, output is unchanged from before this branch.
- [ ] Task 9's opencode half is either done with a verified parameter name, or explicitly reported as blocked.
