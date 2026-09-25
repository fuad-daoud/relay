package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// verifyRole is the consult Role a verify round's reviewer is recorded under
// (#144). It is deliberately not "reviewer": the harness role that runs the
// consult is `reviewer` (`harness.RoleByName`), while this name marks the
// record as the round-close check so `finishConsult` can pick the verdict out
// of its findings. A `relevo ask --role reviewer` consult is a different thing
// and keeps today's behaviour.
const verifyRole = "verify"

// Verdict values parseVerdict returns and store.Verdict.Verdict carries. The
// third is not a judgement: the findings carried no readable block, and the
// planner reads the prose.
const (
	verdictAccepted     = "accepted"
	verdictRejected     = "rejected"
	verdictUnstructured = "unstructured"
)

// verifyPrompt is the question relevo composes for a verify consult. It names
// every artefact the reviewer needs -- the plan, the builder's report, the
// round's diff and the gate log -- and the tier note says why it may run
// tests: it is in a throwaway worktree, not the builder's or the planner's.
const verifyPrompt = `Verify round %d of binding %q independently. You are in a throwaway worktree at the builder's HEAD; you may run tests and read anything; do not edit files.

Plan:   %s      Report: %s
Diff:   %s      Gate:   %s

The Diff line is a git command: run it in this worktree to see the round's change, including edits the builder did not commit.

Acceptance: does the tree do what the plan asked, with evidence you checked yourself?
Code: is the change correct, safe, and maintainable? Do not reject for style.
Answer as your final message: your findings in markdown, ending with exactly this block:

` + "```relevo" + `
verdict: accepted | rejected
reasons: ["..."]
` + "```"

// verifyDiffCommand renders the git diff command handed to a verify reviewer.
// Both baselineTree and closedTree are tree ids from the round's snapshots;
// the objects live in the repository's shared object store, so the command runs
// in the verify worktree, and it shows uncommitted edits the builder left.
func verifyDiffCommand(baselineTree, closedTree string) string {
	if baselineTree == "" || closedTree == "" {
		return "none"
	}
	return "git diff " + baselineTree + " " + closedTree
}

// verifyQuestion renders the reviewer's question. gateLog "" reads "none":
// a round with no gate has no log to name, and an empty field would read as a
// path the reviewer should have been given.
func verifyQuestion(name string, round int, planPath, reportPath, diff, gateLog string) string {
	if gateLog == "" {
		gateLog = "none"
	}
	return fmt.Sprintf(verifyPrompt, round, name, planPath, reportPath, diff, gateLog)
}

// parseVerdict decodes the verdict and reasons from a verify consult's
// findings (#144): the LAST ```relevo block, whose `verdict: accepted|rejected`
// and `reasons:` the reviewer was asked for. Anything else -- no block, an
// unreadable one, a verdict that is neither word -- is "unstructured", and the
// reasons parsed from a block that had any.
//
// Pure; no I/O; every failure returns the unstructured verdict rather than an
// error, so a consult that answered in prose is delivered as prose.
func parseVerdict(findings []byte) (verdict string, reasons []string) {
	if len(findings) == 0 {
		return verdictUnstructured, nil
	}

	// The same fence finder the report tail uses (#133), so a block is a
	// block in both places.
	lines := splitFenceLines(findings)
	openIdx, closeIdx, _ := findRelevoBlock(lines)
	if openIdx == -1 || closeIdx == -1 {
		return verdictUnstructured, nil
	}

	var (
		verdictRaw string
		openList   bool
	)
	for i := openIdx + 1; i < closeIdx; i++ {
		line := strings.TrimSpace(stripComment(lines[i]))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "-") && (len(line) == 1 || line[1] == ' ' || line[1] == '\t') {
			if openList {
				if item := strings.TrimSpace(unquoteScalar(strings.TrimSpace(line[1:]))); item != "" {
					reasons = append(reasons, item)
				}
			}
			continue
		}
		colon := strings.Index(line, ":")
		if colon == -1 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		openList = false
		switch key {
		case "verdict":
			verdictRaw = val
		case "reasons":
			if val == "" {
				// A YAML-ish list: the "- ..." lines that follow.
				openList = true
			} else {
				reasons = parseListValue(val)
			}
		}
	}

	switch strings.ToLower(strings.TrimSpace(unquoteScalar(verdictRaw))) {
	case verdictAccepted:
		return verdictAccepted, reasons
	case verdictRejected:
		return verdictRejected, reasons
	default:
		return verdictUnstructured, reasons
	}
}

