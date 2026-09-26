#!/bin/sh
# scripts/check-coverage_test.sh -- tests for the per-package coverage guard.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT
fixtures="$here/testdata/check-coverage"

# stage puts the guard at its real relative path in a scratch tree with the
# fixture baseline as testdata/coverage-baseline.txt, matching where the
# script expects to find it (next to its own scripts/ directory).
stage() {
	rm -rf "$work/repo"
	mkdir -p "$work/repo/scripts" "$work/repo/testdata"
	cp "$here/check-coverage.sh" "$work/repo/scripts/"
	cp "$fixtures/baseline.txt" "$work/repo/testdata/coverage-baseline.txt"
}

fail=0
status=0
# run executes the guard in the staged repo, leaving its exit status in
# $status and its output in $work/out.
run() {
	status=0
	(cd "$work/repo" && sh scripts/check-coverage.sh "$@") > "$work/out" 2>&1 || status=$?
}

expect_exit() { # description, expected exit
	if [ "$status" -ne "$2" ]; then
		echo "FAIL: $1 (exit $status, want $2)"; fail=1
	fi
}

expect_line() { # description, expected output line
	if ! grep -qF -- "$2" "$work/out"; then
		echo "FAIL: $1 (missing: $2)"; fail=1
	fi
}

# A passing output -- every package at or above its baseline -- exits 0 and
# says so.
stage
cp "$fixtures/passing.txt" "$work/repo/coverage.txt"
run coverage.txt
expect_exit "a passing output exits 0" 0
expect_line "a passing output prints ok" "check-coverage: ok"

# A regression of 1.5 points, more than the one point allowed, fails and names
# the package and both percentages.
stage
cp "$fixtures/regression.txt" "$work/repo/coverage.txt"
run coverage.txt
expect_exit "a 1.5-point regression fails" 1
expect_line "the regressed package is named" \
	"example.com/pkg/b: coverage 48.5% is below baseline 50.0% (-1.0 allowed)"

# A package the baseline has never seen is noted but does not fail the run.
stage
cp "$fixtures/newpkg.txt" "$work/repo/coverage.txt"
run coverage.txt
expect_exit "a new package does not fail" 0
expect_line "the new package is named" "example.com/pkg/c: not in baseline (new package?)"

# A missing FILE fails with the documented message, telling the human to run
# make check first.
stage
run coverage.txt
expect_exit "a missing coverage file fails" 1
expect_line "the missing-file message is printed" \
	"check-coverage: no coverage output (run make check)"

[ "$fail" -eq 0 ] && echo "check-coverage: ok"
exit "$fail"
