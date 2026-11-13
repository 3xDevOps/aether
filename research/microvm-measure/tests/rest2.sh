#!/usr/bin/env bash
# Re-runs Q5 and Q7 after their fixes, once rest.sh has finished.
set -uo pipefail
A1=/srv/aether/1
M=$A1/m
export BASE=$A1/image/base2.raw KERNEL=/srv/aether/shared/item4/vmlinux
step() { echo "$(date -u +%T) $*"; "$@" >/dev/null 2>&1; echo "$(date -u +%T) exit $?"; }
while pgrep -f "[r]est.sh" >/dev/null; do sleep 20; done
step env LABEL=k4 "$M/tests/q5-disks.sh"
step env LABEL=k4-cache-always PARTS=git FS_CACHE=always "$M/tests/q5-disks.sh"
step env LABEL=k4 "$M/tests/q7-fusevsock.sh"
