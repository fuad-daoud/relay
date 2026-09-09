# Positional Binding Names (#50) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `send`, `pull`, `diff` and `status` accept a positional binding name instead of silently discarding it and acting on whatever binding owns the current directory.

**Architecture:** All three of `send`, `pull` and `diff` resolve through `resolveBinding`, which reads `--name` only and otherwise falls back to `FindByCWD`. A positional name is parsed into `fs.Args()` and never read — so from a bound directory `relay send frontend --file p.md` silently prompts the *cwd's* builder. Two small pure helpers fix this without touching herdr: `bindingArg` picks a name from the flag and the positionals (refusing two), and `filterReport` narrows a status report to one binding. Both are unit-testable with no daemon and no herdr socket, which matters because CI has neither.

**Tech Stack:** Go 1.x, stdlib `flag` and `testing`. No new dependencies.

## Global Constraints

- Package `main` in `cmd/relay`. No new third-party dependencies.
- **Do not call `newRuntime()` in any new test.** It builds a real herdr client; CI has no herdr. Test the pure helpers directly, and build a `relay.Runtime{Store: ...}` literal where a runtime is needed — `resolveBinding` dereferences only `Store`.
- Bare `relay status` must keep listing **every** binding. Only `send`, `pull` and `diff` keep the cwd fallback; `status` has no fallback because "all bindings" is its natural default.
- `answer`, `done`, `unbind` and `fork` are **out of scope** — they use `explicitBinding` and must keep refusing to guess. Do not touch them.
- Tests that need state must isolate it with `t.Setenv("HOME", ...)` and `t.Setenv("XDG_STATE_HOME", ...)`, matching `cmd/relay/main_test.go:66-68`. Do not use `t.Chdir` — CI runs Go 1.22, where it does not exist.
- `docs/design.md` is a historical record and must **not** be edited; its `relay pull [<name>]` form becomes correct as a result of this change.
- Every task ends green: `go build ./... && go test ./...`.

---

### Task 1: `bindingArg`, and positionals for send/pull/diff

**Files:**
- Modify: `cmd/relay/main.go:961-980` (`resolveBinding`), plus its three call sites at lines 599 (`cmdSend`), 624 (`cmdPull`), 656 (`cmdDiff`)
- Test: `cmd/relay/main_test.go` (append)

**Interfaces:**
- Consumes: `relay.Runtime` (field `Store *store.Store`), `rt.Store.FindByCWD(cwd string) (store.Binding, bool, error)`, `store.New(root string) *store.Store`, `store.Binding`, `store.StateActive`.
- Produces:
  - `func bindingArg(nameFlag string, positional []string) (string, error)` — the chosen name, or `""` when none was given. Errors when a name is given twice or more than one positional appears.
  - `func resolveBinding(rt relay.Runtime, nameFlag string, positional []string) (string, error)` — **signature change**, all three call sites updated to pass `fs.Args()`.

- [ ] **Step 1: Confirm the current suite is green**

Run: `cd /home/fuad/projects/relay && go test ./...`
Expected: PASS (all packages).

- [ ] **Step 2: Add the relay import to the test file**

`cmd/relay/main_test.go` imports `flag`, `errors`, `io`, `os`, `os/exec`, `path/filepath`, `strings`, `testing`, and `github.com/fuad-daoud/relay/internal/store`. Add `"github.com/fuad-daoud/relay/internal/relay"` to the second import group, keeping it gofmt-sorted (it sorts before `.../internal/store`).

- [ ] **Step 3: Write the failing tests**

Append to `cmd/relay/main_test.go`:

