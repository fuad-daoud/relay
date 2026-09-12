# Pick modes: `done`, `unbind` and `answer` at a keystroke -- Go side (#15)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #15. **Spec:** `docs/specs/2026-09-12-pick-modes-design.md` --
read §3 to §6 and §8 to §10 before starting; section numbers below refer to
it. The plugin manifests, the pane-open script and the README are a separate
plan (`docs/plans/2026-09-12-pick-modes-plugin.md`) and are **not** touched
here.
**Depends on:** nothing open.

**Goal:** `relay done --pick`, `relay unbind --pick` and `relay answer --pick`
open a small terminal picker, run the verb on the chosen binding, show the
result, and exit with the status the spec gives.

**Architecture:** a new `internal/pick` package holds one bubbletea `Model`
with three screens (`list`, `answer`, `result`). Every herdr or store call
runs inside a `tea.Cmd` that returns a message, so `Update` is pure and tests
drive it with messages against a fake `Runtime`. `internal/relay` gains the
pure pieces both the CLI and the picker need: `ParseAnswer`, the exported
dialog-read constants, and the three result-text functions. `cmd/relay` only
adds the flag and maps the picker's outcomes to exit codes. `pick` does not
import `internal/ui`; the two helpers it shares with `ui` (`listWindow`,
`renderError`/`wrapLine`) are copied, per spec §4 and §8.

**Tech stack:** Go 1.22, bubbletea v1.3.4, bubbles v0.20.0 (`textinput`,
`viewport` -- already required by `go.mod`, so `go mod tidy` must not change
`go.sum`), lipgloss v1.0.0. Verification is `make check`.

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

Do **not** run `herdr` yourself. The popup behaviours in spec §11 are verified
by the planner, after merge.

## Global constraints

- The no-`--pick` paths of `done`, `unbind` and `answer` stay byte-for-byte
  unchanged in behaviour and output (spec §3). Task 1 moves their result text
  into functions; the printed bytes must not change.
- `internal/pick` must not import `internal/ui` (spec §8).
- relay parses nothing out of the dialog text (spec §2, §6). No option
  detection, no regexes over the body.
- No test in `cmd/relay` may reach `newRuntime`; every `cmd/relay` test here
  fails in flag validation first. CI runners have no `herdr` binary.
- Nothing new is written to `log.jsonl` (spec §9).
- One commit per task, on the worktree's branch.

---

### Task 1: `internal/relay` -- `ParseAnswer`, dialog constants, result texts

**Files:**
- Modify: `internal/relay/answer.go` (add `ParseAnswer`, `DialogSource`, `DialogLines`)
- Modify: `internal/relay/reconcile.go:36-40,267` (use the exported constant)
- Modify: `internal/relay/reconcile_blocked_test.go:38-39` (same)
- Create: `internal/relay/text.go`, `internal/relay/text_test.go`
- Modify: `internal/relay/answer_test.go` (append `TestParseAnswer`)
- Modify: `cmd/relay/main.go` -- `cmdUnbind` (~line 733-747), `cmdDone` (~1323), `cmdAnswer` (~1139)

**Interfaces:**
- Consumes: `AnswerInput`, `UnbindResult` (exist).
- Produces:
  - `func ParseAnswer(s string) AnswerInput`
  - `const DialogSource = "detection"`, `const DialogLines = 200`
  - `func DoneText(name string) string`
  - `func UnbindText(name string, res UnbindResult) string`
  - `func AnswerText(name string) string`

- [ ] **Step 1: Write the failing `ParseAnswer` test**

Append to `internal/relay/answer_test.go`:

```go
// TestParseAnswer pins spec §6: a positive integer is a dialog option, one of
// herdr's logical key names is a key, anything else is text. Whitespace and
// case never change the outcome.
func TestParseAnswer(t *testing.T) {
	cases := []struct {
		in   string
		want AnswerInput
	}{
		{"12", AnswerInput{Choice: 12}},
		{" 3 ", AnswerInput{Choice: 3}},
		{"0", AnswerInput{Text: "0"}},
		{"-1", AnswerInput{Text: "-1"}},
		{"enter", AnswerInput{Keys: "enter"}},
		{"ENTER", AnswerInput{Keys: "enter"}},
		{" esc ", AnswerInput{Keys: "esc"}},
		{"tab", AnswerInput{Keys: "tab"}},
		{"up", AnswerInput{Keys: "up"}},
		{"down", AnswerInput{Keys: "down"}},
		{"space", AnswerInput{Keys: "space"}},
		{"yes please", AnswerInput{Text: "yes please"}},
		{"y", AnswerInput{Text: "y"}},
		{"", AnswerInput{}},
	}
	for _, c := range cases {
		if got := ParseAnswer(c.in); got != c.want {
			t.Errorf("ParseAnswer(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/relay -run TestParseAnswer`
Expected: FAIL -- `undefined: ParseAnswer`.

- [ ] **Step 3: Add `ParseAnswer` and the constants to `answer.go`**

Add after the `AnswerInput` type in `internal/relay/answer.go` (add
`"strings"` to the imports):

```go
// DialogSource and DialogLines are how relay reads a blocking dialog. A TUI
// approval prompt is drawn on the alternate screen, which never reaches the
// scrollback recent-unwrapped reads, so the dialog has to come from detection
// instead. The daemon's blocked handler and the answer picker use the same
// pair so the human sees what the daemon saw.
const (
	DialogSource = "detection"
	DialogLines  = 200
)

// logicalKeys are the key names herdr send-keys accepts by name. Anything
// else typed at the answer picker is literal text.
var logicalKeys = map[string]bool{
	"enter": true, "esc": true, "tab": true, "up": true, "down": true, "space": true,
}

// ParseAnswer turns one typed line into an AnswerInput (spec §6). A positive
// integer is a numbered dialog option; a logical key name is a key; anything
// else is text as typed, trimmed. Zero and negatives are not options and fall
// through to text -- AnswerInput.resolve treats Choice 0 as unset.
func ParseAnswer(s string) AnswerInput {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return AnswerInput{Choice: n}
	}
	if k := strings.ToLower(s); logicalKeys[k] {
		return AnswerInput{Keys: k}
	}
	return AnswerInput{Text: s}
}
```

Then in `internal/relay/reconcile.go` delete the `dialogSource` constant and
its three-line comment (lines 36-40) and change line 267 to:

```go
	dialog, err := rt.Herdr.ReadAgentSource(ctx, Target(b.Builder), DialogSource, DialogLines)
```

In `internal/relay/reconcile_blocked_test.go:38-39` replace both
`dialogSource` with `DialogSource`. `scrapeLines` stays where it is: the
report scrape still uses it.

- [ ] **Step 4: Run the relay tests**

Run: `go test ./internal/relay`
Expected: PASS (including `TestParseAnswer` and the blocked-reconcile tests).

- [ ] **Step 5: Write the failing result-text tests**

Create `internal/relay/text_test.go`:

```go
package relay

import "testing"

// These pin the exact bytes the CLI prints today (cmd/relay/main.go before
// this change), because the picker's result screen and the terminal must
// never say different things (spec §5).

func TestDoneText(t *testing.T) {
	got := DoneText("webshop")
	want := "webshop marked done; relaying stopped (relay gc archives it when you are finished with it)"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestAnswerText(t *testing.T) {
	if got, want := AnswerText("webshop"), "answered webshop's builder"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestUnbindText(t *testing.T) {
	cases := []struct {
		name string
		res  UnbindResult
		want string
	}{
		{"deleted, no worktree", UnbindResult{},
			"unbound webshop (panes left untouched)"},
		{"archived", UnbindResult{ArchivedTo: "/a/webshop.tar.gz"},
			"archived webshop to /a/webshop.tar.gz (panes left untouched)"},
		{"worktree removed", UnbindResult{WorktreeRemoved: "/w/webshop"},
			"unbound webshop (panes left untouched)\nremoved worktree /w/webshop"},
		{"worktree kept", UnbindResult{WorktreeKept: "/w/webshop", KeptReason: "uncommitted changes"},
			"unbound webshop (panes left untouched)\nkept worktree /w/webshop (uncommitted changes)\n  remove by hand: git -C /w/webshop worktree remove /w/webshop"},
		{"worktree gone", UnbindResult{WorktreeGone: "/w/webshop"},
			"unbound webshop (panes left untouched)\nworktree /w/webshop was already gone"},
	}
	for _, c := range cases {
		if got := UnbindText("webshop", c.res); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 6: Run to verify they fail**

Run: `go test ./internal/relay -run 'Text$'`
Expected: FAIL -- `undefined: DoneText` (and the others).

- [ ] **Step 7: Create `internal/relay/text.go`**

```go
package relay

