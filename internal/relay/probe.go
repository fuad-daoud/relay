package relay

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/latency"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/transcript"
)

const (
	// probeBudget is the harness's own print budget for one latency probe:
	// long enough for a trivial round, short enough that a wedged harness
	// does not hold the listing for a whole round budget (#324 part 1).
	probeBudget = 60 * time.Second

	// probeTimeout bounds the whole probe, harness start included, so a
	// harness that never exits is data ("timeout") rather than a hang.
	probeTimeout = 90 * time.Second

	// probePrompt is the one-line prompt every probe sends: the smallest
	// possible answer, with no tools, so the measurement is time to first
	// model output and not time to do work.
	probePrompt = "relay latency probe: reply with the single word ok. Do not use any tools."
)

// ProbeResult is one probe's outcome. It is latency.Sample itself, so the
// recorded shape and the printed shape can never drift.
type ProbeResult = latency.Sample

// LineExec is the process seam Probe runs a candidate's harness through.
// cmd/relay implements it with os/exec; tests implement it with a script, so
// no test in this package ever spawns a harness or reaches the network.
type LineExec interface {
	// Run starts argv in dir with the parent environment minus proc.DeniedEnv,
	// calls onLine for every stdout line as it arrives (without the newline),
	// and returns when the process exits or ctx is done. A non-zero exit is an
	// error whose text ends with the last 300 bytes of stderr.
	Run(ctx context.Context, dir string, argv []string, onLine func(line []byte)) error
}

// probeTier is the most restrictive tier kind's harness can honour, for a
// probe that must not change anything: read, else edit, else harness (the
// harness's own config decides). Never yolo.
func probeTier(kind string) (harness.Tier, error) {
	h, ok := harness.Lookup(kind)
	if !ok {
		return "", fmt.Errorf("unknown harness %q", kind)
	}

	for _, t := range []harness.Tier{harness.TierRead, harness.TierEdit, harness.TierHarness} {
		if _, err := h.PermissionArgs(t); err == nil {
			return t, nil
		}
	}

	// TierHarness always succeeds, so this is unreachable.
	return harness.TierHarness, nil
}

// ProbeCandidate runs one candidate's harness once, headless, at the most
// restrictive tier the harness can honour (probeTier), in a fresh temp
// directory, and measures how long it takes to produce its first model output
// (#324 part 1). Failures are data: they come back in Err, never as a returned
// error, so one broken candidate cannot hide the rest of the listing.
func ProbeCandidate(ctx context.Context, rt Runtime, x LineExec, c candidate.Candidate, host string) ProbeResult {
	ref := c.Ref().String()

	var role harness.RoleSpec
	known := false
	for _, name := range c.Roles {
		if r, ok := harness.RoleByName(name); ok {
			role, known = r, true
			break
		}
	}
	if !known {
		return ProbeResult{At: rt.Now().UTC(), Token: ref, Host: host, Err: "no known role"}
	}

	dir, err := os.MkdirTemp("", "relay-probe-")
	if err != nil {
		return ProbeResult{At: rt.Now().UTC(), Token: ref, Host: host, Err: probeErrText(err.Error())}
	}
	defer os.RemoveAll(dir)

	tier, err := probeTier(c.Harness)
	if err != nil {
		return ProbeResult{At: rt.Now().UTC(), Token: ref, Host: host, Err: probeErrText(err.Error())}
	}

	argv, err := headlessLaunch(c, role, tier, probeBudget, probePrompt, dir, dir)
	if err != nil {
		return ProbeResult{At: rt.Now().UTC(), Token: ref, Host: host, Err: probeErrText(err.Error())}
	}

	start := rt.Now()
	var (
		ttft int64
		seen bool
	)

	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	runErr := x.Run(pctx, dir, argv, func(line []byte) {
		if seen {
			return
		}
		if transcript.FirstOutput(c.Harness, line) {
			seen = true
			ttft = rt.Now().Sub(start).Milliseconds()
		}
	})

	r := ProbeResult{
		At:      start.UTC(),
		Token:   ref,
		Host:    host,
		TTFTMS:  ttft,
		TotalMS: rt.Now().Sub(start).Milliseconds(),
	}
	switch {
	case runErr != nil:
		r.Err = probeErrText(runErr.Error())
	case !seen:
		r.Err = "no model output"
	}

	return r
}

// probeErrText bounds an error's text to the 300 bytes a sample carries, so
// one pathological harness cannot bloat the history file.
func probeErrText(s string) string {
	if len(s) <= 300 {
		return s
	}
	return s[:300]
}

// Probe measures every named candidate, sequentially, one at a time so the
// probes do not contend with each other, and records each result in the
// latency history. An unknown or unparsable token is a user error returned
// before anything runs. each, when non-nil, is called once per result in
// order, so the CLI can print as it goes.
func Probe(ctx context.Context, rt Runtime, x LineExec, tokens []string, host string, each func(ProbeResult)) ([]ProbeResult, error) {
	var cands []candidate.Candidate
	if len(tokens) == 0 {
		if rt.Candidates != nil {
			for _, ref := range rt.Candidates.Refs() {
				parsed, err := candidate.ParseRef(ref)
				if err != nil {
					continue
				}
				c, err := rt.Candidates.Lookup(parsed)
				if err != nil {
					continue
				}
				cands = append(cands, c)
			}
		}
	} else {
		for _, token := range tokens {
			parsed, err := candidate.ParseRef(token)
			if err != nil {
				return nil, err
			}
			c, err := rt.Candidates.Lookup(parsed)
			if err != nil {
				return nil, err
			}
			cands = append(cands, c)
		}
	}

	var results []ProbeResult
	for _, c := range cands {
		r := ProbeCandidate(ctx, rt, x, c, host)
		results = append(results, r)
		if each != nil {
			each(r)
		}
		recordLatency(rt, r)
	}

	return results, nil
}

// recordLatency appends one probe result to the latency history under the
// store lock, so a probe and a listing never read a torn file. A history
// failure is one stderr line; it never fails a probe. An empty LatencyPath
// means no store is configured, so there is nothing to record into.
func recordLatency(rt Runtime, r ProbeResult) {
	if rt.LatencyPath == "" {
		return
	}

	err := rt.Store.WithLock(func(*store.Tx) error {
		h, err := latency.Load(rt.LatencyPath)
		if err != nil {
			return err
		}
		return latency.Save(rt.LatencyPath, h.Prune(rt.Now()).Append(r))
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay: could not record latency: %v\n", err)
	}
}

// probeMS renders a millisecond count the way a listing reads: whole
// milliseconds below a second, one decimal above.
func probeMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// FormatProbe renders one probe result as a line, without a trailing
// newline: the time to first output and the total on success, or the error,
// with the TTFT appended when output was seen before the failure.
func FormatProbe(r ProbeResult, width int) string {
	if r.Err == "" {
		return fmt.Sprintf("%-*s  ttft %s  total %s", width, r.Token, probeMS(r.TTFTMS), probeMS(r.TotalMS))
	}

	out := fmt.Sprintf("%-*s  error: %s", width, r.Token, r.Err)
	if r.TTFTMS > 0 {
		out += "  (ttft " + probeMS(r.TTFTMS) + ")"
	}
	return out
}
