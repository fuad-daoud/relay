package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/transcript"
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

// scopeUnitName is the per-round systemd scope unit's base name (the
// runner appends ".scope"): "relay-round-<owner8>-<name>-<round>" (#244,
// #216). owner8 is the first 8 hex characters of the owning client's id
// ("local" for a binding with no owner; "unknown" for a non-empty Owner
// that does not parse as a ClientID, which never happens for what the
// server itself wrote, but must never block a round).
func scopeUnitName(b store.Binding) string {
	owner8 := "local"
	if b.Owner != "" {
		owner8 = "unknown"
		if dir, ok := remote.ClientID(b.Owner).Dir(); ok {
			owner8 = dir[:8]
		}
	}
	return "relay-round-" + owner8 + "-" + safeUnitPart(b.Name) + "-" + strconv.Itoa(b.Round)
}

// safeUnitPart replaces every rune outside [A-Za-z0-9:_.-] with '-', for a
// string destined for a systemd unit name.
func safeUnitPart(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ':', r == '_', r == '.', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	return sb.String()
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
// binary, then its print form with the prompt, the budget, the round's
// working tree and the binding's state directory filled in (headless spec
// §4.2, #192, #230). Pure. An unknown kind is an error, not a panic: Load
// validated the set, but a binding written by a future relay could name a
// kind this one does not know.
func headlessLaunch(c candidate.Candidate, role harness.RoleSpec, tier harness.Tier, budget time.Duration, prompt, dir, state string) ([]string, error) {
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return nil, fmt.Errorf("unknown harness kind %q", c.Harness)
	}
	l, err := h.Launch(c.Provider, c.Model, c.ExtraArgs, role, tier)
	if err != nil {
		return nil, err
	}
	if l.PromptAt < 0 {
		return nil, fmt.Errorf("harness %q has no print form", c.Harness)
	}
	return append([]string{h.Binary}, l.PrintArgs(prompt, budget, dir, state)...), nil
}

// startRound starts the round's process for a headless binding and records
// its handle on the endpoint (headless spec §4.3). The caller holds the
// state lock, has staged the plan, and saves what comes back.
//
// Preconditions:  b.Builder.Headless(); no live process on the endpoint
// (Send checks with Runner.Alive first); rt.Runner non-nil.
// Postconditions: on success PID, StartedAt and LogPath describe the new
// process. On failure the endpoint is returned as it was (cursor moved to this
// round), PID 0, and the candidate's spawn_failed is in the ledger -- the same
// record a pane spawn failure leaves, because it is the same failure: the
// candidate could not be launched. The caller decides the binding's state.
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
	argv, err := headlessLaunch(c, role, effectiveTier(b), roundBudget(b), prompt, b.CWD, rt.Store.Dir(b.Name))
	if err != nil {
		return b, err
	}
	if b.Builder.StreamRound != b.Round {
		// A new round is a new stream file; a mid-round switch (same
		// round) keeps rendering the file both processes append to.
		b.Builder.StreamRound, b.Builder.StreamOffset = b.Round, 0
	}
	// A new process announces its own session on its own stream (#147);
	// drainStream fills this in again from the first line it writes.
	b.Builder.StreamSessionID = ""
	logPath := rt.Store.BuilderLogPath(b.Name, b.Round)
	spec := ProcSpec{
		Dir: b.CWD, Argv: argv,
		LogPath:    logPath,
		StreamPath: rt.Store.BuilderStreamPath(b.Name, b.Round),
	}
	if rt.Scope != nil {
		spec.Scope = &ScopeSpec{
			Unit:      scopeUnitName(b),
			Slice:     rt.Scope.Slice,
			CPUWeight: rt.Scope.CPUWeight,
			MemoryMax: rt.Scope.MemoryMax,
			CPUQuota:  rt.Scope.CPUQuota,
			TasksMax:  rt.Scope.TasksMax,
		}
	}
	h, err := rt.Runner.Start(ctx, spec)
	if err != nil {
		recordSpawnFailureLocked(rt, c.Ref().String(), b.Name, err)
		return b, fmt.Errorf("start headless builder for %q (%s): %w", b.Name, c.Ref().String(), err)
	}
	b.Builder.PID = h.PID
	b.Builder.StartedAt = h.StartedAt.Unix()
	b.Builder.LogPath = logPath
	return b, nil
}

