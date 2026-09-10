# Consults Task 2: consult schema

> Executing **one task** from a larger plan. Do not implement any other task,
> and do not "helpfully" add a neighbouring one -- other builders work those in
> their own worktrees and your edits would race.

**Full plan:** `docs/plans/2026-09-10-consults.md` (this is Task 2 of 8)
**Design spec:** `docs/specs/2026-09-10-consults-design.md`
**Issue:** #36

Both are in your worktree. Read the spec section a step cites when a step's
rationale is not obvious -- the plan tells you what, the spec tells you why.

## Prerequisites -- check these FIRST

This task builds on work another builder already landed. Your worktree was cut
from a branch that should already contain Task 1 (FIFO queue). Verify before you start:

```bash
grep -q 'func (t \*Tx) ConfirmIndex' internal/store/log.go   # Task 1 (FIFO queue)
```

If any check fails, **stop immediately and report it**. Your base is wrong, and
every step below will fail in confusing ways. This is not something to work
around by implementing the missing piece yourself -- that is another task's
work and would collide with it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan. A halt that surfaces a design error
is worth more than a green suite that worked around one.

## Running commands

`make` is intercepted on this laptop and runs on the desktop. Use
`dev run make check`, and `dev run go test ./... -run ...` for single tests.

## Global Constraints

- Verification is `make check`, never `go test ./...` alone. It adds `gofmt -l .` over the whole tree, `go vet`, and a `go mod tidy` check.
- Go stdlib only. Do not add a module dependency; `go mod tidy` must produce no diff.
- Compose relay config paths through `userConfigRoot()` (`cmd/relay/main.go`), never by hand.
- relay makes no judgements. The only gate on a consult is `does the findings file exist`.
- "Read-only" is a property of the role's configuration, never a claim relay enforces. Do not write a comment or a doc line saying relay prevents writes.
- Every new exported symbol gets a doc comment saying what it does and, where a choice was made, why. Match the existing house style: comments explain rationale, not mechanics.
- Existing `bind.json` files must load, round-trip and re-serialise unchanged until a consult exists on them. Both new `Binding` fields carry `omitempty`.
- Commit when the task's steps are all done. Do not push, and do not open a PR.

- You are already in your own git worktree on your own branch. Do not create
  another branch and do not switch branches.

---

### Task 2: Consult schema

Types and constants only. No behaviour changes, nothing reads these yet.

**Files:**
- Modify: `internal/store/types.go`
- Modify: `internal/store/log.go:20-38` (Direction and Kind constants)
- Modify: `internal/store/store.go:118-152` (path helpers)
- Test: `internal/store/types_test.go`, `internal/store/store_test.go`

**Interfaces:**
- Consumes: Task 1's log.
- Produces:
  - `store.ConsultState` with `ConsultRunning`, `ConsultDone`, `ConsultSilent`
  - `store.Consult` struct (fields below)
  - `Binding.Consults []Consult`, `Binding.ConsultCap int`
  - `store.KindAsk`, `store.KindFindings`, `store.DirToConsult`
  - `func (s *Store) AskPath(name string, round int, id string) string`
  - `func (s *Store) FindingsPath(name string, round int, id string) string`
  - `const DefaultConsultCap = 8`

- [ ] **Step 1: Write the failing tests**

Add to `internal/store/types_test.go`:

```go
func TestBindingWithoutConsultsSerialisesWithoutTheKeys(t *testing.T) {
	// Every bind.json already on disk was written before consults existed.
	// Loading and re-saving one must not add keys to it.
	b := newBinding("webshop", "/repo")

	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"consults", "consult_cap"} {
		if bytes.Contains(raw, []byte(key)) {
			t.Errorf("empty binding serialised %q; both fields need omitempty", key)
		}
	}
}

func TestConsultRoundTripsThroughJSON(t *testing.T) {
	b := newBinding("webshop", "/repo")
	b.ConsultCap = 4
	b.Consults = []Consult{{
		ID:           "7f2a3c1d",
		Role:         "reviewer",
		Endpoint:     Endpoint{AgentName: "webshop-reviewer-7f2a3c1d", PaneID: "w2:p9", Kind: "claude"},
		Round:        3,
		AskPath:      "/state/webshop/003-7f2a3c1d-ask.md",
		FindingsPath: "/state/webshop/003-7f2a3c1d-findings.md",
		State:        ConsultRunning,
		SpawnedAt:    time.Unix(1757000000, 0).UTC(),
	}}

	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Binding
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(got.Consults) != 1 {
		t.Fatalf("got %d consults, want 1", len(got.Consults))
	}
	if got.Consults[0] != b.Consults[0] {
		t.Errorf("consult did not round-trip:\n got %+v\nwant %+v", got.Consults[0], b.Consults[0])
	}
	if got.ConsultCap != 4 {
		t.Errorf("ConsultCap = %d, want 4", got.ConsultCap)
	}
}
```

Add to `internal/store/store_test.go`:

```go
func TestConsultPathsCarryRoundAndID(t *testing.T) {
	s := New("/state")

	if got, want := s.AskPath("webshop", 3, "7f2a3c1d"), "/state/webshop/003-7f2a3c1d-ask.md"; got != want {
		t.Errorf("AskPath = %q, want %q", got, want)
	}
	if got, want := s.FindingsPath("webshop", 12, "7f2a3c1d"), "/state/webshop/012-7f2a3c1d-findings.md"; got != want {
		t.Errorf("FindingsPath = %q, want %q", got, want)
	}
	// NNN-question.md belongs to the blocked-dialog capture. A consult being
	// asked something is not a builder being blocked on something.
	if s.AskPath("webshop", 3, "7f2a3c1d") == s.QuestionPath("webshop", 3) {
		t.Error("AskPath collides with QuestionPath")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/store/ -run 'TestBindingWithoutConsults|TestConsultRoundTrips|TestConsultPaths' -v
```

