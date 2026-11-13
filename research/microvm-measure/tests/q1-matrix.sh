#!/usr/bin/env bash
# Runs the Q1 matrix in sequence: VMs (memory shared=off, no virtio-fs,
# page_reporting_order=0) and plain containers, Claude Code at 1, 5 and 10
# sessions and Codex at 1.
set -uo pipefail
M=/srv/aether/1/m
for kind in vm ctr; do
  for run in claude:1 codex:1 claude:5 claude:10; do
    KIND=$kind AGENT=${run%%:*} N=${run#*:} HOME_FS=0 "$M/tests/q1-memory.sh" >/dev/null 2>&1
    echo "$kind ${run%%:*} ${run#*:} exit $?"
  done
done
