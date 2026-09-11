# Candidates T4b: wire candidates into bind, add, fork, send and the CLI (#80)

**Design spec:** `docs/specs/2026-09-11-candidates-design.md` -- read §3.8, §3.9, §4.4–4.7, §4.11, §4.13, §5.2, §6
**Issue:** #80
**Depends on:** T1, T2, T3, T4a -- all merged into this tree.

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

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

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- **`Runtime.Aliases` stays in this task.** `ask.go`, `names.go` and
  `cmd/relay/doctor.go` still read it; T5 and T6 move them off it and T7
  deletes it. Add `Runtime.Candidates` beside it. `newRuntime` in
  `main.go` loads both.
- **No test may execute a `cmd/relay` subcommand that reaches herdr.** CI
  runners have no `herdr` binary. Every behavioural test is in
  `internal/relay` against `fakeHerdr` / `fakeGit`. The only `cmd/relay`
  change tested is the pure `isPaneID` helper.
- The decision rule is `resolveCandidate` from T4a. Do not re-implement
  any part of it at a call site; call it.
- `BuilderCandidate` is always stored as `c.Ref().String()` -- the
  canonical token -- never as the user's raw input, which may be empty.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: runtime field, three spawn paths, the preamble, the CLI

**Files:**
- Modify: `internal/relay/herdr.go` (Runtime)
- Modify: `internal/relay/bind.go`, `internal/relay/bind_test.go`
- Modify: `internal/relay/add.go`, `internal/relay/add_test.go`
- Modify: `internal/relay/fork.go`, `internal/relay/fork_test.go`
- Modify: `internal/relay/send.go`, `internal/relay/send_test.go`
- Modify: `internal/relay/answer.go` (error text only)
- Modify: `internal/relay/fake_test.go`, `internal/relay/status_test.go`, and any other `internal/relay/*_test.go` that sets `Alias:`
- Modify: `cmd/relay/main.go`, `cmd/relay/main_test.go`

**Interfaces consumed:**
- T1: `harness.Lookup`, `harness.RoleByName("builder")`, `Harness.Launch(provider, model, extra, role) Launch`
- T2: `candidate.Set`, `candidate.Load`, `candidate.ParseRef`, `candidate.ErrUnknownCandidate`
- T3: `store.Binding.BuilderCandidate`
- T4a: `resolveCandidate`, `ErrNoCandidates`, `ErrRoleNotServed`, `ErrAmbiguousCandidate`; test fixtures `candidateSet`, `testCandidatesJSON`, `testOpencodeRef`, `testClaudeRef`, `testAgyRef`

**Interfaces produced:**
- `relay.Runtime.Candidates *candidate.Set`
- `relay.BindOptions.Candidate string` (replaces `Alias`)
- `relay.AddOptions.Candidate string` (replaces `Alias`)
- `relay.ForkOptions.Candidate string` (replaces `Alias`)
- `relay.ErrNoBuilderCandidate` (replaces `ErrNoBuilderAlias`)
- `resolveBuilder(...) (store.Endpoint, string, error)` -- second value is the canonical token, `""` when adopted (unexported)
- `main.isPaneID(s string) bool` (unexported)

