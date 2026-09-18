# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Static HTML/CSS. No build step, no dependencies, no framework. Chosen by the user
in intake. Deploy target undecided; the choice constrains nothing, since a single
static page deploys anywhere.

## Users

**Primary:** a developer who already runs [herdr](https://github.com/herdrdev/herdr)
and drives more than one AI coding agent at a time from the terminal — one agent
planning, one or more implementing. They work on Linux or macOS, live in a terminal
workspace manager, and are currently moving plans and reports between agent panes by
hand with copy-paste.

**Their situation at the moment they hit the site:** they have felt the copy-paste
loop. They are not looking to be convinced that multi-agent coding is worthwhile —
they are already doing it, manually, and want the mechanical part automated.

**Secondary (inferred from the repository, not confirmed by the user):** a developer
evaluating herdr-based agent workflows who has not installed herdr yet. relay is
useless to them until they do, so the site must not pretend otherwise.

## Product Purpose

relay automates the plan/report handoff between two AI coding agent panes running
under herdr. A human talks to a **planner** agent; the planner hands work to a
**builder** agent; relay moves the files between them so the human never copy-pastes
a plan or a report by hand.

Success is that the human stays in conversation with the planner at all times, and
the mechanical transport disappears.

## Positioning

**relay makes no judgements.** It moves files, types prompts, and watches herdr's
live agent state. Whether a report is good, whether a question needs a human,
whether the work is done — every one of those decisions stays with the planner or
the human, at every step. That refusal to summarise, rewrite or decide is the
mechanism a neighbouring "AI orchestrator" could not truthfully copy, because almost
all of them exist to make exactly those judgements.

Three structural commitments follow from it and are equally uncopyable by anything
that automates judgement:

- **File handoffs, not screen scraping.** Agent TUIs run on the alternate screen and
  completed responses scroll out of reach; relay hands work over as files on disk
  (`NNN-plan.md`, `NNN-report.md`), so nothing depends on reading a terminal
  correctly.
- **Panes are yours.** relay opens exactly one pane per binding and closes a pane in
  exactly two places. A broken, orphaned or timed-out binding is flagged and
  reported, never cleaned up — the builder's terminal is often the only record of
  why a round went wrong.
- **The anti-clobber rule.** relay cannot see what a human has half-typed, so a
  focused planner pane is treated as unsafe to inject into. The payload is held and
  a notification sent instead of typing over the person.

## Operating Context

- **Hard dependency:** herdr 0.8.2+ on `PATH`. herdr owns the panes; everything relay
  observes or controls goes through the `herdr` CLI. relay is useless without it.
- **Two agent harnesses** that herdr can drive — one planner, one builder. relay knows
  how to start `agy`, `claude` and `opencode`.
- `git` on `PATH` is optional; without it relay works normally but rounds capture no
  diffs.
- **Linux and macOS only.** Windows is not supported — the blocker is herdr, not
  relay. The tree cross-compiles for `windows/amd64` and refuses at runtime with a
  clear error rather than running without a state lock.
- Installed as a herdr plugin (`herdr plugin install fuad-daoud/relay`), as a release
  binary, or `go install`. Go 1.22+ to build from source.
- Two processes: the `relay` CLI, invoked by the planner through its Bash tool, and
  `relay daemon`, one reconciler per herdr session, run under systemd or launchd.
- State lives in `$XDG_STATE_HOME/relay` (default `~/.local/state/relay`); config
  resolves through `os.UserConfigDir()`.

## Capabilities and Constraints

**The round loop.** `relay bind` opens a builder and binds it to the working tree.
`relay send --file plan.md` hands it the plan. `relay status` / `relay wait` watch the
round. `relay pull` prints the report. `relay done` stops relaying. The builder writes
`NNN-report.md` and then creates an empty `NNN-done` marker as its last action; relay
closes the round on that marker.

**Command surface** (the site draws from this; it is not the full reference):

| Verb | Does |
|---|---|
| `bind` / `add` / `fork` | start a binding, attach a peer builder on its own worktree, or branch from an earlier round |
| `send` / `pull` / `answer` | hand out a plan, fetch a report without typing into a pane, answer a blocked builder's dialog |
| `status` / `wait` / `log` / `diff` / `ui` | observe: rows, blocking wait with meaningful exit codes, round log, captured patch, full-screen reader |
| `candidates` / `policy` / `unavailable` / `available` | which harness+model fills a role, in what order, and what is currently rate-limited |
| `doctor` | preflight herdr, the daemon, each harness binary, integrations and role files |
| `done` / `unbind` / `gc` | the destructive verbs; all require an explicit name or `--pick` |

**Distinctive capabilities:**

- **Headless builders** (`--headless`) — a process instead of a pane. One fresh
  process per round, streamed JSON rendered into a live log, no tab, no idle harness
  holding memory (an idle opencode builder is roughly 800 MB). No dialogs, no memory
  across rounds.
- **Candidates and policy** — a candidate is `harness/provider/model`. relay ships
  none: which model you are entitled to run is a fact about your accounts, not about
  relay. `policy.json` sets the order to try them in per role.
- **Availability ledger and mid-round switching** — `relay unavailable` gates a
  *provider* (that is who enforces the quota, not the model). The daemon can replace
  a builder mid-round, hand the replacement the same round's plan, and log what was
  tried and why. Bounded by `max_switches`, default 2.
- **Consults** — `relay ask --role reviewer` spawns a one-shot agent beside a
  binding. Not persistent, does not advance the round, does not count against the
  one-writer-per-tree rule.
- **Round diff capture** — each round's patch, plus between-round drift.
- **Four display states** — ACTIVE, NEEDS YOU, HELD, DONE. Plus BROKEN and ORPHANED,
  which are flagged and left for a human.
- **Lifecycle hooks** — `state_changed` and `round_started` scripts, 10s timeout.
- **Status line** — `relay statusline` under the Claude Code prompt.
- **Roles relay ships definitions for:** `plan-executor` (the builder), `researcher`
  (read-only, for the builder's own sub-agents, because exactly one agent may write
  to a working tree), `reviewer` (consults), and `architect` — the planner persona
  relay ships but never launches.

**Explicit non-goals, from the design document:** no summarising or rewriting, no
judgement about whether work is done, no modification of herdr, no scheduling, no
auto-merging of builder trees. (The original "no headless execution" non-goal has
since been superseded — headless builders shipped.)

**Undecided:** deploy target and domain for the site.

## Brand Commitments

- Name is lowercase **`relay`**, always. Never "Relay" mid-sentence, never "RELAY".
- Repository: `github.com/fuad-daoud/relay`. Licensed MIT.
- Voice, inherited from the README and design doc: plain, exact, unhedged. States
  what a thing does and what it refuses to do. Explains the reason behind a
  constraint rather than asserting a benefit. No superlatives, no "effortlessly",
  no "supercharge".

## Evidence on Hand

**Real and verifiable:**

- Public GitHub repository, MIT license, CI badge and workflow.
- Plugin manifest version `0.2.0`, `min_herdr_version = "0.8.2"`.
- Release binaries for Linux and macOS, amd64 and arm64.
- The literal command surface, flag names, config file shapes and output formats
  quoted in this file and in `README.md`.
- Star count and release tag, should the site choose to read them.

**Absent — must never be fabricated:**

- No screenshots, no asciinema casts, no video of relay running.
- No users, customers, testimonials, case studies or quotes.
- No adoption numbers, no download counts, no benchmarks, no uptime figures, no
  "saves N hours" claim.
- No pricing, no hosted service, no company.

Terminal output shown on the site is authored from relay's real output formats as a
**demonstration**, and must be labelled as such rather than presented as a capture of
a real session.

## Product Principles

1. **Mechanism, not judgement.** The site explains what relay moves and what it
   refuses to decide. Any copy that implies relay evaluates work is wrong about the
   product.
2. **The constraint is the feature.** Every restriction — one writer per tree, panes
   are yours, `done` refuses to guess a binding, file handoffs over screen reads —
   exists for a stated reason. Say the reason; it is more persuasive than the
   capability.
3. **herdr first, honestly.** herdr is a hard dependency, not an integration. The
   site must say so early enough that nobody installs relay and discovers it later.
4. **Show the real surface.** The commands, flags and output on the page are relay's
   actual ones. A prettified fake command is a lie about a tool whose entire
   interface is text.
5. **Claim nothing that is not on disk.** No invented metric, user or outcome, in any
   layout position, however much the design wants a number there.

## Accessibility & Inclusion

No product-specific requirement was established in intake. The general floor applies:
WCAG AA contrast, visible focus, keyboard reachability, `prefers-reduced-motion`
honoured.
