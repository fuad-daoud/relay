# Wave 2 chains K/L, step 0: headless consults -- relay ask --headless runs a one-shot read-only process and turns its final message into the findings (#147, #144 groundwork)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. Never
run `make check` here (the planner runs it); run the gate commands in §7
exactly as written. Every command in the foreground; no sub-agents for
edits. If the pre-flight `git status` shows a dirty tree, follow your
definition's rule for a tree that already carries part of the plan.

## 1. System overview

A consult today is a pane: `Ask` (`internal/relay/ask.go:93`) opens a tab,
`StartAgent`s the reviewer/researcher, prompts it to read the staged
question and write a findings file; `reconcileConsults` (`consult.go:57`)
watches the pane through herdr and `finishConsult` queues the findings to
the planner. Two things need a consult that is a **process** instead: #147
(`ask --round N` resumes a closed round's builder session -- a one-shot by
nature) and #144 (a verifier at round close). Neither exists because no
headless consult exists; this round adds it and nothing else. `relay ask
--headless` launches the consult through `proc.Runner` in the harness's
print form (`headlessLaunch`, the same argv builder builders use) with the
consult's tier; because a `read`-tier process may not be able to write a
file, the prompt asks for the findings **as the final message**, and relay
extracts that message from the stream at exit (`transcript.FinalText`, a
pure function per harness) and writes it to `FindingsPath` itself -- the
same file, the same `KindFindings` delivery, the same `relay reap`
bookkeeping (nothing to close). Refusals: a candidate whose harness cannot
honour the tier (opencode/codex on `read`) is refused as today
(`harness.ErrTierUnsupported`); remote bindings stay local-only.

## 2. File structure

```
internal/transcript/final.go        + FinalText(kind string, stream []byte) string  -- last assistant message per harness; "" when none
internal/transcript/final_test.go   + table over the four testdata streams
internal/store/store.go             + ConsultStreamPath(name, round, id) NNN-<id>-consult.jsonl, ConsultLogPath(...) NNN-<id>-consult.log
internal/store/types.go             Consult: no new fields -- Endpoint.Mode = ModeHeadless, PID/StartedAt/LogPath (existing) carry the process; Endpoint.LogPath = the STREAM path (usage's consultSource already reads LogPath as a stream)
internal/relay/ask.go               AskOptions + Headless bool; a headless branch in phase 2 (Runner.Start instead of openTab/StartAgent/prompt); consultHeadlessPrompt
internal/relay/consult.go           reconcileConsults: headless branch (Alive / ExitCode / timeout / FinalText -> FindingsPath -> finishConsult)
internal/relay/reap.go              headless consults: nothing to close; a running one is left alone (reap never kills)
internal/relay/ask_test.go          + TestAskHeadlessStartsAProcessNotAPane, TestAskHeadlessRefusesUnsupportedTier
internal/relay/consult_test.go      + TestHeadlessConsultFinalMessageBecomesFindings, TestHeadlessConsultExitWithoutTextIsSilent, TestHeadlessConsultTimesOut
cmd/relay/main.go                   ask: --headless flag; output line names the pid instead of a pane
README.md                           consults section: --headless paragraph
docs/plans/2026-09-21-w2kl0-headless-consult.md   copy of this plan
```

## 3. Data structures

None new beyond `AskOptions.Headless bool`. A headless consult's
`store.Consult.Endpoint` is `{AgentName, Kind, Mode: ModeHeadless, PID,
StartedAt, LogPath: <stream path>}`; `Endpoint.Headless()` already
distinguishes it everywhere.

## 4. Interfaces