import (
	"fmt"
	"strings"
)

// The result lines of the three pick-able verbs live here, not in cmd/relay,
// because the picker's result screen (internal/pick) prints the same text the
// terminal does. Each returns the exact bytes cmd/relay printed before #15,
// without a trailing newline; the caller adds one.

// DoneText is what `relay done` says on success.
func DoneText(name string) string {
	return fmt.Sprintf("%s marked done; relaying stopped (relay gc archives it when you are finished with it)", name)
}

// AnswerText is what `relay answer` says on success.
func AnswerText(name string) string {
	return fmt.Sprintf("answered %s's builder", name)
}

// UnbindText is what `relay unbind` says on success: one line for the
// binding, then at most one for its worktree.
func UnbindText(name string, res UnbindResult) string {
	var lines []string
	if res.ArchivedTo != "" {
		lines = append(lines, fmt.Sprintf("archived %s to %s (panes left untouched)", name, res.ArchivedTo))
	} else {
		lines = append(lines, fmt.Sprintf("unbound %s (panes left untouched)", name))
	}
	switch {
	case res.WorktreeRemoved != "":
		lines = append(lines, fmt.Sprintf("removed worktree %s", res.WorktreeRemoved))
	case res.WorktreeKept != "":
		lines = append(lines, fmt.Sprintf("kept worktree %s (%s)\n  remove by hand: git -C %s worktree remove %s",
			res.WorktreeKept, res.KeptReason, res.WorktreeKept, res.WorktreeKept))
	case res.WorktreeGone != "":
		lines = append(lines, fmt.Sprintf("worktree %s was already gone", res.WorktreeGone))
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 8: Run the text tests**

Run: `go test ./internal/relay -run 'Text$'`
Expected: PASS.

- [ ] **Step 9: Make `cmd/relay` print through the text functions**

In `cmd/relay/main.go`:

`cmdUnbind` -- replace everything from `if res.ArchivedTo != "" {` down to
the `return nil` before `func cmdGC` with:

```go
	fmt.Println(relay.UnbindText(target, res))

	return nil
```

`cmdDone` -- replace the `fmt.Printf("%s marked done; ...")` line with:

```go
	fmt.Println(relay.DoneText(target))
```

`cmdAnswer` -- replace `fmt.Printf("answered %s's builder\n", target)` with:

```go
	fmt.Println(relay.AnswerText(target))
```

- [ ] **Step 10: Verify and commit**

Run: `make check`
Expected: green. `git diff --stat` touches only the files listed for this
task.

```bash
git add internal/relay/answer.go internal/relay/answer_test.go internal/relay/reconcile.go \
        internal/relay/reconcile_blocked_test.go internal/relay/text.go internal/relay/text_test.go \
        cmd/relay/main.go
git commit -m "refactor(relay): ParseAnswer, exported dialog constants, verb result texts (#15 task 1)"
```

---

### Task 2: `internal/pick` -- the list and result screens, `done`/`unbind` run

**Files:**
- Create: `internal/pick/verb.go`, `internal/pick/list.go`,
  `internal/pick/result.go`, `internal/pick/model.go`
- Create: `internal/pick/fake_test.go`, `internal/pick/verb_test.go`,
  `internal/pick/list_test.go`, `internal/pick/model_test.go`

**Interfaces:**
- Consumes (Task 1): `relay.DoneText`, `relay.UnbindText`, `relay.AnswerText`;
  (existing) `relay.Status`, `relay.HideDone`, `relay.Done`, `relay.Unbind`,
  `relay.Answer`, `relay.AnswerInput`, `relay.Report`, `relay.BindingStatus`,
  `herdr.StatusBlocked`.
- Produces (used by Tasks 3 and 4):
  - `type Verb string`; `VerbDone`, `VerbUnbind`, `VerbAnswer`
  - `type Options struct { Verb Verb; Archive bool }`
  - `var ErrCancelled, ErrNothingToPick, ErrVerbFailed error`
  - `type Model struct` with fields `ctx, rt, opts, screen, width, height,
    rows, loaded, cursor, top, result, outcome`
  - `type screen int`; `screenList`, `screenAnswer`, `screenResult`
  - `type statusMsg`, `type verbDoneMsg`, `type resultModel`
  - `func newModel(ctx, rt, opts) Model`
  - `func runVerb(ctx, rt, opts, name string, in relay.AnswerInput) tea.Cmd`
  - `func (m Model) quit(outcome error) (tea.Model, tea.Cmd)`
  - `func (m Model) pick(row relay.BindingStatus) (tea.Model, tea.Cmd)`
  - `func listWindow(top, cursor, rows, n int) int`, `func renderError(err error, width int) string`
  - styles: `titleStyle`, `hintStyle`, `cursorStyle`, `errorStyle`

- [ ] **Step 1: Create the fake herdr for pick tests**

Create `internal/pick/fake_test.go`. Unlike `internal/ui`'s fake, this one
must *allow* `SendKeys` (the answer verb types) and `ReadAgentSource` (the
answer screen reads); it records both.

```go
package pick

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

type sentKeys struct{ Target, Keys string }

type fakeHerdr struct {
	t       *testing.T
	agents  []herdr.Agent
	listErr error

	readOut string
	readErr error
	reads   []string // targets ReadAgentSource was addressed to

	keys []sentKeys
}

func newFakeHerdr(t *testing.T) *fakeHerdr { return &fakeHerdr{t: t} }

func (f *fakeHerdr) ListAgents(_ context.Context) ([]herdr.Agent, error) {
	return f.agents, f.listErr
}

func (f *fakeHerdr) SendKeys(_ context.Context, target, keys string) error {
	f.keys = append(f.keys, sentKeys{Target: target, Keys: keys})
	return nil
}

func (f *fakeHerdr) ReadAgentSource(_ context.Context, target, _ string, _ int) (string, error) {
	f.reads = append(f.reads, target)
	return f.readOut, f.readErr
}

func (f *fakeHerdr) ReadAgent(_ context.Context, _ string, _ int) (string, error) {
	f.t.Errorf("pick never reads scrollback: ReadAgent called")
	return "", errors.New("ReadAgent called")
}

func (f *fakeHerdr) Prompt(_ context.Context, _, _ string) error {
	f.t.Errorf("pick never prompts: Prompt called")
	return errors.New("Prompt called")
}

func (f *fakeHerdr) CreateTab(_ context.Context, _, _, _ string) (string, error) {
	f.t.Errorf("pick never spawns: CreateTab called")
	return "", errors.New("CreateTab called")
}

func (f *fakeHerdr) StartAgent(_ context.Context, _, _, _ string, _ []string) error {
	f.t.Errorf("pick never spawns: StartAgent called")
	return errors.New("StartAgent called")
}

func (f *fakeHerdr) Notify(_ context.Context, _ string) error {
	f.t.Errorf("pick never notifies: Notify called")
	return errors.New("Notify called")
}

func (f *fakeHerdr) ClosePane(_ context.Context, _ string) error {
	f.t.Errorf("pick never closes panes: ClosePane called")
	return errors.New("ClosePane called")
}

// testBinding is a bound, active binding whose builder herdr knows by name.
func testBinding(name string) store.Binding {
	return store.Binding{
		Name:             name,
		CWD:              "/tmp/" + name,
		Planner:          store.Endpoint{PaneID: "w1:p1", SessionID: "planner-session", Kind: "claude"},
		Builder:          store.Endpoint{AgentName: name + "-builder", PaneID: "w1:p2", Kind: "opencode"},
		BuilderCandidate: "agy",
		Round:            2,
		State:            store.StateActive,
	}
}

// blockedAgent is the herdr view of testBinding(name)'s builder at a dialog.
func blockedAgent(name string) herdr.Agent {
	return herdr.Agent{Name: name + "-builder", Kind: "opencode", PaneID: "w1:p2", Status: herdr.StatusBlocked}
}

// testRuntime seeds a store with the given bindings and returns a runtime
// over it and the fake herdr.
func testRuntime(t *testing.T, fh *fakeHerdr, bindings ...store.Binding) relay.Runtime {
	t.Helper()
	st := store.New(t.TempDir())
	for _, b := range bindings {
		if err := st.Save(b); err != nil {
			t.Fatalf("Save %s: %v", b.Name, err)
		}
	}
	return relay.Runtime{Store: st, Herdr: fh}
}

// rowsMsg builds the statusMsg the list would receive for these rows.
func rowsMsg(rows ...relay.BindingStatus) statusMsg {
	return statusMsg{report: relay.Report{Bindings: rows}}
}

func row(name, display, builderStatus string) relay.BindingStatus {
	return relay.BindingStatus{Name: name, Display: display, Round: 2, BuilderCandidate: "agy", BuilderStatus: builderStatus}
}

// update runs one message through the model and returns the Model back.
func update(t *testing.T, m Model, msg interface{}) (Model, func() interface{}) {
	t.Helper()
	nm, cmd := m.Update(msg)
	out, ok := nm.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", nm)
	}
	if cmd == nil {
		return out, nil
	}
	return out, func() interface{} { return cmd() }
}
```

(`interface{}` rather than `tea.Msg` keeps the helper free of a bubbletea
import; `tea.Msg` is `interface{}`.)

- [ ] **Step 2: Write the failing `rowsFor` / `emptyText` tests**

Create `internal/pick/verb_test.go`:

```go
package pick

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
)

// TestRowsForFiltersPerVerb pins the spec §4 table.
func TestRowsForFiltersPerVerb(t *testing.T) {
	rep := relay.Report{Bindings: []relay.BindingStatus{
		{Name: "active", Display: "ACTIVE", BuilderStatus: herdr.StatusWorking},
		{Name: "blocked", Display: "NEEDS YOU", BuilderStatus: herdr.StatusBlocked},
		{Name: "finished", Display: "DONE", BuilderStatus: herdr.StatusIdle},
	}}
	names := func(rows []relay.BindingStatus) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Name)
		}
		return out
	}
	cases := []struct {
		verb Verb
		want []string
	}{
		{VerbDone, []string{"active", "blocked"}},
		{VerbUnbind, []string{"active", "blocked", "finished"}},
		{VerbAnswer, []string{"blocked"}},
	}
	for _, c := range cases {
		got := names(rowsFor(c.verb, rep))
		if len(got) != len(c.want) {
			t.Errorf("%s: rows = %v, want %v", c.verb, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: rows = %v, want %v", c.verb, got, c.want)
				break
			}
		}
	}
}

func TestEmptyTextPerVerb(t *testing.T) {
	cases := map[Verb]string{
		VerbDone:   "no bindings to mark done",
		VerbUnbind: "nothing bound",
		VerbAnswer: "no builder is blocked",
	}
	for verb, want := range cases {
		if got := emptyText(verb); got != want {
			t.Errorf("%s: %q, want %q", verb, got, want)
		}
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/pick`
Expected: FAIL to build -- `undefined: rowsFor` etc. (The package does not
exist yet; `go test` reports the build error.)

- [ ] **Step 4: Create `internal/pick/verb.go`**

```go
// Package pick is the interactive mode behind `relay done --pick`,
// `relay unbind --pick` and `relay answer --pick` (#15): a list of bindings,
// the verb run on the chosen one, and the result held on screen until a key.
// It is built for a herdr popup pane, which closes when the command exits.
package pick

import (
	"errors"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
)

// Verb is the relay command a picker runs on the chosen binding.
type Verb string

const (
	VerbDone   Verb = "done"
	VerbUnbind Verb = "unbind"
	VerbAnswer Verb = "answer"
)

// Options is what the command passes in. Archive is `unbind --archive`; the
// other verbs ignore it.
type Options struct {
	Verb    Verb
	Archive bool
}

// The non-zero outcomes of Run. The picker has already shown the text for
// each on screen, so the command maps all three to a silent exit 1: a
// second "relay: ..." line would land in the plugin log, not in front of the
// human (spec §3).
var (
	ErrCancelled     = errors.New("cancelled")
	ErrNothingToPick = errors.New("nothing to pick")
	// ErrVerbFailed covers the verb returning an error and the status fetch
	// before the list failing: both end on the result screen with the error
	// shown.
	ErrVerbFailed = errors.New("verb failed")
)

// rowsFor is the spec §4 table: what each verb can act on. done cannot act
// on a DONE binding; unbind clears DONE bindings, so it lists them; answer
// only makes sense against a builder herdr reports blocked, which is the
// guard relay.Answer enforces anyway.
func rowsFor(verb Verb, rep relay.Report) []relay.BindingStatus {
	switch verb {
	case VerbDone:
		return relay.HideDone(rep).Bindings
	case VerbAnswer:
		var out []relay.BindingStatus
		for _, b := range rep.Bindings {
			if b.BuilderStatus == herdr.StatusBlocked {
				out = append(out, b)
			}
		}
		return out
	default:
		return rep.Bindings
	}
}

// emptyText is the one line shown when rowsFor is empty (spec §4).
func emptyText(verb Verb) string {
	switch verb {
	case VerbDone:
		return "no bindings to mark done"
	case VerbAnswer:
		return "no builder is blocked"
	default:
		return "nothing bound"
	}
}
```

- [ ] **Step 5: Write the failing list-window test**

Create `internal/pick/list_test.go`:

```go
package pick

import "testing"

// TestListWindow pins the window rule copied from internal/ui/list.go: the
// cursor is always visible and the top moves as little as possible.
func TestListWindow(t *testing.T) {
	cases := []struct {
		name                 string
		top, cursor, rows, n int
		want                 int
	}{
		{"no budget", 3, 5, 0, 10, 0},
		{"everything fits", 3, 5, 10, 8, 0},
		{"cursor inside window", 2, 4, 3, 10, 2},
		{"cursor above window", 5, 2, 3, 10, 2},
		{"cursor below window", 0, 6, 3, 10, 4},
		{"top past the end is clamped", 9, 9, 3, 10, 7},
	}
	for _, c := range cases {
		if got := listWindow(c.top, c.cursor, c.rows, c.n); got != c.want {
			t.Errorf("%s: listWindow(%d,%d,%d,%d) = %d, want %d", c.name, c.top, c.cursor, c.rows, c.n, got, c.want)
		}
	}
}

func TestRenderRowMarksTheSelection(t *testing.T) {
	r := row("webshop", "NEEDS YOU", "blocked")
	sel := renderRow(r, true)
	unsel := renderRow(r, false)
	if sel == unsel {
		t.Fatal("selected and unselected rows render identically")
	}
	for _, want := range []string{"webshop", "NEEDS YOU", "r2", "agy", "blocked"} {
		if !contains(unsel, want) {
			t.Errorf("row %q lacks %q", unsel, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 6: Create `internal/pick/list.go`**

```go
package pick

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
)

// The picker's own styles. internal/ui has near-identical ones; they are
// not shared because pick must not import ui (spec §8).
var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	hintStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	cursorStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	needsYouStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	doneStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	activeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
)

