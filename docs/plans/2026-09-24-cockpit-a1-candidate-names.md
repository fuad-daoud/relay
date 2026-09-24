# Cockpit A1: every candidate gets a short name

Spec: `docs/specs/2026-09-24-cockpit-design.md` §3.1 (Candidate, name derivation,
resolution, display) and §3.8 (the A1 migration). Line numbers are from `ff04f5d`.

**This plan runs in two rounds. Each send states which round it is. Do only the steps
of that round, then stop and report.**

- **Round 1:** the model plus every input. Names are accepted everywhere a token is,
  and output is unchanged apart from error texts.
- **Round 2:** every human-facing output shows the name, and JSON gains a `name`
  beside the token.

**If a step is impossible as written or contradicts what you find, stop and report. Do
not improvise.**

CI has no harness binary and no network. Every new rule is tested as a pure function in
`internal/candidate`, `internal/relevo` or `internal/roles`. No `cmd/relevo` test may
run `bind`, `send`, `ask`, `config --probe` or any verb that spawns a harness or reaches
a server.

**Do not touch** `internal/ui/**`: a parallel plan (B1) is rewriting it. After both
land, one line in the ui switches to the name.

## 1. System overview

Today a candidate's only identity is its token `harness/provider/model`
(`opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high`). After A1:

- Every candidate also has a **name**, such as `deepseek-v4.1-flash`. It is unique,
  at most 24 characters, and never contains `/`.
- The name is **accepted wherever a token is accepted**: flags, `gate`, `gate
  --clear`, `config --probe`, the MCP send tool, `history --candidate` and
  `candidate:` queries, roles `candidates` lists and policy `order`.
- The name is **printed wherever a human reads a candidate**.
- **Internally nothing changes identity.** The canonical token is still what is
  stored in bindings, rounds, the ledger, latency samples and log notes, and what is
  sent over the wire to servers. A name is always resolved to its token at the edge.

## 2. File structure

```
internal/candidate/candidate.go   Candidate.Name; Set.byName; Resolve, NameOf, Names, IsName
internal/candidate/names.go       NEW  DeriveNames (pure), namePattern, IsName
internal/candidate/names_test.go  NEW
internal/config/names.go          NEW  (*Store).EnsureCandidateNames — the A1 migration
internal/config/names_test.go     NEW
internal/roles/file.go            candidates entries: name or token
internal/roles/registry.go        resolve entries through the set; Serves compares resolved tokens; NameOf
internal/policy/policy.go         order entries: name or token
internal/relevo/candidate.go      resolveRole resolves through Set.Resolve; PickText (round 2)
internal/relevo/available.go      ResolveClearSubject: name → candidate
internal/relevo/ledger.go         Unavailable resolves names; forwards canonical tokens
internal/relevo/probe.go          Probe resolves names
internal/relevo/remote.go         addRemote matches names; ForwardUnavailable/ForwardAvailable send canonical tokens
internal/relevo/send.go           sendPreflight (remote) resolves names locally
internal/relevo/served.go         PickServedCandidateFor fallback resolves names
internal/relevo/history.go        HistoryOptions.Names resolves a candidate filter
internal/remote/proto.go          CandidateView.Name
internal/serve/candidates.go      fills CandidateView.Name
cmd/relevo/main.go                newRuntime calls EnsureCandidateNames; flag help texts
cmd/relevo/serve.go               loadConfig calls EnsureCandidateNames
cmd/relevo/history.go             passes rt.Candidates as HistoryOptions.Names
round 2 display files: §5.3
```

## 3. Data structures

### `candidate.Candidate` (`internal/candidate/candidate.go:51-72`)

It gains `Name string \`json:"name,omitempty"\``, the first field. It is optional in
stored JSON: a missing name is derived at parse time.

### `candidate.Set` (`candidate.go:95-103`)

It gains `byName map[string]string` (name → canonical token). It is filled by `Parse`,
and every candidate in the set has exactly one entry.

### `internal/candidate/names.go` (new)

