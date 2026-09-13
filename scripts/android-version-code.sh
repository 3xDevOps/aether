#!/bin/sh
# The Android versionCode for a release tag.
#
#     sh scripts/android-version-code.sh v0.4.0-alpha.6
#
# Android installs an update only when its versionCode is above the installed
# one, and the number is the only ordering it reads: versionName is a label.
# A commit count is not an ordering across branches - a hotfix tagged off a
# branch with fewer reachable commits scores below the release it fixes, and
# the correctly signed APK is refused as a downgrade - so the number comes
# from the tag:
#
#     MAJOR * 10000000 + MINOR * 100000 + PATCH * 1000 + rank
#
# where rank orders the pre-releases of one version below its final release:
# alpha.N is 100 + N, beta.N is 300 + N, rc.N is 500 + N, and a final release
# is 999. So v0.4.0-alpha.6 is 400106, v0.4.0-rc.1 is 400501, v0.4.0 is
# 400999, and v0.4.1-alpha.1 is 401101.
#
# Android's versionCode maximum is 2100000000, which is what the ceilings
# below hold the number under: major 209, minor 99, patch 99, and a
# pre-release number of 199, above which alpha.N would reach beta's rank.
set -eu

MAX_MAJOR=209
MAX_MINOR=99
MAX_PATCH=99
MAX_PRERELEASE=199

if [ "$#" -ne 1 ]; then
	echo "usage: android-version-code.sh <tag>" >&2
	exit 2
fi

fail() {
	echo "android-version-code: $1" >&2
	exit 1
}

# A decimal number with no leading zero, which is what semantic versioning
# allows. Bounded at nine digits so every comparison below stays inside what
# `[ x -le y ]` can hold: one digit more than the largest ceiling has.
number() {
	case $1 in
	'' | *[!0-9]*) return 1 ;;
	0 | [1-9]*) [ "${#1}" -le 9 ] ;;
	*) return 1 ;;
	esac
}

tag=${1#v}
core=${tag%%-*}
prerelease=""
case $tag in
*-*) prerelease=${tag#*-} ;;
esac

old_ifs=$IFS
IFS=.
# Unquoted on purpose: this is the split on IFS.
# shellcheck disable=SC2086
set -- $core
IFS=$old_ifs
[ "$#" -eq 3 ] || fail "not a release tag: \"$tag\" is not MAJOR.MINOR.PATCH"
major=$1
minor=$2
patch=$3

for part in "$major" "$minor" "$patch"; do
	number "$part" || fail "not a release tag: \"$part\" is not a version number"
done
[ "$major" -le "$MAX_MAJOR" ] || fail "major version $major is above $MAX_MAJOR"
[ "$minor" -le "$MAX_MINOR" ] || fail "minor version $minor is above $MAX_MINOR"
[ "$patch" -le "$MAX_PATCH" ] || fail "patch version $patch is above $MAX_PATCH"

if [ -z "$prerelease" ]; then
	rank=999
else
	case $prerelease in
	alpha.*) base=100 count=${prerelease#alpha.} ;;
	beta.*) base=300 count=${prerelease#beta.} ;;
	rc.*) base=500 count=${prerelease#rc.} ;;
	*) fail "not a release tag: \"$prerelease\" is not alpha.N, beta.N or rc.N" ;;
	esac
	number "$count" || fail "not a release tag: \"$count\" is not a pre-release number"
	[ "$count" -le "$MAX_PRERELEASE" ] || fail "pre-release number $count is above $MAX_PRERELEASE"
	rank=$((base + count))
fi

echo $((major * 10000000 + minor * 100000 + patch * 1000 + rank))
