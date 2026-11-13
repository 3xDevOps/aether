#!/usr/bin/env bash
# (Re)starts the mock model as unit a1-mock, on the VM bridge and docker0.
# Scripts turn and w1..w40 each run one shell command, then answer.
set -euo pipefail
args=()
for s in turn $(seq -f 'w%g' 1 40); do args+=(-script "$s=uptime"); done
sudo systemctl stop a1-mock 2>/dev/null || true
sudo systemd-run -q --unit a1-mock --slice a1.slice --collect -p Type=simple \
  -p StandardOutput=append:/srv/aether/1/logs/mock.log -p StandardError=append:/srv/aether/1/logs/mock.log \
  /srv/aether/1/bin/mockmodel -listen 10.201.0.1:18080,172.17.0.1:18080 "${args[@]}"