```
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,23}$`)

// IsName reports whether s is shaped like a candidate name (never contains "/").
func IsName(s string) bool

// DeriveNames returns one name per entry, in entry order.
// Entries with a non-empty Name keep it verbatim (validated by Parse, not here).
// Reserved first: every explicit name, and every provider name among entries.
// Then, for each entry without a name, in order:
//   base = the model's last "/"-segment, with an effort suffix ("#..." or ":...")
//          removed, lowercased, each run of chars outside [a-z0-9.-] turned into
//          "-", leading "-" and "." trimmed, truncated to 24; "" becomes "c".
//   try, taking the first that is not reserved and matches namePattern:
//     base
//     base + "-" + effort (effort = the removed suffix without "#"/":"; skipped if none)
//     harness + "-" + base
//     harness + "-" + base + "-" + effort (skipped if no effort)
//     base + "-2", base + "-3", ... (truncating base so the whole fits 24)
//   each candidate is truncated to 24, then checked; the chosen name is reserved.
func DeriveNames(entries []Candidate) []string
```

The expected output for this machine's config (a test fixture) is in §6.1.

### `(*config.Store).EnsureCandidateNames() (bool, error)` (new, `internal/config/names.go`)

1. Read the stored `candidates` body. If it is absent, return `false, nil`.
2. Decode it as `[]map[string]json.RawMessage`, which keeps every key and the order.
3. If every element already has a non-empty `"name"`, return `false, nil`.
4. Otherwise decode the same bytes as `[]candidate.Candidate`, run `DeriveNames`, set
   `"name"` on each element that lacks one, re-encode (two-space indent, the way
   `config export` prints), and `Put(Candidates, body)`.
5. Return `true, nil`.

It is idempotent: a second run returns `false`. A concurrent CLI and daemon compute
the same names, and `Put` is one transaction.

## 4. Contracts

### 4.1 `internal/candidate` (round 1)

| function | contract |
|---|---|
| `Parse(name, data)` (`candidate.go:198-267`) | Before the loop, compute `names := DeriveNames(entries)`. In the loop, for each entry: if `c.Name != ""` and `!IsName(c.Name)`, error `candidates <name>: candidate <i>: name "<n>": want ^[a-z0-9][a-z0-9.-]{0,23}$`. Set `c.Name = names[i]`. After the duplicate-token check: a duplicate name is an error `candidates <name>: candidate <i>: duplicate name "<n>" at index <first> and <i>`, and a name equal to any entry's provider is an error `candidates <name>: candidate <i>: name "<n>" is also a provider name`. Fill `byName[c.Name] = key`. Warnings and skips are unchanged. A skipped entry (unknown harness or role) still takes part in `DeriveNames`, so the names of the others do not shift when it is fixed. |
| `(*Set) Resolve(s string) (Candidate, error)` | If `s` contains `/`: `ParseRef`, then `Lookup` (their errors unchanged). Else if `byName[s]` exists, return that candidate. Else error `unknown candidate "<s>" (known: <Names() joined by ", ">): unknown candidate`, wrapping `ErrUnknownCandidate`. A nil set: `unknown candidate "<s>" (no candidates configured)`, also wrapping it. |
| `(*Set) NameOf(token string) string` | The name of the candidate whose canonical token is `token`, else `token` unchanged. A nil set returns `token`. Never errors. |
| `(*Set) Names() []string` | Every name, sorted. |
| `Lookup` | Unchanged. Its error text now lists names: `candidate "<ref>" not found (configured: <Names()>)`. |

### 4.2 Inputs (round 1)

Each of these sites switches from `ParseRef` + `Lookup` (or a raw string compare) to
`Set.Resolve`. After resolution every site uses the canonical token (`c.Ref().String()`
or `res.Token()`), exactly as today.

| site | change |
|---|---|
| `resolveRole` (`internal/relevo/candidate.go:216-293`) | Replace its `ParseRef`+`Lookup` of the explicit token with `rt.Candidates.Resolve(tok)`. From there on `tok = c.Ref().String()`. Its error texts name the candidate by `c.Name` (the "does not serve role" texts at 229 and 231, and `<tok>: <gate note>` at 240). This one change covers bind, add, fork, ask, `send --builder` (via `ResolveSendBuilderFor`), served create and the MCP send tool. |
| `PickServedCandidateFor` (`internal/relevo/served.go:221-237`) | Its fallback `ParseRef`+`Lookup` becomes `Resolve`. The miss behaviour is unchanged: `(token, "")`, no error. |
| `sendPreflight`, remote branch (`internal/relevo/send.go:152-155`) | If the value has no `/`: `rt.Candidates.Resolve(v)`, and send the canonical token. If it cannot be resolved, error `send --builder <v>: unknown candidate "<v>"; a candidate only the server has must be named by its harness/provider/model token`. A value with `/` keeps today's shape check. |
| `addRemote` (`internal/relevo/remote.go:183-202`) | Match the requested value against each server `CandidateView`'s `Token` **or** `Name`, and send the matched view's `Token`. If there is no match and the value has no `/`, try `rt.Candidates.Resolve(v)` and match its canonical token. The error text lists `name (token)` pairs when a view has a name. |
| `Unavailable` (`internal/relevo/ledger.go:158-181`) | `Resolve` instead of `ParseRef`+`Lookup`. Everything downstream uses the canonical token. |
| `ForwardUnavailable` (`remote.go:1278-1311`) | Is given, and forwards, the canonical token, never the raw argument. Change its caller in `gateUnavailable` (`cmd/relevo/main.go:1004-1044`) so the local resolution happens first. |
| `ResolveClearSubject` (`internal/relevo/available.go:40-67`) | New first branch: if `IsName(s)` and `set` has that name, treat it exactly as the canonical token of that candidate. Then today's branches. |
| `ForwardAvailable` (`remote.go:1320-1362`) | Forwards the canonical token when the subject resolved to a candidate, and the provider name otherwise. |
| `Probe` (`internal/relevo/probe.go:225-235`) | `Resolve` per argument. `cmdCandidates` (`main.go:874-887`) sizes its column from the resolved tokens, not argv. |
| roles `validate` (`internal/roles/file.go:191-200`) | An entry is valid if `candidate.IsName(s)` or `ParseRef(s)` succeeds. Otherwise error `<path>: <role>.candidates[i]: "<s>": want a candidate name or harness/provider/model: bad roles`. The duplicate check on raw strings stays. |
| policy order (`internal/policy/policy.go:726-735`) | The same rule and error shape (`order.<role>[i]: ...: bad policy`). |
| roles `buildLegacy` (`internal/roles/registry.go:105-152`) and `buildFile` (158-241) | Resolve each entry with `set.Resolve`. An entry that does not resolve is skipped silently, as today. Store the canonical `ref.String()` in `Ranked` in both modes (`buildLegacy` stored the raw `tok` at 134). Two entries that resolve to the same token keep the first. Each role keeps its resolved canonical list for `Serves`. |
| `Registry.Serves` (`registry.go:298-315`) | In file mode, compare `ref.String()` with the role's **resolved** canonical list, not the raw row. |
| `roles.candidateTier` (`internal/roles/init.go:103-116`) | `Resolve` instead of `ParseRef`+`Lookup`. |
| `HistoryOptions` (`internal/relevo/history.go:110-232`) | New field `Names *candidate.Set`. At the end of `Filter`, if `q.Filter.Candidate` has no `/` and `Names` resolves it, replace it with the canonical token. An unresolved value is left as typed (it matches no rounds, as today). `cmd/relevo/history.go` sets `Names: rt.Candidates` beside `Candidate: *candidateTok` (263). |
| flag help texts | `--builder` on bind (`main.go:1177`) and send (1676), `--candidate` on ask (1751) and history (`cmd/relevo/history.go:154`): `candidate name or harness/provider/model token`. |

### 4.3 Migration and wire (round 1)

- `newRuntime` (`cmd/relevo/main.go:545-581`): immediately before `L, err :=
  cs.Load()` (571), call `cs.EnsureCandidateNames()`. On error, `slog.Warn` and carry
  on: names are still derived in memory by `Parse`. Skip the call when `d.Newer()`
  (the schema is newer; this binary must not write), in the same `if` as the import
  above it (561-569). `newRuntimePeek` (598-632) must **not** call it.
- `loadConfig` (`cmd/relevo/serve.go:303-320`): the same call before its load, with
  the same newer-schema guard if it has one there.
- `remote.CandidateView` (`internal/remote/proto.go:180-185`): add `Name string
  \`json:"name,omitempty"\``. `handleCandidates` (`internal/serve/candidates.go:11-54`)
  fills it from the candidate. An old client ignores it, and an old server sends none.