// listChrome is what the list screen spends outside the rows: the title
// line, the blank under it, and the hint line.
const listChrome = 3

// listWindow returns where the first rendered row must be for cursor to be
// visible in a window of rows rows over n items, moving top as little as
// possible. rows <= 0 means no limit: the answer is 0 and every row renders.
// Copied from internal/ui/list.go, which pick must not import.
func listWindow(top, cursor, rows, n int) int {
	if rows <= 0 || n <= rows {
		return 0
	}
	if top > n-rows {
		top = n - rows
	}
	if top < 0 {
		top = 0
	}
	if cursor < top {
		top = cursor
	}
	if cursor >= top+rows {
		top = cursor - rows + 1
	}
	return top
}

func styleDisplay(display string) string {
	padded := fmt.Sprintf("%-9s", display)
	switch display {
	case "NEEDS YOU":
		return needsYouStyle.Render(padded)
	case "DONE":
		return doneStyle.Render(padded)
	case "ACTIVE":
		return activeStyle.Render(padded)
	default:
		return padded
	}
}

// renderRow is one binding line (spec §4):
//
//	> webshop        NEEDS YOU  r3  builder agy blocked
func renderRow(b relay.BindingStatus, selected bool) string {
	cursor := " "
	if selected {
		cursor = cursorStyle.Render(">")
	}
	return strings.TrimRight(fmt.Sprintf("%s %-14s %s r%-2d builder %s %s",
		cursor, b.Name, styleDisplay(b.Display), b.Round, b.BuilderCandidate, b.BuilderStatus), " ")
}

