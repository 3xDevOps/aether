#!/usr/bin/env bash
# Builds acpdrive on moss and copies it to the KVM host.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(git -C "$HERE" rev-parse --show-toplevel)
HOST=${HOST:-ubuntu@98.93.21.225}
KEY=${KEY:-/root/.ssh/aether_kvm_test}
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 timeout 300 go build -o "$HERE/acpdrive.bin" ./research/microvm-measure/acpdrive)
scp -q -i "$KEY" "$HERE/acpdrive.bin" "$HOST:/srv/aether/1/bin/acpdrive.new"
ssh -i "$KEY" "$HOST" mv /srv/aether/1/bin/acpdrive.new /srv/aether/1/bin/acpdrive
rm -f "$HERE/acpdrive.bin"