```go
func TestBindingArgTakesEitherForm(t *testing.T) {
	cases := []struct {
		label      string
		flag       string
		positional []string
		want       string
		wantErr    bool
	}{
		{"flag only", "webshop", nil, "webshop", false},
		{"positional only", "", []string{"webshop"}, "webshop", false},
		{"neither is not an error", "", nil, "", false},
		{"both at once is refused", "webshop", []string{"other"}, "", true},
		{"same name twice is still refused", "webshop", []string{"webshop"}, "", true},
		{"two positionals refused", "", []string{"a", "b"}, "", true},
	}

	for _, c := range cases {
		got, err := bindingArg(c.flag, c.positional)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected a refusal, got %q", c.label, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.label, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.label, got, c.want)
		}
	}
}

func TestResolveBindingAcceptsAPositionalName(t *testing.T) {
	rt := relay.Runtime{Store: store.New(t.TempDir())}

	// The whole point of #50: a named binding must be used, not discarded in
	// favour of whatever owns the cwd.
	got, err := resolveBinding(rt, "", []string{"webshop"})
	if err != nil {
		t.Fatalf("resolveBinding: %v", err)
	}
	if got != "webshop" {
		t.Errorf("got %q, want webshop", got)
	}
}

func TestResolveBindingRefusesTwoNames(t *testing.T) {
	rt := relay.Runtime{Store: store.New(t.TempDir())}

	if _, err := resolveBinding(rt, "webshop", []string{"frontend"}); err == nil {
		t.Fatal("naming the binding twice must be refused rather than one silently winning")
	}
}

func TestResolveBindingStillFallsBackToCWD(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))

	s := store.New(filepath.Join(tempHome, ".local", "state", "relay"))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Seed a binding that owns the test's own working directory, so the
	// fallback has something to find without any chdir.
	if err := s.Save(store.Binding{
		Name: "here", CWD: cwd, Round: 1, State: store.StateActive,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := resolveBinding(relay.Runtime{Store: s}, "", nil)
	if err != nil {
		t.Fatalf("resolveBinding: %v", err)
	}
	if got != "here" {
		t.Errorf("a bare invocation must still fall back to the cwd binding, got %q", got)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run 'TestBindingArg|TestResolveBinding' -v`
Expected: FAIL to compile — `undefined: bindingArg`, and `resolveBinding` called with 3 arguments when it takes 2. Report the exact compiler output.

- [ ] **Step 5: Write `bindingArg` and rewrite `resolveBinding`**

In `cmd/relay/main.go`, replace `resolveBinding` (lines 959-980, comment included) in full:

```go
// bindingArg picks the binding name out of a --name flag and whatever
// positionals were left over, and refuses to take two. An empty return means
// no name was given at all, which each caller interprets for itself.
//
// It refuses even when both spellings agree: a caller who wrote the name twice
// has a mistaken model of the command, and silently accepting one of them hides
// that until the day the two differ.
func bindingArg(nameFlag string, positional []string) (string, error) {
	switch {
	case nameFlag != "" && len(positional) > 0:
		return "", fmt.Errorf("binding named twice: --name %s and %q; pass it once", nameFlag, positional[0])
	case len(positional) > 1:
		return "", fmt.Errorf("too many binding names: %v; pass one", positional)
	case nameFlag != "":
		return nameFlag, nil
	case len(positional) == 1:
		return positional[0], nil
	default:
		return "", nil
	}
}

// resolveBinding names the binding a command should act on: the one given by
// --name or as a positional, else the binding that owns the current directory,
// so the planner rarely has to name it at all.
//
// The positional form is not decoration. Before #50 these commands read --name
// only and dropped a positional on the floor, which from a bound directory sent
// `relay send frontend --file p.md` to the CWD's builder instead of frontend's,
// with no error and a round log that recorded it as legitimate.
func resolveBinding(rt relay.Runtime, nameFlag string, positional []string) (string, error) {
	name, err := bindingArg(nameFlag, positional)
	if err != nil {
		return "", err
	}
	if name != "" {
		return name, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}

	b, found, err := rt.Store.FindByCWD(cwd)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("no binding for %s; name one with `relay <command> NAME` or run relay bind first", cwd)
	}

	return b.Name, nil
}
```

- [ ] **Step 6: Update the three call sites**

Each currently reads `target, err := resolveBinding(rt, *name)`. Locate them by content — they are in `cmdSend`, `cmdPull` and `cmdDiff` — and change each to:

```go
	target, err := resolveBinding(rt, *name, fs.Args())
```

