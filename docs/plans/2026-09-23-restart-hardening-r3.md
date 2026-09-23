# Plan: restart hardening, round 3 of 3: resume a lost builder (#370)

**Read first:** `docs/specs/2026-09-23-restart-hardening-design.md` in your tree, §4.3 and §4.10. This round implements the R3 half of §4.3 plus §4.10. Where this plan and the spec disagree, the spec wins, **except for the prompt amendment in §4 below**. Stop and report rather than pick.

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, report which step and why, and create the done marker. Don't bend a test to fit.

## 1. System overview

When a local builder is lost to a daemon restart (round 1's `lostToRestart`: no exit trailer, started before this daemon, never seen alive by it), relay today relaunches it as a **fresh** harness session. The fresh session redoes the research and pays for it twice.

After this round, relay first tries to **continue the builder's own session**:
- claude: `--resume <id>`;
- agy: `--conversation <id>`;
- opencode: `--session <id> --fork`.

It uses the *full builder launch argv*, so the model, agent definition and permission flags are identical to the original round. codex, any harness whose flags can't be verified, and a round that never announced a session fall back to round 1's fresh relaunch. Either way the relaunch stays uncounted, keeps `RoundStartedAt`, and carries round 1's interrupted note.

Also in this round: correct a stale comment in `dist/relay.service`.

## 2. File structure

```
internal/harness/resume.go (+ resume_test.go)   ResumeBuild (spec §4.10)
internal/relay/headless.go (+ headless_test.go) startProcess extraction; resumeRound; lost branch tries resume first
dist/relay.service                              comment fix only (see step 5)
```

## 3. Data structures

None new. Uses `store.Endpoint.StreamSessionID` (types.go:129), `harness.Launch{Kind, Print, PromptAt}` and `ErrResumeUnsupported`.

## 4. Interfaces and contracts

`func (h Harness) ResumeBuild(sessionID string, l Launch, prompt string, budget time.Duration, dir, state string) ([]string, error)`

- It validates `sessionID` exactly as `Resume` does: non-empty, no whitespace, no leading `-`.
- It returns `l.PrintArgs(prompt, budget, dir, state)` followed by the resume selector:

  | h.Kind | selector appended |
  |---|---|
  | claude | `--resume <id>` |
  | agy | `--conversation <id>` |
  | opencode | `--session <id> --fork` |
  | codex, anything else | `nil, fmt.Errorf("%w: <kind>", ErrResumeUnsupported)` |

- The result is the argv *after* the binary. The caller prepends `h.Binary`, as `headlessLaunch` does.

`func startProcess(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, argv []string, c candidate.Candidate) (store.Binding, error)`

- This is exactly the part of today's `startRound` after argv is built:
  - the `StreamRound` bookkeeping;
  - clearing `StreamSessionID`;
  - `ProcSpec` with the scope;
  - `Runner.Start`;
  - the spawn-failure ledger record;
  - the `Watched.Mark`;
  - setting PID, StartedAt and LogPath.
- `startRound` becomes: CPU assignment, candidate lookup, `headlessLaunch`, then `startProcess`. Its behaviour must stay byte-identical: every existing startRound, switch and send test passes unedited.
- If `assignRoundCPU` or anything else belongs before argv in a way that makes this split unclean, halt.

`func resumeRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, sessionID, prompt string) (store.Binding, error)`

- It has startRound's pre- and postconditions.
- It resolves the candidate like startRound, builds `l` with `h.Launch(c.Provider, c.Model, c.ExtraArgs, role, effectiveTier(b))`, and builds argv as `append([]string{h.Binary}, h.ResumeBuild(sessionID, l, prompt, roundBudget(b), b.CWD, rt.Store.Dir(b.Name))...)`. Then it calls `startProcess`.
- It returns `ErrResumeUnsupported` (wrapped) unchanged, so the caller can fall back.

**Prompt amendment:** the resume prompt is **the same text** as round 1's fresh relaunch prompt: `composePrompt(...) + "\n\n" + interruptedNote(rt.StartedAt)`. composePrompt already restates the plan, report, done-marker paths and the report block, which satisfies spec §4.10's "restates the contract". Don't write a second prompt.

## 5. High-level pseudocode

```
lost && switchable && local:
  text := composePrompt(...) + "\n\n" + interruptedNote(rt.StartedAt)
  keep := b.RoundStartedAt
  sess := b.Builder.StreamSessionID            // read before anything clears it
  var next store.Binding; var err error; how := "relaunched"
  if sess != "":
     next, err = resumeRound(ctx, rt, tx, b, sess, text)
     if err == nil { how = "resumed session " + sess }
     else if errors.Is(err, harness.ErrResumeUnsupported) { err = nil; next, err = startRound(ctx, rt, tx, b, text) }
     else if the spawn itself failed { fall back to startRound once as well, and log Warn "resume failed; relaunching fresh" }
  else:
     next, err = startRound(ctx, rt, tx, b, text)
  if err != nil -> haltBinding (unchanged message)
  next.RoundStartedAt = keep
  the KindSwitch log note: "<how> builder (lost to a daemon restart at T): picked <cand> for builder: same candidate, not counted"
     -- today's note starts "relaunched builder (lost ..."; keep that exact prefix for the fresh path
  appendLogMarker(..., how + " " + cand + " (lost to a daemon restart)")
```

## 6. Error handling strategy

- Unsupported resume → silent fallback to fresh, which is expected for codex.
- A resume spawn failure → one fresh attempt. If that fails too, today's halt.
- A resumed session that dies again is judged by round 1's rules on later ticks, like any builder.
- No new halt reasons.

## 7. Ordered implementation steps

**Step 0: verify the flags.** Do not guess them. For each of claude, agy and opencode, if its binary is on PATH, run its help (`claude --help`, `agy --help`, `opencode run --help`) and confirm both of these:
- the resume selector exists;
- it can be combined with the print-form flags `Launch` already emits for that kind (`-p`/`--model`/`--agent`/`--output-format` for claude; `run`/`-m`/`--agent`/`--format json`/`--standalone` for opencode; the agy equivalents).

Record what you checked, verbatim help lines, in the report.

If a harness's binary is missing, or its help doesn't show the selector, make `ResumeBuild` return `ErrResumeUnsupported` for that kind and say so in the report. That is the safe fallback, not a halt. **Don't run a real harness session**: help output only, with no prompt, no network and no spend.

**Step 1: `ResumeBuild`.** A table test per kind, over a real `Launch` built with `Launch(provider, model, nil, builderRole, TierHarness)`:
- the argv equals `PrintArgs(...)` plus the selector;
- codex and an unknown kind → `ErrResumeUnsupported`;
- a bad session id → an error.

**Step 2: extract `startProcess`.** All existing tests pass unedited.

**Step 3: `resumeRound`, and the lost branch** (§5). Tests with the fake Runner:
- (a) a claude-kind builder with `StreamSessionID` set, lost → the second Start's argv contains `--resume <id>` and the model flag, the prompt contains the interrupted note, the log note starts with "resumed session", `RoundSwitches == 0` and `RoundStartedAt` is unchanged;
- (b) the same with a codex candidate → fresh argv without any resume flag, and the note starts "relaunched builder";
- (c) no session → fresh;
- (d) the fake Runner fails only the first Start → a second, fresh Start happens.

The existing `TestReconcileHeadlessLostToDaemonRestartRelaunches` must keep passing as written after round 1b's amendment. If its fixture announces a session and it now resumes, **halt** rather than edit it.

Mutations:
- remove the `sess != ""` branch → (a) fails;
- remove the Unsupported fallback → (b) fails.

**Step 4: live check** (no harness spend). This needs no real harness: a shell script named `claude` on a temp PATH that logs its argv. Run `go test` only. Mention in the report that the step-0 help check is the only contact with real harness binaries.

**Step 5: unit comment.** `dist/relay.service`'s `# No MemoryMax: builders started with relay add are children of the daemon and share this unit's cgroup.` is stale since #309.

Reword it:
- builders, gates and consults run in their own `relay-*.scope` units and survive a restart of this one (verified 2026-09-23, #370);
- a builder runs in this cgroup only when scopes are unavailable (`relay doctor` → `restart`), which is why there is still no MemoryMax here.

Keep the `OOMPolicy` lines and their meaning. The template test must still pass.

**Step 6: gate.** `make check` and `make e2e`. Commit ending `(#370)`.

Declared scope: §2's files.
