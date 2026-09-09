# Plan B: relay status Detail Line Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `relay status` say which of the three broken-binding situations a `NEEDS YOU` row actually is, and what to do about it — without adding a display state or changing the store.

**Architecture:** One additive `Detail` field on `BindingStatus`, populated in `statusRow` only for `store.StateBroken` from Plan A's `DiagnoseBuilder`, and rendered as one extra line in `RenderStatus`. `displayState` is not touched: the three-state collapse stays.

**Tech Stack:** Go. Standard library only. Tests use the existing `fakeHerdr` fixtures in `internal/relay/*_test.go`.

**Spec:** `docs/specs/2026-09-09-broken-binding-recovery-design.md`, section 6.3. Issue: [#41](https://github.com/fuad-daoud/relay/issues/41).

**Depends on:** Plan A. `DiagnoseBuilder` and `(BuilderDiagnosis).Detail(round int) string` must already exist in `internal/relay/diagnose.go`. If they do not, stop — do not reimplement them here.

## Global Constraints

- **Do not modify `displayState`** (`internal/relay/status.go`). A fourth display word is explicitly out of scope; `broken` still shows as `NEEDS YOU`.
- **Do not modify any `store` type or `store.State` constant.** No schema change, no migration.
- The `Detail` JSON field must be `omitempty`, so an existing `--json` consumer sees no new key on healthy bindings.
- Only `store.StateBroken` gets a detail. `orphaned` and `needs_you` are unambiguous and stay untouched.
- A non-broken binding must render **byte-identically** to today.
- Run `go test ./...` from the repo root and `gofmt -l internal/relay` (expect no output) before each commit.

---

### Task 1: Populate the field

**Files:**
- Modify: `internal/relay/status.go` (the `BindingStatus` struct, and `statusRow`)
- Test: `internal/relay/status_test.go`

**Interfaces:**
- Consumes: `DiagnoseBuilder(b store.Binding) BuilderDiagnosis` and `(BuilderDiagnosis).Detail(round int) string` from Plan A.
- Produces: `BindingStatus.Detail string` with JSON tag `detail,omitempty`. Task 2 renders it.

**Background you need:** `statusRow` (in `internal/relay/status.go`) builds one `BindingStatus` from the stored `store.Binding` plus the live agent list. It already reads `b.State` for `Display`. The fixture `sentBinding(t, f)` puts a binding one `Send` into round 1 and deliberately leaves `Builder.SessionID` empty (see the comment in `seedBound`, `internal/relay/send_test.go`), which is exactly the unidentified case.

- [ ] **Step 1: Write the failing test**

Append to `internal/relay/status_test.go`:

```go
func TestStatusDetailsBrokenBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.State = store.StateBroken
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The builder is gone; only the planner is live.
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]

	if got.Display != "NEEDS YOU" {
		t.Errorf("display = %q, want NEEDS YOU (the collapse must not change)", got.Display)
	}
	want := DiagnoseBuilder(b).Detail(b.Round)
	if got.Detail != want {
		t.Errorf("detail =\n  %q\nwant\n  %q", got.Detail, want)
	}
	// sentBinding leaves the builder session-less, so the warning must appear.
	if !strings.Contains(got.Detail, "moved pane") {
		t.Errorf("detail must warn about a moved pane, got %q", got.Detail)
	}
}

func TestStatusOmitsDetailForHealthyBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.Detail != "" {
		t.Errorf("detail = %q, want empty for a healthy binding", got.Detail)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "detail") {
		t.Errorf("detail must be omitempty, got %s", raw)
	}
}

// orphaned also collapses into NEEDS YOU but is not overloaded, so it gets no
// detail. This pins the scope decision.
func TestStatusOmitsDetailForOrphanedBinding(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.State = store.StateOrphaned
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f.agents = nil

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if d := rep.Bindings[0].Detail; d != "" {
		t.Errorf("detail = %q, want empty for orphaned", d)
	}
}
```

The file already imports `context`, `encoding/json`, `strings`, `testing`, `herdr` and `store`. Confirm, and add nothing that is already there.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/relay/ -run 'TestStatusDetails|TestStatusOmitsDetail' -v`
Expected: FAIL to build, with `got.Detail undefined (type BindingStatus has no field or method Detail)`.

- [ ] **Step 3: Add the field**

In `internal/relay/status.go`, in the `BindingStatus` struct, add the field immediately after `BuilderStatus`:

```go
	BuilderStatus string       `json:"builder_status"`
	// Detail explains an overloaded state where the display word cannot.
	// Populated only for store.StateBroken, which covers three situations
	// whose correct recoveries differ -- and in one of which the obvious
	// recovery orphans a builder that is still running.
	Detail        string       `json:"detail,omitempty"`
	Last          *LastEvent   `json:"last,omitempty"`
```

- [ ] **Step 4: Populate it**

In `statusRow`, immediately after the two `FindAgent` blocks and before the `rt.Store.ReadLog(b.Name)` call, add:

```go
	// Only broken is overloaded: it means "builder pane is gone", which covers
	// a clean exit, a mid-round exit, and a pane that merely moved workspaces
	// while the agent kept running. orphaned and needs_you are unambiguous.
	if b.State == store.StateBroken {
		row.Detail = DiagnoseBuilder(b).Detail(b.Round)
	}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/relay/ -run 'TestStatusDetails|TestStatusOmitsDetail' -v`
Expected: PASS, three tests.

- [ ] **Step 6: Verify the whole suite and formatting**

Run: `gofmt -l internal/relay && go test ./...`
Expected: no gofmt output; all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/relay/status.go internal/relay/status_test.go
git commit -m "feat(status): carry a detail for the overloaded broken state

broken covers three situations whose recoveries differ, and one of them is
destroyed by the recovery another needs. The field is additive and omitempty,
and displayState is unchanged: the three-state collapse is good design, the
problem was only that broken says nothing about which case it is.

Refs #41."
```

---

### Task 2: Render the line

**Files:**
- Modify: `internal/relay/status.go` (`RenderStatus`)
- Test: `internal/relay/status_test.go`

**Interfaces:**
- Consumes: `BindingStatus.Detail` from Task 1.
- Produces: nothing new. Terminal output only.

**Background you need:** `RenderStatus` writes, per binding, a header line then `  planner  …`, `  builder  …`, an optional `  last     …`, and `  pending  …`. The detail line goes after `builder` and before `last`, using the same two-space indent and the same column width as the other labels (`%-8s` worth of label padding is achieved literally, e.g. `"  detail   "`).

- [ ] **Step 1: Write the failing test**

Append to `internal/relay/status_test.go`:

```go
func TestRenderStatusShowsDetailLine(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "doctor", CWD: "/repo", Workspace: "wM", Round: 3,
		Display:      "NEEDS YOU",
		BuilderAlias: "abuilder",
		PlannerPane:  "wM:p1", PlannerKind: "claude", PlannerStatus: "idle",
		BuilderPane: "wM:pV", BuilderKind: "agy", BuilderStatus: "gone",
		Detail: "round 2 report delivered; nothing outstanding -- unless you want another round",
	}}})

	if !strings.Contains(out, "  detail   round 2 report delivered") {
		t.Errorf("detail line missing from:\n%s", out)
	}
	// It must sit between the builder line and pending, where a human about to
	// rebind is already looking.
	builderAt := strings.Index(out, "  builder ")
	detailAt := strings.Index(out, "  detail ")
	pendingAt := strings.Index(out, "  pending ")
	if !(builderAt < detailAt && detailAt < pendingAt) {
		t.Errorf("detail must follow builder and precede pending, got:\n%s", out)
	}
}

func TestRenderStatusOmitsEmptyDetail(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "ok", CWD: "/repo", Round: 1, Display: "ACTIVE",
		PlannerPane: "wM:p1", PlannerKind: "claude", PlannerStatus: "idle",
		BuilderPane: "wM:p2", BuilderKind: "agy", BuilderStatus: "working",
		BuilderAlias: "abuilder",
	}}})

	if strings.Contains(out, "detail") {
		t.Errorf("no detail line may appear for a healthy binding:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/relay/ -run TestRenderStatus -v`
Expected: FAIL — `TestRenderStatusShowsDetailLine` reports "detail line missing".

- [ ] **Step 3: Write minimal implementation**

In `RenderStatus`, immediately after the `builder` `Fprintf` and before the `if b.Last != nil` block:

```go
		if b.Detail != "" {
			fmt.Fprintf(&sb, "  detail   %s\n", b.Detail)
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/relay/ -run TestRenderStatus -v`
Expected: PASS.

- [ ] **Step 5: Verify the whole suite and formatting**

Run: `gofmt -l internal/relay && go test ./...`
Expected: no gofmt output; all PASS. Any pre-existing golden/render test that broke means a non-broken binding changed shape — that is a bug in this task, not a stale golden. Fix the code, not the golden.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/status.go internal/relay/status_test.go
git commit -m "feat(status): render the detail line under a broken binding

Placed between builder and pending, which is where the human about to run
bind --resume --builder is already looking.

Closes #41."
```

---

## Done when

- `go test ./...` passes.
- `relay status` on a broken binding shows a `detail` line; on any other binding the output is unchanged.
- `relay status --json` carries `detail` only for broken bindings.
- `displayState` is untouched — verify with `git diff main...HEAD -- internal/relay/status.go` and confirm the `displayState` function body does not appear in the diff.

## Manual check (optional, needs a live herdr)

```bash
relay status            # note a healthy binding renders as before
relay status --json | jq '.bindings[] | {name, state, detail}'
```
