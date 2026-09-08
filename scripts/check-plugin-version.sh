#!/bin/sh
set -eu

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
