#!/bin/sh
# TEMPORARY: CI verification of Docker inside runs; removed before merge.
set -eux
sudo sh scripts/install-sysbox.sh
sudo sh scripts/install-sysbox.sh
sudo env "PATH=$PATH" "HOME=$HOME" AETHER_NESTED_DOCKER_TEST_IMAGE=aether-standard:ci \
	go test -race -timeout=20m -tags integration ./internal/runtime -run '^TestDockerNestedDocker$' -v
sudo sh scripts/nested-docker-smoke.sh aether-standard:ci
start='dockerd >/var/log/d.log 2>&1 & i=0; until docker version >/dev/null 2>&1; do i=$((i+1)); [ $i -lt 30 ] || { tail -50 /var/log/d.log; exit 1; }; sleep 1; done'
if [ "$(dpkg --print-architecture)" = amd64 ]; then
	make browser-image
	sudo env "PATH=$PATH" "HOME=$HOME" AETHER_NESTED_DOCKER_TEST_IMAGE=aether-standard:ci AETHER_BROWSER_TEST_IMAGE=aether/browser:test AETHER_BROWSER_IMAGE=aether/browser:test \
		go test -race -timeout=15m -tags integration ./internal/runtime -run '^TestDockerBrowserJoinsNestedDockerRun$' -v
	sudo docker run --rm --runtime sysbox-runc -v "$PWD:/src" -w /src aether-standard:ci sh -euxc "$start
		git config --global --add safe.directory '*'
		make android
		ls -la dist/
		make test-integration INTEGRATION_PKGS=./internal/runtime INTEGRATION_SKIP_PATTERN='^TestDockerBrowser|^TestDockerNestedDocker'"
else
	sudo docker run --rm --runtime sysbox-runc -v "$PWD:/src" -w /src aether-standard:ci sh -euxc "$start
		git config --global --add safe.directory '*'
		apt-get update -qq && apt-get install -y -qq unzip >/dev/null
		curl -fsSL https://bun.sh/install | bash -s bun-v\$(cat web/.bun-version)
		export PATH=\$HOME/.bun/bin:\$PATH
		(cd web && bun install --frozen-lockfile && bunx playwright install --with-deps chromium)
		docker build -q -f images/standard/Dockerfile -t aether-standard:ci .
		AETHER_E2E_STANDARD_IMAGE=aether-standard:ci CI=1 make test-e2e E2E_ARGS='--shard=1/4 --project=chromium'"
fi
