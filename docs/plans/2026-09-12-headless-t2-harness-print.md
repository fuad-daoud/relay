# Headless builders, step 2: `harness.Launch.Print` -- the non-interactive argv per kind (#99)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #99. **Spec:** `docs/specs/2026-09-12-headless-builders-design.md`
-- read §1, §3.5, §4.2, §4.9 and §7 step 2 before starting. Steps 1 and 3 of
the spec are being built concurrently in other worktrees and touch
`internal/store`, `internal/proc` and `internal/relay` only; this plan
touches `internal/harness` only, so the three merge without conflict.
**Depends on:** nothing open.

**Goal:** `harness.Launch` describes, for each kind, the argv that runs one
prompt non-interactively and exits -- with the prompt and the round budget as
placeholders a later step fills in -- while the interactive `Args` every
existing caller uses stays byte-for-byte what it is.

**Architecture:** `Launch` gains `Print []string` and `PromptAt int`, filled
by the same `switch h.Kind` that fills `Args`, with `extra` appended to both
forms. Two exported placeholder constants, `PromptPlaceholder = "<prompt>"`
and `BudgetPlaceholder = "<budget>"`, stand in the argv as their own
elements. A pure method `PrintArgs(prompt string, budget time.Duration)
[]string` returns a fresh slice with both substituted (`budget.String()` for
the budget), so step 5's `headlessLaunch` is `[binary] + l.PrintArgs(...)`
and no other package learns where a kind puts its prompt.

**One refinement of the spec, decided by the planner:** §3.5 names only
`PromptAt` and says "budget substitution" without saying where the budget
sits. Making the budget a placeholder element like the prompt, and giving
`Launch` the `PrintArgs` method, keeps the substitution inside the package
that owns the table. `PromptAt` is kept (it is the index of
`PromptPlaceholder` in `Print`; `-1` for an unknown kind) so §4.2 reads as
written. Task 2 amends the spec.

**Tech stack:** Go 1.22, standard library only. Verification is `make check`.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-t2` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/headless-t2`, cut from `main`. |
| `~/.local/state/relay/headless-t2` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

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

- Only `internal/harness/harness.go`, `internal/harness/harness_test.go` and
  the spec file change.
- `Launch.Args` is unchanged for every kind and every input. The existing
  `TestLaunch` is **not edited**; it must pass as it is.
- `Launch(provider, model, extra, role)` keeps its signature (spec §4.9).
- `PrintArgs` never mutates `Print` or `extra`; it returns a fresh slice.
- `go.mod`/`go.sum` do not change.
- One commit per task, on the worktree's branch.

---

### Task 1: `Print`, `PromptAt`, the placeholders and `PrintArgs`

**Files:**
- Modify: `internal/harness/harness.go` (`Launch` struct, `Launch` method, `PrintArgs`)
- Modify: `internal/harness/harness_test.go` (new tests appended)

**Interfaces:**
- Consumes: `Harness.Launch(provider, model string, extra []string, role RoleSpec) Launch` (existing).
- Produces (step 5 of the spec calls these by name):

```go
const (
	PromptPlaceholder = "<prompt>"
	BudgetPlaceholder = "<budget>"
)
type Launch struct {
	Kind     string
	Args     []string   // existing
	Print    []string   // non-interactive argv after the binary; placeholders as their own elements
	PromptAt int        // index of PromptPlaceholder in Print; -1 when Print is empty
}
func (l Launch) PrintArgs(prompt string, budget time.Duration) []string
```

The per-kind table, before `extra`:

| kind | Print |
|---|---|
| agy | `-p <prompt> --model M --agent <def> --output-format text --print-timeout <budget>` |
| claude | `-p <prompt> --model M --agent <def> --output-format text` |
| opencode | `run <prompt> -m P/M --agent <def>` |

- [ ] **Step 1: Write the failing tests**

Append to `internal/harness/harness_test.go`:

