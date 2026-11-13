#!/usr/bin/env bash
# Q1: host memory per awake session. Runs on the KVM host.
#   KIND=vm|ctr AGENT=claude|codex N=<count> LABEL=<name> q1-memory.sh
# Phases: N sessions start; one agent turn each; idle 60 s; npm install of
# tests/package.json in all at once (sampled every 2 s); 60 s after; VM
# only: guest proactive reclaim (root memory.reclaim), 20 s after; one more
# agent turn and 60 s after that. VM options pass through to vm.sh.
set -euo pipefail
A1=/srv/aether/1
M=$A1/m
KIND=${KIND:-vm} AGENT=${AGENT:-claude} N=${N:-1}
LABEL=${LABEL:-$KIND-$AGENT-$N}
OUT=$A1/results/q1-$LABEL
rm -rf "$OUT" && mkdir -p "$OUT"
exec > >(tee "$OUT/log.txt") 2>&1

on() { # i command
  case "$KIND" in
    vm) "$M/host/vm.sh" ssh "$1" "$2" ;;
    ctr) docker exec "a1-ctr$1" bash -c "$2" ;;
  esac
}

hostmem() { awk '/^(MemAvailable|AnonPages|Shmem|Cached):/{printf "%s=%s ", $1, $2}' /proc/meminfo; }

sample() { # phase
  local i
  echo "phase=$1 t=$(date +%s) host $(hostmem)" >>"$OUT/samples.txt"
  [ "$1" = before ] && return
  for i in $(seq "$N"); do
    case "$KIND" in
      vm) echo "phase=$1 t=$(date +%s) $("$M/host/vm.sh" mem "$i")" >>"$OUT/samples.txt" ;;
      ctr)
        local id cg
        id=$(docker inspect -f '{{.Id}}' "a1-ctr$i")
        cg=/sys/fs/cgroup/system.slice/docker-$id.scope
        echo "phase=$1 t=$(date +%s) ctr$i current=$(($(cat "$cg/memory.current") / 1024)) $(awk '$1 ~ /^(anon|file|shmem|kernel)$/ {printf "%s=%d ", $1, $2 / 1024}' "$cg/memory.stat")" >>"$OUT/samples.txt"
        ;;
    esac
  done
}

boot() {
  local i
  for i in $(seq "$N"); do
    case "$KIND" in
      vm)
        mkdir -p "$A1/vms/vm$i"
        sudo rm -rf "$A1/vms/vm$i/home" "$A1/vms/vm$i/root.disk"
        sudo mkdir -p "$A1/vms/vm$i/home"
        "$M/host/seed-home.sh" "$A1/vms/vm$i/home"
        "$M/host/vm.sh" start "$i"
        [ "${HOME_FS:-1}" = 0 ] && "$M/host/vm.sh" ssh "$i" 'bash -s /root' <"$M/host/seed-home.sh"
        ;;
      ctr)
        docker rm -f "a1-ctr$i" >/dev/null 2>&1 || true
        sudo rm -rf "$A1/ctrs/ctr$i" && sudo mkdir -p "$A1/ctrs/ctr$i/home"
        "$M/host/seed-home.sh" "$A1/ctrs/ctr$i/home" http://172.17.0.1:18080
        docker run -d -q --name "a1-ctr$i" --hostname "a1-ctr$i" --cpus "${CPUS:-2}" --memory "${MEM:-2g}" \
          -v "$A1/ctrs/ctr$i/home:/root" --entrypoint sleep aether1-measure infinity >/dev/null
        ;;
    esac
  done
}

teardown() {
  local i
  for i in $(seq "$N"); do
    case "$KIND" in
      vm) "$M/host/vm.sh" stop "$i"; sudo rm -rf "$A1/vms/vm$i" ;;
      ctr) docker rm -f "a1-ctr$i" >/dev/null; sudo rm -rf "$A1/ctrs/ctr$i" ;;
    esac
  done
}
trap teardown EXIT

sample before
t0=$(date +%s.%N)
boot
echo "booted $N in $(echo "$(date +%s.%N) - $t0" | bc)s"
sample booted

url=http://10.201.0.1:18080
[ "$KIND" = ctr ] && url=http://172.17.0.1:18080
for i in $(seq "$N"); do MOCK_URL=$url "$M/host/agent.sh" "$KIND" "$i" "$AGENT" turn; done
for i in $(seq "$N"); do
  for _ in $(seq 120); do
    on "$i" "tmux capture-pane -p -t agent" | grep -q aether-turn-done && break
    sleep 1
  done
  on "$i" "tmux capture-pane -p -t agent" | grep -q aether-turn-done || { echo "session $i: no answer"; exit 1; }
done
echo "all $N answered"
sleep 60
sample idle

for i in $(seq "$N"); do
  on "$i" "mkdir -p /workspace/app && cat >/workspace/app/package.json" <"$M/tests/package.json"
  on "$i" "cd /workspace/app && nohup bash -c 's=\$(date +%s); npm install --no-audit --no-fund >/tmp/npm.log 2>&1; echo exit \$? took \$((\$(date +%s) - s))s >/tmp/npm.done' </dev/null >/dev/null 2>&1 &"
done
while :; do
  sample npm
  done=0
  for i in $(seq "$N"); do on "$i" "test -f /tmp/npm.done" && done=$((done + 1)); done
  [ "$done" = "$N" ] && break
  sleep 2
done
for i in $(seq "$N"); do echo "session $i npm: $(on "$i" "cat /tmp/npm.done; du -sh /workspace/app/node_modules | cut -f1; ls /workspace/app/node_modules | wc -l")" | tr '\n' ' '; echo; done
sleep 60
sample after
if [ "$KIND" = vm ]; then
  for i in $(seq "$N"); do on "$i" "free -k | sed -n 2p" | sed "s/^/guest$i before reclaim: /"; done
  for i in $(seq "$N"); do on "$i" "echo 8G >/sys/fs/cgroup/memory.reclaim 2>/dev/null || true"; done
  sleep 20
  sample reclaimed
  for i in $(seq "$N"); do on "$i" "free -k | sed -n 2p" | sed "s/^/guest$i after reclaim: /"; done
  for i in $(seq "$N"); do MOCK_URL=$url "$M/host/agent.sh" prompt "$KIND" "$i" w1; done
  sleep 60
  sample later
fi
"$M/tests/q1-summary.sh" "$OUT/samples.txt" | tee "$OUT/summary.txt"