There are exactly three. Confirm with `grep -n 'resolveBinding(rt' cmd/relay/main.go` that none remain with two arguments.

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run 'TestBindingArg|TestResolveBinding' -v`
Expected: PASS, all four.

- [ ] **Step 8: Run the whole suite**

Run: `cd /home/fuad/projects/relay && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
cd /home/fuad/projects/relay
git add cmd/relay/main.go cmd/relay/main_test.go
git commit -m "fix(relay): accept a positional binding name for send/pull/diff (#50)

These commands read --name only and dropped a positional on the floor,
falling back to the binding that owns the cwd. From an unbound directory
that produced an error blaming the cwd for a binding the caller had named.
From a BOUND directory it acted on the wrong binding -- \`relay send
frontend --file p.md\` prompted the cwd's builder, with no error and a
round log recording it as a legitimate round.

resolveBinding now takes the positionals too, via bindingArg, which
refuses a name given twice rather than letting one silently win.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

### Task 2: `relay status NAME` filters

**Files:**
- Modify: `cmd/relay/main.go:741-765` (`cmdStatus`)
- Test: `cmd/relay/main_test.go` (append)

**Interfaces:**
- Consumes: `bindingArg` from Task 1; `relay.Report` (field `Bindings []relay.BindingStatus`), `relay.BindingStatus` (field `Name string`), `relay.Status`, `relay.RenderStatus`.
- Produces: `func filterReport(rep relay.Report, name string) (relay.Report, error)` — returns `rep` unchanged when `name` is empty, a one-binding report when it matches, and an error naming the binding when it does not.

`filterReport` is a pure function precisely so it can be tested without a herdr socket.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/relay/main_test.go`:

```go
func TestFilterReportNarrowsToOneBinding(t *testing.T) {
	rep := relay.Report{Bindings: []relay.BindingStatus{
		{Name: "api"}, {Name: "frontend"}, {Name: "backend"},
	}}

	got, err := filterReport(rep, "frontend")
	if err != nil {
		t.Fatalf("filterReport: %v", err)
	}
	if len(got.Bindings) != 1 || got.Bindings[0].Name != "frontend" {
		t.Fatalf("got %+v, want just frontend", got.Bindings)
	}
}

func TestFilterReportKeepsEverythingWhenUnnamed(t *testing.T) {
	rep := relay.Report{Bindings: []relay.BindingStatus{
		{Name: "api"}, {Name: "frontend"},
	}}

	// A bare `relay status` lists every binding; that is its whole job.
	got, err := filterReport(rep, "")
	if err != nil {
		t.Fatalf("filterReport: %v", err)
	}
	if len(got.Bindings) != 2 {
		t.Fatalf("got %+v, want both bindings", got.Bindings)
	}
}

func TestFilterReportRejectsAnUnknownName(t *testing.T) {
	rep := relay.Report{Bindings: []relay.BindingStatus{{Name: "api"}}}

	// Silence here would look identical to "that binding is fine".
	if _, err := filterReport(rep, "nosuch"); err == nil {
		t.Fatal("an unknown binding name must be an error, not an empty report")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestFilterReport -v`
Expected: FAIL to compile — `undefined: filterReport`.

- [ ] **Step 3: Write `filterReport`**

In `cmd/relay/main.go`, immediately **before** `func cmdStatus`, insert:

```go
// filterReport narrows a status report to one binding. An empty name keeps
// every row, because listing them all is what a bare `relay status` is for.
//
// An unknown name is an error rather than an empty report: a silent blank
// would read exactly like a healthy binding with nothing outstanding.
func filterReport(rep relay.Report, name string) (relay.Report, error) {
	if name == "" {
		return rep, nil
	}
	for _, b := range rep.Bindings {
		if b.Name == name {
			return relay.Report{Bindings: []relay.BindingStatus{b}}, nil
		}
	}
	return relay.Report{}, fmt.Errorf("no binding named %q", name)
}
```

- [ ] **Step 4: Wire it into `cmdStatus`**

Replace `cmdStatus` in full:

