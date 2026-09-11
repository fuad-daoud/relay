# `bind --resume --rebind` and de-duplicated gate text (#92, #93)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issues:** #92 (`bind --resume` has no "rebind and let the order pick"
spelling) and #93 (`relay policy` and pick entries repeat a gate's text once
per ledger entry). Closes both. Both are follow-ups to #88; the design spec is
`docs/specs/2026-09-11-policy-order-design.md` -- read §1 (principles) only.
**Depends on:** nothing open. #96 landed.

**Goal:** (#93) identical gate texts on one candidate render once, without
touching the ledger or the `Skip` list; (#92) `relay bind --resume --name N
--rebind` replaces a gone builder by resolving through `policy.json` order and
the ledger, exactly as a fresh `bind` with `--builder` omitted does, and logs a
`pick` entry.

**Architecture:** #93 is display-only: one `uniqStrings` helper applied where
gate texts are joined (`ExplainResolution`, `allGated`, `FormatPolicy`).
`resolveCandidate` still yields one `Skip` per gate, as specified. #92 adds a
`Rebind bool` to `BindOptions` as a third way for `resume` to be "rebinding";
`resolveBuilder` already resolves an empty token through the order, and
`resume` already logs the pick, so the change is the flag, the rule, and the
CLI wiring.

**Tech stack:** Go, `make check`.

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

**Do not run `relay`, `herdr`, or `agy` for any reason.** `relay` is on your
PATH and its subcommands reach herdr; running one from a builder pane spawns
agents the planner then has to clean up. Live verification is the planner's,
after merge. The only commands you need are `go`, `gofmt`, `grep`, `git`.

## Global constraints

- `resolveCandidate` keeps yielding one `Skip` per live gate. #93 changes rendering only; the ledger and its semantics are untouched (`relay unavailable` still appends).
- `gatedNote` (the stderr advisory after a spawn) is **not** de-duplicated: its parts carry `since HH:MM` and the reason, which differ per entry and are the point of that line.
- `--rebind` is an error without `--resume`. `--rebind --builder X` is allowed and means the same as `--resume --builder X` (an explicit token bypasses the order either way).
- One commit per task.

---

### Task 1: #93 -- identical gate texts render once

**Files:**
- Modify: `internal/relay/candidate.go` (`allGated` ~L203, `ExplainResolution` ~L217-255)
- Modify: `internal/relay/policy_view.go` (`FormatPolicy` row tail, ~L155-160)
- Test: `internal/relay/candidate_test.go` (`TestExplainResolution` ~L361), `internal/relay/policy_view_test.go`

**Interfaces:**
- Produces: `func uniqStrings(in []string) []string` in `candidate.go` -- returns `in` with later duplicates dropped, first occurrence kept, order preserved; nil in → nil out.

- [ ] **Step 1: Write the failing tests**

Add a case to the `tests` table in `TestExplainResolution` (`internal/relay/candidate_test.go`), after the `"unlisted"` case:

```go
		{
			// #93: three `relay unavailable` calls on one provider are three
			// ledger entries and three Skips, but one sentence.
			name: "duplicate gates on one token render once",
			role: "builder",
			res: Resolution{
				How:       HowOrder,
				Position:  2,
				Candidate: claude,
				Skipped: []Skip{
					{Token: testAgyRef, Kind: ledger.RateLimited},
					{Token: testAgyRef, Kind: ledger.RateLimited},
					{Token: testAgyRef, Kind: ledger.RateLimited},
				},
			},
			want: "picked claude/test/m for builder: order #2; skipped " + testAgyRef + " (rate-limited until cleared)",
		},
		{
			name: "explicit with duplicate gates renders once",
			role: "builder",
			res: Resolution{
				How:       HowExplicit,
				Candidate: agy,
				Gates: []Skip{
					{Token: testAgyRef, Kind: ledger.RateLimited},
					{Token: testAgyRef, Kind: ledger.RateLimited},
				},
			},
			want: "picked agy/test/m for builder: explicit, policy bypassed; gated: rate-limited until cleared",
		},
```

Append to `internal/relay/policy_view_test.go`:

```go
func TestFormatPolicyRepeatedGateRendersOnce(t *testing.T) {
	// #93: `relay unavailable` three times without an `available` between
	// leaves three live ledger entries on one token. The row says it once.
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef)
	gates := []ledger.Gate{
		{Token: testAgyRef, Kind: ledger.RateLimited},
		{Token: testAgyRef, Kind: ledger.RateLimited},
		{Token: testAgyRef, Kind: ledger.RateLimited},
	}

	got := FormatPolicy(set, pol, gates, history.History{}, baseTime, time.UTC)

	wantRow := "  1  agy/test/m       order     rate-limited until cleared\n"
	if !strings.Contains(got, wantRow) {
		t.Errorf("FormatPolicy =\n%s\nwant a row exactly %q", got, wantRow)
	}
	if strings.Contains(got, "until cleared; rate-limited") {
		t.Errorf("gate text repeated:\n%s", got)
	}
}

func TestAllGatedErrorNamesEachGateOnce(t *testing.T) {
	// The refusal text goes through the same renderer as the pick line.
	set := candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)
	gates := []ledger.Gate{
		{Token: testAgyRef, Kind: ledger.RateLimited},
		{Token: testAgyRef, Kind: ledger.RateLimited},
	}

	_, err := resolveCandidate(set, policy.Policy{}, gates, "", "builder")
	if !errors.Is(err, ErrAllGated) {
		t.Fatalf("got %v, want ErrAllGated", err)
	}
	if n := strings.Count(err.Error(), "rate-limited until cleared"); n != 1 {
		t.Errorf("gate text appears %d times in %q, want 1", n, err.Error())
	}
}
```

`policy_view_test.go` already imports `policy`; add `"errors"` to its imports.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/relay -run 'TestExplainResolution|TestFormatPolicyRepeatedGateRendersOnce|TestAllGatedErrorNamesEachGateOnce' -v`
Expected: FAIL -- the texts repeat (`rate-limited until cleared, agy/test/m (rate-limited until cleared), …` and `until cleared; rate-limited until cleared; …`).

