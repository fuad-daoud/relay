# Wave 2 chain K, step 2b: relay ask --round N -- resume the session that built round N and ask it, headless and read-only; the answer arrives as a consult (#147 part 2)

One feature in one round. This plan stands alone: everything you need is
in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo. Never
run `make check` here (the planner runs it); run the gate commands in §7
exactly as written. Every command in the foreground; no sub-agents for
edits. If the pre-flight `git status` shows a dirty tree, follow your
definition's rule for a tree that already carries part of the plan.

## 1. System overview

Two pieces landed for this: a closed round's report entry carries
`BuilderSession{Kind, ID}` (`internal/store/log.go`, written by
`queueReport`), and `relay ask --headless` runs a consult as a one-shot
process whose final message becomes the findings (`internal/relay/ask.go`
headless branch, `reconcileConsults` headless branch, `transcript.FinalText`).
This round joins them: `relay ask --round N (--file q.md | -q "...") <binding>`
looks up round N's report entry, refuses when it has no session (older
round, unknown id) or when N is the open round (that is the live builder),
stages the question with a preamble ("You built round N of this binding.
Answer from what you did and why; do not change anything."), and launches
the harness's **resume** form as a headless consult: claude `-p --resume <id>`,
agy `-p --conversation <id>`, opencode `run --session <id> --fork`. The
resumed session gets tier `read` where the harness has one (claude, agy);
opencode has none, so it runs on `harness` and `--fork` keeps the original
session untouched; codex resume is not verified and is refused. Design
question 1: resuming mutates the session on claude/agy -- accepted and
documented (the session is the builder's, the round is closed). Question
2: the open round is refused.

## 2. File structure

```
internal/harness/resume.go         + (h Harness) Resume(sessionID, prompt string, tier Tier) ([]string, error); ErrResumeUnsupported
internal/harness/resume_test.go    + per-kind argv table; codex refused; tier args appended
internal/relay/ask.go              AskOptions + Round int, Question string; askRound path (shares reserve/record/launch with the headless branch)
internal/relay/ask_test.go         + tests (§7)
internal/relay/session.go          + roundSession(entries, round) (*store.BuilderSession, bool)
cmd/relay/main.go                  ask: --round N, -q/--question; validation; output line
cmd/relay/main_test.go             + TestAskRoundNeedsAQuestion (fails before newRuntime; reaches no herdr)
README.md                          consults: "### Asking a past round's builder" (what it does, the mutation note, opencode fork, codex refused)
docs/plans/2026-09-21-w2k2b-ask-round.md   copy of this plan
```

## 3. Data structures

```
// internal/relay/ask.go  AskOptions (existing fields unchanged)
Round    int    // > 0: ask the builder that built this closed round; implies Headless; Role/Candidate are ignored (the session fixes both)
Question string // inline question; exactly one of File / Question when Round > 0 (File alone as today otherwise)
// The consult record: Role "round", Endpoint{Kind: session.Kind, Mode: ModeHeadless, ...}; AskPath/FindingsPath as any consult of the CURRENT round number (b.Round), so the files sort with the round that asked.
```

## 4. Interfaces

```
// internal/harness/resume.go
var ErrResumeUnsupported = errors.New("resume is not supported for this harness")
func (h Harness) Resume(sessionID, prompt string, tier Tier) ([]string, error)
    // returns the full argv AFTER the binary (the caller prepends h.Binary), with the prompt already in place:
    //   claude:   ["-p", prompt, "--resume", sessionID, "--output-format", "stream-json", "--verbose"] + PermissionArgs(tier)
    //   agy:      ["-p", prompt, "--conversation", sessionID, "--output-format", "stream-json"] + PermissionArgs(tier)
    //   opencode: ["run", prompt, "--session", sessionID, "--fork", "--format", "json"] + PermissionArgs(tier)   (tier read -> ErrTierUnsupported as today; callers pass TierHarness for opencode)
    //   codex / unknown: ErrResumeUnsupported
    // sessionID must be non-empty and free of whitespace and leading '-' -> error otherwise

// internal/relay/session.go
func roundSession(entries []store.LogEntry, round int) (*store.BuilderSession, bool)
    // the newest KindReport entry for that round with BuilderSession != nil; (nil, false) when the round has no report entry or no session

// internal/relay/ask.go  Ask, when opts.Round > 0:
//   body: from opts.File or opts.Question (exactly one; both/neither -> error "ask --round needs --file or -q")
//   rt.Runner == nil -> ErrRunnerUnavailable
//   b := Load; b.Builder.Remote() -> "consults are local-only" (as today); opts.Round >= b.Round -> fmt.Errorf("round %d is the open round; talk to the live builder or wait for it to close", opts.Round)
//   entries := ReadLog; s, ok := roundSession(entries, opts.Round); !ok -> fmt.Errorf("round %d of %q recorded no builder session (built before relay recorded sessions, or the harness printed none); nothing to resume", opts.Round, name)
//   h := harness.Lookup(s.Kind); tier := TierRead for claude/agy, TierHarness for opencode; argv, err := h.Resume(s.ID, prompt, tier) -> ErrResumeUnsupported/ErrTierUnsupported surface with the kind named
//   prompt := OriginLine(name, opts.Round, DirToConsult, KindAsk) is not defined for DirToConsult (origin.go) -- use the literal
//            fmt.Sprintf("relay: consult · to builder of round %d · about binding %q (not the human)\n\nYou built round %d of this binding. Answer from what you did and why; do not change anything, do not run tools that write.\n\nRead: %s\n\nAnswer as your final message, complete, in markdown; relay records it.", round, name, round, askPath)
//   reserve (phase 1) exactly as the headless branch does, with Role "round" and Endpoint.Kind = s.Kind; NO resolveCandidate, NO RoleByName (Role "round" is a record label; make sure the phase-1 code does not require a harness.RoleSpec for it -- if it does, pass a minimal spec or halt and report)
//   spawn: rt.Runner.Start(ProcSpec{Dir: b.CWD, Argv: append([]string{h.Binary}, argv...), LogPath: ConsultLogPath, StreamPath: ConsultStreamPath})
//   record (phase 3) as the headless branch: pick entry replaced by a KindAsk entry whose Note is fmt.Sprintf("round %d session %s:%s", opts.Round, s.Kind, short8(s.ID))
//   the existing reconcileConsults headless branch delivers the findings; nothing new there

// cmd/relay cmdAsk: --round int; -q/--question string; when --round > 0: --role not required (ignored with a stderr note if given), exactly one of --file / -q; output "asked round %d's builder (%s session %s) on %s (pid %d)\nfindings will appear at: %s"
```

## 5. Pseudocode

Covered by §4. The round consult is the headless consult with three
substitutions: the argv (resume form), the role label, the prompt.

## 6. Error handling

- Every refusal happens before any reservation: open round, no session,
  unsupported harness/tier, no runner, missing question.
- A pruned/missing session on the harness side surfaces as the process
  exiting without a final message -> the existing silent consult with the
  exit code and stream path in the note (the harness's own error is in the
  stream).

## 7. Ordered implementation steps

Commit prefix: exactly **one** `feat(ask):` commit for the code (squash
step commits, or `chore:`/`test:` per step); the plan copy may be its own
`chore(plans):` commit.

### Task 1 -- harness Resume

**Files:** `internal/harness/resume.go`, `resume_test.go`.

**Test first:** `TestResumePerKind` table: claude/agy/opencode exact argv with tier read (claude `--permission-mode plan`, agy `--mode plan`) and opencode with `TierHarness`; opencode with `TierRead` -> `ErrTierUnsupported`; codex -> `ErrResumeUnsupported`; empty id and `-x` id -> error. **Verify:** `go test -count=1 ./internal/harness/`.

### Task 2 -- Ask --round

**Files:** `internal/relay/ask.go`, `session.go`, `ask_test.go`.

**Tests first** (`seedForAsk` fixture + `fakeRunner`; seed a closed round 1 by appending a `KindReport` entry with `BuilderSession{Kind: "claude", ID: "sess-1"}` and setting `b.Round = 2`):
- `TestAskRoundResumesTheSession`: `Ask{Round: 1, Question: "why X?"}` -> one `fr.specs` with `Argv` containing `--resume`, `sess-1` and `--permission-mode plan`; the prompt argument contains `You built round 1` and the ask path; the ask file contains `why X?`; consult `Role == "round"`, `Endpoint.Kind == "claude"`, headless; `KindAsk` note contains `round 1 session claude:sess-1`. **Mutation check:** launch the plain headless print form instead of `Resume` and this fails on `--resume`.
- `TestAskRoundRefusesOpenRound`: `Round: 2` -> error contains `open round`; nothing reserved.
- `TestAskRoundRefusesNoSession`: report entry without `BuilderSession` -> error contains `recorded no builder session`.
- `TestAskRoundRefusesCodex`: session kind codex -> `ErrResumeUnsupported` (wrapped, names codex).
- `TestAskRoundOpencodeForksOnHarnessTier`: session kind opencode -> argv has `--fork`, no `--auto`, no permission flag.
- `TestAskRoundNeedsExactlyOneQuestion`: neither / both -> error.
- Findings delivery: reuse `TestHeadlessConsultFinalMessageBecomesFindings`'s steps on the round consult (stream with a final text, exit 0) -> `KindFindings` queued. No new reconcile code should be needed; if it is, halt and report.

**Then** the code. **Verify:** `go test -race -count=1 ./internal/relay/`.

### Task 3 -- CLI, README, plan copy, gate

`cmdAsk` flags and validation per §4; `TestAskRoundNeedsAQuestion` (`run([]string{"ask", "--round", "1", "x"})` -> error mentions `--file or -q`; fails before `newRuntime`). README subsection per §2 (include the mutation note for claude/agy and that opencode's `--fork` leaves the original untouched).

Copy the plan file to `docs/plans/2026-09-21-w2k2b-ask-round.md` and commit.

Gate, in the foreground, stop at the first failure:
```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/relay/ ./internal/harness/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Final commit message: `feat(ask): --round N resumes the session that built a closed round and asks it, headless and read-only; the answer is a consult (#147)`

## Report

Per task: what was done, test names, verify output, the mutation check's
failing test. Commit shas (one `feat:`). If any step was impossible as
written -- in particular if phase-1 reservation needs a RoleSpec for
"round" -- say which and stop there.
