# Policy T4: `relay policy`, `PolicyWarnings`, `doctor` policy rows (#61 step 2)

**Design spec:** `docs/specs/2026-09-11-policy-order-design.md` -- read §4.6, §4.7, §4.8, §4.9, §5.4
**Issue:** #61 (step 2)
**Depends on:** T2 (`rankedList`, `rankedEntry`, `resolveCandidate`, `ErrAllGated`, `skipsFor`) -- already in this tree. Independent of T3.

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
- **The `<- would pick` marker comes from `resolveCandidate` itself**, not
  from re-implementing the walk (spec §4.7). If the table and `bind` can
  disagree, the table is a bug.
- **`PolicyWarnings` is the one source of the three findings.** `relay
  policy`'s warnings block and `doctor`'s rows both render from it; neither
  computes its own.
- **`relay policy` exits 0 always.** It is a listing, not a check. `doctor`
  rows are `SevWarn` and do not count as failures.
- No test may execute a `cmd/relay` subcommand that reaches herdr.
  `FormatPolicy` and `PolicyWarnings` are tested in `internal/relay`;
  `policyChecks` as a pure function in `cmd/relay/doctor_test.go`.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: the policy view

**Files:**
- Create: `internal/relay/policy_view.go`, `internal/relay/policy_view_test.go`
- Modify: `cmd/relay/main.go` (`cmdPolicy`, dispatch, `usage`)
- Modify: `cmd/relay/doctor.go` (`policyChecks`, wiring in `cmdDoctor`)
- Modify: `cmd/relay/doctor_test.go`

**Interfaces consumed** (T2): `rankedList(set, pol, role) []rankedEntry`,
`rankedEntry{Candidate, How, Position}`, `resolveCandidate(set, pol,
gates, "", role)`, `ErrAllGated`, `ErrAmbiguousCandidate`,
`ErrRoleNotServed`, `ErrNoCandidates`, `skipsFor`, `HowSole`, `HowOrder`,
`HowUnlisted`; `GateKindText`, `GateUntilText`, `Gates(rt)`;
`harness.RoleNames()`; `policy.Policy.Order`; `candidate.Set.{ForRole,
Lookup, Len}`, `candidate.ParseRef`; `doctor.Check`, `doctor.SevWarn`.

**Interfaces produced** (spec §4.6–4.8):

```go
// internal/relay/policy_view.go
type PolicyWarning struct {
    Role  string
    Index int      // index into order[role]; -1 for an unlisted-candidate warning
    Token string
    Text  string
}
func PolicyWarnings(set *candidate.Set, pol policy.Policy) []PolicyWarning
func FormatPolicy(set *candidate.Set, pol policy.Policy, gates []ledger.Gate) string

// cmd/relay/doctor.go
func policyChecks(warnings []relay.PolicyWarning) []doctor.Check
```

- [ ] **Step 1: the failing `PolicyWarnings` tests** (spec §4.6)

  Create `internal/relay/policy_view_test.go`. Use `candidateSet(t,
  testCandidatesJSON)` (opencode, claude, agy on provider `test`; claude
  also serves reviewer) and T2's `orderOf`.

  - `TestPolicyWarningsNoneWhenConsistent`: `orderOf("builder",
    testAgyRef, testClaudeRef, testOpencodeRef)` → empty.
  - `TestPolicyWarningsNoneWhenNoOrder`: zero `Policy` → empty (a role
    with no order has nothing unlisted).
  - `TestPolicyWarningsMatrix`: `policy.Policy{Order: map[string][]string{
    "builder": {testAgyRef, "claude/test/nope"},
    "reviewer": {testAgyRef}}}` → exactly these, in this order:
    1. `{Role: "builder", Index: 1, Token: "claude/test/nope", Text:
       `order.builder[1] "claude/test/nope" is not a configured candidate`}`
    2. `{Role: "builder", Index: -1, Token: testClaudeRef, Text:
       `builder: claude/test/m serves the role but is not in order.builder`}`
    3. `{Role: "builder", Index: -1, Token: testOpencodeRef, Text:
       `builder: opencode/test/m serves the role but is not in order.builder`}`
    4. `{Role: "reviewer", Index: 0, Token: testAgyRef, Text:
       `order.reviewer[0] "agy/test/m" does not serve reviewer (its roles: [builder])`}`
    5. `{Role: "reviewer", Index: -1, Token: testClaudeRef, Text:
       `reviewer: claude/test/m serves the role but is not in order.reviewer`}`

    Roles in `harness.RoleNames()` order; within a role, order entries by
    index, then unlisted by ref order.

  Run: `go test ./internal/relay/ -run PolicyWarnings`. Expected: FAIL to
  compile.

