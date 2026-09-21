# Wave 2 chain J, step 1: a per-binding progress clock from tree, output and screen -- stalled / exploring / stale labels and hook events, never actions (#135)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. Never
run `make check` here (the planner runs it); run the gate commands in §7
exactly as written. Every command in the foreground; no sub-agents for
edits. If the pre-flight `git status` shows a dirty tree, follow your
definition's rule for a tree that already carries part of the plan.

## 1. System overview

#252 (merged as `feat(headless): ... stalled`) gave **headless** builders
a stall label from one signal: the stream file's mtime
(`internal/relay/headless.go` `streamLastActivity`, the block around
`:418-426`, `Binding.StalledSince`, policy `stall_after_ms`, hook
`builder_stalled`, `status`/`ui` `stalled <age>`). A **pane** builder that
is `working` for ninety minutes without touching a file still looks like
one halfway through the plan, and a NEEDS YOU row has no age. This round
generalises the clock to every local builder and adds two labels:

- **Progress sample**, taken by the daemon every `progress_interval_ms`
  (default 30 s) for a binding with an open round: the tree fingerprint
  (`HEAD` + `git status --porcelain`, hashed -- no diff, no snapshot), the
  builder's output (headless: the stream mtime as today; pane: a screen
  fingerprint, the same read `screenFingerprint` does for the nudge path
  but kept in its own field so the quiescence clock is untouched -- design
  question 1: independent). Each signal keeps the time it last changed.
- **stalled** -- no signal changed for `stall_after_ms` while the round is
  open and the builder is not `blocked`. Same field, event and label as
  #252 (`StalledSince`, `builder_stalled`, `stalled <age>`), now for pane
  builders too, plus one notification with sound.
- **exploring** -- output or screen is changing but the tree has not for
  `explore_after_ms` (default 20 m). `ExploringSince`, label
  `exploring <age>`, no event (a label only; some plans are read-heavy).
- **stale** -- a NEEDS YOU or HELD binding unacted for `stale_after_ms`
  (default 4 h): `StaleSince`, hook `binding_stale` once, one notification,
  label `stale <age>` after the state word, and the row sorts first.

Relay never acts on any of these: no kill, nudge, switch or halt. The
round budget stays the only automatic halt. Design question 2 (`relay
explain`) is not in this round.

## 2. File structure

```
internal/policy/policy.go          + ProgressIntervalMS, ExploreAfterMS, StaleAfterMS (*int, > 0) with accessors ProgressInterval() 30s, ExploreAfter() 20m, StaleAfter() 4h
internal/policy/policy_test.go     + tests
internal/store/types.go            Binding + Progress *Progress, ExploringSince, StaleSince, StaleNotifiedAt time.Time; + type Progress
internal/hooks/events.go           + EventBindingStale EventType = "binding_stale"
internal/relay/herdr.go            Git + TreeFingerprint(ctx, dir string) (string, error)
internal/git/client.go             + TreeFingerprint: sha256 of (rev-parse HEAD + "\n" + status --porcelain)
internal/git/client_test.go        + TestTreeFingerprintChangesOnEditAndCommit
internal/relay/fake_test.go        fakeGit + treeFingerprints []string (sequence; last repeats), treeFingerprintErr
internal/relay/progress.go         + progressStep(ctx, rt, b, now, signals) store.Binding, sampleSignals(), labelsOf()
internal/relay/progress_test.go    + pure tests over synthetic samples
internal/relay/reconcile.go        pane path: call progressStep for an open round; needs_you/held path: stale stamp; emitMutations: binding_stale; Send/round close/resume resets
internal/relay/headless.go         the #252 stall block is replaced by progressStep (one implementation)
internal/relay/status.go           BuilderStatus: "stalled <age>" | "exploring <age>"; row.Stale "stale <age>"; JSON last_progress_at, stall, exploring, stale; RenderStatus prints stale after the state word
internal/relay/sort.go             stale rows first within NEEDS YOU / HELD
internal/ui/rail.go                whatAge: NEEDS YOU/HELD rows append " · stale 4h"
internal/relay/*_test.go, internal/ui/*_test.go   as named in §7
README.md                          headless "stalled" paragraph becomes the "Progress labels" subsection; policy keys; hooks list
docs/plans/2026-09-21-w2j1-progress-clock.md   copy of this plan
```

