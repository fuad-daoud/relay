package proc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// ScopeUnitFileName returns the systemd unit file name for a scope unit, built
// here so the supervisor's cgroup guard and systemd-run's --unit= never drift.
func ScopeUnitFileName(unit string) string {
	return unit + ".scope"
}

// ScopeArgv wraps inner (the argv Start would otherwise exec) so it runs as a
// transient systemd --scope unit; systemd-run execs inner in place, so the pid
// Start records is inner's own. The quota precedes the pin, the pin memory.
func ScopeArgv(s spawn.ScopeSpec, inner []string) []string {
	argv := []string{"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=" + ScopeUnitFileName(s.Unit)}
	if s.Slice != "" {
		argv = append(argv, "--slice="+s.Slice)
	}
	argv = append(argv, "-p", "CPUWeight="+strconv.Itoa(s.CPUWeight))
	if s.CPUQuota != "" {
		argv = append(argv, "-p", "CPUQuota="+s.CPUQuota)
	}
	if s.AllowedCPUs != "" {
		argv = append(argv, "-p", "AllowedCPUs="+s.AllowedCPUs)
	}
	if s.MemoryMax != "" {
		argv = append(argv, "-p", "MemoryMax="+s.MemoryMax)
	}
	if s.TasksMax != 0 {
		argv = append(argv, "-p", "TasksMax="+strconv.Itoa(s.TasksMax))
	}
	argv = append(argv, "--")
	argv = append(argv, inner...)
	return argv
}

// ProbeScopes confirms systemd-run can start a scope under slice before the
// daemon relies on it; a non-nil error means served builders run unscoped.
func ProbeScopes(ctx context.Context, slice string) error {
	unit, err := probeUnit("relevo-probe-")
	if err != nil {
		return fmt.Errorf("systemd-run: %s", err.Error())
	}
	spec := spawn.ScopeSpec{Unit: unit, Slice: slice, CPUWeight: 100}
	return runScopeProbe(ctx, spec, "systemd-run")
}

// ProbeAllowedCPUs confirms systemd-run accepts AllowedCPUs=<cpus> before the
// daemon pins every round; a refused pin leaves the scope and its quota but
// drops the pin. It detects refusal only: a systemd that silently ignores an
// undelegated cpuset reports no error, which `relevo doctor` reports.
func ProbeAllowedCPUs(ctx context.Context, slice, cpus string) error {
	unit, err := probeUnit("relevo-probe-cpus-")
	if err != nil {
		return fmt.Errorf("systemd-run AllowedCPUs=%s: %s", cpus, err.Error())
	}
	spec := spawn.ScopeSpec{Unit: unit, Slice: slice, CPUWeight: 100, AllowedCPUs: cpus}
	return runScopeProbe(ctx, spec, "systemd-run AllowedCPUs="+cpus)
}

// runScopeProbe runs spec's throwaway scope, returning systemd-run's first
// stderr line, or its exit status when it printed nothing, prefixed with prefix.
func runScopeProbe(ctx context.Context, spec spawn.ScopeSpec, prefix string) error {
	argv := ScopeArgv(spec, []string{"true"})
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		line := firstNonEmptyLine(stderr.String())
		if line == "" {
			line = err.Error()
		}
		return fmt.Errorf("%s: %s", prefix, line)
	}
	return nil
}

func probeUnit(prefix string) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(suffix), nil
}

// ScopeActive reports whether unit's transient scope is still loaded: its
// ActiveState is active, activating, deactivating or reloading. A missing
// systemctl is (false, nil); any other failure is returned and treated as not
// active by the caller.
func (r *Runner) ScopeActive(ctx context.Context, unit string) (bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "systemctl", "--user", "show", "--property=ActiveState", "--value", ScopeUnitFileName(unit))
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "deactivating", "reloading":
		return true, nil
	default:
		return false, nil
	}
}

// StopScope ends unit's transient scope and every process still in its cgroup,
// so a straggler a harness abandoned cannot hold the unit open. It signals
// first and kills after the runner's grace, and returns once the unit is gone;
// an error means it may still be loaded. A unit that is not loaded, and a host
// with no systemctl, are not errors: there is nothing to end.
func (r *Runner) StopScope(ctx context.Context, unit string) error {
	file := ScopeUnitFileName(unit)
	if err := r.stopScope(ctx, file); err != nil {
		// The stop itself failed. The unit may still be there -- or it may
		// have gone away between the two calls, which is not an error.
		if active, perr := r.ScopeActive(ctx, unit); perr == nil && !active {
			return nil
		}
		return fmt.Errorf("proc: stop %s: %w", file, err)
	}
	if r.scopeGone(ctx, unit) {
		return nil
	}
	if err := r.sigkillScope(ctx, file); err != nil {
		return err
	}
	if r.scopeGone(ctx, unit) {
		return nil
	}
	return fmt.Errorf("proc: scope %s is still running after SIGKILL", file)
}

// stopScope asks systemd to stop file without blocking on the stop job: a
// plain stop waits out systemd's own timeout, which the caller cannot afford.
// A missing systemctl is nil: there is no systemd and no scope to end.
func (r *Runner) stopScope(ctx context.Context, file string) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := exec.CommandContext(cctx, "systemctl", "--user", "stop", "--no-block", file).Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	return err
}

// scopeGone reports whether unit's scope is no longer loaded, polling every
// 200 ms until it is gone or the runner's grace expires. A probe error counts
// as still loaded, so an unreadable state never reads as a stopped scope.
func (r *Runner) scopeGone(ctx context.Context, unit string) bool {
	deadline := time.Now().Add(r.grace())
	for {
		active, err := r.ScopeActive(ctx, unit)
		if err == nil && !active {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// sigkillScope kills every process still in file's cgroup. --kill-whom=all is
// required: the scope's main process is the dead supervisor, so the default
// main would signal nothing.
func (r *Runner) sigkillScope(ctx context.Context, file string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(cctx, "systemctl", "--user", "kill", "--kill-whom=all", "--signal=SIGKILL", file).Run(); err != nil {
		return fmt.Errorf("proc: kill %s: %w", file, err)
	}
	return nil
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// ScopeResult returns the systemd Result of unit's transient scope, e.g.
// "success" or "oom-kill". A missing systemctl returns ("", nil); any other
// failure is returned and treated as not oom-killed by the caller.
func (r *Runner) ScopeResult(ctx context.Context, unit string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "systemctl", "--user", "show", "--property=Result", "--value", ScopeUnitFileName(unit))
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return firstNonEmptyLine(string(out)), nil
}

// ParseRusageTrailer parses a RusageTrailer line, or the legacy line a
// pre-rename stream carries. Missing fields stay zero, unknown keys and
// malformed numbers are ignored, and a line matching neither prefix is not ok.
func ParseRusageTrailer(line string) (spawn.ProcRusage, bool) {
	prefix := spawn.RusageTrailerPrefix
	if !strings.HasPrefix(line, prefix) {
		prefix = legacy.RusageTrailer
		if !strings.HasPrefix(line, prefix) {
			return spawn.ProcRusage{}, false
		}
	}
	var r spawn.ProcRusage
	rest := strings.TrimPrefix(line, prefix)
	for _, field := range strings.Fields(rest) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "cpu_usec":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				r.CPUMS = n / 1000
			}
		case "mem_peak":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				r.PeakMemBytes = n
			}
		}
	}
	return r, true
}
