# codex builder can write its report: the binding's state dir is a writable root at tier edit; tier read is refused (#230)

Closes #230. Design in this file; no separate spec.

Decisions taken by the planner, each verified on codex-cli 0.155.1 on
2026-09-20 (probes in the issue):

- `-s workspace-write` rejects both shell and `apply_patch` writes to
  `~/.local/state/relay/<binding>/`; adding
  `-c sandbox_workspace_write.writable_roots=["<that dir>"]` lets both
  through. **Tier `edit` gains that override.**
- `-s read-only` ignores `writable_roots` entirely. A codex process at tier
  `read` can never write the report, the done marker, a question or a
  findings file relay requires -- so **tier `read` becomes a refusal cell
  for codex** (`ErrTierUnsupported`), like opencode's `read`/`edit`. It is
  not silently mapped onto workspace-write: that would widen the worktree
  permission the human asked to withhold.
- `yolo` (`--dangerously-bypass-approvals-and-sandbox`) and `harness` are
  unchanged.
- The observed denial line replaces the "unverified" codex
  `DenialPatterns`.
- Go's default build cache is outside the sandbox too; that is the user's
  `~/.codex/config.toml` to fix, and the README says so. relay adds nothing
  for it.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else.

**Scope guard.** Touch only the files listed in §2. Do not run codex, `make
e2e`, or any harness binary. Do not add a CLI verb or flag, or a module
dependency. Run every command in the foreground; dispatch no sub-agents.
Every exported signature you add or change is named in §4; change no other.

**Commits.** ONE commit, subject
`fix(harness): codex writes its report -- state dir is a writable root at tier edit, tier read refused (#230)`.
Not `feat:`.

**Commit the plan with the work.** Copy this plan file to
`docs/plans/2026-09-20-codex-writable-state.md` in your worktree and include
it.

**Before step 1**: `git status --short` and `git branch --show-current`; you
must be on `relay/<binding-name>` under `~/.local/state/relay/.worktrees/`
with a clean tree. Otherwise halt.

## 1. System overview

`Harness.Launch` (`internal/harness/harness.go:356`) renders a candidate's
argv in two forms -- `Args` for a pane, `Print` for a headless process --
and appends `PermissionArgs(tier)` (`tier.go`) to both. `Print` carries
placeholders (`<prompt>`, `<budget>`, `<dir>`) that `Launch.PrintArgs`
fills at send time; `Args` carries none and is passed to
`herdr agent start` as is (`bind.go:601`, `ask.go:203`).

The writable root is a value only the caller knows (the binding's state
directory, `rt.Store.Dir(name)`), and it must reach **both** forms: a pane
codex builder writes the same report file a headless one does. So this
plan adds one more placeholder element, fills it in `PrintArgs` (new
parameter) and in a new `PaneArgs` for the pane form, and threads the
state dir through the three `Launch` call sites.

## 2. File structure

```
internal/harness/
  harness.go          EDIT  StatePlaceholder; PrintArgs gains state; PaneArgs; codex DenialPatterns
  tier.go             EDIT  codex: read -> ErrTierUnsupported; edit -> -s workspace-write -c <state placeholder>
  harness_test.go     EDIT  TestLaunchCodex (edit expectations), TestLaunchPrintPerKind/TestLaunch callers of PrintArgs gain the state arg; TestPaneArgsFillsState; TestPrintArgsFillsState
  tier_test.go        EDIT  TestPermissionArgsTable codex rows; TestLaunchAppliesTier if it names codex
internal/relay/
  headless.go         EDIT  headlessLaunch gains state; startRound passes rt.Store.Dir(b.Name)
  headless_test.go    EDIT  headlessLaunch callers gain the state arg (mechanical)
  bind.go             EDIT  StartAgent(..., l.PaneArgs(rt.Store.Dir(name)))
  ask.go              EDIT  StartAgent(..., l.PaneArgs(rt.Store.Dir(opts.Name)))
  bind_test.go / ask_test.go  EDIT only if an assertion on the started args needs the filled value (say which)
README.md             EDIT  tiers table codex row; codex section: writable root + GOCACHE note (§7)
docs/specs/2026-09-19-codex-harness-design.md  EDIT  tier table amended (§7)
docs/plans/2026-09-20-codex-writable-state.md  NEW  copy of this plan
```

Grep before you start: `grep -rn "PrintArgs(\|headlessLaunch(\|\.Args)" --include=*.go internal cmd`
-- every hit must be in a file §2 lists. If not, halt.

## 3. Data structures & type definitions

