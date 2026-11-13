#!/usr/bin/env bash
# Q4: does a docker compose stack (two containers) and a dev server in the
# VM survive a save and restore? Runs on the KVM host. VM 7 runs dockerd;
# the stack is redis plus nginx publishing 8080; a node dev server listens
# on 3000. The VM sleeps SLEEP seconds between save and restore.
set -euo pipefail
A1=/srv/aether/1
M=$A1/m
V=${V:-27}
D=$A1/vms/vm$V
SNAP=$A1/snap/q4
IP=10.201.0.$((10 + V))
OUT=$A1/results/q4-${LABEL:-default}
rm -rf "$OUT" && mkdir -p "$OUT"
exec > >(tee "$OUT/log.txt") 2>&1
export MEM=${MEM:-2G} DOCKER=1 HOME_FS=${HOME_FS:-0}
g() { "$M/host/vm.sh" ssh $V "$@"; }
check() {
  local out
  if out=$(g "$2" 2>&1); then echo "ok   $1: $out"; else echo "FAIL $1: $out"; fi
}
state() { g 'cd /workspace/stack && docker compose ps --format "{{.Name}} {{.State}} {{.Status}}"; docker inspect -f "{{.Name}} started={{.State.StartedAt}} restarts={{.RestartCount}}" $(docker compose ps -q); echo "dev server pid $(pgrep -f a1-dev)"'; }

trap '"$M/host/vm.sh" stop $V' EXIT
"$M/host/vm.sh" stop $V
sudo rm -rf "$D" && mkdir -p "$D" && sudo mkdir -p "$D/home"
"$M/host/vm.sh" start $V
g 'for _ in $(seq 100); do docker info >/dev/null 2>&1 && break; sleep 0.2; done; docker version -f "dockerd {{.Server.Version}}"'
g 'mkdir -p /workspace/stack && cat >/workspace/stack/compose.yaml <<EOF
services:
  cache:
    image: redis:7-alpine
  web:
    image: nginx:alpine
    ports: ["8080:80"]
    depends_on: [cache]
EOF
cd /workspace/stack && docker compose up -d --quiet-pull 2>&1 | tail -n 2
docker compose exec -T cache redis-cli set a1key before-save
nohup node -e "require(\"http\").createServer((q, s) => s.end(\"dev \" + process.pid + \"\\n\")).listen(3000)" a1-dev >/dev/null 2>&1 &
sleep 1'
echo "before save:"; state
curl -s -m 3 -o /dev/null -w "host -> nginx :8080 %{http_code}\n" "http://$IP:8080/"
curl -s -m 3 "http://$IP:3000/" | sed 's/^/host -> dev server: /'

sudo rm -rf "$SNAP" && sudo mkdir -p "$SNAP"
sudo ch-remote --api-socket /run/a1/vm$V/api.sock pause
sudo ch-remote --api-socket /run/a1/vm$V/api.sock snapshot "file://$SNAP"
"$M/host/vm.sh" stop $V
echo "saved: $(sudo du -sh "$SNAP" | cut -f1); sleeping ${SLEEP:-30}s"
sleep "${SLEEP:-30}"
RESTORE_MODE=${RESTORE_MODE:-copy} "$M/host/vm.sh" restore $V "$SNAP"
"$M/host/vm.sh" wait-ssh $V
echo "after restore:"; state
check "redis keeps its data" 'cd /workspace/stack && docker compose exec -T cache redis-cli get a1key'
check "nginx answers in the VM" 'curl -s -m 3 -o /dev/null -w "%{http_code}" http://127.0.0.1:8080/'
check "web reaches cache by name" 'cd /workspace/stack && docker compose exec -T web sh -c "nc -w 2 cache 6379 </dev/null && echo reachable || getent hosts cache"'
check "dev server answers in the VM" 'curl -s -m 3 http://127.0.0.1:3000/'
check "new container starts" 'docker run --rm redis:7-alpine redis-cli -v'
check "guest clock vs host" "echo guest \$(date +%s) host $(date +%s)"
curl -s -m 3 -o /dev/null -w "host -> nginx :8080 after restore %{http_code}\n" "http://$IP:8080/" || echo "FAIL host -> nginx after restore"
curl -s -m 3 "http://$IP:3000/" | sed 's/^/host -> dev server after restore: /' || echo "FAIL host -> dev server after restore"
