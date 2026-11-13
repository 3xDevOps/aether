#!/usr/bin/env bash
# Q5: copy-on-write disks and git on the VM disk versus virtio-fs. Runs on
# the KVM host. Parts (PARTS, default "create io git"):
#   create  time and space for a qcow2 overlay over a 4 GiB raw base versus
#           an XFS reflink copy of it (10 each), and one full copy
#   io      fio in VM 6 on a fresh overlay, a fresh reflink copy and a full
#           copy, attached as extra disks with DISK_OPTS (default direct=on)
#   tree    only generate the 40,000-file tree ($Q/tree.tar)
#   git     git add -A / commit / status on a 40,000-file tree on the VM's
#           ext4 data disk, over virtio-fs (FS_CACHE, default auto), and on
#           the host's XFS directly
set -euo pipefail
A1=/srv/aether/1
M=$A1/m
Q=$A1/q5
V=${V:-26}
OUT=$A1/results/q5-${LABEL:-default}
mkdir -p "$Q" "$OUT"
exec > >(tee -a "$OUT/log.txt") 2>&1
PARTS=${PARTS:-create io git}
g() { "$M/host/vm.sh" ssh $V "$@"; }
ms() { local s; s=$(date +%s%N); "$@" >/dev/null; echo $((($(date +%s%N) - s) / 1000000)); }

[ -f "$Q/base4g.raw" ] || nice dd if=/dev/urandom of="$Q/base4g.raw" bs=4M count=1024 status=none
trap '"$M/host/vm.sh" stop $V' EXIT

if [[ " $PARTS " == *" create "* ]]; then
  rm -f "$Q"/ov*.qcow2 "$Q"/rl*.raw "$Q/full.raw"
  sync
  q=() r=()
  for i in $(seq 10); do
    q+=("$(ms qemu-img create -q -f qcow2 -b "$Q/base4g.raw" -F raw "$Q/ov$i.qcow2")")
    r+=("$(ms cp --reflink=always "$Q/base4g.raw" "$Q/rl$i.raw")")
  done
  echo "qcow2 overlay create ms: ${q[*]}; on disk $(du -k "$Q/ov1.qcow2" | cut -f1) KiB"
  echo "reflink copy create ms:  ${r[*]}; on disk (shared extents) $(du -k "$Q/rl1.raw" | cut -f1) KiB, fs used delta for 10 copies: see df"
  df -k /srv | tail -n 1
  echo "full copy create ms: $(ms cp --reflink=never "$Q/base4g.raw" "$Q/full.raw"); sync after it: $(ms sync) ms"
  df -k /srv | tail -n 1
  rm -f "$Q"/ov[2-9].qcow2 "$Q"/ov10.qcow2 "$Q"/rl[2-9].raw "$Q"/rl10.raw
fi

fio_run() { # dev name rw bs depth
  g "fio --name=$2 --filename=$1 --rw=$3 --bs=$4 --iodepth=$5 --ioengine=libaio --direct=1 --size=2G --runtime=20 --time_based=0 --output-format=json" |
    jq -r --arg n "$2" '.jobs[0] | "\($n): read \(.read.iops | floor) IOPS \(.read.bw / 1024 | floor) MiB/s, write \(.write.iops | floor) IOPS \(.write.bw / 1024 | floor) MiB/s, clat p99 \((.write.clat_ns.percentile["99.000000"] // .read.clat_ns.percentile["99.000000"]) / 1000 | floor) us"'
}

