#!/bin/sh
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash (Ubuntu's /bin/sh) a
# failing EXIT trap's status replaces the script's own, so a teardown hiccup
# reads exactly like a test failure to `sh "$t" || exit 1` in the Makefile
# (#304).
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# Build a fake repo root with both manifests at the given versions: $1 for
# plugin.json, $2 (default $1) for marketplace.json.
stage() {
	v2=${2:-$1}
	rm -rf "$work/repo"
	mkdir -p "$work/repo/claude-plugin/.claude-plugin" "$work/repo/.claude-plugin"
	printf '{"name": "relay", "version": "%s"}\n' "$1" > "$work/repo/claude-plugin/.claude-plugin/plugin.json"
	printf '{"name": "relay", "plugins": [{"name": "relay", "version": "%s"}]}\n' "$v2" > "$work/repo/.claude-plugin/marketplace.json"
	cp "$here/check-plugin-version.sh" "$work/repo/"
}

# Build a fake repo root inside a git repository with the given version
# tag on its first commit and N subsequent feat commits.
stage_repo() {
	stage "$1" "$1"
	(
		cd "$work/repo"
		git init -q
		git config user.name test
		git config user.email test@example.com
		git config commit.gpgsign false
		git config tag.gpgsign false
		# Every `git commit` otherwise spawns `git maintenance run --auto
		# --quiet --detach`, which outlives this subshell and writes under
		# .git/objects while the EXIT trap is removing the tree -- `rm` then
		# fails ENOTEMPTY (#304).
		git config maintenance.auto false
		git config gc.auto 0
		git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false commit --allow-empty -q -m "chore: release v$1"
		git -c tag.gpgsign=false tag "v$1"
		i=1
		while [ "$i" -le "$2" ]; do
			git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false commit --allow-empty -q -m "feat: $i"
			i=$((i + 1))
		done
	)
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

stage 9.9.9 1.2.3
check "plugin.json version disagrees" 1

stage 1.2.3 9.9.9
check "marketplace.json version disagrees" 1

stage_repo 1.2.3 10
check "drift at the limit" 0

stage_repo 1.2.3 11
check "drift past the limit" 1

stage_repo 1.2.3 11
(cd "$work/repo" && git tag -d v1.2.3 >/dev/null)
check "tag absent, drift would be past the limit" 0

stage_repo 1.2.3 5
(
	cd "$work/repo"
	git checkout -b side -q
	i=1
	while [ "$i" -le 6 ]; do
		git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false commit --allow-empty -q -m "feat: side $i"
		i=$((i + 1))
	done
	git checkout - -q
	git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false merge --no-ff -q -m "Merge side" side
)
check "feats behind a merge are not counted" 0

[ "$fail" -eq 0 ] && echo "check-plugin-version: ok"
exit "$fail"
