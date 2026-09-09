# Plan A: BuilderDiagnosis Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one pure function that derives the two facts distinguishing the three situations `store.StateBroken` collapses together, plus the sentence `relay status` will show.

**Architecture:** A new file `internal/relay/diagnose.go` holding a two-field struct, a constructor that reads only the stored `store.Binding`, and a `Detail` method composing a sentence from two independent clauses. No herdr call, no log read, no store schema change. Plans B and C both consume this and neither can start until it lands.

**Tech Stack:** Go. Standard library only (`fmt`). Tests are stdlib `testing` with table-driven subtests, matching the existing style in `internal/relay/*_test.go`.

**Spec:** `docs/specs/2026-09-09-broken-binding-recovery-design.md`, sections 3 and 6.1-6.2.

## Global Constraints

- Package is `relay`, in the existing `internal/relay/` directory. Do not create a new package.
- `DiagnoseBuilder` must be **pure**: it takes `store.Binding` by value and touches nothing else. No `Runtime`, no `context.Context`, no I/O.
- `Detail` is **total**: every combination returns a non-empty string. A caller must never need to handle `""` as a special case.
- Do not modify `displayState`, `store.State`, or any store type in this plan.
- Exact strings matter: Plan B asserts them. Copy them character for character, including the ASCII double hyphen `--` (not an em dash).
- Run `go test ./...` from the repo root. Run `gofmt -l internal/relay` and expect no output before each commit.

---

### Task 1: The two facts

**Files:**
- Create: `internal/relay/diagnose.go`
- Test: `internal/relay/diagnose_test.go`

**Interfaces:**
- Consumes: `github.com/fuad-daoud/relay/internal/store` (`store.Binding`, `store.Endpoint`).
- Produces: `BuilderDiagnosis{RoundOpen bool; SessionIdentified bool}` and `DiagnoseBuilder(b store.Binding) BuilderDiagnosis`. Task 2 adds a method to this struct; Plans B and C both call `DiagnoseBuilder`.

