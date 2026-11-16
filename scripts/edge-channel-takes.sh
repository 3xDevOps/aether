#!/bin/sh
# Exits 0 when aether-edge:$1 may carry a release whose signed manifest
# names channel $2 (empty for a release from before channels). stable takes
# a stable or, until the first channel stable, a pre-channel release; beta
# takes anything but a nightly; nightly takes only a nightly, because it
# moves with nightly.json, which never names a beta or stable.
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: edge-channel-takes.sh <nightly|beta|stable> <release channel>" >&2
	exit 2
fi
case $1:$2 in
stable:stable | stable: | beta:beta | beta:stable | beta: | nightly:nightly) ;;
*)
	echo "::error::$TAG is a ${2:-pre-channel} release, which aether-edge:$1 does not take" >&2
	exit 1
	;;
esac
