#!/usr/bin/env bash
# Q1 with kernel same-page merging: N Claude Code VMs with memory
# shared=off,mergeable=on and host KSM on for the run, then unmerged and
# off. The summary's ksm_saved_avg is the memory merged away per VM; ksmd's
# CPU time over the run is printed at the end.
set -euo pipefail
M=/srv/aether/1/m
ksm=/sys/kernel/mm/ksm
echo "${KSM_PAGES:-2000}" | sudo tee $ksm/pages_to_scan >/dev/null
echo 20 | sudo tee $ksm/sleep_millisecs >/dev/null
echo 1 | sudo tee $ksm/run >/dev/null
trap 'echo 2 | sudo tee $ksm/run >/dev/null; echo 0 | sudo tee $ksm/run >/dev/null' EXIT
t0=$(awk '{print $14 + $15}' "/proc/$(pgrep -x ksmd)/stat")
KIND=vm AGENT=claude N=${N:-10} SHARED=off MERGEABLE=on HOME_FS=0 LABEL=vm-claude-${N:-10}-ksm \
  "$M/tests/q1-memory.sh"
t1=$(awk '{print $14 + $15}' "/proc/$(pgrep -x ksmd)/stat")
echo "ksmd CPU during the run: $(((t1 - t0) / $(getconf CLK_TCK))) s"
