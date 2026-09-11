# Candidates T7: delete `internal/alias` (#80)

**Design spec:** `docs/specs/2026-09-11-candidates-design.md` -- read §1 point 3, §7 step 7
**Issue:** #80
**Depends on:** T6 -- merged into this tree.

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
- This task removes code and nothing else. **No behaviour changes, no new
  tests, no doc edits** (T8 does docs).
- If `go build ./...` fails after the deletion for any reason other than
  the two references named below, stop and report: a previous task
  missed a call site and this plan must not paper over it.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: remove the package and its last two references

**Files:**
- Delete: `internal/alias/alias.go`, `internal/alias/alias_test.go` (the whole directory)
- Modify: `internal/relay/herdr.go` (drop `Aliases` from `Runtime` and the import)
- Modify: `cmd/relay/main.go` (drop `alias.LoadTable` from `newRuntime`, the `Aliases:` field, and the import)

- [ ] **Step 1: confirm the precondition**

  Run: `grep -rn "internal/alias\|alias\.\(Table\|Spec\|LoadTable\|DefaultTable\|ErrUnknownAlias\)" --include='*.go' .`
  Expected: hits only in `internal/alias/`, `internal/relay/herdr.go`,
  `cmd/relay/main.go`. Anything else → stop and report.

- [ ] **Step 2: delete**

  ```bash
  git rm -r internal/alias
  ```

  `internal/relay/herdr.go`: remove the `Aliases *alias.Table` field and
  its comment, and the `internal/alias` import. `cmd/relay/main.go`
  `newRuntime`: remove the `aliases, err := alias.LoadTable(...)` block
  and the `Aliases: aliases,` line; remove the import. `aliases.json` is
  now never read (spec §1 point 3) -- add one sentence to `newRuntime`'s
  doc comment (or the nearest comment) saying so, so the next reader
  does not go looking for the load.

  Run: `go build ./... && go vet ./...`. Expected: clean.

- [ ] **Step 3: `make check`, commit**

  Run: `grep -rn "alias" --include='*.go' internal cmd | grep -v "_test.go"`.
  Expected: no hits that refer to the old package -- prose uses of the
  word in comments are fine but list them in the report.

  ```bash
  git add -A internal cmd
  git commit -m "refactor: delete internal/alias; relay reads candidates.json only (#80)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, and the grep
outputs from steps 1 and 3.
