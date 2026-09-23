# The Claude Code plugin's human half: `/relay:status`, `/relay:diff`, `/relay:log`, and a plugin-version doctor row (#123)

Status: designed 2026-09-23 against `main` at `d664545` (after #303, drop
herdr, landed in full). Supersedes an unmerged 2026-09-22 draft written
before #303, whose claim and doctor sections that change made wrong.

Written as if the relay -> relevo rename (#292) will not happen; it is
scheduled last and will carry these files with it.

## 1. Problem

The relay plugin (`claude-plugin/`) gives the **model** an MCP server --
three tools (`status`, `send`, `done`) and, in channel mode, pushed round
events -- and since #303 a `SessionStart` hook that registers the planner.
It gives the **human** nothing. An operator who wants to see what relay is
doing still types a shell line, or spends a model turn asking Claude to run
`relay status`.

The three read verbs a human reaches for -- `status`, `diff`, `log` -- are
exactly the ones a slash command fits: typed on demand, read-only, and
producing text that is already shaped for reading.

## 2. Goals and non-goals

Goals:

- `/relay:status [--name <binding>] [--all]`, `/relay:diff [<binding>]
  [--round N]`, `/relay:log <binding> [--round N]`: each runs the real CLI and puts its
  output in the transcript, where the human and the model both see it.
- `relay doctor` reports whether the installed plugin's version matches
  the running relay binary.
- Two stale strings fixed: `plugin.json`'s description advertises a tool
  #303 deleted, and `CLAUDE.md` describes a gofmt gate #316 replaced.
- Every new test runs with no harness, no network and no Claude Code, per
  the `cmd/relay` CI rule.

Non-goals:

- **A skill.** See §3.
- Commands for `send` or `done`. They mutate a binding; the model calls
  them as tools, and a human who wants one can ask in a sentence. A slash
  command with a mistyped argument and no confirmation step is a worse
  interface for a destructive verb.
- A `mode` field on the channel claim. The 2026-09-22 draft proposed one
  so doctor could report it; since #303 a claim is only ever written in
  channel mode (`cmd/relay/mcp.go`: `mode == mcp.ModeChannel && haveRec`),
  so a claim's existence already is the mode.
- Any change to delivery, the channel, the claim, or the MCP tools.

## 3. Why there is no skill

#123 as filed asks for "one skill that carries the round protocol". The
protocol already reaches every session that loads the plugin: the MCP
server returns it as `instructions` in its `initialize` result
(`internal/mcp/instructions.go`), and Claude Code gives that to the model
when the server connects. A skill would be a second copy of the same text
in the same plugin, differing only in when it is read.

#303 is the demonstration of the cost: it rewrote the protocol wholesale
(no pane builders, no `relay answer`, delivery by channel / deliverer /
background wait) and had to update `instructions.go` and the `architect`
definitions. A skill would have been a third place to miss.

The `architect` definitions (`internal/harness/agents/architect.*.md`)
are not a duplicate in this sense: opencode, codex and agy planners have
no MCP server to deliver `instructions`, so the definitions are their only
copy.

## 4. Mechanism

### 4.1 The commands

```
claude-plugin/commands/status.md    ->  /relay:status
claude-plugin/commands/diff.md      ->  /relay:diff
claude-plugin/commands/log.md       ->  /relay:log
```

Each is YAML frontmatter, a bang-fenced preamble that Claude Code runs
before the prompt is sent, and a short prompt telling the model what to do
with the output:

```markdown
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
```

Rules for all three:

- **Human output, not `--json`.** The CLI already shapes these for
  reading; shaping them a second time in the command is the duplication
  §3 avoids.
- **`allowed-tools` is exactly `Bash(relay:*)`**, so the preamble cannot
  become a general shell.
- **The prompt forbids re-running the verb.** A model handed a status
  block otherwise tends to call the `status` tool "to check", doubling
  the work and the tokens.
- **No argument validation or rewriting in the command.** A missing
  binding name is the CLI's error to print, and it prints a better one.
  The `argument-hint` states each verb's real shape instead -- and the
  three differ: `status` takes the binding as `--name <n>`, `diff` takes
  it positionally and defaults to the binding for the cwd, `log` requires
  it positionally. A hint that papered over that (say, a bare `[name]` on
  status) would make `/relay:status judge` fail.

### 4.2 The doctor row

#303 added two plugin rows to `relay doctor` (`internal/doctor/planner.go`):
`plugin` (is `relay@relay` enabled in `settings.json`) and `plugin hook`
(does the installed plugin's `hooks/hooks.json` run `relay planner init` on
`SessionStart`). Both appear only when a claude candidate or planner
exists (`PlannerCheckInput.Claude`).

This design adds a third beside them, `plugin version`:

| situation | Severity | Detail |
|---|---|---|
| `installed_plugins.json` missing, unreadable, or no `relay@*` entry | `SevOK` | `"not checked (...)"` |
| running relay has no parseable release version (`(devel)`, untagged) | `SevOK` | `"not checked (relay is <v>)"` |
| installed plugin version equals the running release | `SevOK` | `"plugin <v> matches relay"` |
| they differ | `SevWarn` | `"plugin <p>, relay <r>"`, `Fix: "claude plugin update relay@relay"` |

**Comparison.** Parse both with `release.ParseVersion` and compare
`Major.Minor.Patch` only, ignoring `Suffix`. A local build is
`v0.8.0-15-gd664545`; the plugin says `0.8.0`; those match. A build that
cannot be parsed is `"not checked"`, the same stance `releaseCheck` takes:
never claim a mismatch you cannot prove.

**Why a warning and not a failure.** The MCP server is the `relay` binary
on PATH, so a stale plugin does *not* serve stale tools. What it carries
is a stale manifest and a stale `hooks.json`. #303's `plugin hook` row
already FAILs the concrete symptom of that (a missing `SessionStart`
hook). This row catches the cause earlier and names the fix; it is
advisory.

**Why this is not a mismatch today.** On the author's machine the
installed plugin is 0.8.0 and relay is `v0.8.0-15-gd664545` -- the row
reads OK. It exists for the machine that installed the plugin once and
kept upgrading the binary.

## 5. Testing

- **Command files** (`cmd/relay/plugin_commands_test.go`). Reads
  `../../claude-plugin/commands/*.md` and asserts, per file: frontmatter
  parses with a non-empty `description`; `allowed-tools` is exactly
  `Bash(relay:*)`; the preamble holds exactly one line starting `relay `;
  and that line's verb is one `cmd/relay/main.go` dispatches. The verb
  list comes from main.go's `case "x":` labels by regexp -- the test
  reads files and executes nothing, so it satisfies the CI rule. This is
  the realistic failure: a verb renamed in the CLI and not in the plugin.
  It lives in `cmd/relay` because that is where main.go is and where the
  CI rule is already written down.
- **Doctor** (`internal/doctor/planner_test.go`). Table cases for every
  row of §4.2, including a dev-build suffix that must match, `(devel)`,
  a missing file and a file that does not parse.
- Nothing invokes Claude Code. The commands are checked by hand once
  after merge, as #124's channel was.

## 6. Risks

- **The preamble syntax is Claude Code's, and it has moved before.** The
  §5 test parses the files; it cannot tell whether Claude Code still
  accepts them. The hand check after merge is the real test.
- **`installed_plugins.json` is Claude Code's private file.** #303's
  `plugin hook` row already reads it through `installedPluginDirs`, so
  this adds no new dependency on its format, and every parse failure is
  `"not checked"`, never a false warning.
