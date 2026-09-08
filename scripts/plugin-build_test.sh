#!/bin/sh
# Runs plugin-build.sh the way herdr runs it: cwd is from-source/, the script
# is reached by a relative path, and the binary must land in cwd.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$here/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail=0

# 1. Builds into the plugin root when run from a subdirectory of the repo.
mkdir -p "$root/from-source"
if (cd "$root/from-source" && sh ../scripts/plugin-build.sh >/dev/null 2>&1); then
	if [ ! -x "$root/from-source/relay" ]; then
		echo "FAIL: no binary in the plugin root"; fail=1
	elif ! "$root/from-source/relay" version >/dev/null 2>&1; then
		echo "FAIL: built binary does not run"; fail=1
	fi
else
	echo "FAIL: build exited non-zero"; fail=1
fi
rm -f "$root/from-source/relay"

# 2. Fails loudly, and does not produce a binary, when go is absent.
mkdir -p "$work/emptybin" "$work/sub"
cp "$here/plugin-build.sh" "$work/"
if (cd "$work/sub" && PATH="$work/emptybin" sh ../plugin-build.sh >/dev/null 2>&1); then
	echo "FAIL: missing go was accepted"; fail=1
elif [ -e "$work/sub/relay" ]; then
	echo "FAIL: missing go left a binary behind"; fail=1
fi

[ "$fail" -eq 0 ] && echo "plugin-build: ok"
exit "$fail"
