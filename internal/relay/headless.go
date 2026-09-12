package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrBuilderBusy reports a send against a headless binding whose previous
// round's process is still running (headless spec §5.2). One process per
// round is the model; two at once in one tree would race each other's
// edits.
var ErrBuilderBusy = errors.New("builder's previous process is still running; wait for its report, or relay done")

// handleOf is the endpoint's stored process fields as the Runner's handle.
// StartedAt is Unix seconds on the endpoint (store spec §3.1, amended).
func handleOf(e store.Endpoint) ProcHandle {
	return ProcHandle{PID: e.PID, StartedAt: time.Unix(e.StartedAt, 0)}
}

// defaultRoundBudget mirrors the store's default round timeout, for a
// binding that was never saved. Store.Save fills RoundTimeoutMS on every
// real binding, so this is a guard, not a policy.
const defaultRoundBudget = 24 * time.Hour

// roundBudget is the binding's round budget as a duration: what agy's
// --print-timeout gets, so the harness's own default (5m) never cuts a
// round short (headless spec §3.5).
func roundBudget(b store.Binding) time.Duration {
	if b.RoundTimeoutMS <= 0 {
		return defaultRoundBudget
	}
	return time.Duration(b.RoundTimeoutMS) * time.Millisecond
}

// headlessLaunch renders the argv for one headless round: the candidate's
// binary, then its print form with the prompt and the budget filled in
// (headless spec §4.2). Pure. An unknown kind is an error, not a panic:
// Load validated the set, but a binding written by a future relay could
// name a kind this one does not know.
func headlessLaunch(c candidate.Candidate, role harness.RoleSpec, budget time.Duration, prompt string) ([]string, error) {
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return nil, fmt.Errorf("unknown harness kind %q", c.Harness)
	}
	l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)
	if l.PromptAt < 0 {
		return nil, fmt.Errorf("harness %q has no print form", c.Harness)
	}
	return append([]string{h.Binary}, l.PrintArgs(prompt, budget)...), nil
}

// startRound starts the round's process for a headless binding and records
// its handle on the endpoint (headless spec §4.3). The caller holds the
// state lock, has staged the plan, and saves what comes back.
//
// Preconditions:  b.Builder.Headless(); no live process on the endpoint
// (Send checks with Runner.Alive first); rt.Runner non-nil.
// Postconditions: on success PID, StartedAt and LogPath describe the new
// process. On failure the endpoint is returned as it was, PID 0, and the
// candidate's spawn_failed is in the ledger -- the same record a pane spawn
// failure leaves, because it is the same failure: the candidate could not
// be launched. The caller decides the binding's state.
func startRound(ctx context.Context, rt Runtime, b store.Binding, prompt string) (store.Binding, error) {
	if rt.Runner == nil {
		return b, ErrRunnerUnavailable
	}
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	role, _ := harness.RoleByName("builder")
	argv, err := headlessLaunch(c, role, roundBudget(b), prompt)
	if err != nil {
		return b, err
	}
	logPath := rt.Store.BuilderLogPath(b.Name, b.Round)
	h, err := rt.Runner.Start(ctx, ProcSpec{Dir: b.CWD, Argv: argv, LogPath: logPath})
	if err != nil {
		recordSpawnFailureLocked(rt, c.Ref().String(), b.Name, err)
		return b, fmt.Errorf("start headless builder for %q (%s): %w", b.Name, c.Ref().String(), err)
	}
	b.Builder.PID = h.PID
	b.Builder.StartedAt = h.StartedAt.Unix()
	b.Builder.LogPath = logPath
	return b, nil
}

// logTailLines is how much of a builder log the exit entry carries: enough
// to see why it died, not enough to flood the round log (spec §3.8).
const logTailLines = 20

