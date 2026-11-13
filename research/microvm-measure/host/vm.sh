#!/usr/bin/env bash
# Item 1 VM control on the KVM host. Every VM, tap, unit and file carries
# "a1". Usage:
#   vm.sh net-up | net-down
#   vm.sh start <n>           boot VM n (options below, as environment)
#   vm.sh ssh <n> <command>
#   vm.sh stop <n>            stop VM n and its virtiofsd units
#   vm.sh save <n> <dir>      pause, snapshot to <dir>, stop the monitor
#   vm.sh restore <n> <dir>   restore VM n from <dir> (RESTORE_MODE)
#   vm.sh mem <n>             one line of host memory for VM n
# Options: MEM (2G), CPUS (2), SHARED (on with a share, else off),
# MERGEABLE (off), PRO (page_reporting_order, 0), HOME_FS (1: home over
# virtio-fs), DOCKER (0), DISKS (extra raw/qcow2 paths, space separated),
# DISK_OPTS (Cloud Hypervisor options for the extra disks), DATA (dev:dir),
# FS (extra tag=dir virtio-fs shares, space separated), FS_CACHE (auto),
# RESTORE_MODE (copy), KERNEL, BASE, ROOT_COPY (reflink|qcow2).
set -euo pipefail
A1=/srv/aether/1
KERNEL=${KERNEL:-$A1/kernel/out/vmlinux}
BASE=${BASE:-$A1/image/base.raw}
BRIDGE=a1br0
NET=10.201.0
CH=/usr/local/bin/cloud-hypervisor
CHR=/usr/local/bin/ch-remote
VIRTIOFSD=/usr/libexec/virtiofsd

vmdir() { echo "$A1/vms/vm$1"; }
run() { echo "/run/a1/vm$1"; }
api() { sudo "$CHR" --api-socket "$(run "$1")/api.sock" "${@:2}"; }

net_up() {
  sudo ip link show "$BRIDGE" >/dev/null 2>&1 || {
    sudo ip link add "$BRIDGE" type bridge
    sudo ip addr add "$NET.1/24" dev "$BRIDGE"
    sudo ip link set "$BRIDGE" up
  }
  sudo sysctl -qw net.ipv4.ip_forward=1
  sudo iptables -C DOCKER-USER -i "$BRIDGE" -m comment --comment a1 -j ACCEPT 2>/dev/null ||
    sudo iptables -I DOCKER-USER -i "$BRIDGE" -m comment --comment a1 -j ACCEPT
  sudo iptables -C DOCKER-USER -o "$BRIDGE" -m comment --comment a1 -j ACCEPT 2>/dev/null ||
    sudo iptables -I DOCKER-USER -o "$BRIDGE" -m comment --comment a1 -j ACCEPT
  sudo iptables -t nat -C POSTROUTING -s "$NET.0/24" ! -o "$BRIDGE" -m comment --comment a1 -j MASQUERADE 2>/dev/null ||
    sudo iptables -t nat -A POSTROUTING -s "$NET.0/24" ! -o "$BRIDGE" -m comment --comment a1 -j MASQUERADE
}

net_down() {
  sudo iptables -D DOCKER-USER -i "$BRIDGE" -m comment --comment a1 -j ACCEPT 2>/dev/null || true
  sudo iptables -D DOCKER-USER -o "$BRIDGE" -m comment --comment a1 -j ACCEPT 2>/dev/null || true
  sudo iptables -t nat -D POSTROUTING -s "$NET.0/24" ! -o "$BRIDGE" -m comment --comment a1 -j MASQUERADE 2>/dev/null || true
  for t in $(ip -o link show | awk -F': ' '{print $2}' | grep '^a1tap' || true); do sudo ip link del "${t%@*}"; done
  sudo ip link del "$BRIDGE" 2>/dev/null || true
}

tap_up() {
  local tap=a1tap$1
  ip link show "$tap" >/dev/null 2>&1 || sudo ip tuntap add "$tap" mode tap
  sudo ip link set "$tap" master "$BRIDGE" up
}