- [ ] **Step 1: `Runtime.Candidates` and the test runtime** (spec §3.8)

  `internal/relay/herdr.go`: add `Candidates *candidate.Set` to `Runtime`
  directly below `Aliases`, with the comment: the configured
  harness/provider/model triples (#80); `Aliases` remains only until T5–T7
  move `ask`, `names` and `doctor` off it.

  `internal/relay/bind_test.go` `newRuntime`: add
  `Candidates: candidateSet(t, testCandidatesJSON),`. Find every other
  constructor of `Runtime{...}` in `internal/relay/*_test.go`
  (`grep -n "Runtime{" internal/relay/*_test.go`; `newForkRuntime` is
  one) and add the same line.

  Run: `go build ./... && go vet ./...`. Expected: clean. `go test
  ./internal/relay/` still green (nothing reads the field yet).

- [ ] **Step 2: rename the option fields; migrate test call sites**

  Rename `BindOptions.Alias` → `Candidate`, `AddOptions.Alias` →
  `Candidate`, `ForkOptions.Alias` → `Candidate`. Update the doc comments:
  a `harness/provider/model` token; empty means resolve by role through
  `resolveCandidate`, except in `resume`, where empty means "not
  rebinding" (see step 3).

  In every `internal/relay/*_test.go`, apply this mapping to option
  literals **only** (not to `BuilderCandidate:` struct keys on bindings --
  those get the same mapping in step 5):

  | before | after |
  |---|---|
  | `Alias: "builder"` | `Candidate: testOpencodeRef` |
  | `Alias: "cbuilder"` | `Candidate: testClaudeRef` |
  | `Alias: "abuilder"` | `Candidate: testAgyRef` |
  | `Alias: "reviewer"` (only in `TestBindRefusesAConsultAlias`) | step 3 replaces that test |
  | `Alias: "nonexistent-builder"` | `Candidate: "claude/test/nope"` |

  `cmd/relay/main.go`: `opts.Alias = *builderAlias` → `opts.Candidate =
  ...`; `Alias: *builderAlias` in `cmdAdd` → `Candidate:`; the fork call
  likewise.

  Run: `go build ./... && go vet ./...`. Expected: clean. Tests will not
  all pass yet.

- [ ] **Step 3: `bind.go`** (spec §4.4, §5.2)

  Delete `ErrConsultAlias` and its use. Change `resolveBuilder`'s
  signature to return `(store.Endpoint, string, error)`; the string is the
  canonical token, `""` on the adopt path. Spawn path:

  ```
  c, err := resolveCandidate(rt.Candidates, opts.Candidate, "builder")
  if err != nil { return store.Endpoint{}, "", err }
  role, _ := harness.RoleByName("builder")
  h, _ := harness.Lookup(c.Harness)          // cannot miss: Load validated it; comment says so
  l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)
  agentName := builderAgentName(name) ...
  paneID := builderPane(...) ...
  StartAgent(ctx, agentName, l.Kind, paneID, l.Args)
  ep := store.Endpoint{AgentName: agentName, PaneID: paneID, Kind: l.Kind}
  … session lookup unchanged …
  return ep, c.Ref().String(), nil
  ```

  Delete the "There is no default builder" block and its error: an empty
  token now goes to `resolveCandidate`, whose errors say what to do.
  Update the function's doc comment accordingly (the adopt-path paragraph
  stays).

  Callers:
  - `Bind` (fresh): `builder, token, err := resolveBuilder(...)`;
    `BuilderCandidate: token` in the literal (replacing `opts.Alias`).
  - `resume`: `rebinding := opts.Candidate != "" || opts.BuilderPane != ""`
    -- **unchanged in meaning**: a bare `bind --resume` must not spawn, so
    resume never asks `resolveCandidate` to fill an empty token. Then
    `builder, token, err = resolveBuilder(...)` and `b.BuilderCandidate =
    token`. The comment `// "" when adopting a pane` stays.

  Tests (`bind_test.go`):
  - Every existing bind test passes with the step-2 mapping. Where a test
    asserts `BuilderCandidate == "builder"` (lines ~411, ~1248 region),
    change the expectation to `testOpencodeRef`.
  - Replace `TestBindRefusesAConsultAlias` with
    `TestBindRefusesACandidateThatDoesNotServeBuilder`: set
    `rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"test","model":"m","roles":["reviewer"]}]`)`,
    bind with `Candidate: testClaudeRef`; want `errors.Is(err,
    ErrRoleNotServed)`, `len(f.starts) == 0`, `f.splits == 0` (keep the
    existing comment about asserting splits).
  - New `TestBindResolvesTheOnlyBuilderCandidate`: `rt.Candidates =
    candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--x"]}]`)`,
    bind with `Candidate: ""`; want no error, `f.starts[0].Kind == "agy"`,
    `f.starts[0].Args` `DeepEqual` `[]string{"--model", "m", "--x"}`, and
    the stored binding's `BuilderCandidate == "agy/test/m"`.
  - New `TestBindRefusesAnAmbiguousCandidate`: default fixture (three
    serve builder), `Candidate: ""`; want `ErrAmbiguousCandidate`,
    `len(f.starts) == 0`, `f.splits == 0`.
  - New `TestBindWithNoCandidatesSaysSo`: `rt.Candidates = candidateSet(t,
    "[]")`, `Candidate: ""`; want `ErrNoCandidates`.
  - New `TestResumeWithoutABuilderDoesNotSpawn`: seed a bound binding,
    then `Bind` with `Resume: true, Name: <it>, Candidate: ""` on a
    runtime whose set has exactly one builder candidate; want no error and
    `len(f.starts) == 0` -- proves resume did not treat the empty token as
    "resolve".

  Run: `go test ./internal/relay/ -run 'Bind|Resume'`. Expected: green.

