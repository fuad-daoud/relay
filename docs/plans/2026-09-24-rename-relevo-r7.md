# Plan: #292 round 7: the config merge treats a file that differs only by the slice rename as identical

Found while the planner walked through contabo's cutover:

1. contabo's `srv install` seeds `~/.config/relevo/{candidates,policy}.json` on the
   box from the laptop's **already migrated** config. The laptop's `policy.json`
   now says `"slice": "relevo.slice"`, because `rename-slice` rewrote it.
2. The box's old `~/.config/relay/policy.json` is byte-identical to the laptop's
   old one: `"slice": "relay.slice"`.
3. The config merge (round 3a, `internal/migrate`) sees two different
   `policy.json` files and refuses. `relevo-serve`'s `ExecStartPre=relevo migrate`
   then fails, and so does the deploy.

The merge is right to refuse real conflicts. But a file whose only difference is
the exact rewrite `rename-slice` would make anyway is not a conflict: migrate's
own result would be identical.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 1. Change (`internal/migrate`)

1. Add `func normalizeLegacy(b []byte) []byte`, which replaces every exact
   `"` + `legacy.Slice` + `"` with `"relevo.slice"`. It's the same byte rule
   `RenameSliceValue` uses. Reuse a shared helper if `units.go` already has one;
   otherwise put it in `detect.go` and have `RenameSliceValue` call it, so the rule
   lives in one place.
2. Wherever the config merge compares an old entry with the new one, compare
   `normalizeLegacy(old)` with `new`, not raw bytes. That's the refusal check in
   `detect.go` and the defensive re-check in `moveRoot` (`migrate.go` ~356,
   `sameBytes`).
   - If equal, the old copy is dropped as today. The new file already holds the
     normalized content.
   - Only the comparison changes. The move and drop logic stay as they are.
3. **Scope:** the comparison only. Nothing else normalizes, and state files are
   not merged at all.

## 2. Tests (`internal/migrate/migrate_test.go`, beside `TestRunConfigMerge`)

Add a subtest: ConfigTo holds `policy.json` with `"slice": "relevo.slice"`, and
ConfigFrom holds the same bytes except `"slice": "relay.slice"`. `Run` succeeds,
ConfigTo's `policy.json` is unchanged (it still says `relevo.slice`), and
ConfigFrom is gone.

Mutation: compare raw bytes again, and the subtest must fail. The existing
"differs → refuses" subtest must still pass. Build the legacy bytes from
`legacy.Slice`, not a literal (round 4's guard).

## 8. Working efficiently

- **Focused:** `go test -count=1 ./internal/migrate/`, `sh scripts/check-name.sh`.
- **Once at the end:** `make check`.

## 9. Steps

1. §1.
2. §2 and the mutation.
3. `make check` must pass. One commit:
   `fix(migrate): a config file that differs only by the slice rename merges (#292)`.
   Don't push.

**Declared scope:** `internal/migrate/detect.go`, `internal/migrate/migrate.go`,
`internal/migrate/units.go` (only if the shared helper moves there),
`internal/migrate/migrate_test.go`.

**Report:**
- the mutation;
- `git diff --stat HEAD~1`;
- the result of `make check`.