```go
func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable output")
	name := fs.String("name", "", "show only this binding (default: all)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	target, err := bindingArg(*name, fs.Args())
	if err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	rep, err := relay.Status(context.Background(), rt)
	if err != nil {
		return err
	}

	rep, err = filterReport(rep, target)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	fmt.Print(relay.RenderStatus(rep))
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestFilterReport -v`
Expected: PASS, all three.

- [ ] **Step 6: Run the whole suite**

Run: `cd /home/fuad/projects/relay && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /home/fuad/projects/relay
git add cmd/relay/main.go cmd/relay/main_test.go
git commit -m "feat(relay): relay status NAME shows one binding

status discarded a positional name and printed every binding, which reads
as an answer to a question the caller did not ask. It now filters, by
positional or --name, and errors on a name it does not know rather than
printing an empty report that looks like good news.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

### Task 3: Documentation

**Files:**
- Modify: `README.md` — the `send`, `pull`, `diff` and `status` bullets, and the `--name` defaulting paragraph

**Interfaces:**
- Consumes: the behaviour shipped in Tasks 1 and 2.
- Produces: nothing code-facing.

Do **not** edit `docs/design.md`: it is a historical record, and its `relay pull [<name>]` form is made correct by this change.

- [ ] **Step 1: Update the four command bullets**

In `README.md`, change each of these bullets so the positional form appears first. Locate them by content; the surrounding prose of each bullet is unchanged apart from the opening signature:

- `- `relay send --file PATH [--name N]` — ` becomes `- `relay send [NAME|--name N] --file PATH` — `
- `- `relay pull [--name N]` — ` becomes `- `relay pull [NAME|--name N]` — `
- `- `relay diff [--name N] [--round R] [--stat]` — ` becomes `- `relay diff [NAME|--name N] [--round R] [--stat]` — `
- `- `relay status [--json]` — ` becomes `- `relay status [NAME|--name N] [--json]` — `

For the `status` bullet, append this sentence to its existing text: `Naming a binding shows only that one.`

- [ ] **Step 2: Update the `--name` defaulting paragraph**

Replace this paragraph:

```markdown
`--name` defaults to whichever binding owns the current working directory for
`send`, `pull`, `diff` and `status`. It is **required** for `answer`, `done` and
`unbind`: those act on a specific loop — `answer` types into a live dialog, the
other two end one — and they refuse to guess (see below).
```

with:

```markdown
Every binding-scoped command takes its binding either positionally or as
`--name`; naming it both ways at once is refused. `send`, `pull` and `diff`
fall back to whichever binding owns the current working directory, and a bare
`relay status` lists them all. Naming one is **required** for `answer`, `done`
and `unbind`: those act on a specific loop — `answer` types into a live dialog,
the other two end one — and they refuse to guess (see below).
```

- [ ] **Step 3: Verify the docs match the shipped behaviour**

Run: `cd /home/fuad/projects/relay && go run ./cmd/relay status -h && go run ./cmd/relay diff -h`
Expected: `status` lists `-json` and `-name`; `diff` lists `-name`, `-round`, `-stat`. Both match the README bullets.

- [ ] **Step 4: Commit**

```bash
cd /home/fuad/projects/relay
git add README.md
git commit -m "docs(relay): document positional binding names

Closes #50

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

## Notes for the reviewer

- **Why refuse when both spellings agree?** `bindingArg("webshop", ["webshop"])` errors rather than accepting. A caller who wrote the name twice has a mistaken model of the command, and quietly accepting one hides that until the day the two differ — which is the day it matters.
- **Why does `status` get no cwd fallback?** Listing every binding is already its useful default. Falling back to the cwd binding would make a bare `relay status` in a bound tree hide the other loops, which is a worse default than showing everything.
- **Why is an unknown name an error rather than an empty report?** An empty `relay status frontend` looks identical to a healthy binding with nothing outstanding. Erroring makes a typo obvious instead of reassuring.
- **`answer`, `done`, `unbind`, `fork` are untouched.** They use `explicitBinding`, which has no cwd fallback at all, and that refusal is deliberate — see #47.
