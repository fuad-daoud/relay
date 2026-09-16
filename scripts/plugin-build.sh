#!/bin/sh
# herdr [[build]] step for the from-source variant: build relay from the
# cloned repository and leave ./relay in the plugin root.
#
# herdr clones the whole repository and makes the manifest's directory the
# plugin root, so this runs with cwd = from-source/ and the repository one
# level up. It never prompts and gets no HERDR_* environment.
set -eu

plugin_root=$(pwd)

if ! command -v go >/dev/null 2>&1; then
	echo "plugin-build: no 'go' on PATH." >&2
	echo "  The from-source variant compiles relay and needs Go 1.22 or newer." >&2
	echo "  Install Go, or use the release variant instead:" >&2
	echo "    herdr plugin install fuad-daoud/relay" >&2
	exit 1
fi

# Prefer git for the repository root; fall back to the parent directory, which
# is where herdr puts a subdirectory plugin's root.
if repo_root=$(git rev-parse --show-toplevel 2>/dev/null) && [ -d "$repo_root/cmd/relay" ]; then
	:
elif [ -d ../cmd/relay ]; then
	# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
	repo_root=$(CDPATH= cd -- .. && pwd)
else
	echo "plugin-build: cannot find cmd/relay from $plugin_root" >&2
	exit 1
fi

echo "plugin-build: building relay from $repo_root"
cd "$repo_root"

# Stamp a version a human can place against a release. herdr's plugin
# clone has no tags, so a bare `git describe --always` printed a lone hash
# (#166 §4): fetch tags when none are reachable, and if there are still
# none, fall back to the manifest's version plus the hash.
if ! version=$(git describe --tags --dirty 2>/dev/null); then
	git fetch --tags --quiet 2>/dev/null || true
	if ! version=$(git describe --tags --dirty 2>/dev/null); then
		if hash=$(git rev-parse --short HEAD 2>/dev/null); then
			manifest=$(sed -n 's/^version = "\(.*\)"$/\1/p' "$plugin_root/herdr-plugin.toml" 2>/dev/null)
			version="${manifest:-devel}+$hash"
		else
			version=devel
		fi
	fi
fi
CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$version" -o "$plugin_root/relay" ./cmd/relay

echo "plugin-build: installed relay $version"