### 4.4 Display (round 2)

Rule: **print `NameOf(token)`**. It returns the name, or the token when the candidate
is no longer configured. **Logic keeps using the canonical token**: gate lookups,
`<- would pick` compares, latency maps and group keys.

| surface | location | change |
|---|---|---|
| pick block | `formatPolicyRole` (`internal/relevo/policy_view.go:201-249`) | Print `set.NameOf(tok)` at 242. `refWidth` (166-174) measures names. `tok` stays for 211-213, 228 and 235. |
| policy warnings | `PolicyWarnings` 37-95, `PolicyWarningsFor` 368-412 | Tokens that resolve are shown as the name. Unresolved ones stay raw, since they are the point of the warning. |
| candidates block | `formatCandidatesLatency` (`internal/relevo/candidates_list.go:61-105`) | Columns: `name`, then the token in faint text, then roles and ttft as today. Width comes from names and tokens separately. |
| roles block | `formatRole`, `legacyCandidates`, `fileCandidates` (`internal/relevo/roles_list.go:35-101`) | Print names. Add `(*roles.Registry) NameOf(token string) string`, delegating to its private set (`registry.go:77`). `fileCandidates` prints `NameOf` of each resolved entry, and a raw entry that did not resolve stays raw with ` (unknown)`. |
| status | `statusRow` (`internal/relevo/status.go:326-340`); `RenderStatus` builder line 719-731; `writeGatedBlock` 616-641 | `BindingStatus` gains `BuilderName string \`json:"builder_name,omitempty"\`` beside `BuilderCandidate`, filled with `rt.Candidates.NameOf`. `RenderStatus` prints `BuilderName` when it is set, else the token. The gates block prints `g.Name` when set. |
| gates | `ledger.Gate` (`internal/ledger/ledger.go:228-241`); `Gates(rt)` (`internal/relevo/ledger.go:259`) | `Gate` gains `Name string \`json:",omitempty"\``. `Gates` fills it with `rt.Candidates.NameOf(g.Token)`. `serve.RenderGates` (`internal/serve/admin.go:534-544`) prints `Name` when set. |
| gated note | `gatedNote` (`internal/relevo/ledger.go:433-456`) | `note: <name> is gated: …`. |
| history rows | `HistoryLine`, `FormatHistory` (`internal/relevo/history.go:255-293`) | `FormatHistory(rows, names func(string) string)`. The candidate column prints `names(token)`, padded to 24 instead of 40. |
| history groups | `FormatGroups` (`history.go:301-323`) | With `by == builder`, print `names(g.Key)`. Keys and sums stay tokens. |
| history `--json` | `cmd/relevo/history.go:318-323` | Encode each row as `struct{ db.RoundRow; BuilderName string \`json:"BuilderName,omitempty"\` }`. Group JSON is unchanged. |
| probe | `FormatProbe` (`internal/relevo/probe.go:284-294`) | Print the name. `ProbeResult` keeps `Token` and gains `Name`, set in `ProbeCandidate` (76). |
| bind/add/fork lines | `cmd/relevo/main.go` 1337-1345, 1352-1353, 1521, 1528, 1530-1531 | Print `rt.Candidates.NameOf(b.BuilderCandidate)`. |
| pick note printed to a human | `notePick` (`main.go:807-812`); `send.go:410` (`pickLine`) | Add `PickText(role string, res Resolution, set *candidate.Set) string`: the same text as `ExplainResolution`, with every token passed through `set.NameOf`. Implement it by giving the builder behind `ExplainResolution` (`candidate.go:309-347`, and `skipText` 172-174) a `name func(string) string` parameter. `ExplainResolution` passes identity, **so the stored `KindPick` note is byte-identical**, and `PickText` passes `set.NameOf`. `notePick` and `send.go:410` use `PickText`. `pickEntry` (353-358) and `switchEntry` keep `ExplainResolution`. |
| dry run | `DryRun` (`internal/relevo/send.go:504-518`), `dryRunBuilderLine` (`text.go:115-121`) | `DryRun` gains `CandidateName string \`json:"candidate_name,omitempty"\``. The text prints the name. |
| unknown-candidate errors | round 1 already | — |