// verifyTier is the reviewer's permission tier (#144): in file mode, the
// reviewer row's tier when roles.json set one, else yolo; otherwise
// policy.json tier.reviewer when set, else the candidate's own tier, else
// yolo.
//
// yolo is deliberate and it is why verify is not an ordinary `relevo ask`:
// the consult runs in a throwaway worktree at the builder's HEAD that relevo
// created for it and removes afterwards, so letting it execute tests there
// costs nothing the builder or the planner owns. A review that cannot run
// anything is not the review this feature promises. The role definition
// still tells it not to edit; relevo cannot observe writes.
//
// It is deliberately not capped by max_tier: this consult only, in this
// tree only.
func verifyTier(rt Runtime, c candidate.Candidate) harness.Tier {
	reg := rt.RoleRegistry()
	if reg.FileMode() {
		// roles.json is the only place a file-mode role's tier comes from,
		// so the candidate's own tier is ignored here (#374 §4.6).
		if t, ok := reg.TierFor("reviewer", c); ok {
			return t
		}
		return harness.TierYolo
	}
	if t, ok := rt.Policy.TierFor("reviewer"); ok {
		return t
	}
	if c.Tier != "" {
		if t, err := harness.ParseTier(c.Tier); err == nil {
			return t
		}
	}
	return harness.TierYolo
}

// removeVerifyWorktree takes the throwaway tree away. Failure is a warning,
// never fatal: the round has already closed, and a leftover tree under
// .worktrees/.verify/ is the human's to `git worktree remove` (README).
func removeVerifyWorktree(ctx context.Context, rt Runtime, b store.Binding, round int) {
	if rt.Git == nil {
		return
	}
	if err := rt.Git.RemoveWorktree(ctx, b.CWD, rt.Store.VerifyWorktreePath(b.Name, round), true); err != nil {
		slog.Warn("verify worktree not removed", "binding", b.Name, "round", round, "err", err)
		return
	}
	rt.Store.PruneWorktreeDirs()
}

