#!/bin/sh
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Build a fake repo root with two manifests at the given versions.
stage() {
	rm -rf "$work/repo"
	mkdir -p "$work/repo/from-source"
	printf 'id = "x"\nversion = "%s"\n' "$1" > "$work/repo/herdr-plugin.toml"
	printf 'id = "x"\nversion = "%s"\n' "$2" > "$work/repo/from-source/herdr-plugin.toml"
	cp "$here/check-plugin-version.sh" "$work/repo/"
}

fail=0
check() { # description, expected-exit, args...
	desc=$1; want=$2; shift 2
	if (cd "$work/repo" && sh check-plugin-version.sh "$@" >/dev/null 2>&1); then got=0; else got=1; fi
	if [ "$got" -ne "$want" ]; then
		echo "FAIL: $desc (exit $got, want $want)"; fail=1
	fi
}

stage 1.2.3 1.2.3
check "agreeing manifests, no tag" 0
check "agreeing manifests, matching tag" 0 v1.2.3
check "agreeing manifests, mismatched tag" 1 v9.9.9

stage 1.2.3 4.5.6
check "disagreeing manifests, no tag" 1
check "disagreeing manifests, matching tag" 1 v1.2.3

[ "$fail" -eq 0 ] && echo "check-plugin-version: ok"
exit "$fail"