**Not changed by A1** (listed so none of it is touched by accident):

- Stored `LogEntry.Note` texts (pick, switch and relaunch notes) and their parsers:
  `internal/ingest/outcome.go:78-196`, `internal/relevo/stats.go:552-600`.
- `b.Halt` texts.
- Ledger subjects, latency sample keys, db columns, remote wire tokens.
- `history --stats` (C2 rewrites it) and `history --tab`.
- `doctor` output.
- The statusline.
- The `done --pick` picker (`internal/pick/list.go`).
- All of `internal/ui/**`.

## 5. Pseudocode: resolving a user string

```
Resolve(s):
  if s contains "/":
     ref = ParseRef(s)            // ErrBadRef as today
     return Lookup(ref)           // ErrUnknownCandidate, text lists names
  if tok, ok = byName[s]: return byRef[tok]
  return ErrUnknownCandidate("unknown candidate %q (known: %s)")

gate --clear x:
  if IsName(x) and set.byName has x: subject = that candidate's token   // new
  else: today's branches (token, else bare provider)
```

## 6. Error handling

| case | result |
|---|---|
| An explicit invalid name in config | `config set` / `import` / `edit` refuse, with the `Parse` error text (§4.1). The stored config is unchanged. |
| A duplicate name, or a name equal to a provider | Refused the same way. |
| An unknown name on any input | `ErrUnknownCandidate`, with the known names listed. |
| A remote-only candidate given by name | Send refused with the text in §4.2. The token still works. |
| `EnsureCandidateNames` fails | A warning; the process continues with derived names in memory. |