// listRows is how many binding rows fit: the height less the chrome, never
// below one once a height is known, zero (no limit) before the first
// WindowSizeMsg.
func (m Model) listRows() int {
	if m.height <= 0 {
		return 0
	}
	n := m.height - listChrome
	if n < 1 {
		n = 1
	}
	return n
}

func (m Model) listView() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("relay %s -- pick a binding", m.opts.Verb)))
	sb.WriteString("\n\n")
	if !m.loaded {
		sb.WriteString(hintStyle.Render("loading…"))
		return sb.String()
	}
	rows := m.listRows()
	top := listWindow(m.top, m.cursor, rows, len(m.rows))
	end := len(m.rows)
	if rows > 0 && top+rows < end {
		end = top + rows
	}
	for i := top; i < end; i++ {
		sb.WriteString(renderRow(m.rows[i], i == m.cursor))
		sb.WriteString("\n")
	}
	sb.WriteString(hintStyle.Render("↑/↓ move  Enter pick  Esc cancel"))
	return sb.String()
}
```

- [ ] **Step 7: Create `internal/pick/result.go`**

```go
package pick

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// resultModel is the last screen: what the verb said (or the error), held
// until a key so a popup pane does not vanish before it is read (spec §5).
type resultModel struct {
	pending bool   // the verb is running; keys other than ctrl+c are ignored
	text    string // success text, or the empty-list line
	err     error  // shown wrapped, in place of text
	// outcome is what Run returns once the key is pressed: nil after a
	// successful verb, else one of the sentinels in verb.go.
	outcome error
}

const maxErrorLines = 8

func (m Model) resultView() string {
	var sb strings.Builder
	switch {
	case m.result.pending:
		sb.WriteString(hintStyle.Render(fmt.Sprintf("running relay %s…", m.opts.Verb)))
		return sb.String()
	case m.result.err != nil:
		sb.WriteString(errorStyle.Render("relay: ") + renderError(m.result.err, m.width))
	default:
		sb.WriteString(m.result.text)
	}
	sb.WriteString("\n\n")
	sb.WriteString(hintStyle.Render("press any key"))
	return sb.String()
}

// renderError wraps err across width, preserving the newlines the message
// already has, capped at maxErrorLines with a trailing "…". Copied from
// internal/ui/list.go, which pick must not import.
func renderError(err error, width int) string {
	if err == nil {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	var wrapped []string
	for _, l := range strings.Split(err.Error(), "\n") {
		wrapped = append(wrapped, wrapLine(l, width)...)
	}
	if len(wrapped) == 0 {
		return ""
	}
	if len(wrapped) > maxErrorLines {
		wrapped = wrapped[:maxErrorLines]
		last := wrapped[maxErrorLines-1]
		ellipsis := "…"
		ew := lipgloss.Width(ellipsis)
		for lipgloss.Width(last)+ew > width && len(last) > 0 {
			runes := []rune(last)
			last = string(runes[:len(runes)-1])
		}
		wrapped[maxErrorLines-1] = last + ellipsis
	}
	return strings.Join(wrapped, "\n")
}

func wrapLine(line string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	var lines []string
	var cur strings.Builder
	curW := 0
	flush := func() {
		lines = append(lines, cur.String())
		cur.Reset()
		curW = 0
	}
	writeLong := func(w string) {
		for _, r := range w {
			rw := lipgloss.Width(string(r))
			if curW+rw > width && cur.Len() > 0 {
				flush()
			}
			cur.WriteRune(r)
			curW += rw
		}
	}
	for _, w := range strings.Split(line, " ") {
		ww := lipgloss.Width(w)
		switch {
		case cur.Len() == 0 && ww > width:
			writeLong(w)
		case cur.Len() == 0:
			cur.WriteString(w)
			curW = ww
		case curW+1+ww <= width:
			cur.WriteByte(' ')
			cur.WriteString(w)
			curW += 1 + ww
		default:
			flush()
			if ww > width {
				writeLong(w)
			} else {
				cur.WriteString(w)
				curW = ww
			}
		}
	}
	if cur.Len() > 0 {
		flush()
	}
	return lines
}
```

- [ ] **Step 8: Write the failing model tests**

Create `internal/pick/model_test.go`:

```go
package pick

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// wantQuit asserts the command is tea.Quit and the model carries outcome.
func wantQuit(t *testing.T, m Model, cmd func() interface{}, outcome error) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected tea.Quit, got no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected tea.Quit")
	}
	if !errors.Is(m.outcome, outcome) {
		t.Fatalf("outcome = %v, want %v", m.outcome, outcome)
	}
}

func TestEmptyListEndsOnResultScreenWithNothingToPick(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg())
	if m.screen != screenResult || m.result.text != "no bindings to mark done" {
		t.Fatalf("screen=%v text=%q", m.screen, m.result.text)
	}
	m, cmd := update(t, m, key("x"))
	wantQuit(t, m, cmd, ErrNothingToPick)
}

func TestStatusErrorEndsOnResultScreen(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbUnbind})
	m, _ = update(t, m, statusMsg{err: errors.New("herdr: connection refused")})
	if m.screen != screenResult || m.result.err == nil {
		t.Fatalf("screen=%v err=%v", m.screen, m.result.err)
	}
	m, cmd := update(t, m, key("enter"))
	wantQuit(t, m, cmd, ErrVerbFailed)
}

func TestListCursorStaysInBounds(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbUnbind})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working"), row("b", "ACTIVE", "working")))
	m, _ = update(t, m, key("up"))
	if m.cursor != 0 {
		t.Fatalf("cursor above the top: %d", m.cursor)
	}
	m, _ = update(t, m, key("j"))
	m, _ = update(t, m, key("down"))
	if m.cursor != 1 {
		t.Fatalf("cursor past the end: %d", m.cursor)
	}
	m, _ = update(t, m, key("k"))
	if m.cursor != 0 {
		t.Fatalf("k did not move up: %d", m.cursor)
	}
}

func TestEscCancelsFromTheList(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("a", "ACTIVE", "working")))
	m, cmd := update(t, m, key("esc"))
	wantQuit(t, m, cmd, ErrCancelled)
}

func TestCtrlCCancelsFromAnyScreen(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg())
	m, cmd := update(t, m, key("ctrl+c"))
	wantQuit(t, m, cmd, ErrCancelled)
}

