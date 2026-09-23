# The plugin's human half: `/relay:status`, `/relay:diff`, `/relay:log`, a plugin-version doctor row, and two stale strings (#123)

One small feature in one round. This plan stands alone: everything you
need is in this file and in the tree. If a step is impossible as written
or contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a worktree of this repo. Do not run `make
check` or `make e2e` (the planner runs them); run the gate in §6 exactly as
written. Every command in the foreground; no sub-agents for edits. No git
fetch/rebase.

Spec: `docs/specs/2026-09-23-planner-commands-design.md`, in your base
commit. **Read it first**, especially §3 -- it explains why this round
adds **no skill**. Do not create `claude-plugin/skills/`.

## Working efficiently

- Read each file in §2 once before editing it; the changes are small and
  local. Do not survey the rest of the repo.
- Batch independent reads into one message.
- The three command files are near-identical: write `status.md` from §3,
  then derive the other two from it.

## 1. System overview

The relay Claude Code plugin gives the **model** an MCP server (three
tools: `status`, `send`, `done`) and a `SessionStart` hook. It gives the
**human** nothing: the operator still types shell lines or spends a model
turn on `relay status`. This round adds three slash commands over the read
verbs a human reaches for, one advisory `relay doctor` row that compares
the installed plugin's version with the running binary, and fixes two
stale strings. It changes no delivery, channel, claim or MCP tool code.

## 2. File structure

```
claude-plugin/commands/status.md          /relay:status -- `relay status $ARGUMENTS`
claude-plugin/commands/diff.md            /relay:diff   -- `relay diff $ARGUMENTS`
claude-plugin/commands/log.md             /relay:log    -- `relay log $ARGUMENTS`
cmd/relay/plugin_commands_test.go         reads the three .md files and main.go; executes nothing
internal/doctor/planner.go                + pluginVersionCheck; PlannerCheckInput gains Running string; PlannerChecks calls it after the two existing plugin rows
internal/doctor/planner_test.go           + one table case per row of §4
cmd/relay/doctor.go                       set in.Running = buildVersion() where PlannerCheckInput is built (~line 493)
claude-plugin/.claude-plugin/plugin.json  description: drop "answer" (the tool was deleted by #303)
CLAUDE.md                                 "Verifying a builder's work": the gofmt sentence (see §5)
README.md                                 under the existing Claude Code plugin section: one line per command
```

No other file changes. Do not touch `internal/mcp/*`, `internal/relay/*`,
`claude-plugin/hooks/*`, `.claude-plugin/marketplace.json` or any version
string.

## 3. The commands

Write `claude-plugin/commands/status.md` exactly:

````markdown
---
description: Show relay's bindings for this planner
argument-hint: "[--name <binding>] [--all]"
allowed-tools: Bash(relay:*)
---

```!
relay status $ARGUMENTS
```

The block above is relay's live state for this planner, already fetched.
Say only what changed or what needs a decision. Do not call the relay
status tool or run `relay status` again -- you already have the answer.
````

Then the other two, same shape:

| file | description | argument-hint | preamble line | closing prompt |
|---|---|---|---|---|
| `diff.md` | `Show what a builder changed in a round` | `"[<binding>] [--round N]"` | `relay diff $ARGUMENTS` | the diff is already here; summarise it against what the round was for; do not run `relay diff` again |
| `log.md` | `Show a binding's round log` | `"<binding> [--round N]"` | `relay log $ARGUMENTS` | the log is already here; say what happened and anything that needs a decision; do not run `relay log` again |

The hints differ on purpose: `status` takes the binding as `--name`,
`diff` takes it positionally and defaults to the binding for the cwd, and
`log` requires it positionally. Verify each against the CLI before
writing it (`go run ./cmd/relay status --help`, `... log` with no args,
`... diff` with no args) and **halt if any differs** from this table.

No argument validation or rewriting in the files.

**Preamble syntax.** The bang-fenced block (three backticks followed by
`!`) is the Claude Code form this plan was written against. If the Claude
Code docs installed on this machine describe a different form, halt and
report rather than guessing.

## 4. The doctor row

In `internal/doctor/planner.go`:

1. `PlannerCheckInput` gains:

   ```go
   // Running is the relay binary's own version (buildVersion()), compared
   // with the installed plugin's by the plugin version row. "" skips it.
   Running string
   ```

2. `PlannerChecks`, inside the existing `if in.Claude { ... }` block, after
   the two current rows, appends `pluginVersionCheck(in.Home, in.Running)`.