// drainStream brings the round's builder log up to date with its stream
// (transcript spec §4.2): every complete line of the stream file past the
// endpoint's cursor is rendered with transcript.Render and appended to the
// log in one write, and the cursor moves past the last newline consumed.
// A trailing partial line waits for the next tick. The cursor is keyed on
// StreamRound, not b.Round, so a round that closed on its marker while the
// builder was still flushing keeps draining until the next round starts.
//
// The first drained line that names the harness's own session records it on
// the endpoint (#147); later lines cannot change it, so a sub-agent's session
// appearing mid-round is ignored. A line that names none is not an error.
//
// It never fails the tick: every problem is a slog.Warn and an unchanged
// binding, and the cursor advances only after the append succeeded, so a
// failed write renders the same lines again next tick rather than dropping
// them. Nothing here is read for meaning (headless spec §1).
func drainStream(rt Runtime, b store.Binding) store.Binding {
	round := b.Builder.StreamRound
	if round == 0 {
		return b
	}
	b.Builder.StreamOffset = drainFile(
		rt.Store.BuilderLogPath(b.Name, round),
		rt.Store.BuilderStreamPath(b.Name, round),
		b.Builder.StreamOffset,
		func(line []byte) []string {
			if b.Builder.StreamSessionID == "" {
				if id := transcript.SessionID(b.Builder.Kind, line); id != "" {
					b.Builder.StreamSessionID = id
				}
			}
			return transcript.Render(b.Builder.Kind, line)
		},
		"stream", "binding", b.Name, "round", round,
	)
	return b
}

// drainFile appends render(line) for every complete line of src past off
// to logPath and returns the new offset. It is the shared body of
// drainStream and drainSession (#184): it never fails the tick -- every
// problem is a slog.Warn (with what) and the offset unchanged, except an
// offset past EOF, which resets to 0. what names the source in warnings
// ("stream", "session").
func drainFile(logPath, src string, off int64, render func(line []byte) []string, what string, fields ...any) int64 {
	info, err := os.Stat(src)
	if err != nil {
		return off // not started yet, or gone with the round: nothing to drain
	}
	if off > info.Size() {
		slog.Warn("builder "+what+": cursor past end of "+what+"; rendering from the start",
			append(append([]any{}, fields...), "offset", off, "size", info.Size())...)
		off = 0
	}
	if off == info.Size() {
		return off
	}
	data, err := readFrom(src, off)
	if err != nil {
		slog.Warn("builder "+what, append(append([]any{}, fields...), "err", err)...)
		return off
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return off
	}
	var out []string
	for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
		out = append(out, render(line)...)
	}
	if len(out) > 0 {
		if err := appendLines(logPath, out); err != nil {
			slog.Warn("builder "+what, append(append([]any{}, fields...), "err", err)...)
			return off
		}
	}
	return off + int64(end) + 1
}

