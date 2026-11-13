#!/usr/bin/env bash
# Builds fusevsock on moss and copies it to the KVM host.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
HOST=${HOST:-ubuntu@98.93.21.225}
KEY=${KEY:-/root/.ssh/aether_kvm_test}
(cd "$HERE" && CGO_ENABLED=0 GOWORK=off timeout 300 go build -o fusevsock.bin .)
scp -q -i "$KEY" "$HERE/fusevsock.bin" "$HOST:/srv/aether/1/bin/fusevsock.new"
ssh -i "$KEY" "$HOST" mv /srv/aether/1/bin/fusevsock.new /srv/aether/1/bin/fusevsock
rm -f "$HERE/fusevsock.bin"
