#!/usr/bin/env bash
# Builds the measurement session image on the KVM host and flattens it to a
# raw ext4 base disk: $WORK/base.raw. Needs the repo's images/agents and
# internal/harness/acpregistry.json under $CTX (sync.sh copies them).
set -euo pipefail
RELEASE=${RELEASE:-v0.8.7-beta.14}
WORK=${WORK:-/srv/aether/1/image}
CTX=${CTX:-/srv/aether/1/repo}
HERE=$(cd "$(dirname "$0")" && pwd)
SIZE=${SIZE:-16G}

mkdir -p "$WORK"
docker pull -q "ghcr.io/3xdevops/aether-standard:$RELEASE"
docker build -q -t aether1-agents --build-arg "BASE=ghcr.io/3xdevops/aether-standard:$RELEASE" \
  -f "$CTX/images/agents/Dockerfile" "$CTX"
[ -f /srv/aether/1/keys/a1 ] || { mkdir -p /srv/aether/1/keys; ssh-keygen -q -t ed25519 -N '' -f /srv/aether/1/keys/a1; }
cp /srv/aether/1/keys/a1.pub "$HERE/a1.pub"
docker build -q -t aether1-measure "$HERE"

sudo rm -rf "$WORK/rootfs" && sudo mkdir -p "$WORK/rootfs"
docker rm -f aether1-export >/dev/null 2>&1 || true
docker create --name aether1-export aether1-measure >/dev/null
docker export aether1-export | sudo tar -x -C "$WORK/rootfs"
docker rm aether1-export >/dev/null
rm -f "$WORK/base.raw"
truncate -s "$SIZE" "$WORK/base.raw"
sudo mkfs.ext4 -q -F -L a1root -d "$WORK/rootfs" "$WORK/base.raw"
sudo chown "$(id -u):$(id -g)" "$WORK/base.raw"
echo "base disk: $(du -h --apparent-size "$WORK/base.raw" | cut -f1) apparent, $(du -h "$WORK/base.raw" | cut -f1) used; rootfs $(sudo du -sh "$WORK/rootfs" | cut -f1)"