start_fs() { # n tag dir
  local unit="a1-fs$1-$2"
  sudo systemctl stop "$unit" 2>/dev/null || true
  sudo rm -f "$(run "$1")/$2.sock"
  sudo systemd-run -q --unit "$unit" --slice a1.slice --collect -p Type=simple \
    "$VIRTIOFSD" --socket-path="$(run "$1")/$2.sock" --shared-dir="$3" \
    --cache="${FS_CACHE:-auto}" --sandbox=chroot --log-level=error
  for _ in $(seq 100); do [ -S "$(run "$1")/$2.sock" ] && return; sleep 0.05; done
  echo "virtiofsd $unit did not open its socket" >&2
  return 1
}

fs_shares() { # n -> --fs args, starting virtiofsd for each
  local n=$1 spec tag dir
  FS_ARGS=() FS_CMDLINE=()
  local shares=(${FS:-})
  [ "${HOME_FS:-1}" = 1 ] && shares=("home=$(vmdir "$n")/home" "${shares[@]}")
  for spec in "${shares[@]}"; do
    tag=${spec%%=*} dir=${spec#*=}
    start_fs "$n" "$tag" "$dir"
    FS_ARGS+=(--fs "tag=$tag,socket=$(run "$n")/$tag.sock")
    case "$tag" in home) FS_CMDLINE+=("home:/root") ;; *) FS_CMDLINE+=("$tag:/mnt/$tag") ;; esac
  done
}

disk_arg() { # path [extra options]
  local fmt a
  fmt=$(qemu-img info --output=json "$1" | jq -r .format)
  a="path=$1,image_type=$fmt"
  [ "$fmt" = qcow2 ] && a+=",backing_files=on"
  echo "$a${2:+,$2}"
}

