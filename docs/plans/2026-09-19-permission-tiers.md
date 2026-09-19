# Permission tiers per harness: --tier read|edit|yolo|harness, max_tier, permission-blocked (#141)

Closes #141. Design in this file; no separate spec. The flag table in §3.1
was verified by the planner against the installed CLIs on 2026-09-19
(claude 2.1.278, agy 1.2.7, opencode 2.0.8); the builder does not re-verify
it and does not run any harness binary.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. A halt that surfaces a design error is the
wanted outcome; an improvised workaround is not.

**Scope guard.** Touch only the files listed in §2. Do not run `make e2e`
(the planner runs it). Do not add a CLI verb; the only new flags are `--tier`
and `--allow-yolo` on `bind`, `add`, `fork` and `send`. Do not add a module
dependency. Run every command in the foreground; dispatch no sub-agents and
start no background work. Do not widen any exported signature that §4 does
not name -- if a test outside §2 stops compiling, halt and say which. Never
execute `claude`, `agy`, `opencode` or `herdr`: CI has none of them and the
rules here are pure functions.

**Commit the plan with the work.** Copy this plan file (the path relay gave
you) to `docs/plans/2026-09-19-permission-tiers.md` in your worktree and
include it in the first commit.

**Before step 1**: run `git status --short` and `git branch --show-current`.
You must be on `relay/<binding-name>` in a worktree under
`~/.local/state/relay/.worktrees/`. If you are on `main` or in
`/home/fuad/projects/relay`, halt.

## 1. System overview

Today a candidate's permissions are whatever its `extra_args` say: agy
carries `--dangerously-skip-permissions`, opencode `--auto`, claude nothing.
The same flags reach every role the candidate serves, so a reviewer consult
is only as read-only as its prompt, and there is no way to say "this round
may edit but must ask before anything else".

This plan makes permissions a **tier**, rendered by relay into each
harness's real flags at launch. A tier is a *ceiling*: the most a harness
will do without asking. Four values, ordered `read < edit < yolo`, plus
`harness`, which is outside the order:

| tier | means |
|---|---|
| `harness` | relay adds no permission flag; the harness's own settings decide. Today's behaviour, byte for byte, and the built-in default. |
| `read` | look, don't touch: read-only tools; any edit or command is refused or asks |
| `edit` | edit files without asking; commands and anything else still ask |
| `yolo` | no prompts at all |

The rendering table lives in `internal/harness` beside `Launch`. A tier a
harness cannot honour is a refusal naming the tier and the kind, never an
approximation: opencode has no read or edit flag, so those two tiers refuse
on it; its `--auto` is `yolo`.

**Where a tier comes from**, first match wins: `--tier` on the command;
`"tier"` on the candidate in `candidates.json`; `policy.json` `tier.<role>`;
`harness`. The effective tier is stored on the binding at `bind`/`add`/`fork`
and used by every later launch of that binding (a mid-round switch spawns the
replacement at the same tier). `send --tier` overrides for one round on a
headless binding (each round is a fresh process); on a pane binding, whose
flags were fixed when the pane was spawned, it is refused with the
alternative named.

**Never silently escalate.** `policy.json` `max_tier` (default `edit`) caps
what a command may ask for without `--allow-yolo`. `policy.Load` refuses a
`tier.<role>` above `max_tier`, so a policy file can raise the cap only by
saying so in the same file. `harness` is exempt from the cap: relay adds
nothing, and the flags in the human's own config are the human's.