## 7. Tests

### 7.1 Round 1

**`internal/candidate/names_test.go`**

- `TestDeriveNamesThisMachine`: the seven entries of this machine's config, in this
  order:

  | token | name |
  |---|---|
  | `agy/google/gemini-3.8-flash-high` | `gemini-3.8-flash-high` |
  | `agy/antigravity/claude-sonnet-4-6` | `claude-sonnet-4-6` |
  | `claude/anthropic/sonnet` | `sonnet` |
  | `claude/anthropic/haiku` | `haiku` |
  | `opencode/openrouter/z-ai/glm-5.3-flash` | `glm-5.3-flash` |
  | `opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high` | `deepseek-v4.1-flash` |
  | `codex/openai/gpt-5.6-terra:high` | `gpt-5.6-terra` |

- `TestDeriveNamesCollisions`:
  - `claude/anthropic/sonnet` + `agy/antigravity/sonnet` → `sonnet`, `agy-sonnet`.
  - `codex/openai/gpt-x:high` + `codex/openai/gpt-x:low` → `gpt-x`, `gpt-x-low`.
  - An explicit `"name":"sonnet"` on the second entry reserves `sonnet`, so the first
    becomes `claude-sonnet`.
  - A model named `openai` under provider `openai` → `codex-openai`.
  - A 40-char model is truncated to 24.
  - Three identical bases with no effort under one harness → `x`, `h-x`, `x-2`.
