#!/usr/bin/env bash
# Does page cache freed in the guest go back to the host? Boots VM 1 with
# CMDLINE_EXTRA (e.g. page_reporting.page_reporting_order=0), runs npm
# install of tests/package.json, drops the guest page cache, then samples
# the monitor's host memory for 60 s and prints the guest's free-block
# orders.
set -euo pipefail
M=/srv/aether/1/m
V=${V:-1}
sudo rm -rf /srv/aether/1/vms/vm$V && mkdir -p /srv/aether/1/vms/vm$V && sudo mkdir -p /srv/aether/1/vms/vm$V/home
"$M/host/vm.sh" start $V
trap '"$M/host/vm.sh" stop $V; sudo rm -rf /srv/aether/1/vms/vm$V' EXIT
g() { "$M/host/vm.sh" ssh $V "$@"; }
g "cat /sys/module/page_reporting/parameters/page_reporting_order 2>/dev/null | sed 's/^/page_reporting_order=/'; mkdir -p /workspace/app && cat >/workspace/app/package.json" <"$M/tests/package.json"
g "cd /workspace/app && npm install --no-audit --no-fund >/dev/null 2>&1 && du -sh node_modules"
echo "after npm:   $("$M/host/vm.sh" mem $V)"
g "sync; echo 3 >/proc/sys/vm/drop_caches; free -m | sed -n 2p"
for s in 5 20 60; do
  sleep $((s - ${last:-0})); last=$s
  echo "dropped +${s}s: $("$M/host/vm.sh" mem $V)"
done
g "cat /proc/buddyinfo"