```
// internal/transcript/final.go
func FinalText(kind string, stream []byte) string
    // scans lines; ignores the relay-exit trailer and non-JSON; returns the LAST assistant text:
    //   claude:   the last "assistant" event's concatenated text blocks (message.content[].type=="text"); if none, the "result" event's "result" string
    //   opencode: the last {"type":"text", part.text} event's text
    //   agy:      the "result" event's result.response; if empty, the last step_update text if the stream has one (read internal/transcript/agy.go for the field it renders)
    //   codex:    the last item.completed with item.type=="agent_message" -> item.text
    // trimmed; "" when nothing matched

// internal/relay/ask.go
const consultHeadlessPrompt = "Read: %s\n\nAnswer as your final message: your findings, complete, in markdown. Do not modify any file in this repository. Do not write a findings file; relay records your final message."
// phase 2, when opts.Headless:
//   c := the resolved candidate; l from h.Launch(...) as today (tier resolution unchanged; ErrTierUnsupported surfaces as today)
//   argv, err := headlessLaunch(c, role, tier, consultTimeout, fmt.Sprintf(consultHeadlessPrompt, askPath), b.CWD, rt.Store.Dir(b.Name))
//   h, err := rt.Runner.Start(ctx, ProcSpec{Dir: b.CWD, Argv: argv, LogPath: rt.Store.ConsultLogPath(...), StreamPath: rt.Store.ConsultStreamPath(...)})
//   rt.Runner == nil -> ErrRunnerUnavailable before anything is reserved (check it in phase 0 next to the role check)
//   Start error -> the consult is recorded Silent with note "spawn failed: <err>" (the shape of the pane StartAgent failure, TestAskRecordsAReapableConsultWhenTheStartFails)
//   success -> consult.Endpoint = {AgentName, Kind: l.Kind, Mode: ModeHeadless, PID: h.PID, StartedAt: h.StartedAt.Unix(), LogPath: streamPath}; State Running; the same pick + KindAsk log entries as the pane path
// AskResult: unchanged; cmd prints "asked %s consult %s on %s (pid %d)" for headless

// internal/relay/consult.go reconcileConsults, per running consult with c.Endpoint.Headless():
//   rt.Runner == nil -> leave it (cannot observe); log once at Warn
//   alive, err := rt.Runner.Alive(ctx, handleOf(c.Endpoint)); err -> treat as alive
//   alive: if now.Sub(c.SpawnedAt) >= consultTimeout -> rt.Runner.Kill; finishConsult(silent, "timed out after <d>; process killed")   else continue
//   exited: text := transcript.FinalText(c.Endpoint.Kind, read(stream)); code, ok := rt.Runner.ExitCode(ctx, handle, streamPath)
//           text != "" -> os.WriteFile(c.FindingsPath, text+"\n") then finishConsult(done, "") -- the existing done payload/usage path (consultSource reads Endpoint.LogPath as the stream: keep LogPath = stream path)
//           text == "" -> finishConsult(silent, fmt.Sprintf("process exited (code %s) with no final message; see %s", codeText, logPath))
//   the pane-only steps (FindAgent, nudge, dialog/blocked) are skipped for headless
// reap.go: a headless consult record is never "closable"; Reap reports it as Dropped only if State is Spawning (as today) and otherwise leaves it; no Kill
```

## 5. Pseudocode

Covered by §4. The headless branch in `reconcileConsults` sits before the
`FindAgent` lookup: `if c.Endpoint.Headless() { ...; continue }`.

## 6. Error handling

- `--headless` with a candidate whose harness refuses the resolved tier:
  the existing `ErrTierUnsupported` error, before any reservation.
- No runner configured: `ErrRunnerUnavailable`, before any reservation.
- Process died without a final message: silent consult with the exit code
  and log path in the note (the planner reads the log).
- Timeout: killed, silent, note says so. `consultTimeout` (10 m) is
  unchanged and also the print-form budget passed to `headlessLaunch`.

## 7. Ordered implementation steps

Commit prefix: exactly **one** `feat(ask):` commit for the code (squash
step commits, or `chore:`/`test:` per step); the plan copy may be its own
`chore(plans):` commit.

### Task 1 -- FinalText and the store paths