if [[ " $PARTS " == *" io "* ]]; then
  rm -f "$Q/ov1.qcow2" "$Q/rl1.raw"
  qemu-img create -q -f qcow2 -b "$Q/base4g.raw" -F raw "$Q/ov1.qcow2"
  cp --reflink=always "$Q/base4g.raw" "$Q/rl1.raw"
  [ -f "$Q/full.raw" ] || cp --reflink=never "$Q/base4g.raw" "$Q/full.raw"
  sudo rm -rf "$A1/vms/vm$V" && mkdir -p "$A1/vms/vm$V/home"
  MEM=2G HOME_FS=0 DISKS="$Q/ov1.qcow2 $Q/rl1.raw $Q/full.raw" DISK_OPTS=${DISK_OPTS:-direct=on} "$M/host/vm.sh" start $V
  g 'for d in /sys/block/vd*; do echo "$(basename $d) $(cat $d/serial 2>/dev/null) $(($(cat $d/size) / 2048)) MiB"; done'
  for dev in vdb:qcow2-overlay vdc:reflink-copy vdd:full-copy; do
    d=/dev/${dev%%:*} n=${dev#*:}
    fio_run "$d" "$n first randwrite 4k qd32" randwrite 4k 32
    fio_run "$d" "$n again randwrite 4k qd32" randwrite 4k 32
    fio_run "$d" "$n randread 4k qd32" randread 4k 32
    fio_run "$d" "$n seqwrite 1M qd8" write 1M 8
    fio_run "$d" "$n seqread 1M qd8" read 1M 8
  done
  "$M/host/vm.sh" stop $V
  echo "after io: overlay $(du -h "$Q/ov1.qcow2" | cut -f1), reflink copy unshared extents: $(xfs_bmap -v "$Q/rl1.raw" 2>/dev/null | wc -l) extent lines"
fi

git_run() { # vm|host dir; the host run does not drop the host page cache
  local run=g drop="sync; echo 3 >/proc/sys/vm/drop_caches"
  [ "$1" = host ] && run="bash -c" drop=true
  $run "set -e; cd $2; t() { s=\$(date +%s%N); \"\$@\" >/dev/null; echo \$(( (\$(date +%s%N) - s) / 1000000 )); }
    export GIT_CONFIG_GLOBAL=/tmp/a1-gitconfig
    git config --global user.email a1@example.invalid; git config --global user.name a1; git config --global init.defaultBranch main
    git init -q .
    add=\$(t git add -A); commit=\$(t git commit -q -m init)
    s1=\$(t git status); s2=\$(t git status); s3=\$(t git status)
    $drop; cold=\$(t git status)
    for f in \$(git ls-files | awk 'NR % 40 == 0'); do echo x >>\$f; done
    m=\$(t git status); madd=\$(t git add -A)
    echo \"files \$(git ls-files | wc -l): add -A \${add} ms, commit \${commit} ms, status warm \${s1}/\${s2}/\${s3} ms, status cold \${cold} ms, status after 1000 edits \${m} ms, add -A after edits \${madd} ms\""
}

make_tree() {
  [ -f "$Q/tree.tar" ] && return
  rm -rf "$Q/tree" && mkdir -p "$Q/tree"
  python3 -I -c '
import os, random, base64, sys
random.seed(1)
root = sys.argv[1]
for d in range(400):
    p = os.path.join(root, f"pkg{d // 20}", f"mod{d}")
    os.makedirs(p)
    for f in range(100):
        n = random.randint(512, 8192)
        with open(os.path.join(p, f"file{f}.txt"), "w") as fh:
            fh.write(base64.b64encode(random.randbytes(n * 3 // 4)).decode())
' "$Q/tree"
  tar -C "$Q/tree" -cf "$Q/tree.tar" .
  rm -rf "$Q/tree"
}

[[ " $PARTS " == *" tree "* ]] && make_tree

if [[ " $PARTS " == *" git "* ]]; then
  make_tree
  rm -f "$Q/data.raw" && truncate -s 8G "$Q/data.raw" && mkfs.ext4 -q -F "$Q/data.raw"
  sudo rm -rf "$Q/share" "$Q/native" && sudo mkdir -p "$Q/share/repo" "$Q/native/repo"
  sudo tar -C "$Q/share/repo" -xf "$Q/tree.tar"
  sudo tar -C "$Q/native/repo" -xf "$Q/tree.tar"
  sudo rm -rf "$A1/vms/vm$V" && mkdir -p "$A1/vms/vm$V/home"
  MEM=2G CPUS=${CPUS:-2} HOME_FS=0 DISKS="$Q/data.raw" DATA=/dev/vdb:/data FS="work=$Q/share" "$M/host/vm.sh" start $V
  g "mkdir -p /data/repo && tar -C /data/repo -xf -" <"$Q/tree.tar"
  echo "VM ext4 data disk (virtio-blk raw): $(git_run vm /data/repo)"
  echo "virtio-fs cache=${FS_CACHE:-auto}: $(git_run vm /mnt/work/repo)"
  echo "host XFS native: $(sudo -H bash -c "$(declare -f git_run); git_run host $Q/native/repo")"
  "$M/host/vm.sh" stop $V
fi