- [ ] **Step 4: `add.go` and `fork.go`** (spec §4.5, §4.6)

  `add.go`: delete `ErrAliasRequired`. Replace the `opts.Alias == ""`
  check (which sits **before** the worktree is cut -- keep it there) with:

  ```
  c, err := resolveCandidate(rt.Candidates, opts.Candidate, "builder")
  if err != nil { return AddResult{}, err }
  ```

  and pass `Candidate: c.Ref().String()` into `bindOpts`. Comment: resolve
  before `AddWorktree` for the same reason `builderAgentName` runs here --
  a refused add must leave no worktree. The `BuilderCandidate:` set on
  the result binding (line ~177) uses the same canonical string.

  `fork.go`: rename `ErrNoBuilderAlias` → `ErrNoBuilderCandidate`, text
  `source binding has no builder candidate; pass --builder`. Replace the
  inherit block:

  ```
  token := opts.Candidate
  if token == "" { token = src.BuilderCandidate }
  c, err := resolveCandidate(rt.Candidates, token, "builder")
  if err != nil && token == "" {
      return ForkResult{}, fmt.Errorf("%w (%v)", ErrNoBuilderCandidate, err)
  }
  if err != nil { return ForkResult{}, err }
  ```

  Use `c.Ref().String()` for `Candidate:` in the inner `BindOptions` and
  `BuilderCandidate:` on the result. This block runs where the alias block
  ran today -- before `AddWorktree`. Update the `Errors:` line in `Fork`'s
  doc comment and the `ForkOptions.Candidate` comment ("empty inherits the
  source's `BuilderCandidate`; a source with none (an adopted builder)
  falls through to `resolveCandidate`, so a one-candidate machine still
  forks without a flag").

  Tests:
  - `add_test.go`: rename `TestAddRequiresABuilderAlias` →
    `TestAddRefusesAnAmbiguousCandidateBeforeCuttingAWorktree`: default
    fixture, `Candidate: ""`; want `ErrAmbiguousCandidate` and
    `len(fg.addWorktreeCalls) == 0` (find the fake git's recorded calls;
    the name may differ -- read `fake_test.go`).
  - New `TestAddResolvesTheOnlyBuilderCandidate`: single-candidate set,
    `Candidate: ""`; want no error and `res.Binding.BuilderCandidate ==
    "agy/test/m"`.
  - `fork_test.go` ~line 340: the `src.BuilderCandidate = ""` case now
    wants `ErrNoBuilderCandidate` (default fixture is ambiguous, so
    resolution fails). Add a sibling case with a single-candidate runtime
    where the same fork **succeeds** and stores `"agy/test/m"`.
  - `fork_test.go` ~line 418 (unknown alias → rollback): an unknown token
    now fails **before** the worktree exists, so this subtest can no
    longer exercise rollback. Change it to use a valid `Candidate:
    testClaudeRef` and `fh.startErr = errors.New("start failed")` (read
    `fakeHerdr` for the field name) so `resolveBuilder` fails at
    `StartAgent` after the worktree is cut; keep the rollback assertions.
    Add a separate assertion-only subtest `unknown candidate cuts no
    worktree`: `Candidate: "claude/test/nope"`, want
    `candidate.ErrUnknownCandidate` and zero `AddWorktree` calls.

  Run: `go test ./internal/relay/ -run 'Add|Fork'`. Expected: green.

- [ ] **Step 5: `send.go` `composePrompt`, and the error strings** (spec §4.7)

  Rewrite `composePrompt` per spec §4.7:

  ```
  if !(b.Round == 1 || b.PreamblePending): return text, nil
  if b.BuilderCandidate == "": return text, nil          // adopted, or a pre-#80 binding
  ref, err := candidate.ParseRef(b.BuilderCandidate); if err: return "", err
  c, err := rt.Candidates.Lookup(ref)
  if errors.Is(err, candidate.ErrUnknownCandidate): return text, nil   // see comment
  if err: return "", err
  role, _ := harness.RoleByName("builder"); h, _ := harness.Lookup(c.Harness)
  l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)
  if l.Preamble == "": return text, nil
  return l.Preamble + "\n\n" + text, nil
  ```

  Comment on the tolerance: the builder is already running; the preamble
  is a round-1 courtesy, so a candidate removed from the file after bind
  must not fail the round (spec §4.7). `ErrBadRef` on a stored token is
  **not** tolerated -- relay wrote that value, so a malformed one is a bug.

  Error strings: in `send.go:73,103` and `answer.go:66,106` change
  `alias %s` → `candidate %s`.

  Test migration in `send_test.go` and `status_test.go` and any other
  test that sets `BuilderCandidate:` on a binding literal: apply the same
  mapping as step 2 (`"builder"` → `testOpencodeRef`, `"abuilder"` →
  `testAgyRef`, `"agy"` in `internal/ui` tests is a display string and
  stays). `seedBound` in `send_test.go` binds with `testAgyRef`, whose
  `Launch` preamble contains `plan-executor`, so
  `TestSendIncludesPreambleOnFirstRoundOnly` keeps its assertions.

  New test `TestSendWithoutPreambleWhenCandidateWasRemoved`: bind with
  `testAgyRef`, then `rt.Candidates = candidateSet(t, "[]")`, then `Send`
  round 1; want no error and `f.prompts[0].Text` **not** containing
  `plan-executor`.

  Run: `go test ./internal/relay/`. Expected: green except tests in
  `ask_test.go`/`names_test.go` that step 6 covers -- if any of those
  fail, stop and report; they should be untouched by this task.

- [ ] **Step 6: leave `ask` alone -- check it still compiles against `Aliases`**

  `ask.go` and `names.go` still read `rt.Aliases` and `consultTable(t)`.
  Do not touch them. Confirm `go test ./internal/relay/ -run 'Ask|Consult'`
  is green as before. If step 2's mapping touched an `Alias: "reviewer"`
  in `ask_test.go`, revert that: `AskOptions.Role` is not renamed.

- [ ] **Step 7: `cmd/relay/main.go`** (spec §4.11, §4.13)

  `newRuntime`: after `alias.LoadTable`, add

  ```
  candidates, err := candidate.Load(filepath.Join(configDir, "relay", "candidates.json"))
  ```

  and set `Candidates: candidates` in the `Runtime` literal. Keep the
  alias load.

  Add near `cmdBind`:

  ```go
  // isPaneID tells a herdr pane id apart from a candidate token on the same
  // --builder flag: a pane id has a ':' and never a '/', a candidate token
  // always has a '/'. A model name may contain ':', so ':' alone is not enough.
  func isPaneID(s string) bool
  ```

  returning `strings.Contains(s, ":") && !strings.Contains(s, "/")`. Use it
  in `cmdBind` in place of the bare `strings.Contains(*builderAlias, ":")`.

  Preflight kind in `cmdBind`: replace the `rt.Aliases.Lookup(opts.Alias)`
  branch with `resolveCandidate`-free code -- `cmd/relay` cannot call an
  unexported `internal/relay` function. Instead: export a thin wrapper in
  `internal/relay/candidate.go`:

  ```go
  // CandidateKind returns the harness kind a bind with this token would start,
  // for advisory preflight only; every error is reported as "" because the real
  // resolution happens inside Bind and says why.
  func CandidateKind(rt Runtime, token string) string
  ```

  (calls `resolveCandidate(rt.Candidates, token, "builder")` and returns
  `c.Harness` or `""`), with a two-row test in `candidate_test.go`
  (resolvable → kind; unknown → `""`). `cmdBind` uses it.

  `cmdAdd`: delete the `if *builderAlias == "" { return ... "relay add
  requires --builder ALIAS" }` block. Flag help strings, exactly:

  | command | help |
  |---|---|
  | bind `--builder` | `candidate harness/provider/model to spawn, or a pane id to adopt; omit when exactly one candidate serves builder` |
  | add `--builder` | `candidate harness/provider/model to spawn; omit when exactly one candidate serves builder` |
  | fork `--builder` | `candidate harness/provider/model to spawn (default: inherits source)` |

  Usage text in `cmdFork`'s error: `[--builder CANDIDATE]`. `cmdAdd`'s
  printf already reads `BuilderCandidate` (T3).

  Test (`cmd/relay/main_test.go`): `TestIsPaneID` -- `w2:p4` true,
  `claude/anthropic/sonnet` false, `opencode/openrouter/z-ai/glm-5.3-flash`
  false, `claude/anthropic/model:tag` false, `` false. Pure; no herdr.

  Run: `go build ./... && go test ./cmd/relay/`. Expected: green.

- [ ] **Step 8: `make check`, commit**

  ```bash
  git add -A internal cmd
  git commit -m "feat(relay): bind, add, fork and send run on candidates; --builder takes harness/provider/model (#80)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, and the list of
test functions you added or renamed. If any `ask_test.go` or
`names_test.go` test changed, say which and why -- it should be none.
