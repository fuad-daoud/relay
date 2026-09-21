# Wave 2 chain M, step 1: relay send --dry-run -- every precondition Send runs, printed, with zero writes; Send and the dry run share one preflight (#149)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. Never
run `make check` here (the planner runs it); run the gate commands in §7
exactly as written. Every command in the foreground; no sub-agents for
edits. If the pre-flight `git status` shows a dirty tree, follow your
definition's rule for a tree that already carries part of the plan.

## 1. System overview

`Send` (`internal/relay/send.go:97-303`) checks its preconditions in two
halves: before the store lock (plan readable :100, tier parse/cap :105,
`Load` hint :119 -- errors swallowed, remote branch-off :120, pane tier
:123, baseline capture :126, pane lookup :130) and inside `WithLock`
(`tx.Load` :152, broken :156, round cap :159, headless busy :165, pane
located/same :181, tier :190). The first write is the staged plan at :200;
for a headless binding with no process yet, a missing `Runner` and an
unsupported tier are only discovered by `startRound` **after** that write
(`headless.go:84`, `send.go:208-213`). A planner composing a first send, or
a script, cannot ask "would this work, and to whom" first. After this
round: the read-only checks live in one `sendPreflight` that both `Send`
and the new `SendDryRun` call; `Send` keeps its in-lock re-checks (races)
and its writes; `SendDryRun` returns a `DryRun` description and makes no
write -- no staged plan, no log entry, no `Save`, no `Prompt`, no
`Runner.Start`, no baseline snapshot (`CaptureBaseline` adds git objects,
so it is **not** part of the preflight). The preflight also gives `Send`
the runner-present and launch-args checks before staging, which is a small
behaviour fix.

## 2. File structure

```
internal/relay/send.go         + preflight type, sendPreflight(); Send uses it; + DryRun type, SendDryRun()
internal/relay/text.go         + RenderDryRun(d DryRun) string
internal/relay/send_test.go    + tests (§7); existing tests unchanged
internal/relay/headless_test.go + TestSendHeadlessWithoutRunnerStagesNothing
cmd/relay/main.go              cmdSend: --dry-run; help text
cmd/relay/main_test.go         + TestSendDryRunRequiresFile (fails before newRuntime; reaches no herdr)
README.md                      relay send: --dry-run paragraph with the sample output
docs/plans/2026-09-21-w2m1-send-dry-run.md   copy of this plan
```

## 3. Data structures

```
// internal/relay/send.go
type preflight struct {
    b        store.Binding      // the binding as loaded (read-only; Send re-loads under the lock)
    body     []byte             // the plan file's bytes
    tier     harness.Tier       // effective tier for this round (opts.Tier parsed, or effectiveTier(b))
    builder  herdr.Agent        // pane builders: the located agent
    located  bool               // pane builders: FindAgent succeeded
    argv     []string           // headless: headlessLaunch's argv (proves the launch is well-formed); nil for pane/remote
    gate     *ledger.Gate       // advisory: a gate on b.BuilderCandidate (rate-limited or roles_missing), nil when none
    planPath, reportPath, donePath string
    prompt   string             // composePrompt(...) -- computed, never sent
}

type DryRun struct {
    Name      string   `json:"name"`
    Round     int      `json:"round"`
    Mode      string   `json:"mode"`               // "pane" | "headless" | "remote"
    Candidate string   `json:"candidate"`
    Where     string   `json:"where"`              // pane: "pane w2:p4 (working)"; headless: the harness binary + first arg; remote: "server contabo, branch relay/x @ <sha12>"
    GateNote  string   `json:"gate_note,omitempty"` // "rate-limited until 00:26; the daemon would switch after start" / "roles missing: ...; the daemon would switch after start"
    PlanPath  string   `json:"plan_path"`
    PlanFrom  string   `json:"plan_from"`
    PlanBytes int64    `json:"plan_bytes"`
    ReportPath string  `json:"report_path"`
    DonePath  string   `json:"done_path"`
    Tier      string   `json:"tier"`
    PromptHead []string `json:"prompt_head"`        // the prompt's first two non-empty lines
}
```

## 4. Interfaces