**Background you need:** `Binding.RoundStartedAt` is stamped in exactly one place (`Send`, `internal/relay/send.go:122`) and cleared in exactly one place (`queueReport`, `internal/relay/reconcile.go:430`, after the round's report is logged and `b.Round` is incremented). So a zero value means "no round is in flight". `reconcile.go:269` already relies on this same meaning. `Builder.SessionID` is backfilled by `refreshEndpoint` the first time the endpoint is located and the agent reports a session, and is never overwritten once set.

- [ ] **Step 1: Write the failing test**

Create `internal/relay/diagnose_test.go`:

```go
package relay

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

func TestDiagnoseBuilderDerivesBothFacts(t *testing.T) {
	sent := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		binding  store.Binding
		wantOpen bool
		wantSess bool
	}{
		{
			name: "round in flight and session recorded",
			binding: store.Binding{
				RoundStartedAt: sent,
				Builder:        store.Endpoint{PaneID: "w2:p4", SessionID: "sess-1"},
			},
			wantOpen: true,
			wantSess: true,
		},
		{
			name: "round closed and session recorded",
			binding: store.Binding{
				Builder: store.Endpoint{PaneID: "w2:p4", SessionID: "sess-1"},
			},
			wantOpen: false,
			wantSess: true,
		},
		{
			name: "round in flight and no session",
			binding: store.Binding{
				RoundStartedAt: sent,
				Builder:        store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			},
			wantOpen: true,
			wantSess: false,
		},
		{
			name: "round closed and no session",
			binding: store.Binding{
				Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			},
			wantOpen: false,
			wantSess: false,
		},
		{
			name:     "zero binding does not panic",
			binding:  store.Binding{},
			wantOpen: false,
			wantSess: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DiagnoseBuilder(tc.binding)
			if got.RoundOpen != tc.wantOpen {
				t.Errorf("RoundOpen = %v, want %v", got.RoundOpen, tc.wantOpen)
			}
			if got.SessionIdentified != tc.wantSess {
				t.Errorf("SessionIdentified = %v, want %v", got.SessionIdentified, tc.wantSess)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/relay/ -run TestDiagnoseBuilderDerivesBothFacts -v`
Expected: FAIL to build, with `undefined: DiagnoseBuilder`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/relay/diagnose.go`:

```go
package relay

import (
	"github.com/fuad-daoud/relay/internal/store"
)

// BuilderDiagnosis explains a broken binding: what was at stake when the
// builder went away, and whether relay can trust that it is really gone.
//
// store.StateBroken is documented as "builder pane is gone" and covers three
// situations relay does not otherwise distinguish -- a builder that exited
// after a clean round, one that exited mid-round, and one whose pane merely
// moved between workspaces while the agent kept running. The first needs no
// action; the third is destroyed by the action the second needs.
//
// Both fields are derived from the stored binding alone: no herdr call and no
// log read, so a diagnosis costs nothing and cannot disagree with the binding
// it came from.
type BuilderDiagnosis struct {
	// RoundOpen reports that a round was handed to the builder and no report
	// came back, so that round's work is unaccounted for.
	RoundOpen bool

	// SessionIdentified reports that herdr has recorded a session for the
	// builder. When false, SameAgent has been matching on pane plus kind, and
	// a workspace move changes the pane id -- so a failed match cannot be
	// distinguished from a moved pane, and the builder may still be alive.
	SessionIdentified bool
}

// DiagnoseBuilder derives the diagnosis for a binding. Pure.
//
// RoundStartedAt is stamped only by Send at handoff and cleared only by
// queueReport once the round's report is logged, so a zero value means no
// round is in flight. SessionID is backfilled by refreshEndpoint the first
// time the builder is located carrying a session, and never overwritten, so
// an empty value means herdr has never reported one.
func DiagnoseBuilder(b store.Binding) BuilderDiagnosis {
	return BuilderDiagnosis{
		RoundOpen:         !b.RoundStartedAt.IsZero(),
		SessionIdentified: b.Builder.SessionID != "",
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/relay/ -run TestDiagnoseBuilderDerivesBothFacts -v`
Expected: PASS, five subtests.

- [ ] **Step 5: Verify nothing else broke and formatting is clean**

Run: `gofmt -l internal/relay && go test ./...`
Expected: no gofmt output; all packages PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/diagnose.go internal/relay/diagnose_test.go
git commit -m "feat(relay): derive the two facts a broken binding turns on

RoundOpen and SessionIdentified are orthogonal and both readable from the
stored binding, so a diagnosis needs no herdr call and no log read.

Refs #41, #21."
```

---

### Task 2: The detail sentence

**Files:**
- Modify: `internal/relay/diagnose.go`
- Test: `internal/relay/diagnose_test.go`

**Interfaces:**
- Consumes: `BuilderDiagnosis` from Task 1.
- Produces: `(BuilderDiagnosis) Detail(round int) string`. Plan B calls this as `DiagnoseBuilder(b).Detail(b.Round)` and asserts the exact strings below.

**Background you need:** `queueReport` increments `b.Round` **after** logging the round's report. So when no round is open, the report that was delivered belongs to round `round-1`. A binding that was bound but never sent has `round == 1` and no open round, and naming "round 0" there would be naming a report that does not exist. That is why the stake clause is a three-way split, not two.

- [ ] **Step 1: Write the failing test**

Append to `internal/relay/diagnose_test.go`:

```go
func TestBuilderDiagnosisDetail(t *testing.T) {
	const moved = ", and the builder was never session-identified, " +
		"so it may be alive in a moved pane -- verify before rebinding"

	tests := []struct {
		name  string
		d     BuilderDiagnosis
		round int
		want  string
	}{
		{
			name:  "round open, identified",
			d:     BuilderDiagnosis{RoundOpen: true, SessionIdentified: true},
			round: 3,
			want:  "round 3 was open -- that work is unaccounted for; rebind and resend the round",
		},
		{
			name:  "round open, unidentified",
			d:     BuilderDiagnosis{RoundOpen: true},
			round: 3,
			want:  "round 3 was open -- that work is unaccounted for" + moved,
		},
		{
			name:  "report delivered, identified",
			d:     BuilderDiagnosis{SessionIdentified: true},
			round: 3,
			want:  "round 2 report delivered; nothing outstanding -- unless you want another round",
		},
		{
			name:  "report delivered, unidentified",
			d:     BuilderDiagnosis{},
			round: 3,
			want:  "round 2 report delivered; nothing outstanding" + moved,
		},
		{
			name:  "never sent, identified",
			d:     BuilderDiagnosis{SessionIdentified: true},
			round: 1,
			want:  "no round has been sent yet; nothing outstanding -- unless you want to send one",
		},
		{
			name:  "never sent, unidentified",
			d:     BuilderDiagnosis{},
			round: 1,
			want:  "no round has been sent yet; nothing outstanding" + moved,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.d.Detail(tc.round)
			if got != tc.want {
				t.Errorf("Detail(%d) =\n  %q\nwant\n  %q", tc.round, got, tc.want)
			}
		})
	}
}

// A binding bound but never sent has Round 1, and queueReport's increment
// means the "delivered report" wording would name round 0 -- a report that
// does not exist. That binding is also the one whose builder has taken no
// turn, so it is exactly the unidentified case this detail exists to warn
// about; it must not be papered over with a nonsense round number.
func TestDetailNeverNamesRoundZero(t *testing.T) {
	for _, d := range []BuilderDiagnosis{{}, {SessionIdentified: true}} {
		got := d.Detail(1)
		if strings.Contains(got, "round 0") {
			t.Errorf("Detail(1) = %q, must not name round 0", got)
		}
		if got == "" {
			t.Error("Detail must be total, got empty string")
		}
	}
}

func TestDetailIsTotal(t *testing.T) {
	for _, round := range []int{0, 1, 2, 7} {
		for _, open := range []bool{true, false} {
			for _, sess := range []bool{true, false} {
				d := BuilderDiagnosis{RoundOpen: open, SessionIdentified: sess}
				if d.Detail(round) == "" {
					t.Errorf("Detail(%d) empty for %+v", round, d)
				}
			}
		}
	}
}
```

Add `"strings"` to that file's import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/relay/ -run 'TestBuilderDiagnosisDetail|TestDetail' -v`
Expected: FAIL to build, with `d.Detail undefined`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/relay/diagnose.go`, and add `"fmt"` to its imports:

```go
// movedPaneWarning is the clause relay adds when it cannot tell a dead builder
// from one whose pane moved between workspaces. It is the case where the
// documented recovery -- `relay bind --resume --builder` -- is the harm, so it
// is stated wherever a human is about to choose one.
const movedPaneWarning = "the builder was never session-identified, " +
	"so it may be alive in a moved pane -- verify before rebinding"

// Detail renders the sentence `relay status` shows beneath a broken binding.
//
// The sentence is composed from two independent clauses rather than
// enumerated, because RoundOpen and SessionIdentified vary independently.
//
// Detail is total: every combination yields a non-empty sentence, so a caller
// never has to treat an empty return as a special case.
//
// round is the binding's current round. queueReport increments Round after
// logging a report, so a closed round's report belongs to round-1 -- and a
// binding that has never been sent has no delivered report to name at all.
func (d BuilderDiagnosis) Detail(round int) string {
	var stake string
	switch {
	case d.RoundOpen:
		stake = fmt.Sprintf("round %d was open -- that work is unaccounted for", round)
	case round > 1:
		stake = fmt.Sprintf("round %d report delivered; nothing outstanding", round-1)
	default:
		stake = "no round has been sent yet; nothing outstanding"
	}

	if !d.SessionIdentified {
		return stake + ", and " + movedPaneWarning
	}
	if d.RoundOpen {
		// Only safe to advise when the builder is positively identifiable:
		// rebinding an unidentified one is what orphans a live builder.
		return stake + "; rebind and resend the round"
	}
	if round > 1 {
		return stake + " -- unless you want another round"
	}
	return stake + " -- unless you want to send one"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/relay/ -run 'TestBuilderDiagnosisDetail|TestDetail' -v`
Expected: PASS.

- [ ] **Step 5: Verify the whole suite and formatting**

Run: `gofmt -l internal/relay && go test ./...`
Expected: no gofmt output; all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/diagnose.go internal/relay/diagnose_test.go
git commit -m "feat(relay): compose the broken-binding detail sentence

Two independent clauses rather than an enumerated table: what was at stake,
and whether relay can trust the builder is gone. The stake clause is a
three-way split because a binding bound but never sent has no delivered
report to name, and that binding is exactly the unidentified case.

Refs #41, #21."
```

---

## Done when

- `go test ./...` passes.
- `DiagnoseBuilder` and `Detail` exist and are exported from package `relay`.
- Nothing outside `internal/relay/diagnose.go` and its test has changed. Verify with `git diff --stat main...HEAD` -- exactly two files.

Plans B and C can now start, and are independent of each other.
