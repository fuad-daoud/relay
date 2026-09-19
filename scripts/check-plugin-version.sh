#!/bin/sh
# Verifies plugin manifest versions and drift:
# 1. herdr-plugin.toml and from-source/herdr-plugin.toml must exist and agree,
#    and the version stamped in the web/index.html colophon must match them.
# 2. When passed a tag argument, the tag version must match the manifests.
# 3. First-parent feat commits since the manifest tag must not exceed max_feat_drift.
# The release commit itself passes because the tag does not exist yet when make release runs make check.
set -eu

# Maximum number of feat commits allowed on main past the tagged release before
# check-plugin-version requires a release to be cut (#164).
max_feat_drift=10

m1="herdr-plugin.toml"
m2="from-source/herdr-plugin.toml"

if [ $# -gt 1 ]; then
	echo "usage: check-plugin-version.sh [tag]" >&2
	exit 1
fi

if [ ! -f "$m1" ]; then
	echo "check-plugin-version: missing manifest $m1" >&2
	exit 1
fi

if [ ! -f "$m2" ]; then
	echo "check-plugin-version: missing manifest $m2" >&2
	exit 1
fi

v1=$(sed -n 's/^version = "\(.*\)"$/\1/p' "$m1")
if [ -z "$v1" ]; then
	echo "check-plugin-version: missing or unparseable version in $m1" >&2
	exit 1
fi

v2=$(sed -n 's/^version = "\(.*\)"$/\1/p' "$m2")
if [ -z "$v2" ]; then
	echo "check-plugin-version: missing or unparseable version in $m2" >&2
	exit 1
fi

if [ "$v1" != "$v2" ]; then
	echo "check-plugin-version: manifest versions disagree:" >&2
	echo "  $m1: $v1" >&2
	echo "  $m2: $v2" >&2
	exit 1
fi

# The site colophon is stamped by make release alongside the manifests (#196);
# without this check nothing would notice it drifting.
site=web/index.html
if [ ! -f "$site" ]; then
	echo "check-plugin-version: missing site page $site" >&2
	exit 1
fi
v3=$(sed -n 's/.*<span data-version>v\([^<]*\)<\/span>.*/\1/p' "$site")
if [ -z "$v3" ]; then
	echo "check-plugin-version: missing or unparseable <span data-version> in $site" >&2
	exit 1
fi
if [ "$v1" != "$v3" ]; then
	echo "check-plugin-version: site version disagrees with manifests:" >&2
	echo "  $m1: $v1" >&2
	echo "  $site: $v3" >&2
	exit 1
fi

if [ $# -eq 1 ]; then
	tag=$1
	tag_version=${tag#v}
	if [ "$v1" != "$tag_version" ]; then
		echo "check-plugin-version: tag version '$tag' does not match manifest versions:" >&2
		echo "  tag '$tag' (version '$tag_version')" >&2
		echo "  $m1: $v1" >&2
		echo "  $m2: $v2" >&2
		exit 1
	fi
fi

tag="v$v1"
if ! git rev-parse --verify -q "refs/tags/$tag" >/dev/null 2>&1; then
	echo "check-plugin-version: tag $tag not found; skipping drift check" >&2
	exit 0
fi

n=$(git log --first-parent --format=%s "$tag..HEAD" | grep -E -c '^feat(\([^)]*\))?!?:' || true)
if [ "$n" -gt "$max_feat_drift" ]; then
	echo "check-plugin-version: $n feat commits since $tag (limit $max_feat_drift); cut a release: make release VERSION=<next>" >&2
	exit 1
fi