// logTail is the last n lines of the file at path, without a trailing
// newline, or "" when the file is absent or empty. It is text for humans --
// the exit entry's payload, the status snippet -- and is never parsed
// (spec §1: relay reads no builder output for meaning).
func logTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil || n <= 0 {
		return ""
	}
	s := strings.TrimRight(string(data), "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// clearProcess is the endpoint between rounds: no pid, no start time, no
// log. Identity -- name, kind, mode -- is untouched.
func clearProcess(e store.Endpoint) store.Endpoint {
	e.PID, e.StartedAt, e.LogPath = 0, 0, ""
	return e
}

// exitEntry is the log record of a headless builder that exited without a
// report (spec §3.8): relay → log only, Confirmed, never a pending payload.
// codeText is the exit code, or "unknown" when the supervisor's trailer is
// missing (killed, or the log unreadable). The payload is the log's last
// logTailLines lines, for the human; relay reads nothing out of it.
func exitEntry(now time.Time, round int, logPath, codeText string) store.LogEntry {
	return store.LogEntry{
		TS:        now,
		Round:     round,
		Direction: store.DirToPlanner,
		Kind:      store.KindExit,
		Path:      logPath,
		Note:      fmt.Sprintf("builder exited (code %s) without a report", codeText),
		Payload:   logTail(logPath, logTailLines),
		Confirmed: true,
	}
}

// reconcileHeadless is one tick of a headless binding (spec §5.1). Reconcile
// hands off here right after the DONE gate; the pane path never runs for a
// headless endpoint and this never runs for a pane one.
//
// Order mirrors the pane path: refresh the planner, halt on the round cap,
// then decide. A report file finishes the round whatever the process did
// (the report is the contract). Otherwise: a gated candidate is switched
// with its process killed; a live process is left alone, its budget the
// only thing that can halt it; a process that exited is logged with its
// code and log tail and the builder is switched, up to max_switches. No
// round open means idle -- never BROKEN -- and a process lingering with no
// round is a stray relay stops.
func reconcileHeadless(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
	if planner, ok := FindAgent(agents, b.Planner); ok {
		b.Planner = refreshEndpoint(b.Planner, planner)
	}
	if b.Round > b.RoundCap {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: hit the round cap of %d", b.Name, b.RoundCap))
	}

	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return b, err
	}
	roundOpen := HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
		!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport)

	if !roundOpen {
		// Idle is normal (spec §5.1): between rounds there is no process.
		// One with no round is a stray -- a send whose round closed some
		// other way -- and relay stops it rather than let it edit a tree
		// nobody is watching.
		if b.Builder.PID != 0 {
			if rt.Runner != nil {
				if err := rt.Runner.Kill(ctx, handleOf(b.Builder)); err != nil {
					slog.Warn("stray headless process not killed", "binding", b.Name, "pid", b.Builder.PID, "err", err)
				} else {
					slog.Warn("killed stray headless process", "binding", b.Name, "pid", b.Builder.PID)
				}
			}
			b.Builder = clearProcess(b.Builder)
		}
		if b.State == store.StateBroken {
			b.State = store.StateActive
		}
		return deliverAndSettle(ctx, rt, tx, b, agents)
	}

	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if _, err := os.Stat(reportPath); err == nil {
		// The report is the contract (spec §1): the process is done with
		// whatever it exits as, and it exits on its own. Never Kill here.
		payload := fmt.Sprintf("Builder finished round %d. Report: %s", b.Round, reportPath)
		next, err := queueReport(ctx, rt, tx, b, entries, reportPath, payload, "")
		if err != nil {
			return b, err
		}
		next.Builder = clearProcess(next.Builder)
		return deliverAndSettle(ctx, rt, tx, next, agents)
	}

	if b.Builder.PID == 0 {
		// Round open, no process, no report: a send whose Start failed.
		// Send already put the binding in NEEDS YOU and the ledger has the
		// spawn_failed; nothing to observe until the human acts.
		return b, nil
	}
	if rt.Runner == nil {
		slog.Warn("headless binding but no Runner configured", "binding", b.Name)
		return b, nil
	}

	switchable := b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()
	if switchable {
		if g, gated := gatedBuilder(rt, b); gated {
			reason := "rate-limited"
			if g.Note != "" {
				reason = "rate-limited: " + g.Note
			}
			return switchBuilder(ctx, rt, tx, b, reason, true)
		}
	}

	alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
	if err != nil {
		// An OS hiccup is not evidence the builder stopped: treat it as
		// alive this tick and say so (spec §6).
		slog.Warn("headless liveness check failed; treating as alive", "binding", b.Name, "pid", b.Builder.PID, "err", err)
		alive = true
	}
	if alive {
		next, halted, err := checkRoundTimeout(ctx, rt, b)
		if halted {
			return next, err // a halt does not deliver, as in the pane path
		}
		if err != nil {
			return b, err
		}
		return deliverAndSettle(ctx, rt, tx, next, agents)
	}

	// Exited without a report.
	codeText := "unknown"
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), b.Builder.LogPath); ok {
		codeText = strconv.Itoa(code)
	}
	now := rt.Now().UTC()
	if err := tx.AppendLog(b.Name, exitEntry(now, b.Round, b.Builder.LogPath, codeText)); err != nil {
		return b, err
	}
	slog.Info("headless builder exited without a report", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "code", codeText)
	b.Builder.PID, b.Builder.StartedAt = 0, 0 // LogPath stays: status and the entry point at it
	if !switchable {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: builder exited (code %s) without a report; see %s", b.Name, codeText, b.Builder.LogPath))
	}
	return switchBuilder(ctx, rt, tx, b, fmt.Sprintf("exited (code %s) without a report", codeText), false)
}

