# Headless builders, step 1: `store` -- `Mode`, endpoint fields, `BuilderLogPath`, `KindExit` (#99)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #99. **Spec:** `docs/specs/2026-09-12-headless-builders-design.md`
-- read §1, §3.1-§3.3, §3.8 and §7 step 1 before starting; section numbers
below refer to it. Steps 2 and 3 of the spec are being built concurrently in
other worktrees and touch `internal/harness`, `internal/proc` and
`internal/relay` only; this plan touches `internal/store` only, so the three
merge without conflict.
**Depends on:** nothing open.

**Goal:** the store can describe a headless builder -- an endpoint with a
mode, a pid, a start time and a log path -- and knows where a round's builder
log lives, without changing how any existing binding reads or writes.

**Architecture:** pure data. `store.Endpoint` gains `Mode`, `PID`,
`StartedAt` and `LogPath`, every one `omitempty`, so a `bind.json` written
before this change is byte-identical after a load/save cycle and reads as a
pane builder. `Store.BuilderLogPath` is one more `roundFile` sibling of
`PlanPath`/`ReportPath`, so `fork` copies it (it copies by leading round
number) and `gc` archives it with the directory. `KindExit` is one more log
kind constant. No behaviour anywhere else changes.

**One deviation from the spec, decided by the planner:** §3.1 gives
`StartedAt` as `time.Time`. Here it is **`int64` Unix seconds**
(`json:"started_at,omitempty"`). `encoding/json` never omits a struct, so a
`time.Time` field on `Endpoint` would write `"started_at":"0001-01-01T00:00:00Z"`
into the planner endpoint and every consult endpoint of every binding on
its next save -- exactly the churn the `Consult.NudgedAt` comment in
`types.go` warns about. The OS reports process start time at one-second
resolution anyway (spec §4.1 compares "within one second"), so nothing is
lost. Step 5 converts with `time.Unix(e.StartedAt, 0)`. Task 3 amends the
spec to say so.

**Tech stack:** Go 1.22, standard library only. Verification is `make check`.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-t1` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/headless-t1`, cut from `main`. |
| `~/.local/state/relay/headless-t1` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

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

Do **not** run `herdr` yourself.

## Global constraints

- Only `internal/store/types.go`, `internal/store/log.go`,
  `internal/store/store.go`, their tests, and the spec file change.
- Every new `Endpoint` field carries `omitempty`. A binding saved by today's
  relay must load and re-save byte-identically (Task 1 Step 4 pins it).
- No validation of the §3.1 invariants in `store`: the store is data; the
  `relay` package enforces them when it builds an endpoint (steps 4-5).
- `go.mod`/`go.sum` do not change.
- One commit per task, on the worktree's branch.

---

### Task 1: `Mode`, the endpoint fields, `Headless()`

**Files:**
- Modify: `internal/store/types.go` (`Mode`, constants, `Endpoint` fields, `Headless`)
- Modify: `internal/store/types_test.go` (new tests appended)

**Interfaces:**
- Consumes: nothing new.
- Produces (steps 4-7 of the spec build on these exact names):

```go
type Mode string
const (
	ModePane     Mode = "pane"
	ModeHeadless Mode = "headless"
)
// Endpoint gains:
Mode      Mode   `json:"mode,omitempty"`
PID       int    `json:"pid,omitempty"`
StartedAt int64  `json:"started_at,omitempty"` // Unix seconds
LogPath   string `json:"log_path,omitempty"`
func (e Endpoint) Headless() bool
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/types_test.go`:

