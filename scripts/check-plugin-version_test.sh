#!/bin/sh
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Build a fake repo root with two manifests at the given versions.
stage() {
	site=${3:-$1}
	rm -rf "$work/repo"
	mkdir -p "$work/repo/from-source" "$work/repo/web"
	printf 'id = "x"\nversion = "%s"\n' "$1" > "$work/repo/herdr-plugin.toml"
	printf 'id = "x"\nversion = "%s"\n' "$2" > "$work/repo/from-source/herdr-plugin.toml"
	printf '<p><span data-version>v%s</span></p>\n' "$site" > "$work/repo/web/index.html"
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

stage 1.2.3 1.2.3 9.9.9
check "site version disagrees with manifests" 1

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
