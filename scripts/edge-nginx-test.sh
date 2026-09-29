#!/bin/sh
# packaging/nginx/aether-edge.conf.example in front of a real edge, with a
# real nginx: TestBehindNginx in internal/edge/edgetest. It needs nginx on
# PATH and skips without it.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if ! command -v nginx >/dev/null 2>&1; then
	echo "edge-nginx-test: skipped: nginx is not on PATH (on Debian or Ubuntu: sudo apt-get install nginx)" >&2
	exit 0
fi
cd "$script_dir/.."
go test -tags nginx -count=1 -run '^TestBehindNginx$' -v ./internal/edge/edgetest/
