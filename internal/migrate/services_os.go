package migrate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// OSServices returns the Services implementation for goos: systemd's
// user-level manager on Linux, launchd on macOS, and a no-op everywhere else.
// The function reads the GOOS it is handed rather than runtime.GOOS so tests
// can exercise each branch without cross-compiling.
//
// Every command runs through exec.CommandContext and a failure returns an
// error carrying the combined output. The file has no build constraints and
// no OS-specific imports, so it compiles on every GOOS CI cross-builds.
func OSServices(goos string) Services {
	switch goos {
	case "linux":
		return systemdServices{}
	case "darwin":
		return launchdServices{}
	default:
		return noopServices{}
	}
}

// systemdServices drives `systemctl --user`.
type systemdServices struct{}

// output runs a systemctl --user command and returns its combined output. It
// never returns an error: the two callers that must tell a non-zero exit from
// a real one do their own checking (State), and the mutating calls use run.
func (systemdServices) output(ctx context.Context, args ...string) string {
	full := append([]string{"--user"}, args...)
	out, _ := exec.CommandContext(ctx, "systemctl", full...).CombinedOutput()
	return string(out)
}

// run runs a mutating systemctl --user command, wrapping a failure with the
// combined output.
func (systemdServices) run(ctx context.Context, args ...string) error {
	full := append([]string{"--user"}, args...)
	out, err := exec.CommandContext(ctx, "systemctl", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(full, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s systemdServices) Stop(ctx context.Context, u Unit) error {
	return s.run(ctx, "stop", u.Name)
}

func (s systemdServices) Start(ctx context.Context, u Unit) error {
	return s.run(ctx, "start", u.Name)
}

func (s systemdServices) Enable(ctx context.Context, u Unit) error {
	return s.run(ctx, "enable", "--now", u.Name)
}

func (s systemdServices) Disable(ctx context.Context, u Unit) error {
	return s.run(ctx, "disable", u.Name)
}

func (s systemdServices) Reload(ctx context.Context) error {
	return s.run(ctx, "daemon-reload")
}

// State asks is-active and is-enabled; a non-zero exit is just false, not an
// error, because "inactive" and "disabled" are normal answers.
func (s systemdServices) State(ctx context.Context, u Unit) (bool, bool, error) {
	active := strings.TrimSpace(s.output(ctx, "is-active", u.Name)) == "active"
	enabled := strings.TrimSpace(s.output(ctx, "is-enabled", u.Name)) == "enabled"
	return active, enabled, nil
}

// launchdServices drives `launchctl`.
type launchdServices struct{}

// run runs a launchctl command, wrapping a failure with the combined output.
func (launchdServices) run(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s launchdServices) Stop(ctx context.Context, u Unit) error {
	return s.run(ctx, "unload", u.Path)
}

func (s launchdServices) Start(ctx context.Context, u Unit) error {
	return s.run(ctx, "load", u.Path)
}

func (s launchdServices) Enable(ctx context.Context, u Unit) error {
	return s.run(ctx, "load", "-w", u.Path)
}

func (s launchdServices) Disable(ctx context.Context, u Unit) error {
	return s.run(ctx, "unload", "-w", u.Path)
}

// Reload is a no-op: launchd has no unit-file cache to reread.
func (launchdServices) Reload(ctx context.Context) error { return nil }

// State reports both true when `launchctl list <label>` exits 0, both false
// otherwise. launchd has no separate "enabled" flag once a plist is loaded.
func (launchdServices) State(ctx context.Context, u Unit) (bool, bool, error) {
	if err := exec.CommandContext(ctx, "launchctl", "list", u.Name).Run(); err == nil {
		return true, true, nil
	}
	return false, false, nil
}

// noopServices is what a platform with no managed client service gets: every
// call does nothing and reports nothing running.
type noopServices struct{}

func (noopServices) Stop(context.Context, Unit) error    { return nil }
func (noopServices) Start(context.Context, Unit) error   { return nil }
func (noopServices) Enable(context.Context, Unit) error  { return nil }
func (noopServices) Disable(context.Context, Unit) error { return nil }
func (noopServices) Reload(context.Context) error        { return nil }

func (noopServices) State(context.Context, Unit) (bool, bool, error) {
	return false, false, nil
}
