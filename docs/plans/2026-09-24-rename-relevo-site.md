# Plan: relay#292 in relay-site: the site becomes relevo-site

Repo: `~/projects/relay-site` (this worktree), branch `relevo-site-292`. relay is
being renamed relevo in one clean break (relay spec
`docs/specs/2026-09-23-rename-relevo-design.md`): every command the site teaches
(`relay add`, `relay send`, …) becomes `relevo …`. The module becomes
`github.com/fuad-daoud/relevo-site`, and the GitHub repo will be renamed to match.

The public hostnames **do not change**: `relay-site.fuad-daoud.com` (this site)
and `relay.fuad-daoud.com` (the API). `docs/` (historical plans, specs and design
boards) is not touched.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the repo, halt and report.

## 1. Overview

The planner committed `rename-relevo.sh`, a mechanical sweep, as this branch's
first commit. A trial on `c2eba08` changed 11 files, and `go build`, `go vet` and
`go test ./...` all passed with no hand edit.

The binary images (`og.png`, the favicons) are not text and are not touched. The
favicons carry no name. `og.png` is rendered from `assets-src/og.html` with
headless chromium, which this machine lacks. The source is swept, but
re-rendering is a follow-up the planner reports. **Do not** try to install
chromium.

## 2. Files

What the script touches (tracked text outside `docs/`), plus:

- `README.md`: after the sweep, confirm:
  - the build line says `-o relevo-site`;
  - the provenance line reads sensibly;
  - the deployment path is `~/projects/servers/contabo/services/relevo-site/`.

  Fix wording only if the sweep made it wrong.
- `.gitignore`: it should now ignore `/relevo-site` (confirm).

## 8. Working efficiently

- **Checks:** `go build ./... && go vet ./... && go test ./...`,
  `shellcheck rename-relevo.sh assets-src/raster.sh`, and
  `test -z "$(gofmt -l .)"`.

## 9. Steps

**Step 0.** `git status` must be clean on `relevo-site-292`, with HEAD `rename-relevo.sh: the one-shot
rename tool (relay#292)`. Otherwise halt.

**Step 1.** `sh rename-relevo.sh`. It ends with `rename-relevo: done`. Then run:

```
git grep -nIE '(RELAY|Relay)([^a-z]|$)|(^|[^A-Za-z]|\\[nt])relay([^a-z]|$)' -- . ':!docs/**' ':!rename-relevo.sh' | grep -v 'relay-site.fuad-daoud.com' | grep -v 'relay.fuad-daoud.com'
```

It must print nothing.

**Step 2.** The §2 README and `.gitignore` checks.

**Step 3.** Run every check; all must pass. Then build the deploy binary exactly as
the README's build line says, into `/tmp/relevo-site`, and run `/tmp/relevo-site
-addr 127.0.0.1:18082 &`. Check that `curl -s 127.0.0.1:18082/ | grep -c relevo`
is > 0 and that `curl -s 127.0.0.1:18082/ | grep -c 'relay '` is 0. Kill it.

**Step 4.** One commit:
`rename: relay-site -> relevo-site; the site teaches relevo (relay#292)`. Don't
push.

**Declared scope:** what the script touches, plus §2.

**Report:**
- the step 1 output;
- the step 3 curl counts;
- the checks;
- `git diff --stat HEAD~1 | tail -1`.