## 3. Data structures

```
// internal/store/types.go
type Progress struct {
    SampledAt time.Time `json:"sampled_at"`           // last sample time
    Tree      string    `json:"tree,omitempty"`        // last tree fingerprint
    TreeAt    time.Time `json:"tree_at"`              // when Tree last changed (round start when never)
    Output    string    `json:"output,omitempty"`      // pane: last screen fingerprint; headless: unused ("")
    OutputAt  time.Time `json:"output_at"`            // pane: when the screen last changed; headless: the stream's last activity
}
Binding.Progress       *Progress `json:"progress,omitempty"`   // transient per round; nil before the first sample; cleared at round close and Send
Binding.ExploringSince time.Time `json:"exploring_since,omitempty"`
Binding.StaleSince     time.Time `json:"stale_since,omitempty"`
Binding.StaleNotifiedAt time.Time `json:"stale_notified_at,omitempty"`   // one notification per stale episode
// StalledSince already exists (#252).

// internal/policy
ProgressIntervalMS *int  // default 30_000
ExploreAfterMS     *int  // default 1_200_000
StaleAfterMS       *int  // default 14_400_000
```

## 4. Interfaces

```
// internal/git/client.go
func (c *Client) TreeFingerprint(ctx, dir string) (string, error)
    // head := rev-parse HEAD (may fail on an unborn branch -> ""), st := status --porcelain; return hex(sha256(head + "\n" + st))[:16]

// internal/relay/progress.go
type signals struct {
    tree     string    // "" when unavailable (no git / no cwd)
    output   string    // pane screen fingerprint; "" for headless
    outputAt time.Time // headless: streamLastActivity; pane: zero (derived from output changes)
    blocked  bool      // pane builder status is blocked -> never stalled
}
func sampleSignals(ctx context.Context, rt Runtime, b store.Binding, agents []herdr.Agent) signals
    // tree: rt.Git != nil && b.CWD != "" -> rt.Git.TreeFingerprint(ctx, b.CWD) (error -> "")
    // pane: fp, err := screenFingerprint(ctx, rt, b) (error -> ""); blocked from FindAgent(agents, b.Builder).Status == herdr.StatusBlocked
    // headless: outputAt = streamLastActivity(rt, b)
func progressStep(rt Runtime, b store.Binding, now time.Time, s signals) store.Binding
    // precondition: the round is open (RoundStartedAt non-zero)
    // if b.Progress == nil { b.Progress = &Progress{SampledAt: now, Tree: s.tree, TreeAt: b.RoundStartedAt, Output: s.output, OutputAt: b.RoundStartedAt} }
    // else if now.Sub(b.Progress.SampledAt) < rt.Policy.ProgressInterval() { return b }   // not yet
    // else { p := b.Progress; p.SampledAt = now
    //        if s.tree != "" && s.tree != p.Tree { p.Tree, p.TreeAt = s.tree, now }
    //        pane:     if s.output != "" && s.output != p.Output { p.Output, p.OutputAt = s.output, now }
    //        headless: if s.outputAt.After(p.OutputAt) { p.OutputAt = s.outputAt } }
    // last := max(p.TreeAt, p.OutputAt)
    // stalled: !s.blocked && now.Sub(last) >= rt.Policy.StallAfter() -> if StalledSince.IsZero() { StalledSince = last; slog.Warn(...) }   else StalledSince = zero
    // exploring: StalledSince.IsZero() && p.OutputAt.After(p.TreeAt) && now.Sub(p.TreeAt) >= rt.Policy.ExploreAfter() -> if ExploringSince.IsZero() { ExploringSince = p.TreeAt }   else ExploringSince = zero
    // returns b
func labelsOf(b store.Binding, now time.Time) (working string, stale string)
    // working: "stalled <AgeText(now-StalledSince)>" | "exploring <age>" | ""   (stalled wins)
    // stale:   "stale <AgeText(now-StaleSince)>" | ""

// reconcile.go
//  pane path, open round (where the nudge/quiescence code runs): b = progressStep(rt, b, now, sampleSignals(ctx, rt, b, agents))   -- ONLY when the round is open; skip when b.State is needs_you/held/done/paused
//  headless.go alive branch: replace the #252 block with b = progressStep(rt, b, now, sampleSignals(ctx, rt, b, nil))   (tree + stream mtime; the stall semantics for headless are unchanged: stream quiet AND tree unchanged; note that this is a strict superset of #252's rule -- a tree that changes while the stream is quiet is no longer stalled, which is the correct reading)
//  stale: for b.State in {needs_you, held}: since := HaltAt when non-zero, else the newest log entry TS; if now.Sub(since) >= StaleAfter && StaleSince.IsZero() { StaleSince = since }; for every other state StaleSince = zero
//  emitMutations: orig.StaleSince.IsZero() && !next.StaleSince.IsZero() -> Dispatch(Event{Type: EventBindingStale, ...}); the stalled event is unchanged
//  notifications (once per episode): stalled -> when StalledSince goes zero->set and the builder is a pane or headless: rt.Herdr.Notify(ctx, "<name>: builder stalled (no progress for <age>)", "round N", herdr.SoundRequest); stale -> when StaleSince set and StaleNotifiedAt.IsZero(): Notify("<name>: NEEDS YOU for <age>", ..., SoundRequest); StaleNotifiedAt = now. Find the existing Notify call shapes (haltBinding) and reuse the sound constant.
//  resets: Send (human) and round close: Progress = nil, ExploringSince = zero, StalledSince = zero (already), StaleSince = zero, StaleNotifiedAt = zero; resume/rebind: the same

// status.go
//  headless rows: replace the #252 "stalled <age>" with labelsOf's working label (it includes stalled); pane rows: when BuilderStatus == "working" and label != "" -> BuilderStatus = label
//  row.Stale (string, json:"stale,omitempty"), row.LastProgressAt (time.Time, json:"last_progress_at,omitempty" = max(TreeAt, OutputAt) when Progress != nil), row.Stall (json:"stall,omitempty" = the working label when it starts with "stalled"), row.Exploring (json:"exploring,omitempty")
//  RenderStatus: after the display word print "  stale 4h10m" when row.Stale != ""
// sort.go: within equal attentionRank, a row with Stale != "" sorts before one without (then name)
// ui rail.go whatAge: for NEEDS YOU / HELD rows with b.Stale != "": what += " · " + b.Stale
```

