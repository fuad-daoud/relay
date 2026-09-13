#!/bin/sh
set -eu

# Asserts the checked-in systemd unit template's [Service] keys (spec
# 2026-09-13-headless-recovery §4.1). Headless builders are children of the
# daemon and share its cgroup, so the unit must not cap memory, and an
# OOM-killed builder must not take the daemon down with it.
#
# Usage: relay-service-template_test.sh [path-to-template]
# Defaults to the repo's dist/relay.service.

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
template=${1:-"$here/../dist/relay.service"}

fail=0
if [ ! -f "$template" ]; then
	echo "FAIL: no template at $template"; exit 1
fi
if grep -q '^MemoryMax=' "$template"; then
	echo "FAIL: $template caps memory (MemoryMax); headless builders share the unit's cgroup"; fail=1
fi
if [ "$(grep -c '^OOMPolicy=continue$' "$template")" -ne 1 ]; then
	echo "FAIL: $template must set OOMPolicy=continue exactly once"; fail=1
fi
if grep -q '^OOMPolicy=' "$template" && ! grep -q '^OOMPolicy=continue$' "$template"; then
	echo "FAIL: $template sets an OOMPolicy other than continue"; fail=1
fi

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "ok: $template has no MemoryMax and OOMPolicy=continue"