`internal/harness/harness.go`, next to `DirPlaceholder`:

```
// StatePlaceholder stands, as its own element, for the codex writable-roots
// override: PrintArgs and PaneArgs replace the element with
// `sandbox_workspace_write.writable_roots=["<state dir>"]` (#230). It is
// only ever the element after a "-c".
StatePlaceholder = "<state>"
```

No struct changes.

## 4. Interface definitions & component contracts

### `internal/harness`

```
// PermissionArgs -- codex rows become:
//   read  -> (nil, ErrTierUnsupported): "codex cannot honour tier read: -s read-only cannot write the report relay needs (writable_roots is ignored under read-only); use --tier edit or --tier harness"
//   edit  -> ["-s", "workspace-write", "-c", StatePlaceholder]
//   yolo  -> ["--dangerously-bypass-approvals-and-sandbox"]   (unchanged)
func (h Harness) PermissionArgs(tier Tier) ([]string, error)

// writableRootsArg renders the -c value for state. state is quoted as a
// TOML basic string with strconv.Quote (identical escapes for every path
// this program produces). Precondition: state is absolute and non-empty.
func writableRootsArg(state string) string
//   "sandbox_workspace_write.writable_roots=[" + strconv.Quote(state) + "]"

// PrintArgs gains state, filled into StatePlaceholder the same way dir
// fills DirPlaceholder. A kind whose Print has no StatePlaceholder ignores
// it. Precondition: state is absolute or empty; when empty and the
// placeholder is present, the element is filled with writableRootsArg("")
// -- which codex rejects loudly -- so callers must pass it; a test pins
// that every production caller does.
func (l Launch) PrintArgs(prompt string, budget time.Duration, dir, state string) []string

// PaneArgs is Args with StatePlaceholder filled; a fresh slice. Every
// pane start goes through it, even for kinds with no placeholder, so the
// rule has one home.
func (l Launch) PaneArgs(state string) []string
```

`Launch` doc comment: mention StatePlaceholder beside the other two and
that `Args` may carry it (only for codex at tier edit) and must be passed
through `PaneArgs`.

Codex `DenialPatterns` become, in this order:

```
`(?i)patch rejected: writing outside of the project`,
`(?i)rejected by user approval settings`,
`(?i)sandbox.*(denied|blocked|not permitted)`,
`(?i)permission denied`,
```

with the comment updated: "first two observed 2026-09-20 (#230), the rest
unverified".

### `internal/relay`

```
func headlessLaunch(c candidate.Candidate, role harness.RoleSpec, tier harness.Tier, budget time.Duration, prompt, dir, state string) ([]string, error)
```

`startRound` passes `rt.Store.Dir(b.Name)`. `bind.go:601` and `ask.go:203`
pass `l.PaneArgs(rt.Store.Dir(<binding name>))` -- in `ask.go` the consult's
files live under the binding's directory (`Store.consultFile` uses
`s.Dir(name)`), so it is the same `opts.Name`.

## 5. High-level pseudocode

```
PermissionArgs("codex", edit) -> ["-s","workspace-write","-c","<state>"]
Launch(...): perm appended to Args and Print as today   // placeholder now in both

PrintArgs(prompt, budget, dir, state):
    for each element: <prompt> -> prompt; <budget> -> budget; <dir> -> dir;
                      <state> -> writableRootsArg(state); else as is
PaneArgs(state):
    for each element of Args: <state> -> writableRootsArg(state); else as is

headlessLaunch(..., dir, state): [binary] + l.PrintArgs(prompt, budget, dir, state)
startRound: headlessLaunch(c, role, tier, budget, prompt, b.CWD, rt.Store.Dir(b.Name))
bind spawn:  StartAgent(ctx, agentName, l.Kind, paneID, l.PaneArgs(rt.Store.Dir(name)))
ask spawn:   StartAgent(ctx, agentName, l.Kind, pane,   l.PaneArgs(rt.Store.Dir(opts.Name)))
```

## 6. Error handling strategy

- Tier `read` on a codex candidate fails at `Launch` with
  `ErrTierUnsupported`, the same error class and the same moment opencode's
  refusals use, so `bind|add|fork|send --tier read` and a policy
  `tier.builder = read` surface it before anything is spawned.
- No other new error. A missing `state` is a programming error covered by
  the caller test in §8, not a runtime check.

## 7. Documentation