// readFrom is the file's bytes from off to its end.
func readFrom(path string, off int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// appendLines appends lines, each newline-terminated, to the file at path
// in one write, the same discipline as appendLogMarker: O_APPEND writes of
// one buffer interleave with the supervisor's stderr at line boundaries.
func appendLines(path string, lines []string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	return err
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
func exitEntry(now time.Time, round int, logPath, codeText, suffix string) store.LogEntry {
	return store.LogEntry{
		TS:        now,
		Round:     round,
		Direction: store.DirToPlanner,
		Kind:      store.KindExit,
		Path:      logPath,
		Note:      fmt.Sprintf("builder exited (code %s) without a report%s", codeText, suffix),
		Payload:   logTail(logPath, logTailLines),
		Confirmed: true,
	}
}

// streamLastActivity is when a live headless builder's stream last moved: the
// later of the stream file's mtime and the round's start (#252). A missing or
// unreadable stream is not an error -- a round that has not produced a line
// yet counts from its start, and a stream that never appears for stall_after_ms
// on a live process is exactly a stall.
func streamLastActivity(rt Runtime, b store.Binding) time.Time {
	st, err := os.Stat(rt.Store.BuilderStreamPath(b.Name, b.Round))
	if err != nil {
		return b.RoundStartedAt
	}
	last := st.ModTime()
	if b.RoundStartedAt.After(last) {
		return b.RoundStartedAt
	}
	return last
}

// reconcileHeadless is one tick of a headless binding (spec §5.1). Reconcile
// hands off here right after the DONE gate; the pane path never runs for a
// headless endpoint and this never runs for a pane one.
//
// Order: halt on the round cap, then decide. A report file finishes the round
// whatever the process did (the report is the contract). Otherwise: a gated
// candidate is switched with its process killed; a live process is left alone,
// its budget the only thing that can halt it; a process that exited is logged
// with its code and log tail and the builder is switched, up to max_switches.
// No round open means idle -- never BROKEN -- and a process lingering with no
// round is a stray relay stops.
func reconcileHeadless(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	if b.Round > b.RoundCap {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: hit the round cap of %d", b.Name, b.RoundCap))
	}

	// Render what the builder has streamed since the last tick before
	// anything below reads the log: the exit entry's tail, the limit scan
	// and the status snippet all see a log that is current (transcript
	// spec §4.3).
	b = drainStream(rt, b)

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
			b.StalledSince = time.Time{}
		}
		if b.State == store.StateBroken {
			b.State = store.StateActive
		}
		return deliverAndSettle(ctx, rt, tx, b)
	}

	// A queued round (#285) has no process and no clocks: nothing to
	// reconcile until relay.Admit starts it. This must precede the PID == 0
	// "spawn failed earlier" branch below, or a queued round would be
	// mistaken for a failed spawn.
	if !b.QueuedAt.IsZero() {
		return b, nil
	}

	// The marker is the contract (completion-marker spec §4.4): the process
	// is done with whatever it exits as, and it exits on its own. Never Kill
	// here. A report without a marker is a process still working.
	//
	// The escape check runs before closeOnMarker, not inside it: queueReport
	// clears RoundBaselineTree, and the snapshot must be compared against it
	// while it is still on b (#192).
	markerNote := ""
	if escapeCheck(ctx, rt, b, true) == EscapeNote {
		markerNote = escapeNote
	}
	// queueReport's reset block clears RoundVerify: read the round's verify
	// flag before the close consumes it (#144), as the pane path does.
	wantVerify := b.RoundVerify
	next, closed, gating, rec, err := closeOnMarker(ctx, rt, tx, b, entries, markerNote)
	if err != nil {
		return b, err
	}
	if gating {
		return next, nil
	}
	if closed {
		closedRound := b.Round
		if next.Owner != "" {
			next = closeServedRound(ctx, rt, next)
		}
		next.Builder = clearProcess(next.Builder)
		next.StalledSince = time.Time{}
		// The gate result -> report queued -> verify consult started ->
		// delivery (#144), exactly as the pane path orders it: the reviewer
		// sees the gate's output, so it starts after the gate and before the
		// planner is told.
		if wantVerify {
			gateLogPath := ""
			if rec != nil {
				gateLogPath = rec.LogPath
			}
			next, err = startVerifyConsult(ctx, rt, tx, next, closedRound, gateLogPath)
			if err != nil {
				return next, err
			}
		}
		// Edges evaluate right after the verify hook and before delivery
		// (#37), exactly as the pane path orders it: see reconcile.go's
		// matching comment for why the pendings return value is discarded.
		next, _, err = evaluateEdges(ctx, rt, tx, next, closedRound)
		if err != nil {
			return next, err
		}
		next, err = deliverAndSettle(ctx, rt, tx, next)
		if err != nil {
			return next, err
		}
		// The report is queued; a failing gate may now open round N+1, as a
		// fresh process, exactly as Send would (#132 part 2). The failed
		// round's own report, diff and gate=fail stand.
		if rec != nil && rec.Result == "fail" && next.Regate > 0 && next.State != store.StateNeedsYou {
			next, err = startRepairRound(ctx, rt, tx, next, *rec, closedRound)
			if err != nil {
				return next, err
			}
		}
		return next, nil
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
			return switchBuilder(ctx, rt, tx, b, reason, true, false)
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
		now := rt.Now().UTC()
		next, halted, err := checkRoundTimeout(ctx, rt, tx, b)
		if halted {
			return next, err // a halt does not deliver, as in the pane path
		}
		if err != nil {
			return b, err
		}
		// The process is alive; the progress clock (#135) replaces #252's
		// stream-only rule: the tree and the stream are both signals, and a
		// stall is quiet on both. The read is gated on the interval (#135
		// follow-up): a tick inside it samples nothing.
		if progressDue(next, now, rt.Policy.ProgressInterval()) {
			next = progressStep(rt, next, now, sampleSignals(ctx, rt, next))
		}
		return deliverAndSettle(ctx, rt, tx, next)
	}

	// Exited. The exit code is read once, from the stream's trailer.
	codeText := "unknown"
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), rt.Store.BuilderStreamPath(b.Name, b.Round)); ok {
		codeText = strconv.Itoa(code)
	}

	// Exited after writing a report but without the marker: an exited
	// process cannot be mid-write, so the report is trusted and the
	// omission noted (spec §4.4).
	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if _, err := os.Stat(reportPath); err == nil {
		_, m, _, err := gateOnLimit(ctx, rt, tx, b, logTail(b.Builder.LogPath, limitScanLines), false)
		if err != nil {
			return b, err
		}
		slog.Warn("headless builder exited with a report but no marker", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "code", codeText, "note", "unmarked")
		payload := fmt.Sprintf(
			"Builder exited (code %s) after writing its report but never confirmed completion (no %s). Report: %s.",
			codeText, filepath.Base(rt.Store.DonePath(b.Name, b.Round)), reportPath)
		if m.Line != "" {
			payload += fmt.Sprintf(" Provider rate-limited: %s; gated until %s.", m.Line, m.Until.Local().Format("15:04"))
		}
		note := "unmarked"
		if escapeCheck(ctx, rt, b, true) == EscapeNote {
			note = joinNotes(note, escapeNote)
		}
		next, err := queueReport(ctx, rt, tx, b, entries, reportPath, payload, note, nil, nil, nil)
		if err != nil {
			return b, err
		}
		next.Builder = clearProcess(next.Builder)
		next.StalledSince = time.Time{}
		return deliverAndSettle(ctx, rt, tx, next)
	}

	// Exited without a report.
	now := rt.Now().UTC()
	h, _ := harness.Lookup(b.Builder.Kind)
	var c candidate.Candidate
	if ref, err := candidate.ParseRef(b.BuilderCandidate); err == nil && rt.Candidates != nil {
		if cand, err := rt.Candidates.Lookup(ref); err == nil {
			c = cand
		}
	}
	denialLine, isDenial := matchDenial(logTail(b.Builder.LogPath, limitScanLines), denialPatterns(c, h))
	suffix := ""
	if isDenial {
		suffix = "; permission-blocked: " + denialLine
	}

	if err := tx.AppendLog(b.Name, exitEntry(now, b.Round, b.Builder.LogPath, codeText, suffix)); err != nil {
		return b, err
	}
	slog.Info("headless builder exited without a report", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "code", codeText)

	// A stop requested for a round that then exited without a report (#138):
	// close it without a report and without a switch, because a stop is not a
	// failure. Belt-and-braces only -- Stop on a headless round closes
	// synchronously and never sets the request -- for a process killed
	// between the request and the close.
	if !b.StopRequestedAt.IsZero() {
		return closeStopped(ctx, rt, tx, b, "killed")
	}

	// A cgroup/group kill of the daemon (systemd restart, kill -9 of the
	// process tree) takes the supervisor with it before it can write the
	// relay-exit: trailer, which reads exactly like a real builder death:
	// code unknown. The daemon can tell the two apart because it knows its
	// own start time: a builder whose recorded start predates the daemon's
	// own could not have been killed by anything the builder itself did.
	// Relaunching the same candidate on the same round, uncounted, keeps a
	// daemon restart from spending the round's switch budget (#244).
	lost := codeText == "unknown" && !rt.StartedAt.IsZero() && b.Builder.StartedAt != 0 &&
		time.Unix(b.Builder.StartedAt, 0).Before(rt.StartedAt)

	b.Builder.PID, b.Builder.StartedAt = 0, 0 // LogPath stays: status and the entry point at it
	b.StalledSince = time.Time{}              // the process is gone: not stalled any more

	if lost && switchable {
		if b.Owner != "" {
			// On a server, a box reboot must not relaunch every builder past
			// the cap: re-queue at the head of the queue instead of
			// relaunching (#285, #244). The local daemon keeps the relaunch
			// below.
			b.QueuedAt = b.RoundStartedAt
			b.RoundStartedAt = time.Time{}
			b.Builder = clearProcess(b.Builder)
			if err := tx.AppendLog(b.Name, store.LogEntry{
				TS: now, Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindQueue, Confirmed: true,
				Note: "re-queued (builder lost to a restart)",
			}); err != nil {
				return b, err
			}
			slog.Info("headless builder re-queued after daemon restart", "binding", b.Name, "round", b.Round)
			return b, nil
		}

		text := composePrompt(b, rt.Store.PlanPath(b.Name, b.Round), rt.Store.ReportPath(b.Name, b.Round), rt.Store.DonePath(b.Name, b.Round))
		relaunched, err := startRound(ctx, rt, b, text)
		if err != nil {
			return haltBinding(ctx, rt, b, fmt.Sprintf("%s: builder lost to a daemon restart and could not be relaunched: %v", b.Name, err))
		}
		b = relaunched
		if err := tx.AppendLog(b.Name, store.LogEntry{
			TS: now, Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindSwitch, Confirmed: true,
			Note: fmt.Sprintf("relaunched builder (lost to a daemon restart at %s): picked %s for builder: same candidate, not counted",
				rt.StartedAt.UTC().Format(time.RFC3339), b.BuilderCandidate),
		}); err != nil {
			return b, err
		}
		appendLogMarker(rt.Store.BuilderLogPath(b.Name, b.Round), now, "relaunched "+b.BuilderCandidate+" (lost to a daemon restart)")
		b.RoundStartedAt = now
		b.State = store.StateActive
		slog.Info("headless builder relaunched after daemon restart", "binding", b.Name, "round", b.Round, "candidate", b.BuilderCandidate)
		return b, nil
	}

	// The escape halt comes before gateOnLimit: a limit line in the log of
	// an escaped round must not turn a halt into a switch (#192).
	if escapeCheck(ctx, rt, b, false) == EscapeHalt {
		return haltBinding(ctx, rt, b, escapeDiagnosis(b, codeText))
	}

	next, _, handled, err := gateOnLimit(ctx, rt, tx, b, logTail(b.Builder.LogPath, limitScanLines), false)
	if handled {
		return next, err
	}

	if isDenial {
		return haltBinding(ctx, rt, b, fmt.Sprintf(
			"%s: builder exited (code %s) without a report after a permission denial (%q); not switched -- re-send with a higher tier (relay send --name %s --file <plan> --tier edit|yolo [--allow-yolo]) or extend the harness's allow list; log: %s",
			b.Name, codeText, denialLine, b.Name, b.Builder.LogPath))
	}

	if !switchable {
		return haltBinding(ctx, rt, b, fmt.Sprintf("%s: builder exited (code %s) without a report; see %s", b.Name, codeText, b.Builder.LogPath))
	}
	// The exclusion is appended to the b that switchBuilder receives so the
	// replacement inherits it and the field is persisted with the switch
	// (#191): a headless builder that exited without a report is excluded
	// from the pick for the rest of this round.
	b.RoundExcluded = appendUnique(b.RoundExcluded, b.BuilderCandidate)
	return switchBuilder(ctx, rt, tx, b, fmt.Sprintf("exited (code %s) without a report", codeText), false, true)
}