func TestEnterRunsDoneAndShowsItsText(t *testing.T) {
	fh := newFakeHerdr(t)
	rt := testRuntime(t, fh, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))

	m, cmd := update(t, m, key("enter"))
	if m.screen != screenResult || !m.result.pending {
		t.Fatalf("enter should move to a pending result screen: screen=%v pending=%v", m.screen, m.result.pending)
	}
	if cmd == nil {
		t.Fatal("enter returned no command")
	}
	m, _ = update(t, m, cmd())
	if m.result.pending || m.result.err != nil {
		t.Fatalf("result: pending=%v err=%v", m.result.pending, m.result.err)
	}
	if m.result.text != relay.DoneText("webshop") {
		t.Fatalf("text = %q, want DoneText", m.result.text)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil || b.State != store.StateDone {
		t.Fatalf("binding after done: state=%v err=%v", b.State, err)
	}
	m, quit := update(t, m, key("enter"))
	wantQuit(t, m, quit, nil)
}

func TestEnterRunsUnbindWithArchive(t *testing.T) {
	fh := newFakeHerdr(t)
	rt := testRuntime(t, fh, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbUnbind, Archive: true})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))

	m, cmd := update(t, m, key("enter"))
	m, _ = update(t, m, cmd())
	if m.result.err != nil {
		t.Fatalf("unbind: %v", m.result.err)
	}
	if !contains(m.result.text, "archived webshop to ") {
		t.Fatalf("text = %q, want the archive line", m.result.text)
	}
	if _, err := rt.Store.Load("webshop"); err == nil {
		t.Fatal("binding still loads after unbind")
	}
}

func TestVerbErrorShowsAndExitsOne(t *testing.T) {
	fh := newFakeHerdr(t)
	rt := testRuntime(t, fh) // no binding named webshop
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))
	m, cmd := update(t, m, key("enter"))
	m, _ = update(t, m, cmd())
	if m.result.err == nil {
		t.Fatal("done on a missing binding should fail")
	}
	m, quit := update(t, m, key("x"))
	wantQuit(t, m, quit, ErrVerbFailed)
}

func TestKeysAreIgnoredWhileTheVerbRuns(t *testing.T) {
	fh := newFakeHerdr(t)
	rt := testRuntime(t, fh, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbDone})
	m, _ = update(t, m, rowsMsg(row("webshop", "ACTIVE", "working")))
	m, _ = update(t, m, key("enter"))
	m, cmd := update(t, m, key("enter"))
	if cmd != nil {
		t.Fatal("a key during a pending verb must not quit or re-run")
	}
	if m.outcome != nil {
		t.Fatalf("outcome set early: %v", m.outcome)
	}
}
```

- [ ] **Step 9: Create `internal/pick/model.go`**

```go
package pick

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

type screen int

const (
	screenList screen = iota
	screenAnswer
	screenResult
)

// statusMsg is the list's rows, or why there are none.
type statusMsg struct {
	report relay.Report
	err    error
}

// verbDoneMsg is what the verb said, or why it failed.
type verbDoneMsg struct {
	text string
	err  error
}

// Model is the whole picker: one of three screens at a time. Every herdr
// and store call runs in a tea.Cmd and comes back as a message, so Update
// is pure and tests drive it with messages.
type Model struct {
	ctx  context.Context
	rt   relay.Runtime
	opts Options

	screen        screen
	width, height int

	// list screen
	rows   []relay.BindingStatus
	loaded bool // false until the first statusMsg
	cursor int
	top    int

	result resultModel

	// outcome is what Run returns: nil after a verb succeeded, else one of
	// the sentinels in verb.go. Set exactly once, by quit.
	outcome error
}

func newModel(ctx context.Context, rt relay.Runtime, opts Options) Model {
	return Model{ctx: ctx, rt: rt, opts: opts, screen: screenList}
}

func (m Model) Init() tea.Cmd {
	return fetchStatus(m.ctx, m.rt)
}

func fetchStatus(ctx context.Context, rt relay.Runtime) tea.Cmd {
	return func() tea.Msg {
		rep, err := relay.Status(ctx, rt)
		if err != nil {
			return statusMsg{err: err}
		}
		return statusMsg{report: rep}
	}
}

// runVerb runs the chosen verb and reports its text, which is the same text
// the CLI prints (spec §5). in is only read by answer.
func runVerb(ctx context.Context, rt relay.Runtime, opts Options, name string, in relay.AnswerInput) tea.Cmd {
	return func() tea.Msg {
		switch opts.Verb {
		case VerbDone:
			if err := relay.Done(ctx, rt, name); err != nil {
				return verbDoneMsg{err: err}
			}
			return verbDoneMsg{text: relay.DoneText(name)}
		case VerbUnbind:
			res, err := relay.Unbind(ctx, rt, name, opts.Archive)
			if err != nil {
				return verbDoneMsg{err: err}
			}
			return verbDoneMsg{text: relay.UnbindText(name, res)}
		case VerbAnswer:
			if err := relay.Answer(ctx, rt, name, in); err != nil {
				return verbDoneMsg{err: err}
			}
			return verbDoneMsg{text: relay.AnswerText(name)}
		}
		return verbDoneMsg{err: fmt.Errorf("unknown verb %q", opts.Verb)}
	}
}

// quit records the outcome and ends the program. It is the only place
// outcome is written.
func (m Model) quit(outcome error) (tea.Model, tea.Cmd) {
	m.outcome = outcome
	return m, tea.Quit
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.top = listWindow(m.top, m.cursor, m.listRows(), len(m.rows))
		return m, nil

	case statusMsg:
		return m.onStatus(msg)

	case verbDoneMsg:
		m.result = resultModel{text: msg.text, err: msg.err}
		if msg.err != nil {
			m.result.outcome = ErrVerbFailed
		}
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m.quit(ErrCancelled)
		}
		switch m.screen {
		case screenList:
			return m.listKeys(msg)
		case screenResult:
			return m.resultKeys(msg)
		}
	}
	return m, nil
}

func (m Model) onStatus(msg statusMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.screen = screenResult
		m.result = resultModel{err: msg.err, outcome: ErrVerbFailed}
		return m, nil
	}
	m.rows = rowsFor(m.opts.Verb, msg.report)
	m.loaded = true
	if len(m.rows) == 0 {
		m.screen = screenResult
		m.result = resultModel{text: emptyText(m.opts.Verb), outcome: ErrNothingToPick}
		return m, nil
	}
	return m, nil
}

func (m Model) listKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		return m.quit(ErrCancelled)
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "enter":
		if !m.loaded || len(m.rows) == 0 {
			return m, nil
		}
		return m.pick(m.rows[m.cursor])
	}
	m.top = listWindow(m.top, m.cursor, m.listRows(), len(m.rows))
	return m, nil
}

// pick acts on the chosen row. done and unbind run at once; answer needs
// its own screen first (spec §6), added with the answer screen.
func (m Model) pick(r relay.BindingStatus) (tea.Model, tea.Cmd) {
	switch m.opts.Verb {
	case VerbDone, VerbUnbind:
		m.screen = screenResult
		m.result = resultModel{pending: true}
		return m, runVerb(m.ctx, m.rt, m.opts, r.Name, relay.AnswerInput{})
	}
	return m, nil
}

func (m Model) resultKeys(_ tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.result.pending {
		return m, nil
	}
	return m.quit(m.result.outcome)
}

