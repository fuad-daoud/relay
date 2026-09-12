# Pick modes: confirm before `done`/`unbind` acts on a live binding (#103)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #103. **Spec:** `docs/specs/2026-09-12-pick-modes-design.md` --
read §2 ("A confirm step" under out of scope), §4 and §5 before starting.
This plan reverses the §2 ruling; Task 2 amends the spec to say so.
**Depends on:** nothing open.

**Goal:** when `relay done --pick` or `relay unbind --pick` is about to act on
a binding whose display is anything but `DONE`, a confirm screen stands
between `Enter` and the verb, and only `y` gets through.

**Why:** a herdr popup is session-modal and takes focus the instant it
opens. A keystroke already in flight lands on it, and the most common such
key is Enter. That ran `relay done` on a live round-5 binding. The list did
show the row's state; nobody had read it yet.

**Architecture:** `internal/pick` gains a fourth `screen`, `screenConfirm`,
and a pure rule `needsConfirm(verb, row)` in `verb.go`. `Model.pick` routes
`done`/`unbind` through the rule: rows that need it move to the confirm
screen holding the row; `y` there does exactly what `pick` did before (move
to a pending result screen and run the verb); any other key returns to the
list with the cursor where it was. `DONE` rows under `unbind` run at once as
today. `answer` is untouched -- its answer screen already needs typed input.
Nothing outside `internal/pick` changes except docs.

**Tech stack:** Go 1.22, bubbletea v1.3.4, lipgloss v1.0.0. Verification is
`make check`.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/pick-confirm` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/pick-confirm`, cut from `main`. |
| `~/.local/state/relay/pick-confirm` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

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

Do **not** run `herdr` yourself. The popup behaviour is verified by the
planner, after merge.

## Global constraints

- The no-`--pick` paths of `done`, `unbind` and `answer` are not touched.
- `answer --pick` behaviour is byte-for-byte unchanged: no confirm screen on
  that verb, and every existing test in `internal/pick` keeps passing
  without modification **except** the four named in Task 1 Step 3, which
  gain one `y` keystroke because the fixture rows are `ACTIVE`.
- `internal/pick` must not import `internal/ui` (spec §8).
- The result screen, its texts and the exit codes (spec §3, §5) are
  unchanged.
- `go.mod`/`go.sum` do not change.
- Nothing new is written to `log.jsonl`.
- One commit per task, on the worktree's branch.

---

### Task 1: the confirm rule and the confirm screen

**Files:**
- Modify: `internal/pick/verb.go` (add `needsConfirm`)
- Modify: `internal/pick/model.go` (`screenConfirm`, `confirm` field, `pick`, `confirmKeys`, `Update`, `View`)
- Create: `internal/pick/confirm.go` (`confirmView`)
- Modify: `internal/pick/verb_test.go` (rule tests)
- Modify: `internal/pick/model_test.go` (screen tests; four existing tests gain a `y`)

**Interfaces:**
- Consumes: `relay.BindingStatus{Name, Display, Round}`, `Verb`, `Options`, `runVerb`, `resultModel`, the `hintStyle`/`titleStyle`/`needsYouStyle`/`activeStyle` vars in `list.go`, `styleDisplay(display string) string` in `list.go`.
- Produces: `func needsConfirm(verb Verb, r relay.BindingStatus) bool`; `const screenConfirm screen`; `Model.confirm relay.BindingStatus`; `func (m Model) confirmKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd)`; `func (m Model) confirmView() string`. Task 2 needs none of them; they are listed so the names are fixed.

- [ ] **Step 1: Write the failing rule tests**

Append to `internal/pick/verb_test.go`:

