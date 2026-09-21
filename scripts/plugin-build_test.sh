#!/bin/sh
# Runs plugin-build.sh the way herdr runs it: cwd is from-source/, the script
# is reached by a relative path, and the binary must land in cwd.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
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

# 3. A tagless clone with a reachable, tagged remote fetches tags and describes.
git clone -q "$root" "$work/origin"                       # carries the enclosing repo's tags if any
if ! git -C "$work/origin" describe --tags >/dev/null 2>&1; then
	git -C "$work/origin" -c tag.gpgsign=false tag v0.0.0-fixture     # hermetic: the fixture provides its own tag
fi
git clone -q --no-tags "$work/origin" "$work/withremote"
mkdir -p "$work/withremote/from-source"
if (cd "$work/withremote/from-source" && sh ../scripts/plugin-build.sh >/dev/null 2>&1); then
	v=$("$work/withremote/from-source/relay" version); v=${v#relay }
	case "$v" in
		v[0-9]*) ;;
		*) echo "FAIL: tagless clone with remote printed '$v', want a tag-relative describe"; fail=1 ;;
	esac
else
	echo "FAIL: build in tagless clone with remote exited non-zero"; fail=1
fi

# 4. No tags and no remote: manifest version plus the short hash, never a bare hash.
git clone -q --no-tags "$root" "$work/noremote"
(cd "$work/noremote" && git remote remove origin)
mkdir -p "$work/noremote/from-source"
if (cd "$work/noremote/from-source" && sh ../scripts/plugin-build.sh >/dev/null 2>&1); then
	v=$("$work/noremote/from-source/relay" version); v=${v#relay }
	case "$v" in
		[0-9]*.[0-9]*.[0-9]*+[0-9a-f]*) ;;
		*) echo "FAIL: tagless clone without remote printed '$v', want <manifest>+<hash>"; fail=1 ;;
	esac
else
	echo "FAIL: build in tagless clone without remote exited non-zero"; fail=1
fi

[ "$fail" -eq 0 ] && echo "plugin-build: ok"
exit "$fail"
