#!/bin/sh
# Smoke test of a built aether-edge image (images/edge/Dockerfile):
#
#   sh scripts/edge-image-smoke.sh <image> [<runtime>]
#
# <runtime> is the container command, docker by default; it may carry global
# options, as in "podman --storage-driver vfs". The edge runs behind a proxy
# (--proxy-listen) with fake GitHub credentials on a port published on
# 127.0.0.1, so nothing leaves this machine; curl plays the proxy. Every
# container and volume it creates is removed on exit.
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
	echo "usage: edge-image-smoke.sh <image> [<runtime>]" >&2
	exit 2
fi
image=$1
runtime=${2:-docker}
command -v curl >/dev/null 2>&1 || { echo "edge-image-smoke: curl is not on PATH" >&2; exit 2; }

name=aether-edge-smoke-$$
work=$(mktemp -d)
containers=
volumes=
cleanup() {
	for c in $containers; do
		# shellcheck disable=SC2086 # $runtime may carry options
		$runtime rm -f "$c" >/dev/null 2>&1 || true
	done
	for v in $volumes; do
		# shellcheck disable=SC2086
		$runtime volume rm -f "$v" >/dev/null 2>&1 || true
	done
	rm -rf "$work"
}
trap cleanup EXIT

rt() {
	# shellcheck disable=SC2086
	$runtime "$@"
}

fail() {
	echo "edge-image-smoke: FAIL: $*" >&2
	exit 1
}

ok() {
	echo "edge-image-smoke: ok: $*"
}

fake_secret=fake-github-client-secret-for-the-smoke-test
printf '%s\n' "$fake_secret" >"$work/github-client-secret"
chmod 0444 "$work/github-client-secret"

# 1. A fixed non-root numeric user, and no shell.
user=$(rt image inspect --format '{{.Config.User}}' "$image")
uid=${user%%:*}
case $uid in
'' | *[!0-9]*) fail "the image's user is \"$user\", not a numeric uid" ;;
0) fail "the image runs as root ($user)" ;;
esac
ok "runs as uid $uid"

for sh in /bin/sh /bin/bash /bin/ash /busybox/sh; do
	status=0
	rt run --rm --entrypoint "$sh" "$image" -c true >/dev/null 2>&1 || status=$?
	[ "$status" -eq 127 ] || fail "--entrypoint $sh exited $status, want 127 (not found)"
done
ok "no shell"

healthcheck=$(rt image inspect --format '{{json .Config.Healthcheck}}' "$image")
case $healthcheck in
*'"healthcheck"'*) ok "declares HEALTHCHECK aether-edge healthcheck" ;;
*) fail "the image declares no aether-edge healthcheck: $healthcheck" ;;
esac

# 2. Without configuration it refuses to start and says why.
status=0
rt run --rm "$image" >"$work/out" 2>&1 || status=$?
if [ "$status" -ne 1 ] || ! grep -q -- '--signin-origin must be https://host\[:port\], not ""; set it or AETHER_EDGE_SIGNIN_ORIGIN' "$work/out"; then
	fail "with no configuration: exit $status, want 1 naming --signin-origin: $(cat "$work/out")"
fi
ok "no configuration: $(cat "$work/out")"

# start <container> <volume> runs the edge with GitHub-only configuration
# and waits until its healthcheck passes.
start() {
	containers="$containers $1"
	rt run -d --name "$1" \
		-e AETHER_EDGE_SIGNIN_ORIGIN=https://auth.example.test \
		-e AETHER_EDGE_RELAY_ORIGIN=https://edge.example.test \
		-e AETHER_EDGE_PROXY_LISTEN=:8443 \
		-e AETHER_EDGE_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.0/8,fc00::/7,::1/128 \
		-e AETHER_EDGE_GITHUB_CLIENT_ID=fake-github-client-id \
		-e AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE=/run/secrets/github-client-secret \
		-v "$work/github-client-secret:/run/secrets/github-client-secret:ro" \
		-v "$2:/var/lib/aether-edge" \
		-p 127.0.0.1::8443 \
		"$image" >/dev/null
	i=0
	until rt exec "$1" /usr/local/bin/aether-edge healthcheck >"$work/health" 2>&1; do
		i=$((i + 1))
		if [ "$i" -ge 30 ] || [ "$(rt inspect --format '{{.State.Running}}' "$1")" != true ]; then
			fail "$1 is not healthy: $(cat "$work/health")
$(rt logs "$1" 2>&1)"
		fi
		sleep 1
	done
}

# fingerprint <container> prints the edge key's fingerprint from the
# relay origin's public metadata, sending what a proxy sends.
fingerprint() {
	addr=$(rt port "$1" 8443/tcp | head -n 1)
	code=$(curl -sS -o "$work/edge.json" -w '%{http_code}' \
		-H 'Host: edge.example.test' -H 'X-Forwarded-For: 192.0.2.10' -H 'Aether-Edge-Version: 1' \
		"http://$addr/v1/edge") || fail "GET /v1/edge on $addr"
	[ "$code" = 200 ] || fail "GET /v1/edge answered $code: $(cat "$work/edge.json")"
	fp=$(sed -n 's/.*"fingerprint":"\(SHA256:[^"]*\)".*/\1/p' "$work/edge.json")
	[ -n "$fp" ] || fail "no fingerprint in $(cat "$work/edge.json")"
	printf '%s\n' "$fp"
}

# stop <container> stops it as the runtime does, with SIGTERM and a 30
# second grace period, and wants exit status 0.
stop() {
	begin=$(date +%s)
	rt stop -t 30 "$1" >/dev/null
	took=$(($(date +%s) - begin))
	code=$(rt inspect --format '{{.State.ExitCode}}' "$1")
	[ "$code" = 0 ] || fail "$1 exited $code after SIGTERM, $took s: $(rt logs "$1" 2>&1)"
	if rt logs "$1" 2>&1 | grep -qF "$fake_secret"; then
		fail "the client secret is in the log of $1"
	fi
	ok "$1 stopped on SIGTERM in ${took}s with status 0"
}

volumes="$name-a $name-b"
rt volume create "$name-a" >/dev/null
rt volume create "$name-b" >/dev/null

# 3. GitHub-only configuration starts, passes its healthcheck and stops
# cleanly; the signing key outlives the container with its volume.
start "$name-1" "$name-a"
ok "GitHub-only configuration is healthy"
first=$(fingerprint "$name-1")
stop "$name-1"
rt rm "$name-1" >/dev/null

start "$name-2" "$name-a"
second=$(fingerprint "$name-2")
stop "$name-2"
[ "$second" = "$first" ] || fail "same volume, new container: fingerprint $second, want $first"
ok "same volume keeps the edge key $first"

start "$name-3" "$name-b"
third=$(fingerprint "$name-3")
stop "$name-3"
[ "$third" != "$first" ] || fail "a new volume kept the edge key $first"
ok "a new volume makes a new edge key $third"