// startVerifyConsult starts this round's read-only reviewer at round close
// (#144).
//
// Preconditions:  the round just closed (b.Round == round+1); the caller
// holds the state lock and passes its tx; gateLog is the closed round's gate
// log path, "" when no gate ran.
//
// Postconditions: on success one headless consult with Role verifyRole is
// reserved and running in a detached worktree at the builder's HEAD, its
// question staged at the binding's ask path, and a to_consult/ask entry
// (note "verify <id>") in the log. On every failure -- no git client, no
// reviewer candidate, a worktree that could not be created, no runner, a
// launch that failed -- the worktree is removed when it exists, one
// to_consult/ask note "verify skipped: <why>" is appended, and (b, nil) is
// returned: a reviewer relevo could not start is not a round failure. Only a
// store error can fail the call.
//
// It runs entirely inside the caller's critical section: the round close
// already advanced, and the consult record must be written with it.
func startVerifyConsult(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, round int, diff string, gateLog string) (store.Binding, error) {
	// skip records why the reviewer did not run and keeps the round closed.
	skip := func(reason string) (store.Binding, error) {
		entry := store.LogEntry{
			TS: rt.Now().UTC(), Round: round,
			Direction: store.DirToConsult, Kind: store.KindAsk,
			Note: "verify skipped: " + reason,
		}
		if err := tx.AppendLog(b.Name, entry); err != nil {
			return b, err
		}
		return b, nil
	}
	// fail is skip plus the worktree cleanup every failure after creation
	// owes.
	fail := func(reason string) (store.Binding, error) {
		removeVerifyWorktree(ctx, rt, b, round)
		return skip(reason)
	}

	if rt.Git == nil {
		return skip("no git client")
	}

	// The tree as the builder left it. CWD is the builder's worktree for an
	// add binding, and the tree the round's diff was taken from either way.
	head, err := rt.Git.HeadCommit(ctx, b.CWD)
	if err != nil {
		return skip("head: " + err.Error())
	}

	wt := rt.Store.VerifyWorktreePath(b.Name, round)
	if err := rt.Git.AddDetachedWorktree(ctx, b.CWD, wt, head); err != nil {
		return skip("worktree: " + err.Error())
	}

	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, Gates(rt), "", "reviewer")
	if err != nil {
		return fail(err.Error())
	}
	c := res.Candidate

	// The harness role that runs the consult is `reviewer`; the record's Role
	// is verifyRole so finishConsult knows to parse a verdict (#144). In file
	// mode the spec comes from roles.json, so a reviewer row whose definition
	// is missing for this kind skips the consult, like any other launch
	// refusal here.
	role, err := rt.RoleRegistry().Spec("reviewer", c.Harness)
	if err != nil {
		return fail(err.Error())
	}

	if rt.Runner == nil {
		return fail("no runner")
	}

	tier := verifyTier(rt, c)
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return fail(fmt.Sprintf("unknown harness kind %q", c.Harness))
	}
	l, err := h.Launch(c.Provider, c.Model, c.ExtraArgs, role, tier)
	if err != nil {
		return fail(err.Error())
	}

	newID := rt.NewID
	if newID == nil {
		newID = randomConsultID
	}
	id := newID()
	askPath := rt.Store.AskPath(b.Name, round, id)
	prompt := verifyQuestion(b.Name, round,
		rt.Store.PlanPath(b.Name, round), rt.Store.ReportPath(b.Name, round),
		diff, gateLog)
	if err := os.WriteFile(askPath, []byte(prompt), 0o644); err != nil {
		return fail(fmt.Sprintf("stage question at %s: %v", askPath, err))
	}

	consult := store.Consult{
		ID:           id,
		Role:         verifyRole,
		Round:        round,
		AskPath:      askPath,
		FindingsPath: rt.Store.FindingsPath(b.Name, round, id),
		Endpoint:     store.Endpoint{AgentName: b.Name + "-" + verifyRole + "-" + id, Kind: l.Kind},
		State:        store.ConsultSpawning,
		SpawnedAt:    rt.Now().UTC(),
	}

	// Reserve: the spawning record exists before the process does, so a
	// crash between the two leaves something `relevo reap` can see, exactly
	// as the headless ask path does (#147).
	b.Consults = append(b.Consults, consult)
	if err := tx.Save(b); err != nil {
		b.Consults = b.Consults[:len(b.Consults)-1]
		return fail("stage consult: " + brief(err))
	}

	streamPath := rt.Store.ConsultStreamPath(b.Name, round, id)
	argv, err := headlessLaunch(c, role, tier, consultTimeout,
		fmt.Sprintf(consultHeadlessPrompt, askPath), wt, rt.Store.Dir(b.Name))
	if err != nil {
		b.Consults = b.Consults[:len(b.Consults)-1]
		if saveErr := tx.Save(b); saveErr != nil {
			return b, saveErr
		}
		return fail("spawn failed: " + brief(err))
	}

	// The consult's Dir is the throwaway worktree, not b.CWD: the reviewer
	// reads the builder's tree without writing in it.
	handle, err := rt.Runner.Start(ctx, ProcSpec{
		Dir:  wt,
		Argv: argv,
		// stderr shares the stream, as the gate's does: FinalText and usage skip non-JSON lines, and nothing read consult.log (R2).
		LogPath:    streamPath,
		StreamPath: streamPath,
		Scope:      scopeFor(rt, scopeVerify, scopeUnitNameFor(scopeVerify, b.Owner, b.Name, round, id), ""),
	})
	if err != nil {
		b.Consults = b.Consults[:len(b.Consults)-1]
		if saveErr := tx.Save(b); saveErr != nil {
			return b, saveErr
		}
		return fail("spawn failed: " + brief(err))
	}
	// The daemon has now seen the reviewer alive (#370, spec §4.2): a later
	// tick never judges it lost to its own restart.
	rt.Watched.Mark(handle.PID, handle.StartedAt.Unix())

	consult.Endpoint = store.Endpoint{
		AgentName: consult.Endpoint.AgentName,
		Kind:      l.Kind,
		Mode:      store.ModeHeadless,
		PID:       handle.PID,
		StartedAt: handle.StartedAt.Unix(),
		LogPath:   streamPath,
	}
	consult.State = store.ConsultRunning

	// Record: the consult is running (Ask's phase 3), and the ask entry logs
	// it as `verify <id>`.
	b.Consults[len(b.Consults)-1] = consult
	if err := tx.AppendLog(b.Name, store.LogEntry{
		TS: rt.Now().UTC(), Round: round,
		Direction: store.DirToConsult, Kind: store.KindAsk,
		Path:      askPath,
		Note:      verifyRole + " " + id,
		Confirmed: true,
	}); err != nil {
		return b, err
	}
	if err := tx.Save(b); err != nil {
		return b, err
	}

	return b, nil
}