## 5. Pseudocode

Covered by §4. The 30 s cadence is enforced inside `progressStep` by
`SampledAt`, so callers call it every tick and pay one `git status` and one
screen read per binding per 30 s at most.

## 6. Error handling

- Any signal that cannot be read is simply absent for that sample (`""` /
  zero); a binding with no readable signal at all (no git, no pane, no
  stream) never stalls: `last` falls back to `RoundStartedAt` only when at
  least one signal was ever sampled non-empty -- if both `tree` and output
  are unavailable, `progressStep` records the sample and sets no label.
- Notify failures are logged at Warn and do not fail the tick (unlike halt
  notifications, these are advisory).

## 7. Ordered implementation steps

Commit prefix: exactly **one** `feat(progress):` commit for the code
(squash step commits, or `chore:`/`test:` per step); the plan copy may be
its own `chore(plans):` commit.

### Task 1 -- policy, store, git, events

**Files:** `internal/policy/policy.go`, `policy_test.go`, `internal/store/types.go`, `internal/hooks/events.go`, `internal/relay/herdr.go`, `internal/git/client.go`, `client_test.go`, `internal/relay/fake_test.go`.

**Tests first:** policy defaults/validation for the three keys (shape of the `stall_after_ms` test); `TestTreeFingerprintChangesOnEditAndCommit`: same fingerprint twice; edit a tracked file -> different; commit -> different again; untracked file -> different.

