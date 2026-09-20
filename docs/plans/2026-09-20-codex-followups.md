# codex follow-ups: `--add-dir` for relay's state dir, doctor version field

Spec: `docs/specs/2026-09-20-codex-sandbox-and-version-design.md`. It is
not on main yet: read it from
`/home/fuad/projects/relay/docs/specs/2026-09-20-codex-sandbox-and-version-design.md`,
copy it byte for byte to the same relative path in your worktree, and
commit it with the work. This plan cites its sections as §N.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass,
stop at that step, write the report saying which step and why, create the
done marker, and do nothing else.

**Scope guard.** Touch only the files in §2. Do not run `make e2e`. Do not
add a module dependency. Do not run `codex`. Every existing test must stay
green unchanged except the ones §6 names as extended. Before step 1, grep
the four packages you touch for the string `codex` in `_test.go` files
and list every test that uses it in the report; if one of them would
break for a reason this plan does not name, halt at that step.

**Commits.** ONE squashed commit, subject
`fix(harness,doctor): codex --add-dir for relay's state dir; parse the version field, not the first`.

**Commit the plan with the work.** Copy this file to
`docs/plans/2026-09-20-codex-followups.md` in your worktree.

**Before step 1**: `git branch --show-current` prints `relay/<binding>`
under `~/.local/state/relay/.worktrees/`; `git status --short` is empty.
Otherwise halt.

## 1. Overview

Two independent fixes from the first live codex rounds (§1 of the spec).
(a) codex's `workspace-write` sandbox refuses writes outside the `-C`
worktree, and relay's report/marker/findings live under
`~/.local/state/relay/<name>/`; relay now passes `--add-dir <that dir>` on
every codex launch line. (b) `relay doctor` parsed `codex --version`
(`codex-cli 0.155.1`) as `codex-cli`; it now picks the version-shaped
field.

## 2. Files

```
internal/harness/harness.go       modify: StateDirPlaceholder, codex forms, PrintArgs signature, PaneArgs
internal/harness/harness_test.go  modify: TestLaunchCodex, TestLaunchPrintPerKind codex row, TestPrintArgsSubstitutesPromptAndBudgetWithoutMutating; add TestPaneArgsIdentityForOtherKinds
internal/relay/headless.go        modify: headlessLaunch gains state; startHeadless passes rt.Store.Dir(b.Name)
internal/relay/bind.go            modify: StartAgent gets l.PaneArgs(rt.Store.Dir(name))
internal/relay/ask.go             modify: StartAgent gets l.PaneArgs(rt.Store.Dir(b.Name))
internal/relay/*_test.go          modify ONLY if a test calls headlessLaunch or PrintArgs directly (grep first; list what you changed)
internal/doctor/env.go            modify: versionField, BinaryVersion uses it
internal/doctor/env_test.go       modify: add TestVersionField
README.md                         modify: launch table codex row, tiers section codex paragraph
docs/specs/2026-09-20-codex-sandbox-and-version-design.md   create (copied)
docs/plans/2026-09-20-codex-followups.md                    create (this file)
```

## 3. Interfaces

```go
// internal/harness
const StateDirPlaceholder = "<state>"   // beside Prompt/Budget/DirPlaceholder

// PrintArgs is Print with the prompt, the round budget, the round's working
// tree and the binding's state dir filled in ... (extend the existing doc
// comment: "state is the binding's relay state dir, `--add-dir` on codex;
// a kind with no StateDirPlaceholder ignores it").
func (l Launch) PrintArgs(prompt string, budget time.Duration, dir, state string) []string

// PaneArgs is Args with StateDirPlaceholder filled: a fresh slice, Args
// untouched. Identity in content for a kind whose Args carry no
// placeholder. Postcondition: no placeholder string remains.
func (l Launch) PaneArgs(state string) []string

// internal/relay
func headlessLaunch(c candidate.Candidate, role harness.RoleSpec, tier harness.Tier, budget time.Duration, prompt, dir, state string) ([]string, error)

// internal/doctor
// versionField picks the version out of a --version line: the first
// whitespace field that, after an optional leading "v", starts with a
// digit; the first field when none does; an error on no fields.
func versionField(out string) (string, error)
```

## 4. Pseudocode

### 4.1 harness.go, codex case

```
cfg := ["-p", role.Definition, "-m", id, "-c", "model_provider="+provider]
if effort != "": cfg += ["-c", "model_reasoning_effort="+effort]
base  = cfg + ["--add-dir", StateDirPlaceholder]
print = ["exec", PromptPlaceholder] + cfg + ["--json", "-C", DirPlaceholder, "--add-dir", StateDirPlaceholder]
```

