#!/usr/bin/env bash
# Builds the item 1 guest kernel on the KVM host: Linux longterm, x86_64
# defconfig + kvm_guest.config + aether1.config. Output: $OUT/vmlinux.
set -euo pipefail
VERSION=${VERSION:-6.18.63}
WORK=${WORK:-/srv/aether/1/kernel}
OUT=${OUT:-$WORK/out}
HERE=$(cd "$(dirname "$0")" && pwd)

mkdir -p "$WORK" "$OUT"
cd "$WORK"
[ -f "linux-$VERSION.tar.xz" ] || curl -fsSLO "https://cdn.kernel.org/pub/linux/kernel/v${VERSION%%.*}.x/linux-$VERSION.tar.xz"
[ -d "linux-$VERSION" ] || tar -xf "linux-$VERSION.tar.xz"
cd "linux-$VERSION"
make -s defconfig
make -s kvm_guest.config
scripts/kconfig/merge_config.sh -m .config "$HERE/aether1.config" >/dev/null
make -s olddefconfig
missing=0
while IFS= read -r line; do
  case "$line" in CONFIG_*=y)
    grep -qx "$line" .config || { echo "not set: $line" >&2; missing=1; } ;;
  esac
done < "$HERE/aether1.config"
nice -n 10 make -s -j"$(nproc)" vmlinux
cp vmlinux "$OUT/vmlinux"
cp .config "$OUT/config"
echo "kernel $VERSION -> $OUT/vmlinux (unset options: $missing)"
