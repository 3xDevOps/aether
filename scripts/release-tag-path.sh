#!/bin/sh
# Prints the path release.yml takes for the pushed tag $1: hotfix for a
# stable vX.Y.Z with Z at least 1, which must be on release/X.Y, or main for
# every other tag, which releases from main without a channel. A -nightly.N
# tag is refused: only nightly.yml releases nightlies. Each path's own
# checks then refuse a tag it cannot release.
set -eu

[ "$#" -eq 1 ] || { echo "usage: release-tag-path.sh <tag>" >&2; exit 2; }
case $1 in
*-nightly.*)
	echo "release-tag-path: $1 is a nightly tag; only nightly.yml releases nightlies, and a pushed one is refused" >&2
	exit 1
	;;
*-* | *.0) echo main ;;
*) echo hotfix ;;
esac
