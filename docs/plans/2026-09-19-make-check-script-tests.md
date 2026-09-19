# make check must fail when a script test fails (#197)

Closes #197. Design is the issue body; no separate spec.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. A halt that surfaces a design error is the
wanted outcome; an improvised workaround is not.

**Scope guard.** Touch only the three files listed in §2. Do not change
`scripts/check-plugin-version.sh` (the checker is correct; its test and the
Makefile are wrong). Do not touch any `.go` file, `web/index.html`, or the
other four `scripts/*_test.sh`. Do not run `make e2e`. Do not add a CLI
verb or flag. Run every command in the foreground; dispatch no sub-agents
and start no background work.

**Commit the plan with the work.** Copy this plan file (the path relay gave
you) to `docs/plans/2026-09-19-make-check-script-tests.md` in your worktree
and include it in the first commit.

## 1. System overview

`make check` is relay's verification gate: CLAUDE.md tells the planner to run
it after every builder round instead of trusting the report. Two defects
make it lie, both introduced independently and both reproducible on `main`
at 1eb28e7:

1. `scripts/check-plugin-version_test.sh` stages a fake repo with the two
   manifests but no `web/index.html`. Since #196 the checker also reads the
   version stamped in the site colophon (`<span data-version>vX.Y.Z</span>`),
   so every case that expects exit 0 now gets exit 1 -- five `FAIL:` lines
   and the script exits 1.
2. `Makefile` runs the script tests in a `for` loop whose exit status is
   that of the last command in the last iteration, so a failing script test
   never fails `make check`. That is why CI has been green through (1).

The fix is: make the fixture complete (with a negative case so the site
check is itself pinned), and make the loop propagate the first failure.
Nothing in the checker changes. Every `*_test.sh` already ends with a
non-zero exit on `fail=1` (verified: four use `exit "$fail"`,
`relay-service-template_test.sh` uses `if [ "$fail" -ne 0 ]; then exit 1; fi`),
so the issue's "verify" note on that point is resolved: no change needed
there.

## 2. File structure

```
Makefile                                   edit: script-test loop propagates failure
scripts/check-plugin-version_test.sh       edit: stage() writes web/index.html; new negative case
docs/plans/2026-09-19-make-check-script-tests.md   this plan (copied in, committed with the work)
```

No new files besides the plan copy. No Go changes.

## 3. Data structures & type definitions

None. The only "data" is the fixture text `stage()` writes:

- `web/index.html` fixture content: one line containing exactly the token
  the checker's `sed` expression extracts, i.e. the substring
  `<span data-version>v<VERSION></span>` where `<VERSION>` is the third
  argument to `stage`. Surrounding markup is free but must be a single line
  (the checker's `sed -n 's/.*<span data-version>v\([^<]*\)<\/span>.*/\1/p'`
  is line-oriented). Minimal acceptable content:
  `<p><span data-version>v1.2.3</span></p>`.

## 4. Interface definitions & component contracts

### 4.1 `stage` (edited, `scripts/check-plugin-version_test.sh`)

```
stage <manifest1-version> <manifest2-version> [<site-version>]
```

- Precondition: `$work` exists.
- Postcondition: `$work/repo` is recreated from scratch containing
  `herdr-plugin.toml` at version `$1`, `from-source/herdr-plugin.toml` at
  version `$2`, `web/index.html` whose colophon version is `$3`, and a copy
  of `check-plugin-version.sh`.
- `$3` defaults to `$1` when omitted, so every existing call site
  (`stage 1.2.3 1.2.3`, `stage 1.2.3 4.5.6`, and `stage_repo`'s
  `stage "$1" "$1"`) stays unchanged and produces an agreeing site.
- `stage_repo` is not edited: it calls `stage "$1" "$1"` and inherits the
  default.

### 4.2 New negative case

After the existing `disagreeing manifests` block (line 55) and before
`stage_repo 1.2.3 10`, add:

```
stage 1.2.3 1.2.3 9.9.9
check "site version disagrees with manifests" 1
```

This is the only case where the site version differs; it pins the #196
check so that removing it from the checker fails this test.

### 4.3 Makefile script-test loop (edited, `Makefile` line 32)

Contract: the `check` target exits non-zero as soon as one script test
exits non-zero, and the `==> <path>` banner is still printed before each
test runs. Exact replacement of the loop body's command:

```
sh "$$t" || exit 1
```