func (m Model) View() string {
	switch m.screen {
	case screenResult:
		return m.resultView()
	default:
		return m.listView()
	}
}
```

- [ ] **Step 10: Run the pick tests**

Run: `go test ./internal/pick`
Expected: PASS -- every test in `verb_test.go`, `list_test.go`,
`model_test.go`.

- [ ] **Step 11: Verify and commit**

Run: `make check`
Expected: green. Confirm `grep -rn '"github.com/fuad-daoud/relay/internal/ui"' internal/pick` prints nothing.

```bash
git add internal/pick
git commit -m "feat(pick): binding list and result screens; done and unbind run from the list (#15 task 2)"
```

---

### Task 3: `internal/pick` -- the answer screen

**Files:**
- Create: `internal/pick/answer.go`, `internal/pick/answer_test.go`
- Modify: `internal/pick/model.go` (`Model.answer` field, `Update` cases,
  `pick`, `onStatus` shortcut, `View`)

**Interfaces:**
- Consumes (Task 1): `relay.ParseAnswer`, `relay.DialogSource`,
  `relay.DialogLines`; (Task 2) `Model`, `runVerb`, `quit`, styles;
  (existing) `relay.Target`, `rt.Store.Load`, `rt.Store.QuestionPath`,
  `rt.Herdr.ReadAgentSource`.
- Produces: `type answerModel`, `type dialogMsg`,
  `func fetchDialog(ctx, rt, name string) tea.Cmd`,
  `func (m Model) enterAnswer(name string) (tea.Model, tea.Cmd)`.

- [ ] **Step 1: Write the failing answer-screen tests**

Create `internal/pick/answer_test.go`:

```go
package pick

import (
	"context"
	"errors"
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
)

func typeString(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// answerModelFor puts the model on the answer screen for name, with the
// dialog already read, the way the list would after Enter.
func answerModelFor(t *testing.T, fh *fakeHerdr, name string) (Model, relay.Runtime) {
	t.Helper()
	rt := testRuntime(t, fh, testBinding(name))
	m := newModel(context.Background(), rt, Options{Verb: VerbAnswer})
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(t, m, rowsMsg(row(name, "NEEDS YOU", "blocked"), row("other", "NEEDS YOU", "blocked")))
	m, _ = update(t, m, key("enter")) // picks name (cursor 0)
	return m, rt
}

func TestSingleBlockedRowSkipsTheList(t *testing.T) {
	fh := newFakeHerdr(t)
	fh.readOut = "Allow rm?\n 1. Yes\n 2. No"
	rt := testRuntime(t, fh, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbAnswer})
	m, cmd := update(t, m, rowsMsg(row("webshop", "NEEDS YOU", "blocked")))
	if m.screen != screenAnswer || m.answer.name != "webshop" {
		t.Fatalf("screen=%v name=%q; one blocked row should open the answer screen directly", m.screen, m.answer.name)
	}
	if cmd == nil {
		t.Fatal("entering the answer screen must fetch the dialog")
	}
}

func TestTwoBlockedRowsShowTheList(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbAnswer})
	m, _ = update(t, m, rowsMsg(row("a", "NEEDS YOU", "blocked"), row("b", "NEEDS YOU", "blocked")))
	if m.screen != screenList {
		t.Fatalf("screen=%v, want the list", m.screen)
	}
}

func TestFetchDialogReadsTheBuilderLive(t *testing.T) {
	fh := newFakeHerdr(t)
	fh.readOut = "Allow rm?\n 1. Yes\n 2. No"
	rt := testRuntime(t, fh, testBinding("webshop"))
	msg := fetchDialog(context.Background(), rt, "webshop")().(dialogMsg)
	if msg.err != nil || msg.text != fh.readOut || msg.round != 2 {
		t.Fatalf("msg = %+v", msg)
	}
	if len(fh.reads) != 1 || fh.reads[0] != relay.Target(testBinding("webshop").Builder) {
		t.Fatalf("reads = %v, want one read of the builder", fh.reads)
	}
}

func TestFetchDialogFallsBackToTheQuestionFile(t *testing.T) {
	fh := newFakeHerdr(t)
	fh.readErr = errors.New("herdr: read timed out")
	rt := testRuntime(t, fh, testBinding("webshop"))
	path := rt.Store.QuestionPath("webshop", 2)
	if err := os.WriteFile(path, []byte("stale dialog"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := fetchDialog(context.Background(), rt, "webshop")().(dialogMsg)
	if msg.text != "stale dialog" {
		t.Fatalf("text = %q, want the question file", msg.text)
	}
	if msg.err == nil {
		t.Fatal("the read error must still be reported so the human knows the text may be stale")
	}
}

func TestFetchDialogReportsWhenNeitherSourceWorks(t *testing.T) {
	fh := newFakeHerdr(t)
	fh.readErr = errors.New("herdr: read timed out")
	rt := testRuntime(t, fh, testBinding("webshop"))
	msg := fetchDialog(context.Background(), rt, "webshop")().(dialogMsg)
	if msg.err == nil || msg.text != "" {
		t.Fatalf("msg = %+v, want an error and no text", msg)
	}
}

func TestAnswerRefusesEmptyInput(t *testing.T) {
	m, _ := answerModelFor(t, newFakeHerdr(t), "webshop")
	m, cmd := update(t, m, key("enter"))
	if cmd != nil || m.screen != screenAnswer {
		t.Fatal("empty input must not submit")
	}
	if m.answer.refuse != "type a key name, a number or text" {
		t.Fatalf("refusal = %q", m.answer.refuse)
	}
	m = typeString(t, m, "2")
	if m.answer.refuse != "" {
		t.Fatal("typing must clear the refusal")
	}
}

func TestAnswerEnterSendsTheParsedAnswer(t *testing.T) {
	fh := newFakeHerdr(t)
	fh.agents = []herdr.Agent{blockedAgent("webshop")}
	m, _ := answerModelFor(t, fh, "webshop")
	m = typeString(t, m, "2")
	m, cmd := update(t, m, key("enter"))
	if m.screen != screenResult || !m.result.pending || cmd == nil {
		t.Fatalf("enter with input should run the verb: screen=%v pending=%v", m.screen, m.result.pending)
	}
	m, _ = update(t, m, cmd())
	if m.result.err != nil {
		t.Fatalf("answer: %v", m.result.err)
	}
	if m.result.text != relay.AnswerText("webshop") {
		t.Fatalf("text = %q", m.result.text)
	}
	if len(fh.keys) != 1 || fh.keys[0].Keys != "2" || fh.keys[0].Target != "w1:p2" {
		t.Fatalf("keys = %+v, want \"2\" to the builder pane", fh.keys)
	}
}

func TestAnswerEscCancels(t *testing.T) {
	m, _ := answerModelFor(t, newFakeHerdr(t), "webshop")
	m = typeString(t, m, "half typed")
	m, cmd := update(t, m, key("esc"))
	wantQuit(t, m, cmd, ErrCancelled)
}

func TestAnswerViewShowsTheReadErrorAndStillTakesInput(t *testing.T) {
	m, _ := answerModelFor(t, newFakeHerdr(t), "webshop")
	m, _ = update(t, m, dialogMsg{round: 2, err: errors.New("herdr: read timed out")})
	if !contains(m.View(), "could not read dialog: herdr: read timed out") {
		t.Fatalf("view lacks the read error:\n%s", m.View())
	}
	m = typeString(t, m, "enter")
	if m.answer.input.Value() != "enter" {
		t.Fatalf("input = %q", m.answer.input.Value())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/pick`
Expected: FAIL to build -- `m.answer undefined`, `undefined: fetchDialog`,
`undefined: dialogMsg`.

- [ ] **Step 3: Create `internal/pick/answer.go`**

```go
package pick

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

// dialogMsg is the builder's dialog text, or why it could not be read. text
// may be set alongside err: the live read failed and the round's question
// file stood in, which the human should know is possibly stale.
type dialogMsg struct {
	round int
	text  string
	err   error
}

// answerModel is the answer screen (spec §6): the dialog above, one input
// line below. It parses nothing from the dialog.
type answerModel struct {
	name   string
	round  int
	loaded bool
	err    error  // read failure, shown above the body
	refuse string // inline refusal after an empty submit; cleared on typing
	vp     viewport.Model
	input  textinput.Model
}

// answerChrome is what the answer screen spends outside the body: header,
// two rules, the input line, and the refusal/blank line.
const answerChrome = 5

func bodyHeight(h int) int {
	n := h - answerChrome
	if n < 1 {
		n = 1
	}
	return n
}

func newAnswerModel(name string, width, height int) answerModel {
	ti := textinput.New()
	ti.Prompt = "answer> "
	ti.CharLimit = 200
	ti.Focus()
	return answerModel{name: name, vp: viewport.New(width, bodyHeight(height)), input: ti}
}

// fetchDialog reads the builder's dialog the way the daemon does, from the
// detection source, and falls back to the question file the daemon wrote
// for this round. Both failing is reported with no text; the human can still
// see the builder pane and type.
func fetchDialog(ctx context.Context, rt relay.Runtime, name string) tea.Cmd {
	return func() tea.Msg {
		b, err := rt.Store.Load(name)
		if err != nil {
			return dialogMsg{err: err}
		}
		text, err := rt.Herdr.ReadAgentSource(ctx, relay.Target(b.Builder), relay.DialogSource, relay.DialogLines)
		if err == nil {
			return dialogMsg{round: b.Round, text: text}
		}
		stale, ferr := os.ReadFile(rt.Store.QuestionPath(name, b.Round))
		if ferr != nil {
			return dialogMsg{round: b.Round, err: err}
		}
		return dialogMsg{round: b.Round, text: string(stale), err: err}
	}
}

func (m Model) enterAnswer(name string) (tea.Model, tea.Cmd) {
	m.screen = screenAnswer
	m.answer = newAnswerModel(name, m.width, m.height)
	return m, tea.Batch(fetchDialog(m.ctx, m.rt, name), textinput.Blink)
}

func (m Model) onDialog(msg dialogMsg) (tea.Model, tea.Cmd) {
	m.answer.round = msg.round
	m.answer.err = msg.err
	m.answer.loaded = true
	m.answer.vp.SetContent(msg.text)
	return m, nil
}

// answerKeys: arrows and page keys scroll the dialog; Enter submits; Esc
// cancels; everything else is typed. j/k are not scroll keys here because
// they are letters the human may need to type.
func (m Model) answerKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m.quit(ErrCancelled)
	case "up":
		m.answer.vp.LineUp(1)
		return m, nil
	case "down":
		m.answer.vp.LineDown(1)
		return m, nil
	case "pgup":
		m.answer.vp.ViewUp()
		return m, nil
	case "pgdown":
		m.answer.vp.ViewDown()
		return m, nil
	case "enter":
		s := strings.TrimSpace(m.answer.input.Value())
		if s == "" {
			m.answer.refuse = "type a key name, a number or text"
			return m, nil
		}
		m.screen = screenResult
		m.result = resultModel{pending: true}
		return m, runVerb(m.ctx, m.rt, m.opts, m.answer.name, relay.ParseAnswer(s))
	}
	m.answer.refuse = ""
	var cmd tea.Cmd
	m.answer.input, cmd = m.answer.input.Update(msg)
	return m, cmd
}

func (m Model) answerView() string {
	a := m.answer
	width := m.width
	if width <= 0 {
		width = 80
	}
	rule := hintStyle.Render(strings.Repeat("─", width))
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("answer %s (round %d)", a.name, a.round)))
	sb.WriteString("  ")
	sb.WriteString(hintStyle.Render("↑/↓ scroll  Enter send  Esc cancel"))
	sb.WriteString("\n" + rule + "\n")
	if a.err != nil {
		sb.WriteString(errorStyle.Render("could not read dialog: ") + a.err.Error() + "\n")
	}
	if !a.loaded {
		sb.WriteString(hintStyle.Render("reading the dialog…"))
	} else {
		sb.WriteString(a.vp.View())
	}
	sb.WriteString("\n" + rule + "\n")
	sb.WriteString(a.input.View())
	if a.refuse != "" {
		sb.WriteString("\n" + errorStyle.Render(a.refuse))
	}
	return sb.String()
}
```

- [ ] **Step 4: Wire the answer screen into `model.go`**

In `internal/pick/model.go`:

Add the field to `Model`, after `result resultModel`:

```go
	answer answerModel
