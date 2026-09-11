# Policy T2: `resolveCandidate` reads order and gates; `Resolution`; `ExplainResolution` (#61 step 2)

**Design spec:** `docs/specs/2026-09-11-policy-order-design.md` -- read §1, §3.3, §4.2, §4.5, §5.1, §6
**Issue:** #61 (step 2)
**Depends on:** T1 (`internal/policy`, `Runtime.Policy`) -- already in this tree.

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
- **`resolveCandidate` stays pure.** It takes `gates` as a value; it never
  opens the ledger itself. Callers pass `Gates(rt)`.
- **An explicit token never consults gates for the decision** (spec §4.2
  row 1). It records them in `Resolution.Gates` and proceeds.
- **A role with no `order` entry and two or more candidates still refuses**
  with `ErrAmbiguousCandidate` (spec §1 principle 2). Do not fall back to
  alphabetical when nothing is ordered.
- **Nothing is recorded in this task.** The four spawn paths and
  `CandidateKind` switch to the new signature and read `.Candidate`; the
  `pick` log entry, the result fields and the stderr line are T3.
- No test may execute a `cmd/relay` subcommand that reaches herdr.
  Everything here is in `internal/relay` with hand-built gates.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: the resolver

**Files:**
- Modify: `internal/relay/candidate.go`, `internal/relay/candidate_test.go`
- Modify (call sites only): `internal/relay/bind.go` (`resolveBuilder`),
  `internal/relay/add.go`, `internal/relay/fork.go`, `internal/relay/ask.go`

**Interfaces consumed:** `policy.Policy.Order(role) []string` (T1);
`ledger.Gate{Token, Kind, Until}`; `ledger.Kind`; `Gates(rt)`,
`GateKindText`, `GateUntilText` (`internal/relay/ledger.go`);
`candidate.Set.{Lookup, ForRole, Refs, Len}`, `candidate.ParseRef`,
`Candidate.{Ref, Serves}`.

**Interfaces produced** (spec §3.3, §4.2, §4.5) -- T3 and T4 build on
these names exactly:

```go
// internal/relay/candidate.go
var ErrAllGated = errors.New("every candidate serving the role is gated")

type How string
const (
    HowExplicit How = "explicit"
    HowSole     How = "sole"
    HowOrder    How = "order"
    HowUnlisted How = "unlisted"
)

type Skip struct {
    Token string
    Kind  ledger.Kind
    Until time.Time
}

type Resolution struct {
    Candidate     candidate.Candidate
    How           How
    Position      int
    Skipped       []Skip
    Gates         []Skip
    InheritedFrom string
}
func (r Resolution) Token() string        // "" when How == ""

func resolveCandidate(set *candidate.Set, pol policy.Policy, gates []ledger.Gate, token, role string) (Resolution, error)

// rankedEntry is one row of the walk; T4's FormatPolicy renders the same rows.
type rankedEntry struct {
    Candidate candidate.Candidate
    How       How   // HowOrder or HowUnlisted
    Position  int   // 1-based index in order[role] for HowOrder; 0 for HowUnlisted
}
func rankedList(set *candidate.Set, pol policy.Policy, role string) []rankedEntry
func skipsFor(gates []ledger.Gate, token string) []Skip
func skipText(s Skip) string
func ExplainResolution(role string, res Resolution) string
```

