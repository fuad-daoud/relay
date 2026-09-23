# Plan: #292 round 4: the name guard, plugin 0.12.0, and a prose pass

Spec: `docs/specs/2026-09-23-rename-relevo-design.md` §4. Rounds 1-3b are on this
branch. This is the last code round of the rename.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 1. System overview

Three things close the rename:

1. **A guard in `make check`.** It fails when a `relay` name comes back into the
   live tree. In-flight branches written against the old names would otherwise
   merge them back in silently.
2. **Plugin and marketplace version 0.12.0.** Round 1 already renamed them to
   `relevo`. Installed plugins are cached by version, so the bump is what makes a
   reinstall pick up the new one.
3. **A short prose pass** over anything the sweep made read wrongly.

## 2. File structure

```
scripts/check-name.sh         NEW  the guard
scripts/check-name_test.sh    NEW  its test (make check runs scripts/*_test.sh)
Makefile                      EDIT check: run `sh scripts/check-name.sh` right after check-plugin-version.sh (~line 26)
claude-plugin/.claude-plugin/plugin.json   EDIT "version": "0.12.0"
.claude-plugin/marketplace.json            EDIT "version": "0.12.0"
<files the guard flags>       EDIT only to add an allow marker (§4), never to change behaviour
README.md, CONTRIBUTING.md, CLAUDE.md      EDIT prose pass (§5), wording only
```

## 3. The guard's contract (`scripts/check-name.sh`)

- **POSIX sh**, shellcheck-clean, run from the repo root. It uses `git grep`, so
  only tracked files are checked.
- **Pattern** (extended regex), exactly the one round 1's leftover scan used:
  `(RELAY|Relay)([^a-z]|$)|(^|[^A-Za-z]|\\[nt])relay([^a-z]|$)`.
- **Excluded paths** (pathspecs):
  - `docs/plans/**`, `docs/specs/**`, `docs/superpowers/**`
  - `go.sum`
  - `internal/harness/agents/shipped.sha256`
  - `internal/legacy/**`
  - `scripts/rename-relevo.sh`, `scripts/check-name.sh`, `scripts/check-name_test.sh`
  - `internal/harness/install_test.go` (its `olderArchitectDoc` is frozen bytes, spec §1 item 5)
- **Allowed lines:**
  - a line containing `relay-site.fuad-daoud.com` (spec §1 item 4);
  - a line containing the marker `name-guard: legacy`;
  - in Markdown, every line between a line `<!-- name-guard: off -->` and the next
    line `<!-- name-guard: on -->`.
- **Output:** each offending `path:line:text`, then
  `check-name: <n> relay name(s) outside the legacy allowlist (docs/specs/2026-09-23-rename-relevo-design.md §4)`,
  then exit 1. Clean → `check-name: ok`, exit 0.
- **Implementation hint:** `git grep -nIE` with the pathspecs, piped through `awk`.
  For the Markdown on/off blocks, it's simplest to pre-compute the off ranges per
  `*.md` file with awk, then filter. Any correct approach is fine.

**`scripts/check-name_test.sh`**, in the style of the existing `*_test.sh`: build
a throwaway git repo in a temp dir, copy the script in, and assert:

- a Go file with `relay-exit:` fails;
- the same line with `// name-guard: legacy` passes;
- `relayd`, `relaying` and `Relayed` pass (no match);
- a README with the hostname passes;
- a Markdown on/off block passes, and the same text outside it fails;
- a file under `internal/legacy/` passes.

## 4. Allow markers: what to do with the guard's first run

Run `sh scripts/check-name.sh` after writing it. Expected hits:

- tests from rounds 2-3b that feed relay-era input on purpose (literal
  `relay-exit:`, `relay-rusage:`, `"source": "relay"`, relay-era dirs);
- the README's "Upgrading from relay" section.

For each hit:

- **A test feeding legacy input.** Prefer switching the literal to the
  `internal/legacy` constant. Where the literal is inside a multi-line fixture
  string, or the test is pinning the literal bytes, add `// name-guard: legacy` to
  that line instead.
- **The README section.** Wrap it in `<!-- name-guard: off -->` /
  `<!-- name-guard: on -->`.
- **Any other hit** is a `relay` the earlier rounds missed. That includes a
  non-test Go file with a literal old name that should come from `internal/legacy`.
  Fix it by using the constant, **and list it in the report**. If a hit is
  something you can't classify, halt.

The guard must end clean.

## 5. Prose pass (wording only)

Read the top ~120 lines of `README.md`, all of `CONTRIBUTING.md` and all of
`CLAUDE.md`, and fix only sentences the mechanical rename made wrong or
ungrammatical. Examples:

- an article before a vowel ("a relevo" is fine, but check "an relevo");
- "relevo" used as a verb, meaning *to relay* something (use "hand over" or
  "pass on");
- a sentence explaining the name's meaning.

Also add one line near the top of `README.md`: *relevo is Spanish for relay (the
changeover in a relay race); it was called relay until v0.12.0.* Put it inside
`<!-- name-guard: off -->` / `<!-- name-guard: on -->`.

Change nothing else. If more than ~15 sentences seem to need changes, stop at 15 and
list the rest in the report.

## 6. One small fix from round 3b: the `rename-slice` dry run

In a dry run the config root has not moved yet, so
`migrate.RenameSliceValue(<ConfigTo>, true)` reports `no policy.json` even when
the real run will rewrite it. Round 3b's report flagged this.

Fix it in `cmd/relevo/migrate.go`, where rename-slice is called: in a dry run,
when `<ConfigTo>/policy.json` does not exist but `<ConfigFrom>/policy.json` does,
pass `ConfigFrom` so the step reports what the real run will do.

Test it in `cmd/relevo/migrate_test.go`, in the existing dry-run test: the temp
tree's old `policy.json` holds `"slice": "relay.slice"`, and the dry-run output's
rename-slice line says it would rename, not `no policy.json`. Mutation: revert
the fix and that assertion fails.

## 8. Working efficiently

- **Focused loop:** `sh scripts/check-name.sh`, `sh scripts/check-name_test.sh`,
  `shellcheck scripts/check-name*.sh`.
- **Full check, once at the end:** `make check`.

## 9. Ordered implementation steps

1. **The guard.** Write `scripts/check-name.sh` and `scripts/check-name_test.sh`.
   Done when the test passes and shellcheck is clean.
2. **Clean the tree.** Run the guard on the tree and apply §4 until it's clean.
   Keep a list of every file you touched and why.
3. **Makefile.** Wire the guard into `check` (after `check-plugin-version.sh`).
4. **Versions.** Set 0.12.0 in both manifests, then run `sh
   scripts/check-plugin-version.sh`.
5. **Prose pass** (§5).
5b. **The §6 dry-run fix,** with its test and mutation.
6. **Mutation check.** Put a bare `relay` into a non-test Go comment, see `make
   check`'s guard step fail, then remove it.
7. **Full check and commit.**
   - `make check` must pass.
   - One commit: `chore(rename): a make check guard against relay names, plugin 0.12.0, prose (#292)`.
   - Don't push.

**Declared scope:** §2's files, plus `cmd/relevo/migrate.go` and `cmd/relevo/migrate_test.go` (§6), plus test files and the README section given allow
markers or legacy constants under §4.

**Report:**
- per-step status;
- the §4 list (file, line, marker or constant or fix);
- the prose changes;
- the mutation check;
- `git diff --stat HEAD~1`;
- the result of `make check`.
