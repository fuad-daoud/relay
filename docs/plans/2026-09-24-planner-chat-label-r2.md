# Plan: #386 round 2: the chat label in `relevo status`, doctor's planner row, and the planner name in the statusline

## 1. System overview

Round 1 (commit `09fd625` on this branch) added `internal/chatlabel` and a `chat`
column to `relevo planner list`:

- `chatlabel.Label{Text, Link}` with `String()`, which renders `-`, the text, the
  link, or `text · link`;
- `chatlabel.Resolver.Resolve(ctx, kind, sessionID, locator) Label`;
- `chatResolver()` in `cmd/relevo/planner.go`, which returns the production
  resolver.

Read those files first. They are the whole API this round uses. This round uses
the same label in the two other places relevo names a planner to a human, and
puts the planner's own name in the statusline:

1. The `planner` line of `relevo status`, and two matching `status --json`
   fields.
2. The `planner` row of `relevo doctor`.
3. A first statusline line that names the planner, so each terminal shows which
   planner it is.

**Not in scope:**

- `relevo ui`'s pane (`internal/ui/pane.go:32`);
- the SessionStart hook text (`internal/planner/hook.go:73`), which the model
  reads, not the human;
- any error message. The issue mentions "<binding> belongs to planner …"
  messages, but no such message exists in the code, so nothing changes there.

**Privacy rule (from the issue):** a label is computed only in `cmd/relevo`,
inside the command a person ran, and only printed. `internal/relevo.Status` must
**not** compute it. That function also serves `relevo serve`, and a label must
never be computed on, or sent to, a server. It is never stored or logged.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 2. Files touched

```
internal/relevo/status.go        BindingStatus (~79-92): two fields; RenderStatus planner line (~696-697)
internal/relevo/status_test.go   one new test
internal/relevo/statusline.go    new RenderPlannerLine
internal/relevo/statusline_test.go  one new test
cmd/relevo/main.go               cmdStatus (~1745-1824): one call; cmdStatusline (~1826-1854): one print
cmd/relevo/planner.go            new annotatePlannerChat
cmd/relevo/planner_test.go       one new test
cmd/relevo/doctor.go             plannerCheckInput (~587-647): set in.Chat
internal/doctor/planner.go       PlannerCheckInput.Chat; the planner row details (~100-127)
internal/doctor/planner_test.go  one new test
README.md                        the "## Status line" section (~1211-1220)
```

## 3. Data structures

### `relevo.BindingStatus` (`internal/relevo/status.go`)

Add after `PlannerName` (line ~82):

- `PlannerChatLabel string` with tag `json:"planner_chat_label,omitempty"`: the
  planner's `chatlabel.Label.Text`.
- `PlannerChatLink string` with tag `json:"planner_chat_link,omitempty"`: its
  `Label.Link`.

Add a comment saying that only `cmd/relevo` fills these (see the privacy rule in
§1), and that `Status` leaves them empty.

### `doctor.PlannerCheckInput` (`internal/doctor/planner.go`)

Add after `Resolved`: `Chat string`, the resolved planner's
`chatlabel.Label.String()`. It is `""` when the label is empty or `Resolved` is
nil.

## 4. Contracts

### `RenderStatus` planner line (`internal/relevo/status.go` ~696)

Today:

    fmt.Fprintf(&sb, "  planner  %-14s %-8s route %s\n", plannerNameOrID(b), b.PlannerKind, b.PlannerRoute)

After:

- When `chatlabel.Label{Text: b.PlannerChatLabel, Link: b.PlannerChatLink}` has
  either field set, the line gets `" · " + label.String()` inserted before the
  `\n`.
- Otherwise the line is **byte-identical** to today's.

### `func RenderPlannerLine(name string, columns int) string` (`internal/relevo/statusline.go`)

- `name == ""` returns `""`.
- Otherwise it returns `ansiDim + "planner " + name + ansiReset + "\n"`.
- When `columns > 0` and `"planner " + name` is longer than `columns` runes, cut
  the visible text with the existing `truncate(s, columns)` helper (line ~162)
  before wrapping it in the colour codes.

