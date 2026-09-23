package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// RunningProc is one local process a daemon restart could kill (#370 §4.8):
// the binding it belongs to, which of the round's roles it plays, and its pid.
type RunningProc struct {
	Binding string
	Kind    string // "builder", "gate" or "consult"
	PID     int
}

// ParseUnifiedCgroup returns the cgroup path of the `0::<path>` line of
// /proc/<pid>/cgroup -- the unified hierarchy on a cgroup v2 host. A file with
// no such line (a cgroup v1 host, or a format relevo does not know) reports ok
// false, which RestartSafety reads as "cannot tell".
func ParseUnifiedCgroup(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return rest, true
		}
	}
	return "", false
}

// inOwnScope reports whether a cgroup path's last element is a relevo scope: the
// process is in its own `relevo-*.scope` and survives a daemon restart (#370
// §4.8). Only the last element is read, so any slice above it is irrelevant.
func inOwnScope(cgroupPath string) bool {
	base := path.Base(cgroupPath)
	return strings.HasPrefix(base, "relevo-") && strings.HasSuffix(base, ".scope")
}

// RestartSafety is the doctor row that says whether a daemon restart right now
// would kill anything (#370 §4.8). Name "restart". It never fails the report:
// a process whose cgroup read says not-exist has exited and is skipped, and a
// host relevo cannot read -- any other read error, or a file with no `0::`
// line, which is what macOS and a cgroup v1 host give -- degrades to
// `cannot tell on this host`.
//
// The Warn detail names at most three offenders, then `and K more`, and Unsafe
// carries the full count for restartNotice.
func RestartSafety(procs []RunningProc, readCgroup func(pid int) (string, error)) Check {
	c := Check{Group: "", Name: "restart"}
	if len(procs) == 0 {
		return restartOK(c, "no rounds running")
	}

	running := 0
	var offenders []string
	for _, p := range procs {
		content, err := readCgroup(p.PID)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // the process exited between its binding's read and this one
			}
			return restartCannotTell(c)
		}
		cgPath, ok := ParseUnifiedCgroup(content)
		if !ok {
			return restartCannotTell(c)
		}
		running++
		if !inOwnScope(cgPath) {
			offenders = append(offenders, fmt.Sprintf("%s %s pid %d in %s", p.Binding, p.Kind, p.PID, path.Base(cgPath)))
		}
	}

	// Every gathered process read as not-exist. On Linux they simply exited;
	// on a host with no /proc (macOS) that is every process, and relevo cannot
	// tell the two apart -- which is why this is the "cannot tell" answer
	// rather than a quiet OK.
	if running == 0 {
		return restartCannotTell(c)
	}
	if len(offenders) == 0 {
		return restartOK(c, fmt.Sprintf("%d running, each in its own scope; a daemon restart leaves them running", running))
	}
	c.Severity = SevWarn
	c.Detail = fmt.Sprintf("%d of %d running outside their own scope (%s): a daemon restart may kill them",
		len(offenders), running, offendersText(offenders))
	c.Fix = "let them finish before restarting relevo.service"
	c.Unsafe = len(offenders)
	return c
}

// restartOK returns c as a quiet OK row with detail.
func restartOK(c Check, detail string) Check {
	c.Severity = SevOK
	c.Detail = detail
	return c
}

// restartCannotTell is the "cannot tell on this host" row: relevo could not
// establish the fact, and nothing is wrong that it can act on.
func restartCannotTell(c Check) Check {
	c.Severity = SevOK
	c.Detail = "cannot tell on this host"
	return c
}

// offendersText joins the offenders for the Warn detail, listing at most three
// and summarising the rest as `and K more` (#370 §4.8).
func offendersText(offenders []string) string {
	const max = 3
	if len(offenders) <= max {
		return strings.Join(offenders, ", ")
	}
	return strings.Join(offenders[:max], ", ") + fmt.Sprintf(", and %d more", len(offenders)-max)
}
