#!/bin/sh
# Exercises plugin-fetch.sh against a local fixture served over file://.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

goos=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$goos" in linux) goos=linux ;; darwin) goos=darwin ;; esac
case $(uname -m) in
	x86_64 | amd64) goarch=amd64 ;;
	aarch64 | arm64) goarch=arm64 ;;
	*) echo "unsupported test arch $(uname -m)" >&2; exit 1 ;;
esac

# Build a fixture release: an archive shaped exactly like release.yml's.
mkdir -p "$work/rel/v9.9.9" "$work/stage"
printf '#!/bin/sh\necho fixture-relay\n' > "$work/stage/relay"
chmod +x "$work/stage/relay"
echo readme > "$work/stage/README.md"
echo license > "$work/stage/LICENSE"
archive="relay_v9.9.9_${goos}_${goarch}.tar.gz"
tar -czf "$work/rel/v9.9.9/$archive" -C "$work/stage" relay README.md LICENSE

sum() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}
(cd "$work/rel/v9.9.9" && sum "$archive" > checksums.txt)

# A plugin root that looks like the real one.
mkdir -p "$work/plugin"
cat > "$work/plugin/herdr-plugin.toml" <<'EOF'
id = "fuad-daoud.relay"
name = "relay"
version = "9.9.9"
min_herdr_version = "0.8.2"
EOF
cp "$here/plugin-fetch.sh" "$work/plugin/"
echo "keep me" > "$work/plugin/README.md"

fail=0

# 1. Happy path.
if (cd "$work/plugin" && RELAY_RELEASE_BASE_URL="file://$work/rel" sh plugin-fetch.sh >/dev/null 2>&1); then
	if [ ! -x "$work/plugin/relay" ]; then
		echo "FAIL: relay binary not produced"; fail=1
	elif [ "$("$work/plugin/relay")" != "fixture-relay" ]; then
		echo "FAIL: wrong binary extracted"; fail=1
	fi
	# The archive also contains README.md; it must not clobber the checkout's.
	if [ "$(cat "$work/plugin/README.md")" != "keep me" ]; then
		echo "FAIL: extraction overwrote README.md"; fail=1
	fi
else
	echo "FAIL: happy path exited non-zero"; fail=1
fi

# 2. Corrupt checksum must fail loudly and leave no binary.
rm -f "$work/plugin/relay"
printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$archive" > "$work/rel/v9.9.9/checksums.txt"
if (cd "$work/plugin" && RELAY_RELEASE_BASE_URL="file://$work/rel" sh plugin-fetch.sh >/dev/null 2>&1); then
	echo "FAIL: bad checksum was accepted"; fail=1
elif [ -e "$work/plugin/relay" ]; then
	echo "FAIL: bad checksum left a binary behind"; fail=1
fi

# 3. Missing release must fail, not hang.
if (cd "$work/plugin" && RELAY_RELEASE_BASE_URL="file://$work/nope" sh plugin-fetch.sh >/dev/null 2>&1); then
	echo "FAIL: missing release was accepted"; fail=1
fi

[ "$fail" -eq 0 ] && echo "plugin-fetch: ok"
exit "$fail"
