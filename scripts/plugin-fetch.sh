#!/bin/sh
# herdr [[build]] step for the release variant: download the relay release
# matching this manifest, verify it, and leave ./relay in the plugin root.
#
# Build commands get no HERDR_* environment and no terminal, so this script is
# self-contained and never prompts. Any failure exits non-zero, which aborts
# the install -- that is deliberate. A registered plugin with no binary is
# worse than an install that failed and said why.
set -eu

base_url=${RELAY_RELEASE_BASE_URL:-https://github.com/fuad-daoud/relay/releases/download}

version=$(sed -n 's/^version = "\(.*\)"$/\1/p' herdr-plugin.toml)
if [ -z "$version" ]; then
	echo "plugin-fetch: no version in herdr-plugin.toml" >&2
	exit 1
fi
tag="v$version"

case $(uname -s) in
	Linux) goos=linux ;;
	Darwin) goos=darwin ;;
	*) echo "plugin-fetch: unsupported OS $(uname -s); relay supports Linux and macOS" >&2; exit 1 ;;
esac

case $(uname -m) in
	x86_64 | amd64) goarch=amd64 ;;
	aarch64 | arm64) goarch=arm64 ;;
	*) echo "plugin-fetch: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

archive="relay_${tag}_${goos}_${goarch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "plugin-fetch: downloading $archive"
curl -fsSL --retry 2 -o "$tmp/$archive" "$base_url/$tag/$archive"
curl -fsSL --retry 2 -o "$tmp/checksums.txt" "$base_url/$tag/checksums.txt"

# release.yml writes `sha256sum relay_*.tar.gz > checksums.txt`, so each line is
# "<64 hex>  <filename>". macOS has shasum, not sha256sum.
expected=$(awk -v want="$archive" '$2 == want { print $1 }' "$tmp/checksums.txt")
if [ -z "$expected" ]; then
	echo "plugin-fetch: $archive is not listed in checksums.txt for $tag" >&2
	exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
else
	echo "plugin-fetch: no sha256sum or shasum on PATH; cannot verify the download" >&2
	exit 1
fi

if [ "$actual" != "$expected" ]; then
	echo "plugin-fetch: checksum mismatch for $archive" >&2
	echo "  expected $expected" >&2
	echo "  actual   $actual" >&2
	exit 1
fi

# The archive also carries README.md and LICENSE. Extract only the binary:
# the plugin root is a git checkout with its own README.md.
tar -xzf "$tmp/$archive" -C "$tmp" relay
mv "$tmp/relay" ./relay
chmod +x ./relay

echo "plugin-fetch: installed relay $version ($goos/$goarch)"
