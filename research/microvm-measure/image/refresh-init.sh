#!/usr/bin/env bash
# Replaces /sbin/a1-init in the base disk without a rebuild.
set -euo pipefail
BASE=${BASE:-/srv/aether/1/image/base.raw}
HERE=$(cd "$(dirname "$0")" && pwd)
mnt=$(mktemp -d)
sudo mount -o loop "$BASE" "$mnt"
sudo install -m 755 "$HERE/init.sh" "$mnt/sbin/a1-init"
sudo umount "$mnt"
rmdir "$mnt"
