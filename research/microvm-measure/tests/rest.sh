#!/usr/bin/env bash
# Runs the measurements after the Q1 and Q2 matrices, in sequence so they do
# not skew each other, on item 4's kernel unless a line says otherwise.
set -uo pipefail
A1=/srv/aether/1
M=$A1/m
export BASE=$A1/image/base2.raw KERNEL=/srv/aether/shared/item4/vmlinux
step() { echo "$(date -u +%T) $*"; "$@" >/dev/null 2>&1; echo "$(date -u +%T) exit $?"; }
step env LABEL=k4 "$M/tests/q5-disks.sh"
step env LABEL=k4-cache-always PARTS=git FS_CACHE=always "$M/tests/q5-disks.sh"
step env LABEL=k4 "$M/tests/q7-fusevsock.sh"
step env KIND=vm N=10 AGENT=claude LABEL=vm-claude-10-k4 "$M/tests/q6-idle-cpu.sh"
step env KIND=vm N=10 AGENT=none LABEL=vm-none-10-k4 "$M/tests/q6-idle-cpu.sh"
step env KIND=vm N=10 AGENT=claude LABEL=vm-claude-10-mine KERNEL=$A1/kernel/out/vmlinux "$M/tests/q6-idle-cpu.sh"
step env KIND=ctr N=10 AGENT=claude LABEL=ctr-claude-10 "$M/tests/q6-idle-cpu.sh"
step env KIND=vm AGENT=claude N=1 HOME_FS=0 LABEL=vm-claude-1-k4 "$M/tests/q1-memory.sh"
step env KIND=vm AGENT=claude N=10 HOME_FS=0 LABEL=vm-claude-10-concurrent "$M/tests/q1-memory.sh"
step env KIND=ctr AGENT=claude N=1 "$M/tests/q1-memory.sh"
step env KIND=ctr AGENT=codex N=1 "$M/tests/q1-memory.sh"
step env KIND=ctr AGENT=claude N=10 LABEL=ctr-claude-10-concurrent "$M/tests/q1-memory.sh"
step env N=10 "$M/tests/q1-ksm.sh"
step env MEM=2G SHARED=on FILL=0 RECLAIM=0 HOME_FS=0 LABEL=k4-mem2G-sharedon "$M/tests/q2-save-restore.sh"