- `TestIsName`: valid and invalid shapes, including `a/b`, `-x`, `A` and 25 chars.

**`internal/candidate/candidate_test.go`**

- `TestParseNames`: derived names are filled; an explicit name is kept; the three
  `Parse` errors (bad shape, duplicate name, provider clash); a skipped unknown-harness
  entry does not shift later names.
- `TestResolve`: by name, by token, a bad token (`ErrBadRef`), unknown (text lists
  names), a nil set.
- `TestNameOf`: known, unknown and nil set.

**`internal/config/names_test.go`**

- `TestEnsureCandidateNamesWritesOnce`: the first run writes names and keeps unknown
  keys and order; the second run returns `false` and writes nothing (the config
  version is unchanged).
- `TestEnsureCandidateNamesAbsentSection`: returns `false, nil`.

**`internal/roles` and `internal/policy`**

- A name entry validates.
- `a` (not a token, but a valid name) validates.
- `A/b` fails with the new text.
- `buildFile` with a name entry ranks the canonical token.
- `Serves` is true for a name entry.

**`internal/relevo`**

- `TestResolveRoleByName`.
- `TestResolveClearSubjectName`: a name clears that candidate, and a provider still
  clears the provider.
- `TestUnavailableByName` records the canonical token.
- `TestProbeUnknownNameRunsNothing`.
- `TestHistoryFilterResolvesName`.
- The remote `addRemote` match by name, using the existing fake server helpers in
  `remote` tests if they exist. If they do not, test a pure `matchCandidateView(views,
  v)` helper that `addRemote` calls.
- `ForwardUnavailable` is called with the canonical token. Use the existing forwarding
  test seam if there is one; if not, report it and skip.

**Existing tests** whose error-text assertions change: `TestResolveCandidate`,
`TestUnavailableRefusesAnUnknownToken`, `TestResolveSendBuilderUnknownToken`,
`TestProbeUnknownTokenRunsNothing`, `TestResolveClearSubject`, and the roles/policy
validation tests. Update the expected text only. The reason each fails is §4.1's new
message.

**Mutation check:** make `Resolve` try `byName` only when `s` contains `/`.
`TestResolveRoleByName` must fail. Report it, then revert.

### 7.2 Round 2

- Update the display assertions listed in the research map:
  - `policy_view_test.go`
  - `roles_views_test.go`
  - `candidates_list_test.go`
  - `roles_list_test.go`
  - `status_test.go` (`TestRenderStatusGatedBlock` 242-245)
  - `headless_test.go` `TestStatusHeadlessWorkingShowsPidAndLogTail`
  - `writer_role_test.go` `TestStatusShowsRole`
  - `ledger_test.go` `TestGatedNote*`
  - `history_test.go` `TestHistoryLineColumns`, `TestFormatGroupsColumns`
  - `probe_test.go` `TestFormatProbe`
  - `send_test.go` `TestRenderDryRunShape`
  - `internal/serve/admin_test.go` `TestAdminGatesAvailableUnavailable`

  Each now expects names. The fixtures in `internal/relevo/candidate_test.go:16-29` get
  no explicit names, so the derived names are used. Write the expected values from
  `DeriveNames`, not by hand.
- New:
  - `TestPickTextUsesNamesStoredNoteUnchanged`: `ExplainResolution` output is
    byte-identical to its pre-A1 golden, and `PickText` shows names.
  - `TestStatusJSONHasBuilderName`.
  - `TestHistoryJSONHasBuilderName`.
  - `TestGatesCarryName`.
- `internal/ingest/outcome_test.go` and `stats_test.go` must pass **unchanged**. If
  either needs an edit, a stored note changed: stop and report.
- **Mutation check:** make `ExplainResolution` pass `set.NameOf` instead of identity.
  An ingest or stats test must fail. Name it, then revert.

## 8. Working efficiently

A builder pays one round trip per step whatever the step does, so:

- Batch independent reads and edits as parallel tool calls in one step.
- Read once, from the locations this plan names. Do not search for what it already
  located.
