# Remove `web/` from relay; the site now lives in fuad-daoud/relay-site

If any step below is impossible as written or contradicts what you find on
disk, STOP and report. Do not improvise around it.

## 1. System overview

The static site under `web/` (two files, `index.html` and `style.css`) has
moved to its own repo, `github.com/fuad-daoud/relay-site`, and is served at
`https://relay-site.fuad-daoud.com`. Three things in this repo still know
about `web/`: the `release` target in `Makefile` stamps the version into its
colophon, `scripts/check-plugin-version.sh` fails if that stamp drifts from
the manifests, and `scripts/check-plugin-version_test.sh` stages a fake
`web/index.html` to test that. This plan deletes the directory and the three
couplings, and leaves one pointer in the README. Nothing else changes; in
particular the manifest-agreement, tag and feat-drift checks stay exactly
as they are.

## 2. Files touched

```
web/index.html                          delete
web/style.css                           delete
Makefile                                edit: release target
scripts/check-plugin-version.sh         edit: drop the site block + comment
scripts/check-plugin-version_test.sh    edit: stage() + one case
README.md                               edit: one pointer paragraph
docs/plans/2026-09-21-remove-web-site.md  this file (commit it too)
```

`docs/plans/2026-09-19-make-check-script-tests.md` mentions `web/index.html`;
it is a historical record and is NOT edited.

## 3. Ordered implementation steps

Work in the current directory (a git worktree on branch `chore/remove-web`).
`git status --short` must show nothing except this untracked plan file; if
anything else appears, halt.

### Step 1 — Delete the directory
`git rm -r -q web`
Verify: `ls web` fails; `git status --short` shows `D  web/index.html` and
`D  web/style.css`.

### Step 2 — Makefile
In the `release:` target, delete the whole line that begins with
`	sed 's|<span data-version>` (the one that rewrites `web/index.html`),
and in the `git commit -m "chore(release): v$(VERSION)"` line remove the
trailing ` web/index.html` argument so it ends with
`herdr-plugin.toml from-source/herdr-plugin.toml`.
Verify: `grep -n 'web' Makefile` prints nothing; the `release:` target
still has the two manifest `sed` lines, `$(MAKE) check`, the commit, the tag
and the echo, in that order.

### Step 3 — check-plugin-version.sh
- In the header comment, change the line
  `#    and the version stamped in the web/index.html colophon must match them.`
  by deleting it entirely and changing the line above it so item 1 reads
  `# 1. herdr-plugin.toml and from-source/herdr-plugin.toml must exist and agree.`
- Delete the entire block that starts with the comment
  `# The site colophon is stamped by make release alongside the manifests (#196);`
  and ends with the `fi` that closes `if [ "$v1" != "$v3" ]; then … fi`.
  That block is: the two comment lines, `site=web/index.html`, the
  `if [ ! -f "$site" ]` … `fi`, the `v3=$(sed …)` line, the
  `if [ -z "$v3" ]` … `fi`, and the `if [ "$v1" != "$v3" ]` … `fi`. Keep
  the blank line separation so the tag check (`if [ $# -eq 1 ]; then`)
  follows the manifest-agreement check directly.
Verify: `grep -n 'site\|v3\|web' scripts/check-plugin-version.sh` prints
nothing; `sh -n scripts/check-plugin-version.sh` exits 0;
`shellcheck scripts/check-plugin-version.sh` exits 0.

### Step 4 — check-plugin-version_test.sh
- In `stage()`: delete the line `	site=${3:-$1}`; change the `mkdir -p`
  line to `	mkdir -p "$work/repo/from-source"`; delete the `printf` line
  that writes `web/index.html`.
- Delete the two-line case
  ```
  stage 1.2.3 1.2.3 9.9.9
  check "site version disagrees with manifests" 1
  ```
  together with the blank line that separates it from its neighbours, so
  the cases stay evenly spaced.
Verify: `grep -n 'site\|web' scripts/check-plugin-version_test.sh` prints
nothing; `sh scripts/check-plugin-version_test.sh` prints
`check-plugin-version: ok` and exits 0; `shellcheck` on the file exits 0.

### Step 5 — README pointer
Directly after the two badge lines at the top of `README.md` (before the
blank line that precedes "`relay` automates …"), insert a blank line and
then exactly:

```
Site: [relay-site.fuad-daoud.com](https://relay-site.fuad-daoud.com) (source in [fuad-daoud/relay-site](https://github.com/fuad-daoud/relay-site)).
```

Verify: `sed -n 1,8p README.md` shows the two badges, a blank line, the
Site line, a blank line, then the `relay` paragraph.

### Step 6 — Full check
Run `make check`. It must exit 0. This is CI's gate; if it fails, do not
patch around it — halt and report the failing output.
Verify: last lines of `make check` output include the ok lines from the
shell tests and exit status 0. Also `grep -rn 'web/' Makefile scripts`
prints nothing.

### Step 7 — Commit (do NOT push)
`git add -A` then `git status --short` must show exactly: the two `web/`
deletions, `Makefile`, `README.md`, the two scripts, and this plan file —
seven paths. Commit with exactly:

```
chore: move the static site out to fuad-daoud/relay-site

web/ now lives in its own repo and is served at relay-site.fuad-daoud.com,
so make release no longer stamps its colophon and check-plugin-version no
longer reads it. The manifest, tag and feat-drift checks are unchanged.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
```

Verify: `git status` clean; `git show --stat HEAD` lists the seven paths.

## Report

Include the `make check` tail, the output of
`sh scripts/check-plugin-version_test.sh`, and `git show --stat HEAD`.