`PrintArgs`: add `case StateDirPlaceholder: out = append(out, state)`.
`PaneArgs`: loop over `l.Args`, same substitution for `StateDirPlaceholder`
only, into a fresh slice.

### 4.2 relay call sites

- `headless.go:56` `headlessLaunch(..., prompt, dir, state string)`; its
  return becomes `l.PrintArgs(prompt, budget, dir, state)`. `headless.go:95`
  passes `rt.Store.Dir(b.Name)` as the new last argument.
- `bind.go:601` `rt.Herdr.StartAgent(ctx, agentName, l.Kind, paneID, l.PaneArgs(rt.Store.Dir(name)))`
  -- `name` is the binding name already in scope in `resolveBuilder`.
- `ask.go:203` `... l.PaneArgs(rt.Store.Dir(b.Name)) ...` -- `b` is the
  binding `ask` loaded; if it is named differently in that scope, use the
  binding's Name field whatever the variable is called.

### 4.3 doctor/env.go

```
func versionField(out string) (string, error):
    fields := strings.Fields(strings.TrimSpace(out))
    if len(fields) == 0: return "", errors.New("empty version output")
    for _, f := range fields:
        v := strings.TrimPrefix(f, "v")
        if v != "" && v[0] >= '0' && v[0] <= '9': return f, nil
    return fields[0], nil

BinaryVersion: out, err := exec...Output(); if err: return "", err
               return versionField(string(out))
```

## 5. README

- "How relay launches one" codex row: append ` --add-dir <state dir>` to
  the pane form shown, and the sentence: "`<state dir>` is
  `~/.local/state/relay/<binding>`, where the plan, report and findings
  live; codex's sandbox refuses writes outside the worktree without it."
- Permission tiers, after the table: one paragraph for codex -- at
  `harness`/`edit` the sandbox is `workspace-write` on the worktree plus
  relay's state dir; anything else outside the tree (a build cache such
  as Go's `~/.cache/go-build`) is read-only, so either run codex at
  `--tier yolo` or add
  `[sandbox_workspace_write]` / `writable_roots = ["/home/<you>/.cache/go-build"]`
  to `~/.codex/config.toml`. Under `yolo` the researcher profile's
  `sandbox_mode = "read-only"` is overridden by the bypass flag, and its
  read-only-ness rests on its instructions, as on claude and opencode.

## 6. Ordered steps

Run `go test ./internal/harness/ ./internal/relay/ ./internal/doctor/`
after every step.

1. **versionField.** Write `TestVersionField` in `env_test.go` with the
   six cases of spec §3. Run: fails to compile. Implement §4.3. Run: pass.
2. **Placeholder + forms.** Update `TestLaunchCodex` (expected Args end
   with `"--add-dir", StateDirPlaceholder`; expected Print ends with
   `"-C", DirPlaceholder, "--add-dir", StateDirPlaceholder`; tier-edit and
   extra-args variants keep those BEFORE the tier/extra args, i.e. the
   order is cfg, add-dir, tier args, extra; `PaneArgs("/s")` returns Args
   with `/s` in place of the placeholder and `l.Args` still holds the
   placeholder; `PrintArgs("hi", time.Hour, "/w", "/s")` ends with
   `"-C", "/w", "--add-dir", "/s"`). Update the codex row of
   `TestLaunchPrintPerKind` and the call in
   `TestPrintArgsSubstitutesPromptAndBudgetWithoutMutating` (add a fourth
   argument `"/s"`; its expectations are for agy and do not change). Add
   `TestPaneArgsIdentityForOtherKinds` (agy, claude, opencode at
   TierHarness: `reflect.DeepEqual(l.PaneArgs("/s"), l.Args)`). Run: fail.
   Implement §4.1 and the two methods. Run: pass.
3. **Call sites.** §4.2. `go build ./...` and `go vet ./...` clean. Grep
   `internal/relay/*_test.go` for `headlessLaunch(` and `PrintArgs(`;
   update any direct caller with a state argument of `"/s"` and list each
   in the report.
4. **README.** §5.
5. **Gate.** `make check` clean; `git status --short` covers only §2;
   `go.mod`/`go.sum` unchanged; copy spec and plan; ONE squashed commit.
   Report: tests written; the two mutation checks of spec §3 run, the
   named failure seen, reverted.
