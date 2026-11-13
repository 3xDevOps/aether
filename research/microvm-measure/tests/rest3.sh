#!/usr/bin/env bash
# Re-runs the Q5 git part with automatic gc off, once rest2.sh has finished.
set -uo pipefail
A1=/srv/aether/1
M=$A1/m
export BASE=$A1/image/base2.raw KERNEL=/srv/aether/shared/item4/vmlinux
step() { echo "$(date -u +%T) $*"; "$@" >/dev/null 2>&1; echo "$(date -u +%T) exit $?"; }
while pgrep -f "[r]est2.sh" >/dev/null; do sleep 20; done
step env LABEL=k4-git PARTS=git "$M/tests/q5-disks.sh"
step env LABEL=k4-git-cache-always PARTS=git FS_CACHE=always "$M/tests/q5-disks.sh"
step env LABEL=k4-io-direct-off PARTS=io DISK_OPTS=direct=off "$M/tests/q5-disks.sh"