```go
func TestNeedsConfirmOnlyForLiveRowsUnderDestructiveVerbs(t *testing.T) {
	live := []string{"ACTIVE", "NEEDS YOU", "HELD"}
	for _, d := range live {
		if !needsConfirm(VerbDone, row("a", d, "working")) {
			t.Errorf("done on %s row: needsConfirm = false, want true", d)
		}
		if !needsConfirm(VerbUnbind, row("a", d, "working")) {
			t.Errorf("unbind on %s row: needsConfirm = false, want true", d)
		}
		if needsConfirm(VerbAnswer, row("a", d, "blocked")) {
			t.Errorf("answer on %s row: needsConfirm = true, want false", d)
		}
	}
	if needsConfirm(VerbUnbind, row("a", "DONE", "idle")) {
		t.Error("unbind on a DONE row must run at once")
	}
	if needsConfirm(VerbDone, row("a", "DONE", "idle")) {
		t.Error("done never lists DONE rows; the rule still says no confirm for one")
	}
}
```

(`row` is defined in `fake_test.go`, same package.)

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/pick -run TestNeedsConfirm`
Expected: compile error, `undefined: needsConfirm`.

- [ ] **Step 3: Give the four `ACTIVE`-fixture tests their `y`**

In `internal/pick/model_test.go`, four tests press `enter` on an `ACTIVE` row
under `done` or `unbind` and expect the verb to run. After this task they
land on the confirm screen first. Change each as shown -- one inserted line
per test, nothing else.

`TestEnterRunsDoneAndShowsItsText`: replace

```go
	m, cmd := update(t, m, key("enter"))
	if m.screen != screenResult || !m.result.pending {
```

with

```go
	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("y"))
	if m.screen != screenResult || !m.result.pending {
```

`TestEnterRunsUnbindWithArchive`: replace

```go
	m, cmd := update(t, m, key("enter"))
	m, _ = update(t, m, cmd())
	if m.result.err != nil {
		t.Fatalf("unbind: %v", m.result.err)
	}
```

with

```go
	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("y"))
	m, _ = update(t, m, cmd())
	if m.result.err != nil {
		t.Fatalf("unbind: %v", m.result.err)
	}
```

`TestVerbErrorShowsAndExitsOne`: replace

```go
	m, cmd := update(t, m, key("enter"))
	m, _ = update(t, m, cmd())
	if m.result.err == nil {
```

with

```go
	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("y"))
	m, _ = update(t, m, cmd())
	if m.result.err == nil {
```

`TestKeysAreIgnoredWhileTheVerbRuns`: replace

```go
	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("enter"))
	if cmd != nil {
```

with

```go
	m, _ = update(t, m, key("enter"))
	m, _ = update(t, m, key("y"))
	m, cmd := update(t, m, key("enter"))
	if cmd != nil {
```

No other existing test changes. If another test in the package fails after
Step 6, stop and report which -- do not edit it.

- [ ] **Step 4: Write the failing screen tests**

Append to `internal/pick/model_test.go`:

```go
func TestEnterOnLiveRowAsksBeforeDone(t *testing.T) {
	fh := newFakeHerdr(t)
	rt := testRuntime(t, fh, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))

	m, cmd := update(t, m, key("enter"))
	if m.screen != screenConfirm {
		t.Fatalf("enter on an ACTIVE row: screen=%v, want screenConfirm", m.screen)
	}
	if cmd != nil {
		t.Fatal("moving to the confirm screen must not run the verb")
	}
	if m.confirm.Name != "webshop" {
		t.Fatalf("confirm holds %q, want webshop", m.confirm.Name)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil || b.State != store.StateActive {
		t.Fatalf("binding touched before confirm: state=%v err=%v", b.State, err)
	}
}

func TestConfirmViewNamesTheBindingItsStateAndRound(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))
	m, _ = update(t, m, key("enter"))
	v := m.confirmView()
	for _, want := range []string{"mark webshop done?", "ACTIVE", "round 2", "y", "any other key cancels"} {
		if !contains(v, want) {
			t.Errorf("confirm view lacks %q:\n%s", want, v)
		}
	}
	m = newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbUnbind})
	m, _ = update(t, m, rowsMsg(row("webshop", "NEEDS YOU", "blocked")))
	m, _ = update(t, m, key("enter"))
	v = m.confirmView()
	for _, want := range []string{"unbind webshop?", "NEEDS YOU", "round 2"} {
		if !contains(v, want) {
			t.Errorf("unbind confirm view lacks %q:\n%s", want, v)
		}
	}
}