**`extra_args` and permission flags.** A candidate whose `extra_args`
carries one of the harness's known permission flags still launches at tier
`harness` (today's configs keep working), and `relay candidates` prints a
note on the row. At any other tier the launch refuses, naming the flag: two
sources of permission flags on one argv is how a `read` reviewer ends up
with `--dangerously-skip-permissions`.

**`permission-blocked`.** A headless builder that exits without a report and
whose log tail matches the harness's denial text is **halted**, not switched:
the exit entry's note says `permission-blocked: <line>`, the NEEDS YOU
message tells the planner to re-send with a higher tier or extend the
harness's allow list. A switch would spend `max_switches` on a config problem
and land the next candidate on the same wall; a rate-limit gate is wrong
because the provider is fine.

The plan log entry records `tier=<t>` so `relay log` shows what each round
ran with.

Out of scope, and the builder must not attempt: an OS sandbox; path
allow/deny lists; changing `relay answer`; tiers for remote builders (the
server-side binding keeps its own tier; `sendRemote` passes none and the
server resolves as if no `--tier` was given); a tier flag on `relay ask`
(consults resolve from candidate, policy, `harness`).

## 2. File structure

```
internal/harness/
  tier.go                  NEW  Tier type, ParseTier, ordering, PermissionArgs table, PermissionFlags, DenialPatterns defaults
  tier_test.go             NEW  table per cell; refusal cells; extra_args conflict; ordering; argv identical at harness
  harness.go               EDIT Harness.DenialPatterns field on every kind; Launch gains tier and returns error
  harness_test.go          EDIT existing Launch tests pass TierHarness; TestDenialPatternsSetOnEveryKind
internal/candidate/
  candidate.go             EDIT Candidate.Tier, Candidate.DenialPatterns; Load validates both
  candidate_test.go        EDIT bad tier / bad denial pattern -> error; good ones load
internal/policy/
  policy.go                EDIT Policy.Tier map, Policy.MaxTier; validation; MaxTierOrDefault, TierFor
  policy_test.go           EDIT cases
internal/store/
  types.go                 EDIT Binding.Tier, Binding.RoundTier
  log.go                   EDIT LogEntry.Tier
internal/relay/
  tier.go                  NEW  resolveTier, checkTierCap, effectiveTier, ErrTier* sentinels
  tier_test.go             NEW  resolution chain, cap, pane refusal rule (pure)
  denial.go                NEW  matchDenial, denialPatterns(candidate, harness)
  denial_test.go           NEW  fixtures per kind; candidate override; no match
  bind.go                  EDIT BindOptions.Tier/AllowYolo; create stores Binding.Tier; resolveBuilder passes tier to Launch
  add.go                   EDIT AddOptions.Tier/AllowYolo; stores Binding.Tier
  fork.go                  EDIT ForkOptions.Tier/AllowYolo; stores Binding.Tier (default: inherit source's Tier)
  send.go                  EDIT Send gains SendOptions; RoundTier; pane refusal; plan entry Tier
  headless.go              EDIT headlessLaunch takes tier; startRound uses effectiveTier; exit path: permission-blocked halt
  switch.go                EDIT resolveBuilder call passes effectiveTier(b)
  ask.go                   EDIT consult tier via resolveTier; Launch error handled
  reconcile.go             EDIT queueReport clears RoundTier with RoundSwitches/RoundExcluded
  logline.go               EDIT " tier=<t>" on plan entries
  candidates_list.go       EDIT tier column and extra_args permission-flag note
  bind_test.go             EDIT Tier stored; Launch called with the tier (fake records Args)
  send_test.go             EDIT RoundTier set/cleared; pane refusal; plan entry Tier; --allow-yolo
  headless_test.go         EDIT permission-blocked halt; argv carries the tier flags
  ask_test.go              EDIT reviewer at read renders --permission-mode plan (claude) / --mode plan (agy); opencode read refuses
  logline_test.go          EDIT tier= case
  candidates_list_test.go  EDIT tier column; note
  e2e_test.go              EDIT Send call sites pass SendOptions{} (planner omission, added after round 1)
  limit_test.go            EDIT same
  remote_test.go           EDIT same
  switch_test.go           EDIT same
internal/e2e/
  remote_test.go           EDIT same (landed on main in #214 after round 1; fixed by the planner)
internal/serve/
  rounds.go                EDIT relay.Send call passes relay.SendOptions{}
cmd/relay/
  main.go                  EDIT --tier/--allow-yolo on bind, add, fork, send; pass through
README.md                  EDIT "Permission tiers" section; candidates `tier`; policy `tier`/`max_tier`; permission-blocked; migration from extra_args
docs/plans/2026-09-19-permission-tiers.md   NEW  this file
```

## 3. Data structures & type definitions

### 3.1 `internal/harness/tier.go`

```go
// Tier is the permission ceiling relay renders into a harness's flags.
type Tier string

const (
    TierHarness Tier = "harness" // relay adds nothing; outside the order
    TierRead    Tier = "read"
    TierEdit    Tier = "edit"
    TierYolo    Tier = "yolo"
)

// ParseTier accepts exactly the four names; "" is an error (callers decide
// their own default before calling).
func ParseTier(s string) (Tier, error)   // error text: `unknown tier %q (known: harness, read, edit, yolo)`

// Rank orders read < edit < yolo as 1 < 2 < 3; TierHarness is 0 and must
// never be compared -- Above returns false when either side is harness.
func (t Tier) Rank() int
func (t Tier) Above(u Tier) bool          // t.Rank() > u.Rank(), false if either is harness

var ErrTierUnsupported = errors.New("tier not supported by harness")
var ErrExtraArgsPermission = errors.New("extra_args carries a permission flag")
```

Rendering table -- **verified 2026-09-19** on claude 2.1.278 (`--permission-mode`
choices: acceptEdits, auto, bypassPermissions, manual, dontAsk, plan;
`--dangerously-skip-permissions`), agy 1.2.7 (`--mode (accept-edits, plan)`;
`--dangerously-skip-permissions`; `--sandbox`), opencode 2.0.8 (`run --auto`
only):

| kind | harness | read | edit | yolo |
|---|---|---|---|---|
| claude | (none) | `--permission-mode plan` | `--permission-mode acceptEdits` | `--dangerously-skip-permissions` |
| agy | (none) | `--mode plan` | `--mode accept-edits` | `--dangerously-skip-permissions` |
| opencode | (none) | refuse | refuse | `--auto` |

```go
// PermissionArgs is the table above. TierHarness -> (nil, nil) for every
// kind. A refusal cell -> (nil, ErrTierUnsupported wrapped with kind and
// tier: `opencode cannot honour tier read: it has no read-only flag; use
// --tier harness (opencode.jsonc decides) or --tier yolo (--auto)`).
// An unknown kind -> (nil, nil): Launch already returns an empty form for it.
func (h Harness) PermissionArgs(tier Tier) ([]string, error)

// PermissionFlags are the flags relay recognises as permission flags for
// this kind, matched against extra_args as whole elements or as `flag=`
// prefixes:
//   claude:   --permission-mode, --dangerously-skip-permissions,
//             --allow-dangerously-skip-permissions, --allowedTools, --allowed-tools,
//             --disallowedTools, --disallowed-tools, --permission-prompts
//   agy:      --mode, --dangerously-skip-permissions, --sandbox
//   opencode: --auto
func (h Harness) PermissionFlags() []string

// ExtraArgsPermissionFlag returns the first element of extra that is a
// permission flag for this kind, or "".
func (h Harness) ExtraArgsPermissionFlag(extra []string) string
```

Denial patterns, on `Harness` in `harness.go` (defaults; every one must
compile; a `TestDenialPatternsSetOnEveryKind` mirrors the limit/dialog
tests). They are **unverified against a real denied round** and each entry
says so in a comment, in the style the `LimitPatterns` comments use:

```go
// DenialPatterns are default regexes for the text this harness prints when
// a tool call was refused by its permission mode in print mode (#141).
DenialPatterns []string
// claude:   `(?i)requested permissions to use .* but you haven't granted`,
//           `(?i)permission (to use .* was )?denied`,
//           `(?i)tool use was rejected`
// agy:      `(?i)permission (request )?(denied|rejected)`,
//           `(?i)tool (call|use) (was )?rejected`,
//           `(?i)not permitted in (plan|accept-edits) mode`
// opencode: `(?i)permission.*(denied|rejected)`,
//           `(?i)rejected: external_directory`
```

### 3.2 `internal/candidate`

```go
// on Candidate:
// Tier is the candidate's default permission tier (#141); "" means "use the
// role default from policy, else harness". Validated by ParseTier.
Tier string `json:"tier,omitempty"`
// DenialPatterns replace the harness's default denial regexes for this
// candidate (#141), like LimitPatterns and DialogPatterns.
DenialPatterns []string `json:"denial_patterns,omitempty"`
```
`Load`: `tier` present and not parseable -> `candidates %s: candidate %d: tier: %v`;
each `denial_patterns[j]` must compile, same message shape as `limit_patterns`.

### 3.3 `internal/policy`

```go
// Tier maps a role name to its default tier (#141). Absent role -> no
// default here. Every key must be a known role; every value must parse;
// no value may be Above MaxTierOrDefault().
Tier map[string]string `json:"tier,omitempty"`
// MaxTier is the highest tier a command may request without --allow-yolo
// (#141). "" is DefaultMaxTier. Must parse and must not be "harness".
MaxTier string `json:"max_tier,omitempty"`

const DefaultMaxTier = harness.TierEdit

func (p Policy) MaxTierOrDefault() harness.Tier          // DefaultMaxTier when MaxTier == ""
func (p Policy) TierFor(role string) (harness.Tier, bool) // ok false when absent
```
Validation errors, all wrapping `ErrBadPolicy` in the file's existing style:
`max_tier: %v` (parse), `max_tier: "harness" is not a cap`, `tier.%s: unknown role (known: %v)`,
`tier.%s: %v` (parse), `tier.%s: %s exceeds max_tier %s; raise max_tier in the same file`.

### 3.4 `internal/store`

```go
// on Binding, after BuilderCandidate:
// Tier is the effective permission tier the binding's builder launches at
// (#141), resolved once at bind/add/fork. "" on bindings written before the
// field existed and means harness.
Tier string `json:"tier,omitempty"`
// RoundTier overrides Tier for the CURRENT round of a headless binding,
// written by Send --tier and cleared by queueReport with RoundSwitches.
RoundTier string `json:"round_tier,omitempty"`

// on LogEntry, after Late:
// Tier is the permission tier the round was sent at, on plan entries (#141).
Tier string `json:"tier,omitempty"`
```

### 3.5 `internal/relay/tier.go`

```go
var ErrTierAboveMax   = errors.New("tier exceeds max_tier")
var ErrTierPaneFixed  = errors.New("pane builder's tier is fixed at spawn")

// resolveTier is the chain: explicit, candidate.Tier, policy.TierFor(role),
// TierHarness. explicit and candidate values are already validated by their
// parsers; an unparseable stored value is treated as harness.
func resolveTier(explicit string, c candidate.Candidate, pol policy.Policy, role string) harness.Tier

// checkTierCap refuses tier when tier.Above(pol.MaxTierOrDefault()) and
// !allowYolo: `tier %s exceeds max_tier %s; pass --allow-yolo or raise
// max_tier in ~/.config/relay/policy.json`. harness never refuses.
func checkTierCap(tier harness.Tier, pol policy.Policy, allowYolo bool) error

// effectiveTier is RoundTier if set, else Tier, else harness.
func effectiveTier(b store.Binding) harness.Tier
```

### 3.6 `internal/relay/send.go`

```go
// SendOptions is what a send may add to the plan file (#141).
type SendOptions struct {
    Tier      string // "" means the binding's Tier; else a one-round override (headless only)
    AllowYolo bool
}
```

## 4. Interface definitions & component contracts

### 4.1 `harness.Launch` -- widened

```go
// Launch renders argv for role at tier. tier's PermissionArgs are appended
// after the base form and before extra, in both Args and Print. Errors:
// ErrTierUnsupported (refusal cell); ErrExtraArgsPermission when tier !=
// TierHarness and extra carries a permission flag for this kind
// (`candidate extra_args carries %s; remove it or use --tier harness`).
// At TierHarness the result is byte-identical to the pre-#141 Launch.
func (h Harness) Launch(provider, model string, extra []string, role RoleSpec, tier Tier) (Launch, error)
```
Every existing caller and test passes `harness.TierHarness` unless §5 says
otherwise. `PrintArgs` is unchanged.

### 4.2 `relay.Send` -- widened

```go
func Send(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (SendResult, error)
```
Callers: `cmd/relay/main.go` (from flags) and `internal/serve/rounds.go`
(`relay.SendOptions{}`). Preconditions added: if `opts.Tier != ""` it parses;
`checkTierCap` passes; the binding is headless (else `ErrTierPaneFixed`:
`binding %q has a pane builder; its permissions were fixed when the pane was
spawned -- re-bind with relay bind --resume --rebind --tier %s, or use a
headless binding`). Postcondition: `b.RoundTier = opts.Tier` when given; the
plan entry's `Tier` is `string(effectiveTier(b))` after that assignment.

### 4.3 Options structs

`BindOptions`, `AddOptions`, `ForkOptions` each gain
```go
Tier      string // "" -> resolve by chain
AllowYolo bool
```
`create` (bind), `Add`, `Fork`: after the candidate is resolved and before
the builder is spawned, `tier := resolveTier(opts.Tier, c, rt.Policy, "builder")`;
`checkTierCap(tier, rt.Policy, opts.AllowYolo)`; the spawn uses it; the
binding is written with `Tier: string(tier)`. Fork with `opts.Tier == ""`
uses the **source binding's** `Tier` as the explicit value when it is
non-empty (a fork continues a timeline), then the chain. `resume`
(`bind --resume`) keeps the stored `Tier`; with `--rebind --tier X` it
re-resolves and stores X.

`resolveBuilder(ctx, rt, tx, opts BindOptions, name, plannerPane)` reads
`opts.Tier` as the *effective* tier string (callers pass the resolved
value, never ""), and passes `harness.Tier(opts.Tier)` to `Launch`. A
`Launch` error is returned as-is (it is a refusal, not a spawn failure: no
ledger record).

### 4.4 `headlessLaunch` -- widened (unexported)

```go
func headlessLaunch(c candidate.Candidate, role harness.RoleSpec, tier harness.Tier, budget time.Duration, prompt, dir string) ([]string, error)
```
`startRound` passes `effectiveTier(b)`.

### 4.5 `matchDenial`

```go
// matchDenial scans text line by line from the last line backwards, like
// matchLimit, and returns the first (i.e. latest) line matching any
// pattern. Pure.
func matchDenial(text string, patterns []*regexp.Regexp) (line string, ok bool)

// denialPatterns compiles the candidate's DenialPatterns when set, else the
// harness's defaults; patterns that fail to compile are skipped (Load
// validated them; this is defence).
func denialPatterns(c candidate.Candidate, h harness.Harness) []*regexp.Regexp
```

### 4.6 `FormatCandidates`

Row gains, after the roles column and before the `[extra_args]` bracket:
`   tier: <c.Tier>` when `c.Tier != ""`. After the bracket, when
`h.ExtraArgsPermissionFlag(c.ExtraArgs) != ""`:
`   note: extra_args carries <flag>; launches at tier harness only -- move it to "tier"`.

### 4.7 `LogLine`

After ` late`: `if e.Kind == store.KindPlan && e.Tier != "" { first += " tier=" + e.Tier }`.

## 5. High-level pseudocode

### 5.1 `Launch`

```
base, print, promptAt = per-kind forms (unchanged)
perm, err = h.PermissionArgs(tier); if err: return Launch{}, err
if tier != TierHarness:
  if f = h.ExtraArgsPermissionFlag(extra); f != "": return Launch{}, fmt.Errorf("%w: %s", ErrExtraArgsPermission, f)
args  = base + perm + extra
print = print + perm + extra   (only when print != nil)
return Launch{...}, nil
```

### 5.2 Bind/add/fork tier flow

```
res = resolveCandidate(...)                     // existing
c = res.Candidate
explicit = opts.Tier                            // fork: if "" and source.Tier != "": explicit = source.Tier
tier = resolveTier(explicit, c, rt.Policy, "builder")
if err = checkTierCap(tier, rt.Policy, opts.AllowYolo): return err   // before any pane/worktree is created
opts.Tier = string(tier)                        // resolveBuilder reads it
... existing spawn; binding.Tier = string(tier)
```
Headless bind: no launch happens at bind, but the tier is still resolved,
capped and stored here, so the first `send` needs no flags.

### 5.3 `Send`

```
if opts.Tier != "":
  t, err = harness.ParseTier(opts.Tier); if err: return
  if err = checkTierCap(t, rt.Policy, opts.AllowYolo): return
  if !hint.Builder.Headless(): return ErrTierPaneFixed (wrapped with the message in §4.2)
under the lock, after the existing checks and before startRound:
  if opts.Tier != "": b.RoundTier = opts.Tier
  startRound(ctx, rt, b, text)                  // uses effectiveTier(b)
plan entry: Tier = string(effectiveTier(b))
```
A `Launch` refusal from `startRound` (unsupported tier / extra_args
conflict) is returned to the caller unchanged and nothing is written: the
round does not advance. Check `startRound`'s error path -- if it currently
records `spawn_failed` for every error, distinguish `ErrTierUnsupported` /
`ErrExtraArgsPermission` with `errors.Is` and skip the ledger for those.

### 5.4 Headless exit without a report (`headless.go`)

Insert after `gateOnLimit` returns `handled == false` and before the
`!switchable` halt:
```
h, _ = harness.Lookup(b.Builder.Kind); c = candidate for b.BuilderCandidate (rt.Candidates.Lookup; zero Candidate if missing)
if line, ok = matchDenial(logTail(b.Builder.LogPath, limitScanLines), denialPatterns(c, h)); ok:
  amend the exit entry already appended? NO -- restructure: compute the match BEFORE
  appending exitEntry, and pass a suffix:
    exitEntry(now, round, logPath, codeText, suffix)   // suffix "" or "; permission-blocked: <line>"
  return haltBinding(ctx, rt, b, fmt.Sprintf(
    "%s: builder exited (code %s) without a report after a permission denial (%q); not switched -- re-send with a higher tier (relay send --name %s --file <plan> --tier edit|yolo [--allow-yolo]) or extend the harness's allow list; log: %s",
    b.Name, codeText, line, b.Name, b.Builder.LogPath))
```
Order stays: escape halt, then limit gate, then denial halt, then switch.
The denial match is computed once, before the exit entry is written, and
reused for the halt; `exitEntry`'s Note becomes
`builder exited (code %s) without a report%s` with the suffix.

### 5.5 `queueReport`

Where `RoundSwitches` and `RoundExcluded` are reset for the new round, also
`b.RoundTier = ""`.

### 5.6 `ask`

```
tier = resolveTier("", c, rt.Policy, opts.Role)
if err = checkTierCap(tier, rt.Policy, false): return err   // a yolo consult needs max_tier raised in policy
l, err = h.Launch(c.Provider, c.Model, c.ExtraArgs, role, tier); if err: return err
```

### 5.7 CLI (`cmd/relay/main.go`)

`bind`, `add`, `fork`, `send` each add
```
tier      := fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then policy tier.<role>, then harness)")
allowYolo := fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
```
and pass them into the options struct. `send`: `relay.Send(ctx, rt, target, *file, relay.SendOptions{Tier: *tier, AllowYolo: *allowYolo})`.
Usage text in the header block: one line per verb mentioning `--tier`.

## 6. Error handling strategy

| Error | Where | Effect |
|---|---|---|
| `ErrTierUnsupported` | `Launch` | refusal before any pane, worktree or process; no ledger record; message names kind, tier and the alternatives |
| `ErrExtraArgsPermission` | `Launch` | same; message names the flag |
| `ErrTierAboveMax` | `checkTierCap` | refusal; message names both tiers and both remedies |
| `ErrTierPaneFixed` | `Send` | refusal; message names `bind --resume --rebind --tier` |
| bad `tier`/`max_tier`/`denial_patterns` | `policy.Load` / `candidate.Load` | startup error, like every other config field |
| permission-blocked | headless exit path | halt (NEEDS YOU) with the quoted line; exit entry note; not a switch, not a gate |

Nothing here changes what happens at `TierHarness` with a clean
`extra_args`: every existing test that builds argv must pass unmodified once
`TierHarness` is passed.

## 7. README

New section "Permission tiers" after "Candidates": the table in §3.1 with
the verification date; the ceiling semantics; the resolution chain;
`max_tier` and `--allow-yolo`; `send --tier` headless-only and why;
`permission-blocked` and what to do; migration -- "move
`--dangerously-skip-permissions` from `extra_args` to `"tier": "yolo"` and
set `max_tier` accordingly, or leave it and stay at `harness`". Add `tier`
and `denial_patterns` to the candidates field list, `tier`/`max_tier` to the
policy example JSON and prose.

## 8. Tests

All pure; nothing executes a harness or herdr.

`internal/harness/tier_test.go`
- `TestParseTier`: four names; "" and "YOLO" and "bypass" -> error naming the known set.
- `TestTierOrder`: `read < edit < yolo`; `Above` false when either side is harness.
- `TestPermissionArgsTable`: every (kind, tier) cell of §3.1 -- exact slices for the flag cells, `ErrTierUnsupported` for the two opencode refusals, nil for harness on all three.
- `TestLaunchAppliesTier`: claude builder at edit -> Args and Print both contain `--permission-mode acceptEdits` after `--agent plan-executor` and before extra; agy reviewer at read -> `--mode plan`; opencode at yolo -> `--auto`.
- `TestLaunchHarnessTierUnchanged`: for each kind, `Launch(..., TierHarness)` equals the pre-#141 output (copy the expected slices from the existing tests).
- `TestLaunchRefusesExtraArgsPermissionFlag`: agy extra `--dangerously-skip-permissions` at edit -> `ErrExtraArgsPermission` naming it; same extra at harness -> ok and the flag present once; claude extra `--permission-mode=plan` (equals form) at read -> refused.
- `TestDenialPatternsSetOnEveryKind`, `TestDenialPatternsCompile`.

`internal/candidate/candidate_test.go`: `tier: "yolo"` loads; `tier: "god"` -> error naming candidate index; bad `denial_patterns[0]` -> error.

`internal/policy/policy_test.go`: `max_tier` default edit; `"max_tier": "yolo"` + `tier.builder yolo` loads; `tier.builder yolo` with default cap -> `ErrBadPolicy` "exceeds max_tier"; `max_tier: harness` -> error; unknown role key -> error; `TierFor` ok/absent.

`internal/relay/tier_test.go`: `resolveTier` chain -- explicit wins; candidate over policy; policy over harness; all empty -> harness. `checkTierCap`: yolo vs default -> `ErrTierAboveMax`; with allowYolo -> nil; edit vs default -> nil; harness -> nil always; yolo vs `max_tier: yolo` -> nil. `effectiveTier`: RoundTier over Tier over harness.

`internal/relay/denial_test.go`: for each kind one fixture tail whose last lines include a denial line -> that line returned; a tail with an earlier denial and a later benign line -> the denial line (scan finds the latest matching, not the last line); a benign tail -> ok false; candidate override replaces defaults.

`internal/relay/bind_test.go`: bind with `--tier edit` on a claude candidate -> `Binding.Tier == "edit"` and `fakeHerdr`'s recorded `StartAgent` args contain `--permission-mode acceptEdits`; bind with `--tier yolo` and no `--allow-yolo` -> `ErrTierAboveMax` and **no** tab opened, no binding written (mutation: drop the `checkTierCap` call in `create`, this fails); opencode candidate `--tier read` -> `ErrTierUnsupported`, nothing written; policy `tier.builder = read` and no flag -> `Binding.Tier == "read"`.

`internal/relay/send_test.go`: headless binding, `SendOptions{Tier: "yolo", AllowYolo: true}` -> `RoundTier == "yolo"`, `fakeRunner`'s recorded argv contains `--dangerously-skip-permissions` (agy) and the plan entry has `Tier == "yolo"`; then a queueReport round close -> `RoundTier == ""`; pane binding with `Tier: "edit"` -> `ErrTierPaneFixed`, round not advanced; no `--tier` -> plan entry `Tier == "harness"` and argv identical to before (pin with the existing expected argv).

`internal/relay/headless_test.go`: exit-without-report whose log tail ends in a claude denial line -> binding NEEDS YOU, `Halt` contains `permission-blocked` is NOT required -- assert `Halt` contains "permission denial" and the quoted line, the exit entry `Note` ends with `; permission-blocked: <line>`, `RoundSwitches` unchanged, ledger has no new gate, `fakeRunner` started no new process (mutation: remove the denial branch, this fails on the switch happening); a benign tail -> existing switch behaviour unchanged.

`internal/relay/ask_test.go`: reviewer on claude with policy `tier.reviewer = read` -> `StartAgent` args contain `--permission-mode plan`; reviewer on opencode with `tier.reviewer = read` -> `ErrTierUnsupported`, no reservation written.

`internal/relay/logline_test.go`: plan entry `Tier "edit"` -> line ends ` tier=edit`; report entry with `Tier` set -> not printed.

`internal/relay/candidates_list_test.go`: candidate with `Tier: "yolo"` -> `tier: yolo` in the row; agy with `--dangerously-skip-permissions` in extra -> the `note:` line.

## 9. Ordered implementation steps

Commit after each step; prefixes `feat(harness):` 1, `feat(candidate):` 2,
`feat(policy):` 3, `feat(store):` 4, `feat(relay):` 5-8, `feat(cli):` 9,
`chore:` 10. Commit normally; if signing fails, halt and say so.

1. **`internal/harness`**: `tier.go` + `tier_test.go`; `DenialPatterns` on all three kinds; `Launch` widened per §4.1; update `harness_test.go` callers to pass `TierHarness` and add `TestLaunchHarnessTierUnchanged`. The rest of the tree will not compile until step 5 -- that is expected; verify with `go test ./internal/harness` only. Depends on: nothing.
2. **`internal/candidate`**: fields + validation + tests. `go test ./internal/candidate`. Depends on: 1.
3. **`internal/policy`**: fields, defaults, accessors, validation + tests. `go test ./internal/policy`. Depends on: 1.
4. **`internal/store`**: `Binding.Tier`, `Binding.RoundTier`, `LogEntry.Tier`. `go test ./internal/store`. Depends on: nothing.
5. **`internal/relay` compile step**: `tier.go`, `denial.go` with tests; pass `harness.TierHarness` at the three `Launch` sites and in `headlessLaunch`'s new parameter from `effectiveTier(b)`; widen `Send` with `SendOptions` and update `cmd/relay/main.go` and `internal/serve/rounds.go` to pass zero options. Deliverable: `go build ./...` green, `go test ./...` green with **no behaviour change yet** (every existing test passes unmodified except for the `Launch`/`Send` signature updates). Depends on: 1-4.
6. **Bind/add/fork/switch/ask tier flow** (§5.2, §5.6, `switch.go` passing `effectiveTier(b)`), `Binding.Tier` stored, tests in `bind_test.go` and `ask_test.go` including the `checkTierCap` mutation check -- run it, report both outcomes. Depends on: 5.
7. **Send and round close** (§5.3, §5.5), `LogLine`, tests in `send_test.go`, `logline_test.go`. Depends on: 6.
8. **permission-blocked** (§5.4), `exitEntry` suffix, tests in `headless_test.go` including the mutation check -- run it, report both outcomes. Depends on: 5.
9. **CLI flags** (§5.7) and `FormatCandidates` (§4.6) with `candidates_list_test.go`. `go vet ./cmd/relay`. No test in `cmd/relay` may execute a subcommand (CLAUDE.md). Depends on: 7.
10. **README** (§7). Depends on: 9.
11. **Gate.** Run exactly:
    ```
    test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
    make check
    ```
    Both must exit 0; `git status --short` empty. Report the `make check`
    tail verbatim, both mutation outcomes (steps 6 and 8), and
    `git diff --stat main..HEAD`.

## 10. Report

End `NNN-report.md` with the ```relay block: `status`, `halted_at`,
`changed_paths` (every file), `commands_run` (the step-11 gate lines),
`not_done`. In prose: confirm that every pre-existing argv expectation
passed unmodified once `TierHarness` was passed, and give both mutation
results.
