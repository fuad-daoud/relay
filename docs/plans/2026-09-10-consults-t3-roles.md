# Consults Task 3: role-aware alias table

> Executing **one task** from a larger plan. Do not implement any other task,
> and do not "helpfully" add the neighbouring ones -- another builder is
> working them concurrently in its own worktree and your edits would race.

**Full plan:** `docs/plans/2026-09-10-consults.md` (this is Task 3 of 8)
**Design spec:** `docs/specs/2026-09-10-consults-design.md`
**Issue:** #36

Both are in your worktree. Read the spec section a step cites when a step's
rationale is not obvious -- the plan tells you what, the spec tells you why.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan. A halt that surfaces a design error
is worth more than a green suite that worked around one.

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

### Task 3: Role-aware alias table

A role is not a suggestion. Asking a builder alias for one-shot findings would hand a persistent writer a prompt with no plan in it; binding a consult alias would install a reader as the thing that does the work.

**Files:**
- Modify: `internal/alias/alias.go:17-22` (`Spec`), `:33-70` (`DefaultTable` doc comment only)
- Modify: `internal/relay/bind.go:266-290` (`resolveBuilder`)
- Modify: `internal/relay/add.go` (via `Bind`, no direct change needed — verify)
- Test: `internal/alias/alias_test.go`, `internal/relay/bind_test.go`

**Interfaces:**
- Consumes: nothing from Tasks 1-2.
- Produces:
  - `alias.Spec.Role string` — `""`/`"builder"` or `"consult"`
  - `alias.Spec.Tree string` — `""`/`"binding"` or `"none"`
  - `func (s Spec) IsConsult() bool`
  - `relay.ErrConsultAlias` — `Bind`/`Add` refused a consult alias

- [ ] **Step 1: Write the failing tests**

Add to `internal/alias/alias_test.go`:

```go
func TestSpecDefaultsToBuilderRoleAndBindingTree(t *testing.T) {
	// The three shipped aliases predate roles. They must keep parsing as
	// builders with no edit to aliases.json.
	tbl := DefaultTable()
	for _, name := range tbl.Names() {
		spec, err := tbl.Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		if spec.IsConsult() {
			t.Errorf("%q reports as a consult; shipped aliases are builders", name)
		}
	}
}

func TestLoadTableReadsRoleAndTree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliases.json")
	body := `[{"name":"reviewer","kind":"claude","args":["--agent","reviewer"],"role":"consult","tree":"binding"}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write aliases: %v", err)
	}

	tbl, err := LoadTable(path)
	if err != nil {
		t.Fatalf("LoadTable: %v", err)
	}
	spec, err := tbl.Lookup("reviewer")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !spec.IsConsult() {
		t.Errorf("role = %q, want a consult", spec.Role)
	}
	if spec.Tree != "binding" {
		t.Errorf("tree = %q, want %q", spec.Tree, "binding")
	}
}
```

Add to `internal/relay/bind_test.go`:

```go
func TestBindRefusesAConsultAlias(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Aliases = consultTable(t)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Alias: "reviewer", PlannerPane: "w2:p3", CWD: "/repo",
	})

	if !errors.Is(err, ErrConsultAlias) {
		t.Fatalf("want ErrConsultAlias, got %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("started %d agents; a refused bind must spawn nothing", len(f.starts))
	}
}
```

And this helper, in `internal/relay/fake_test.go` (Task 4 and Task 6 both use it):

```go
// consultTable is a table with one consult role, `reviewer`, layered over the
// shipped builder aliases.
func consultTable(t *testing.T) *alias.Table {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aliases.json")
	body := `[{"name":"reviewer","kind":"claude","args":["--agent","reviewer"],"role":"consult","tree":"binding"}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write aliases: %v", err)
	}
	tbl, err := alias.LoadTable(path)
	if err != nil {
		t.Fatalf("LoadTable: %v", err)
	}
	return tbl
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/alias/ ./internal/relay/ -run 'TestSpecDefaults|TestLoadTableReadsRole|TestBindRefusesAConsultAlias' -v
```

Expected: FAIL to compile — `IsConsult`, `Spec.Tree`, `ErrConsultAlias` undefined.

- [ ] **Step 3: Extend `Spec`**

In `internal/alias/alias.go`, replace the `Spec` struct (`:17-22`):

```go
// Spec is how to start one agent. Preamble is prepended to the first prompt of
// a session for harnesses that cannot select a role at launch.
type Spec struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Args     []string `json:"args"`
	Preamble string   `json:"preamble,omitempty"`

	// Role is "" or "builder" for a persistent writer, or "consult" for an
	// ephemeral read-only one-shot. Empty defaults to builder, so every
	// aliases.json written before roles existed parses unchanged.
	Role string `json:"role,omitempty"`

	// Tree is "" or "binding" to run in the owning binding's working tree, or
	// "none" for a treeless role. "none" is forward-declared for a future
	// explorer-light and is REFUSED by `relay ask` today; a config field with an
	// explicit refusal is honest where a silently ignored one is not.
	Tree string `json:"tree,omitempty"`
}

// IsConsult reports whether this spec describes a consult rather than a
// builder. Callers must branch on this rather than comparing Role directly, so
// the empty-means-builder default lives in exactly one place.
func (s Spec) IsConsult() bool { return s.Role == "consult" }
```

- [ ] **Step 4: Refuse a consult alias at bind time**

In `internal/relay/bind.go`, add near the other package errors:

```go
// ErrConsultAlias reports a bind or add naming a consult role. A consult is
// ephemeral, read-only and one-shot; installing one as a binding's builder
// would put a reader where the work happens.
var ErrConsultAlias = errors.New("that alias is a consult role, not a builder")
```

In `resolveBuilder`, immediately after the `rt.Aliases.Lookup(opts.Alias)` block (`:287-290`):

```go
	if spec.IsConsult() {
		return store.Endpoint{}, fmt.Errorf(
			"%q is a consult role; ask it with `relay ask --role %s`: %w",
			opts.Alias, opts.Alias, ErrConsultAlias)
	}
```

This sits before `builderPane`, so a refused bind spawns nothing — the same discipline as the cwd check at `:213`. `Add` reaches this through `Bind`, so it needs no separate change; confirm with the test in Step 5.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
make check
```

Expected: PASS, including the untouched `alias_test.go:25,40,52` arg-list assertions.

Also confirm `Add` inherits the refusal:

```bash
go test ./internal/relay/ -run TestAdd -v
```

- [ ] **Step 6: Commit**

```bash
git add internal/alias/ internal/relay/bind.go internal/relay/bind_test.go internal/relay/fake_test.go
git commit -m "feat(alias): give a spec a role and a tree policy

Empty means builder and binding-tree, so the three shipped aliases and any
aliases.json written before roles existed parse unchanged.

Bind refuses a consult alias before it spawns anything: a consult is
ephemeral, read-only and one-shot, and installing one as a builder would put
a reader where the work happens."
```
