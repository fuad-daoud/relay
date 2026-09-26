#!/bin/sh
# scripts/test-shard_test.sh -- tests for the shard assignment.
#
# Every assertion runs --dry-run, which needs only `go list` and the package
# sources: no test is compiled or run. The invariant under test: for any
# TOTAL, the shards are pairwise
# disjoint and their union is every whole package plus every test name of every
# split package.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
repo=$(CDPATH= cd -- "$here/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

fail=0
status=0

# dry_run runs test-shard.sh --dry-run for INDEX/TOTAL from the repo root,
# leaving its exit status in $status and its output in $work/out. The script
# resolves packages relative to the cwd, as CI does.
dry_run() {
	status=0
	(cd "$repo" && sh "$here/test-shard.sh" --dry-run "$1" "$2") > "$work/out" 2>&1 || status=$?
}

# The bad-usage cases run without --dry-run, exactly as the usage text reads,
# and must exit 2 before running anything.
check_usage() {
	status=0
	(cd "$repo" && sh "$here/test-shard.sh" "$@") > "$work/out" 2>&1 || status=$?
	if [ "$status" -ne 2 ]; then
		echo "FAIL: 'test-shard.sh $*' exits $status, want 2"
		fail=1
	fi
}

# The TOTAL=1 shard is the whole assignment; every other TOTAL is a partition
# of it, so it is the reference the union is compared against.
dry_run 0 1
if [ "$status" -ne 0 ]; then
	echo "FAIL: TOTAL=1 dry run exits $status, want 0"
	fail=1
fi
LC_ALL=C sort -u "$work/out" > "$work/all.txt"

test_lines=$(grep -cE '^test .*/internal/(relevo|delivery) ' "$work/all.txt" || :)
if [ "$test_lines" -le 1000 ]; then
	echo "FAIL: TOTAL=1 lists $test_lines internal/relevo+delivery tests, want more than 1000"
	fail=1
fi

pkg_lines=$(grep -c '^pkg ' "$work/all.txt" || :)
if [ "$pkg_lines" -lt 30 ]; then
	echo "FAIL: TOTAL=1 lists $pkg_lines packages, want at least 30"
	fail=1
fi

for total in 1 2 3 4; do
	: > "$work/union-$total.txt"
	index=0
	while [ "$index" -lt "$total" ]; do
		dry_run "$index" "$total"
		if [ "$status" -ne 0 ]; then
			echo "FAIL: TOTAL=$total INDEX=$index exits $status, want 0"
			fail=1
		fi
		cat "$work/out" >> "$work/union-$total.txt"
		index=$((index + 1))
	done

	# Disjoint: no line is produced by two shards.
	dupes=$(LC_ALL=C sort "$work/union-$total.txt" | uniq -d)
	if [ -n "$dupes" ]; then
		echo "FAIL: TOTAL=$total shards overlap:"
		printf '%s\n' "$dupes"
		fail=1
	fi

	# Union: the shards together are exactly the TOTAL=1 assignment.
	LC_ALL=C sort -u "$work/union-$total.txt" > "$work/union-$total.sorted"
	if ! cmp -s "$work/union-$total.sorted" "$work/all.txt"; then
		echo "FAIL: TOTAL=$total shards do not cover the TOTAL=1 assignment"
		fail=1
	fi
done

# Bad usage: no arguments, INDEX == TOTAL, and a negative INDEX.
check_usage
check_usage 3 3
check_usage -1 2

[ "$fail" -eq 0 ] && echo "test-shard: ok"
exit "$fail"