- [ ] **Step 1: types, `rankedList`, `skipsFor`** (spec §3.3, §4.2)

  In `internal/relay/candidate.go`, add the types from the interface block
  with doc comments taken from spec §3.3's field comments. `Token()`:

  ```go
  // Token is the canonical ref of the resolved candidate, or "" when there
  // was no resolution (an adopted pane has no candidate).
  func (r Resolution) Token() string {
      if r.How == "" {
          return ""
      }
      return r.Candidate.Ref().String()
  }
  ```

  `rankedList(set, pol, role)`: exactly spec §4.2 "Ranked list":

  ```
  seen := map[string]bool
  for i, tok := range pol.Order(role):
      ref, err := candidate.ParseRef(tok); if err → continue      // Load validated; belt and braces
      c, err := set.Lookup(ref);           if err → continue      // tolerated: T4 warns
      if !c.Serves(role) → continue                               // tolerated: T4 warns
      if seen[tok] → continue
      seen[tok] = true; out += rankedEntry{c, HowOrder, i+1}
  for _, c := range set.ForRole(role):                            // ref-sorted
      if seen[c.Ref().String()] → continue
      out += rankedEntry{c, HowUnlisted, 0}
  ```

  Doc comment: the order's configured, serving entries first, then every
  other serving candidate in ref order; an order entry relay cannot use is
  skipped here and reported by `PolicyWarnings` (T4), never an error,
  because removing a candidate must not break every subcommand (spec §3.1).

  `skipsFor(gates, token)`: one `Skip{g.Token, g.Kind, g.Until}` per gate
  whose `Token == token`, in `gates` order. `skipText(s)`:
  `fmt.Sprintf("%s (%s %s)", s.Token, GateKindText(s.Kind), GateUntilText(s.Until))`.

  Run: `go build ./internal/relay/`. Expected: clean (nothing calls them yet).