- [ ] **Step 2: `PolicyWarnings`** (spec §4.6)

  Create `internal/relay/policy_view.go`. Doc comment on the function: the
  cross-file check `policy.Load` deliberately does not do (spec §3.1);
  tolerated at resolve time, reported here, rendered by `relay policy` and
  `doctor`.

  ```
  for role in harness.RoleNames():
      order := pol.Order(role); if len(order) == 0: continue
      listed := map[string]bool
      for i, tok in order:
          ref, err := ParseRef(tok)          -- cannot fail after Load; treat as "not configured" if it does
          c, err := set.Lookup(ref)
          if err: warn{role, i, tok, `order.<role>[<i>] "<tok>" is not a configured candidate`}; continue
          if !c.Serves(role): warn{role, i, tok, `order.<role>[<i>] "<tok>" does not serve <role> (its roles: <c.Roles>)`}; continue
          listed[tok] = true
      for c in set.ForRole(role):
          tok := c.Ref().String()
          if !listed[tok]: warn{role, -1, tok, `<role>: <tok> serves the role but is not in order.<role>`}
  ```

  `%v` of `c.Roles` renders `[builder]`, matching the test.

  Run: `go test ./internal/relay/ -run PolicyWarnings`. Expected: green.

- [ ] **Step 3: the failing `FormatPolicy` tests** (spec §4.7)

  In `policy_view_test.go`. Gates use `until := baseTime.Add(10 *
  time.Minute)` and expected text is built with `GateUntilText(until)` --
  never a literal clock time. Compare whole strings with a `want` built
  by `strings.Join([]string{...}, "\n") + "\n"` so a column drift is
  visible in the diff.

  Each test calls `FormatPolicy(candidateSet(t, body), pol, gates)`.

  - `TestFormatPolicyOrderWithGatedFirst`: testCandidatesJSON,
    `orderOf("builder", testAgyRef, testClaudeRef)`, gates
    `[{Token: testAgyRef, Kind: SpawnFailed, Until: until}]`. Want:
    ```
    builder  (order set in ~/.config/relay/policy.json)
      1  agy/test/m       order     spawn failed <untilText>
      2  claude/test/m    order     <- would pick
      3  opencode/test/m  unlisted
    reviewer  (no order set)
      1  claude/test/m    sole      <- would pick
    researcher  (no order set)
      no candidate serves this role

    warnings
      builder: opencode/test/m serves the role but is not in order.builder
    ```
  - `TestFormatPolicyNoOrderTwoServeRefuses`: testCandidatesJSON, zero
    policy, nil gates. Want the builder block:
    ```
    builder  (no order set)
      1  agy/test/m
      2  claude/test/m
      3  opencode/test/m
      would refuse: 3 candidates serve builder and no order is set
    ```
    (the tag column is blank: no order, more than one), then reviewer as
    above, researcher as above, then the footer line
    `no policy configured; write ~/.config/relay/policy.json (see README "Policy")`
    and no `warnings` block.
  - `TestFormatPolicyAllGated`: `orderOf("builder", testAgyRef,
    testClaudeRef, testOpencodeRef)`, one `RateLimited` gate per token
    (zero `Until`). Want each builder row to end `rate-limited until
    cleared` with no marker, then
    `  would refuse: every candidate serving builder is gated`.
  - `TestFormatPolicyEmptySet`: `candidateSet(t, "[]")`, any policy →
    exactly `no candidates configured; write ~/.config/relay/candidates.json (see README "Candidates")\n`.
  - `TestFormatPolicySoleWithOrder`: body with only the claude candidate,
    `orderOf("reviewer", testClaudeRef)` → the reviewer row's tag is
    `sole` (not `order`) and marked `<- would pick`.

  Column rules, so the expectations above are reproducible: rows are
  `"  %d  %-*s  %-8s  %s"` with the token width = longest token in
  `set.Refs()` (one width for the whole listing, so the columns line up
  across roles), then the row is `strings.TrimRight`-ed so a blank tag or
  blank tail leaves no trailing spaces. The tail is the gate texts joined
  by `; `, then (when marked) `<- would pick` -- if both, gate text first,
  then two spaces, then the marker (this combination cannot occur: a
  gated row is never picked; document it and move on).

  Run: `go test ./internal/relay/ -run FormatPolicy`. Expected: FAIL to
  compile.

