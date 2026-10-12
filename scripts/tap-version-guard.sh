#!/usr/bin/env bash
# Refuses a tap update that would not move the formula forward (REL-03: upgrading never
# downgrades). Exit 0 when NEW is a bare X.Y.Z release that is strictly newer than CURRENT,
# or CURRENT is empty (no formula yet); exit 1 otherwise: equal, older, a prerelease or not
# a version at all. A leading "v" on either argument is dropped.
#
#   scripts/tap-version-guard.sh CURRENT NEW
set -euo pipefail

if [ "$#" -ne 2 ]; then
	echo "usage: tap-version-guard.sh CURRENT NEW" >&2
	exit 2
fi
current="${1#v}"
new="${2#v}"

if ! [[ "$new" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "tap-version-guard.sh: ${new} is not a bare X.Y.Z release version" >&2
	exit 1
fi
if [ -z "$current" ]; then
	exit 0
fi
# sort -V orders bare X.Y.Z numerically; anything else in CURRENT makes the order unknown.
if ! [[ "$current" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "tap-version-guard.sh: the tap's ${current} is not a bare X.Y.Z version" >&2
	exit 1
fi
if [ "$current" = "$new" ]; then
	echo "tap-version-guard.sh: the tap already has ${new}" >&2
	exit 1
fi
highest="$(printf '%s\n%s\n' "$current" "$new" | sort -V | tail -n 1)"
if [ "$highest" != "$new" ]; then
	echo "tap-version-guard.sh: ${new} is not newer than the tap's ${current}" >&2
	exit 1
fi