- [ ] **Step 3: Add `uniqStrings` and apply it at the three join points**

In `internal/relay/candidate.go`, directly below `skipText`:

```go
// uniqStrings drops later duplicates, keeping first occurrences in order.
// Gate texts are de-duplicated at render time only (#93): the ledger keeps
// every `relay unavailable` entry and resolveCandidate yields one Skip per
// gate, but a row or a pick line says each distinct text once.
func uniqStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
```

Then:
- `allGated`: `strings.Join(texts, ", ")` → `strings.Join(uniqStrings(texts), ", ")`.
- `ExplainResolution`, the `Skipped` block: `out += "; skipped " + strings.Join(texts, ", ")` → `out += "; skipped " + strings.Join(uniqStrings(texts), ", ")`.
- `ExplainResolution`, the `HowExplicit && len(res.Gates) > 0` block: `out += "; gated: " + strings.Join(texts, ", ")` → `out += "; gated: " + strings.Join(uniqStrings(texts), ", ")`.

In `internal/relay/policy_view.go` `FormatPolicy`, the gate loop currently appends straight into `tailParts` after the peak text. Collect the gate texts separately so the peak text is never a dedupe candidate:

```go
			var gateTexts []string
			for _, g := range byToken[tok] {
				gateTexts = append(gateTexts, GateKindText(g.Kind)+" "+GateUntilText(g.Until))
			}
			tailParts = append(tailParts, uniqStrings(gateTexts)...)
```

(replacing the existing `for _, g := range byToken[tok] { tailParts = append(...) }` loop; the `peakText` append above it stays as is).

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS, including the three new/extended tests and every existing `TestFormatPolicy*` (distinct gates, e.g. `spawn failed until 10:20` and `rate-limited until cleared` on one row, still both render).

- [ ] **Step 5: Commit**

```bash
git add internal/relay/candidate.go internal/relay/policy_view.go internal/relay/candidate_test.go internal/relay/policy_view_test.go
git commit -m "fix(policy): identical gate texts render once per row and per pick line (#93)"
```

---

### Task 2: #92 -- `relay bind --resume --rebind` resolves through the order

**Files:**
- Modify: `internal/relay/bind.go` (`BindOptions` ~L33-56; `resume` ~L117-123 and its doc comment)
- Modify: `cmd/relay/main.go` (`cmdBind` ~L486-575)
- Modify: `README.md` (~L160 synopsis; ~L795-799 recovery block)
- Test: `internal/relay/bind_test.go`, `cmd/relay/main_test.go`

**Interfaces:**
- Consumes: `resolveBuilder(ctx, rt, nil, opts, name, plannerPane)` -- with `opts.Candidate == ""` and `opts.BuilderPane == ""` it already goes through `resolveCandidate(…, "", "builder")` and returns a `Resolution` with `How` set; `resume` already appends `pickEntry` when `rebinding && res.How != ""`. Nothing there changes.
- Produces: `BindOptions.Rebind bool`; the resume rule `rebinding := opts.Rebind || opts.Candidate != "" || opts.BuilderPane != ""`; the `--rebind` flag on `relay bind`.

