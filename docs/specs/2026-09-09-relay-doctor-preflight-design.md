# relay doctor, and shipping the plan-executor role

Status: design approved, not yet implemented.
Issue: [#24](https://github.com/fuad-daoud/relay/issues/24).
Date: 2026-09-09.

## 1. System overview

relay installs cleanly on someone else's machine and then does not work. The
binary is portable -- XDG paths honoured, no absolute paths, generic CI -- but
everything relay needs *past* the binary is assumed rather than shipped or
checked: the builder's `plan-executor` role, a model whose provider is
configured, and a herdr lifecycle integration for the harness. None of it fails
loudly. The most likely first run is a binding that sits in `unknown` forever,
because without a harness's integration herdr reports `unknown`, and relay --
correctly -- never treats `unknown` as done. The user watches nothing happen and
concludes relay is broken.

This spec covers two of #24's six items, the two that carry the most of that
harm:

1. **`relay doctor`** -- a preflight that checks herdr, the daemon, each
   harness binary, each herdr integration, and each `plan-executor` role file,
   then prints what is missing and the exact command that fixes it.
2. **`relay agent print`** -- the `plan-executor` definition shipped inside the
   binary, emitted to stdout so the user can redirect it into their harness's
   config directory.

Plus one small change on the hot path: `relay bind` prints the non-OK rows for
the alias it is about to spawn, and binds anyway.

The remaining #24 items stay open; section 11 lists them.

## 2. Constraints the design is derived from

These are the facts the rest of the spec follows from. Each one rules out an
otherwise obvious design.

1. **relay never writes outside its own config.** Doctor reports and prints
   commands; it does not install anything, and there is no `--fix`. The
   `plan-executor` file lands in `~/.claude/agents/` because the user's shell
   redirect put it there, not because relay wrote it. This is the same posture
   as the rest of relay: it does not act on the user's behalf.

2. **The repo may not exist on the user's disk.** `herdr plugin install
   fuad-daoud/relay` fetches a release binary. A fix line of the form `cp
   dist/agents/plan-executor.md ...` is wrong or unresolvable for that install,
   so the role definition must be reachable *from the binary*. Hence `go:embed`
   plus `relay agent print`.

3. **`go:embed` cannot reach outside its own package directory.** The embedded
   definitions therefore live at `internal/harness/agents/`, not in `dist/`
   beside the service units. They remain browsable on GitHub.

4. **A herdr agent kind is not its integration target.** `herdr agent start
   --kind` accepts `agy`; the matching `herdr integration install` target is
   `antigravity-cli`. Any check that maps one to the other needs an explicit
   table.

5. **`herdr integration status` has no `--json`.** Output is one line per
   target, `"<target>: <state> (<path>)"`, with states observed as
   `not installed`, `outdated (vN < vM)`, and an installed-and-current form.
   It must be parsed as text, and its absence (it may not exist at the declared
   0.8.2 floor) must degrade to a warning rather than an error.

6. **Most users will have one harness, and relay checks three.** A harness the
   user does not have is not a problem. The severity model in section 6 exists
   entirely to keep a single-harness machine from reading as three failures.

## 3. Repository layout

New files:

```
internal/harness/
  harness.go              the kind -> (binary, integration, role) table
  harness_test.go
  agents.go               go:embed of the definitions, lookup by doc key
  agents_test.go
  agents/
    plan-executor.claude.md     claude flavour: `name:` frontmatter, Agent tool
    plan-executor.opencode.md   opencode flavour: `mode: all`, Task tool
```

The two `agents/*.md` files are **already committed alongside this spec** --
copied verbatim from the author's working definitions. Do not rewrite,
reformat, or "improve" them; `go:embed` them as they are. Everything else in the
list is still to be written.

```

internal/doctor/
  doctor.go               Check, Severity, Report, Run
  env.go                  the Env interface and its real implementation
  doctor_test.go
  env_test.go             the `herdr integration status` parser

docs/specs/2026-09-09-relay-doctor-preflight-design.md   (this file)
```

Modified files:

```
cmd/relay/main.go         `doctor` and `agent` subcommands in the run() switch;
                          the bind-time warning inside cmdBind (main.go:213);
                          help text
cmd/relay/doctor.go       cmdDoctor: scope assembly, rendering, exit code
cmd/relay/agent.go        cmdAgent / cmdAgentPrint
internal/herdr/client.go  Version, IntegrationStatus
README.md                 a "first run on a clean machine" path
```

## 4. `internal/harness` -- the kind/machine table

One exported type and two functions. This is the only place in relay that knows
how a herdr agent kind relates to things on the machine.

```go
// Harness describes how one herdr agent kind appears on the local machine.
type Harness struct {
    Kind        string // herdr agent kind, as passed to `herdr agent start --kind`
    Binary      string // executable name looked up on PATH
    Integration string // herdr integration target; "" when the harness has none
    RolePath    string // home-relative path to the role file; "" when the role
                       // is selected by preamble rather than by a file
    RoleDoc     string // key into the embedded definitions; "" when RolePath is ""
}

// Lookup returns the entry for a kind. ok is false for a kind relay was not
// taught, which is not an error: callers degrade to what they can check.
func Lookup(kind string) (h Harness, ok bool)

// All returns every known entry, sorted by Kind for stable output.
func All() []Harness
```

The table:

| Kind | Binary | Integration | RolePath | RoleDoc |
| --- | --- | --- | --- | --- |
| `agy` | `agy` | `antigravity-cli` | *(empty)* | *(empty)* |
| `claude` | `claude` | `claude` | `.claude/agents/plan-executor.md` | `claude` |
| `opencode` | `opencode` | `opencode` | `.config/opencode/agents/plan-executor.md` | `opencode` |

Notes that the implementer must not "tidy away":

- `agy`'s `Integration` is `antigravity-cli`, and that is correct. It is the
  one row that looks like a typo and is not.
- `agy` has empty `RolePath`/`RoleDoc` because agy has no `--agent` flag; the
  `abuilder` alias selects the role with a `Preamble` on round 1's prompt. The
  role for agy is **not** a file, and doctor must say so rather than reporting
  a missing one.
- `opencode`'s role directory is `agents/` (plural). Verified on disk.
- `RolePath` is home-relative and joined with `os.UserHomeDir()` at check time.
  It is deliberately not XDG-derived: these are other tools' paths, and they are
  what those tools actually use, not what relay would prefer.

**Unknown kinds.** A user's `aliases.json` may name `codex`, `droid`, or
anything else herdr supports. `Lookup` returns `ok == false` and the caller
falls back to: binary name equals the kind; integration target equals the kind
*if* `herdr integration status` lists it, otherwise not checked; role not
checked. The role row for an unknown kind is `SevOK` with the detail `not
checked -- relay has no role path for kind "codex"`. Relay does not claim to
know harnesses it was not taught, and an unknown harness is never a problem.

### Embedded definitions

```go
//go:embed agents/plan-executor.claude.md agents/plan-executor.opencode.md
var agentFS embed.FS

// AgentDoc returns the embedded plan-executor definition for a RoleDoc key.
// ErrNoAgentDoc reports a key with no embedded definition.
func AgentDoc(key string) ([]byte, error)

var ErrNoAgentDoc = errors.New("no embedded agent definition")
```

The two definitions are the author's working files, which differ only in
frontmatter and one tool name: claude uses `name: plan-executor` and refers to
the **Agent** tool; opencode omits `name`, adds `mode: all`, and refers to the
**Task** tool. Both are copied in verbatim. They are worked definitions, not a
specification of the role -- but they are the thing the loop actually depends
on, so they ship.

## 5. `internal/doctor` -- checks as data

```go
type Severity int

const (
    SevOK Severity = iota // nothing to do; also used for "not checked"
    SevWarn               // wrong, but relay can still run
    SevFail               // relay cannot run
)

// Check is one probe's result. Fix is a literal command the user can paste,
// never prose, and is empty when Severity is SevOK.
type Check struct {
    Group    string   // "" for global rows, else the harness kind
    Name     string   // "herdr", "daemon", "binary", "integration", "plan-executor"
    Severity Severity
    Detail   string   // what was actually found
    Fix      string   // the command that fixes it
}

// Report is every check, in render order, plus the derived verdict.
type Report struct {
    Checks []Check
    // UsableBuilder is true when at least one checked kind has a complete
    // path: binary on PATH and integration installed. It is the verdict the
    // footer states and the exit code follows.
    UsableBuilder bool
}

// Failures and Warnings count by severity.
func (r Report) Failures() int
func (r Report) Warnings() int

// Run executes every check for the given kinds against env.
// kinds is the caller's choice of scope; Run does not discover it.
func Run(ctx context.Context, env Env, kinds []string) Report
```

`Run` never returns an error. A probe that fails becomes a `Check` saying so;
that is the whole point of a diagnostic.

### The Env interface

Every external fact comes through `Env`, so the package is testable on a
machine with no herdr and no harnesses installed.

```go
type IntegrationState struct {
    Installed bool   // a hook file is in place
    Outdated  bool   // installed, but older than herdr expects
    Detail    string // herdr's own words, e.g. "outdated (v10 < v11)"
}

type Env interface {
    // HerdrVersion returns the parsed semver of the herdr CLI.
    HerdrVersion(ctx context.Context) (string, error)
    // IntegrationStatus returns every target herdr knows, keyed by target name.
    IntegrationStatus(ctx context.Context) (map[string]IntegrationState, error)
    // DaemonRunning reports whether a relay daemon holds the lock.
    DaemonRunning(ctx context.Context) (bool, error)
    // LookPath resolves an executable on PATH.
    LookPath(binary string) (string, error)
    // HomePath joins a home-relative path, and Stat reports whether it exists.
    HomePath(rel string) (string, error)
    Stat(path string) error
}
```

The real implementation (`env.go`) wraps `internal/herdr.Client`, `os/exec`,
`os.Stat`, `os.UserHomeDir`, and `store.AcquireDaemonLock`'s
`ErrDaemonRunning` probe. Two methods are new on `herdr.Client`:

```go
// Version runs `herdr --version` and returns the bare semver.
func (c *Client) Version(ctx context.Context) (string, error)

// IntegrationStatus runs `herdr integration status` and parses its lines.
func (c *Client) IntegrationStatus(ctx context.Context) (map[string]IntegrationState, error)
```

`DaemonRunning` reuses the existing lock rather than inventing a second
mechanism: attempt `AcquireDaemonLock`, and `errors.Is(err, ErrDaemonRunning)`
means a daemon is up. Release immediately on success. This is exactly what
`relay daemon --check` already does.

### The `herdr integration status` parser

Line form, one per target:

```
opencode: outdated (v10 < v11) (/home/fuad/.config/opencode/plugins/herdr-agent-state.js)
pi: not installed (/home/fuad/.pi/agent/extensions/herdr-agent-state.ts)
```

Parse rule: split on the **first** `": "`. The remainder is a state followed by
a parenthesised path; the path is the **last** parenthesised group, because
`outdated (v10 < v11)` contains one of its own. Classify the state prefix:
`not installed` -> `{Installed: false}`; `outdated` -> `{Installed: true,
Outdated: true}`; anything else -> `{Installed: true}`. Keep herdr's own text
in `Detail` verbatim, so a state relay has not seen before still renders
something truthful. An unparseable line is skipped, not fatal.

## 6. Severity and the verdict

The rule that matters, stated once: **relay fails only when it cannot run at
all.** Everything else warns.

`SevFail` is reserved for exactly two conditions:

1. **herdr is missing, unparseable, or below the floor.** herdr is a hard
   runtime dependency, not an integration -- it owns the panes. The floor is
   `0.8.2`, read from a single constant that already backs the README and the
   plugin manifest's `min_herdr_version`. Doctor reads the floor; changing it is
   out of scope.

2. **Zero complete builder paths.** A path is complete when a checked kind has
   its binary on PATH *and* its integration installed (`outdated` still counts
   as installed). A missing role file does not break completeness -- the user may
   define the role elsewhere, or under another name -- so it is always a warning.

Per-row severities:

| Row | Condition | Severity |
| --- | --- | --- |
| herdr | absent, unparseable, or `< 0.8.2` | `SevFail` |
| daemon | not running | `SevWarn` |
| binary | not on PATH | `SevWarn`, and **the rest of that harness's rows are skipped entirely** |
| integration | not installed, on a harness whose binary *is* present | `SevFail`, demoted to `SevWarn` by the second pass below if some other kind is complete |
| integration | outdated | `SevWarn` |
| integration | `herdr integration status` unavailable | `SevWarn` |
| plan-executor | role file missing, harness present | `SevWarn` |
| plan-executor | kind has no role file (`agy`) | `SevOK`, detail `selected by preamble, not a file` |
| plan-executor | unknown kind | `SevOK`, detail `not checked` |

### The second pass

Row severities as listed are *local* judgements, and one of them can contradict
the rule at the top of this section. Consider a machine with claude complete and
opencode installed but missing its integration: the opencode row is locally a
failure, yet relay runs fine with `cbuilder`. Exiting 1 there would contradict
"relay fails only when it cannot run at all".

So `Run` computes the report in two passes:

1. Probe everything and assign local severities per the table above.
2. If `UsableBuilder` is true, demote every `integration: not installed` row
   from `SevFail` to `SevWarn`.

The demoted row keeps its detail text in full -- that harness still cannot
finish a round, and the user must be told -- it simply stops being the reason
relay exits non-zero. When `UsableBuilder` is false nothing is demoted, so the
rows that explain *why* there is no usable builder stay failures. This keeps one
source of truth for the exit code and the footer: they both read
`Failures()`.

Two of those rows carry the design's whole intent and must not be softened:

- **A missing integration on an installed harness is the failure.** It is the
  silent stall at the heart of #24: the binding reports `unknown` forever and
  never finishes a round. It is also what makes the "zero complete paths"
  verdict land on the right row.
- **A harness whose binary is absent produces one warning row and nothing
  else.** Relay stops asking questions about a harness the user does not have.
  This single rule is what keeps a one-harness box from reading as three
  problems.

### Scope: which kinds get checked

`cmdDoctor` assembles the kind list -- `doctor.Run` does not discover it:

1. Every `Kind` in the effective alias table, i.e. `alias.LoadTable` with the
   user's `aliases.json` already layered over the built-ins. On a clean box
   that is the three built-in kinds, which is the intended "everything relay
   could need".
2. Every `Kind` on an existing binding's builder endpoint.

Deduplicated, sorted. A user who has replaced the table with one entry is
checked on one harness.

## 7. Output

Rendered to stdout. Global rows first, then one block per kind. `SevOK` rows
print `ok`, warnings print `warn`, failures print `FAIL`. A non-empty `Fix`
prints on its own indented `fix:` line. The trailing line states the counts and
the verdict in plain words.

A machine with everything installed but three stale integrations:

```
herdr                     ok       0.9.0 (floor 0.8.2)
daemon                    ok       running

opencode
  binary                  ok       /usr/bin/opencode
  integration             warn     outdated (v10 < v11)
    fix: herdr integration install opencode
  plan-executor           ok       ~/.config/opencode/agents/plan-executor.md

claude
  binary                  ok       /usr/bin/claude
  integration             warn     outdated (v8 < v9)
    fix: herdr integration install claude
  plan-executor           ok       ~/.claude/agents/plan-executor.md

agy
  binary                  ok       ~/.local/bin/agy
  integration             warn     outdated (v2 < v3)
    fix: herdr integration install antigravity-cli
  plan-executor           ok       selected by preamble, not a file

3 warnings, 0 failures -- relay can run.
```

A clean machine with only claude installed -- the case #24 is about:

```
herdr                     ok       0.8.2 (floor 0.8.2)
daemon                    warn     not running
    fix: relay daemon

opencode
  binary                  warn     not on PATH -- skipping the rest of this harness

claude
  binary                  ok       /usr/bin/claude
  integration             FAIL     not installed -- this binding will report
                                   `unknown` forever and never finish a round
    fix: herdr integration install claude
  plan-executor           warn     missing: ~/.claude/agents/plan-executor.md
    fix: relay agent print --kind claude > ~/.claude/agents/plan-executor.md

agy
  binary                  warn     not on PATH -- skipping the rest of this harness

1 failure, 4 warnings -- no usable builder. Fix the failure above.
```

**Exit code:** 1 if `Failures() > 0`, else 0. Warnings never change the exit
code, so a one-harness machine with relay working exits 0.

Every `Fix` is a literal command. No fix line may say "see the README", "install
the integration", or any other instruction the user has to translate.

## 8. `relay agent print`

```
relay agent print --kind <KIND>
```

Writes the embedded definition for that kind to stdout and nothing else -- no
header, no trailing commentary, no progress line -- so `>` produces a valid
file. The intended use, which is also what doctor prints:

```
relay agent print --kind claude > ~/.claude/agents/plan-executor.md
```

Behaviour:

- `--kind claude`, `--kind opencode`: emit the definition, exit 0.
- `--kind agy`: exit 2 to **stderr** with: `agy selects its role with a preamble
  on the first prompt, not an agent file; see the abuilder alias in
  ~/.config/relay/aliases.json`.
- A kind relay was not taught: exit 2 to stderr naming the kinds that do have
  definitions.
- `--kind` omitted: exit 2 to stderr with usage. There is no default, for the
  same reason `relay bind --builder` has no default: guessing writes the wrong
  file.

`agent` is a subcommand group with one verb today. `print` is spelled out
rather than implied so a later `relay agent list` does not have to break it.

## 9. Bind-time warning

`cmdBind`, after resolving the alias spec and before spawning the pane, runs
`doctor.Run` scoped to that single kind and prints every non-`SevOK` row to
**stderr**, prefixed `relay: `. Then it binds. Always.

```
relay: claude integration not installed -- this binding will report `unknown`
relay:   and never finish a round. Fix: herdr integration install claude
relay: run `relay doctor` for the full check
```

Three rules, each of which the implementer will be tempted to break:

1. **It never blocks and never fails the bind.** A `SevFail` row prints like any
   other. relay does not refuse work on the strength of its own preflight.
2. **Probe errors are swallowed.** If `herdr integration status` errors or
   times out, print nothing and bind. The preflight must not be able to break
   or noticeably slow the hot path.
3. **An adopted pane gets the integration row only.** Binding by pane id, or
   `--resume`, means the user launched that agent themselves, in whatever role
   they chose -- the binary and role rows are none of relay's business. The
   integration row still applies, because it decides whether the round can ever
   be observed to finish.

## 10. Testing

- **`internal/doctor`**, table-driven against a fake `Env`, asserting per-row
  severities, the counts, and the verdict: herdr absent; herdr unparseable;
  herdr below floor; herdr at exactly the floor (passes); `IntegrationStatus`
  returning an error; one complete path among three kinds (exit 0); zero
  complete paths (exit 1); **the second pass** -- an installed harness missing
  its integration *beside* a complete harness demotes to `SevWarn` and exits 0,
  while the same row with no complete harness anywhere stays `SevFail` and exits
  1; binary absent suppresses that kind's later rows;
  `agy`'s role row is OK rather than a missing file; an unknown kind degrades
  without failing.
- **The status parser**, over captured real output including
  `outdated (v10 < v11)` -- whose inner parentheses are the trap -- `not
  installed`, an installed-and-current line, and an unparseable line that must
  be skipped rather than fatal.
- **`internal/harness`**: every entry with a non-empty `RoleDoc` resolves to an
  embedded file that exists and is non-empty; every entry with an empty
  `RolePath` also has an empty `RoleDoc`; `All` is sorted.
- **`relay agent print`**: output for each kind is byte-identical to the
  embedded file; `agy` and an unknown kind exit 2 and write nothing to stdout.
- **The bind warning**: tested at the function that renders the lines, not
  through a real bind. Assert that a probe error yields zero lines, and that an
  adopted bind yields only the integration row.
- **Existing tests must keep passing unchanged.** This spec adds no behaviour to
  the relay loop.

## 11. Out of scope

Remaining on #24 after this lands, deliberately:

- Whether relay ships a default alias table at all, or prints a "no aliases
  configured, here is the file to write" message instead.
- Making `--dangerously-skip-permissions` opt-in rather than shipped on
  `abuilder`.
- Decoupling `internal/alias/alias_test.go` from specific model strings, which
  currently makes the worked examples read as a contract.
~~Moving the author's personal `.gitignore` entries to `.git/info/exclude`.~~
**Done as part of this spec, because it blocked it.** `plan*.md` swallowed
`internal/harness/agents/plan-executor.claude.md` the moment it was added --
the exact harm #24 predicted, reached from an unexpected direction. `.gitignore`
now holds only `/relay` and `/dist/*.tar.gz`; the personal entries moved to
`.git/info/exclude` **anchored with a leading slash** (`/plan*.md`), because an
unanchored pattern matches at every depth and would have kept hiding the
embedded role files wherever they lived.

Also out of scope:

- **Any form of `--fix`, or a `relay init`.** relay reports; the user's shell
  acts. Section 2.1.
- **Raising the herdr version floor** off 0.8.2. Doctor reads the floor.
- **Checking that a model's provider is configured.** relay cannot know which
  providers a harness has credentials for without running it, and a wrong answer
  here is worse than no answer.
- **Packaging and install mechanics** -- [#16](https://github.com/fuad-daoud/relay/issues/16).

relay still makes no judgements. Doctor diagnoses the *machine*, never an
agent's work.

## 12. Verification

The design is done when, on a machine that is not the author's:

1. `relay doctor` on a box with no herdr exits 1 and says so as the first row.
2. `relay doctor` on a box with herdr, one harness, and no integration exits 1,
   names the integration as the failure, and prints the `herdr integration
   install <target>` line that fixes it.
3. Running that one command and re-running `relay doctor` exits 0.
4. `relay agent print --kind claude > ~/.claude/agents/plan-executor.md`
   produces a file the harness loads, and the `plan-executor` row turns `ok`.
5. `relay bind --builder cbuilder` on a box with no claude integration prints
   the warning and still binds.
6. A README "first run on a clean machine" path exists and has been followed
   end to end, by someone other than the author, to a completed round.
