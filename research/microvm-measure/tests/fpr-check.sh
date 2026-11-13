#!/usr/bin/env bash
# Does free page reporting return guest memory to the host? Boots VM 1,
# fills 1200 MiB of guest anonymous memory, frees it, and samples the
# monitor's host memory. SHARED=on|off, HOME_FS=0|1 pass through.
set -euo pipefail
M=/srv/aether/1/m
sudo rm -rf /srv/aether/1/vms/vm1 && mkdir -p /srv/aether/1/vms/vm1/home
"$M/host/vm.sh" start 1
host() { awk '/^(MemAvailable|Shmem):/{printf "host_%s=%s ", $1, $2}' /proc/meminfo; }
trap '"$M/host/vm.sh" stop 1; sudo rm -rf /srv/aether/1/vms/vm1' EXIT
echo "booted:    $("$M/host/vm.sh" mem 1) $(host)"
"$M/host/vm.sh" ssh 1 "python3 -c 'b = bytearray(1200 << 20); [b.__setitem__(i, 1) for i in range(0, len(b), 4096)]; import time; time.sleep(3)' &
sleep 2"
echo "filled:    $("$M/host/vm.sh" mem 1) $(host)"
sleep 5
echo "freed +3s: $("$M/host/vm.sh" mem 1) $(host)"
sleep 10
echo "freed +13s: $("$M/host/vm.sh" mem 1) $(host)"
"$M/host/vm.sh" ssh 1 "cat /proc/vmstat | grep -E 'balloon|page_report' ; free -m | sed -n 2p"
sudo "/usr/local/bin/ch-remote" --api-socket /run/a1/vm1/api.sock balloon-stats 2>&1 | head -c 600; echo
