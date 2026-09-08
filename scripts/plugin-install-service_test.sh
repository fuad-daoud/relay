#!/bin/sh
# Runs plugin-install-service.sh against stub systemctl/launchctl/herdr binaries
# and asserts it restarts an already-running daemon rather than only enabling it.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/bin" "$work/home" "$work/plugin/dist"
calls="$work/calls"

for tool in systemctl launchctl; do
	cat > "$work/bin/$tool" <<STUB
#!/bin/sh
printf '$tool %s\n' "\$*" >> "$calls"
exit 0
STUB
	chmod +x "$work/bin/$tool"
done

cat > "$work/bin/herdr" <<'STUB'
#!/bin/sh
exit 0
STUB
chmod +x "$work/bin/herdr"

# A plugin root shaped like the installed one: a relay binary and dist/.
printf '#!/bin/sh\nexit 0\n' > "$work/plugin/relay"
chmod +x "$work/plugin/relay"
# Stub unit templates rather than the repo's: this test asserts how the script
# drives systemctl/launchctl, not the contents of dist/.
printf '[Service]\nExecStart=%%h/.local/bin/relay daemon\n' > "$work/plugin/dist/relay.service"
printf '<plist>@BIN@ @HOME@</plist>\n' > "$work/plugin/dist/com.github.fuad-daoud.relay.plist.in"
cp "$here/plugin-install-service.sh" "$work/plugin/"

fail=0
(cd "$work/plugin" && HOME="$work/home" PATH="$work/bin:$PATH" HERDR_BIN_PATH="$work/bin/herdr" \
	sh plugin-install-service.sh >/dev/null 2>&1) || { echo "FAIL: script exited non-zero"; fail=1; }

[ -x "$work/home/.local/bin/relay" ] || { echo "FAIL: relay not installed to ~/.local/bin"; fail=1; }

case $(uname -s) in
Linux)
	# `enable --now` starts an inactive unit but leaves a running one on the old
	# binary, which is how a relay upgrade silently keeps serving the old daemon.
	grep -q 'systemctl .*restart relay.service' "$calls" \
		|| { echo "FAIL: no 'systemctl restart' -- an already-running daemon keeps the old binary"; fail=1; }
	;;
Darwin)
	grep -q 'launchctl load' "$calls" || { echo "FAIL: no launchctl load"; fail=1; }
	;;
esac

[ "$fail" -eq 0 ] && echo "plugin-install-service: ok"
exit "$fail"
