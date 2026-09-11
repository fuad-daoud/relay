# agy tools allowlist, round 2: narrow the drift check, then commit (#91, #94)

**Round 1 plan:** `docs/plans/2026-09-11-agy-tools-allowlist.md` -- steps 1, 2, 4
stand as built. Do not revisit them.
**Round 1 report:** both concerns accepted. This round resolves them by
**narrowing step 3's rule** (your option 2, refined), not by touching
`cmd/relay/doctor_test.go`. The tree is still uncommitted; leave every
round-1 edit in place except where a step below names it.

Same worktree, same constraints as round 1 (`make check` is the gate; no
`cmd/relay` test; commit at the end, no push, no PR).

## The decision

The README tells claude and opencode users to edit `model:` in their
installed copy. A kind-agnostic byte comparison turns that documented
customisation into a permanent warning whose fix overwrites it. That was a
design error in round 1's step 3, not in your implementation of it.

The drift check applies only where relay owns the installed file: kinds
whose role entry carries `ExpectModel` (today: agy), where any edit is
already a warn because the pin must be `inherit`. On every other kind the
installed definition is the user's to customise and doctor says nothing
about its contents beyond what it said before this change.

## Step 1: gate the drift check on `r.ExpectModel != ""`

**File:** `internal/doctor/doctor.go`

In `roleCheck`, wrap the block you added (`if shipped, shipErr :=
harness.AgentDoc(r.Name, kind); shipErr == nil { ... }`) so it runs only
when `r.ExpectModel != ""`. Add a comment above it:

```go
// Drift from the shipped bytes is only a finding on a kind whose
// definition relay owns outright (ExpectModel set: the pin must be
// inherit, so any edit is already wrong). Elsewhere the README invites
// the user to repin model:, and a warning whose fix overwrites that
// edit would be worse than silence. #91 is the case this catches:
// an agy copy that predates a tools: fix starts a builder that
// cannot build, and nothing else on this machine notices.
```

No other change to `doctor.go`. Detail and Fix text stay as built.

## Step 2: tests reflect the narrowed rule

**File:** `internal/doctor/doctor_test.go`

- `TestDoctorClaudeRoleNeverWarnsOnAPin`: restore its original meaning. A
  claude definition with a hand-edited `model:` yields `SevOK`, not the
  drift warn. Replace the round-1 comment explaining the new behaviour with
  one sentence saying this test pins the decision that non-agy definitions
  are the user's to edit.
- `TestDoctorRoleDriftFromShipped`: keep the four subtests. Add a fifth,
  `"a claude definition that differs from shipped is still OK"`: installed
  bytes = shipped `plan-executor.claude.md` plus one extra line ->
  `SevOK`. This is the test the gate in step 1 is for.
- The `agyEnv()` / `TestDoctorEmitsOneRowPerRole` /
  `TestDoctorOmitsModelSuffixWhenUnpinned` fixture changes from round 1 are
  fine to keep as they are; building fixtures from `shippedDoc` is
  strictly more faithful. Do not revert them.

**Verify:** `go test ./internal/doctor/` passes. Mutation: delete the
`r.ExpectModel != ""` guard -> the new fifth subtest fails and
`TestDoctorClaudeRoleNeverWarnsOnAPin` fails; restore the guard. Then
`go test ./cmd/relay/` passes (`TestBindWarningLinesSilentWhenNothingIsWrong`
runs doctor for `claude` only, so it is green without any edit to that
file -- confirm, do not edit it).

## Step 3: docs say what the rule is

**Files:** `docs/specs/2026-09-11-agy-role-definitions-design.md`, `README.md`

- Spec §7.4, the last bullet ("`relay doctor` warns when an installed
  definition differs from the shipped one, ..."): append ", on agy only --
  a kind whose definition relay owns because `model:` must stay `inherit`.
  On claude and opencode the installed copy is the user's to edit (the
  README tells them to repin `model:`), and doctor does not compare it."
- Spec §8, the doctor drift bullet you added: mention the fifth subtest
  (claude differing -> OK) and its mutation (drop the `ExpectModel` guard).
- README, the sentence you appended in round 1 ending "`relay doctor`
  warns when the installed copy differs from it": change the tail to
  "`relay doctor` warns when the installed agy copy differs from it (on
  claude and opencode the copy is yours to edit, and doctor leaves it
  alone)."

**Verify:** `make check` passes -- all of it, including `cmd/relay`.

## Step 4: commit

One commit, as round 1's step 5 specified:

```
fix: agy definitions carry verified tools allowlists -- plan-executor can write, researcher/reviewer start; doctor warns on agy drift (#91, #94)

On agy 1.2.1 a definition with no tools: gets a read-mostly default with no
write, shell or invoke_subagent, and one naming a tool the registry lacks
does not start. All three agy definitions now carry an explicit, verified
list; relay doctor warns when an installed agy copy differs from shipped.

Closes #91
Closes #94
```

Report the `git log -1 --stat` output.
