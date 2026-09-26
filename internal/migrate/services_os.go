package migrate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// OSServices returns the Services implementation for goos. It takes goos
// rather than reading runtime.GOOS so tests can exercise every branch
// without cross-compiling.
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

// output runs a systemctl --user command, ignoring its exit code: State tells
// a non-zero exit that means false from a real failure itself.
func (systemdServices) output(ctx context.Context, args ...string) string {
	full := append([]string{"--user"}, args...)
	out, _ := exec.CommandContext(ctx, "systemctl", full...).CombinedOutput()
	return string(out)
}

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

// State treats a non-zero exit as false, not an error: "inactive" and
// "disabled" are normal answers.
func (s systemdServices) State(ctx context.Context, u Unit) (bool, bool, error) {
	active := strings.TrimSpace(s.output(ctx, "is-active", u.Name)) == "active"
	enabled := strings.TrimSpace(s.output(ctx, "is-enabled", u.Name)) == "enabled"
	return active, enabled, nil
}

// launchdServices drives `launchctl`.
type launchdServices struct{}

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

func (launchdServices) Reload(ctx context.Context) error { return nil }

// State reports both true when `launchctl list <label>` exits 0, both false
// otherwise: launchd has no separate "enabled" flag once a plist is loaded.
func (launchdServices) State(ctx context.Context, u Unit) (bool, bool, error) {
	if err := exec.CommandContext(ctx, "launchctl", "list", u.Name).Run(); err == nil {
		return true, true, nil
	}
	return false, false, nil
}

// noopServices is every other platform: no call does anything.
type noopServices struct{}

func (noopServices) Stop(context.Context, Unit) error    { return nil }
func (noopServices) Start(context.Context, Unit) error   { return nil }
func (noopServices) Enable(context.Context, Unit) error  { return nil }
func (noopServices) Disable(context.Context, Unit) error { return nil }
func (noopServices) Reload(context.Context) error        { return nil }

func (noopServices) State(context.Context, Unit) (bool, bool, error) {
	return false, false, nil
}