// appendUnique returns s with v appended, unless it is already present.
func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}

// ErrStopFailed reports that relay marked a binding done or unbound but
// could not stop its headless process (spec §4.6). The state change stands;
// the pid stays on the endpoint (done) or in the result (unbind) so the
// human can find the process.
var ErrStopFailed = errors.New("could not stop the builder process")

// appendLogMarker writes a single relay marker line to a builder's log file
// without ever failing the caller (spec §4.1).
func appendLogMarker(path string, now time.Time, text string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("builder log marker", "path", path, "err", err)
		return
	}
	defer f.Close()
	line := "--- relay " + now.Local().Format("15:04:05") + ": " + text + " ---\n"
	if _, err := f.WriteString(line); err != nil {
		slog.Warn("builder log marker", "path", path, "err", err)
	}
}

// stopProcess kills a headless endpoint's live process, if it has one. It
// returns the pid it addressed -- 0 when there was nothing to stop -- and
// Kill's error. A pane endpoint, or a headless one between rounds, is a
// no-op. Runner nil with a pid recorded is an error: relay cannot say the
// process is stopped.
func stopProcess(ctx context.Context, rt Runtime, e store.Endpoint, why string) (int, error) {
	if !e.Headless() || e.PID == 0 {
		return 0, nil
	}
	if rt.Runner == nil {
		return e.PID, ErrRunnerUnavailable
	}
	if err := rt.Runner.Kill(ctx, handleOf(e)); err != nil {
		return e.PID, err
	}
	appendLogMarker(e.LogPath, rt.Now(), "stopped: "+why)
	return e.PID, nil
}

// statusTailLines is how much of the log `relay status` shows under a
// headless builder line (spec §4.8).
const statusTailLines = 3

// headlessStatus is what `relay status` says about a headless endpoint: the
// status word and the process details. Idle between rounds; otherwise a live
// Alive check, then, for an exited process, the trailer's code. No Runner
// means relay cannot say. A live process whose stream has gone quiet
// (binding.StalledSince, #252) reads "stalled <age>" in place of "working".
func headlessStatus(ctx context.Context, rt Runtime, b store.Binding) (string, *HeadlessInfo) {
	e := b.Builder
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
		// #252's label is now one of #135's progress labels, so a live
		// headless builder can also read "exploring <age>".
		if working, _ := labelsOf(b, rt.Now()); working != "" {
			return working, info
		}
		return "working", info
	}
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(e), e.LogPath); ok {
		info.ExitCode = strconv.Itoa(code)
		return "exited " + info.ExitCode, info
	}
	info.ExitCode = "unknown"
	return "exited", info
}
