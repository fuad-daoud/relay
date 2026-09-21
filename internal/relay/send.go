package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrPromptLate is returned by promptWithRetry in place of nil when the first
// stall was contradicted by the screen. Every caller treats it as success and
// records/logs late.
var ErrPromptLate = errors.New("prompt landed late")

// lateScanLines is how many lines are read from the visible source when
// confirming a fingerprint.
const lateScanLines = 40

// ErrBuilderBlocked reports that the builder is sitting at a dialog, so a plan
// cannot be submitted until the planner answers it with `relay answer`.
var ErrBuilderBlocked = errors.New("builder is blocked at a dialog; answer it with relay answer")

// ErrBuilderGone reports that a binding's builder could not be located among
// the live agents, so there is nothing to address.
var ErrBuilderGone = errors.New("builder is gone; rebind before sending")

// ErrBuilderNotBlocked is returned when `relay answer` is asked to type into a
// builder that is not at a dialog. herdr's blocked-detection false-positives
// (#55), and relay prints an instruction to answer whenever it fires, so the
// guard has to live where the keystrokes are sent rather than in the prose.
var ErrBuilderNotBlocked = errors.New("builder is not blocked; nothing to answer")

// builderPrompt is the fixed handoff template. It names both paths explicitly
// because alternate-screen output is unrecoverable, so the report must be a
// file rather than something relay reads off the terminal. The marker is the
// builder's own "the tree is final": relay closes the round on it, not on the
// report appearing (spec 2026-09-12-completion-marker §1).
//
// It opens by naming the working tree and a halt rule (#192): a headless
// agy builder has been observed to run its shell somewhere else and execute
// a round against the planner's main checkout instead of its own worktree.
// Telling the builder which tree is its own, and to check with `git status`
// before doing anything else, is relay's second line of defence alongside
// pinning the process's workspace with --add-dir.
const builderPrompt = `Your working tree is: %s
It is the only tree you may touch. Before anything else, run ` + "`git status`" + `
there. If that fails, or reports a different directory or branch than you
expect for this tree, stop: write a report saying so, create the done marker,
and do nothing else.

Read: %s
When you are done, write your report to: %s
Then, as the very last thing you do -- after every edit, test and commit --
create this empty file: %s
End the report with this block as its last lines, filled in honestly:

` + "```relay" + `
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # repo-relative files you changed
commands_run: []        # commands you ran, e.g. ["make check"]
not_done: []            # adjacent work you deliberately left
` + "```" + `
Reply here with only the report path.`

// Target is the herdr target for an endpoint: its pane id, which Reconcile
// keeps current by refreshing every endpoint it locates. AgentName is
// provenance rather than an address, because herdr can forget it across a
// server restart while the pane stays addressable (#20).
func Target(ep store.Endpoint) string {
	return ep.PaneID
}

// SendResult is what one successful Send produced.
type SendResult struct {
	Round int    // the round the plan was filed under
	Drift string // the drift line for stdout, or "" when there is nothing to say
}

// SendOptions is what a send may add to the plan file (#141).
type SendOptions struct {
	Tier      string // "" means the binding's Tier; else a one-round override (headless only)
	AllowYolo bool
	// Regate sets the binding's repair-round budget (#132 part 2); nil leaves
	// it unchanged.
	Regate *int
	// Verify marks the round for a read-only reviewer at round close (#144).
	// nil takes policy.json verify.default, so a plain send honours the
	// planner's configured default and --verify/--no-verify overrides it.
	Verify *bool
}