```go
func TestEndpointHeadless(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
		want bool
	}{
		{"empty mode reads as pane", "", false},
		{"explicit pane", ModePane, false},
		{"headless", ModeHeadless, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := Endpoint{Kind: "agy", Mode: c.mode}
			if got := e.Headless(); got != c.want {
				t.Errorf("Headless() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestEndpointHeadlessFieldsRoundTripAndAreOmittedWhenZero(t *testing.T) {
	// A pane endpoint written by today's relay carries none of the new keys.
	pane := Endpoint{AgentName: "webshop-builder", PaneID: "w1:p2", Kind: "opencode"}
	data, err := json.Marshal(pane)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"mode", "pid", "started_at", "log_path"} {
		if bytes.Contains(data, []byte(`"`+key+`"`)) {
			t.Errorf("pane endpoint JSON carries %q: %s", key, data)
		}
	}

	// A headless endpoint carries all four and reads back equal.
	want := Endpoint{
		AgentName: "webshop-builder",
		Kind:      "agy",
		Mode:      ModeHeadless,
		PID:       4242,
		StartedAt: 1789000000,
		LogPath:   "/state/webshop/003-builder.log",
	}
	data, err = json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Endpoint
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
	if !got.Headless() {
		t.Error("decoded headless endpoint reports Headless() false")
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal map: %v", err)
	}
	if decoded["mode"] != "headless" || decoded["log_path"] != want.LogPath {
		t.Errorf("JSON keys: %s", data)
	}
	if decoded["pid"] != float64(4242) || decoded["started_at"] != float64(1789000000) {
		t.Errorf("pid/started_at must be JSON numbers: %s", data)
	}
}
```

`bytes`, `encoding/json` and `time` are already imported by
`types_test.go`; if `time` becomes unused by your edit it was not -- do not
remove imports the existing tests use.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/store -run 'TestEndpointHeadless'`
Expected: compile error -- `undefined: Mode`, `e.Headless undefined`.

- [ ] **Step 3: The type and the fields**

In `internal/store/types.go`, replace the `Endpoint` declaration and its
comment with:

```go
// Mode is the shape of a builder: a herdr pane relay watches, or a process
// relay runs itself (#99). "" reads as pane so every binding written before
// the field existed is unchanged.
type Mode string

const (
	ModePane     Mode = "pane"
	ModeHeadless Mode = "headless"
)

// Endpoint is one side of a binding. PaneID moves when a pane is moved between
// workspaces; SessionID does not, so it is the durable identity.
//
// A headless builder (#99) has no pane and no session: Mode is ModeHeadless,
// PaneID and SessionID are "", and the process fields below describe the
// current round's process -- PID 0 between rounds. Every one of them is
// omitempty so a pane endpoint's JSON is byte-identical to what it was.
type Endpoint struct {
	AgentName string `json:"agent_name,omitempty"`
	PaneID    string `json:"pane_id"`
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind"`

	Mode Mode `json:"mode,omitempty"`
	// PID is the headless supervisor's pid; 0 when no process is running.
	PID int `json:"pid,omitempty"`
	// StartedAt is the process's start time as the OS reports it, in Unix
	// seconds, for pid-reuse defence. Seconds, not time.Time: encoding/json
	// never omits a struct, and the OS reports start time at one-second
	// resolution anyway.
	StartedAt int64 `json:"started_at,omitempty"`
	// LogPath is the current round's builder log (Store.BuilderLogPath);
	// "" between rounds.
	LogPath string `json:"log_path,omitempty"`
}

// Headless reports whether this endpoint is a process relay runs rather than
// a pane it watches. "" is pane.
func (e Endpoint) Headless() bool { return e.Mode == ModeHeadless }
```

- [ ] **Step 4: Pin byte-identity of an existing binding**

Append to `internal/store/store_test.go`:

```go
func TestLegacyPaneBindingReSavesByteIdentical(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/projects/webshop")
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(s.Dir("webshop"), "bind.json"))
	if err != nil {
		t.Fatalf("read bind.json: %v", err)
	}
	for _, key := range []string{`"mode"`, `"pid"`, `"started_at"`, `"log_path"`} {
		if bytes.Contains(before, []byte(key)) {
			t.Errorf("a pane binding's bind.json must not carry %s:\n%s", key, before)
		}
	}
	got, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Builder.Headless() || got.Builder.Mode != "" {
		t.Errorf("legacy builder must read as pane with empty Mode: %+v", got.Builder)
	}
	if got.Builder.PID != 0 || got.Builder.StartedAt != 0 || got.Builder.LogPath != "" {
		t.Errorf("legacy builder must have zero process fields: %+v", got.Builder)
	}
}
```

If `bind.json` is not the file name the store writes, look at `Save` in
`store.go` for the actual name and use it; if `bytes` or `os` is not yet
imported by `store_test.go`, add it. If `newBinding` is not the fixture's
name, stop and report.

- [ ] **Step 5: Run the package tests**

Run: `go test -count=1 ./internal/store`
Expected: PASS -- the three new tests and every existing one.

- [ ] **Step 6: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/store/types.go internal/store/types_test.go internal/store/store_test.go
git commit -m "feat(store): Endpoint.Mode, PID, StartedAt, LogPath for headless builders (#99 step 1)"
```