start() {
  local n=$1 d r
  d=$(vmdir "$n") r=$(run "$n")
  sudo mkdir -p "$r"
  mkdir -p "$d"
  [ -f "$d/root.disk" ] || case "${ROOT_COPY:-reflink}" in
    reflink) cp --reflink=always "$BASE" "$d/root.disk" ;;
    qcow2) qemu-img create -q -f qcow2 -b "$BASE" -F raw "$d/root.disk" ;;
  esac
  tap_up "$n"
  fs_shares "$n"
  local disks=(--disk "$(disk_arg "$d/root.disk")")
  for p in ${DISKS:-}; do disks+=(--disk "$(disk_arg "$p" "serial=$(basename "$p" | cut -c1-20)${DISK_OPTS:+,$DISK_OPTS}")"); done
  local cmdline="console=hvc0 root=/dev/vda rw init=/sbin/a1-init quiet a1.name=a1-vm$n a1.ip=$NET.$((10 + n))/24 a1.gw=$NET.1"
  [ ${#FS_CMDLINE[@]} -gt 0 ] && cmdline+=" a1.fs=$(IFS=,; echo "${FS_CMDLINE[*]}")"
  [ "${DOCKER:-0}" = 1 ] && cmdline+=" a1.docker=1"
  [ -n "${DATA:-}" ] && cmdline+=" a1.data=$DATA"
  cmdline+=" page_reporting.page_reporting_order=${PRO:-0}"
  cmdline+=" ${CMDLINE_EXTRA:-}"
  sudo rm -f "$r/api.sock" "$r/vsock.sock"
  sudo systemd-run -q --unit "a1-vm$n" --slice a1.slice --collect -p Type=simple \
    "$CH" --api-socket "path=$r/api.sock" --kernel "$KERNEL" --cmdline "$cmdline" \
    --cpus "boot=${CPUS:-2}" --memory "size=${MEM:-2G},shared=${SHARED:-$([ ${#FS_ARGS[@]} -gt 0 ] && echo on || echo off)},mergeable=${MERGEABLE:-off}" \
    --balloon "size=0,free_page_reporting=on" "${disks[@]}" \
    --net "tap=a1tap$n,mac=52:54:a1:00:00:$(printf %02x "$n")" "${FS_ARGS[@]}" \
    --vsock "cid=$((100 + n)),socket=$r/vsock.sock" \
    --console "file=$d/console.log" --serial off --log-file "$d/ch.log"
  wait_ssh "$n"
}

wait_ssh() {
  for _ in $(seq 600); do
    systemctl is-active -q "a1-vm$1" || { echo "vm$1: monitor exited" >&2; sudo tail -n 5 "$(vmdir "$1")/ch.log" >&2; return 1; }
    vssh "$1" true 2>/dev/null && return
    sleep 0.1
  done
  echo "vm$1: no ssh after 60s" >&2
  return 1
}

vssh() {
  ssh -q -i "$A1/keys/a1" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=2 -o BatchMode=yes "root@$NET.$((10 + $1))" "${@:2}"
}

stop() {
  local n=$1
  api "$n" shutdown-vmm 2>/dev/null || true
  sudo systemctl stop "a1-vm$n" 2>/dev/null || true
  for u in $(systemctl list-units --plain --no-legend "a1-fs$n-*" | awk '{print $1}'); do sudo systemctl stop "$u"; done
}

save() { # n dir
  local n=$1 dir=$2
  sudo rm -rf "$dir" && sudo mkdir -p "$dir"
  local t0 t1 t2
  t0=$(date +%s.%N)
  api "$n" pause
  t1=$(date +%s.%N)
  api "$n" snapshot "file://$dir"
  t2=$(date +%s.%N)
  stop "$n"
  sync -f "$dir"
  local t3
  t3=$(date +%s.%N)
  printf 'save vm%s: pause %.3fs snapshot %.3fs sync %.3fs total %.3fs; size %s apparent %s\n' "$n" \
    "$(echo "$t1 - $t0" | bc)" "$(echo "$t2 - $t1" | bc)" "$(echo "$t3 - $t2" | bc)" "$(echo "$t3 - $t0" | bc)" \
    "$(sudo du -sh "$dir" | cut -f1)" "$(sudo du -sh --apparent-size "$dir" | cut -f1)"
}

restore() { # n dir
  local n=$1 dir=$2 r
  r=$(run "$n")
  sudo mkdir -p "$r"
  tap_up "$n"
  fs_shares "$n"
  sudo rm -f "$r/api.sock" "$r/vsock.sock"
  sudo systemd-run -q --unit "a1-vm$n" --slice a1.slice --collect -p Type=simple \
    "$CH" --api-socket "path=$r/api.sock" --log-file "$(vmdir "$n")/ch-restore.log" \
    --restore "source_url=file://$dir,memory_restore_mode=${RESTORE_MODE:-copy},resume=true"
}

mem() { # n -> one line of KiB figures for VM n's monitor and virtiofsd
  local n=$1 pid fsrss=0 p u
  pid=$(systemctl show -p MainPID --value "a1-vm$n")
  for u in $(systemctl list-units --plain --no-legend "a1-fs$n-*" | awk '{print $1}'); do
    p=$(systemctl show -p MainPID --value "$u")
    fsrss=$((fsrss + $(sudo awk '/^RssAnon:/{print $2}' "/proc/$p/status")))
  done
  echo "vm$n $(sudo awk '/^(VmRSS|RssAnon|RssFile|RssShmem):/{sub(":", "", $1); printf "%s=%s ", $1, $2}' "/proc/$pid/status")virtiofsd_anon=$fsrss"
}

cmd=$1
shift
case "$cmd" in
  net-up) net_up ;;
  net-down) net_down ;;
  start) start "$@" ;;
  ssh) vssh "$@" ;;
  stop) stop "$@" ;;
  save) save "$@" ;;
  restore) restore "$@" ;;
  mem) mem "$@" ;;
  wait-ssh) wait_ssh "$@" ;;
  *) echo "unknown command $cmd" >&2; exit 2 ;;
esac