- [ ] **Step 4: `FormatPolicy`** (spec §4.7, §5.4)

  ```
  if set == nil || set.Len() == 0: return the ErrNoCandidates listing text (same as FormatCandidates)
  byToken := gates grouped by Token
  for role in harness.RoleNames():
      serving := set.ForRole(role)
      ordered := len(pol.Order(role)) > 0
      header := role + ("  (order set in ~/.config/relay/policy.json)" | "  (no order set)")
      if len(serving) == 0: header; "  no candidate serves this role"; continue
      rows := ordered ? rankedList(set, pol, role) : serving as rankedEntry{How: ""}
      res, err := resolveCandidate(set, pol, gates, "", role)
      for i, r in rows:
          tag := len(serving) == 1 ? "sole" : string(r.How)     -- "" when no order and >1
          tok := r.Candidate.Ref().String()
          tail := gate texts for tok (byToken); if err == nil && tok == res.Token(): tail += "<- would pick"
          row(i+1, tok, tag, tail)
      switch:
          errors.Is(err, ErrAmbiguousCandidate): "  would refuse: %d candidates serve %s and no order is set"
          errors.Is(err, ErrAllGated):           "  would refuse: every candidate serving %s is gated"
  if warnings := PolicyWarnings(set, pol); len(warnings) > 0:
      blank line; "warnings"; "  " + w.Text per warning
  if len(pol.Order) == 0:
      "no policy configured; write ~/.config/relay/policy.json (see README \"Policy\")"
  ```

  Doc comment: what the resolver would do right now, per role, computed
  by calling it -- the marker cannot disagree with `bind` (spec §4.7).

  Run: `go test ./internal/relay/`. Expected: green. Adjust only the
  formatter until the `want` strings in step 3 match; do not edit the
  wants.

- [ ] **Step 5: `relay policy`** (spec §4.9)

  `cmd/relay/main.go`:
  - `cmdPolicy(args []string) error`: a `flag.NewFlagSet("policy",
    flag.ContinueOnError)`, `parseFlags`, `newRuntime`, then
    `fmt.Print(relay.FormatPolicy(rt.Candidates, rt.Policy,
    relay.Gates(rt)))`; return nil. Mirror `cmdCandidates`.
  - dispatch: `case "policy": return cmdPolicy(args[1:])` after
    `candidates`.
  - `usage`: after the `candidates` line, aligned the same way:
    `policy       show, per role, which candidate relay would pick right now and why`.

  Run: `go build ./... && go vet ./...`. Expected: clean. No subcommand
  test.

- [ ] **Step 6: `doctor` rows** (spec §4.8)

  `cmd/relay/doctor.go`, beside `ledgerChecks`:

  ```go
  // policyChecks turns policy/candidates inconsistencies into doctor rows.
  // Warnings, not failures: an unlisted candidate is a degraded order, not
  // a broken machine (spec §4.8).
  func policyChecks(warnings []relay.PolicyWarning) []doctor.Check
  ```

  One `doctor.Check{Group: "", Name: "policy", Severity: doctor.SevWarn,
  Detail: w.Text, Fix: "edit ~/.config/relay/policy.json"}` per warning,
  in order. In `cmdDoctor`, after the `ledgerChecks` append:
  `rep.Checks = append(rep.Checks, policyChecks(relay.PolicyWarnings(rt.Candidates, rt.Policy))...)`.

  Test in `cmd/relay/doctor_test.go`, `TestPolicyChecks`, mirroring
  `TestLedgerChecks`: two hand-built `relay.PolicyWarning{Text: ...}`
  values → two checks with `Group == ""`, `Name == "policy"`, `Severity
  == doctor.SevWarn`, `Detail == Text`, `Fix == "edit
  ~/.config/relay/policy.json"`; `policyChecks(nil)` is empty. Pure --
  it does not reach herdr.

  Run: `go test ./cmd/relay/ -run PolicyChecks`. Expected: green.

- [ ] **Step 7: `make check`, commit**

  ```bash
  git add -A internal cmd
  git commit -m "feat(relay): relay policy shows the pick per role; doctor warns on policy/candidates drift (#61 step 2)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, the list of
test functions added, and the full output of `FormatPolicy` for the
`TestFormatPolicyOrderWithGatedFirst` fixture (paste the `want` string)
so the planner can eyeball the layout.