`RenderStatusLine` itself does **not** change, and `TestRenderStatusLineEmpty`
must still pass unmodified.

### `func annotatePlannerChat(rt relevo.Runtime, rep *relevo.Report, res chatlabel.Resolver)` (`cmd/relevo/planner.go`)

- If `rt.Planners == nil`, return.
- For each `rep.Bindings[i]` with `PlannerID != ""`:
  1. Resolve the label at most once per `PlannerID`, using a local
     `map[string]chatlabel.Label`:
     - get the record with `rt.Planners.Get(id)`; on error, store `Label{}`;
     - otherwise call `res.Resolve(context.Background(), rec.HarnessKind, rec.SessionID, rec.TranscriptLocator)`.
  2. Set `PlannerChatLabel` and `PlannerChatLink` from the label.
- It never returns an error and never prints.

### Doctor planner row (`internal/doctor/planner.go` ~100-127)

Add an unexported helper `plannerRef(rec *planner.Record, chat string) string`:

- `chat == ""` returns `rec.Name + " (" + rec.ID + ")"`;
- otherwise `rec.Name + " (" + rec.ID + ") · " + chat`.

In the three `Resolved != nil` cases, replace the `%s (%s)` name part with
`plannerRef(in.Resolved, in.Chat)`:

- the `!in.MCPChild` case (`"planner %s (%s): no relevo mcp …"`);
- the `!in.ClaimLive` case (`"planner %s (%s): tools mode: …"`);
- the default case (`"%s (%s); channel claim live"`).

Every other character of those details stays as it is, so with `Chat == ""`
each detail is byte-identical to today's.

## 5. Pseudocode: call sites

`cmdStatus` (`cmd/relevo/main.go`). After `rep = scopeReport(rep, target, *all)`
and before the `if *asJSON` branch:

    annotatePlannerChat(rt, &rep, chatResolver())

Both the JSON document and the rendered text then carry the label.

`cmdStatusline` (`cmd/relevo/main.go`). After `plannerFilter` returns `rec, ok`
with ok true:

1. Print `relevo.RenderPlannerLine(rec.Name, columns)` first, **before** calling
   `PlannerStatus`.
2. Then continue as today. If `PlannerStatus` fails, the planner line has
   already been printed. That is intended: the line is the planner's identity,
   not a binding row.

`plannerCheckInput` (`cmd/relevo/doctor.go` ~634). Right after
`in.Resolved = &rec`:

    if lbl := chatResolver().Resolve(context.Background(), rec.HarnessKind, rec.SessionID, rec.TranscriptLocator); lbl != (chatlabel.Label{}) {
        in.Chat = lbl.String()
    }

## 6. Error handling

Nothing new can fail. Every lookup failure ends in the empty label, and the empty
label reproduces today's output byte-for-byte on every surface except the
statusline, which gains its planner line.

## 7. Tests

**The CLI test must spawn nothing and reach no network.** Use claude records
only, and exercise `annotatePlannerChat` directly rather than running
`relevo status` end to end.

1. **`TestRenderStatusPlannerChat`** (`internal/relevo/status_test.go`). Build a
   `Report` with two `BindingStatus` rows, using the fields `RenderStatus` needs.
   Copy the minimum from an existing `RenderStatus` test in that file.
   - Row one has `PlannerName: "architect-1"`, `PlannerChatLabel: "\"fix the flake\""`
     and `PlannerChatLink: "https://claude.ai/code/session_01TEST"`. Its planner
     line ends with `route <route> · "fix the flake" · https://claude.ai/code/session_01TEST`.
   - Row two has neither field set. Its planner line ends with `route <route>`
     and nothing after it.
2. **`TestRenderPlannerLine`** (`internal/relevo/statusline_test.go`):
   - `RenderPlannerLine("architect-14", 80)` contains `planner architect-14` and
     ends in `\n`;
   - `RenderPlannerLine("", 80) == ""`;
   - with `columns = 10`, the visible text (after stripping ANSI codes) is at
     most 10 runes.