```
// internal/relay/send.go
func sendPreflight(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (preflight, error)
    // in this order, returning Send's EXACT error text for each (move the fmt.Errorf calls, do not re-word):
    //  1. os.ReadFile(file)                                   -> "read plan %s: %w"
    //  2. opts.Tier parse + checkTierCap                      (send.go:105-113 verbatim)
    //  3. b, err := rt.Store.Load(name)                       -> err (ErrNotFound now surfaces here; same wrapped text as tx.Load gives)
    //  4. b.Builder.Remote(): rt.Remote == nil -> ErrRemoteUnavailable; rt.Git == nil -> ErrGitRequired; rt.Transport == nil -> the same error sendRemote gives;
    //     branch sha := rt.Git.RefSHA(ctx, b.Repo, "refs/heads/"+b.Branch) -> "resolve branch %s: %w" on error / not found;  (NO WhoAmI, NO bundle)
    //     return preflight{b, body, tier, ...paths, prompt}   -- Send's remote path continues to call sendRemote as today
    //  5. opts.Tier != "" && !b.Builder.Headless()            -> ErrTierPaneFixed text (send.go:123-124)
    //  6. b.State == StateBroken                              -> "binding %q is broken; rebind before sending"
    //  7. b.Round > b.RoundCap                                -> "binding %q hit its round cap of %d"
    //  8. headless: rt.Runner == nil -> fmt.Errorf("binding %q: %w", name, ErrRunnerUnavailable)   (now BEFORE staging, PID 0 or not)
    //     if b.Builder.PID != 0: Alive check + ErrBuilderBusy exactly as send.go:165-180
    //     argv, err := headlessLaunch(c, role, tier, roundBudget(b), prompt, b.CWD, rt.Store.Dir(b.Name)) with c from rt.Candidates.Lookup(ParseRef(b.BuilderCandidate)) -- the same lookups startRound does (headless.go:87-96), same error texts; ErrTierUnsupported / ErrExtraArgsPermission surface here
    //  9. pane: agents := rt.Herdr.ListAgents -> "list agents: %w"; FindAgent -> ErrBuilderGone text (send.go:130-141)
    // 10. gate := first gate in Gates(rt) with Token == b.BuilderCandidate and Kind in {RateLimited, RolesMissing} (advisory only; never an error)
    // paths: PlanPath/ReportPath/DonePath(name, b.Round); prompt: composePrompt(b, ...) -- the same text Send sends
    // MAKES NO WRITE. Store.Load takes the store lock briefly (it always has); that is a read.

func Send(ctx, rt, name, file string, opts SendOptions) (SendResult, error)
    // pf, err := sendPreflight(...); err -> return
    // remote: return sendRemote(ctx, rt, pf.b, pf.body, opts.Tier)   (unchanged behaviour)
    // baseline capture, hintRound, then WithLock exactly as today (the in-lock checks stay: they guard against a change between preflight and lock)
    // headless: startRound as today (it re-does the lookups; fine)
    // the pane `builder`/`locatedBuilder` come from pf

func SendDryRun(ctx, rt, name, file string, opts SendOptions) (DryRun, error)
    // pf, err := sendPreflight(...); err -> return DryRun{}, err   (the same error Send would give)
    // fill DryRun from pf: Mode, Where per §3; PlanFrom = file (absolute via filepath.Abs when possible), PlanBytes = len(pf.body);
    // GateNote from pf.gate: GateKindText(g.Kind) + " " + GateUntilText(g.Until) + "; the daemon would switch after start" (roles_missing: g.Note verbatim + same suffix)
    // PromptHead = first two non-empty lines of pf.prompt
    // MAKES NO WRITE (test-pinned).

// internal/relay/text.go
func RenderDryRun(d DryRun) string
    // would send round 5 to api-auth
    //   builder   headless agy/google/gemini-3.8-flash-high            <- Mode + Candidate; then "  (" + GateNote + ")" when set
    //   where     /usr/bin/agy ...                                     <- Where
    //   tier      yolo
    //   plan      /home/.../api-auth/005-plan.md  (staged from ./plan.md, 4.1 KiB)   <- humanBytes lives in cmd/relay/db.go; move it to internal/relay (exported HumanBytes) or duplicate the 1024-based formatter here and have cmd use the relay one -- one implementation, say which in the report
    //   report    /home/.../api-auth/005-report.md
    //   marker    /home/.../api-auth/005-done
    //   prompt    relay: round 5 · to builder "api-auth" · from the planner (not the human)
    //             Your working tree is: /home/.../.worktrees/api-auth

// cmd/relay/main.go cmdSend
    --dry-run bool "check every precondition and print what send would do, without sending"
    --file still required (before newRuntime, as today)
    dry run: d, err := relay.SendDryRun(...); err -> return err (exit 1, same text as send); fmt.Print(relay.RenderDryRun(d)); exit 0; no warnWaitingOnYou
    help line :57 gains " [--dry-run]"
```

## 5. Pseudocode

Covered by §4. The only structural change to `Send` is that its pre-lock
block becomes `sendPreflight`; the in-lock block is unchanged except that
the headless runner/launch checks now cannot fail there in the common case
(keep them anyway).

## 6. Error handling