```

In `Update`, extend the `tea.WindowSizeMsg` case so the viewport follows
the terminal:

```go
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.top = listWindow(m.top, m.cursor, m.listRows(), len(m.rows))
		m.answer.vp.Width = msg.Width
		m.answer.vp.Height = bodyHeight(msg.Height)
		return m, nil
```

Add a `dialogMsg` case after `verbDoneMsg`:

```go
	case dialogMsg:
		return m.onDialog(msg)
```

In the `tea.KeyMsg` case add the answer screen:

```go
		case screenAnswer:
			return m.answerKeys(msg)
```

After the `tea.KeyMsg` case, before the closing brace of the switch, add a
default so the text input's blink messages reach it:

```go
	default:
		if m.screen == screenAnswer {
			var cmd tea.Cmd
			m.answer.input, cmd = m.answer.input.Update(msg)
			return m, cmd
		}
```

In `onStatus`, after `m.loaded = true` and the empty check, add the spec §4
shortcut:

```go
	if m.opts.Verb == VerbAnswer && len(m.rows) == 1 {
		return m.enterAnswer(m.rows[0].Name)
	}
```

In `pick`, add the answer case and drop the trailing comment about the
answer screen being added later:

```go
	case VerbAnswer:
		return m.enterAnswer(r.Name)
```

In `View`, add:

```go
	case screenAnswer:
		return m.answerView()
```

- [ ] **Step 5: Run the pick tests**

Run: `go test ./internal/pick`
Expected: PASS, including every test in `answer_test.go`. If
`TestAnswerEnterSendsTheParsedAnswer` fails inside `relay.Answer` with
`ErrBuilderGone`, the fake's agent does not match the binding's endpoint by
`AgentName` -- fix the test fixture, not `relay.Answer`.

- [ ] **Step 6: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/pick
git commit -m "feat(pick): answer screen -- live dialog above, one typed line below (#15 task 3)"
```

---

### Task 4: `pick.Run` and the `--pick` flag on `done`, `unbind`, `answer`

**Files:**
- Create: `internal/pick/pick.go`, `internal/pick/pick_test.go`
- Modify: `cmd/relay/main.go` -- imports, `cmdUnbind`, `cmdAnswer`, `cmdDone`,
  new `runPick`, the help text at lines ~52-58
- Modify: `cmd/relay/main_test.go` (append two tests)

**Interfaces:**
- Consumes (Task 2): `Options`, `newModel`, `Model.outcome`, the sentinels.
- Produces: `func Run(ctx context.Context, rt relay.Runtime, opts Options) error`.

- [ ] **Step 1: Write the failing `Run` tests**

Create `internal/pick/pick_test.go`:

```go
package pick

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

type fakeFileInfo struct{ mode os.FileMode }

func (f fakeFileInfo) Name() string       { return "stdout" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() interface{}   { return nil }

func TestRunRefusesWithoutATerminal(t *testing.T) {
	orig := stdoutStat
	stdoutStat = func() (os.FileInfo, error) { return fakeFileInfo{mode: 0}, nil }
	defer func() { stdoutStat = orig }()

	err := Run(context.Background(), relay.Runtime{}, Options{Verb: VerbDone})
	want := "--pick needs a terminal; name the binding instead"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

// TestRunResult pins the exit mapping without starting a terminal program:
// a cancelled context is ErrCancelled (exit 1, spec §9), any other program
// error passes through, and a clean exit returns the model's outcome.
func TestRunResult(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	other := errors.New("tty gone")

	cases := []struct {
		name    string
		ctx     context.Context
		runErr  error
		outcome error
		want    error
	}{
		{"cancelled ctx, killed", cancelled, tea.ErrProgramKilled, nil, ErrCancelled},
		{"cancelled ctx, interrupted", cancelled, tea.ErrInterrupted, nil, ErrCancelled},
		{"live ctx, other error", context.Background(), other, nil, other},
		{"clean exit, success", context.Background(), nil, nil, nil},
		{"clean exit, verb failed", context.Background(), nil, ErrVerbFailed, ErrVerbFailed},
		{"clean exit, nothing to pick", context.Background(), nil, ErrNothingToPick, ErrNothingToPick},
	}
	for _, c := range cases {
		got := runResult(c.ctx, c.runErr, Model{outcome: c.outcome})
		if !errors.Is(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/pick -run 'TestRun'`
