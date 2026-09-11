# Candidates T3: `Binding.BuilderCandidate` replaces `BuilderAlias` (#80)

**Design spec:** `docs/specs/2026-09-11-candidates-design.md` -- read §3.6, §3.10
**Issue:** #80
**Depends on:** nothing. Runs in parallel with T1/T2 in its own worktree.

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
- **This is a rename, not a semantic change.** The field's *value* is still
  whatever it was (an alias name today; a candidate token after T4b).
  Do not touch `internal/alias`, do not change what is stored in the field,
  do not change any error text or user-visible string except where a step
  says so, do not rename `ErrNoBuilderAlias` or any function.
- Test string literals like `"builder"`, `"abuilder"`, `"agy"` assigned to
  the field stay as they are. Only the field name changes.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: rename the field, drop the old JSON key, prove the old key is ignored

**Files:**
- Modify: `internal/store/types.go:38`, `internal/store/store_test.go`
- Modify: `internal/relay/status.go:27,158,321`, `internal/relay/status_test.go`
- Modify: `internal/relay/bind.go:173,239`, `internal/relay/add.go:177`, `internal/relay/fork.go:32,128,209`, `internal/relay/send.go:73,103,202,206`, `internal/relay/answer.go:66,106`
- Modify: `internal/relay/bind_test.go`, `internal/relay/fork_test.go`
- Modify: `internal/ui/fetch.go:166`, `internal/ui/list.go:154`, `internal/ui/fetch_test.go`, `internal/ui/list_test.go`
- Modify: `cmd/relay/main.go:385,386,396,505`

(Line numbers are as of `main` at e207e2e; use `grep -rn BuilderAlias` as
the source of truth.)

**Interfaces produced** (spec §3.6, §3.10):
- `store.Binding.BuilderCandidate string \`json:"builder_candidate,omitempty"\``
- `relay.StatusRow.BuilderCandidate string \`json:"builder_candidate"\``

- [ ] **Step 1: the failing decode test** (spec §3.6)

  In `internal/store/store_test.go` add `TestLoadIgnoresLegacyBuilderAlias`:
  write a `bind.json` by hand into the store root at the path `Save` would
  use for a binding named `old` (look at how `Save` composes the path; do
  not call `Save`), with body

  ```json
  {"name":"old","cwd":"/repo","planner":{"pane_id":"w2:p3","kind":"claude"},"builder":{"pane_id":"w2:p4","kind":"agy"},"builder_alias":"abuilder","round":1,"state":"active","round_cap":20,"round_timeout_ms":1800000}
  ```

  then `Load("old")` and assert `got.BuilderCandidate == ""` and
  `got.Name == "old"`. Comment on the test: a binding written before #80
  carries `builder_alias`; the decoder drops it, so the binding reads as
  adopted -- that is the whole migration (spec §3.6).

  Run: `go test ./internal/store/ -run TestLoadIgnoresLegacyBuilderAlias`.
  Expected: **compile error**, `BuilderCandidate` undefined.

- [ ] **Step 2: rename in `store`**

  `internal/store/types.go`: `BuilderAlias string \`json:"builder_alias"\``
  → `BuilderCandidate string \`json:"builder_candidate,omitempty"\``. Doc
  comment: the `harness/provider/model` token the builder was started from
  (#80); empty for an adopted builder and for any binding written before
  the field existed. `store_test.go` `newBinding`: rename the key.

  Run: `go test ./internal/store/`. Expected: green, including step 1.

- [ ] **Step 3: rename every other reference**

  `grep -rn BuilderAlias --include='*.go' .` and rename each **field
  access and struct key** to `BuilderCandidate`. This includes
  `relay.StatusRow.BuilderAlias` → `BuilderCandidate` with tag
  `json:"builder_candidate"`. Leave alone:

  - `ErrNoBuilderAlias` (a sentinel; T4b renames it)
  - test function names (`TestAskRefusesABuilderAlias`,
    `TestAddRequiresABuilderAlias`)
  - the word "alias" inside format strings and comments

  For the one status test that reads the field:
  `status_test.go:34-35` becomes `if got.BuilderCandidate != "abuilder"` /
  `t.Errorf("candidate = %q", got.BuilderCandidate)`.

  Run: `go build ./... && go vet ./...`. Expected: clean.
  Run: `grep -rn BuilderAlias --include='*.go' .`. Expected: only
  `ErrNoBuilderAlias` lines and the two test function names.

- [ ] **Step 4: `make check`, commit**

  ```bash
  git add -A internal cmd
  git commit -m "refactor(store): Binding.BuilderCandidate replaces BuilderAlias; legacy key ignored (#80)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, and the output of
`grep -rn BuilderAlias --include='*.go' .` after the change.