// preflight is everything Send checks before it takes the state lock and
// writes: the plan bytes, the effective tier, the located pane builder (or the
// headless launch argv), the paths and the composed prompt. sendPreflight
// computes it read-only; Send and SendDryRun both call it, so a dry run can
// never disagree with a real send about the state of the world (#149). A
// failed precondition is an error in Send's exact wording.
type preflight struct {
	b       store.Binding // the binding as loaded (read-only; Send re-loads under the lock)
	body    []byte        // the plan file's bytes
	tier    harness.Tier  // effective tier for this round (opts.Tier parsed, or effectiveTier(b))
	builder herdr.Agent   // pane builders: the located agent
	located bool          // pane builders: FindAgent succeeded
	argv    []string      // headless: headlessLaunch's argv (proves the launch is well-formed); nil for pane/remote
	gate    *ledger.Gate  // advisory: a gate on b.BuilderCandidate (rate-limited or roles_missing), nil when none

	planPath, reportPath, donePath string
	prompt                         string // composePrompt(...) -- computed, never sent

	remoteSHA string // remote: the resolved branch tip, for the dry run's Where
}

// sendPreflight runs Send's read-only preconditions in Send's exact order and
// error wording. It makes no write: Store.Load takes the store lock briefly
// (it always has) but that is a read. CaptureBaseline is deliberately not here:
// it adds git objects. The remote path stops after the checks that need no
// server contact (no WhoAmI, no bundle); Send's remote branch calls sendRemote
// as it always did.
func sendPreflight(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (preflight, error) {
	// Read the caller's file first; it is the one input that does not depend
	// on binding state.
	body, err := os.ReadFile(file)
	if err != nil {
		return preflight{}, fmt.Errorf("read plan %s: %w", file, err)
	}

	var tier harness.Tier
	if opts.Tier != "" {
		t, err := harness.ParseTier(opts.Tier)
		if err != nil {
			return preflight{}, err
		}
		if err := checkTierCap(t, rt.Policy, opts.AllowYolo); err != nil {
			return preflight{}, err
		}
		tier = t
	}

	b, err := rt.Store.Load(name)
	if err != nil {
		return preflight{}, err
	}
	if b.State == store.StatePaused {
		return preflight{}, fmt.Errorf("binding %q is paused; relay bind --resume --name %s first", name, name)
	}
	if tier == "" {
		tier = effectiveTier(b)
	}

	planPath := rt.Store.PlanPath(name, b.Round)
	reportPath := rt.Store.ReportPath(name, b.Round)
	donePath := rt.Store.DonePath(name, b.Round)
	prompt := composePrompt(b, planPath, reportPath, donePath)

	pf := preflight{
		b: b, body: body, tier: tier,
		planPath: planPath, reportPath: reportPath, donePath: donePath,
		prompt: prompt,
	}

	// A remote binding's read-only prefix: the client and transport must be
	// configured and the branch must resolve locally. Nothing here contacts
	// the server, so a dry run of a remote binding is offline and safe.
	if b.Builder.Remote() {
		if rt.Remote == nil {
			return preflight{}, ErrRemoteUnavailable
		}
		if rt.Git == nil {
			return preflight{}, ErrGitRequired
		}
		if rt.Transport == nil {
			return preflight{}, errors.New("no remote transport configured")
		}
		branchRef := b.Branch
		if !strings.HasPrefix(branchRef, "refs/heads/") {
			branchRef = "refs/heads/" + branchRef
		}
		sha, ok, err := rt.Git.RefSHA(ctx, b.Repo, branchRef)
		if err != nil {
			return preflight{}, fmt.Errorf("resolve branch %s: %w", b.Branch, err)
		}
		if !ok {
			return preflight{}, fmt.Errorf("branch %s not found", b.Branch)
		}
		pf.remoteSHA = sha
		return pf, nil
	}

	if opts.Tier != "" && !b.Builder.Headless() {
		return preflight{}, fmt.Errorf("%w: binding %q has a pane builder; its permissions were fixed when the pane was spawned -- re-bind with relay bind --resume --rebind --tier %s, or use a headless binding", ErrTierPaneFixed, name, opts.Tier)
	}
	if b.State == store.StateBroken {
		return preflight{}, fmt.Errorf("binding %q is broken; rebind before sending", name)
	}
	if b.Round > b.RoundCap {
		return preflight{}, fmt.Errorf("binding %q hit its round cap of %d", name, b.RoundCap)
	}

	if b.Builder.Headless() {
		// A headless builder (#99) is a process relay starts per round, so
		// the runner must exist and no previous process may still be alive --
		// and both are checked here, before Send stages anything.
		if rt.Runner == nil {
			return preflight{}, fmt.Errorf("binding %q: %w", name, ErrRunnerUnavailable)
		}
		if b.Builder.PID != 0 {
			alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
			if err != nil {
				return preflight{}, fmt.Errorf("binding %q: check previous process %d: %w", name, b.Builder.PID, err)
			}
			if alive {
				return preflight{}, fmt.Errorf("binding %q (pid %d): %w", name, b.Builder.PID, ErrBuilderBusy)
			}
		}
		ref, err := candidate.ParseRef(b.BuilderCandidate)
		if err != nil {
			return preflight{}, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
		}
		c, err := rt.Candidates.Lookup(ref)
		if err != nil {
			return preflight{}, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
		}
		role, _ := harness.RoleByName("builder")
		argv, err := headlessLaunch(c, role, tier, roundBudget(b), prompt, b.CWD, rt.Store.Dir(b.Name))
		if err != nil {
			return preflight{}, err
		}
		pf.argv = argv
	} else {
		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return preflight{}, fmt.Errorf("list agents: %w", err)
		}
		builder, ok := FindAgent(agents, b.Builder)
		if !ok {
			return preflight{}, fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, b.Builder.PaneID, b.BuilderCandidate, ErrBuilderGone)
		}
		pf.builder = builder
		pf.located = true
	}

	// The gate is advisory only: a gated candidate can still be sent to, it
	// just tells the human the daemon would switch away after the start.
	for _, g := range Gates(rt) {
		if g.Token == b.BuilderCandidate && (g.Kind == ledger.RateLimited || g.Kind == ledger.RolesMissing) {
			gate := g
			pf.gate = &gate
			break
		}
	}

	return pf, nil
}

