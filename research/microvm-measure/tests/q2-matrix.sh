#!/usr/bin/env bash
# Runs Q2 configs mem:shared:fill:reclaim in sequence (no virtio-fs).
set -uo pipefail
M=/srv/aether/1/m
CONFIGS=${CONFIGS:-1G:off:0:0 2G:off:0:0 1G:off:1:0 2G:off:1:0 2G:on:0:0}
for cfg in $CONFIGS; do
  IFS=: read -r mem shared fill reclaim <<<"$cfg"
  MEM=$mem SHARED=$shared FILL=$fill RECLAIM=$reclaim HOME_FS=0 "$M/tests/q2-save-restore.sh" >/dev/null 2>&1
  echo "$cfg exit $?"
done