- Make each file's changes in one edit call.
- Iterate on the focused command:
  `go test ./internal/candidate/... ./internal/config/... ./internal/roles/... ./internal/policy/... ./internal/relevo/... ./internal/serve/... ./internal/remote/...`.
  Fix every reported error before the next run.
- Run the full check once at the end of the round: `make check`.

## 9. Ordered steps

### Round 1: names are accepted everywhere

**1.1 `internal/candidate`.**
- Deliverable: `names.go` (`IsName`, `DeriveNames`); in `candidate.go`: `Name`,
  `byName`, the `Parse` changes, `Resolve`, `NameOf`, `Names`, and `Lookup`'s text.
  Tests in §7.1 for this package.
- Depends on nothing.
- Verify: `go test ./internal/candidate/...`.

**1.2 Validators and registry.**
- Deliverable: `internal/roles/file.go`, `registry.go` (`buildLegacy`, `buildFile`,
  `Serves`), `init.go` `candidateTier`, and `internal/policy/policy.go` order, per
  §4.2. Their tests.
- Depends on 1.1.
- Verify: `go test ./internal/roles/... ./internal/policy/...`.

**1.3 Resolution sites in `internal/relevo`.**
- Deliverable: `resolveRole`, `PickServedCandidateFor`, `sendPreflight` (remote),
  `addRemote` (+ `matchCandidateView`), `Unavailable`, `ForwardUnavailable` /
  `ForwardAvailable` (canonical), `ResolveClearSubject`, `Probe`, and
  `HistoryOptions.Names`, per §4.2. Tests.
- Depends on 1.1 and 1.2.
- Verify: `go test ./internal/relevo/...`.

**1.4 Wire, migration and cmd.**
- Deliverable: `CandidateView.Name` and `handleCandidates`; `internal/config/names.go`
  and its tests; `EnsureCandidateNames` calls in `newRuntime` and serve `loadConfig`;
  `gateUnavailable` resolving before it forwards; `cmdCandidates` column width;
  `history.go` `Names`; the flag help texts.
- Depends on 1.3.
- Verify: `go build ./...`, then `go test ./internal/config/... ./internal/serve/... ./cmd/relevo/...`.

**1.5 Check and report.**
- Run the mutation check (§7.1), then `make check`.
- Report:
  - every changed function with its line range;
  - the updated error-text tests, each with the reason;
  - `git diff --stat` (it must not include `internal/ui`, `internal/ingest` or
    `internal/relevo/stats.go`).
- Depends on 1.4.

### Round 2: names are shown everywhere

The send for this round may refresh line numbers.

**2.1 Carriers.**
- Deliverable:
  - `BindingStatus.BuilderName`, filled in `statusRow`.
  - `ledger.Gate.Name`, filled in `Gates`.
  - `ProbeResult.Name`.
  - `DryRun.CandidateName`.
  - `(*roles.Registry).NameOf`.
  - The `name func(string) string` parameter behind `ExplainResolution`, plus
    `PickText`.
- Verify: `go build ./...`. The ingest and stats tests pass unchanged.

**2.2 Renderers.**
- Deliverable: every row of the §4.4 table.
- Depends on 2.1.

**2.3 cmd call sites.**
- Deliverable: `notePick`, `send.go:410`, the bind/add/fork lines,
  `FormatHistory` / `FormatGroups` callers with `rt.Candidates.NameOf`, and the
  history `--json` row wrapper.
- Depends on 2.2.

**2.4 Tests.**
- Deliverable: §7.2, then the mutation check.
- Depends on 2.3.

**2.5 Check and report.**
- Run `make check`.
- Report the updated assertions grouped by file.
- Report `git diff --stat`. It must not include `internal/ui`, `internal/ingest` or
  `stats.go`.
- Paste the output of `relevo config` against the fixture from
  `TestDeriveNamesThisMachine`, if a test renders it. If none does, paste
  `FormatPolicyFor`'s output from a test.