- [ ] **Step 1: Write the failing test (core)**

Append to `internal/relay/bind_test.go`:

```go
func TestResumeRebindResolvesThroughTheOrder(t *testing.T) {
	// #92: a builder that halted between rounds is gone, no round is open,
	// and the planner wants a replacement without naming a token. Rebind
	// must walk policy.json order and the ledger exactly as create does,
	// and record the pick.
	existing := store.Binding{
		Name:             "webshop",
		CWD:              "/repo",
		Round:            3,
		State:            store.StateBroken,
		Planner:          store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:          store.Endpoint{PaneID: "w2:p4", SessionID: "dead-builder-sess", Kind: "opencode"},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{
		agents: []herdr.Agent{
			plannerAgent(),
			{Kind: "agy", Status: herdr.StatusWorking, PaneID: "w2:p9", Session: herdr.Session{Value: "new-builder-sess"}},
		},
		newPane: "w2:p9",
	}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}

	if res.How != HowOrder || res.Position != 1 {
		t.Errorf("resolution = %+v, want order #1", res)
	}
	if got.BuilderCandidate != testAgyRef {
		t.Errorf("BuilderCandidate = %q, want the order's first, %s", got.BuilderCandidate, testAgyRef)
	}
	if got.Builder.PaneID != "w2:p9" || got.State != store.StateActive || got.Round != 3 {
		t.Errorf("binding = %+v, want new builder in w2:p9, active, still round 3", got)
	}
	if len(f.starts) != 1 || f.starts[0].Kind != "agy" {
		t.Errorf("starts = %+v, want exactly one agy start", f.starts)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var picks int
	for _, e := range entries {
		if e.Kind == store.KindPick && e.Round == 3 && e.Note == ExplainResolution("builder", res) {
			picks++
		}
	}
	if picks != 1 {
		t.Errorf("want exactly one pick entry for round 3 reading %q, got entries %+v", ExplainResolution("builder", res), entries)
	}
}

func TestResumeRebindRefusesALiveBuilder(t *testing.T) {
	// --rebind is a rebind: the ErrBuilderAlive guard applies to it exactly
	// as it does to --builder.
	liveBuilder := herdr.Agent{
		Kind: "opencode", Status: herdr.StatusWorking, PaneID: "w2:p4",
		Session: herdr.Session{Value: "live-builder-sess"}, CWD: "/repo",
	}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent(), liveBuilder}, newPane: "w2:p5"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	existing := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 3, State: store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", SessionID: "live-builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("got %v, want ErrBuilderAlive", err)
	}
	if len(f.starts) != 0 || len(f.tabs) != 0 {
		t.Errorf("a refused rebind must spawn nothing: starts=%+v tabs=%+v", f.starts, f.tabs)
	}
}
```

`TestResumeWithoutABuilderDoesNotSpawn` (already in the file) stays as the
pin that `--resume` alone is still planner-only.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/relay -run 'TestResumeRebind' -v`
Expected: compile error `unknown field Rebind in struct literal of type BindOptions`.

- [ ] **Step 3: Add the field and the rule**

In `internal/relay/bind.go`, `BindOptions`, directly after the `Resume bool` line:

```go
	// Rebind, with Resume, replaces a builder that is gone by resolving a
	// candidate through policy.json order and the ledger, exactly as a
	// fresh bind with Candidate empty does (#92). Without it, an empty
	// Candidate and BuilderPane on resume mean "planner-only: touch no
	// builder". Ignored when Candidate or BuilderPane is set.
	Rebind bool
```

In `resume`, change the rule to:

```go
	rebinding := opts.Rebind || opts.Candidate != "" || opts.BuilderPane != ""
