# Plan: #292 round 1: the mechanical rename relay -> relevo

Spec: `docs/specs/2026-09-23-rename-relevo-design.md` (§1 names, §2 this round).

## 1. System overview

This round renames the codebase with a checked-in script and fixes the few tests
the script can't. There is no behaviour change beyond the names: the binary, module,
packages, config and state roots, units, env vars, git refs, wire headers, log
markers, MCP server name and ledger source all move from `relay` to `relevo`.
`relevo migrate`, the legacy readers and the name guard are rounds 2 and 3, **not
this one**.

The planner cut branch `rename-relevo` from `origin/main` and committed
`scripts/rename-relevo.sh` as its only commit. A trial of the same script on
`7e607ea` gave a tree where build, vet, tests, gofmt, tidy, plugin-version,
shellcheck, the script tests and e2e all pass, after exactly the fixes in step 3.

**Stop rather than improvise.** If a step can't be done as written, or the
results differ from what the plan predicts in a way the steps don't cover, halt and
report. Do not change the script's rules to make a test pass.

## 2. File structure

There are no new files besides the script, which is already committed. The script
touches:

- **Moves:** `cmd/relay/` → `cmd/relevo/`, `internal/relay/` →
  `internal/relevo/`, `dist/relay.service` → `dist/relevo.service`,
  `dist/relay-serve.service` → `dist/relevo-serve.service`,
  `dist/com.github.fuad-daoud.relay.plist.in` → `...relevo.plist.in`,
  `scripts/relay-service-template_test.sh` → `scripts/relevo-service-template_test.sh`.
- **Token rewrite:** every tracked text file except `docs/plans/**`,
  `docs/specs/**`, `docs/superpowers/**`, `go.sum`, the script itself and
  `internal/harness/agents/shipped.sha256`.
- **Regenerated:** `internal/harness/agents/shipped.sha256`, by
  `agents-shipped.sh --write`, which the script runs.

Hand edits in this round (closed list):

1. `internal/ui/testdata/*.golden` and `internal/ui/dash/testdata/*.golden`,
   regenerated with `-update` (step 3a).
2. `internal/ui/rail_test.go`, the `headless` fixture in `TestCardLinesShapes`
   (step 3b).
3. Anything else only under step 3c's rule, and each one named in the report.

## 3. Data structures / 4. Interfaces

None change. Every exported identifier keeps its shape. Only names containing
`Relay` change (`RelayVerbs` → `RelevoVerbs`, `findRelayBlock` → `findRelevoBlock`,
and so on), and the package `relay` becomes `relevo`.

## 5. Pseudocode

```
verify HEAD is the planner's script commit on rename-relevo, tree clean
run the script                      -> moves, token rules 1-5, rule 6 restore, gofmt, shipped hashes
build + vet                          -> expect clean
test                                 -> expect exactly 3 failures (step 3)
  regenerate the two golden sets; fix the card fixture
  anything else: 3c rule or halt
leftover scan                        -> expect only the kept list
make check                           -> green
commit
```

## 6. Error handling

- The script refuses on a dirty tree, on a second run, or when a relay-named path
  outside its move list is tracked. It prints that path. **Halt and report the
  path.** Do not add it to the move list.
- If the script dies inside a `perl` step, halt. The tree is half swept, so report
  and leave it as it is.

## 8. Working efficiently

- Everything is located here, so don't search for it. Batch independent reads.
- Iterate with the focused commands:
  - `go build ./... && go vet ./...`
  - `go test ./internal/ui/... ./internal/harness/ ./internal/proc/`
- Then run `go test ./...` once, and `make check` once at the end.
- `make e2e` is not part of `make check`. Run it once, too:
  `go test ./internal/e2e/ -run TestHeadlessE2E -count=1`.

## 9. Ordered implementation steps

**Step 0: check the starting point.** Run `git status` and `git log --oneline -3`.
HEAD's subject must be `scripts: the rename tool relay -> relevo (#292)` and the
tree must be clean. Otherwise halt.

**Step 1: run the script.** `sh scripts/rename-relevo.sh`. It ends with
`rename-relevo: done`.

- Verify: `test -d cmd/relevo && test ! -d cmd/relay`, and `head -1 go.mod` prints
  `module github.com/fuad-daoud/relevo`.

**Step 2: build.** `go build ./... && go vet ./...`. Expected: clean. If not, the
sweep produced something the trial did not: apply step 3c's rule.

**Step 3: tests.** `go test ./...`. The trial saw exactly these failures, with these
fixes:

- **3a. UI goldens** (`internal/ui` `TestGoldenViews`, `internal/ui/dash`
  `TestGoldenViews`). The header now reads `relevo`, one column wider, and the
  padding shifts.
  - Run `go test ./internal/ui/ ./internal/ui/dash/ -run TestGoldenViews -update`.
  - Then run `git diff --word-diff -- internal/ui/testdata internal/ui/dash/testdata`
    and confirm every change is `relay`→`relevo` or whitespace alignment on the
    same line. Anything else means halt.
- **3b. `TestCardLinesShapes`** (`internal/ui/rail_test.go`, the `headless`
  fixture, about lines 33-35 and 54). `relevo/api` no longer fits the rail width
  and renders as `relevo/ap`. Change the fixture's `Branch: "relevo/api"` to
  `Branch: "relevo/io"`, and the want line `"opencode · headless · relevo/api"` to
  `"opencode · headless · relevo/io"`. That keeps the test's point: the full branch
  shows. Leave the rail width alone.
- **3c. Anything else.** A fix is allowed only when the test fails because the sweep
  renamed one side of a comparison and not the other, for example a string built by
  concatenation (`"re" + "lay"`), or a name inside a binary fixture that `git grep
  -I` skipped. Make the two sides agree on `relevo`, then name the file, line and
  reason in the report. If a failure is not that, halt.

**Step 4: leftover scan.** Run:

```
git grep -nIE '(RELAY|Relay)([^a-z]|$)|(^|[^A-Za-z]|\\[nt])relay([^a-z]|$)' -- . \
  ':!docs/plans/**' ':!docs/specs/**' ':!docs/superpowers/**' ':!go.sum' \
  ':!scripts/rename-relevo.sh' ':!internal/harness/agents/shipped.sha256'
```

The only expected hits are in `internal/harness/install_test.go`, inside the
`olderArchitectDoc` literal (spec §1, kept item 5). Report any other hit. **Do not
fix it by hand.**

**Step 5: full check.**

- `make check`: gofmt, vet, `go test -race`, tidy, plugin version, shellcheck
  (which lints the new script) and the script tests.
- Also run `go test ./internal/e2e/ -run TestHeadlessE2E -count=1`.

Both must pass.

**Step 6: commit.** One commit:

```
refactor: rename relay -> relevo, the mechanical sweep (#292)

scripts/rename-relevo.sh run once; UI goldens regenerated; card fixture
branch shortened to fit the rail.
```

Don't push; the planner pushes.

**Declared scope:** what the script touches, plus hand edits 1 and 2 in §2, plus
any 3c fixes, each named in the report.

**Report:**

- the script's final line;
- `git diff --stat HEAD~1 | tail -1`;
- the step 4 output;
- every 3c fix, with file, line and reason;
- the result of `make check` and of e2e.