```go
func TestLaunchPrintPerKind(t *testing.T) {
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}
	tests := []struct {
		kind       string
		extra      []string
		wantPrint  []string
		wantPrompt int
	}{
		{
			kind: "agy",
			wantPrint: []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor",
				"--output-format", "text", "--print-timeout", BudgetPlaceholder},
			wantPrompt: 1,
		},
		{
			kind: "agy", extra: []string{"--dangerously-skip-permissions"},
			wantPrint: []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor",
				"--output-format", "text", "--print-timeout", BudgetPlaceholder, "--dangerously-skip-permissions"},
			wantPrompt: 1,
		},
		{
			kind:       "claude",
			wantPrint:  []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor", "--output-format", "text"},
			wantPrompt: 1,
		},
		{
			kind:       "opencode",
			wantPrint:  []string{"run", PromptPlaceholder, "-m", "prov/m/x", "--agent", "plan-executor"},
			wantPrompt: 1,
		},
		{
			kind: "opencode", extra: []string{"--auto"},
			wantPrint:  []string{"run", PromptPlaceholder, "-m", "prov/m/x", "--agent", "plan-executor", "--auto"},
			wantPrompt: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.kind+" "+strings.Join(tt.extra, " "), func(t *testing.T) {
			h, ok := Lookup(tt.kind)
			if !ok {
				t.Fatalf("Lookup(%q) not found", tt.kind)
			}
			got := h.Launch("prov", "m/x", tt.extra, builder)
			if !reflect.DeepEqual(got.Print, tt.wantPrint) {
				t.Errorf("Print = %v, want %v", got.Print, tt.wantPrint)
			}
			if got.PromptAt != tt.wantPrompt {
				t.Errorf("PromptAt = %d, want %d", got.PromptAt, tt.wantPrompt)
			}
			if got.Print[got.PromptAt] != PromptPlaceholder {
				t.Errorf("Print[PromptAt] = %q, want the placeholder", got.Print[got.PromptAt])
			}
		})
	}

	unknown := Harness{Kind: "unknown"}
	got := unknown.Launch("prov", "m/x", []string{"--z"}, builder)
	if len(got.Print) != 0 || got.PromptAt != -1 {
		t.Errorf("unknown kind: Print = %v PromptAt = %d; want empty and -1", got.Print, got.PromptAt)
	}
}

func TestPrintArgsSubstitutesPromptAndBudgetWithoutMutating(t *testing.T) {
	builder, _ := RoleByName("builder")
	h, _ := Lookup("agy")
	extra := []string{"--dangerously-skip-permissions"}
	l := h.Launch("prov", "m/x", extra, builder)
	before := append([]string(nil), l.Print...)

	prompt := "Read /state/x/003-plan.md and write /state/x/003-report.md"
	got := l.PrintArgs(prompt, 90*time.Minute)
	want := []string{"-p", prompt, "--model", "m/x", "--agent", "plan-executor",
		"--output-format", "text", "--print-timeout", "1h30m0s", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PrintArgs = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(l.Print, before) {
		t.Errorf("PrintArgs mutated Print: %v", l.Print)
	}
	if !reflect.DeepEqual(extra, []string{"--dangerously-skip-permissions"}) {
		t.Errorf("extra was modified: %v", extra)
	}
	got[0] = "changed"
	if l.Print[0] != "-p" {
		t.Error("PrintArgs must return a fresh slice, not alias Print")
	}

	// A kind with no budget flag ignores the budget; the prompt still lands.
	c, _ := Lookup("claude")
	cl := c.Launch("prov", "m/x", nil, builder)
	got = cl.PrintArgs("hello", time.Hour)
	if !reflect.DeepEqual(got, []string{"-p", "hello", "--model", "m/x", "--agent", "plan-executor", "--output-format", "text"}) {
		t.Errorf("claude PrintArgs = %v", got)
	}
	for _, a := range got {
		if a == BudgetPlaceholder || a == PromptPlaceholder {
			t.Errorf("placeholder survived substitution: %v", got)
		}
	}

	// Unknown kind: empty in, empty out, no panic.
	if got := (Launch{Kind: "unknown", PromptAt: -1}).PrintArgs("x", time.Minute); len(got) != 0 {
		t.Errorf("unknown kind PrintArgs = %v, want empty", got)
	}
}
```

Add `"strings"` and `"time"` to the test file's imports if absent
(`reflect` is already there).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/harness -run 'TestLaunchPrint|TestPrintArgs'`
Expected: compile error -- `undefined: PromptPlaceholder`, `got.Print undefined`.

- [ ] **Step 3: Implement**

In `internal/harness/harness.go`, add `"time"` to the imports, and replace
the `Launch` type and the `Launch` method with:

```go
// Placeholders that stand in Launch.Print for the values only the caller
// knows at send time. PrintArgs replaces them; they are exported so a test
// or a caller can recognise them, never so a caller can build argv by hand.
const (
	PromptPlaceholder = "<prompt>"
	BudgetPlaceholder = "<budget>"
)

// Launch describes how to start an agent process for a specific role and
// model, in both of its forms.
//
// Args is the interactive form herdr starts in a pane. Print is the
// non-interactive form a headless builder runs (#99): one prompt in, the
// process exits when it is done. Print holds PromptPlaceholder and, for kinds
// with a timeout flag, BudgetPlaceholder as their own elements; PrintArgs
// fills them. PromptAt is the index of PromptPlaceholder in Print, -1 when
// the kind is unknown and Print is empty.
type Launch struct {
	Kind     string
	Args     []string
	Print    []string
	PromptAt int
}