Expected: FAIL to build -- `undefined: stdoutStat`, `undefined: Run`,
`undefined: runResult`.

- [ ] **Step 3: Create `internal/pick/pick.go`**

```go
package pick

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

// stdoutStat is os.Stdout.Stat, replaceable so the terminal refusal path can
// be tested without a terminal.
var stdoutStat = os.Stdout.Stat

// Run opens the picker for opts.Verb and returns when the human is done.
//
// Preconditions:  stdout is a character device; rt.Herdr and rt.Store non-nil.
// Postconditions: the terminal is restored. On ErrCancelled, "cancelled" has
// been printed to stdout after the alternate screen closed (spec §3).
// Errors:         nil after a successful verb; ErrCancelled, ErrNothingToPick
// or ErrVerbFailed for the three outcomes already shown on screen; any other
// error is a startup or terminal failure the caller should print.
func Run(ctx context.Context, rt relay.Runtime, opts Options) error {
	info, err := stdoutStat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return errors.New("--pick needs a terminal; name the binding instead")
	}
	if rt.Herdr == nil || rt.Store == nil {
		return errors.New("runtime requires Herdr and Store")
	}

	p := tea.NewProgram(newModel(ctx, rt, opts), tea.WithAltScreen(), tea.WithContext(ctx))
	final, err := p.Run()
	m, _ := final.(Model)
	err = runResult(ctx, err, m)
	if errors.Is(err, ErrCancelled) {
		fmt.Println("cancelled")
	}
	return err
}

// runResult maps bubbletea's exit into Run's contract. A cancelled context
// is a cancel, not a crash; a clean exit returns whatever the model decided.
// Kept separate from Run so it can be tested without a tty.
func runResult(ctx context.Context, err error, m Model) error {
	if ctx.Err() != nil && (errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, tea.ErrInterrupted)) {
		return ErrCancelled
	}
	if err != nil {
		return err
	}
	return m.outcome
}
```

- [ ] **Step 4: Run the pick tests**

Run: `go test ./internal/pick`
Expected: PASS.

- [ ] **Step 5: Write the failing CLI tests**

Append to `cmd/relay/main_test.go`:

```go
// TestPickRejectsAName pins spec §3: --pick chooses the binding, so naming
// one as well is a usage error. Each case fails before newRuntime, so no
// herdr is reached.
func TestPickRejectsAName(t *testing.T) {
	for _, args := range [][]string{
		{"done", "--pick", "x"},
		{"done", "--pick", "--name", "x"},
		{"unbind", "--pick", "x"},
		{"unbind", "--pick", "--name", "x", "--archive"},
		{"answer", "--pick", "x"},
		{"answer", "--pick", "--name", "x"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "--pick chooses the binding") {
			t.Errorf("%v: got %v, want the --pick usage error", args, err)
		}
	}
}

// TestPickRejectsAnswerFlags: the answer comes from the screen, so --keys,
// --choice and --text have nothing to apply to. Fails before newRuntime.
func TestPickRejectsAnswerFlags(t *testing.T) {
	for _, args := range [][]string{
		{"answer", "--pick", "--keys", "enter"},
		{"answer", "--pick", "--choice", "2"},
		{"answer", "--pick", "--text", "yes"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "--pick takes the answer from the screen") {
			t.Errorf("%v: got %v, want the answer-flags usage error", args, err)
		}
	}
}
```

- [ ] **Step 6: Run to verify they fail**

Run: `go test ./cmd/relay -run 'TestPick'`
Expected: FAIL -- `flag provided but not defined: -pick`.

- [ ] **Step 7: Wire `--pick` into `cmd/relay/main.go`**

Add `"github.com/fuad-daoud/relay/internal/pick"` to the imports.

Add this helper next to `cmdUI`:

```go
// runPick opens the interactive picker for one verb (#15). The three
// non-zero outcomes have already been shown on screen, so they exit 1
// silently: a second "relay: ..." line would go to the plugin log, not to
// the human (spec §3). Anything else is a startup failure and prints.
func runPick(opts pick.Options) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = pick.Run(ctx, rt, opts)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pick.ErrCancelled), errors.Is(err, pick.ErrNothingToPick), errors.Is(err, pick.ErrVerbFailed):
		return exitCodeErr{code: 1}
	default:
		return err
	}
}

// pickNamesNothing is the spec §3 rule shared by the three verbs: --pick and
// a binding name are mutually exclusive.
func pickNamesNothing(nameFlag string, positional []string) error {
	if nameFlag != "" || len(positional) > 0 {
		return errors.New("--pick chooses the binding; do not also name one")
	}
	return nil
}
```

`cmdDone` -- add the flag and the branch:

```go
	name := fs.String("name", "", "binding to mark done")
	pickFlag := fs.Bool("pick", false, "choose the binding from a list (needs a terminal)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *pickFlag {
		if err := pickNamesNothing(*name, fs.Args()); err != nil {
			return err
		}
		return runPick(pick.Options{Verb: pick.VerbDone})
	}
```

and change its usage string to `"usage: relay done <name> | --pick  (or --name <name>)%s\n"`.

`cmdUnbind` -- same shape:

```go
	archive := fs.Bool("archive", false, "move the binding aside instead of deleting it, keeping its round log")
	pickFlag := fs.Bool("pick", false, "choose the binding from a list (needs a terminal)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *pickFlag {
		if err := pickNamesNothing(*name, fs.Args()); err != nil {
			return err
		}
		return runPick(pick.Options{Verb: pick.VerbUnbind, Archive: *archive})
	}
```

usage string: `"usage: relay unbind <name> | --pick [--archive]  (or --name <name>)%s\n"`.

`cmdAnswer` -- the answer flags are refused too, since the screen supplies
the answer:

```go
	choice := fs.Int("choice", 0, "numbered dialog option to pick")
	pickFlag := fs.Bool("pick", false, "choose the blocked builder from a list and type the answer there (needs a terminal)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *pickFlag {
		if err := pickNamesNothing(*name, fs.Args()); err != nil {
			return err
		}
		if *keys != "" || *text != "" || *choice != 0 {
			return errors.New("--pick takes the answer from the screen; drop --keys, --choice and --text")
		}
		return runPick(pick.Options{Verb: pick.VerbAnswer})
	}
```

usage string: `"usage: relay answer <name> (--keys K | --choice N | --text S) | --pick  (or --name <name>)%s\n"`.

Help text (the block around lines 52-58): change the three lines to

```
  answer    answer a builder that is blocked at a dialog (--pick to choose it on screen)
  done      mark a binding done; relaying stops (--pick to choose it on screen)
  unbind    forget a binding, deleting or archiving its directory (--pick to choose it on screen)
```

- [ ] **Step 8: Run the CLI tests**

Run: `go test ./cmd/relay`
Expected: PASS, including `TestPickRejectsAName` and
`TestPickRejectsAnswerFlags`. If the flag-ordering helper `parseFlags` puts
the positional before `--pick` is parsed, the error still comes from
`pickNamesNothing` -- confirm by reading the message, not just non-nil.

- [ ] **Step 9: Verify and commit**

Run: `make check`
Expected: green. `go mod tidy` must leave `go.mod`/`go.sum` untouched
(`textinput` and `viewport` live in the already-required bubbles module).

```bash
git add internal/pick/pick.go internal/pick/pick_test.go cmd/relay/main.go cmd/relay/main_test.go
git commit -m "feat: relay done/unbind/answer --pick open the picker (#15 task 4)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), and `git diff --stat main..HEAD`. If any step was
impossible as written, say which and why -- do not work around it.