```

In the `resume` doc comment, change `and -- when the caller supplied a builder -- at a new builder as well.` to `and -- when the caller supplied a builder, or asked for one with Rebind -- at a new builder as well.`

- [ ] **Step 4: Run the core tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS.

- [ ] **Step 5: Write the failing CLI test**

Append to `cmd/relay/main_test.go`. It fails in `cmdBind`'s own argument
check, before `newRuntime`, so it never reaches herdr (which is why it is
allowed in `cmd/relay`: CI runners have no `herdr` binary).

```go
// TestBindRebindNeedsResume pins #92: --rebind only means something on a
// resume. It is refused before newRuntime, so no herdr is reached.
func TestBindRebindNeedsResume(t *testing.T) {
	err := run([]string{"bind", "--rebind", "--name", "x"})
	if err == nil || !strings.Contains(err.Error(), "--rebind") || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("got %v, want an error naming --rebind and --resume", err)
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test -count=1 ./cmd/relay -run TestBindRebindNeedsResume -v`
Expected: FAIL with `flag provided but not defined: -rebind` (wrong error: it must mention both flags).

- [ ] **Step 7: Wire the flag**

In `cmd/relay/main.go` `cmdBind`:

- After the `assumeDead` flag definition, add:
  ```go
  	rebind := fs.Bool("rebind", false,
  		"with --resume: replace a gone builder, picking it by policy.json order and the ledger (like bind with --builder omitted)")
  ```
- Directly after the existing `if *resume && *name == "" { … }` check, add:
  ```go
  	if *rebind && !*resume {
  		return fmt.Errorf("relay bind --rebind only applies with --resume (it replaces a gone builder on an existing binding)")
  	}
  ```
- In the `opts := relay.BindOptions{…}` literal, add `Rebind: *rebind,` after `Resume: *resume,`.
- The advisory preflight computes `kind` for the adopted path from the stored binding's old builder. For a rebind that would preflight the wrong harness, so change the `adopted` block: after `adopted := *resume || opts.BuilderPane != ""`, add nothing; instead change the `else` branch condition so a rebind preflights the candidate the order would pick:
  ```go
  	adopted := *resume || opts.BuilderPane != ""
  	kind := ""
  	switch {
  	case *rebind && opts.BuilderPane == "":
  		kind = relay.CandidateKind(rt, opts.Candidate)
  	case adopted:
  		// (existing body of the `if adopted {` block, unchanged)
  	default:
  		kind = relay.CandidateKind(rt, opts.Candidate)
  	}
  ```
  Keep the existing `if adopted { … } else { … }` body text; only the shape changes to a `switch` with the rebind case first.
- The "rebound" message condition `if *resume && *builderAlias != "" {` becomes `if *resume && (*builderAlias != "" || *rebind) {`. The body is unchanged: it already prints `builderDesc` from `b.BuilderCandidate` and calls `notePick("builder", res)`.

- [ ] **Step 8: README**

- Line ~160 synopsis: `relay bind [--name N] [--builder CANDIDATE|PANE_ID] [--resume] [--timeout D]` → `relay bind [--name N] [--builder CANDIDATE|PANE_ID] [--resume [--rebind]] [--timeout D]`.
- In the recovery block (~L795-799) add a third line so it reads:
  ```bash
  relay bind --resume --name N --rebind                                       # spawn a fresh builder, picked by policy order
  relay bind --resume --name N --builder agy/google/gemini-3.8-flash-high     # spawn a fresh builder, naming it
  relay bind --resume --name N --builder w2:p4        # adopt an existing pane
  ```
- Two lines below that block, after `like any builder relay spawns.`, add the sentence: `With --rebind the candidate is resolved through policy.json order and the ledger, and the pick is logged, exactly as a fresh bind with --builder omitted.`

- [ ] **Step 9: Full verification**

Run: `go test -count=1 ./cmd/relay ./internal/relay`
Expected: PASS.

Run: `make check` (or its constituents; see "Running commands").
Expected: green.

Mutation: in `internal/relay/bind.go` `resume`, temporarily change the rule back to `rebinding := opts.Candidate != "" || opts.BuilderPane != ""`. Run `go test -count=1 ./internal/relay -run TestResumeRebindResolvesThroughTheOrder`. Expected: FAIL with `starts = [], want exactly one agy start` (or the resolution/candidate line). Revert with `git checkout internal/relay/bind.go`, re-run, PASS. Do not commit the mutation.

- [ ] **Step 10: Commit**

```bash
git add internal/relay/bind.go internal/relay/bind_test.go cmd/relay/main.go cmd/relay/main_test.go README.md
git commit -m "feat(bind): --resume --rebind replaces a gone builder by policy order (#92)"
```

---

## Report

Your report says, in this order:

1. `make check` output tail (or the constituents, if `make` was intercepted).
2. Which test failed under the Task 2 mutation, and its message.
3. `git log --oneline main..HEAD` and `git diff --stat main..HEAD`.
4. Anything you had to stop on.

## Planner verification (after merge, not the builder's job)

- `relay unavailable <token>` three times, then `relay policy`: the row reads `rate-limited until cleared` once. `relay available <provider>` to clear.
- On a binding whose builder has halted between rounds: `relay bind --resume --name N --rebind` spawns the order's first ungated candidate, prints `rebound N: builder <pane> (<token>), still on round R` plus the `picked … order #1` line, and `relay log --name N` shows the pick.
- `relay bind --rebind --name N` (no `--resume`) is refused with a message naming both flags.
