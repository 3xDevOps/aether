#!/usr/bin/env bash
# Builds mockmodel on moss from the real-agent suite's mock model and copies
# it to the KVM host. mockmodel.go is generated and not committed.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(git -C "$HERE" rev-parse --show-toplevel)
HOST=${HOST:-ubuntu@98.93.21.225}
KEY=${KEY:-/root/.ssh/aether_kvm_test}
sed -e '/^\/\/go:build/d' -e 's/^package server$/package main/' \
  "$ROOT/internal/server/mockmodel_integration_test.go" >"$HERE/mockmodel.go"
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 timeout 300 go build -o "$HERE/mockmodel.bin" ./research/microvm-measure/mockmodel)
scp -q -i "$KEY" "$HERE/mockmodel.bin" "$HOST:/srv/aether/1/bin/mockmodel.new"
ssh -i "$KEY" "$HOST" mv /srv/aether/1/bin/mockmodel.new /srv/aether/1/bin/mockmodel
rm -f "$HERE/mockmodel.bin"
