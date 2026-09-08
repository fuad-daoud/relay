#!/bin/sh
# herdr action: open the relay reader as an overlay pane.
#
# This exists as an action rather than being invoked as a pane directly because
# a herdr keybinding can only target an action, never a pane entrypoint.
set -eu

herdr_bin=${HERDR_BIN_PATH:-herdr}

if err=$("$herdr_bin" plugin pane open --plugin fuad-daoud.relay --entrypoint ui 2>&1); then
	exit 0
fi

# ui_busy means Settings, Copy mode, or another herdr modal is open. Say so
# rather than failing silently -- the user pressed a key and deserves an answer.
case "$err" in
	*ui_busy*)
		"$herdr_bin" notification show "relay" \
			--body "close the open herdr dialog first, then try again" || true
		;;
	*)
		"$herdr_bin" notification show "relay: could not open the reader" \
			--body "$err" --sound request || true
		;;
esac

exit 0
