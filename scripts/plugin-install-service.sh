#!/bin/sh
# herdr action: install the relay binary to ~/.local/bin and register the
# daemon with systemd (Linux) or launchd (macOS).
#
# This is an action, not a build step, for two reasons. Writing outside herdr's
# own directories deserves explicit consent, and an action the user invokes is
# that consent. And it removes a real hazard: until it runs, the plugin's relay
# and any relay already on PATH are two binaries sharing one state directory.
# Afterwards the plugin's binary is the PATH binary.
set -eu

herdr_bin=${HERDR_BIN_PATH:-herdr}
plugin_root=$(pwd)
bin="$HOME/.local/bin/relay"

notify() {
	# shellcheck disable=SC2086 # the --sound flag must disappear when $2 is unset
	"$herdr_bin" notification show "relay" --body "$1" ${2:+--sound "$2"} || true
}

if [ ! -x ./relay ]; then
	notify "no relay binary in the plugin; reinstall the plugin" "request"
	exit 1
fi

mkdir -p "$(dirname "$bin")"
install -m755 ./relay "$bin"

case $(uname -s) in
Darwin)
	label=com.github.fuad-daoud.relay
	plist="$HOME/Library/LaunchAgents/$label.plist"
	template="$plugin_root/dist/$label.plist.in"
	if [ ! -f "$template" ]; then
		template="$plugin_root/../dist/$label.plist.in"
	fi
	mkdir -p "$(dirname "$plist")"
	sed -e "s|@BIN@|$bin|g" -e "s|@HOME@|$HOME|g" "$template" > "$plist"
	launchctl unload "$plist" 2>/dev/null || true
	launchctl load -w "$plist"
	notify "installed $bin and loaded the LaunchAgent" "done"
	;;
Linux)
	unit="$HOME/.config/systemd/user/relay.service"
	template="$plugin_root/dist/relay.service"
	if [ ! -f "$template" ]; then
		template="$plugin_root/../dist/relay.service"
	fi
	mkdir -p "$(dirname "$unit")"
	cp "$template" "$unit"
	chmod 644 "$unit"
	systemctl --user daemon-reload
	systemctl --user enable --now relay.service
	notify "installed $bin and started relay.service" "done"
	;;
*)
	notify "unsupported platform $(uname -s)" "request"
	exit 1
	;;
esac
