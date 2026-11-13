#!/usr/bin/env bash
# Runs the Q8 configs kind:agent:n:tuned:warm in sequence on item 4's kernel.
set -uo pipefail
A1=/srv/aether/1
M=$A1/m
export BASE=$A1/image/base2.raw KERNEL=/srv/aether/shared/item4/vmlinux
CONFIGS=${CONFIGS:-vm:claude:3:0:0 vm:claude:3:1:0 ctr:claude:3:0:0 vm:codex:3:0:0 vm:codex:3:1:0 vm:codex:3:1:1 ctr:codex:3:0:0 ctr:codex:3:0:1 vm:none:10:1:0}
for cfg in $CONFIGS; do
  IFS=: read -r kind agent n tuned warm <<<"$cfg"
  KIND=$kind AGENT=$agent N=$n TUNED=$tuned WARM=$warm "$M/tests/q8-enhanced.sh" >/dev/null 2>&1
  echo "$(date -u +%T) $cfg exit $?"
done
