#!/usr/bin/env bash
# Refuses a tap update that would not move the formula forward (REL-03: upgrading never
# downgrades). Exit 0 when NEW is a canonical X.Y.Z release (no leading zeros, no
# prerelease) that is strictly newer than CURRENT, or CURRENT is empty (no formula yet);
# exit 1 otherwise: equal, older, a prerelease or not a canonical version. A leading "v" on
# either argument is dropped.
#
# CURRENT must be canonical X.Y.Z too. The tap is only ever given releases, so a prerelease
# or an unreadable version there is an anomaly; its order is never guessed, it is refused.
#
#   scripts/tap-version-guard.sh CURRENT NEW
set -euo pipefail

if [ "$#" -ne 2 ]; then
	echo "usage: tap-version-guard.sh CURRENT NEW" >&2
	exit 2
fi
current="${1#v}"
new="${2#v}"

# SemVer's numeric identifiers: 0, or digits without a leading zero.
num='(0|[1-9][0-9]*)'
release="^${num}\.${num}\.${num}\$"

if ! [[ "$new" =~ $release ]]; then
	echo "tap-version-guard.sh: ${new} is not a canonical X.Y.Z release version" >&2
	exit 1
fi
if [ -z "$current" ]; then
	exit 0
fi
if [[ "$current" =~ ^${num}\.${num}\.${num}-. ]]; then
	echo "tap-version-guard.sh: the tap's ${current} is a prerelease, which the tap never holds; refusing to order ${new} against it" >&2
	exit 1
fi
if ! [[ "$current" =~ $release ]]; then
	echo "tap-version-guard.sh: the tap's ${current} is not a canonical X.Y.Z version" >&2
	exit 1
fi
if [ "$current" = "$new" ]; then
	echo "tap-version-guard.sh: the tap already has ${new}" >&2
	exit 1
fi

# cmp_num A B prints -1, 0 or 1. Canonical numbers have no leading zeros, so the longer one
# is larger and equal lengths order as strings; no component is ever parsed as an integer,
# so none can overflow.
cmp_num() {
	if [ "${#1}" -lt "${#2}" ]; then
		echo -1
	elif [ "${#1}" -gt "${#2}" ]; then
		echo 1
	elif [ "$1" = "$2" ]; then
		echo 0
	elif [[ "$1" < "$2" ]]; then
		echo -1
	else
		echo 1
	fi
}

IFS=. read -r c1 c2 c3 <<<"$current"
IFS=. read -r n1 n2 n3 <<<"$new"
order=0
for pair in "$n1 $c1" "$n2 $c2" "$n3 $c3"; do
	# shellcheck disable=SC2086 # the pair is two digit strings, split on purpose
	order="$(cmp_num $pair)"
	[ "$order" = 0 ] || break
done
if [ "$order" != 1 ]; then
	echo "tap-version-guard.sh: ${new} is not newer than the tap's ${current}" >&2
	exit 1
fi
