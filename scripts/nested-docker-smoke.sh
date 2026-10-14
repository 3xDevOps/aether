#!/bin/sh
# Run Aether's own Docker-backed tests inside a Sysbox container: the
# container starts its own dockerd, then the resource-limit tests drive it.
# Needs the sysbox-runc runtime on the host (scripts/install-sysbox.sh).
set -eu
[ "$#" -eq 1 ] || { echo 'usage: nested-docker-smoke.sh <image>' >&2; exit 2; }

docker run --rm --runtime sysbox-runc -v "$PWD:/src" -w /src "$1" sh -euxc '
	dockerd >/var/log/aether-dockerd.log 2>&1 &
	i=0
	until docker version >/dev/null 2>&1; do
		i=$((i + 1))
		[ "$i" -lt 30 ] || { tail -n 50 /var/log/aether-dockerd.log; exit 1; }
		sleep 1
	done
	go test -race -tags integration ./internal/runtime \
		-run "^TestDocker(ResourceLimits|PIDLimitEnforced|OOMContained)$" -v
'
