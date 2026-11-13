#!/bin/bash
# PID 1 of a measurement VM. Stands in for item 3's helper: mounts,
# network from the kernel command line, optional shares and dockerd, sshd.
# Command line keys: a1.ip=<cidr> a1.gw=<ip> a1.fs=<tag>:<dir>[,...]
# a1.data=<dev>:<dir> a1.docker=1
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc
mount -t sysfs sys /sys
mountpoint -q /dev || mount -t devtmpfs dev /dev
mkdir -p /dev/pts /dev/shm
mount -t devpts -o gid=5,mode=620,ptmxmode=666 devpts /dev/pts
mount -t tmpfs tmpfs /dev/shm
mount -t tmpfs tmpfs /run
mount -t tmpfs tmpfs /tmp
mount -t cgroup2 cgroup2 /sys/fs/cgroup
ln -sf /proc/self/fd /dev/fd

arg() { tr ' ' '\n' </proc/cmdline | sed -n "s/^$1=//p" | tail -1; }

hostname "$(arg a1.name)"
ip link set lo up
ip addr add "$(arg a1.ip)" dev eth0
ip link set eth0 up
ip route add default via "$(arg a1.gw)"
echo "nameserver 1.1.1.1" >/etc/resolv.conf
printf '127.0.0.1 localhost %s\n::1 localhost\n' "$(arg a1.name)" >/etc/hosts

IFS=, read -ra shares <<<"$(arg a1.fs)"
for s in "${shares[@]}"; do
  mkdir -p "${s#*:}"
  mount -t virtiofs "${s%%:*}" "${s#*:}"
done
data=$(arg a1.data)
if [ -n "$data" ]; then
  mkdir -p "${data#*:}"
  mount "${data%%:*}" "${data#*:}"
fi
if [ "$(arg a1.docker)" = 1 ]; then
  dockerd >/var/log/dockerd.log 2>&1 &
fi

mkdir -p /run/sshd
/usr/sbin/sshd -E /var/log/sshd.log
echo "a1-init ready" >/dev/hvc0

trap '' TERM
while :; do wait -n 2>/dev/null || sleep 1; done