---

### Task 2: `BuilderLogPath` and `KindExit`

**Files:**
- Modify: `internal/store/store.go` (`BuilderLogPath`, beside `ReportPath`)
- Modify: `internal/store/log.go` (`KindExit`)
- Modify: `internal/store/store_test.go`, `internal/store/log_test.go`

**Interfaces:**
- Produces:

```go
func (s *Store) BuilderLogPath(name string, round int) string // <state>/<name>/NNN-builder.log
const KindExit Kind = "exit"
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/store_test.go`:

```go
func TestBuilderLogPathIsARoundFileBesideTheReport(t *testing.T) {
	s := New("/state")
	if got, want := s.BuilderLogPath("webshop", 3), filepath.Join("/state", "webshop", "003-builder.log"); got != want {
		t.Errorf("BuilderLogPath = %q, want %q", got, want)
	}
	// roundOfFile is what ForkState uses to decide which files to copy; the
	// log must be one of them.
	if r, ok := roundOfFile(filepath.Base(s.BuilderLogPath("webshop", 12))); !ok || r != 12 {
		t.Errorf("roundOfFile(012-builder.log) = %d, %v; want 12, true", r, ok)
	}
}
```

If `roundOfFile` has a different name or signature in `fork.go`, use the
real one; its job is "leading round number of a file in a binding dir". If
no such helper exists, stop and report.

Append to `internal/store/log_test.go`:

```go
func TestKindExitIsDistinct(t *testing.T) {
	kinds := []Kind{KindPlan, KindReport, KindQuestion, KindAnswer, KindDiff, KindDrift, KindFork, KindPick, KindSwitch, KindAsk, KindFindings, KindExit}
	seen := map[Kind]bool{}
	for _, k := range kinds {
		if seen[k] {
			t.Errorf("duplicate kind %q", k)
		}
		seen[k] = true
	}
	if KindExit != "exit" {
		t.Errorf("KindExit = %q, want exit", KindExit)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/store -run 'TestBuilderLogPath|TestKindExit'`
Expected: compile error -- `s.BuilderLogPath undefined`, `undefined: KindExit`.

- [ ] **Step 3: Implement**

In `internal/store/store.go`, directly after `ReportPath`:

```go
// BuilderLogPath is where a headless builder's stdout and stderr for a round
// are appended (#99). A round file like the plan and the report, so fork
// copies it with the history and gc archives it with the directory.
// Layout: <binding dir>/NNN-builder.log
func (s *Store) BuilderLogPath(name string, round int) string {
	return s.roundFile(name, round, "builder", ".log")
}
```

In `internal/store/log.go`, in the `Kind` const block, after `KindSwitch`:

```go
	KindExit     Kind = "exit"   // relay -> log only: a headless builder exited without a report (#99)
```

Keep the block `gofmt`-aligned.

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./internal/store`
Expected: PASS.

- [ ] **Step 5: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/store
git commit -m "feat(store): BuilderLogPath and KindExit for headless builders (#99 step 1)"
```

---

### Task 3: spec amendment

**Files:**
- Modify: `docs/specs/2026-09-12-headless-builders-design.md` (§3.1)

- [ ] **Step 1: Record the `StartedAt` decision**

In §3.1, replace the line

```
  StartedAt time.Time headless only; the process's start time as the OS reports it, for pid-reuse defence
```

with

```
  StartedAt int64     headless only; the process's start time as the OS reports it, Unix seconds, for
                      pid-reuse defence. (Amended at step 1: seconds rather than time.Time, because
                      encoding/json never omits a struct and a time.Time here would rewrite every
                      planner and consult endpoint on its next save. ProcHandle.StartedAt stays
                      time.Time; the conversion is time.Unix(e.StartedAt, 0).)
```

Keep the surrounding code fence intact.

- [ ] **Step 2: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add docs/specs/2026-09-12-headless-builders-design.md
git commit -m "docs(spec): headless -- Endpoint.StartedAt is Unix seconds (#99 step 1)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), and `git diff --stat main..HEAD`. Confirm that no
file outside `internal/store` and the spec changed. If any step was
impossible as written, say which and why -- do not work around it.