`README.md`:
- Tiers table (`:948`) codex row: `| codex | (none) | refuse | `-s workspace-write -c sandbox_workspace_write.writable_roots=["<binding state dir>"]` | `--dangerously-bypass-approvals-and-sandbox` |`,
  and after the opencode paragraph below the table:
  > codex does not support the `read` tier: `-s read-only` cannot write the
  > report, marker, question and findings files relay stages under
  > `~/.local/state/relay/<binding>/`, and codex ignores `writable_roots`
  > under read-only. Choosing `read` for a codex candidate is refused with
  > an error directing you to `--tier edit` or `--tier harness`. At `edit`
  > relay adds the binding's state directory as a writable root; that is
  > the only path outside the worktree the sandbox lets the builder write.
- Codex candidate section (near `:725`): one paragraph:
  > Under `workspace-write`, codex also cannot write to Go's default build
  > cache (`~/.cache/go-build`), so a Go plan fails at `go build` unless
  > the plan sets `GOCACHE` inside the worktree or `/tmp`, or your
  > `~/.codex/config.toml` lists it under
  > `sandbox_workspace_write.writable_roots`. relay adds only its own state
  > directory.

`docs/specs/2026-09-19-codex-harness-design.md` tier table (`:118-124`):
amend the `read` row to `refuse (#230: read-only cannot write the report)`
and the `edit` row to add the writable-roots override; replace "No refusal
cell." with "One refusal cell, `read`, since #230." Keep the rest.

## 8. Tests

`internal/harness/tier_test.go` `TestPermissionArgsTable`: codex `read` ->
`ErrTierUnsupported` (error text contains `writable_roots`); `edit` ->
`["-s","workspace-write","-c",StatePlaceholder]`; `yolo` unchanged.

`internal/harness/harness_test.go`:
- `TestLaunchCodex`: the TierEdit expectations gain `"-c", StatePlaceholder`
  in both `Args` and `Print`; add a TierRead case expecting
  `ErrTierUnsupported`.
- `TestPrintArgsFillsState`: codex edit launch, `PrintArgs("p", 0, "/wt",
  "/home/u/.local/state/relay/x")` -> the element after `-c` following
  `workspace-write` equals
  `sandbox_workspace_write.writable_roots=["/home/u/.local/state/relay/x"]`
  and no `<state>` remains; a claude launch ignores state (identical to
  before).
- `TestPaneArgsFillsState`: same on `Args`; for claude/agy/opencode
  `PaneArgs(state)` equals `Args`.
- `TestWritableRootsArgQuotes`: a path with a space and one with a `"`
  render as valid TOML basic strings (`strconv.Quote` output).
- Existing `PrintArgs` callers in this file gain the state argument.
  **Mutation check (run and report):** in `PaneArgs`, return `l.Args`
  unchanged; `TestPaneArgsFillsState` fails.

`internal/relay/headless_test.go`: `headlessLaunch` callers gain a state
argument; add `TestStartRoundPassesStateDir` (or extend an existing
startRound test): a codex candidate at tier edit -> the fake runner's argv
contains `sandbox_workspace_write.writable_roots=["<rt.Store.Dir(name)>"]`.
If no codex-candidate fixture exists in that file, build one from the
existing candidate JSON pattern (`testCandidatesJSON`-style) inside the
test.

`internal/relay/bind_test.go`: one test asserting a pane codex spawn at
tier edit passes the filled root in `fakeHerdr.starts[0]` args (the fake
records `StartAgent`), and that a claude pane spawn's args are unchanged.

CI note: none of these reach herdr or codex; `cmd/relay` gets no test.

## 9. Ordered implementation steps

1. `harness.go`: placeholder, `writableRootsArg`, `PrintArgs` state param,
   `PaneArgs`, `DenialPatterns`; `tier.go` codex rows; `harness_test.go` +
   `tier_test.go` per §8 (fix the in-package `PrintArgs` callers). Mutation
   check. `go test ./internal/harness -count=1`.
2. `internal/relay`: `headlessLaunch`/`startRound`, `bind.go`, `ask.go`,
   test-caller updates, the two new tests. `go build ./... && go vet ./...`
   clean; `go test ./internal/relay -count=1`.
3. Docs per §7.
4. Copy the plan to `docs/plans/2026-09-20-codex-writable-state.md`. One
   commit. Gate, exactly:
   ```
   test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
   make check
   ```
   Report the tail verbatim, the mutation outcome, and
   `git diff --stat main..HEAD`.

## 10. Report

End `NNN-report.md` with the ```relay block (`status`, `halted_at`,
`changed_paths`, `commands_run`, `not_done`). Say that the real check --
a codex smoke round on the installed binary -- is the planner's, after
`make service`.
