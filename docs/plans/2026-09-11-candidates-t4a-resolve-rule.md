# Candidates T4a: the `resolveCandidate` rule, pure (#80)

**Design spec:** `docs/specs/2026-09-11-candidates-design.md` -- read §4.3, §5.4, §6
**Issue:** #80
**Depends on:** T1, T2 -- already in this tree.

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
- This task **creates** `internal/relay/candidate.go` and
  `internal/relay/candidate_test.go` and touches nothing else. No call
  site uses `resolveCandidate` yet -- that is T4b. `go vet` does not flag
  an unused unexported function, so this compiles.
- `resolveCandidate` is pure: no I/O, no `Runtime`, no herdr. It is the
  one piece of decision logic in #80 and it is tested exhaustively here so
  T4b can wire it without re-testing the rule.
- Every exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: sentinels, `resolveCandidate`, the test fixture T4b will reuse

**Files:**
- Create: `internal/relay/candidate.go`
- Create: `internal/relay/candidate_test.go`

**Interfaces consumed** (T2): `candidate.Set` with `Len()`, `Refs()`,
`ForRole(role)`, `Lookup(ref)`; `candidate.ParseRef`; `candidate.ErrBadRef`,
`candidate.ErrUnknownCandidate`; `candidate.Load(path)`.

**Interfaces produced** (spec §4.3, §6):

```go
var ErrNoCandidates       = errors.New("no candidates configured")
var ErrRoleNotServed      = errors.New("no candidate serves role")
var ErrAmbiguousCandidate = errors.New("more than one candidate serves role")
var ErrUnknownRole        = errors.New("unknown role")

func resolveCandidate(set *candidate.Set, token, role string) (candidate.Candidate, error)

// test-only, package relay:
func candidateSet(t *testing.T, body string) *candidate.Set
```

- [ ] **Step 1: the fixture** 

  In `candidate_test.go` add:

  ```go
  // candidateSet loads a candidate set from a JSON body, for tests that need
  // a specific configuration without a file in the repo.
  func candidateSet(t *testing.T, body string) *candidate.Set
  ```

  It writes `body` to `filepath.Join(t.TempDir(), "candidates.json")`,
  calls `candidate.Load`, and `t.Fatalf`s on error. Also add three
  package-level constants the tests use (T4b will use them too):

  ```go
  const (
      testOpencodeRef = "opencode/test/m"
      testClaudeRef   = "claude/test/m"
      testAgyRef      = "agy/test/m"
  )

  // testCandidatesJSON mirrors the shape of the three aliases DefaultTable
  // used to ship, plus a reviewer on claude, so migrated tests keep their
  // meaning: opencode and agy serve builder only; claude serves both.
  const testCandidatesJSON = `[
    {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
    {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
    {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}
  ]`
  ```

- [ ] **Step 2: the failing tests** (spec §4.3)

  `TestResolveCandidate` -- a table. Each row names the set body, `token`,
  `role`, the wanted ref string (when no error), the wanted sentinel
  (`errors.Is`), and a substring the message must contain:

  | case | set | token | role | want ref | want err | message contains |
  |---|---|---|---|---|---|---|
  | named, serves | testCandidatesJSON | `claude/test/m` | builder | `claude/test/m` | — | — |
  | named, serves consult | testCandidatesJSON | `claude/test/m` | reviewer | `claude/test/m` | — | — |
  | named, does not serve | testCandidatesJSON | `agy/test/m` | reviewer | — | `ErrRoleNotServed` | `does not serve role "reviewer"` and `[builder]` |
  | named, unknown | testCandidatesJSON | `claude/test/opus` | builder | — | `candidate.ErrUnknownCandidate` | `configured:` |
  | named, malformed | testCandidatesJSON | `claude/test` | builder | — | `candidate.ErrBadRef` | `harness/provider/model` |
  | empty set, no token | `[]` | `` | builder | — | `ErrNoCandidates` | `candidates.json` |
  | none serve, no token | testCandidatesJSON | `` | researcher | — | `ErrRoleNotServed` | `no configured candidate serves role "researcher"` |
  | exactly one, no token | testCandidatesJSON | `` | reviewer | `claude/test/m` | — | — |
  | ambiguous, no token | testCandidatesJSON | `` | builder | — | `ErrAmbiguousCandidate` | `3 candidates serve "builder"` and `name one with` |

  Also `TestResolveCandidateIsDeterministic`: call the "exactly one" case
  twice and the "ambiguous" case twice; the results (ref or error string)
  are identical each time.

  Run: `go test ./internal/relay/ -run TestResolveCandidate`. Expected:
  compile error, `resolveCandidate` undefined.

- [ ] **Step 3: `resolveCandidate`** (spec §4.3, §5.4)

  In `candidate.go`, the four sentinels with doc comments, then:

  ```go
  // resolveCandidate is the one rule for an omitted candidate token, shared by
  // bind, add, fork and ask: a named token is looked up and must serve the
  // role; with no token, exactly one configured candidate serving the role is
  // used, and anything else is refused with the list. It is deterministic and
  // makes no judgement -- the refusal is the seam #61 fills with a scorer.
  func resolveCandidate(set *candidate.Set, token, role string) (candidate.Candidate, error)
  ```

  Behaviour, in order:

  1. `token != ""`: `ref, err := candidate.ParseRef(token)`; return `err`
     as is. `c, err := set.Lookup(ref)`; return `err` as is. If
     `!c.Serves(role)`: `fmt.Errorf("candidate %q does not serve role %q
     (its roles: %v): %w", token, role, c.Roles, ErrRoleNotServed)`.
     Return `c`.
  2. `set.Len() == 0`: `fmt.Errorf("%w; write ~/.config/relay/candidates.json
     (see README \"Candidates\")", ErrNoCandidates)`.
  3. `cs := set.ForRole(role)`; `len(cs) == 0`: `fmt.Errorf("no configured
     candidate serves role %q (configured: %v): %w", role, set.Refs(),
     ErrRoleNotServed)`.
  4. `len(cs) == 1`: return `cs[0]`.
  5. otherwise, with `refs` built from each `c.Ref().String()`:
     `fmt.Errorf("%d candidates serve %q: %v; name one with --builder or
     --candidate: %w", len(cs), role, refs, ErrAmbiguousCandidate)`.

  Note the `~/.config/relay/candidates.json` path is prose in an error
  message, not a path relay composes -- the real path is composed in
  `cmd/relay/main.go` through `userConfigRoot()` (T4b).

  Run: `go test ./internal/relay/ -run TestResolveCandidate`. Expected:
  green.

- [ ] **Step 4: `make check`, commit**

  ```bash
  git add internal/relay/candidate.go internal/relay/candidate_test.go
  git commit -m "feat(relay): resolveCandidate, the one rule for an omitted candidate token (#80)"
  ```

## Report

Include the `go test ./internal/relay/ -run TestResolveCandidate -v`
summary, the `make check` result, and `git diff --stat HEAD~1` (exactly two
new files).
