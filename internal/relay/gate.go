package relay

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

// gateTailLines is how much of the gate's log a failing result carries in
// the payload: enough to see why it failed, not enough to flood it (#132).
const gateTailLines = 5

// gateStep advances the gate for a round whose marker exists. Pure with
// respect to the store: it touches only rt.Runner, the clock and files under
// the binding dir. Returns the updated binding, and either done == false
// (the gate is running; caller returns gating) or done == true with the
// record to attach to the report.
//
//	b.Gate == ""                         -> done, rec == nil          (no gate; zero cost)
//	rt.Runner == nil                     -> done, rec{Result:"error", Note:"no runner"}
//	GateRun == nil                       -> Start; on error rec{Result:"error", Note: err}; else GateRun set, KindGate entry appended, done == false
//	GateRun != nil, Alive                -> if now - StartedAt >= timeout: Kill, rec{Result:"timeout"}; else done == false
//	GateRun != nil, exited               -> code, ok := ExitCode(stream); !ok -> rec{Result:"error", Note:"no exit trailer"}; code == 0 -> "pass"; else "fail"
//
// On done, GateRun is cleared on the returned binding.
func gateStep(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, bool, *store.GateRecord, error) {
	if b.Gate == "" {
		return b, true, nil, nil
	}

	log := rt.Store.GateLogPath(b.Name, b.Round)

	if rt.Runner == nil {
		return b, true, &store.GateRecord{Command: b.Gate, Result: "error", Note: "no runner", LogPath: log}, nil
	}

	if b.GateRun == nil {
		h, err := rt.Runner.Start(ctx, ProcSpec{
			Dir:        b.CWD,
			Argv:       []string{"sh", "-c", b.Gate + " 2>&1"},
			LogPath:    log,
			StreamPath: log,
			Scope:      scopeFor(rt, scopeGate, scopeUnitNameFor(scopeGate, b.Owner, b.Name, b.Round, ""), cpuPinText(b)),
		})
		if err != nil {
			return b, true, &store.GateRecord{Command: b.Gate, Result: "error", Note: err.Error(), LogPath: log}, nil
		}
		b.GateRun = &store.GateRun{PID: h.PID, StartedAt: h.StartedAt.Unix(), Round: b.Round, Command: b.Gate}
		if err := tx.AppendLog(b.Name, store.LogEntry{
			TS:        rt.Now().UTC(),
			Round:     b.Round,
			Direction: store.DirToPlanner,
			Kind:      store.KindGate,
			Path:      log,
			Note:      "gate started: " + b.Gate,
			Confirmed: true,
		}); err != nil {
			return b, false, nil, err
		}
		return b, false, nil, nil
	}

	h := ProcHandle{PID: b.GateRun.PID, StartedAt: time.Unix(b.GateRun.StartedAt, 0)}
	alive, err := rt.Runner.Alive(ctx, h)
	if err != nil {
		// An OS hiccup is not evidence the gate stopped: treat it as alive
		// this tick, the same rule the headless path applies (spec §6).
		slog.Warn("gate liveness check failed; treating as alive", "binding", b.Name, "pid", b.GateRun.PID, "err", err)
		alive = true
	}
	elapsed := rt.Now().Sub(time.Unix(b.GateRun.StartedAt, 0))

	if alive {
		if elapsed >= gateTimeoutFor(b, rt.Policy) {
			if err := rt.Runner.Kill(ctx, h); err != nil {
				slog.Warn("gate timeout kill failed", "binding", b.Name, "pid", b.GateRun.PID, "err", err)
			}
			rec := &store.GateRecord{Command: b.GateRun.Command, Result: "timeout", DurationMS: elapsed.Milliseconds(), LogPath: log}
			b.GateRun = nil
			return b, true, rec, nil
		}
		return b, false, nil, nil
	}

	code, ok := rt.Runner.ExitCode(ctx, h, log)
	rec := &store.GateRecord{Command: b.GateRun.Command, LogPath: log, DurationMS: elapsed.Milliseconds()}
	switch {
	case !ok:
		rec.Result = "error"
		rec.Note = "no exit trailer"
	case code == 0:
		rec.Result = "pass"
		rec.ExitCode = 0
	default:
		rec.Result = "fail"
		rec.ExitCode = code
	}
	b.GateRun = nil
	return b, true, rec, nil
}

// gateTimeoutFor is the timeout that bounds one gate run: the binding's own
// override when set, else the policy default.
func gateTimeoutFor(b store.Binding, pol policy.Policy) time.Duration {
	if b.GateTimeoutMS > 0 {
		return time.Duration(b.GateTimeoutMS) * time.Millisecond
	}
	return pol.GateTimeout()
}

// gateLine is the payload line describing a gate's result. pass:
//
//	"Gate: make check -- PASS (exit 0, 1m40s). Output: <log>"
//
// fail adds the tail:
//
//	"Gate: make check -- FAIL (exit 2, 1m40s). Output: <log>\n  <last 5 non-empty lines of the log, each indented two spaces>"
//
// timeout: "Gate: make check -- TIMEOUT after 10m0s. Output: <log>"
// error:   "Gate: make check -- ERROR: <note>."
func gateLine(rec store.GateRecord, tail []string) string {
	dur := time.Duration(rec.DurationMS) * time.Millisecond
	switch rec.Result {
	case "pass":
		return fmt.Sprintf("Gate: %s -- PASS (exit %d, %s). Output: %s", rec.Command, rec.ExitCode, dur, rec.LogPath)
	case "fail":
		var sb strings.Builder
		fmt.Fprintf(&sb, "Gate: %s -- FAIL (exit %d, %s). Output: %s", rec.Command, rec.ExitCode, dur, rec.LogPath)
		for _, l := range tail {
			sb.WriteString("\n  ")
			sb.WriteString(l)
		}
		return sb.String()
	case "timeout":
		return fmt.Sprintf("Gate: %s -- TIMEOUT after %s. Output: %s", rec.Command, dur, rec.LogPath)
	default: // "error"
		return fmt.Sprintf("Gate: %s -- ERROR: %s.", rec.Command, rec.Note)
	}
}

// tailLines returns the last n non-empty lines of the file at path, or nil
// when it cannot be read; never an error (the log is a convenience). Lines
// carrying the rusage trailer are skipped (#313): any scoped spawn prints one
// before its exit trailer, and it is not gate output.
func tailLines(path string, n int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var nonEmpty []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, RusageTrailerPrefix) {
			continue
		}
		nonEmpty = append(nonEmpty, l)
	}
	if len(nonEmpty) > n {
		nonEmpty = nonEmpty[len(nonEmpty)-n:]
	}
	return nonEmpty
}
