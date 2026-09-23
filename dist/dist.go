// Package dist embeds the service files relevo installs, so a binary can
// write them without the source checkout it was built from. `relevo migrate`
// (round 3, #292 §3 step 6) is the first reader: it writes ClientUnit as the
// user-level systemd unit, or renders LaunchdPlist into a LaunchAgent, and
// never installs a serve unit, whose content is host-specific.
package dist

import _ "embed"

// ClientUnit is dist/relevo.service, byte for byte: the user-level systemd
// unit `relevo migrate` installs on Linux.
//
//go:embed relevo.service
var ClientUnit string

// LaunchdPlist is dist/com.github.fuad-daoud.relevo.plist.in, byte for byte,
// with its @BIN@ and @HOME@ placeholders still in place: `relevo migrate`
// renders them, exactly as `make service` does, before writing the plist to
// ~/Library/LaunchAgents.
//
//go:embed com.github.fuad-daoud.relevo.plist.in
var LaunchdPlist string
