# Plan: upgrade handoff, round 3: existing installs must auto-update too (#371)

**Read first:** `docs/specs/2026-09-23-upgrade-handoff-design.md` §4.10 "Roles" in your tree, and round 2's code in `internal/harness/install.go` and `manifest.go`. This round amends §4.10. Where this plan and the spec disagree about role *identification*, this plan wins. On everything else, the spec wins.

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, report which step and why, and create the done marker. Don't bend a test to fit.

## 1. System overview

Round 2's manifest answers "unchanged since relay wrote it" only for files written *after* round 2 ships. Every existing install has no manifest, so its old shipped copies read as user edits and are never updated.

The planner verified this on this machine. `relay agent install --dry-run` on the branch build shows every `architect` and `plan-executor` definition under `~/.claude/agents`, `~/.codex`, `~/.config/opencode/agents` and `~/.gemini/config/agents` as "kept (differs)". `diff` shows those are **older shipped versions** (the architect lacks the "8. Working Efficiently" section), not edits. The doctor row calls them "edited by you (kept)", which is wrong.

The fix: relay ships the sha256 of **every version of every definition it has ever shipped**, as an embedded file generated from git history. `installOne` treats an on-disk file whose sha is in that set as "a copy relay shipped, unmodified", exactly like a manifest match. The doctor and verb texts stop claiming the user edited a file unless relay really can't recognise it.

## 2. File structure

```
internal/harness/agents/shipped.sha256          NEW  generated; lines "<sha256-hex>  <file name in agents/>" sorted, unique; embedded
internal/harness/shipped.go (+ shipped_test.go) NEW  //go:embed of shipped.sha256; ShippedBefore(doc string, sha string) bool
internal/harness/install.go (+ test)            decision table: manifest match OR ShippedBefore(...) -> Updated/WouldUpdate
internal/doctor/doctor.go (+ test)              texts: see §4
scripts/agents-shipped.sh                       NEW  generator: --write (regenerate from full git history) and --check
scripts/agents-shipped_test.sh                  NEW  exercises --check against the committed file (make check runs scripts/*_test.sh)
```

## 3. Data structures

**`shipped.sha256`**
- One line per distinct historical blob: `<64 lowercase hex>  <basename>`, where the basename is the file's *current* name under `internal/harness/agents/` (e.g. `architect.claude.md`).
- Sorted, with no duplicates. It is append-only in spirit: `--write` produces the union of the existing file and the history.
- For each blob, hash **both** the raw bytes and the bytes with trailing ASCII whitespace trimmed and one `\n` appended, if the two differ. Then an installed copy written by any past relay matches whether or not that relay normalised line endings.

**The in-memory index:** `map[string]map[string]struct{}`, basename to a set of shas, built once with `sync.Once`.

## 4. Interfaces and contracts

- `func ShippedBefore(doc, sha string) bool`: `doc` is the embedded basename the role reads (`r.Doc + "." + ext`, as `agents.go:32` composes it).
- installOne's decision table, where the manifest row gains an alternative. The order:
  1. missing → write;
  2. `DocEqual` → KeptIdentical;
  3. `manifest[p] == sha(existing)` **or** `ShippedBefore(doc, sha(existing))` → Updated or WouldUpdate;
  4. Force → Overwrote or WouldOverwrite;
  5. else → KeptDiffers.

  The manifest is recorded exactly as round 2 does.
- **What gets hashed.** If `installOne` writes something other than the raw embedded bytes (a rendered or substituted form, e.g. a model line for agy), `ShippedBefore` can only match raw historical blobs. Say so in the report, and list which kinds are affected. Don't try to re-render history.
- **Texts:**
  - `KeptDiffers` in the verb stays `kept (differs; --force to overwrite)`.
  - The doctor roles row for KeptDiffers becomes: detail `differs from every copy relay has shipped (kept as your edit)`, severity OK.
  - The WouldUpdate text is unchanged: `role definitions are stale; the daemon refreshes them on its next start, or run relay agent install`.
- **`scripts/agents-shipped.sh`** (POSIX sh, shellcheck-clean):
  - `--write`: for each file in `internal/harness/agents/`, run `git log --follow --format=%H --name-only -- <file>` to get every (commit, path-at-that-commit) pair, hash `git show <commit>:<path>` both ways (§3), hash the working-tree file both ways, union with the existing lines, sort -u, and write the result.
  - `--check`: recompute the working-tree files' hashes only, and exit 1 naming any file whose current hash is missing from `shipped.sha256`, with the fix `sh scripts/agents-shipped.sh --write`. This needs no git history, so it works in any checkout.
  - Hashing uses `sha256sum` on Linux, falling back to `shasum -a 256` on macOS.
- `scripts/agents-shipped_test.sh`: runs `--check` and passes when it exits 0.

## 5. High-level pseudocode

```
install flow per role file:
  existing := read
  sha := docSHA(existing)
  if missing -> write
  elif DocEqual(shipped, existing) -> KeptIdentical; manifest[p] = sha
  elif manifest[p] == sha || ShippedBefore(doc, sha) -> (DryRun ? WouldUpdate : write -> Updated); manifest[p] = docSHA(shipped)
  elif Force -> ...
  else -> KeptDiffers
```

## 6. Error handling strategy

- A malformed `shipped.sha256` line is skipped. A test asserts the committed file has none.
- An empty index means today's round-2 behaviour. Never an error at runtime.

## 7. Ordered implementation steps

**Step 1: the generator.** Write `scripts/agents-shipped.sh`, run `--write`, and commit the generated `shipped.sha256`. Sanity check in the report: the number of lines, and that the file contains the sha of the planner's installed `~/.claude/agents/architect.md` (compute it with `sha256sum ~/.claude/agents/architect.md`; reading it is fine). If it **isn't** in the set, halt and report both hashes plus `git log --follow --oneline -- internal/harness/agents/architect.claude.md`. That would mean the installed copy isn't a shipped blob, and the premise is wrong.

**Step 2: `shipped.go` and `ShippedBefore`**, with tests:
- a known historical blob's sha matches;
- a random sha doesn't;
- every line of the committed file parses (no malformed lines).

**Step 3: the install decision.** Table tests:
- a file whose content is an older shipped blob, with **no manifest**: Updated, or WouldUpdate under DryRun;
- a file with an arbitrary edit: KeptDiffers.

Mutation: drop the `ShippedBefore` clause, and the first test fails.

**Step 4: doctor text** and its test.

**Step 5: the script test.** `scripts/agents-shipped_test.sh`. Confirm that `make check` runs it. Negative case: append a byte to a copy of one definition in a temp dir and run `--check` against a temp copy of the tree layout, if the script can be pointed at a root (add a `--root DIR` option if needed); it must exit 1.

**Step 6: live dry run** (read-only). `go run ./cmd/relay agent install --dry-run` in your tree. The files that were `kept (differs)` now read `would update`, except any that really are edits. Paste the output into the report. **Don't run it without `--dry-run`**: it would write into the planner's real home.

**Step 7: gate.** `make check` and `make e2e`. Commit ending `(#371)`.

Declared scope: §2's files.
