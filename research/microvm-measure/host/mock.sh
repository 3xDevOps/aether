#!/usr/bin/env bash
# (Re)starts a mock model unit: mock.sh [unit] [listen]. Defaults: a1-mock
# on the VM bridge and docker0, port 18080. Scripts turn and w1..w40 each
# run one shell command, then answer.
set -euo pipefail
unit=${1:-a1-mock}
listen=${2:-10.201.0.1:18080,172.17.0.1:18080}
args=()
for s in turn $(seq -f 'w%g' 1 40); do args+=(-script "$s=uptime"); done
sudo systemctl stop "$unit" 2>/dev/null || true
sudo systemd-run -q --unit "$unit" --slice a1.slice --collect -p Type=simple \
  -p StandardOutput=append:/srv/aether/1/logs/$unit.log -p StandardError=append:/srv/aether/1/logs/$unit.log \
  /srv/aether/1/bin/mockmodel -listen "$listen" "${args[@]}"
for _ in $(seq 50); do curl -s -o /dev/null "http://${listen%%,*}/" && exit 0; sleep 0.1; done
echo "mock $unit did not listen" >&2
exit 1
