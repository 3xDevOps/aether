#!/usr/bin/env bash
# Q6: host CPU of N idle sessions. Runs on the KVM host.
#   KIND=vm|ctr N=10 AGENT=none|claude|codex WINDOW=120 CMDLINE_EXTRA= q6-idle-cpu.sh
# Starts N sessions (VMs 11..10+N, 1 GiB, 1 vCPU unless CPUS/MEM say
# otherwise), runs one agent turn in each unless AGENT=none, settles 60 s,
# then measures CPU over WINDOW seconds: per VM the monitor's utime+stime
# (all its threads, vCPUs included) and virtiofsd's; per container its
# cgroup usage_usec.
set -euo pipefail
A1=/srv/aether/1
M=$A1/m
KIND=${KIND:-vm} N=${N:-10} AGENT=${AGENT:-none} WINDOW=${WINDOW:-120}
LABEL=${LABEL:-$KIND-$AGENT-$N}
OUT=$A1/results/q6-$LABEL
rm -rf "$OUT" && mkdir -p "$OUT"
exec > >(tee "$OUT/log.txt") 2>&1
export MEM=${MEM:-1G} CPUS=${CPUS:-1}
ids=$(seq 11 $((10 + N)))
HZ=$(getconf CLK_TCK)

on() {
  case "$KIND" in
    vm) "$M/host/vm.sh" ssh "$1" "$2" ;;
    ctr) docker exec "a1-ctr$1" bash -c "$2" ;;
  esac
}
usage_us() { # i -> CPU microseconds used so far by session i
  case "$KIND" in
    vm)
      local t=0 u p
      for u in "a1-vm$1" $(systemctl list-units --plain --no-legend "a1-fs$1-*" | awk '{print $1}'); do
        p=$(systemctl show -p MainPID --value "$u")
        t=$((t + $(awk '{print $14 + $15}' "/proc/$p/stat")))
      done
      echo $((t * 1000000 / HZ))
      ;;
    ctr) awk '/^usage_usec/{print $2}' "/sys/fs/cgroup/system.slice/docker-$(docker inspect -f '{{.Id}}' "a1-ctr$1").scope/cpu.stat" ;;
  esac
}

teardown() {
  for i in $ids; do
    case "$KIND" in
      vm) "$M/host/vm.sh" stop "$i"; sudo rm -rf "$A1/vms/vm$i" ;;
      ctr) docker rm -f "a1-ctr$i" >/dev/null 2>&1; sudo rm -rf "$A1/ctrs/ctr$i" ;;
    esac
  done
}
trap teardown EXIT

for i in $ids; do
  case "$KIND" in
    vm)
      sudo rm -rf "$A1/vms/vm$i" && mkdir -p "$A1/vms/vm$i" && sudo mkdir -p "$A1/vms/vm$i/home"
      "$M/host/seed-home.sh" "$A1/vms/vm$i/home"
      "$M/host/vm.sh" start "$i"
      ;;
    ctr)
      sudo rm -rf "$A1/ctrs/ctr$i" && sudo mkdir -p "$A1/ctrs/ctr$i/home"
      "$M/host/seed-home.sh" "$A1/ctrs/ctr$i/home" http://172.17.0.1:18080
      docker run -d -q --name "a1-ctr$i" --cpus "$CPUS" --memory "${MEM,,}" -v "$A1/ctrs/ctr$i/home:/root" \
        --entrypoint sleep aether1-measure infinity >/dev/null
      ;;
  esac
done
if [ "$AGENT" != none ]; then
  url=http://10.201.0.1:18080
  [ "$KIND" = ctr ] && url=http://172.17.0.1:18080
  for i in $ids; do on "$i" "mkdir -p /workspace"; t=$i; [ "$KIND" = ctr ] && t=a1-ctr$i; MOCK_URL=$url "$M/host/agent.sh" "$KIND" "$t" "$AGENT" turn; done
  for i in $ids; do
    for _ in $(seq 120); do on "$i" "tmux capture-pane -p -t agent" | grep -q aether-turn-done && break; sleep 1; done
  done
fi
echo "started $N $KIND sessions, agent $AGENT; settling 60 s"
sleep 60

declare -A before
for i in $ids; do before[$i]=$(usage_us "$i"); done
read -r _ u0 n0 s0 i0 w0 q0 sq0 st0 _ </proc/stat
sleep "$WINDOW"
read -r _ u1 n1 s1 i1 w1 q1 sq1 st1 _ </proc/stat
total=0
for i in $ids; do
  d=$(($(usage_us "$i") - before[$i]))
  total=$((total + d))
  printf 'session %s: %.3f%% of one CPU\n' "$i" "$(echo "$d / $WINDOW / 10000" | bc -l)"
done
busy=$(((u1 + n1 + s1 + q1 + sq1 + st1) - (u0 + n0 + s0 + q0 + sq0 + st0)))
all=$((busy + (i1 + w1) - (i0 + w0)))
printf 'sum of %s sessions: %.3f%% of one CPU = %.3f%% of the %s-CPU host; host busy overall (all tenants): %.2f%%\n' \
  "$N" "$(echo "$total / $WINDOW / 10000" | bc -l)" "$(echo "$total / $WINDOW / 10000 / $(nproc)" | bc -l)" "$(nproc)" "$(echo "$busy * 100 / $all" | bc -l)"
