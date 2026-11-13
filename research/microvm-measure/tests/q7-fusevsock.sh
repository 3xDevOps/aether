#!/usr/bin/env bash
# Q7: cost of a guest FUSE filesystem forwarding by path to the host over
# vsock (fusevsock), the design chosen for the home after Q3. Runs on the
# KVM host after q5 (uses $A1/q5/tree.tar, the 40,000-file tree). VM 5
# measures raw vsock and virtio-net round trips, then walks, stats and
# reads the tree on its ext4 root disk, through a local passthrough FUSE,
# and through fusevsock. Cold runs drop the guest page cache first.
set -euo pipefail
A1=/srv/aether/1
M=$A1/m
V=${V:-25}
R=/run/a1/vm$V
OUT=$A1/results/q7-${LABEL:-default}
rm -rf "$OUT" && mkdir -p "$OUT"
exec > >(tee "$OUT/log.txt") 2>&1
g() { "$M/host/vm.sh" ssh $V "$@"; }

sudo rm -rf "$A1/q7" && mkdir -p "$A1/q7/tree" && tar -C "$A1/q7/tree" -xf "$A1/q5/tree.tar"
trap '"$M/host/vm.sh" stop $V; sudo systemctl stop a1-fusevsock$V 2>/dev/null || true' EXIT
sudo rm -rf "$A1/vms/vm$V" && mkdir -p "$A1/vms/vm$V/home"
MEM=2G CPUS=${CPUS:-2} HOME_FS=0 "$M/host/vm.sh" start $V
sudo systemd-run -q --unit a1-fusevsock$V --slice a1.slice --collect -p Type=simple \
  "$A1/bin/fusevsock" serve "$A1/q7/tree" "$R/vsock.sock_5000" 10.201.0.1:5001
sleep 0.5
g "cat >/usr/local/bin/fusevsock && chmod +x /usr/local/bin/fusevsock" <"$A1/bin/fusevsock"
g "mkdir -p /var/tmp/tree /mnt/lb /mnt/fv && tar -C /var/tmp/tree -xf -" <"$A1/q5/tree.tar"

for size in 64 4096 131072; do
  g "fusevsock rtt vsock:5000 5000 $size"
  g "fusevsock rtt tcp:10.201.0.1:5001 5000 $size"
done

g "nohup fusevsock loopback /mnt/lb /var/tmp/tree </dev/null >/tmp/lb.log 2>&1 &
nohup fusevsock mount /mnt/fv 5000 </dev/null >/tmp/fv.log 2>&1 &
for _ in \$(seq 50); do mountpoint -q /mnt/lb && mountpoint -q /mnt/fv && break; sleep 0.1; done; mount | grep fuse"

walk() { # dir
  g "cd $1; t() { s=\$(date +%s%N); \"\$@\" >/dev/null 2>&1; echo \$(( (\$(date +%s%N) - s) / 1000000 )); }
    sync; echo 3 >/proc/sys/vm/drop_caches
    a=\$(t find . -type f); b=\$(t find . -type f -printf '%s %T@\n'); sleep 2; c=\$(t find . -type f -printf '%s %T@\n')
    echo 3 >/proc/sys/vm/drop_caches; d=\$(t sh -c 'find . -type f -print0 | xargs -0 cat')
    e=\$(t sh -c 'find . -type f -print0 | xargs -0 cat')
    echo \"$1: walk cold \$a ms, stat all \$b ms, stat all after 2 s (attr TTL 1 s) \$c ms, read all cold \$d ms, read all again \$e ms\""
}
walk /var/tmp/tree
walk /mnt/lb
walk /mnt/fv
echo "server CPU: $(ps -o times= -p "$(systemctl show -p MainPID --value a1-fusevsock$V)")s"