**Verify:** `go test -count=1 ./internal/policy/ ./internal/git/ ./internal/relay/`.

### Task 2 -- progressStep and the daemon

**Files:** `internal/relay/progress.go`, `progress_test.go`, `reconcile.go`, `headless.go`, `send.go` (resets), `bind.go` (resume resets), `reconcile_hooks_test.go`, `headless_test.go`, `reconcile_test.go`.

**Tests first** (pure, with a fixed `now` and a `Runtime{Policy: ...}`):
- `TestProgressTreeChangeResetsEverything`: samples 30 s apart; tree changes at t+10m after 25 m quiet -> `StalledSince` zero, `ExploringSince` zero.
- `TestProgressScreenOnlyIsExploringAfterThreshold`: output changes every sample, tree constant -> at t+20m `ExploringSince == TreeAt` (== round start); at t+19m zero. **Mutation check:** compare against `StallAfter` instead of `ExploreAfter` and this fails.
- `TestProgressNothingIsStalled`: no changes -> at t+15m `StalledSince == RoundStartedAt`; `ExploringSince` zero (stalled wins).
- `TestProgressBlockedNeverStalls`: same with `blocked: true` -> zero.
- `TestProgressHeadlessStreamMtimeStillCounts`: headless signals (outputAt advancing) -> not stalled; tree constant for 20 m -> exploring.
- `TestProgressNoSignalsNoLabel`: tree "" and output "" and outputAt zero -> no labels ever.
- `TestProgressSampleCadence`: two calls 10 s apart sample once (`SampledAt` unchanged on the second).
- Daemon: `TestReconcilePaneStampsStall` (pane fixture `sentBinding`, fake herdr `readOut` constant, fakeGit fingerprint constant, clock advanced 16 m across ticks -> `StalledSince` set, one `builder_stalled` hook event, one notice); `TestReconcileNeedsYouGoesStale` (a halted binding with `HaltAt = now-5h` -> `StaleSince` set, one `binding_stale` event, one notice; a second tick adds nothing; `Send` clears it). The existing #252 tests (`TestReconcileHeadlessStampsStallWhenStreamQuiet` etc.) must still pass -- if one needs a fakeGit fingerprint to be present, add it and say so.

**Then** the code. **Verify:** `go test -race -count=1 ./internal/relay/`.

### Task 3 -- status, sort, ui, README, plan copy, gate

**Files:** `internal/relay/status.go`, `sort.go`, `status_test.go`, `sort_test.go`, `internal/ui/rail.go`, `rail_test.go`, `README.md`.

**Tests first:** `TestStatusLabelsStalledExploringStale` (three rows: pane stalled -> `BuilderStatus == "stalled 17m"`; headless exploring -> `"exploring 22m"`; needs_you stale -> `Stale == "stale 4h10m"`, JSON keys present); `TestSortStaleFirst`; rail test: a NEEDS YOU row with `Stale` shows ` · stale 4h`. Goldens: regenerate only if one changes, and say so.

README: replace the #252 "stalled" paragraph under headless builders with a "### Progress labels" subsection (the three labels, their thresholds and policy keys, the two hook events, "never an action"); the hooks section lists `binding_stale`; the policy example gains the three keys.

Copy the plan file to `docs/plans/2026-09-21-w2j1-progress-clock.md` and commit.

Gate, in the foreground, stop at the first failure:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```
`make e2e` is the planner's (this touches the reconcile paths and reads the same screen the nudge path does); say so.

Final commit message: `feat(progress): a per-binding progress clock from tree, output and screen -- stalled, exploring and stale labels with hook events, never an action (#135)`

## Report

Per task: what was done, test names, verify output, the mutation check's
failing test, whether goldens were regenerated, whether any #252 test
needed a fixture change. Commit shas (one `feat:`). If any step was
impossible as written, say which and stop there.
