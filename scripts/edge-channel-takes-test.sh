#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

fail() {
	echo "FAIL: $1" >&2
	exit 1
}

takes() {
	TAG=v1.0.0 sh "$script_dir/edge-channel-takes.sh" "$1" "$2" 2>/dev/null
}

for pair in stable:stable stable: beta:beta beta:stable beta: nightly:nightly; do
	takes "${pair%%:*}" "${pair#*:}" || fail "aether-edge:${pair%%:*} refused a ${pair#*:} release"
done
for pair in stable:beta stable:nightly beta:nightly nightly:beta nightly:stable nightly: latest:stable; do
	if takes "${pair%%:*}" "${pair#*:}"; then fail "aether-edge:${pair%%:*} took a ${pair#*:} release"; fi
done
out=$(TAG=v0.9.0-beta.1 sh "$script_dir/edge-channel-takes.sh" nightly beta 2>&1) && fail "nightly took a beta"
[ "$out" = "::error::v0.9.0-beta.1 is a beta release, which aether-edge:nightly does not take" ] || fail "message: $out"

echo "edge-channel-takes tests passed"
