package proc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// ScopeUnitFileName returns the systemd unit file name for a scope unit
// (the value cgroupfs shows as the cgroup path's last element), built in
// exactly one place so the supervisor's own guard (#216) and systemd-run's
// --unit= flag never drift apart.
func ScopeUnitFileName(unit string) string {
	return unit + ".scope"
}

// ScopeArgv wraps inner (the argv Start would otherwise exec) so it runs as
// a transient systemd --scope unit instead: systemd-run execs inner in
// place once the scope is registered, so the pid Start records is inner's
// own pid (#244).
func ScopeArgv(s relevo.ScopeSpec, inner []string) []string {
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
// daemon relies on it for every served round: a throwaway scope that runs
// "true" and exits. nil means scopes work; a non-nil error names why they
// do not (systemd-run missing, the user manager refusing), and the caller
// runs served builders unscoped instead.
func ProbeScopes(ctx context.Context, slice string) error {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("systemd-run: %s", err.Error())
	}
	spec := relevo.ScopeSpec{Unit: "relevo-probe-" + hex.EncodeToString(suffix), Slice: slice, CPUWeight: 100}
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
		return fmt.Errorf("systemd-run: %s", line)
	}
	return nil
}

// ProbeAllowedCPUs confirms systemd-run accepts AllowedCPUs=<cpus> before the
// daemon relies on pinning every round (#314): a throwaway scope that runs
// "true" and exits, launched with the property. nil means it is accepted; a
// non-nil error names why it was refused, and the caller runs its spawns with
// the scope and its quota but without pinning. It detects refusal only: a
// systemd that silently ignores an undelegated cpuset shows no exit code, and
// `relevo doctor` is where that is reported.
func ProbeAllowedCPUs(ctx context.Context, slice, cpus string) error {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("systemd-run AllowedCPUs=%s: %s", cpus, err.Error())
	}
	spec := relevo.ScopeSpec{Unit: "relevo-probe-cpus-" + hex.EncodeToString(suffix), Slice: slice, CPUWeight: 100, AllowedCPUs: cpus}
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
		return fmt.Errorf("systemd-run AllowedCPUs=%s: %s", cpus, line)
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

// RusageTrailer prefixes the line the supervisor appends, inside a
// relevo-round-*.scope cgroup only, right before the exit trailer:
// "relevo-rusage:cpu_usec=<n> mem_peak=<n>".
const RusageTrailer = "relevo-rusage:"

// ParseRusageTrailer parses a RusageTrailer line, or the relay-rusage: line a // name-guard: legacy
// pre-rename stream carries (#292 §1): whichever prefix matches is stripped,
// and behaviour for the new prefix is unchanged. Fields are space-separated
// key=value; either may be absent (that field stays zero); unknown keys are
// ignored; a malformed number leaves that field zero. A line matching neither
// prefix reports ok false.
func ParseRusageTrailer(line string) (relevo.ProcRusage, bool) {
	prefix := RusageTrailer
	if !strings.HasPrefix(line, prefix) {
		prefix = legacy.RusageTrailer
		if !strings.HasPrefix(line, prefix) {
			return relevo.ProcRusage{}, false
		}
	}
	var r relevo.ProcRusage
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
