#!/bin/sh
# Verifies plugin manifest versions and drift:
# 1. The Claude Code plugin manifest (claude-plugin/.claude-plugin/plugin.json)
#    and the marketplace manifest (.claude-plugin/marketplace.json) must exist
#    and agree.
# 2. When passed a tag argument, the tag version must match the manifests.
# 3. First-parent feat commits since the manifest tag must not exceed max_feat_drift.
# The release commit itself passes because the tag does not exist yet when make release runs make check.
set -eu

# Maximum number of feat commits allowed on main past the tagged release before
# check-plugin-version requires a release to be cut (#164).
max_feat_drift=10

m1="claude-plugin/.claude-plugin/plugin.json"
m2=".claude-plugin/marketplace.json"

if [ $# -gt 1 ]; then
	echo "usage: check-plugin-version.sh [tag]" >&2
	exit 1
fi

# extract_version prints one JSON manifest's version string: plugin.json
# carries "version" at the top level, marketplace.json inside its one
# plugins[] entry, so the first match in the file is always the one meant.
extract_version() {
	grep -o '"version"[[:space:]]*:[[:space:]]*"[^"]*"' "$1" | head -n1 | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p'
}

for m in "$m1" "$m2"; do
	if [ ! -f "$m" ]; then
		echo "check-plugin-version: missing manifest $m" >&2
		exit 1
	fi
done

v1=$(extract_version "$m1")
if [ -z "$v1" ]; then
	echo "check-plugin-version: missing or unparseable version in $m1" >&2
	exit 1
fi

v2=$(extract_version "$m2")
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