// Launch renders the command-line arguments needed to run the given role on
// this harness. Relay renders the argv because model and role are fields
// (candidates spec §1 point 2): with a verbatim args list in config, the
// model would be a label relay could not check against what it launched.
// Every kind selects its role with --agent; there is no other mechanism (#85).
//
// The print form per kind (headless spec §3.5), before extra:
//
//	agy       -p <prompt> --model M --agent <def> --output-format text --print-timeout <budget>
//	claude    -p <prompt> --model M --agent <def> --output-format text
//	opencode  run <prompt> -m P/M --agent <def>
//
// agy gets the budget because its default print timeout (5m) would kill any
// real round; claude and opencode have no such flag.
func (h Harness) Launch(provider, model string, extra []string, role RoleSpec) Launch {
	var base, print []string
	promptAt := -1

	switch h.Kind {
	case "claude":
		base = []string{"--model", model, "--agent", role.Definition}
		print = []string{"-p", PromptPlaceholder, "--model", model, "--agent", role.Definition, "--output-format", "text"}
		promptAt = 1
	case "opencode":
		base = []string{"--agent", role.Definition, "-m", provider + "/" + model}
		print = []string{"run", PromptPlaceholder, "-m", provider + "/" + model, "--agent", role.Definition}
		promptAt = 1
	case "agy":
		base = []string{"--model", model, "--agent", role.Definition}
		print = []string{"-p", PromptPlaceholder, "--model", model, "--agent", role.Definition,
			"--output-format", "text", "--print-timeout", BudgetPlaceholder}
		promptAt = 1
	}

	args := append(append([]string(nil), base...), extra...)
	if print != nil {
		print = append(print, extra...)
	}
	return Launch{
		Kind:     h.Kind,
		Args:     args,
		Print:    print,
		PromptAt: promptAt,
	}
}

// PrintArgs is Print with the prompt and the round budget filled in: a fresh
// slice, so neither Print nor the caller's extra is touched. The budget is
// rendered as a Go duration ("1h30m0s"), which is what agy's --print-timeout
// parses. A kind whose Print has no BudgetPlaceholder ignores budget.
func (l Launch) PrintArgs(prompt string, budget time.Duration) []string {
	out := make([]string, 0, len(l.Print))
	for _, a := range l.Print {
		switch a {
		case PromptPlaceholder:
			out = append(out, prompt)
		case BudgetPlaceholder:
			out = append(out, budget.String())
		default:
			out = append(out, a)
		}
	}
	return out
}
```

Note the `append(print, extra...)`: `print` was built with a literal of
exactly the right length, so Go allocates a new backing array on that
append when `extra` is non-empty, and `extra` itself is only read.
`TestPrintArgsSubstitutesPromptAndBudgetWithoutMutating` pins that `extra`
is untouched; if it fails, copy `extra` explicitly as `args` does.

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./internal/harness`
Expected: PASS -- the two new tests and every existing one, `TestLaunch`
unmodified. If `TestLaunch` fails, `Args` changed: stop and report.

- [ ] **Step 5: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/harness
git commit -m "feat(harness): Launch.Print -- non-interactive argv per kind, PrintArgs fills prompt and budget (#99 step 2)"
```

---

### Task 2: spec amendment

**Files:**
- Modify: `docs/specs/2026-09-12-headless-builders-design.md` (§3.5, §4.2)

- [ ] **Step 1: §3.5**

Replace the `Launch` block in §3.5:

```
Launch
  Kind  string    (existing)
  Args  []string  (existing; the interactive argv, after the binary)
  Print []string  the non-interactive argv, after the binary, with <prompt> as its own element
                  at the index PromptAt
  PromptAt int
```

with

```
Launch
  Kind     string    (existing)
  Args     []string  (existing; the interactive argv, after the binary)
  Print    []string  the non-interactive argv, after the binary, with harness.PromptPlaceholder
                     ("<prompt>") and, where the kind has a timeout flag, harness.BudgetPlaceholder
                     ("<budget>") as their own elements
  PromptAt int       index of PromptPlaceholder in Print; -1 for an unknown kind

func (l Launch) PrintArgs(prompt string, budget time.Duration) []string
                     Print with both placeholders filled (budget as a Go duration string), a fresh
                     slice. Amended at step 2: the substitution lives here, so no other package
                     learns where a kind puts its prompt or its budget.
```

- [ ] **Step 2: §4.2**

Replace the sentence

```
Renders `[binary] + Launch.Print` with `<prompt>` substituted at `PromptAt`
and the budget applied. Pure.
```

with

```
Renders `[binary] + Launch.PrintArgs(prompt, budget)`. Pure.
```

- [ ] **Step 3: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add docs/specs/2026-09-12-headless-builders-design.md
git commit -m "docs(spec): headless -- Launch.PrintArgs owns the placeholder substitution (#99 step 2)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), and `git diff --stat main..HEAD`. Confirm `TestLaunch`
was not edited. If any step was impossible as written, say which and why --
do not work around it.