// Send copies the planner's plan into relay state and hands it to the builder:
// typed into its pane, or -- for a headless binding (#99) -- as the prompt of
// a fresh process started in the binding's tree. It returns a SendResult
// describing the round and any between-rounds drift.
//
// Every precondition that needs no lock lives in sendPreflight, which
// `relay send --dry-run` calls too (#149). The in-lock checks stay: they guard
// against a change between the preflight and the lock.
func Send(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (SendResult, error) {
	pf, err := sendPreflight(ctx, rt, name, file, opts)
	if err != nil {
		return SendResult{}, err
	}

	// A remote binding's preflight stops at the read-only checks; the round
	// itself is still shipped by sendRemote, which contacts the server.
	if pf.b.Builder.Remote() {
		return sendRemote(ctx, rt, pf.b, pf.body, opts.Tier)
	}

	// The baseline snapshot adds git objects, so it stays out of the
	// read-only preflight and is taken here, before the lock.
	baseline, baselineHead := CaptureBaseline(ctx, rt, pf.b)
	hintRound := pf.b.Round
	builder := pf.builder
	locatedBuilder := pf.located

	var round int
	var driftLineOut string

	// The whole round advance is one critical section: the daemon rewrites this
	// same binding on every tick, and a lost update here would re-send a plan
	// the builder already has. `Prompt` does not wait on the agent, so holding
	// the lock across it costs milliseconds, not the length of a turn.
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		if b.State == store.StateBroken {
			return fmt.Errorf("binding %q is broken; rebind before sending", name)
		}
		if b.State == store.StatePaused {
			return fmt.Errorf("binding %q is paused; relay bind --resume --name %s first", name, name)
		}
		if b.Round > b.RoundCap {
			return fmt.Errorf("binding %q hit its round cap of %d", name, b.RoundCap)
		}
		// The pre-lock load and this locked load are two separate acquisitions
		// of the state lock, so a binding can appear between them. An unlocated
		// builder must never fall through to an empty target.
		if b.Builder.Headless() {
			// One process per round (headless spec §5.2): a previous round's
			// process still running means the human is early, not that
			// relay should start a second builder in the same tree.
			if b.Builder.PID != 0 {
				if rt.Runner == nil {
					return fmt.Errorf("binding %q: %w", name, ErrRunnerUnavailable)
				}
				alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
				if err != nil {
					return fmt.Errorf("binding %q: check previous process %d: %w", name, b.Builder.PID, err)
				}
				if alive {
					return fmt.Errorf("binding %q (pid %d): %w", name, b.Builder.PID, ErrBuilderBusy)
				}
			}
		} else {
			if !locatedBuilder {
				return fmt.Errorf("binding %q: %w", name, ErrBuilderGone)
			}
			if !SameAgent(builder, b.Builder) {
				return fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, b.Builder.PaneID, b.BuilderCandidate, ErrBuilderGone)
			}
		}

		if opts.Tier != "" {
			if !b.Builder.Headless() {
				return fmt.Errorf("%w: binding %q has a pane builder; its permissions were fixed when the pane was spawned -- re-bind with relay bind --resume --rebind --tier %s, or use a headless binding", ErrTierPaneFixed, name, opts.Tier)
			}
			b.RoundTier = opts.Tier
		}

		planPath := rt.Store.PlanPath(name, b.Round)
		reportPath := rt.Store.ReportPath(name, b.Round)
		donePath := rt.Store.DonePath(name, b.Round)
		if err := os.WriteFile(planPath, pf.body, 0o644); err != nil {
			return fmt.Errorf("stage plan at %s: %w", planPath, err)
		}

		text := composePrompt(b, planPath, reportPath, donePath)

		late := false
		if b.Builder.Headless() {
			started, err := startRound(ctx, rt, b, text)
			if err != nil {
				if errors.Is(err, harness.ErrTierUnsupported) || errors.Is(err, harness.ErrExtraArgsPermission) {
					_ = os.Remove(planPath)
					return err
				}
				// The plan is staged and the round is open; nothing was
				// started. NEEDS YOU says so in status, and the ledger's
				// spawn_failed (written by startRound) gates the candidate
				// for the next pick, as a pane spawn failure would.
				b.State = store.StateNeedsYou
				b.Halt = "builder spawn failed: " + err.Error()
				b.HaltAt = rt.Now().UTC()
				if saveErr := tx.Save(b); saveErr != nil {
					return fmt.Errorf("%v; and saving NEEDS YOU failed: %w", err, saveErr)
				}
				return err
			}
			b = started
		} else {
			if builder.Status == herdr.StatusUnknown {
				patterns := dialogPatterns(rt, builder.Kind, b.BuilderCandidate)
				if dialogGuard(ctx, rt, builder.PaneID, patterns) {
					return fmt.Errorf("binding %q: %w", name, ErrBuilderBlocked)
				}
			}

			if err := promptWithRetry(ctx, rt, builder.PaneID, text, planPath); err != nil {
				if errors.Is(err, ErrPromptLate) {
					late = true
				} else if errors.Is(err, herdr.ErrAgentBlocked) {
					return fmt.Errorf("binding %q: %w", name, ErrBuilderBlocked)
				} else {
					return fmt.Errorf("prompt builder: %w", err)
				}
			}
		}

		// armSessionCursor cuts the pane builder's round log at the record's
		// current size, the way startRound cuts a headless one's (#184). The
		// guard restates what the branch above already implies: headless has
		// startRound, remote has no local record to render.
		if !b.Builder.Headless() && !b.Builder.Remote() {
			b = armSessionCursor(rt, b)
		}

		entry := store.LogEntry{
			TS: rt.Now().UTC(), Round: b.Round,
			Direction: store.DirToBuilder, Kind: store.KindPlan,
			Path: planPath, Confirmed: true, Late: late,
			Tier: string(effectiveTier(b)),
		}
		if err := tx.AppendLog(name, entry); err != nil {
			return err
		}

		driftLine := ""
		if b.Round == hintRound {
			res := CaptureDrift(ctx, rt, b, baseline)
			if (res.Available && !res.Stat.Empty()) || res.Reason != "" {
				driftEntry := store.LogEntry{
					TS: rt.Now().UTC(), Round: b.Round,
					Direction: store.DirToPlanner, Kind: store.KindDrift,
					Path: res.Path, Note: DriftSummary(res),
					Confirmed: true,
				}
				if err := tx.AppendLog(name, driftEntry); err != nil {
					return err
				}
				driftLine = DriftLine(res, b.Round)
			}
		}

		round = b.Round
		driftLineOut = driftLine
		b.RoundBaselineTree = baseline
		b.RoundBaselineHead = baselineHead
		b.RoundClosedTree = ""
		b.RoundStartedAt = rt.Now().UTC()
		b.FinishPending = true
		b.State = store.StateActive
		b.Halt = ""
		b.HaltAt = time.Time{}
		// A human re-send is a fresh attempt: the next halt in this round
		// notifies again, and the round gets a full switch budget.
		b.HaltNotifiedRound = 0
		b.RoundSwitches = 0
		b.RoundExcluded = nil
		// A fresh send is a fresh process: any stall stamp from the previous
		// round is gone (#252), and so is the whole progress clock -- the
		// tree, the output and the stale stamp all describe the round that
		// just ended (#135).
		b.StalledSince = time.Time{}
		// A new round supersedes any stop requested for the old one (#138):
		// the builder is being asked to work again, not to wrap up.
		b.StopRequestedAt = time.Time{}
		b.StopGraceMS = 0
		b.Progress = nil
		b.ExploringSince = time.Time{}
		b.StaleSince = time.Time{}
		b.StaleNotifiedAt = time.Time{}
		// A human send is a fresh attempt, so the repair bookkeeping from the
		// old rounds says nothing about this one (#132 part 2): the budget
		// starts unspent and no previous failure is held against the builder.
		b.RepairCount = 0
		b.LastGateSig = ""
		if opts.Regate != nil {
			b.Regate = *opts.Regate
		}

		// Whether this round gets a reviewer at its close (#144): the flag,
		// else policy.json verify.default. Persisted with the round, and
		// cleared by queueReport once the close has acted on it.
		if opts.Verify != nil {
			b.RoundVerify = *opts.Verify
		} else {
			b.RoundVerify = rt.Policy.VerifyDefault()
		}

		return tx.Save(b)
	})
	if err != nil {
		return SendResult{}, err
	}

	return SendResult{Round: round, Drift: driftLineOut}, nil
}