**Files:** `internal/transcript/final.go`, `final_test.go`, `internal/store/store.go` (+ a path test if `store_test.go` has a table of round-file names).

**Test first:** `TestFinalTextPerKind` over `internal/transcript/testdata/{claude,opencode,agy,codex}.jsonl` -- assert the exact last text each carries (read the fixtures and write the expectation from them; the opencode fixture's line 5 text is `done`, the codex fixture's last `agent_message` text is the expectation, the claude fixture's last assistant text block likewise; if agy's fixture has neither a response nor a text step, its expectation is `""` and add a synthetic line to cover the non-empty case). A stream of only the trailer -> `""`.

**Verify:** `go test -count=1 ./internal/transcript/ ./internal/store/`.

### Task 2 -- Ask --headless and the reconcile branch

**Files:** `internal/relay/ask.go`, `consult.go`, `reap.go`, `ask_test.go`, `consult_test.go`.

**Tests first** (fixtures: `seedForAsk(t, f)` ask_test.go:23; `fakeRunner`):
- `TestAskHeadlessStartsAProcessNotAPane`: `rt.Runner = fr`; `Ask{Role: "reviewer", Headless: true, ...}` -> `len(fr.specs) == 1`, spec `Dir == b.CWD`, `Argv[0]` is the harness binary, the prompt argument contains `Answer as your final message` and the ask path; `f.tabs` and `f.starts` and `f.prompts` empty; consult `Endpoint.Headless()`, `PID == fr.handles[0].PID`, `LogPath == ConsultStreamPath`; `KindAsk` entry logged. **Mutation check:** route `Headless` through the pane branch and this fails on `fr.specs`.
- `TestAskHeadlessRefusesUnsupportedTier`: opencode reviewer candidate on tier read -> `ErrTierUnsupported`, nothing reserved (shape of `TestAskReviewerOnOpencodeTierReadRefused` :784).
- `TestAskHeadlessWithoutRunnerIsRefused`: `rt.Runner = nil` -> `ErrRunnerUnavailable`, no consult record.
- `TestHeadlessConsultFinalMessageBecomesFindings`: after the ask, write the claude fixture (or two lines: an assistant text `FINDINGS BODY` and `relay-exit:0`) to the stream path; `fr.script(pid, false); fr.exit(pid, 0)`; tick -> `FindingsPath` exists with `FINDINGS BODY`, consult `State == done`, a `KindFindings` entry queued with `Path == FindingsPath`. **Mutation check:** skip the `WriteFile` and this fails.
- `TestHeadlessConsultExitWithoutTextIsSilent`: stream = trailer only, exit 1 -> silent, note contains `code 1` and the log path; no findings file.
- `TestHeadlessConsultTimesOut`: alive past `consultTimeout` (advance the fake clock) -> `fr.kills == 1`, silent, note contains `timed out`.
- Existing pane tests unchanged.

**Then** the code per §4. **Verify:** `go test -race -count=1 ./internal/relay/`.

### Task 3 -- CLI, README, plan copy, gate

**Files:** `cmd/relay/main.go`, `README.md`.

`cmdAsk`: `--headless` flag ("run the consult as a one-shot process instead of a pane; findings are its final message"); success line per §4. README consults section: one paragraph on `--headless` (when to use it, that the findings are the final message, tier note: claude/agy can run read-only, opencode/codex cannot and are refused on `read`).

Copy the plan file to `docs/plans/2026-09-21-w2kl0-headless-consult.md` and commit.

Gate, in the foreground, stop at the first failure:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```
`make e2e` is the planner's; say so.

Final commit message: `feat(ask): --headless runs a consult as a one-shot read-only process; its final message becomes the findings (#147, #144)`

## Report

Per task: what was done, test names, verify output, the mutation checks'
failing tests, the exact FinalText expectations you derived from the
fixtures. Commit shas (one `feat:`). If any step was impossible as
written, say which and stop there.
