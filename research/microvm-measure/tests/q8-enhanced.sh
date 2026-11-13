#!/usr/bin/env bash
# Q8 (coordinator follow-up): host memory of Enhanced sessions, idle after
# one turn. Runs on the KVM host.
#   KIND=vm|ctr AGENT=claude|codex|none N=3 TUNED=0|1 WARM=0|1 LABEL=<name> q8-enhanced.sh
# Each session gets a fresh home; acpdrive opens the Enhanced session from
# the host as the server would. WARM=1 first runs one session to its end
# and drops the guest page cache, so the measured start finds what the
# agent left in the home (Codex's copy of its release). Samples: 60 s after
# the turn; then for TUNED=1 VMs (memory shared=on) the guest writes 8G to
# its root memory.reclaim, sampled every 5 s for 180 s. Untuned VMs and
# containers are sampled the same 180 s without a reclaim.
set -euo pipefail
A1=/srv/aether/1
M=$A1/m
KIND=${KIND:-vm} AGENT=${AGENT:-claude} N=${N:-3} TUNED=${TUNED:-0} WARM=${WARM:-0}
LABEL=${LABEL:-$KIND-$AGENT-$N-tuned$TUNED-warm$WARM}
OUT=$A1/results/q8-$LABEL
rm -rf "$OUT" && mkdir -p "$OUT"
exec > >(tee "$OUT/log.txt") 2>&1
export SHARED=off
[ "$TUNED" = 1 ] && SHARED=on
ids=$(seq 31 $((30 + N)))
url=http://10.201.0.1:18080
[ "$KIND" = ctr ] && url=http://172.17.0.1:18080

on() {
  case "$KIND" in
    vm) "$M/host/vm.sh" ssh "$1" "$2" ;;
    ctr) docker exec -i "a1-ctr$1" bash -c "$2" ;;
  esac
}

sample() { # phase
  local i id cg
  echo "phase=$1 t=$(date +%s) host $(awk '/^(SecPageTables|PageTables|Shmem|AnonPages):/{printf "%s=%s ", $1, $2}' /proc/meminfo)" >>"$OUT/samples.txt"
  [ "$1" = before ] && return
  for i in $ids; do
    case "$KIND" in
      vm) echo "phase=$1 t=$(date +%s) $("$M/host/vm.sh" mem "$i")" >>"$OUT/samples.txt" ;;
      ctr)
        id=$(docker inspect -f '{{.Id}}' "a1-ctr$i")
        cg=/sys/fs/cgroup/system.slice/docker-$id.scope
        echo "phase=$1 t=$(date +%s) ctr$i current=$(($(cat "$cg/memory.current") / 1024)) $(awk '$1 ~ /^(anon|file|shmem|kernel)$/ {printf "%s=%d ", $1, $2 / 1024}' "$cg/memory.stat")" >>"$OUT/samples.txt"
        ;;
    esac
  done
}

drive() { # i script-log
  local target=vm:10.201.0.$((10 + $1))
  [ "$KIND" = ctr ] && target=ctr:a1-ctr$1
  sudo systemctl stop "a1-acp$1" 2>/dev/null || true
  sudo systemd-run -q --unit "a1-acp$1" --slice a1.slice --collect -p Type=simple \
    -p StandardOutput="file:$2" -p StandardError="append:$2" \
    "$A1/bin/acpdrive" -agent "$AGENT" -target "$target" -url "$url" -log "$A1/acplog/$KIND$1-$(date +%s%N).log"
}

wait_turn() { # i script-log
  for _ in $(seq 180); do grep -q "^turn turn done" "$2" 2>/dev/null && return; sleep 1; done
  echo "session $1: no answer"; cat "$2"; exit 1
}

teardown() {
  for i in $ids; do
    sudo systemctl stop "a1-acp$i" 2>/dev/null || true
    case "$KIND" in
      vm) "$M/host/vm.sh" stop "$i"; sudo rm -rf "$A1/vms/vm$i" ;;
      ctr) docker rm -f "a1-ctr$i" >/dev/null 2>&1; sudo rm -rf "$A1/ctrs/ctr$i" ;;
    esac
  done
}
trap teardown EXIT
mkdir -p "$A1/acplog"

sample before
for i in $ids; do
  case "$KIND" in
    vm)
      sudo rm -rf "$A1/vms/vm$i" && mkdir -p "$A1/vms/vm$i"
      HOME_FS=0 "$M/host/vm.sh" start "$i"
      on "$i" "mkdir -p /workspace"
      "$M/host/vm.sh" ssh "$i" "bash -s /root $url" <"$M/host/seed-home.sh"
      ;;
    ctr)
      docker rm -f "a1-ctr$i" >/dev/null 2>&1 || true
      sudo rm -rf "$A1/ctrs/ctr$i" && sudo mkdir -p "$A1/ctrs/ctr$i/home"
      "$M/host/seed-home.sh" "$A1/ctrs/ctr$i/home" "$url"
      docker run -d -q --name "a1-ctr$i" --hostname "a1-ctr$i" --cpus 2 --memory 2g \
        -v "$A1/ctrs/ctr$i/home:/root" --entrypoint sleep aether1-measure infinity >/dev/null
      on "$i" "mkdir -p /workspace"
      ;;
  esac
done
sample booted

if [ "$AGENT" != none ]; then
  if [ "$WARM" = 1 ]; then
    for i in $ids; do drive "$i" "$OUT/warmup$i.txt"; done
    for i in $ids; do wait_turn "$i" "$OUT/warmup$i.txt"; sudo systemctl stop "a1-acp$i"; done
    sleep 3
    for i in $ids; do
      on "$i" "pgrep -fa 'claude|codex' | grep -v pgrep | head -3 || true"
      on "$i" "du -sh /root/.codex /root/.claude 2>/dev/null | tr '\n' ' '; echo"
      [ "$KIND" = vm ] && on "$i" "sync; echo 3 >/proc/sys/vm/drop_caches"
    done
    sleep 20
    sample warmed
  fi
  for i in $ids; do drive "$i" "$OUT/drive$i.txt"; done
  for i in $ids; do wait_turn "$i" "$OUT/drive$i.txt"; done
  grep -h "^turn" "$OUT"/drive*.txt
fi
sleep 60
sample idle
for i in $ids; do on "$i" "ps -eo rss=,args= --sort=-rss | head -6 | cut -c1-110" | sed "s/^/session $i ps: /"; done
[ "$KIND" = vm ] && for i in $ids; do on "$i" "free -k | sed -n 2p" | sed "s/^/session $i guest before reclaim: /"; done
if [ "$KIND" = vm ] && [ "$TUNED" = 1 ]; then
  for i in $ids; do on "$i" "echo 8G >/sys/fs/cgroup/memory.reclaim 2>/dev/null || true"; done
fi
for s in $(seq 5 5 180); do
  sleep 5
  sample "settle$s"
done
[ "$KIND" = vm ] && for i in $ids; do on "$i" "free -k | sed -n 2p; grep -E '^(MemTotal|Slab|PageTables|KernelStack|Percpu):' /proc/meminfo | tr -s ' ' | tr '\n' ' '; echo" | sed "s/^/session $i guest end: /"; done
"$M/tests/q8-summary.sh" "$OUT/samples.txt" | tee "$OUT/summary.txt"