// DryRun is what SendDryRun found: the round Send would open, the builder it
// would go to, the paths and the head of the prompt. It is a description only;
// nothing was written (#149).
type DryRun struct {
	Name       string   `json:"name"`
	Round      int      `json:"round"`
	Mode       string   `json:"mode"` // "pane" | "headless" | "remote"
	Candidate  string   `json:"candidate"`
	Where      string   `json:"where"`               // pane: "pane w2:p4 (working)"; headless: the harness binary + first arg; remote: "server contabo, branch relay/x @ <sha12>; server not contacted"
	GateNote   string   `json:"gate_note,omitempty"` // "rate-limited until 00:26; the daemon would switch after start" / "roles missing: ...; the daemon would switch after start"
	PlanPath   string   `json:"plan_path"`
	PlanFrom   string   `json:"plan_from"`
	PlanBytes  int64    `json:"plan_bytes"`
	ReportPath string   `json:"report_path"`
	DonePath   string   `json:"done_path"`
	Tier       string   `json:"tier"`
	PromptHead []string `json:"prompt_head"` // the prompt's first two non-empty lines
}

// SendDryRun checks every precondition Send checks and describes the round
// Send would open, without making a single write: no staged plan, no log
// entry, no Save, no Prompt, no Runner.Start, no baseline snapshot (#149). A
// failed precondition is the identical error Send would return for the same
// state, so a script can rely on the dry run as a gate.
func SendDryRun(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (DryRun, error) {
	pf, err := sendPreflight(ctx, rt, name, file, opts)
	if err != nil {
		return DryRun{}, err
	}

	d := DryRun{
		Name:       pf.b.Name,
		Round:      pf.b.Round,
		Mode:       dryRunMode(pf.b),
		Candidate:  pf.b.BuilderCandidate,
		Where:      dryRunWhere(pf),
		PlanPath:   pf.planPath,
		PlanFrom:   absoluteOr(file),
		PlanBytes:  int64(len(pf.body)),
		ReportPath: pf.reportPath,
		DonePath:   pf.donePath,
		Tier:       string(pf.tier),
		PromptHead: promptHead(pf.prompt),
	}
	if pf.gate != nil {
		d.GateNote = dryRunGateNote(pf.gate)
	}
	return d, nil
}

// dryRunMode names the builder's shape as the dry run prints it.
func dryRunMode(b store.Binding) string {
	switch {
	case b.Builder.Remote():
		return "remote"
	case b.Builder.Headless():
		return "headless"
	default:
		return "pane"
	}
}

// dryRunWhere is where the round would go: a located pane, the headless argv
// that proves the launch is well-formed, or the remote server and the branch
// the plan would be shipped from.
func dryRunWhere(pf preflight) string {
	switch {
	case pf.b.Builder.Remote():
		sha := pf.remoteSHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		return fmt.Sprintf("server %s, branch %s @ %s; server not contacted", pf.b.Builder.Server, pf.b.Branch, sha)
	case pf.b.Builder.Headless():
		if len(pf.argv) == 0 {
			return ""
		}
		if len(pf.argv) == 1 {
			return pf.argv[0]
		}
		return pf.argv[0] + " " + pf.argv[1]
	default:
		return fmt.Sprintf("pane %s (%s)", pf.builder.PaneID, pf.builder.Status)
	}
}

// dryRunGateNote is the advisory sentence for a gated candidate: what the gate
// is, and that the daemon would switch the builder once the round started.
func dryRunGateNote(g *ledger.Gate) string {
	if g.Kind == ledger.RolesMissing {
		return g.Note + "; the daemon would switch after start"
	}
	return GateKindText(g.Kind) + " " + GateUntilText(g.Until) + "; the daemon would switch after start"
}

// promptHead is the prompt's first two non-empty lines: enough for a human to
// recognise the handoff without printing the whole template.
func promptHead(prompt string) []string {
	var out []string
	for _, line := range strings.Split(prompt, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// absoluteOr resolves path against the current directory when it can, so a dry
// run can report where the plan came from even when the caller typed a
// relative path.
func absoluteOr(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// promptWithRetry retries once past herdr's five second stall detection, then
// gives up. It never fires a third time: a double-submitted plan means two
// builders' worth of edits, which is worse than a stalled round.
func promptWithRetry(ctx context.Context, rt Runtime, target, text, fingerprint string) error {
	err := rt.Herdr.Prompt(ctx, target, text)
	if !errors.Is(err, herdr.ErrPromptStalled) {
		return err
	}

	if fingerprint != "" {
		screen, rerr := rt.Herdr.ReadAgentSource(ctx, target, "visible", lateScanLines)
		if rerr != nil {
			slog.Warn("late check: screen unreadable", "target", target, "err", rerr)
		} else if strings.Contains(screen, fingerprint) {
			return ErrPromptLate
		}
	}

	if retryErr := rt.Herdr.Prompt(ctx, target, text); retryErr != nil {
		// Only a second stall is a stall. The retry can fail for an unrelated
		// reason -- the builder became blocked between the two attempts, say --
		// and reporting that as a stall sends the human looking at the wrong
		// thing.
		if errors.Is(retryErr, herdr.ErrPromptStalled) {
			return fmt.Errorf("prompt %s stalled twice: %w", target, retryErr)
		}
		return fmt.Errorf("prompt %s failed on retry: %w", target, retryErr)
	}

	return nil
}

// composePrompt renders the builder prompt for this round. Line 1 is the
// origin line naming the round and builder, followed by a blank line and the
// handoff text (#139).
func composePrompt(b store.Binding, planPath, reportPath, donePath string) string {
	origin := OriginLine(b.Name, b.Round, store.DirToBuilder, store.KindPlan)
	body := fmt.Sprintf(builderPrompt, b.CWD, planPath, reportPath, donePath)
	return origin + "\n\n" + body
}
