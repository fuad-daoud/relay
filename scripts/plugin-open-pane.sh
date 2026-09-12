#!/bin/sh
# herdr action: open one of relay's plugin panes.
#
# usage: plugin-open-pane.sh <entrypoint-id>
#
# Actions exist because a herdr keybinding can only target an action, never a
# pane entrypoint. Every relay action that opens a pane goes through this one
# script: `ui` for the reader, `pick-done` / `pick-unbind` / `pick-answer` for
# the pickers (#15).
set -eu

entrypoint=${1:?usage: plugin-open-pane.sh <entrypoint-id>}
herdr_bin=${HERDR_BIN_PATH:-herdr}

if err=$("$herdr_bin" plugin pane open --plugin fuad-daoud.relay --entrypoint "$entrypoint" 2>&1); then
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
		"$herdr_bin" notification show "relay: could not open $entrypoint" \
			--body "$err" --sound request || true
		;;
esac

exit 0
