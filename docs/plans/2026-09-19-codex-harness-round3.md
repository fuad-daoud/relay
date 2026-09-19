# codex harness -- round 3: rename the unknown-kind stand-in, then finish steps 9-10

Continues `docs/plans/2026-09-19-codex-harness.md` (base plan) and
`docs/plans/2026-09-19-codex-harness-round2.md` (round 2) from the state
round 2 left. Everything in both still applies except as amended here.

**Before step 1**: `git branch --show-current` prints `relay/codex-harness`;
`git status --short` shows the round 2 state (modified and untracked files
across `internal/harness`, `internal/transcript`, `internal/usage`,
`internal/doctor`, the two plan files and the spec; nothing committed;
`README.md` unmodified). Anything else: halt.

## Amendment D (adds to round 2's A-C)

Until this round, `"codex"` was the repository's stand-in for "a kind
relay does not know". The base plan's §3 made it a known kind, so two
pre-existing tests now test the wrong thing. Both are hereby named as
tests to modify; the change in each is to rename the stand-in to
`"droid"` -- a kind herdr has (`herdr agent start --kind droid`) and relay
does not -- and nothing else:

1. `internal/doctor/doctor_test.go`, `TestDoctorUnknownKindDegradesWithoutFailing`:
   every `codex` in the function body becomes `droid` (the `lookPaths` key
   and value `/usr/bin/droid`, the comment, the `Run` argument, the three
   `findCheck` kinds, the four message strings). The assertions are
   unchanged: role row `SevOK`, integration row `SevOK`.
2. `internal/transcript/transcript_test.go`, the `TestRenderRules` case
   `"unknown kind"`: `{"codex", ...}` becomes `{"droid", ...}`; expected
   output stays `[]string{"[item]"}`.

Add to the plan copy's `## 9. Amendments (round 2)` section (rename its
heading to `## 9. Amendments (rounds 2-3)`) this bullet:

```
- Round 3: `TestDoctorUnknownKindDegradesWithoutFailing` and the
  `"unknown kind"` case of `TestRenderRules` used `codex` as the stand-in
  for a kind relay does not know; both now use `droid`. Round 2 halted on
  the doctor one, correctly.
```

Also commit this file as `docs/plans/2026-09-19-codex-harness-round3.md`
(copy from `~/.local/state/relay/codex-harness/003-plan.md`).

## Steps

1. Apply amendment D. Run `go test ./...` (the whole module, not four
   packages): everything green. If any test other than the two named
   fails, halt and name it.
2. Base plan step 9 (README).
3. Base plan step 10 (Gate) with round 2's amendment C and this round's
   plan-copy edit: `make check` clean; `git status --short` covers only the
   base plan's §2 list plus the three plan files and the spec;
   `go.mod`/`go.sum` unchanged; ONE squashed commit with the base plan's
   subject. The report lists every test written across rounds 1-3 and
   restates the three mutation checks (already run in round 2; do not
   re-run them, cite round 2's results).
