package main

import "github.com/fuad-daoud/relay/internal/store"

// daemonNotice is the line `relay status` prints above the rows when the
// running daemon's recorded state needs a human (#371 §4.8). Pure: every input
// is an argument, so it is table-tested with no store and no daemon.
//
// It returns "" unless the daemon is running and one of two conditions holds:
// the daemon predates version tracking (no daemon.json), or it refused the
// binary it was asked to load. A plain version difference prints nothing,
// because it is transient during a re-exec.
func daemonNotice(cli string, info store.DaemonInfo, ok, running bool) string {
	if !running {
		return ""
	}
	if !ok {
		return "relay daemon predates version tracking -- restart it once (relay doctor)"
	}
	if info.ReexecFailed != nil {
		return "relay daemon refused the new binary -- relay doctor"
	}
	return ""
}
