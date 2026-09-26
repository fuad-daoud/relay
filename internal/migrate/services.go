package migrate

import "context"

// Unit names one client service: a systemd unit on Linux, a LaunchAgent on
// macOS.
type Unit struct {
	Name string // "relay.service" | "relevo.service" | launchd label // name-guard: legacy
	Path string // absolute unit file (systemd) or plist (launchd)
}

// Services is the slice of the platform's service manager `relevo migrate`
// needs; tests pass a recording fake, so CI never runs systemctl or
// launchctl. A method the platform cannot express is a no-op.
type Services interface {
	Stop(ctx context.Context, u Unit) error
	Start(ctx context.Context, u Unit) error
	Enable(ctx context.Context, u Unit) error  // start at login too
	Disable(ctx context.Context, u Unit) error // stop starting at login
	Reload(ctx context.Context) error          // re-read the platform's unit files
	// State reports whether u runs now and whether it starts at login. A
	// non-zero systemctl exit just means false, not an error.
	State(ctx context.Context, u Unit) (active, enabled bool, err error)
}