- Every dry-run failure is the identical error `Send` would return for the
  same state, exit 1 (pinned by the table test below, which runs both).
- A remote binding's dry run does not contact the server; `Where` says so
  (`server contabo, branch relay/x @ <sha>; server not contacted`).
- `Gates(rt)` failing to read the ledger prints its stderr line as today
  and yields no gate note.

## 7. Ordered implementation steps

Commit prefix: exactly **one** `feat(send):` commit for the code (squash
step commits, or `chore:`/`test:` per step); the plan copy may be its own
`chore(plans):` commit.

### Task 1 -- preflight extraction (behaviour-preserving, plus the earlier runner check)

**Files:** `internal/relay/send.go`, `headless_test.go`.

**Test first:** `TestSendHeadlessWithoutRunnerStagesNothing`: `seedHeadless`
binding, `rt.Runner = nil`, `Send` -> `errors.Is(err, ErrRunnerUnavailable)`,
`os.Stat(PlanPath(name, 1))` is not-exist, log length unchanged, binding
`State` unchanged (not `needs_you`). (Today this stages the plan and halts
the binding -- confirm it fails before the change.)

**Then** extract `sendPreflight` per §4 and make `Send` call it. **Every
existing test in `send_test.go`, `headless_test.go`, `remote_test.go`,
`reconcile_test.go` passes unchanged** -- if one needs an assertion change,
halt and report which and why.

**Verify:** `go test -race -count=1 ./internal/relay/`.

### Task 2 -- SendDryRun and RenderDryRun

**Files:** `internal/relay/send.go`, `text.go`, `send_test.go`.

**Tests first**
- `TestSendDryRunPaneMakesNoWrites`: `seedBound` fixture; before: `bBefore := Load`, `nBefore := len(ReadLog)`; `d, err := SendDryRun(...)` -> nil; `d.Round == 1`, `d.Mode == "pane"`, `d.Where` contains `w2:p4`, `d.PlanPath == PlanPath("webshop", 1)`, `d.PromptHead[0]` == `OriginLine("webshop", 1, DirToBuilder, KindPlan)`; after: `len(f.prompts) == 0`, `os.Stat(PlanPath)` not-exist, `len(ReadLog) == nBefore`, `store.SameBinding(bBefore, Load)`. **Mutation check:** make `SendDryRun` call `Send` instead and every "no write" assertion fails.
- `TestSendDryRunHeadlessShowsArgv`: `seedHeadless`; `d.Mode == "headless"`, `d.Where` starts with the harness binary from `harness.Lookup(testAgyRef's kind).Binary`; `len(fr.specs) == 0`.
- `TestSendDryRunGateNote`: gate the candidate's provider via `Unavailable(rt, token, zero, "quota")` -> `d.GateNote` contains `rate-limited` and `would switch`; the dry run still succeeds.
- `TestSendDryRunErrorsMatchSend`: table over: missing plan file; unknown binding; `State = broken`; `Round > RoundCap`; pane builder gone (`f.agents` without the builder); headless busy (previous pid alive); headless `rt.Runner = nil`; `--tier edit` on a pane binding. For each: `_, dryErr := SendDryRun(...)`; `_, sendErr := Send(...)` on an identical fresh fixture; `dryErr.Error() == sendErr.Error()`; and after the dry run no plan file, no prompt, no start, log length unchanged.
- `TestRenderDryRunShape`: golden-ish string check of the seven labelled lines in order (`builder`, `where`, `tier`, `plan`, `report`, `marker`, `prompt`), `4.1 KiB` style size.

**Then** the code. **Verify:** `go test -race -count=1 ./internal/relay/`.

### Task 3 -- CLI, README, plan copy, gate

**Files:** `cmd/relay/main.go`, `main_test.go`, `README.md`.

- `TestSendDryRunRequiresFile`: `run([]string{"send", "--dry-run", "--name", "x"})` -> error contains `--file` (fails before `newRuntime`; reaches no herdr, CI has none).
- README `relay send`: a `--dry-run` paragraph with the rendered sample from §4 and one sentence that a failed precondition is the same error `send` gives, exit 1, nothing written.

Copy the plan file to `docs/plans/2026-09-21-w2m1-send-dry-run.md` and commit.

Gate, in the foreground, stop at the first failure:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Final commit message: `feat(send): --dry-run runs every precondition and prints the builder, paths and prompt with zero writes; Send shares the preflight and checks the runner before staging (#149)`

## Report

Per task: what was done, test names, verify output, the mutation check's
failing assertions, where `humanBytes` ended up. Commit shas (one
`feat:`). If any existing test needed changing, or any step was impossible
as written, say which and stop there.