// ErrStopFailed reports that relay marked a binding done or unbound but
// could not stop its headless process (spec §4.6). The state change stands;
// the pid stays on the endpoint (done) or in the result (unbind) so the
// human can find the process.
var ErrStopFailed = errors.New("could not stop the builder process")

// stopProcess kills a headless endpoint's live process, if it has one. It
// returns the pid it addressed -- 0 when there was nothing to stop -- and
// Kill's error. A pane endpoint, or a headless one between rounds, is a
// no-op. Runner nil with a pid recorded is an error: relay cannot say the
// process is stopped.
func stopProcess(ctx context.Context, rt Runtime, e store.Endpoint) (int, error) {
	if !e.Headless() || e.PID == 0 {
		return 0, nil
	}
	if rt.Runner == nil {
		return e.PID, ErrRunnerUnavailable
	}
	if err := rt.Runner.Kill(ctx, handleOf(e)); err != nil {
		return e.PID, err
	}
	return e.PID, nil
}

// statusTailLines is how much of the log `relay status` shows under a
// headless builder line (spec §4.8).
const statusTailLines = 3

// headlessStatus is what `relay status` says about a headless endpoint: the
// status word that sits where a pane's herdr status sits, and the process
// details. Idle between rounds; otherwise a live Alive check -- the same
// cost class as the herdr list pane rows pay -- then, for an exited
// process, the trailer's code. No Runner means relay cannot say.
func headlessStatus(ctx context.Context, rt Runtime, e store.Endpoint) (string, *HeadlessInfo) {
	info := &HeadlessInfo{PID: e.PID, LogPath: e.LogPath}
	if e.StartedAt != 0 {
		info.StartedAt = time.Unix(e.StartedAt, 0)
	}
	if e.LogPath != "" {
		if tail := logTail(e.LogPath, statusTailLines); tail != "" {
			info.Tail = strings.Split(tail, "\n")
		}
	}
	if e.PID == 0 {
		return "idle", info
	}
	if rt.Runner == nil {
		return "unknown", info
	}
	alive, err := rt.Runner.Alive(ctx, handleOf(e))
	if err != nil {
		return "unknown", info
	}
	if alive {
		return "working", info
	}
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(e), e.LogPath); ok {
		info.ExitCode = strconv.Itoa(code)
		return "exited " + info.ExitCode, info
	}
	info.ExitCode = "unknown"
	return "exited", info
}