func TestAnyKeyButYReturnsToTheList(t *testing.T) {
	fh := newFakeHerdr(t)
	rt := testRuntime(t, fh, testBinding("a"), testBinding("b"))
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working"), row("b", "ACTIVE", "working")))
	m, _ = update(t, m, key("down"))
	m, _ = update(t, m, key("enter"))
	if m.screen != screenConfirm {
		t.Fatalf("screen=%v, want screenConfirm", m.screen)
	}
	for _, k := range []string{"enter", "n", "esc", "Y"} {
		mm, cmd := update(t, m, key(k))
		if mm.screen != screenList {
			t.Errorf("%q on confirm: screen=%v, want screenList", k, mm.screen)
		}
		if cmd != nil {
			t.Errorf("%q on confirm returned a command; want none", k)
		}
		if mm.cursor != 1 {
			t.Errorf("%q on confirm moved the cursor to %d, want 1", k, mm.cursor)
		}
		if mm.outcome != nil {
			t.Errorf("%q on confirm set outcome %v; the picker must stay open", k, mm.outcome)
		}
	}
	for _, name := range []string{"a", "b"} {
		b, err := rt.Store.Load(name)
		if err != nil || b.State != store.StateActive {
			t.Fatalf("%s touched by a cancelled confirm: state=%v err=%v", name, b.State, err)
		}
	}
}

func TestUnbindOnDoneRowRunsWithoutConfirm(t *testing.T) {
	fh := newFakeHerdr(t)
	done := testBinding("old")
	done.State = store.StateDone
	rt := testRuntime(t, fh, done)
	m := newModel(context.Background(), rt, Options{Verb: VerbUnbind})
	m, _ = update(t, m, rowsMsg(row("old", "DONE", "idle")))
	m, cmd := update(t, m, key("enter"))
	if m.screen != screenResult || !m.result.pending {
		t.Fatalf("enter on a DONE row should move straight to a pending result: screen=%v pending=%v", m.screen, m.result.pending)
	}
	if cmd == nil {
		t.Fatal("enter on a DONE row returned no command")
	}
	m, _ = update(t, m, cmd())
	if m.result.err != nil {
		t.Fatalf("unbind: %v", m.result.err)
	}
	if _, err := rt.Store.Load("old"); err == nil {
		t.Fatal("binding still loads after unbind")
	}
}

func TestCtrlCCancelsFromConfirm(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working")))
	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("ctrl+c"))
	wantQuit(t, m, cmd, ErrCancelled)
}
```

`contains` already exists in the package's tests (used by
`TestEnterRunsUnbindWithArchive`). Note `key("Y")` produces a rune key `Y`,
which is **not** `y`: the confirm accepts lowercase `y` only.

- [ ] **Step 5: Run them to see them fail**

Run: `go test ./internal/pick`
Expected: compile error -- `undefined: screenConfirm`, `m.confirm undefined`,
`m.confirmView undefined`.

- [ ] **Step 6: The rule**

Append to `internal/pick/verb.go`:

```go
// needsConfirm is the #103 rule: done and unbind stop for a `y` before
// acting on any row that is not DONE. A herdr popup takes focus the instant
// it opens, so a keystroke already in flight lands on it -- and the most
// common such key is Enter. The list showed the row's state; nobody had
// read it yet. DONE rows under unbind are what gc clears anyway and run at
// once. answer needs typed input on its own screen and is never confirmed.
func needsConfirm(verb Verb, r relay.BindingStatus) bool {
	switch verb {
	case VerbDone, VerbUnbind:
		return r.Display != "DONE"
	default:
		return false
	}
}
```

- [ ] **Step 7: The screen**

In `internal/pick/model.go`:

Add the constant after `screenResult`:

```go
const (
	screenList screen = iota
	screenAnswer
	screenResult
	screenConfirm
)
```

Update the `Model` doc comment's first line to `// Model is the whole
picker: one of four screens at a time.` and add a field after `answer
answerModel`:

```go
	// confirm is the row a done/unbind is waiting on a `y` for (#103).
	// Meaningful only while screen == screenConfirm.
	confirm relay.BindingStatus
