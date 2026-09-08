#!/bin/sh
# herdr [[startup]] hook: report whether a relay reconciler is running.
#
# This is deliberately not a supervisor. herdr startup hooks are one-shot and
# re-run when a new server takes over during live handoff, so spawning a daemon
# here would eventually run two against one state directory. Supervision belongs
# to systemd and launchd, which restart on failure; this hook only notices.
#
# It always exits 0. A missing daemon is a nudge, not a server-level failure.
set -eu

herdr_bin=${HERDR_BIN_PATH:-herdr}

if [ ! -x ./relay ]; then
	"$herdr_bin" notification show "relay" \
		--body "plugin installed but no relay binary; reinstall the plugin" \
		--sound request || true
	exit 0
fi

if ./relay daemon --check; then
	exit 0
fi

"$herdr_bin" notification show "relay daemon is not running" \
	--body "Reports will only arrive when you run 'relay pull'. Run the plugin's install-service action to start it with your session." \
	--sound request || true

exit 0
