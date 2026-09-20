# codex follow-ups: the sandbox vs. relay's state dir, and doctor's version parse

Amends `docs/specs/2026-09-19-codex-harness-design.md` after the first live
rounds on codex-cli 0.155.1 (2026-09-20).

## 1. What the smoke rounds showed

At tier `harness` (and therefore `edit`), `codex exec` runs with sandbox
`workspace-write` rooted at the `-C` worktree. Two things relay's contract
needs are outside that root:

- the round's report and done marker, under `~/.local/state/relay/<name>/`
  -- codex refused the write (`patch rejected: writing outside of the
  project`) and the round exited 0 with no report;
- this repo's Go build cache, `~/.cache/go-build` -- `go build ./...` failed
  with `read-only file system`.

At tier `yolo` (`--dangerously-bypass-approvals-and-sandbox`) both work and
the round completed: report, marker, luna/medium researcher, usage row.

Separately, `relay doctor` reports `version: warn unparseable version
"codex-cli"`: `doctor.BinaryVersion` returns the first whitespace field of
`--version`, and codex prints `codex-cli 0.155.1`. opencode prints
`opencode v2.0.8`, so any future opencode floor would fail the same way.
claude (`2.1.278 (Claude Code)`) and agy (`1.2.7`) print the number first.

## 2. Decisions

### 2.1 relay makes its own state dir writable: `--add-dir`

Both `codex` and `codex exec` accept `--add-dir <DIR>` ("additional
directories that should be writable alongside the primary workspace").
For kind `codex` only, relay appends `--add-dir <binding state dir>` to
BOTH launch forms, at every tier (redundant under yolo, harmless). The
state dir is `Store.Dir(name)` = `~/.local/state/relay/<name>`, which holds
the plan, the report, the marker and every consult's ask/findings files.

Mechanics in `internal/harness`:

- New placeholder `StateDirPlaceholder = "<state>"`, beside the three that
  exist. The codex print form becomes
  `exec <prompt> -p <r> -m <id> -c model_provider=<p> [-c model_reasoning_effort=<e>] --json -C <dir> --add-dir <state>`
  and the pane form
  `-p <r> -m <id> -c model_provider=<p> [-c model_reasoning_effort=<e>] --add-dir <state>`.
  Tier args and extra_args still follow, as today.
- `PrintArgs(prompt string, budget time.Duration, dir, state string) []string`
  -- one new trailing parameter, fills `StateDirPlaceholder`. Its one call
  site (`headlessLaunch`) gains a `state` parameter and `startHeadless`
  passes `rt.Store.Dir(b.Name)`.
- New `func (l Launch) PaneArgs(state string) []string`: `Args` with
  `StateDirPlaceholder` replaced by `state`, a fresh slice; identity for a
  kind whose Args carry no placeholder. `bind.go` and `ask.go` pass
  `l.PaneArgs(rt.Store.Dir(<binding name>))` to `StartAgent` instead of
  `l.Args`. Postcondition, same as PrintArgs: no placeholder string remains.

Not done: making build caches writable. That is project-specific;
README says how (`--tier yolo`, or in `~/.codex/config.toml`
`[sandbox_workspace_write] writable_roots = ["/home/<you>/.cache/go-build"]`).

### 2.2 The researcher profile's `sandbox_mode` under yolo

The child rollout of the yolo round shows the researcher running with
`danger-full-access`: the CLI bypass flag overrides the role profile's
`sandbox_mode = "read-only"`. The key stays (it holds at harness/edit);
README states that under yolo the researcher's read-only-ness rests on
its instructions alone, the same footing as claude's and opencode's.

### 2.3 doctor: pick the version field, not the first field

`realEnv.BinaryVersion` returns the first whitespace-separated field of
the trimmed `--version` output that `looksLikeVersion` accepts -- a
field that, after an optional leading `v`, starts with a digit -- and
falls back to the first field when none does (so an unparseable output
still reaches `semverAtLeast` and produces the existing warning). New
pure `func versionField(out string) (string, error)` in `env.go`:
`ErrEmptyVersion` (existing "empty version output" text) on no fields.

## 3. Tests

- `TestSplitEffort`, `TestLaunchCodex`, `TestLaunchPrintPerKind` (codex
  row) updated for the trailing `--add-dir StateDirPlaceholder`;
  `TestLaunchCodex` also checks `PaneArgs("/s")` fills it and leaves
  `Args` untouched, and `PrintArgs("hi", time.Hour, "/w", "/s")` yields
  `... -C /w --add-dir /s`.
- `TestPaneArgsIdentityForOtherKinds`: for agy, claude, opencode
  `PaneArgs("/s")` equals `Args`.
- `TestPrintArgsSubstitutesPromptAndBudgetWithoutMutating` gains the
  state argument (value irrelevant for agy).
- `TestVersionField` in `env_test.go`: `codex-cli 0.155.1` -> `0.155.1`;
  `opencode v2.0.8` -> `v2.0.8`; `2.1.278 (Claude Code)` -> `2.1.278`;
  `1.2.7` -> `1.2.7`; `codex-cli` -> `codex-cli` (fallback, no error);
  `""` -> error.
- Mutation checks: drop `--add-dir` from the print form ->
  `TestLaunchCodex` fails; make `versionField` return `fields[0]` ->
  `TestVersionField` fails on the codex and opencode cases.

## 4. Live verification (planner)

Install, `relay doctor` shows `codex version ok 0.155.1 (floor 0.155.0)`;
a headless round on a `--tier edit` codex binding delivers its report
(the build step is expected to fail on the Go cache unless
`writable_roots` is set; the plan for that round must not need a build).