```

Replace `pick` with:

```go
// pick acts on the chosen row. done and unbind run at once on a DONE row and
// stop for a `y` on any other (#103, needsConfirm); answer needs its own
// screen first (spec §6).
func (m Model) pick(r relay.BindingStatus) (tea.Model, tea.Cmd) {
	switch m.opts.Verb {
	case VerbDone, VerbUnbind:
		if needsConfirm(m.opts.Verb, r) {
			m.screen = screenConfirm
			m.confirm = r
			return m, nil
		}
		return m.run(r.Name)
	case VerbAnswer:
		return m.enterAnswer(r.Name)
	}
	return m, nil
}

// run moves to a pending result screen and starts the verb on name. It is
// what Enter did before #103; the confirm screen's `y` reaches it now.
func (m Model) run(name string) (tea.Model, tea.Cmd) {
	m.screen = screenResult
	m.result = resultModel{pending: true}
	return m, runVerb(m.ctx, m.rt, m.opts, name, relay.AnswerInput{})
}

// confirmKeys: only a lowercase y proceeds. Every other key -- Enter
// included, since a stray Enter is the whole reason this screen exists --
// returns to the list with the cursor where it was. ctrl+c is handled
// before this in Update and still cancels the picker.
func (m Model) confirmKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "y" {
		return m.run(m.confirm.Name)
	}
	m.screen = screenList
	return m, nil
}
```

In `Update`, add a case to the `switch m.screen` under `tea.KeyMsg`:

```go
		case screenConfirm:
			return m.confirmKeys(msg)
```

In `View`, add before `default`:

```go
	case screenConfirm:
		return m.confirmView()
```

- [ ] **Step 8: The view**

Create `internal/pick/confirm.go`:

```go
package pick

import (
	"fmt"
	"strings"
)

// confirmView is the #103 screen between Enter and a destructive verb:
//
//	mark webshop done?  it is ACTIVE in round 5
//
//	y confirm  any other key cancels
//
// The state is styled the way the list styles it, so the word the human
// missed on the list is the same word, in the same colour, alone on a line.
func (m Model) confirmView() string {
	var sb strings.Builder
	var question string
	switch m.opts.Verb {
	case VerbUnbind:
		question = fmt.Sprintf("unbind %s?", m.confirm.Name)
	default:
		question = fmt.Sprintf("mark %s done?", m.confirm.Name)
	}
	sb.WriteString(titleStyle.Render(question))
	sb.WriteString(fmt.Sprintf("  it is %s in round %d", strings.TrimRight(styleDisplay(m.confirm.Display), " "), m.confirm.Round))
	sb.WriteString("\n\n")
	sb.WriteString(hintStyle.Render("y confirm  any other key cancels"))
	return sb.String()
}
```

Note: `styleDisplay` pads to 9 columns *inside* the styled string, so
`TrimRight` on the rendered result may leave the padding when lipgloss emits
escape codes after the spaces. That is cosmetic and acceptable; the test
checks for the state word, not the spacing. If `go vet` or the test
complains about anything here, stop and report rather than restructure
`styleDisplay`.

- [ ] **Step 9: Run the package tests**

Run: `go test -count=1 ./internal/pick`
Expected: PASS -- every test, the five new ones and the four amended ones
included. `TestAnswer*` tests must pass unmodified: if any fails, the
confirm screen has leaked into the answer path -- stop and report.

- [ ] **Step 10: Mutation check, then verify and commit**

Mutation: in `needsConfirm`, temporarily change `r.Display != "DONE"` to
`false`. Run `go test ./internal/pick -run 'TestNeedsConfirm|TestEnterOnLiveRow|TestAnyKeyButY'`.
Expected: all three FAIL. Revert the mutation. Run the same command again:
PASS. Mention in the report that this was done.

Run: `make check`
Expected: green.

```bash
git add internal/pick
git commit -m "feat(pick): confirm before done/unbind acts on a live binding (#103)"
```

---

### Task 2: spec amendment and README

**Files:**
- Modify: `docs/specs/2026-09-12-pick-modes-design.md` (§2 out-of-scope bullet; §5)
- Modify: `README.md` (the "done and unbind are the destructive verbs" paragraph)

**Interfaces:** none.

- [ ] **Step 1: Amend the spec's §2**

In `docs/specs/2026-09-12-pick-modes-design.md`, replace the bullet

```
- **A confirm step.** The list shows each row's state, which is what a confirm
  dialog would repeat. `unbind` is already reversible through
  `relay bind --resume`.
