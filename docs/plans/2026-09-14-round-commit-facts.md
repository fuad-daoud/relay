# Round commit facts: round close records whether the builder committed (#130)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-14-round-commit-facts-design.md`. Section
numbers below (§) refer to it.
**Issue:** #130. Closes it.
**Depends on:** nothing open.

**Goal:** When a round closes, relay records how many commits the round
added and whether the worktree was clean or dirty, on the `diff` log entry
and on the `Diff:` line the planner receives; `relay status` says `dirty`
on a binding whose last close left uncommitted work; `add` and `fork`
persist the branch and base commit they already compute.

**Architecture:** One new `Git` method (`RevListCount`). `CaptureBaseline`
returns the HEAD commit next to the tree, and `Send` stores it as
`RoundBaselineHead`. A new `CommitFacts` beside `CaptureRoundDiff` runs
three git calls at close and never errors; `DiffSummary`/`DiffLine` take
its result and render one extra clause. `queueReport` wires the three
together and clears the head with the tree. `statusRow` derives
`LastClose`/`Dirty` from the newest diff entry. Nothing is refused, no
prompt text changes, and the nudge/fingerprint/scrape paths are untouched
(no `make e2e`).

**Tech stack:** Go 1.22. Verification is the `make check` constituent set
(runs `-race`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/commit-facts` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/commit-facts`, cut from `main`. |
| `~/.local/state/relay/commit-facts` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` directly, in this order, and say so in your
report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` yourself. **No test is added under `cmd/relay`** (CI
runners have no `herdr`; CLAUDE.md). `internal/git`'s tests run a real
`git` binary against `t.TempDir()` repositories through the existing
`runGit` helper; Task 1 adds one such test and nothing else touches a real
repository.

## Global constraints

- Every new git call at close lands in `CommitResult.Reason`; no new error
  is ever returned from `queueReport`, `Send`, `CaptureBaseline` or
  `CommitFacts` because of it (§6).
- `CommitResult.Known` is all-or-nothing: the first failing call stops the
  sequence `HeadCommit`, `RevListCount`, `Dirty` (§4.3).
- `LogEntry.Tree` is exactly `"clean"`, `"dirty"`, or `""`; `Commits` is
  meaningful only when `Tree != ""` (§3.2).
- Rendered clauses are exactly the strings in §3.5. `no changes` /
  `Diff: no file changes` gain nothing; unknown facts with an empty
  `Reason` render no clause.
- `on <branch>` appears in `DiffLine` only when the branch argument is
  non-empty.
- New `Binding` and `LogEntry` fields are `omitempty`; an unchanged
  binding or entry serialises byte-identically to before.
- The builder prompt (`builderPrompt`, `send.go:32`) is not edited.
- One commit per task, on the worktree's branch.

---

### Task 1: `RevListCount` in `internal/git`, the `Git` interface, and `fakeGit`

**Files:**
- Modify: `internal/git/client.go` (new method; place directly after `HeadCommit` at line ~268)
- Modify: `internal/git/client_test.go` (new test at the end)
- Modify: `internal/relay/herdr.go:33-41` (the `Git` interface)
- Modify: `internal/relay/fake_test.go:39-73` (struct) and after `Dirty` at line ~142 (method)

**Interfaces:**
- Produces: `func (c *Client) RevListCount(ctx context.Context, dir, from, to string) (int, error)` (§4.1), runs `git rev-list --count <from>..<to>` in `dir`. Errors: `ErrNotRepo`, `ErrGitUnavailable`, or a wrapped git failure (an unresolvable ref included).
- Produces: `RevListCount` on `relay.Git`; `fakeGit` fields `revListCount int`, `revListErr error`, `revListCalls int`, `lastRevListDir, lastRevListFrom, lastRevListTo string`.

- [ ] **Step 1: Write `TestRevListCount`** in `internal/git/client_test.go`, modelled on `TestHeadCommit_BranchExists_Dirty` (line 393):

```go
func TestRevListCount(t *testing.T) {
	ctx := context.Background()
	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "first")
	base, err := client.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}

	// same commit -> 0
	n, err := client.RevListCount(ctx, repo, base, base)
	if err != nil || n != 0 {
		t.Fatalf("RevListCount(base..base) = %d, %v; want 0, nil", n, err)
	}

	// two more commits -> 2
	for i, name := range []string{"b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", name)
		runGit(t, repo, "commit", "-m", fmt.Sprintf("commit %d", i+2))
	}
	head, err := client.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	n, err = client.RevListCount(ctx, repo, base, head)
	if err != nil || n != 2 {
		t.Fatalf("RevListCount(base..head) = %d, %v; want 2, nil", n, err)
	}

	// unresolvable ref -> error
	if _, err := client.RevListCount(ctx, repo, "0123456789abcdef0123456789abcdef01234567", head); err == nil {
		t.Fatal("RevListCount with an unresolvable ref returned nil error")
	}

	// not a repository -> ErrNotRepo
	if _, err := client.RevListCount(ctx, t.TempDir(), base, head); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("RevListCount outside a repo: err = %v, want ErrNotRepo", err)
	}
}
```

Add `fmt` to the test file's imports if it is not already there.

- [ ] **Step 2: Run to verify it fails** -- `go test -count=1 ./internal/git -run TestRevListCount`: compile error, `RevListCount` undefined.

- [ ] **Step 3: Implement** after `HeadCommit`:

```go
// RevListCount returns the number of commits reachable from to and not from
// from: `git rev-list --count from..to`. 0 when they are the same commit.
// Errors: ErrNotRepo, ErrGitUnavailable, or a wrapped git failure --
// including a ref that no longer resolves.
func (c *Client) RevListCount(ctx context.Context, dir, from, to string) (int, error) {
	out, err := c.run(ctx, dir, nil, "rev-list", "--count", from+".."+to)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count: parse %q: %w", strings.TrimSpace(string(out)), err)
	}
	return n, nil
}
```

Add `strconv` to `client.go`'s imports.

- [ ] **Step 4: Add to the interface and the fake.** In `internal/relay/herdr.go` add
`RevListCount(ctx context.Context, dir, from, to string) (int, error)` after
`HeadCommit` in the `Git` interface. In `fake_test.go` add to `fakeGit`:

```go
	revListCount    int
	revListErr      error
	revListCalls    int
	lastRevListDir  string
	lastRevListFrom string
	lastRevListTo   string
```

and after `Dirty`:

```go
func (f *fakeGit) RevListCount(ctx context.Context, dir, from, to string) (int, error) {
	f.revListCalls++
	f.lastRevListDir = dir
	f.lastRevListFrom = from
	f.lastRevListTo = to
	if f.revListErr != nil {
		return 0, f.revListErr
	}
	return f.revListCount, nil
}
```

`TestFakeSatisfiesGit` (line 144) already pins both implementations against the interface.

- [ ] **Step 5: Run** `go test -count=1 ./internal/git -run TestRevListCount && go build ./... && go vet ./internal/relay`. Expected: PASS, no build errors.

- [ ] **Step 6: Commit**

```bash
git add internal/git/client.go internal/git/client_test.go internal/relay/herdr.go internal/relay/fake_test.go
git commit -m "feat(git): RevListCount, commits in from..to (#130)"
```

---

### Task 2: store fields

**Files:**
- Modify: `internal/store/types.go` (`Binding`, after `RoundClosedTree` at line ~107; and after `ForkedAtRound` at line ~150 for `Branch`/`Base`)
- Modify: `internal/store/log.go:55-66` (`LogEntry`)
- Modify: `internal/store/types_test.go` (new test at the end)
- Modify: `internal/store/log_test.go` (new test at the end)

**Interfaces:**
- Produces on `store.Binding`: `Branch string` `json:"branch,omitempty"`, `Base string` `json:"base,omitempty"`, `RoundBaselineHead string` `json:"round_baseline_head,omitempty"` (§3.1).
- Produces on `store.LogEntry`: `Commits int` `json:"commits,omitempty"`, `Tree string` `json:"tree,omitempty"` (§3.2).

- [ ] **Step 1: Write the tests.** In `types_test.go`, modelled on `TestBindingRoundClosedTreeJSON` (line 10):

```go
func TestBindingCommitFactFieldsRoundTripAndAreOmittedWhenEmpty(t *testing.T) {
	t.Run("empty omits the keys", func(t *testing.T) {
		data, err := json.Marshal(Binding{})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		for _, key := range []string{"branch", "base", "round_baseline_head"} {
			if _, ok := decoded[key]; ok {
				t.Errorf("expected %s to be omitted when empty, got JSON: %s", key, data)
			}
		}
	})

	t.Run("set values round-trip", func(t *testing.T) {
		in := Binding{Branch: "relay/api-auth", Base: "c0ffee", RoundBaselineHead: "beef"}
		data, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var out Binding
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if out.Branch != in.Branch || out.Base != in.Base || out.RoundBaselineHead != in.RoundBaselineHead {
			t.Errorf("round trip: got %+v, want %+v", out, in)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if decoded["branch"] != "relay/api-auth" || decoded["base"] != "c0ffee" || decoded["round_baseline_head"] != "beef" {
			t.Errorf("JSON keys: %s", data)
		}
	})
}
```

In `log_test.go`:

```go
func TestLogEntryCommitFactsRoundTripAndAreOmittedWhenUnknown(t *testing.T) {
	data, err := json.Marshal(LogEntry{Kind: KindDiff})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"commits", "tree"} {
		if _, ok := decoded[key]; ok {
			t.Errorf("expected %s to be omitted when unknown, got JSON: %s", key, data)
		}
	}

	in := LogEntry{Kind: KindDiff, Commits: 3, Tree: "clean"}
	data, err = json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out LogEntry
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Commits != 3 || out.Tree != "clean" {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}
}
```

Add `encoding/json` to `log_test.go`'s imports if absent.

- [ ] **Step 2: Run to verify they fail** -- compile error, unknown fields.

- [ ] **Step 3: Add the fields.** In `Binding`, directly after `RoundClosedTree`:

```go
	// RoundBaselineHead is the commit HEAD pointed at when the CURRENT round
	// was sent, written by Send next to RoundBaselineTree and cleared with it
	// by queueReport. Empty means no commit count is possible for the round:
	// a non-git tree, an unborn HEAD, git unavailable, or a round sent before
	// the field existed (#130).
	RoundBaselineHead string `json:"round_baseline_head,omitempty"`
```

Directly after `ForkedAtRound`:

```go
	// Branch is the branch relay created for this binding's worktree
	// (relay/<name>), and Base the commit it was cut at. Written once by add
	// and fork; empty for a --cwd binding, an adopted bind, and every
	// bind.json written before the fields existed. Display and provenance
	// today; the branch-integration verbs key on them (#130).
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
```

In `LogEntry`, after `Note`:

```go
	// Commits and Tree are the round's commit facts, on diff entries only
	// (#130): commits added since the round's baseline HEAD, and whether the
	// worktree was "clean" or "dirty" at close. Tree == "" means the facts
	// are unknown and Commits is meaningless.
	Commits int    `json:"commits,omitempty"`
	Tree    string `json:"tree,omitempty"`
```

- [ ] **Step 4: Run** `go test -count=1 ./internal/store`. Expected: PASS, every existing store test unedited.

- [ ] **Step 5: Commit**

```bash
git add internal/store/types.go internal/store/log.go internal/store/types_test.go internal/store/log_test.go
git commit -m "feat(store): Branch, Base, RoundBaselineHead on Binding; Commits, Tree on diff entries (#130)"
```

---

### Task 3: `CaptureBaseline` returns the HEAD; `Send` stores it

**Files:**
- Modify: `internal/relay/capture.go:62-80` (`CaptureBaseline`)
- Modify: `internal/relay/send.go:66-70` and `:192-193`
- Modify: `internal/relay/capture_test.go:185-209` (`TestCaptureBaseline`)
- Modify: `internal/relay/send_test.go` (extend `TestSendCapturesBaselineWithFakeGit` at line 196; new test)

**Interfaces:**
- Modifies: `func CaptureBaseline(ctx context.Context, rt Runtime, b store.Binding) (tree, head string)` (§4.2). `head` is captured only when `tree != ""`; `""` on any `HeadCommit` error. Never errors.
- Produces: `Send` sets `b.RoundBaselineHead = head` next to `b.RoundBaselineTree = baseline`.

- [ ] **Step 1: Rewrite `TestCaptureBaseline`** to the two-value form:

```go
func TestCaptureBaseline(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	b := store.Binding{Name: "webshop", CWD: "/repo"}
	newRT := func(g Git) Runtime {
		return Runtime{Store: s, Git: g, LedgerPath: filepath.Join(t.TempDir(), "ledger.json"), HistoryPath: filepath.Join(t.TempDir(), "history.json")}
	}

	tree, head := CaptureBaseline(ctx, newRT(&fakeGit{snapshotTreeID: "tree-base", headCommitID: "head-base"}), b)
	if tree != "tree-base" || head != "head-base" {
		t.Fatalf("got (%q, %q), want (tree-base, head-base)", tree, head)
	}

	if tree, head := CaptureBaseline(ctx, newRT(nil), b); tree != "" || head != "" {
		t.Fatalf("nil git: got (%q, %q), want both empty", tree, head)
	}

	fgSnap := &fakeGit{snapshotTreeErr: errors.New("fail"), headCommitID: "head-base"}
	if tree, head := CaptureBaseline(ctx, newRT(fgSnap), b); tree != "" || head != "" {
		t.Fatalf("snapshot failure: got (%q, %q), want both empty", tree, head)
	}
	if fgSnap.headCalls != 0 {
		t.Fatalf("HeadCommit called %d times after a failed snapshot, want 0", fgSnap.headCalls)
	}

	if tree, head := CaptureBaseline(ctx, newRT(&fakeGit{snapshotTreeID: "tree-base", headCommitErr: errors.New("unborn")}), b); tree != "tree-base" || head != "" {
		t.Fatalf("head failure: got (%q, %q), want (tree-base, \"\")", tree, head)
	}
}
```

- [ ] **Step 2: Extend `TestSendCapturesBaselineWithFakeGit`**: set `fg := &fakeGit{snapshotTreeID: "tree-abc123", headCommitID: "head-abc123"}` and after the `RoundBaselineTree` assertion add:

```go
	if b.RoundBaselineHead != "head-abc123" {
		t.Errorf("RoundBaselineHead = %q, want head-abc123", b.RoundBaselineHead)
	}
```

Add a new test directly after it:

```go
func TestSendHeadFailureLeavesTreeAndClearsHead(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	rt.Git = &fakeGit{snapshotTreeID: "tree-abc123", headCommitErr: errors.New("unborn HEAD")}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# test plan")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree != "tree-abc123" || b.RoundBaselineHead != "" {
		t.Errorf("baseline = (%q, %q), want (tree-abc123, \"\")", b.RoundBaselineTree, b.RoundBaselineHead)
	}
}
```

- [ ] **Step 3: Run to verify they fail** -- compile error (`CaptureBaseline` returns one value; `RoundBaselineHead` unset).

- [ ] **Step 4: Implement.** `CaptureBaseline` per §5:

```go
func CaptureBaseline(ctx context.Context, rt Runtime, b store.Binding) (tree, head string) {
	if rt.Git == nil || b.CWD == "" {
		return "", ""
	}
	tree, err := rt.Git.SnapshotTree(ctx, b.CWD)
	if err != nil {
		return "", ""
	}
	head, err = rt.Git.HeadCommit(ctx, b.CWD)
	if err != nil {
		return tree, ""
	}
	return tree, head
}
```

Update its doc comment: postconditions gain "`head` is HEAD's commit id when the snapshot succeeded and `HeadCommit` did; `""` otherwise. A tree without a head is normal (unborn HEAD) and the round then reports `commits unknown (no baseline)`."

In `send.go`: line ~66 `var baseline string` becomes `var baseline, baselineHead string`; line ~70 becomes `baseline, baselineHead = CaptureBaseline(ctx, rt, hint)`; after line ~192 `b.RoundBaselineTree = baseline` add `b.RoundBaselineHead = baselineHead`.

- [ ] **Step 5: Run** `go test -count=1 ./internal/relay -run 'TestCaptureBaseline|TestSend'`. Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/capture.go internal/relay/send.go internal/relay/capture_test.go internal/relay/send_test.go
git commit -m "feat(relay): send records the round's baseline HEAD next to its tree (#130)"
```

---

### Task 4: `CommitFacts`

**Files:**
- Modify: `internal/relay/capture.go` (new type and function; place directly after `CaptureRoundDiff`, before `DiffLine`)
- Modify: `internal/relay/capture_test.go` (new test at the end)

**Interfaces:**
- Produces (§3.3, §4.3):

```go
type CommitResult struct {
	Known   bool
	Commits int
	Dirty   bool
	Reason  string
}
func CommitFacts(ctx context.Context, rt Runtime, b store.Binding) CommitResult
```

- [ ] **Step 1: Write `TestCommitFacts`**, one subtest per §4.3 row:

```go
func TestCommitFacts(t *testing.T) {
	ctx := context.Background()
	s := store.New(t.TempDir())
	newRT := func(g Git) Runtime {
		return Runtime{Store: s, Git: g, LedgerPath: filepath.Join(t.TempDir(), "ledger.json"), HistoryPath: filepath.Join(t.TempDir(), "history.json")}
	}
	withHead := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, RoundBaselineHead: "head-start"}

	t.Run("nil git", func(t *testing.T) {
		got := CommitFacts(ctx, newRT(nil), withHead)
		if got.Known || got.Reason != "" {
			t.Fatalf("got %+v, want Known=false, Reason empty", got)
		}
	})

	t.Run("no baseline head", func(t *testing.T) {
		fg := &fakeGit{headCommitID: "h"}
		got := CommitFacts(ctx, newRT(fg), store.Binding{Name: "webshop", CWD: "/repo", Round: 1})
		if got.Known || got.Reason != "no baseline" {
			t.Fatalf("got %+v, want Reason \"no baseline\"", got)
		}
		if fg.headCalls != 0 || fg.revListCalls != 0 || fg.dirtyCalls != 0 {
			t.Fatalf("git was called without a baseline: %+v", fg)
		}
	})

	t.Run("head fails", func(t *testing.T) {
		fg := &fakeGit{headCommitErr: errors.New("boom: head")}
		got := CommitFacts(ctx, newRT(fg), withHead)
		if got.Known || got.Reason != "head: boom: head" {
			t.Fatalf("got %+v, want Reason \"head: boom: head\"", got)
		}
		if fg.revListCalls != 0 || fg.dirtyCalls != 0 {
			t.Fatalf("sequence did not stop at the first failure: %+v", fg)
		}
	})

	t.Run("rev-list fails", func(t *testing.T) {
		fg := &fakeGit{headCommitID: "head-end", revListErr: errors.New("boom: rev-list")}
		got := CommitFacts(ctx, newRT(fg), withHead)
		if got.Known || got.Reason != "rev-list: boom: rev-list" {
			t.Fatalf("got %+v, want Reason \"rev-list: boom: rev-list\"", got)
		}
		if fg.dirtyCalls != 0 {
			t.Fatalf("Dirty called after rev-list failed: %+v", fg)
		}
	})

	t.Run("dirty check fails", func(t *testing.T) {
		fg := &fakeGit{headCommitID: "head-end", revListCount: 2, dirtyErr: errors.New("boom: status")}
		got := CommitFacts(ctx, newRT(fg), withHead)
		if got.Known || got.Reason != "dirty check: boom: status" {
			t.Fatalf("got %+v, want Reason \"dirty check: boom: status\"", got)
		}
	})

	t.Run("not a repository is silent", func(t *testing.T) {
		fg := &fakeGit{headCommitErr: fmt.Errorf("%w: nope", git.ErrNotRepo)}
		got := CommitFacts(ctx, newRT(fg), withHead)
		if got.Known || got.Reason != "" {
			t.Fatalf("got %+v, want Known=false with empty Reason", got)
		}
	})

	t.Run("all succeed", func(t *testing.T) {
		fg := &fakeGit{headCommitID: "head-end", revListCount: 3, dirtyResult: true}
		got := CommitFacts(ctx, newRT(fg), withHead)
		want := CommitResult{Known: true, Commits: 3, Dirty: true}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
		if fg.lastRevListDir != "/repo" || fg.lastRevListFrom != "head-start" || fg.lastRevListTo != "head-end" {
			t.Fatalf("rev-list range: dir=%q from=%q to=%q", fg.lastRevListDir, fg.lastRevListFrom, fg.lastRevListTo)
		}
		if fg.lastDirtyDir != "/repo" {
			t.Fatalf("Dirty dir = %q, want /repo", fg.lastDirtyDir)
		}
	})
}
```

Add `fmt` and `github.com/fuad-daoud/relay/internal/git` to the test file's imports if absent.

- [ ] **Step 2: Run to verify it fails** -- compile error, undefined.

- [ ] **Step 3: Implement** per §5:

```go
// CommitResult is what one round-end commit-facts capture produced (#130).
// Known is all-or-nothing: either both facts were captured or neither was,
// and Reason names the step that failed ("" for a non-repository, which is
// not worth a sentence).
type CommitResult struct {
	Known   bool   // both facts were captured
	Commits int    // rev-list --count RoundBaselineHead..HEAD; 0 when Known is false
	Dirty   bool   // uncommitted or untracked changes; false when Known is false
	Reason  string // why Known is false; "" when it is true
}

