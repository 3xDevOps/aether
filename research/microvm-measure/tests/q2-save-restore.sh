#!/usr/bin/env bash
# Q2: save and restore timing. Runs on the KVM host.
#   MEM=1G|2G SHARED=on|off HOME_FS=1|0 FILL=0|1 RECLAIM=0|1
#   MODES="copy ondemand copyonwrite" AGENT=claude|codex LABEL=<name> q2-save-restore.sh
# Boots VM 9, runs one agent turn (FILL=1 then fills guest memory with
# random data in tmpfs, leaving 256 MiB; RECLAIM=1 runs the guest's root
# memory.reclaim and waits 15 s), times one prompt on the live VM as the
# baseline, saves it to $A1/snap/$LABEL, then
# for each mode: resets the root disk and home to their saved copies,
# evicts the snapshot from the host page cache, restarts its own mock model
# (a1-mock2) and destroys every host socket to the VM, so the agent's
# kept-alive connections get a reset as from a server that dropped them
# during the sleep (restoring one snapshot several times otherwise resumes
# connections whose host side has moved on, and they hang ~2 min), restores,
# and sends a prompt. Times are from the restore request.
set -euo pipefail
A1=/srv/aether/1
M=$A1/m
V=${V:-29}
MEM=${MEM:-1G} AGENT=${AGENT:-claude} MODES=${MODES:-copy ondemand copyonwrite}
LABEL=${LABEL:-mem$MEM-shared${SHARED:-on}-fill${FILL:-0}-reclaim${RECLAIM:-0}}
OUT=$A1/results/q2-$LABEL
SNAP=$A1/snap/$LABEL
D=$A1/vms/vm$V
rm -rf "$OUT" && mkdir -p "$OUT"
exec > >(tee "$OUT/log.txt") 2>&1
export MEM MOCK_URL=http://10.201.0.1:18081

now() { date +%s.%N; }
since() { echo "$(now) - $1" | bc; }
api() { sudo /usr/local/bin/ch-remote --api-socket /run/a1/vm$V/api.sock "$@"; }
answers() { "$M/host/vm.sh" ssh $V "tmux capture-pane -p -S - -t agent | grep -c aether-turn-done"; }
evict() { sudo python3 -c 'import os, sys
for p in sys.argv[1:]:
    fd = os.open(p, os.O_RDONLY); os.posix_fadvise(fd, 0, 0, os.POSIX_FADV_DONTNEED); os.close(fd)' "$@"; }

trap '"$M/host/vm.sh" stop $V; sudo systemctl stop a1-mock2' EXIT
"$M/host/vm.sh" stop $V
sudo rm -rf "$D" && mkdir -p "$D" && sudo mkdir -p "$D/home"
"$M/host/seed-home.sh" "$D/home" "$MOCK_URL"
"$M/host/mock.sh" a1-mock2 10.201.0.1:18081
"$M/host/vm.sh" start $V
[ "${HOME_FS:-1}" = 0 ] && "$M/host/vm.sh" ssh $V "bash -s /root $MOCK_URL" <"$M/host/seed-home.sh"
"$M/host/vm.sh" ssh $V "mkdir -p /workspace"
"$M/host/agent.sh" vm $V "$AGENT" turn
for _ in $(seq 120); do [ "$(answers)" -ge 1 ] && break; sleep 1; done
[ "$(answers)" -ge 1 ] || { echo "no first answer"; exit 1; }
if [ "${FILL:-0}" = 1 ]; then
  "$M/host/vm.sh" ssh $V 'free_kb=$(awk "/MemAvailable/{print \$2}" /proc/meminfo); head -c $(( (free_kb - 262144) * 1024 )) /dev/urandom >/dev/shm/fill; free -m | sed -n 2p'
fi
t0=$(now)
"$M/host/agent.sh" prompt vm $V w40
"$M/host/vm.sh" ssh $V "until [ \$(tmux capture-pane -p -S - -t agent | grep -c aether-turn-done) -ge 2 ]; do sleep 0.05; done"
echo "baseline: live VM, prompt to answer $(since "$t0")s (ssh connect included)"
if [ "${RECLAIM:-0}" = 1 ]; then
  echo "before reclaim: $("$M/host/vm.sh" mem $V)"
  "$M/host/vm.sh" ssh $V "echo 8G >/sys/fs/cgroup/memory.reclaim 2>/dev/null || true"
  sleep 15
fi
sleep 5
echo "before save: $("$M/host/vm.sh" mem $V)"

sudo rm -rf "$SNAP" && sudo mkdir -p "$SNAP"
t0=$(now)
api pause
t1=$(now)
api snapshot "file://$SNAP"
t2=$(now)
"$M/host/vm.sh" stop $V
sudo sync -f "$SNAP"
t3=$(now)
printf 'save: pause %.3fs snapshot %.3fs stop+sync %.3fs total %.3fs\n' "$(echo "$t1-$t0" | bc)" "$(echo "$t2-$t1" | bc)" "$(echo "$t3-$t2" | bc)" "$(echo "$t3-$t0" | bc)"
sudo ls -ls "$SNAP"
echo "snapshot size: $(sudo du -sh "$SNAP" | cut -f1) on disk, $(sudo du -sh --apparent-size "$SNAP" | cut -f1) apparent"
cp --reflink=always "$D/root.disk" "$D/root.saved"
sudo rm -rf "$D/home.saved" && sudo cp -a --reflink=always "$D/home" "$D/home.saved"

w=0
for mode in $MODES; do
  w=$((w + 1))
  cp --reflink=always "$D/root.saved" "$D/root.disk"
  sudo rm -rf "$D/home" && sudo cp -a --reflink=always "$D/home.saved" "$D/home"
  evict "$SNAP"/*
  "$M/host/mock.sh" a1-mock2 10.201.0.1:18081
  sudo ss -K dst "10.201.0.$((10 + V))" >/dev/null
  t0=$(now)
  RESTORE_MODE=$mode "$M/host/vm.sh" restore $V "$SNAP"
  until api info 2>/dev/null | jq -e '.state == "Running"' >/dev/null; do
    systemctl is-active -q a1-vm$V || { echo "mode $mode: monitor exited"; sudo tail -n 5 "$D/ch-restore.log"; continue 2; }
    sleep 0.01
  done
  running=$(since "$t0")
  "$M/host/vm.sh" wait-ssh $V
  ssh_at=$(since "$t0")
  "$M/host/agent.sh" prompt vm $V "w$w"
  "$M/host/vm.sh" ssh $V "until [ \$(tmux capture-pane -p -S - -t agent | grep -c aether-turn-done) -ge 3 ]; do sleep 0.05; done"
  answer=$(since "$t0")
  printf 'restore %s: running %.3fs ssh %.3fs answered %.3fs; %s\n' "$mode" "$running" "$ssh_at" "$answer" "$("$M/host/vm.sh" mem $V)"
  sudo grep -iE 'warn|fall' "$D/ch-restore.log" | grep -v 'already exists' | head -n 3 || true
  sleep 30
  echo "  30 s later: $("$M/host/vm.sh" mem $V)"
  "$M/host/vm.sh" stop $V
done