```

with

```
- ~~**A confirm step.**~~ **Reversed by #103.** The original reasoning -- the
  list shows each row's state, which is what a confirm dialog would repeat --
  holds only while a human is looking at the list. A popup opened by a key
  or an action takes focus the instant it opens, and a keystroke already in
  flight lands on it as Enter. `done --pick` and `unbind --pick` now stop for
  a `y` on any row that is not `DONE`; see §5.
```

- [ ] **Step 2: Amend the spec's §5**

In §5, after the paragraph beginning "`Enter` runs the verb at once, in the
picker's process:" and its table, and before "The result screen then prints
exactly the lines the CLI prints", insert:

```
**Confirm (#103).** For `done` and `unbind`, `Enter` on a row whose display is
anything but `DONE` does not run the verb; it opens a confirm screen:

```
mark webshop done?  it is ACTIVE in round 5

y confirm  any other key cancels
```

(`unbind webshop?` for `unbind`.) Only a lowercase `y` runs the verb. Any
other key -- Enter included -- returns to the list with the cursor where it
was; `Ctrl+C` still cancels the picker. `unbind` on a `DONE` row runs at once,
as before. `answer` has no confirm: its answer screen already needs typed
input before anything is sent. The rule is `needsConfirm(verb, row)` in
`internal/pick/verb.go`.
```

Note the inner fenced block: the spec already uses nested fences elsewhere
(§4's row-format block); match that style. If the surrounding fences make the
markdown ambiguous, indent the example by four spaces instead of fencing it.

- [ ] **Step 3: README**

In `README.md`, in the paragraph under "### done and unbind are the
destructive verbs", replace

```
`--pick` is explicit for the same reason -- a bare verb never opens a picker,
so the planner agent, whose pane is also a terminal, can never fall into one.
```

with

```
`--pick` is explicit for the same reason -- a bare verb never opens a picker,
so the planner agent, whose pane is also a terminal, can never fall into one.
And because a popup takes focus the instant it opens, `Enter` on a binding
that is not `DONE` asks first -- `mark webshop done? it is ACTIVE in round 5`
-- and only `y` proceeds; any other key returns to the list.
```

- [ ] **Step 4: Verify and commit**

Run: `make check`
Expected: green (docs only; this confirms nothing else moved).

```bash
git add docs/specs/2026-09-12-pick-modes-design.md README.md
git commit -m "docs(pick): confirm step -- spec §2 reversed, §5 and README describe it (#103)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), that the Step 10 mutation check was done and which
tests it failed, and `git diff --stat main..HEAD`. If any step was impossible
as written, say which and why -- do not work around it.