// CommitFacts captures how many commits b's current round added and whether
// its tree is dirty, for the round that is closing. It NEVER returns an
// error: the facts are informational and a round advance must not be blocked
// by them. Runs under the state lock, like CaptureRoundDiff.
//
// Preconditions:  none.
// Postconditions: Known is false with an empty Reason when rt.Git is nil or
//
//	the tree is not a repository; false with a Reason naming the
//	step when RoundBaselineHead is empty or a git call failed; true
//	with both facts otherwise. The sequence HeadCommit, RevListCount,
//	Dirty stops at the first failure.
func CommitFacts(ctx context.Context, rt Runtime, b store.Binding) CommitResult {
	if rt.Git == nil {
		return CommitResult{}
	}
	if b.RoundBaselineHead == "" {
		return CommitResult{Reason: "no baseline"}
	}
	head, err := rt.Git.HeadCommit(ctx, b.CWD)
	if errors.Is(err, git.ErrNotRepo) {
		return CommitResult{}
	}
	if err != nil {
		return CommitResult{Reason: "head: " + brief(err)}
	}
	n, err := rt.Git.RevListCount(ctx, b.CWD, b.RoundBaselineHead, head)
	if errors.Is(err, git.ErrNotRepo) {
		return CommitResult{}
	}
	if err != nil {
		return CommitResult{Reason: "rev-list: " + brief(err)}
	}
	dirty, err := rt.Git.Dirty(ctx, b.CWD)
	if errors.Is(err, git.ErrNotRepo) {
		return CommitResult{}
	}
	if err != nil {
		return CommitResult{Reason: "dirty check: " + brief(err)}
	}
	return CommitResult{Known: true, Commits: n, Dirty: dirty}
}
```

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay -run TestCommitFacts`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/capture.go internal/relay/capture_test.go
git commit -m "feat(relay): CommitFacts, commit count and tree state at round close (#130)"
```

---

### Task 5: `DiffSummary` and `DiffLine` render the clause

**Files:**
- Modify: `internal/relay/capture.go` (`DiffSummary` at line ~49, `DiffLine` at line ~150)
- Modify: `internal/relay/capture_test.go` (six existing `DiffLine(res)` calls at lines 24, 40, 60, 86, 118, 159 become `DiffLine(res, CommitResult{}, "")`; new table test at the end)

**Interfaces:**
- Modifies (§4.4):

```go
func DiffSummary(res DiffResult, facts CommitResult) string
func DiffLine(res DiffResult, facts CommitResult, branch string) string
```

- Produces: `commitClause(facts CommitResult, branch string, forLine bool) string` -- unexported helper; `""` when nothing should be appended.

- [ ] **Step 1: Update the six existing calls** to `DiffLine(res, CommitResult{}, "")`. Their expected strings do not change: unknown facts with an empty `Reason` render no clause.

- [ ] **Step 2: Write `TestDiffTextWithCommitFacts`**:

```go
func TestDiffTextWithCommitFacts(t *testing.T) {
	normal := DiffResult{Available: true, Path: "/p/007-diff.patch", Stat: git.Stat{FilesChanged: 6, Insertions: 120, Deletions: 30}}
	empty := DiffResult{Available: true}
	truncated := DiffResult{Available: true, Truncated: true, Stat: git.Stat{FilesChanged: 312, Insertions: 48120, Deletions: 9033}}
	unavailable := DiffResult{Available: false, Reason: "no baseline"}
	silent := DiffResult{Available: false}

	cases := []struct {
		name        string
		res         DiffResult
		facts       CommitResult
		branch      string
		wantSummary string
		wantLine    string
	}{
		{"commits clean with branch", normal, CommitResult{Known: true, Commits: 3}, "relay/api-auth",
			"6 files, +120 -30; 3 commits, clean",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- 3 commits on relay/api-auth, tree clean"},
		{"one commit singular", normal, CommitResult{Known: true, Commits: 1}, "relay/api-auth",
			"6 files, +120 -30; 1 commit, clean",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- 1 commit on relay/api-auth, tree clean"},
		{"commits dirty with branch", normal, CommitResult{Known: true, Commits: 3, Dirty: true}, "relay/api-auth",
			"6 files, +120 -30; 3 commits, dirty",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- 3 commits on relay/api-auth, tree dirty"},
		{"commits clean without branch", normal, CommitResult{Known: true, Commits: 3}, "",
			"6 files, +120 -30; 3 commits, clean",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- 3 commits, tree clean"},
		{"no commits dirty", normal, CommitResult{Known: true, Commits: 0, Dirty: true}, "relay/api-auth",
			"6 files, +120 -30; no commits, dirty",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- no commits; changes are uncommitted in the worktree"},
		{"no commits clean", normal, CommitResult{Known: true}, "relay/api-auth",
			"6 files, +120 -30; no commits, clean",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- no commits, tree clean"},
		{"unknown with reason", normal, CommitResult{Reason: "rev-list: boom"}, "relay/api-auth",
			"6 files, +120 -30; commits unknown (rev-list: boom)",
			"Diff: /p/007-diff.patch (6 files, +120 -30) -- commits unknown (rev-list: boom)"},
		{"unknown silent", normal, CommitResult{}, "relay/api-auth",
			"6 files, +120 -30",
			"Diff: /p/007-diff.patch (6 files, +120 -30)"},
		{"empty diff gains nothing", empty, CommitResult{Known: true, Commits: 2}, "relay/api-auth",
			"no changes",
			"Diff: no file changes"},
		{"truncated keeps the clause", truncated, CommitResult{Known: true, Dirty: true}, "",
			"truncated; no commits, dirty",
			"Diff: 312 files, +48120 -9033 (patch omitted, over the 4 MiB cap) -- no commits; changes are uncommitted in the worktree"},
		{"unavailable keeps the clause", unavailable, CommitResult{Known: true, Commits: 2}, "relay/api-auth",
			"unavailable: no baseline; 2 commits, clean",
			"Diff: unavailable (no baseline) -- 2 commits on relay/api-auth, tree clean"},
		{"unavailable and unknown", unavailable, CommitResult{Reason: "no baseline"}, "",
			"unavailable: no baseline; commits unknown (no baseline)",
			"Diff: unavailable (no baseline) -- commits unknown (no baseline)"},
		{"silent diff stays silent", silent, CommitResult{Known: true, Commits: 2}, "relay/api-auth",
			"unavailable; 2 commits, clean",
			""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DiffSummary(tc.res, tc.facts); got != tc.wantSummary {
				t.Errorf("DiffSummary = %q, want %q", got, tc.wantSummary)
			}
			if got := DiffLine(tc.res, tc.facts, tc.branch); got != tc.wantLine {
				t.Errorf("DiffLine = %q, want %q", got, tc.wantLine)
			}
		})
	}
}
```

Note the last case: `DiffSummary` for a silent diff is today `"unavailable"`, and the log note keeps the facts clause because the log is the record; `DiffLine` stays `""` because the line's own rule is "nothing to tell the planner" (§3.5: "The empty line ... stays empty").

- [ ] **Step 3: Run to verify it fails** -- compile error (argument counts).

- [ ] **Step 4: Implement.** Add the helper and rewrite the two functions:

```go
// commitClause renders the commit facts for a diff note (forLine false) or
// the planner's Diff: line (forLine true), or "" when there is nothing to
// say: unknown facts with no reason.
func commitClause(facts CommitResult, branch string, forLine bool) string {
	if !facts.Known {
		if facts.Reason == "" {
			return ""
		}
		return "commits unknown (" + facts.Reason + ")"
	}
	tree := "clean"
	if facts.Dirty {
		tree = "dirty"
	}
	if !forLine {
		if facts.Commits == 0 {
			return "no commits, " + tree
		}
		return fmt.Sprintf("%s, %s", formatCommits(facts.Commits), tree)
	}
	if facts.Commits == 0 {
		if facts.Dirty {
			return "no commits; changes are uncommitted in the worktree"
		}
		return "no commits, tree clean"
	}
	on := ""
	if branch != "" {
		on = " on " + branch
	}
	return fmt.Sprintf("%s%s, tree %s", formatCommits(facts.Commits), on, tree)
}

func formatCommits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}

// DiffSummary returns the human summary for the diff log entry, with the
// round's commit facts appended after "; " unless the diff is empty.
func DiffSummary(res DiffResult, facts CommitResult) string {
	var base string
	switch {
	case !res.Available && res.Reason != "":
		base = fmt.Sprintf("unavailable: %s", res.Reason)
	case !res.Available:
		base = "unavailable"
	case res.Stat.Empty():
		return "no changes"
	case res.Truncated:
		base = "truncated"
	default:
		base = fmt.Sprintf("%s, +%d -%d", formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
	}
	if clause := commitClause(facts, "", false); clause != "" {
		return base + "; " + clause
	}
	return base
}

// DiffLine renders the report-payload line for a result, or "" when the result
// says nothing worth telling the planner (rt.Git off, or not a repository).
// The commit facts follow " -- " unless the diff is empty; branch names the
// binding's branch in the clause, or is "" for a tree relay did not create.
func DiffLine(res DiffResult, facts CommitResult, branch string) string {
	var base string
	switch {
	case !res.Available && res.Reason == "":
		return ""
	case !res.Available:
		base = fmt.Sprintf("Diff: unavailable (%s)", res.Reason)
	case res.Stat.Empty():
		return "Diff: no file changes"
	case res.Truncated:
		base = fmt.Sprintf("Diff: %s, +%d -%d (patch omitted, over the 4 MiB cap)",
			formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
	default:
		base = fmt.Sprintf("Diff: %s (%s, +%d -%d)",
			res.Path, formatFiles(res.Stat.FilesChanged), res.Stat.Insertions, res.Stat.Deletions)
	}
	if clause := commitClause(facts, branch, true); clause != "" {
		return base + " -- " + clause
	}
	return base
}
```

So that the package compiles before Task 6, change the two calls in `queueReport` (`reconcile.go:554` and `:560`) to `DiffSummary(result, CommitResult{})` and `DiffLine(result, CommitResult{}, "")`. Task 6 replaces both with the real arguments.

- [ ] **Step 5: Run** `go test -count=1 ./internal/relay -run 'TestDiffText|TestCaptureRoundDiff'`. Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/capture.go internal/relay/capture_test.go internal/relay/reconcile.go
git commit -m "feat(relay): diff note and Diff: line carry the round's commit facts (#130)"
```

---

### Task 6: `queueReport` records the facts and clears the head

**Files:**
- Modify: `internal/relay/reconcile.go:543-591` (`queueReport`)
- Modify: `internal/relay/reconcile_test.go` (new test after `TestReconcileDiffCapture`, line ~810)

**Interfaces:**
- Consumes: `CommitFacts`, `DiffSummary(res, facts)`, `DiffLine(res, facts, branch)`, `store.LogEntry.Commits/Tree`, `store.Binding.Branch/RoundBaselineHead`.

- [ ] **Step 1: Write `TestQueueReportRecordsCommitFacts`**, three subtests. Each is `TestReconcileDiffCapture` (line 688) up to the first tick, with `b.RoundBaselineHead = "head-start"` and `b.Branch = "relay/webshop"` set next to `b.RoundBaselineTree = "tree-start"` before the `Save`, and `fg` extended per subtest:

```go
func TestQueueReportRecordsCommitFacts(t *testing.T) {
	closeRound := func(t *testing.T, fg *fakeGit, head string) (store.Binding, []store.LogEntry) {
		t.Helper()
		f := &fakeHerdr{}
		rt, b := sentBinding(t, f)
		rt.Git = fg
		b.RoundBaselineTree = "tree-start"
		b.RoundBaselineHead = head
		b.Branch = "relay/webshop"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("report content"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("webshop", 1))
		agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
		got, err := reconcile(t, rt, b, agents)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatal(err)
		}
		return got, entries
	}
	diffAndReport := func(t *testing.T, entries []store.LogEntry) (store.LogEntry, store.LogEntry) {
		t.Helper()
		var diff, report store.LogEntry
		for _, e := range entries {
			if e.Round != 1 {
				continue
			}
			switch e.Kind {
			case store.KindDiff:
				diff = e
			case store.KindReport:
				report = e
			}
		}
		if diff.Kind == "" || report.Kind == "" {
			t.Fatalf("missing diff or report entry in %+v", entries)
		}
		return diff, report
	}
	changed := git.Diff{Stat: git.Stat{FilesChanged: 2, Insertions: 10, Deletions: 3}, Patch: []byte("diff content")}

	t.Run("commits and clean", func(t *testing.T) {
		fg := &fakeGit{snapshotTreeID: "tree-end", diffResult: changed, headCommitID: "head-end", revListCount: 3}
		got, entries := closeRound(t, fg, "head-start")
		diff, report := diffAndReport(t, entries)
		if diff.Commits != 3 || diff.Tree != "clean" {
			t.Errorf("diff entry facts = (%d, %q), want (3, clean)", diff.Commits, diff.Tree)
		}
		if diff.Note != "2 files, +10 -3; 3 commits, clean" {
			t.Errorf("diff note = %q", diff.Note)
		}
		if !strings.Contains(report.Payload, " -- 3 commits on relay/webshop, tree clean") {
			t.Errorf("payload %q lacks the commit clause", report.Payload)
		}
		if got.RoundBaselineHead != "" || got.RoundBaselineTree != "" {
			t.Errorf("baseline not cleared: head=%q tree=%q", got.RoundBaselineHead, got.RoundBaselineTree)
		}
		if fg.lastRevListFrom != "head-start" || fg.lastRevListTo != "head-end" {
			t.Errorf("rev-list range %q..%q", fg.lastRevListFrom, fg.lastRevListTo)
		}
	})

	t.Run("none and dirty", func(t *testing.T) {
		fg := &fakeGit{snapshotTreeID: "tree-end", diffResult: changed, headCommitID: "head-start", dirtyResult: true}
		_, entries := closeRound(t, fg, "head-start")
		diff, report := diffAndReport(t, entries)
		if diff.Commits != 0 || diff.Tree != "dirty" {
			t.Errorf("diff entry facts = (%d, %q), want (0, dirty)", diff.Commits, diff.Tree)
		}
		if !strings.Contains(report.Payload, " -- no commits; changes are uncommitted in the worktree") {
			t.Errorf("payload %q lacks the dirty clause", report.Payload)
		}
	})

	t.Run("no baseline head", func(t *testing.T) {
		fg := &fakeGit{snapshotTreeID: "tree-end", diffResult: changed, headCommitID: "head-end", revListCount: 3}
		_, entries := closeRound(t, fg, "")
		diff, report := diffAndReport(t, entries)
		if diff.Tree != "" || diff.Commits != 0 {
			t.Errorf("diff entry facts = (%d, %q), want unknown", diff.Commits, diff.Tree)
		}
		if !strings.Contains(diff.Note, "; commits unknown (no baseline)") {
			t.Errorf("diff note = %q", diff.Note)
		}
		if !strings.Contains(report.Payload, " -- commits unknown (no baseline)") {
			t.Errorf("payload %q", report.Payload)
		}
		if fg.revListCalls != 0 {
			t.Errorf("rev-list called without a baseline head")
		}
	})
}
```

- [ ] **Step 2: Run to verify it fails** -- the facts are absent (or, if Task 5 left temporary arguments, `diff.Tree == ""` and the clause is missing).

- [ ] **Step 3: Implement** in `queueReport`, replacing the diff block's body:

```go
	if !HasEntry(entries, b.Round, store.DirToPlanner, store.KindDiff) {
		result := CaptureRoundDiff(ctx, rt, b)
		facts := CommitFacts(ctx, rt, b)
		closed = result.EndTree
		diffEntry := store.LogEntry{
			TS:        rt.Now().UTC(),
			Round:     b.Round,
			Direction: store.DirToPlanner,
			Kind:      store.KindDiff,
			Path:      result.Path,
			Note:      DiffSummary(result, facts),
			Confirmed: true,
		}
		if facts.Known {
			diffEntry.Commits = facts.Commits
			diffEntry.Tree = "clean"
			if facts.Dirty {
				diffEntry.Tree = "dirty"
			}
		}
		if err := tx.AppendLog(b.Name, diffEntry); err != nil {
			return b, err
		}
		if line := DiffLine(result, facts, b.Branch); line != "" {
			payload = payload + "\n" + line
		}
	}
```

and after `b.RoundBaselineTree = ""` add `b.RoundBaselineHead = ""`.

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay`. Expected: PASS, every existing reconcile test unedited (`TestReconcileDiffCapture`'s `wantLine` uses `strings.Contains`, and with `sentBinding`'s nil-git `Send` the head is empty there, so its clause is `-- commits unknown (no baseline)` after the line it checks -- still contained).

- [ ] **Step 5: Mutation checks.** (a) Remove the `rt.Git.Dirty` call from
`CommitFacts` (return `dirty := false`). Run `go test -count=1 ./internal/relay -run 'TestQueueReportRecordsCommitFacts/none_and_dirty'`. Expected: FAIL. Restore.
(b) Remove `b.RoundBaselineHead = ""` from `queueReport`. Run
`go test -count=1 ./internal/relay -run 'TestQueueReportRecordsCommitFacts/commits_and_clean'`. Expected: FAIL on "baseline not cleared". Restore; both PASS. Say so in your report.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/reconcile.go internal/relay/reconcile_test.go
git commit -m "feat(relay): round close records commit count and tree state on the diff entry and the payload (#130)"
```

---

### Task 7: `Add` and `Fork` persist `Branch` and `Base`

**Files:**
- Modify: `internal/relay/add.go:182-191` (the `store.Binding` literal)
- Modify: `internal/relay/fork.go:224-236` (the `store.Binding` literal)
- Modify: `internal/relay/add_test.go` (extend `TestAddCreatesAWorktreeBindingAtRoundOne` at line 24 and `TestAddBindsAPreparedDirectoryWithCWD` at line 181)
- Modify: `internal/relay/fork_test.go` (extend `TestForkSuccess` at line 107 and `TestForkWithCustomCWD` at line 512)

- [ ] **Step 1: Extend the four tests.** In `TestAddCreatesAWorktreeBindingAtRoundOne`, after the `got.Base` assertion:

```go
	stored, err := rt.Store.Load("frontend")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.Branch != "relay/frontend" || stored.Base != "commit-head-123" {
		t.Errorf("stored branch/base = (%q, %q), want (relay/frontend, commit-head-123)", stored.Branch, stored.Base)
	}
```

In `TestAddBindsAPreparedDirectoryWithCWD`, at the end:

```go
	if got.Binding.Branch != "" || got.Binding.Base != "" {
		t.Errorf("--cwd created no branch, so none may be recorded: (%q, %q)", got.Binding.Branch, got.Binding.Base)
	}
```

In `TestForkSuccess`, after the `Fork` call succeeds (find where `res.Branch` or `res.Base` is asserted; add next to it, or at the end of the test if neither is):

```go
	storedFork, err := rt.Store.Load("alt")
	if err != nil {
		t.Fatalf("Load alt: %v", err)
	}
	if storedFork.Branch != "relay/alt" || storedFork.Base != "commit-head-123" {
		t.Errorf("stored branch/base = (%q, %q), want (relay/alt, commit-head-123)", storedFork.Branch, storedFork.Base)
	}
```

In `TestForkWithCustomCWD`, at the end:

```go
	if res.Binding.Branch != "" || res.Binding.Base != "" {
		t.Errorf("--cwd created no branch, so none may be recorded: (%q, %q)", res.Binding.Branch, res.Binding.Base)
	}
```

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1 ./internal/relay -run 'TestAddCreatesAWorktreeBindingAtRoundOne|TestForkSuccess'`: FAIL, stored branch empty.

- [ ] **Step 3: Implement.** Add `Branch: branch, Base: base,` to both `store.Binding` literals, directly after `Worktree: worktree,`. The `branch`/`base` locals are already in scope in both functions and both are `""` on the `--cwd` path.

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay -run 'TestAdd|TestFork'`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/add.go internal/relay/fork.go internal/relay/add_test.go internal/relay/fork_test.go
git commit -m "feat(relay): add and fork persist the branch and base they cut (#130)"
```

---

### Task 8: `relay status` shows `dirty`

**Files:**
- Modify: `internal/relay/status.go` (`BindingStatus` at line ~22-80: two fields and one type; `statusRow` after the `LastPayload` walk at line ~256; `RenderStatus` header line at line ~429-436)
- Modify: `internal/relay/status_test.go` (new tests at the end)

**Interfaces:**
- Produces (§3.4):

```go
type CloseInfo struct {
	Round   int    `json:"round"`
	Commits int    `json:"commits"`
	Tree    string `json:"tree"`
}
// on BindingStatus:
LastClose *CloseInfo `json:"last_close,omitempty"`
Dirty     bool       `json:"dirty"`
```

`Dirty = LastClose != nil && LastClose.Tree == "dirty" && b.RoundStartedAt.IsZero()`.

- [ ] **Step 1: Write the tests.**

```go
// seedClosedRound appends a diff entry for round 1 with the given facts and
// returns the binding as the daemon leaves it after queueReport: round 2,
// not yet sent (RoundStartedAt zero).
func seedClosedRound(t *testing.T, tree string, commits int) (Runtime, store.Binding) {
	t.Helper()
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: 1, Direction: store.DirToPlanner, Kind: store.KindDiff, Confirmed: true,
		Commits: commits, Tree: tree,
	}); err != nil {
		t.Fatalf("AppendLog diff: %v", err)
	}
	b.Round = 2
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return rt, b
}

func TestStatusLastCloseAndDirty(t *testing.T) {
	t.Run("dirty close, next round not sent", func(t *testing.T) {
		rt, _ := seedClosedRound(t, "dirty", 0)
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Round != 1 || got.LastClose.Tree != "dirty" || got.LastClose.Commits != 0 {
			t.Fatalf("LastClose = %+v, want round 1, dirty, 0 commits", got.LastClose)
		}
		if !got.Dirty {
			t.Error("Dirty = false, want true")
		}
	})

	t.Run("clean close", func(t *testing.T) {
		rt, _ := seedClosedRound(t, "clean", 3)
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Tree != "clean" || got.LastClose.Commits != 3 {
			t.Fatalf("LastClose = %+v, want clean, 3 commits", got.LastClose)
		}
		if got.Dirty {
			t.Error("Dirty = true, want false")
		}
	})

	t.Run("dirty close but the next round is sent", func(t *testing.T) {
		rt, b := seedClosedRound(t, "dirty", 0)
		b.RoundStartedAt = time.Now().UTC()
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Tree != "dirty" {
			t.Fatalf("LastClose = %+v, want dirty", got.LastClose)
		}
		if got.Dirty {
			t.Error("Dirty = true while a round is running, want false")
		}
	})

	t.Run("pre-field diff entry", func(t *testing.T) {
		rt, _ := seedClosedRound(t, "", 0)
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Tree != "" {
			t.Fatalf("LastClose = %+v, want present with unknown tree", got.LastClose)
		}
		if got.Dirty {
			t.Error("Dirty = true for an unknown tree, want false")
		}
	})

	t.Run("no diff entry", func(t *testing.T) {
		f := &fakeHerdr{}
		rt, _ := sentBinding(t, f)
		f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got := rep.Bindings[0]; got.LastClose != nil || got.Dirty {
			t.Errorf("LastClose = %+v, Dirty = %v; want nil, false", got.LastClose, got.Dirty)
		}
	})
}

func TestRenderStatusShowsDirty(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Display: "ACTIVE", Round: 2, Dirty: true, Consults: 1,
	}}}
	out := RenderStatus(r)
	if !strings.Contains(out, "round 2   ACTIVE dirty +1c") {
		t.Errorf("RenderStatus output missing 'dirty' after the display word:\n%s", out)
	}
}

func TestRenderStatusOmitsDirtyWhenClean(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Display: "ACTIVE", Round: 2,
		LastClose: &CloseInfo{Round: 1, Tree: "dirty"}, // the raw fact, without the rule applied
	}}}
	if out := RenderStatus(r); strings.Contains(out, "dirty") {
		t.Errorf("rendered dirty from LastClose instead of Dirty:\n%s", out)
	}
}
```

Add `time` to the test file's imports if absent. The `round 2   ACTIVE dirty +1c` string follows the existing `%-3d` on the round: `round 2  ` then a space then the display word. Verify against the existing header format `"%-8s %-40s %-4s round %-3d %s"` (line ~430) and adjust the expected spacing only if that format differs from what is written here.

- [ ] **Step 2: Run to verify they fail** -- compile error, unknown fields.

- [ ] **Step 3: Implement.** In `status.go`, after the `LastEvent` type (or before `BindingStatus`):

```go
// CloseInfo is the newest round close's commit facts, from its diff log
// entry (#130). Tree is "clean", "dirty", or "" when the entry predates the
// facts or git could not answer.
type CloseInfo struct {
	Round   int    `json:"round"`
	Commits int    `json:"commits"`
	Tree    string `json:"tree"`
}
```

In `BindingStatus`, after `LastPayload`:

```go
	// LastClose is the newest diff entry's commit facts; nil when the log
	// has no diff entry.
	LastClose *CloseInfo `json:"last_close,omitempty"`
	// Dirty is the rendered rule: the newest close left the tree dirty and
	// no newer round has been sent, so the uncommitted work is still what
	// the tree holds. False once a round is running -- a dirty tree is then
	// the expected state.
	Dirty bool `json:"dirty"`