Expected: FAIL to compile — `Consult`, `ConsultRunning`, `AskPath`, `FindingsPath` undefined.

- [ ] **Step 3: Add the types**

Append to `internal/store/types.go`:

```go
// DefaultConsultCap bounds how many consults may be RUNNING on one binding at
// once. It exists for the reason RoundCap does: an idle harness pane holds
// roughly 800 MB, so an unbounded fan-out is a memory failure, not a workspace.
const DefaultConsultCap = 8

// ConsultState is where one consult has got to.
//
// done and silent are both terminal and both reapable. A finished consult's
// record is NOT dropped, because `relay reap` needs the pane id to close it:
// Binding.Consults is the reap worklist as well as the watch list.
type ConsultState string

const (
	ConsultRunning ConsultState = "running" // spawned; no findings yet
	ConsultDone    ConsultState = "done"    // findings queued to the planner
	ConsultSilent  ConsultState = "silent"  // gave up; "no findings" reported
)

// Consult is one ephemeral, read-only, one-shot agent attached to a binding.
//
// It is deliberately not a Binding. A consult has no round counter, no diff
// baseline, no worktree, no screen fingerprint and no persistent session, and
// modelling it as a Binding would leave every one of those fields dead while
// forcing Status, gc, Fork, doctor and the UI to learn to filter it out.
//
// "Read-only" describes how the role is configured, not something relay
// enforces: `herdr agent list` reports a kind, a status, a cwd and a title, and
// nothing more, so relay cannot observe writes.
type Consult struct {
	// ID is 8 lowercase hex characters, unique within one binding. It appears
	// in the log, in both filenames, and in `relay reap`, so it is short enough
	// to read aloud.
	ID string `json:"id"`

	// Role is the alias name that was asked, recorded as a name rather than a
	// resolved spec: the spec can change under the record, and the record
	// should stay truthful about what was intended.
	Role string `json:"role"`

	Endpoint Endpoint `json:"endpoint"`

	// Round is the owning binding's round at spawn. It is a label for audit and
	// filenames and nothing else -- a consult never advances a round, never
	// stamps RoundStartedAt, and never touches a diff baseline.
	Round int `json:"round"`

	AskPath      string `json:"ask_path"`
	FindingsPath string `json:"findings_path"`

	State ConsultState `json:"state"`

	SpawnedAt time.Time `json:"spawned_at"`

	// NudgedAt is when relay sent this consult its single nudge; zero means it
	// has not been nudged. A consult runs for a minute or two, so it does not
	// inherit the builder's screen-fingerprint quiescence or scrape fallback.
	NudgedAt time.Time `json:"nudged_at,omitempty"`

	// Note is why a silent consult gave up. Empty for running and done.
	Note string `json:"note,omitempty"`
}
```

Add to the `Binding` struct in the same file, after `ForkedAtRound` and before `CreatedAt`:

```go
	// Consults are the read-only one-shot agents attached to this binding,
	// running and awaiting-reap alike. omitempty keeps every bind.json written
	// before consults existed byte-identical until its first consult.
	Consults []Consult `json:"consults,omitempty"`

	// ConsultCap bounds RUNNING consults; zero means DefaultConsultCap.
	ConsultCap int `json:"consult_cap,omitempty"`
```

- [ ] **Step 4: Add the log constants**

In `internal/store/log.go`, add to the `Direction` block (`:23-26`):

```go
	// DirToConsult is an outbound message to a consult. It is additive: every
	// consumer of Direction tests equality against a specific value, and there
	// is no exhaustive switch in the tree. Reusing DirToBuilder would instead
	// redefine what a persisted value means.
	DirToConsult Direction = "to_consult"
```

And to the `Kind` block (`:30-38`):

```go
	KindAsk      Kind = "ask"      // planner -> consult, the staged question
	KindFindings Kind = "findings" // consult -> planner, the findings path
```

- [ ] **Step 5: Add the path helpers**

Append to `internal/store/store.go` after `DriftPath` (`:150`):

```go
// AskPath is where a consult's question is staged.
// Layout: <binding dir>/NNN-<id>-ask.md
//
// Deliberately not NNN-question.md: that name belongs to QuestionPath, the
// blocked-dialog capture, and a consult being asked a question is a different
// event from a builder being blocked on one.
func (s *Store) AskPath(name string, round int, id string) string {
	return s.consultFile(name, round, id, "ask", ".md")
}

// FindingsPath is where a consult is told to write. Its existence is the entire
// completion gate.
// Layout: <binding dir>/NNN-<id>-findings.md
func (s *Store) FindingsPath(name string, round int, id string) string {
	return s.consultFile(name, round, id, "findings", ".md")
}

// consultFile is roundFile with a consult id folded in. roundFile takes no id,
// and widening it would touch five call sites that will never have one.
func (s *Store) consultFile(name string, round int, id, suffix, ext string) string {
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return filepath.Join(s.Dir(name), fmt.Sprintf("%03d-%s-%s%s", round, id, suffix, ext))
}
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
make check
```

Expected: PASS. Add `bytes`, `encoding/json` and `time` imports to `types_test.go` if they are not already there.

- [ ] **Step 7: Commit**

```bash
git add internal/store/
git commit -m "feat(store): add the consult record and its log vocabulary

A consult is a record on its owning binding, not a binding: it has no round
counter, no diff baseline, no worktree and no persistent session, and every
one of those fields would be dead weight on a Binding.

Both new Binding fields carry omitempty, so a bind.json written before this
change round-trips byte-identically until its first consult."
```
