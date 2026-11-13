#!/usr/bin/env bash
# Q3: save and restore with virtio-fs shares attached. Runs on the KVM host.
# VM 8 gets the home share and a "work" share at /mnt/work. Before the
# save, a guest process holds a share file open and appends to it every
# second, and a shell sits with its cwd in the share. While the VM is
# saved, the host edits and creates files in the share. After the restore
# every check prints ok or FAIL with the error.
set -euo pipefail
A1=/srv/aether/1
M=$A1/m
V=${V:-28}
D=$A1/vms/vm$V
W=$D/work
SNAP=$A1/snap/q3
OUT=$A1/results/q3-${LABEL:-default}
rm -rf "$OUT" && mkdir -p "$OUT"
exec > >(tee "$OUT/log.txt") 2>&1
export MEM=${MEM:-1G} FS="work=$W"
g() { "$M/host/vm.sh" ssh $V "$@"; }
check() { # name guest-command
  local out
  if out=$(g "set -o pipefail; $2" 2>&1); then echo "ok   $1: $out"; else echo "FAIL $1: $out"; fi
}

trap '"$M/host/vm.sh" stop $V' EXIT
"$M/host/vm.sh" stop $V
sudo rm -rf "$D" && mkdir -p "$D" && sudo mkdir -p "$D/home" "$W"
sudo sh -c "echo host-before >'$W/host-edit.txt'"
"$M/host/vm.sh" start $V
g 'echo guest-before >/mnt/work/guest.txt
mkdir -p /mnt/work/dir && for i in $(seq 100); do echo $i >/mnt/work/dir/f$i; done
nohup python3 -c "
import time
f = open(\"/mnt/work/held.log\", \"a\", buffering=1)
i = 0
while True:
    f.write(f\"{i}\\n\"); i += 1; time.sleep(1)
" >/dev/null 2>&1 &
echo $! >/run/held.pid
tmux new-session -d -s cwd -c /mnt/work/dir "bash"
cat /mnt/work/host-edit.txt'
sleep 3
g 'wc -l </mnt/work/held.log' | sed 's/^/held.log lines before save: /'

sudo rm -rf "$SNAP" && sudo mkdir -p "$SNAP"
sudo ch-remote --api-socket /run/a1/vm$V/api.sock pause
if ! sudo ch-remote --api-socket /run/a1/vm$V/api.sock snapshot "file://$SNAP" 2>&1; then
  echo "FAIL snapshot with virtio-fs attached (log below)"
  sudo tail -n 20 "$D/ch.log"
  exit 0
fi
echo "ok   snapshot with virtio-fs attached: $(sudo du -sh "$SNAP" | cut -f1)"
"$M/host/vm.sh" stop $V
sudo sh -c "echo host-during >'$W/host-edit.txt'; echo host-new >'$W/host-new.txt'; rm '$W/dir/f50'"

RESTORE_MODE=${RESTORE_MODE:-copy} "$M/host/vm.sh" restore $V "$SNAP"
if ! "$M/host/vm.sh" wait-ssh $V; then
  echo "FAIL restore: no ssh"
  sudo tail -n 20 "$D/ch-restore.log"
  exit 0
fi
echo "ok   restore: VM answers ssh"
sleep 3
check "guest file written before save" 'cat /mnt/work/guest.txt'
check "held fd keeps appending" 'a=$(wc -l </mnt/work/held.log); sleep 3; b=$(wc -l </mnt/work/held.log); [ "$b" -gt "$a" ] && echo "$a -> $b lines" || { echo "stuck at $a -> $b; writer alive: $(kill -0 $(cat /run/held.pid) 2>&1 && echo yes)"; false; }'
check "host edit while saved is seen" 'c=$(cat /mnt/work/host-edit.txt); [ "$c" = host-during ] && echo "$c" || { echo "reads: $c"; false; }'
check "host-created file is seen" 'cat /mnt/work/host-new.txt'
check "host-deleted file is gone" 'ls /mnt/work/dir >/dev/null && ! test -e /mnt/work/dir/f50 && echo gone'
check "list a dir known before save" 'ls /mnt/work/dir | wc -l'
check "stat a file known before save" 'stat -c %s /mnt/work/dir/f1'
check "create a file after restore" 'echo after >/mnt/work/after.txt && cat /mnt/work/after.txt'
check "shell with cwd in share" 'tmux send-keys -t cwd "ls >/tmp/cwd.out 2>&1; echo \$? >>/tmp/cwd.out" Enter; sleep 1; tail -n 2 /tmp/cwd.out | tr "\n" " "; [ "$(tail -n 1 /tmp/cwd.out)" = 0 ]'
check "home share readable" 'cat /root/.claude/settings.json'
check "home write" 'echo x >/root/after.txt && cat /root/after.txt'
echo "host sees guest write after restore: $(sudo cat "$W/after.txt" 2>&1)"
g 'dmesg | grep -iE "virtio|fuse" | tail -n 5' || true
