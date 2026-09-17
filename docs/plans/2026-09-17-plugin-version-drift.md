# Plugin manifest drift guard: `make check` fails when `main` outruns the release (#164)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #164, fix option 3. The release half (option 1) is done: `v0.2.0`
was cut on 2026-09-17 and `main` is at the release commit. This plan is the
guard that stops the drift from silently growing to 100 commits again.
**Depends on:** nothing open.

**Goal:** `scripts/check-plugin-version.sh`, which `make check` already runs,
fails when the commit under test carries more than 10 `feat:` commits past
the tag the plugin manifest names. The message says the count and the fix.
CI is given enough history to make the check real.

**Architecture:** The script gains one rule after its existing two
(manifests agree; manifest matches the tag argument). It resolves the tag
`v<manifest version>`; if that tag is not resolvable (shallow clone, fork,
not a git checkout, or the release commit itself -- `make release` runs
`make check` after bumping the manifests and *before* creating the tag) it
prints a one-line skip notice and passes. Otherwise it counts first-parent
commits from the tag to `HEAD` whose subject is Conventional-Commits `feat`
and fails past the threshold. `ci.yml` fetches full history on the job that
runs `make check` so the tag is resolvable there. The sh test gains the
three drift cases in throwaway git repositories.

**Tech stack:** POSIX sh (`set -eu`, shellcheck-clean; `make check` lints
`scripts/*.sh`). Verification is the `make check` constituent set.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/plugin-version-drift` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/plugin-version-drift`, cut from `main`. |
| `~/.local/state/relay/plugin-version-drift` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent behaviour that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` that this change touches directly, and say so
in your report:

```bash
sh scripts/check-plugin-version.sh
shellcheck scripts/*.sh
for t in scripts/*_test.sh; do echo "==> $t"; sh "$t"; done
```

Go is untouched by this plan; `gofmt`, `go vet`, `go test` and the tidy check
do not need to run. Do **not** run `herdr`, `agy`, `claude` or `opencode`
yourself. **No test is added under `cmd/relay`** (CI runners have no `herdr`;
CLAUDE.md). Nothing here reaches herdr.

## Global constraints

- The threshold is one named constant near the top of the script,
  `max_feat_drift=10`, with a comment saying what it bounds. No flag, no
  environment variable overrides it.
- The tag the drift is measured from is `v` + the version in
  `herdr-plugin.toml` (already parsed into `v1` by the script). Resolve it as
  a **tag ref** (`refs/tags/v<version>`), quietly, so a branch with the same
  name is not mistaken for it and a missing tag prints nothing of its own.
- Any failure to resolve that tag -- no such tag, not a git repository, a
  shallow clone without tags -- is the skip path. The skip path prints
  exactly one line to stderr, `check-plugin-version: tag v<version> not
  found; skipping drift check`, and does not change the exit status.
- Drift is counted on the **first-parent** chain from the tag to `HEAD`, by
  commit subject, matching the extended regex `^feat(\([^)]*\))?!?:`. Merge
  commits' own subjects are on that chain and do not match; commits on a
  merged branch's second parent are not on it and are not counted. That is
  what makes the count identical on a `pull_request` merge ref and on the
  squash commit that lands on `main`.
- A count of 0 is a pass and must not trip `set -e`: `grep -c` exits 1 when
  it counts nothing. Handle that explicitly.
- Past the threshold the script prints to stderr, on one line,
  `check-plugin-version: <N> feat commits since v<version> (limit 10); cut a
  release: make release VERSION=<next>` and exits 1.
- The existing behaviour is unchanged: the two manifest rules run first, in
  the same order, with the same messages; the optional tag argument still
  means what it means; a mismatch there still fails before the drift rule.
- Throwaway repositories in the test set `-c user.name`, `-c user.email`,
  `-c commit.gpgsign=false` and `-c tag.gpgsign=false` on every `git commit`
  and `git tag` (or export the equivalent `GIT_*` environment once for the
  test), because this machine's global git config signs both, and CI has no
  key. Commits are `--allow-empty` and quiet.
- The test keeps its shape: `stage`, `check`, `fail`, the `ok` line at the
  end. Existing cases stay and keep passing; the existing `stage` output is
  not a git repository, which now exercises the skip path for free.
- One commit per task, on the worktree's branch. Commit subjects use the
  `ci:` type -- this plan adds no `feat:` commit, so the branch's own drift
  count stays 0.

---

### Task 1: the drift rule in `check-plugin-version.sh`, test first

**Files:**
- Modify: `scripts/check-plugin-version_test.sh`
- Modify: `scripts/check-plugin-version.sh`

**Interfaces:**
- Produces: the drift rule described under Global constraints, appended
  after the existing tag-argument check in `scripts/check-plugin-version.sh`.
- Produces (test-private): `stage_repo <version> <feat-count>` -- like
  `stage`, but the fake repo root is a `git init`ed repository whose first
  commit carries the tag `v<version>` and which then has `<feat-count>`
  further empty commits with subjects `feat: n`.

- [ ] **Step 1: Add the drift cases to the test.**

Add `stage_repo` beside `stage`. Pseudocode:

```
stage_repo VERSION FEATS:
    stage VERSION VERSION            # manifests agree, script copied in
    in $work/repo:
        git init, quiet
        commit "chore: release vVERSION" (empty, unsigned)
        tag vVERSION                 (unsigned, lightweight is fine)
        repeat FEATS times: commit "feat: <i>" (empty, unsigned)
```

Then, after the existing cases, add:

```
stage_repo 1.2.3 10
check "drift at the limit" 0

stage_repo 1.2.3 11
check "drift past the limit" 1

stage_repo 1.2.3 11
in $work/repo: git tag -d v1.2.3
check "tag absent, drift would be past the limit" 0
```

The third case is the one that pins the skip path on a *real* repository:
without it, "tag not found" and "count is 0" are indistinguishable.

Add one more case that pins first-parent counting, so a future rewrite that
uses `git log` without `--first-parent` fails here rather than in CI:

```
stage_repo 1.2.3 5
in $work/repo:
    create branch `side` from HEAD with 6 empty "feat: side <i>" commits
    checkout the first branch again; merge side with --no-ff, subject "Merge side"
check "feats behind a merge are not counted" 0
```

(5 first-parent feats + a merge commit = 5, under the limit; the 6 on the
second parent would push it to 11 if they were counted.)

- [ ] **Step 2:** run `sh scripts/check-plugin-version_test.sh`. The new
"drift past the limit" case must FAIL (the script has no drift rule yet and
exits 0); the others pass or fail incidentally. Record the output.

- [ ] **Step 3: Implement the rule.**

Append to `scripts/check-plugin-version.sh`, after the tag-argument block.
Pseudocode:

```
max_feat_drift=10        # near the top, with its comment

tag="v$v1"
if tag ref refs/tags/$tag does not resolve (quietly; any git error counts):
    stderr "check-plugin-version: tag $tag not found; skipping drift check"
    exit 0

subjects = first-parent commit subjects in $tag..HEAD
n = number of subjects matching ^feat(\([^)]*\))?!?:   (0 when none; do not let grep's exit 1 abort)
if n > max_feat_drift:
    stderr "check-plugin-version: $n feat commits since $tag (limit $max_feat_drift); cut a release: make release VERSION=<next>"
    exit 1
```

Update the header comment of the script: it now states all three rules and
says, in one sentence, why the release commit passes (the tag does not exist
yet when `make release` runs `make check`).

- [ ] **Step 4:** run the three commands under "Running commands". All green.
`sh scripts/check-plugin-version.sh` from the worktree prints nothing (the
worktree's `HEAD` is at or just past `v0.2.0`; count is 0) -- confirm that
and record it.

- [ ] **Step 5: Mutation checks.** Each is a temporary edit, reverted after:
  1. Change `-gt "$max_feat_drift"` (or the equivalent) to `-ge`: "drift at
     the limit" must fail.
  2. Drop `--first-parent`: "feats behind a merge are not counted" must fail.
  3. Make the skip path `exit 1`: "tag absent" and the pre-existing cases
     that expect 0 (they run in a non-git directory) must fail.
  Record before/after for each.

- [ ] **Step 6: Commit**

```bash
git add scripts/check-plugin-version.sh scripts/check-plugin-version_test.sh
git commit -m "ci: fail make check when main is more than 10 feats past the plugin release (#164)"
```

---

### Task 2: CI fetches the history the rule needs

**Files:**
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1:** In the `check` job only, give the `actions/checkout@v4`
step `with: fetch-depth: 0`, and a comment above it: the plugin-version
drift check in `make check` needs the release tags, and the default depth of
1 has none, which would make the check skip on every run. Leave the
`portability` job's checkout alone (it does not run `make check`). Leave
`.github/workflows/release.yml` alone: it checks out the tag itself, so the
count there is 0 whether the tag resolves or not.

- [ ] **Step 2:** `shellcheck` does not lint YAML; re-run
`sh scripts/check-plugin-version_test.sh` anyway to confirm Task 1 is still
green in the tree you are committing.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: full history on the check job so the drift rule can see tags (#164)"
```

---

## Report

Say, per task: the commit hash; the output of the three "Running commands"
verbatim (last lines); every mutation check's before/after; the Step 2
red-test output from Task 1. List every file you touched outside the plan's
`Files` lists, if any -- there should be none. If the tag-resolution or
first-parent counting could not be expressed within POSIX sh and
shellcheck's rules, stop and say what you hit instead of switching to bash.