- [ ] **Step 2: the failing resolver tests** (spec §4.2 table, §5.1)

  In `candidate_test.go`, add to the test candidate fixtures:

  ```go
  // testTwoProviderJSON has builders on two providers, so a rate limit on
  // one leaves the other ungated.
  const testTwoProviderJSON = `[
    {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
    {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
    {"harness":"agy","provider":"other","model":"m","roles":["builder"]}
  ]`
  ```

  and, for the non-serving-entry row, a set where two candidates serve
  reviewer:

  ```go
  // testTwoReviewerJSON: claude and opencode serve reviewer; agy does not.
  const testTwoReviewerJSON = `[
    {"harness":"opencode","provider":"test","model":"m","roles":["builder","reviewer"]},
    {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
    {"harness":"agy","provider":"test","model":"m","roles":["builder"]}
  ]`
  ```

  and a policy helper:

  ```go
  func orderOf(role string, toks ...string) policy.Policy {
      return policy.Policy{Order: map[string][]string{role: toks}}
  }
  ```

  Rewrite `TestResolveCandidate`'s row struct to
  `{name, setBody, pol policy.Policy, gates []ledger.Gate, token, role,
  wantRef, wantHow How, wantPosition int, wantSkipped []string, wantErr,
  messageContains}`. Every existing row keeps its values (zero `pol`,
  nil `gates`); set `wantHow` on the three that succeed
  (`"named, serves"` and `"named, serves consult"` → `HowExplicit`;
  `"exactly one, no token"` → `HowSole`). The `"ambiguous, no token"` row gains `"policy.json"` in
  `messageContains`. Add rows (`spawn` below is a `ledger.Gate{Token: …,
  Kind: ledger.SpawnFailed, Until: baseTime.Add(10*time.Minute)}`; `limit`
  is `{Kind: ledger.RateLimited}` with a zero `Until`):

  | name | set | pol | gates | token | role | want |
  |---|---|---|---|---|---|---|
  | order, first ungated | testCandidatesJSON | `orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)` | nil | "" | builder | ref agy, `HowOrder`, position 1, no skips |
  | order, first gated | same | same | `spawn(agy)` | "" | builder | ref claude, `HowOrder`, position 2, skipped `[agy]` |
  | order, two gated, unlisted wins | same | `orderOf("builder", testAgyRef, testClaudeRef)` | `spawn(agy), spawn(claude)` | "" | builder | ref opencode, `HowUnlisted`, position 0, skipped `[agy, claude]` |
  | order, all gated | same | `orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)` | `limit(agy), limit(claude), limit(opencode)` | "" | builder | `ErrAllGated`; contains `every candidate serving "builder" is gated`, `agy/test/m (rate-limited until cleared)`, `--builder`, `relay available` |
  | order, unknown entry skipped | same | `orderOf("builder", "claude/test/nope", testOpencodeRef)` | nil | "" | builder | ref opencode, `HowOrder`, position 2 |
  | order, non-serving entry skipped | testTwoReviewerJSON | `orderOf("reviewer", testAgyRef, testOpencodeRef)` | nil | "" | reviewer | ref opencode, `HowOrder`, position 2 (agy is in the order but does not serve reviewer) |
  | sole beats order | testCandidatesJSON | `orderOf("reviewer", testClaudeRef)` | nil | "" | reviewer | ref claude, `HowSole`, position 0 (one candidate serves reviewer -- the sole rule runs before the order) |
  | order, two gates on one token | same | `orderOf("builder", testAgyRef, testClaudeRef)` | `spawn(agy), limit(agy)` | "" | builder | ref claude, skipped `[agy, agy]` |
  | sole, gated | same | zero | `limit(claude)` | "" | reviewer | `ErrAllGated` |
  | explicit, gated, proceeds | same | zero | `limit(agy)` | agy | builder | ref agy, `HowExplicit`, `Gates` has one entry (assert `len(got.Gates) == 1`), no skips |
  | no order, two serve, refuses | testTwoProviderJSON | zero | nil | "" | builder | `ErrAmbiguousCandidate` |
  | order with gate on other provider | testTwoProviderJSON | `orderOf("builder", "agy/other/m", testClaudeRef)` | `limit("agy/other/m")` | "" | builder | ref claude, position 2, skipped `[agy/other/m]` |

  The loop body: call `resolveCandidate(set, tt.pol, tt.gates, tt.token,
  tt.role)`; on error rows as today; on success compare
  `got.Candidate.Ref().String()`, `got.How`, `got.Position`, and the
  `Token`s of `got.Skipped` against `wantSkipped` (order matters).

  Update `TestResolveCandidateIsDeterministic` and `TestCandidateKind` to
  the new signature (`policy.Policy{}`, `nil`) and to `.Candidate`.

  Run: `go test ./internal/relay/ -run 'ResolveCandidate|CandidateKind'`.
  Expected: FAIL to compile.

- [ ] **Step 3: `resolveCandidate`** (spec §4.2, §5.1)

  Replace the function. Keep the doc comment's first sentence and rewrite
  the rest: with no token, the first ungated candidate in `order[role]`,
  then the unlisted ones; refuse only when everything serving the role is
  gated, or when nothing is ordered and several serve (the seam #61 step 3
  fills).

  ```
  if token != "":
      … ParseRef, Lookup, Serves as today …
      return Resolution{Candidate: c, How: HowExplicit, Gates: skipsFor(gates, c.Ref().String())}, nil
  if set.Len() == 0: ErrNoCandidates (unchanged text)
  serving := set.ForRole(role)
  if len(serving) == 0: ErrRoleNotServed (unchanged text)
  if len(serving) == 1:
      if s := skipsFor(gates, serving[0].Ref().String()); len(s) > 0: return allGated(role, s)
      return Resolution{Candidate: serving[0], How: HowSole}, nil
  if len(pol.Order(role)) == 0:
      ErrAmbiguousCandidate; message: "%d candidates serve %q: %v; name one with --builder or --candidate, or set order.%s in ~/.config/relay/policy.json: %w"
  var skipped []Skip
  for _, r := range rankedList(set, pol, role):
      s := skipsFor(gates, r.Candidate.Ref().String())
      if len(s) == 0: return Resolution{Candidate: r.Candidate, How: r.How, Position: r.Position, Skipped: skipped}, nil
      skipped = append(skipped, s...)
  return allGated(role, skipped)
  ```

  `allGated(role string, skipped []Skip) (Resolution, error)` builds spec
  §6's text: `every candidate serving %q is gated: <skipText, joined ", ">;
  name one with --builder to bypass, or clear a gate with relay available
  <provider>: %w` wrapping `ErrAllGated`.

  Update the four call sites and `CandidateKind` to
  `resolveCandidate(rt.Candidates, rt.Policy, Gates(rt), <token>, <role>)`
  and use `res.Candidate` where `c` was used (rename the local to `c :=
  res.Candidate` right after the call to keep the diff small). `Ask`
  keeps its `c` for the three `AskResult` literals. `Fork`'s early call
  and `resolveBuilder`'s call both change; `Add`'s early call too.

  Run: `go test ./internal/relay/ -run 'ResolveCandidate|CandidateKind'`.
  Expected: green. Then `go test -count=1 ./...` -- every bind/add/fork/ask
  test still passes: they name a token or hit the sole rule, and the zero
  `Policy` changes nothing for them.

- [ ] **Step 4: `ExplainResolution`** (spec §4.5)

  ```go
  // ExplainResolution is the one line that says what was picked and why.
  // The pick log entry, the stderr line after a spawn, and `relay policy`
  // all render from it, so a pick the planner reads in `relay log` is
  // word-for-word what bind printed (spec §1 principle 1).
  func ExplainResolution(role string, res Resolution) string
  ```

  Exactly spec §4.5's table. Build `head := "picked " + res.Token() + " for " + role + ": "`, then per `How`:
  `sole` → `sole candidate`; `order` → `fmt.Sprintf("order #%d",
  res.Position)`; `unlisted` → `unlisted, after order`; `explicit` →
  `explicit, policy bypassed`, or `explicit, inherited from <src>, policy
  bypassed` when `InheritedFrom != ""`. Then, when `len(res.Skipped) > 0`:
  `"; skipped " + join(skipText over Skipped, ", ")`. Then, when `How ==
  HowExplicit && len(res.Gates) > 0`: `"; gated: " + join(over Gates of
  GateKindText(kind)+" "+GateUntilText(until), ", ")`. No trailing newline.

  Tests in `candidate_test.go`, `TestExplainResolution`, a table over a
  fixed `until := baseTime.Add(10 * time.Minute)` and `untilText :=
  GateUntilText(until)` (local time; build the expected string with the
  same helper rather than a literal `HH:MM`):

  | res | want |
  |---|---|
  | `{How: HowSole, Candidate: claude}` role reviewer | `picked claude/test/m for reviewer: sole candidate` |
  | `{How: HowOrder, Position: 2, Candidate: claude, Skipped: [{agy, SpawnFailed, until}]}` | `picked claude/test/m for builder: order #2; skipped agy/test/m (spawn failed <untilText>)` |
  | `{How: HowUnlisted, Candidate: opencode, Skipped: [agy spawn, claude rate-limited zero]}` | `picked opencode/test/m for builder: unlisted, after order; skipped agy/test/m (spawn failed <untilText>), claude/test/m (rate-limited until cleared)` |
  | `{How: HowExplicit, Candidate: agy}` | `picked agy/test/m for builder: explicit, policy bypassed` |
  | `{How: HowExplicit, Candidate: agy, InheritedFrom: "source", Gates: [{agy, RateLimited, zero}]}` | `picked agy/test/m for builder: explicit, inherited from source, policy bypassed; gated: rate-limited until cleared` |

  Build the `candidate.Candidate` values through `candidateSet(t,
  testCandidatesJSON).Lookup(...)`.

  Run: `go test ./internal/relay/ -run ExplainResolution`. Expected: green.

- [ ] **Step 5: `make check`, commit**

  ```bash
  git add -A internal
  git commit -m "feat(relay): resolveCandidate walks policy order past ledger gates; Resolution + ExplainResolution (#61 step 2)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, the list of
test functions added or rewritten, and the exact `ErrAllGated` and
`ErrAmbiguousCandidate` messages as produced (copy from a failing-row
`t.Log` or `go run`).