```

In `statusRow`, after the `LastPayload` loop:

```go
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Kind == store.KindDiff {
			row.LastClose = &CloseInfo{Round: e.Round, Commits: e.Commits, Tree: e.Tree}
			break
		}
	}
	row.Dirty = row.LastClose != nil && row.LastClose.Tree == "dirty" && b.RoundStartedAt.IsZero()
```

In `RenderStatus`, directly after the header `Fprintf` and before the `Consults` check:

```go
		if b.Dirty {
			fmt.Fprint(&sb, " dirty")
		}
```

- [ ] **Step 4: Run** `go test -count=1 ./internal/relay -run 'TestStatus|TestRenderStatus'`. Expected: PASS, existing status tests unedited (`TestStatusJSONCarriesStructuredFields` checks named keys only, so the new `dirty` key does not disturb it).

- [ ] **Step 5: Commit**

```bash
git add internal/relay/status.go internal/relay/status_test.go
git commit -m "feat(relay): status shows dirty on a binding whose last close left uncommitted work (#130)"
```

---

### Task 9: full constituent set

- [ ] **Step 1: Run the four constituents** from "Running commands", in order. Expected: green. `gofmt -l .` prints nothing.

- [ ] **Step 2: `git diff --stat main..HEAD`** and compare with §2's file list plus the test files named in this plan. Anything outside it goes in the report.

- [ ] **Step 3: No commit** (nothing to commit unless a constituent found something; if `gofmt` reformatted a file, commit that as `style: gofmt (#130)`).

---

## Report

Your report is `NNN-report.md` in the drop directory, then the empty
`NNN-done`. It lists: each task's commit hash; the Task 6 mutation checks
and which subtests failed; the final constituent-set output; the `git diff --stat`; and any step where
you stopped, with the reason.