so the line reads
`@for t in scripts/*_test.sh; do echo "==> $$t"; sh "$$t" || exit 1; done`.
Because the recipe line runs in one `sh -c`, `exit 1` inside the loop
terminates that shell with status 1, which make reports as a failed recipe.
Do not restructure the loop, add `set -e`, or split it into multiple recipe
lines (a later line would run the next test after a failure, and the
`@` prefix must stay so the banner is the only echo).

## 5. High-level pseudocode

```
stage(v1, v2, v3 = v1):
    rm -rf work/repo
    mkdir -p work/repo/from-source work/repo/web
    write manifest(v1) -> work/repo/herdr-plugin.toml
    write manifest(v2) -> work/repo/from-source/herdr-plugin.toml
    write "<p><span data-version>v" + v3 + "</span></p>" -> work/repo/web/index.html
    copy check-plugin-version.sh -> work/repo/

Makefile check:
    ... existing steps ...
    for each t in scripts/*_test.sh:
        echo "==> t"
        run sh t; if it fails: exit 1   # first failure ends the recipe
```

Default-argument handling in POSIX sh: `site=${3:-$1}` (the script runs
under `set -u`, so `$3` must not be referenced bare when absent;
`${3:-$1}` is safe).

## 6. Error handling strategy

- A script test that fails now fails `make check` with the test's own
  `FAIL:` lines immediately above make's `*** [check] Error 1`. No new
  error text is introduced.
- The negative case relies on the checker's existing message
  `site version disagrees with manifests`; the test only checks the exit
  code (as every other case does), not the message.
- Non-recoverable by design: `make check` has no "warn and continue" mode
  and must not gain one.

## 7. Ordered implementation steps

Run from the worktree root. Steps 1-2 are the fixture; step 3 is the
Makefile; steps 4-5 verify. Do not skip the mutation checks: this bug
existed precisely because a green result was never questioned.

1. **`scripts/check-plugin-version_test.sh` -- `stage` writes the site page
   (§4.1, §5).** Add `site=${3:-$1}` as the first statement of `stage`,
   `mkdir -p "$work/repo/web"` alongside the existing `mkdir -p`, and a
   `printf` of the one-line colophon fixture to `$work/repo/web/index.html`.
   Depends on: nothing.
   Verify: `sh scripts/check-plugin-version_test.sh` prints
   `check-plugin-version: ok` and exits 0 (`echo $?`). Before this step it
   printed five `FAIL:` lines and exited 1.

2. **`scripts/check-plugin-version_test.sh` -- negative case (§4.2).**
   Insert the two lines after the `disagreeing manifests, matching tag`
   check.
   Depends on: step 1.
   Verify: script still exits 0. Mutation: temporarily change the fixture
   version in the new `stage` call from `9.9.9` to `1.2.3`, run the script,
   confirm exactly one line `FAIL: site version disagrees with manifests
   (exit 0, want 1)` and exit 1; revert the mutation. Then run
   `shellcheck scripts/check-plugin-version_test.sh` if shellcheck is
   installed; it must report nothing new (the existing `SC1007` disable
   stays).

3. **`Makefile` -- loop propagates failure (§4.3).** Change line 32 only.
   Depends on: nothing (but do it after 1-2 so `make check` is green at the
   end of this step).
   Verify: `make check` exits 0 and its output still shows one
   `==> scripts/<name>_test.sh` banner per script test followed by that
   test's `ok` line. Mutation: `git stash` is not available for a single
   hunk here, so instead re-apply the step-2 mutation (`9.9.9` -> `1.2.3`),
   run `make check`, confirm it exits non-zero with the `FAIL:` line and
   make's error, then revert. This is the mutation the issue asks for
   ("break the fixture's version and confirm `make check` exits non-zero");
   before step 3 the same mutation left `make check` green.

4. **Full gate.** `make check` from a clean tree (`git status` shows only
   the three files in §2 plus the plan copy). It must exit 0. `gofmt -l .`
   and `go mod tidy` inside it are untouched by this change and must stay
   quiet.
   Depends on: steps 1-3.

5. **Commit and report.** One commit, message
   `fix(check): script tests fail make check; stage web/index.html in the version fixture (#197)`,
   containing exactly `Makefile`, `scripts/check-plugin-version_test.sh` and
   `docs/plans/2026-09-19-make-check-script-tests.md`. Report the verbatim
   output of the step-3 mutation run (the failing `make check`) and of the
   final green `make check`, then create the done marker.
   Depends on: step 4.
