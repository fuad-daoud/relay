# Sub-agent coverage: relay status says what "no foreign rows" proves

**Issue:** #84
**Amends:** `docs/specs/2026-09-10-foreign-agent-detection-design.md` §7.2
(the caveat becomes a pointer to the harness table, which is now the record).

## 1. Problem

`ForeignAgents` (`internal/relay/foreign.go`) reports agents that
`herdr agent list` returns inside a bound tree and that no binding accounts
for. Whether a builder's sub-agents appear in that list at all depends on the
builder's harness:

| kind     | what herdr lists for a running sub-agent | observed |
|----------|-------------------------------------------|----------|
| claude   | a separate agent in its own pane; relay shows a `foreign` row | 2026-09-10, herdr 0.9.0, claude integration |
| agy      | nothing extra; the builder pane's session id flips to the sub-agent's (#66) | 2026-09-11, herdr 0.9.0, antigravity-cli integration |
| opencode | nothing extra; nothing changes on the builder pane | 2026-09-10, herdr 0.9.0, opencode integration v11 |

The opencode case is not a fluke. herdr's opencode integration
(`~/.config/opencode/plugins/herdr-agent-state.js`, v11) tracks child sessions
by `info.parentID` into a `childSessions` map and folds them into the pane's
root session on purpose: "Track child sessions so their events cannot replace
the pane's root session." A child's `permission.asked` / `question.asked`
bubbles up as the root's `blocked`; every other child event is dropped. herdr's
model is one agent per pane, and an in-process sub-agent has no pane to be
listed under. The agy flip is the same model with the guard missing: the
integration reports whichever session is in the foreground.

So on two of relay's three builder harnesses, `relay status` printing no
`foreign` rows means "herdr reported nothing", not "the tree is clear", and
nothing in the output distinguishes the two. Silence is the worse case: a row
you did not want is noise you can dismiss; an occupant relay cannot report is
invisible by construction.

## 2. Scope

relay states, per binding, what its foreign-agent coverage means. It does not
try to see what herdr cannot.

In scope:

- A per-kind sub-agent visibility on the harness table, with the observation
  each value rests on recorded beside it.
- One `coverage` row in `relay status`, printed only for bindings whose builder
  kind cannot show sub-agents as separate agents.
- The same fact in the JSON status output.

Out of scope, unchanged from the #40 spec:

- Suppressing or classifying foreign rows by title.
- Hitting the filesystem, resolving symlinks, or reading a harness's own state
  files to enumerate in-process sub-agents.
- A builder-side breadcrumb (a marker per dispatched researcher). It would
  work on every harness but trusts the builder, which CLAUDE.md says not to
  do for verification.
- Live detection that an agy sub-agent is in the foreground *now* (the builder
  matches by name while its session differs from the recorded one). relay
  holds the data and does not render it; a follow-up if the static row proves
  insufficient.
- An upstream ask to herdr. Not filed; nothing here depends on it.

## 3. Harness table

`internal/harness/harness.go` gains:

```
type SubAgentVisibility string

const (
    // SubAgentsSeparate: a sub-agent is a separate herdr agent in its own
    // pane. ForeignAgents reports it as a foreign row.
    SubAgentsSeparate SubAgentVisibility = "separate"
    // SubAgentsForeground: a sub-agent takes over the builder pane's
    // session slot. herdr lists no extra agent; relay's name match keeps
    // the builder known (#66) but has nothing to report for the sub-agent.
    SubAgentsForeground SubAgentVisibility = "foreground"
    // SubAgentsHidden: a sub-agent runs inside the builder's process and
    // herdr lists only the pane. Nothing observable.
    SubAgentsHidden SubAgentVisibility = "hidden"
)
```

and `Harness` gains `SubAgents SubAgentVisibility`. Values:

| kind     | value        |
|----------|--------------|
| agy      | `foreground` |
| claude   | `separate`   |
| opencode | `hidden`     |

Each entry's field carries a comment with the observation from §1: date, herdr
version, integration name and version where known, and what was seen. For
opencode the comment also names the integration behaviour that makes it
stable. **This table is the one place in the repo the acceptance criterion
asks for.** Specs point at it; they do not restate it.

The zero value `""` is not a state. It means "kind not in the table"; the
status layer treats it as unverified (§4). No entry in `knownHarnesses` may
leave the field unset; a test enforces that every known kind has one of the
three values.

## 4. Status output

`internal/relay/status.go`:

- `BindingStatus` gains `SubAgents string `json:"sub_agents,omitempty"``,
  set to `string(h.SubAgents)` when `harness.Lookup(b.Builder.Kind)` succeeds
  and left empty otherwise. It is the harness fact, not the rendered text, so
  the TUI or a script can branch on it.

New file `internal/relay/coverage.go`:

```
// SubAgentCoverage returns the coverage row for a builder kind, and false
// when no row is warranted. It is pure and takes the harness table's value
// so it can be tested without herdr.
func SubAgentCoverage(kind string, vis harness.SubAgentVisibility) (string, bool)
```

Rows, by `vis`:

| vis          | printed | text |
|--------------|---------|------|
| `separate`   | no      | — |
| `foreground` | yes     | `sub-agents hidden: <kind> runs them in the builder pane; no foreign rows above does not mean the tree is clear` |
| `hidden`     | yes     | `sub-agents hidden: <kind> runs them in-process, herdr lists only the pane; no foreign rows above does not mean the tree is clear` |
| `""`         | yes     | `sub-agents unverified for kind "<kind>"; no foreign rows above does not mean the tree is clear` |

`renderStatus` prints the row after the `foreign` rows and before `detail`,
with the same `  %-8s` label column as the other rows:

```
  builder  wM:p1Z         opencode idle      `opencode/anthropic/sonnet`
  coverage sub-agents hidden: opencode runs them in-process, herdr lists only the pane; no foreign rows above does not mean the tree is clear
```

A claude binding's output is byte-for-byte unchanged.

The row is a fact about the harness, not about this round: it prints whether
or not the builder is currently doing anything. Making it conditional on
activity would require relay to know activity it cannot see, which is the
problem being labelled.

## 5. Docs

- Amend `docs/specs/2026-09-10-foreign-agent-detection-design.md` §7.2: replace
  the two single-observation paragraphs with the finding in §1 above and a
  pointer to `harness.Harness.SubAgents` as the record. Keep the paragraph on
  why titles are never used to suppress rows.
- `docs/design.md`, the foreign-rows passage (around line 328): add one
  sentence on the `coverage` row and what its absence means (the builder's
  harness shows sub-agents as separate agents). README does not describe
  foreign rows and is untouched.

## 6. Tests

All pure, all in `internal/harness` or `internal/relay`; nothing reaches herdr
(CI has no `herdr` binary).

- `internal/harness`: every kind in `knownHarnesses` has a non-empty
  `SubAgents` that is one of the three constants.
- `internal/relay/coverage_test.go`: table-driven over the four `vis` values;
  asserts printed/not-printed and that the text names the kind.
- `internal/relay/status_test.go`: render a binding per builder kind; the
  claude case asserts no `coverage` line; agy and opencode assert the row is
  present, sits between the last `foreign` row (when one exists) and `detail`,
  and the JSON `sub_agents` field carries the harness value. An unknown kind
  asserts the `unverified` text and an absent JSON field.

Mutation check for the plan: remove the `SubAgents` value from the opencode
entry and confirm the harness table test and the opencode status test both
fail.

## 7. Files

```
internal/harness/harness.go          SubAgentVisibility, field, per-kind values with observation comments
internal/harness/harness_test.go     every kind has a valid value
internal/relay/coverage.go           NEW  SubAgentCoverage
internal/relay/coverage_test.go      NEW
internal/relay/status.go             BindingStatus.SubAgents; coverage row in renderStatus
internal/relay/status_test.go        per-kind render + JSON
docs/specs/2026-09-10-foreign-agent-detection-design.md   §7.2 amended
docs/design.md                       coverage row described beside foreign rows
```