3. New function:

   ```go
   // pluginVersionCheck compares the installed relay@* plugin's version with
   // the running binary's release version. Advisory: the MCP server is the
   // binary on PATH, so a stale plugin serves current tools -- what it
   // carries stale is its manifest and hooks.json, whose concrete symptom
   // the plugin hook row already fails.
   func pluginVersionCheck(home, running string) Check
   ```

   Rows, all `Name: "plugin version"`:

   | situation | Severity | Detail |
   |---|---|---|
   | `running == ""` | `SevOK` | `"not checked"` |
   | `~/.claude/plugins/installed_plugins.json` missing, unreadable or unparseable | `SevOK` | `"not checked (no readable ~/.claude/plugins/installed_plugins.json)"` |
   | no key in its `plugins` object whose part before `@` is `relay` | `SevOK` | `"not checked (relay plugin not installed)"` |
   | the entry's `version`, or `running`, fails `release.ParseVersion` | `SevOK` | `"not checked (relay is <running>)"` |
   | equal `Major`, `Minor`, `Patch` | `SevOK` | `"plugin <plugin> matches relay"` |
   | otherwise | `SevWarn` | `"plugin <plugin>, relay <running>"`, `Fix: "claude plugin update relay@relay"` |

   The file's shape on this machine: `{"version": 2, "plugins":
   {"relay@relay": [{"version": "0.8.0", "installPath": "...", ...}]}}`.
   Parse that shape with a small struct -- **do not reuse
   `installedPluginDirs`**, which scans every string for a path and cannot
   yield a version. If several entries exist for the key, use the first.

   Compare `Major`, `Minor`, `Patch` only; ignore `Suffix`, so
   `v0.8.0-15-gd664545` matches `0.8.0`.

4. `cmd/relay/doctor.go`: where `doctor.PlannerCheckInput{Home: ...}` is
   built, set `Running: buildVersion()`.

## 5. The two stale strings

- `claude-plugin/.claude-plugin/plugin.json`, `description`: it lists
  `status/send/answer/done as tools`. #303 deleted `relay answer` and the
  `answer` tool (`internal/mcp/tools.go` registers `status`, `send`,
  `done`). Make it `status/send/done as tools`; change nothing else in the
  file.
- `CLAUDE.md`, "Verifying a builder's work": the sentence says `make check`
  adds "`gofmt -l .` over the whole tree". #316 changed the Makefile to
  `gofmt -l $(git ls-files '*.go')` -- tracked files only, so an ignored
  directory (an agent worktree under `.claude/worktrees/`) cannot fail the
  gate. Replace the phrase with "`gofmt` over every tracked `.go` file".
  Read `Makefile` line 16 first to confirm; halt if it says otherwise.

## 6. Steps and gate

1. **Commands** -- the three `.md` files (§3). Verify the three CLI shapes
   first, as §3 says.
2. **Command test** -- `cmd/relay/plugin_commands_test.go`:
   - glob `../../claude-plugin/commands/*.md`; fail if it finds fewer than
     three;
   - per file: a leading `---` frontmatter block with a non-empty
     `description:` line and an `allowed-tools:` line whose value is
     exactly `Bash(relay:*)`;
   - exactly one line in the file starting `relay `, and its second word
     is one of the verbs `main.go` dispatches -- collect those by
     regexp over `main.go`'s `case "..."` labels (a label may list
     several strings, e.g. `case "help", "-h"`). Read files only; execute
     nothing.

   Prove it bites: change `status.md`'s preamble to `relay stattus
   $ARGUMENTS`, run the test and confirm it **fails** naming the verb,
   restore, confirm it passes. Report both observations.
3. **Doctor row** -- §4, with table tests in `planner_test.go` for every
   row, including: a dev build (`v0.8.0-15-gd664545`) against `0.8.0`
   (OK), `0.7.0` against `v0.8.0` (Warn), `(devel)` (not checked),
   missing file, `not json`, and a file with no relay entry. Follow the
   existing tests' way of pointing `Home` at a `t.TempDir()`.
4. **Strings** -- §5.
5. **README** -- one line per command, quoted in your report.

Gate, in the worktree root, pasting each tail:

```
gofmt -l $(git ls-files '*.go')      # must print nothing
go vet ./...
go test ./... 2>&1 | tail -30
```

Commit on the current branch, one commit:

```
feat(plugin): /relay:status, /relay:diff and /relay:log for the human, and a plugin version row in doctor (#123)
```

Report: per step, step 2's two observations, the gate output, and anything
you left out and why.
