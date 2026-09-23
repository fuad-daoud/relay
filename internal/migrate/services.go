package migrate

import "context"

// Unit names one client service: a user-level systemd unit on Linux, a
// LaunchAgent on macOS. Name is the systemd unit name or the launchd label;
// Path is the absolute unit file (systemd) or plist (launchd).
type Unit struct {
	Name string // "relay.service" | "relevo.service" | launchd label // name-guard: legacy
	Path string // absolute unit file (systemd) or plist (launchd)
}

// Services is the slice of the platform's service manager `relevo migrate`
// needs. OSServices returns the real implementations; tests pass a recording
// fake, so CI never runs systemctl or launchctl (#292 §3, round 3b).
//
// A method the platform cannot express is a no-op: launchd has no daemon to
// reload, so its Reload does nothing.
type Services interface {
	// Stop stops the unit now. systemd: systemctl --user stop <Name>;
	// launchd: launchctl unload <Path>.
	Stop(ctx context.Context, u Unit) error
	// Start starts the unit now. systemd: systemctl --user start <Name>;
	// launchd: launchctl load <Path>.
	Start(ctx context.Context, u Unit) error
	// Enable makes the unit start at login. systemd: systemctl --user enable
	// --now <Name>; launchd: launchctl load -w <Path>.
	Enable(ctx context.Context, u Unit) error
	// Disable stops the unit starting at login. systemd: systemctl --user
	// disable <Name>; launchd: launchctl unload -w <Path>.
	Disable(ctx context.Context, u Unit) error
	// Reload re-reads the platform's unit files. systemd: systemctl --user
	// daemon-reload; launchd: a no-op.
	Reload(ctx context.Context) error
	// State reports whether u runs now and whether it starts at login.
	// systemd: `is-active <Name>` == "active", `is-enabled <Name>` ==
	// "enabled" (a non-zero exit is just false). launchd: `launchctl list
	// <Name>` exit 0 means both true, else both false.
	State(ctx context.Context, u Unit) (active, enabled bool, err error)
}