3. **`TestAnnotatePlannerChat`** (`cmd/relevo/planner_test.go`). Set a temp
   `XDG_STATE_HOME`, then build a `planner.FileRegistry` exactly as
   `TestListAlignsLongValues` does.
   - Create one claude record whose `TranscriptLocator` is a synthetic
     transcript with a `custom-title` and a `bridge-session` line.
   - Build a `relevo.Runtime{Planners: reg}` and a `Report` with three rows:
     - two naming that planner's ID;
     - one naming `pl_zzzzzzzzzzzz`, which does not exist.
   - After `annotatePlannerChat(rt, &rep, chatlabel.Resolver{})`:
     - both rows of the known planner carry the title and the
       `https://claude.ai/code/session_…` link;
     - the unknown planner's row carries neither.
4. **`TestPlannerRowNamesChat`** (`internal/doctor/planner_test.go`). Model it on
   the existing tests around lines 94-118. Build `PlannerChecks` input with
   `Detected: true`, a `Resolved` record, `MCPChild: true` and
   `ClaimLive: false`.
   - With `Chat: "relevo-planner · https://claude.ai/code/session_01TEST"`, the
     `planner` row's Detail contains
     `planner <name> (<id>) · relevo-planner · https://claude.ai/code/session_01TEST: tools mode`.
   - With `Chat: ""`, the Detail equals the exact string today's code produces.

**Existing tests are ported, not deleted.** No test should need editing, because
each surface is byte-identical with an empty label. If one fails, stop and report
which one and why. Do not edit it to pass.

**Mutations.** Apply each one, run the named package's tests, confirm the named
test fails, then revert. List each one in the report.

- **M1:** `RenderStatus` never appends the suffix.
  `TestRenderStatusPlannerChat` fails.
- **M2:** `annotatePlannerChat` sets `PlannerChatLabel` but not
  `PlannerChatLink`. `TestAnnotatePlannerChat` fails.
- **M3:** `plannerRef` ignores `chat`. `TestPlannerRowNamesChat` fails.
- **M4:** `RenderPlannerLine` returns `""` unconditionally.
  `TestRenderPlannerLine` fails.

## 8. Working efficiently

Each model step costs a full round trip, so:

- Batch the reads of the files in §2 as parallel tool calls in one step. They are
  already located; do not re-find them.
- Make every change to one file in one edit call.
- **Focused loop:**
  - `go test -count=1 ./internal/relevo/ -run 'TestRenderStatus|TestRenderPlannerLine'`
  - `go test -count=1 ./internal/doctor/`
  - `go test -count=1 -run 'TestAnnotatePlannerChat|TestList' ./cmd/relevo/`
  - Fix every reported error before the next run.
- **Once at the end:** `make check` and `sh scripts/check-name.sh`.

## 9. Ordered steps

1. **internal/relevo.** Deliverable: the `BindingStatus` fields, the
   `RenderStatus` suffix, `RenderPlannerLine`, and tests 1–2. Verify: the first
   focused command passes, and so does the full `go test -count=1 ./internal/relevo/`.
2. **internal/doctor.** Deliverable: `PlannerCheckInput.Chat`, `plannerRef`, the
   three details, and test 4. Verify: `go test -count=1 ./internal/doctor/`.
3. **cmd/relevo.** Deliverable: `annotatePlannerChat`, the `cmdStatus` call, the
   `cmdStatusline` print, `plannerCheckInput`'s `in.Chat`, and test 3. Verify:
   `go test -count=1 ./cmd/relevo/`.
4. **Mutations M1–M4.** Verify: every package passes again after the last revert.
5. **README.** In `## Status line` (~line 1213), after the first sentence, add:

   > The first line names the planner (`planner architect-14`), so each terminal
   > shows which planner it is; `relevo planner list` maps that name to its chat.

   In the paragraph round 1 added after line 251, add one sentence saying the
   same label follows the planner's name in `relevo status` and `relevo doctor`.
   Verify: `sh scripts/check-name.sh`.
6. **Full check.** `make check` passes. Then make one commit:

       feat(planner): status and doctor name each planner's chat; the statusline names the planner (#386)

   Do not push. The report lists:
   - the files changed;
   - each mutation and the test that failed for it;
   - the `make check` result.
