# Aether build and release automation.
#
# Requires GNU make and Go 1.26+. The Go binaries cross-compile with nothing
# beyond that toolchain (pure Go, CGO_ENABLED=0 throughout), but `make
# release` also builds the dashboard, which needs Bun and Node, and the
# Android APK, which needs Docker.

MODULE  := github.com/3xDevOps/Aether
# Both reach shell command lines - the linker flags here, the Windows resource
# arguments in `release` - and a git tag may legally contain a quote, a
# semicolon or a $. CI builds a pull request's own head with its own tags, so
# filter each where it is read: a hostile tag name truncates instead of
# executing. Anything left empty falls back below.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | tr -cd 'A-Za-z0-9.+_-')
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null | tr -cd 'A-Za-z0-9')
VERSION := $(or $(VERSION),dev)
COMMIT  := $(or $(COMMIT),unknown)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT)

DIST := dist
BUN  := bun
NODE := node

# The Go version go.mod pins: the `toolchain` line when it names one, else the
# `go` directive. `toolchain default` is legal and names no version, so only a
# goX.Y value counts. Assigned lazily - only `lint` reads it.
GO_TOOLCHAIN = $(shell awk ' \
  $$1 == "toolchain" && $$2 ~ /^go[0-9]/ { t = $$2 } \
  $$1 == "go" && $$2 ~ /^[0-9]/ { g = "go" $$2 } \
  END { print (t != "" ? t : g) }' go.mod)

# The packages whose test files carry the `integration` build tag, minus
# INTEGRATION_SKIP. Assigned lazily - only `test-integration` reads it. The
# sed pair is not a no-op: grep implementations differ on whether the paths
# start with ./, and `go test` needs the ./ to see a directory, not a module.
INTEGRATION_PKGS = $(filter-out $(INTEGRATION_SKIP),$(shell \
  grep -rl --include='*_test.go' -E '^//go:build .*integration' . \
  | xargs -n1 dirname | sed -e 's|^\./||' -e 's|^|./|' | sort -u))

# Release matrix. The server is Linux-only by design (see the v1 cut-line);
# the CLI additionally ships for macOS and Windows clients.
SERVER_PLATFORMS := linux/amd64 linux/arm64
CLI_PLATFORMS    := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

# Windows release inputs. The client's PE carries a VERSIONINFO resource, an
# icon and an application manifest. Without them the file names no publisher,
# product or version, and Defender's classifiers score an unsigned,
# metadata-less Go binary as a dropper - the download is blocked and the
# binary is quarantined on first run. See docs/install.md.
#
# goversioninfo writes a .syso the Go linker picks up for the matching
# GOOS/GOARCH, so this needs no Windows toolchain and no cgo.
GOVERSIONINFO  := github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0
WINVERSIONINFO := packaging/windows/versioninfo.json
WINMANIFEST    := packaging/windows/aether.manifest
WINICON        := desktop/build/icon.ico

# A VERSIONINFO resource versions itself with four numbers, so the tag's x.y.z
# is taken and any pre-release suffix dropped; a tree with no release tag in
# reach versions the resource 0.0.0. The full `git describe` output still
# reaches the resource's Comments field, and `aether version` remains the
# authority. Assigned lazily - only `release` reads these.
WIN_VERSION = $(or $(shell printf '%s' '$(VERSION)' | sed -n 's/^v\{0,1\}\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\).*/\1/p'),0.0.0)
WIN_MAJOR   = $(word 1,$(subst ., ,$(WIN_VERSION)))
WIN_MINOR   = $(word 2,$(subst ., ,$(WIN_VERSION)))
WIN_PATCH   = $(word 3,$(subst ., ,$(WIN_VERSION)))

# The Android shell. No runner has an Android SDK and none should install one,
# so the APK is built in a container pinned by digest - the same one a
# contributor builds in. Gradle's dependency cache lives outside the tree and
# survives between builds. See docs/install.md and CONTRIBUTING.md.
ANDROID_IMAGE       := ghcr.io/cirruslabs/android-sdk@sha256:f9b3ea9ed2b5fc9522adae82c7b4622ab7aa54207ef532c8e615a347dca08f31
ANDROID_GRADLE_HOME ?= $(HOME)/.cache/aether/android-gradle
ANDROID_APKSIGNER   := /opt/android-sdk-linux/build-tools/36.0.0/apksigner

# The container runs as the invoking user so build outputs are not root-owned,
# which leaves HOME unwritable - Gradle needs both HOME and GRADLE_USER_HOME
# pointed at the mounted cache.
ANDROID_RUN = docker run --rm \
	-u $$(id -u):$$(id -g) \
	-v '$(CURDIR)/android':/src -w /src \
	-v '$(ANDROID_GRADLE_HOME)':/gradle \
	-e HOME=/gradle -e GRADLE_USER_HOME=/gradle

# Android installs an update only when its versionCode is above the installed
# one, so the number has to rise with every release. A commit count does not,
# across branches: a hotfix tagged off a shorter branch scores below the
# release it fixes. scripts/android-version-code.sh derives it from the tag
# instead and carries the formula and its ceilings. An unsigned or debug APK
# can never install over a release, so an untagged tree builds code 1 rather
# than failing; a signed build refuses a tag the script cannot parse, in the
# recipe below. Assigned lazily - only `android` reads it.
ANDROID_VERSION_CODE = $(or $(shell sh scripts/android-version-code.sh '$(VERSION)' 2>/dev/null),1)

# versionName is a label Android never compares, but Play prints it in the
# store listing and Android prints it in the phone's app info, where the tag's
# leading v reads as part of the number.
ANDROID_VERSION_NAME = $(VERSION:v%=%)

# Release signing comes from the environment, never the tree: a keystore path
# and the three secrets beside it. Without them the APK and the bundle come
# out unsigned and are named for it, so a PR's build can never be mistaken for
# a release asset. The release workflow refuses to run at all when the secrets
# are missing.
ifneq ($(ANDROID_KEYSTORE_FILE),)
ANDROID_SIGNING := -v '$(ANDROID_KEYSTORE_FILE)':/keystore:ro \
	-e ANDROID_KEYSTORE_FILE=/keystore \
	-e ANDROID_KEYSTORE_PASSWORD -e ANDROID_KEY_ALIAS -e ANDROID_KEY_PASSWORD
ANDROID_APK   := aether-android.apk
ANDROID_AAB   := aether-android.aab
ANDROID_BUILT := app-release.apk
else
ANDROID_SIGNING :=
ANDROID_APK   := aether-android-unsigned.apk
ANDROID_AAB   := aether-android-unsigned.aab
ANDROID_BUILT := app-release-unsigned.apk
endif

.PHONY: all build test test-integration test-e2e test-scripts vet lint vulncheck fmt-check public-audit dashboard android android-debug release deploy clean

all: build

build: dashboard
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(DIST)/ ./cmd/aether-server ./cmd/aether

test:
	go test -race ./...

# The `integration`-tagged tests (real Docker, real git), in the packages that
# carry them - the unit tests are `make test`'s job. CI shards it: set
# INTEGRATION_PKGS to run one package, INTEGRATION_SKIP to run all but some.
test-integration:
	go test -race -tags integration $(INTEGRATION_PKGS)

# The dashboard end-to-end suite drives the built SPA in a real browser
# against a real `aether gui` gateway and a real aether-server, so it runs on
# the binaries `build` produces: the CLI serves the dashboard out of its own
# embedded web/dist. It needs Docker, real git, and Playwright's browser
# (`cd web && bunx playwright install chromium`, once).
test-e2e: build
	cd web && $(BUN) run test:e2e

# The shell scripts in scripts/ have hermetic tests of their own: every
# external command they call is stubbed, so nothing here touches the network,
# a real host, or a real release.
test-scripts:
	sh scripts/install-test.sh
	sh scripts/deploy-test.sh
	sh scripts/publish-release-test.sh
	sh scripts/android-version-code-test.sh
	sh scripts/android-verify-signature-test.sh

vet:
	go vet ./...

# golangci-lint cannot decode export data produced by a Go newer than the one
# that built it, so on a host whose default Go is ahead of go.mod's toolchain
# it fails before linting anything. Run it under the toolchain go.mod pins,
# which is what CI lints with too: setup-go installs the `go` directive's
# version and the default GOTOOLCHAIN=auto re-execs into the `toolchain` line.
# Go downloads that toolchain on demand, so no local setup is needed.
lint:
	GOTOOLCHAIN=$(or $(GO_TOOLCHAIN),$(error go.mod has no go or toolchain version to pin the linter to)) \
		go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10.1 run

# Advisory: the two Moby CVEs reachable through the Docker SDK have no fixed
# release, and govulncheck has no suppression flag, so this target exits
# non-zero until upstream ships a fix. See docs/security.md.
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

public-audit:
	sh scripts/public-audit.sh

# The server binary embeds web/dist (web/embed.go), so the static dashboard
# export is built before Go compiles. Bun installs dependencies; Node runs the
# Next build.
# The Android shell's unit tests, release APK and app bundle, built in the
# pinned SDK container. The APK is for direct installs; the bundle is what
# Google Play takes (docs/install.md). Both are signed with the same key.
# scripts/android-verify-signature.sh runs in the same container, which is
# where apksigner, jarsigner and keytool live, and is given the keystore the
# build was handed so it can check who actually signed the two artifacts.
android:
	@mkdir -p $(DIST) '$(ANDROID_GRADLE_HOME)'
	@if [ -n '$(ANDROID_KEYSTORE_FILE)' ] && [ ! -f '$(ANDROID_KEYSTORE_FILE)' ]; then \
		echo 'make android: ANDROID_KEYSTORE_FILE does not name a file: $(ANDROID_KEYSTORE_FILE)' >&2; \
		exit 1; \
	fi
	@[ -z '$(ANDROID_SIGNING)' ] || sh scripts/android-version-code.sh '$(VERSION)' >/dev/null
	$(ANDROID_RUN) $(ANDROID_SIGNING) $(ANDROID_IMAGE) \
		./gradlew --console=plain test assembleRelease bundleRelease \
			'-PaetherVersionName=$(ANDROID_VERSION_NAME)' -PaetherVersionCode=$(ANDROID_VERSION_CODE)
	cp android/app/build/outputs/apk/release/$(ANDROID_BUILT) $(DIST)/$(ANDROID_APK)
	cp android/app/build/outputs/bundle/release/app-release.aab $(DIST)/$(ANDROID_AAB)
	@if [ -n '$(ANDROID_SIGNING)' ]; then \
		docker run --rm -v '$(CURDIR)/$(DIST)':/dist:ro \
			-v '$(CURDIR)/scripts':/scripts:ro $(ANDROID_SIGNING) \
			-e APKSIGNER=$(ANDROID_APKSIGNER) $(ANDROID_IMAGE) \
			sh /scripts/android-verify-signature.sh /dist/$(ANDROID_APK) /dist/$(ANDROID_AAB) || exit 1; \
	else \
		echo 'make android: no ANDROID_KEYSTORE_FILE in the environment, so $(DIST)/$(ANDROID_APK) and $(DIST)/$(ANDROID_AAB) are unsigned and cannot be installed or released'; \
	fi

# A debuggable APK for a real phone. Gradle signs it with its own debug key,
# so `adb install` takes it and `chrome://inspect` can attach to its WebView -
# neither of which an unsigned release APK allows. It never updates over a
# released install, and a released APK never updates over it.
# See docs/dashboard-frontend.md.
android-debug:
	@mkdir -p $(DIST) '$(ANDROID_GRADLE_HOME)'
	$(ANDROID_RUN) $(ANDROID_IMAGE) ./gradlew --console=plain assembleDebug
	cp android/app/build/outputs/apk/debug/app-debug.apk $(DIST)/aether-android-debug.apk
	@echo 'built $(DIST)/aether-android-debug.apk; install it with: adb install -r $(DIST)/aether-android-debug.apk'

dashboard:
	@command -v $(BUN) >/dev/null 2>&1 || { \
		echo "make dashboard: $(BUN) not found - install Bun 1.3+ (https://bun.sh) to build the dashboard in web/"; \
		exit 1; \
	}
	@command -v $(NODE) >/dev/null 2>&1 || { \
		echo "make dashboard: $(NODE) not found - install Node.js 22+ to build the Next dashboard in web/"; \
		exit 1; \
	}
	cd web && $(BUN) install --frozen-lockfile && $(BUN) run build

# Dev loop only: build the server for the target machine, install it, and
# restart the systemd service. Deliberately skips tests and CI - releases
# remain the quality gate. DEPLOY_HOST=user@server deploys remotely; unset
# deploys to this machine. See CONTRIBUTING.md.
deploy: dashboard
	sh scripts/deploy.sh

release: dashboard
	@mkdir -p $(DIST)
	@for platform in $(SERVER_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=$(DIST)/aether-server-$$os-$$arch; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/aether-server || exit 1; \
	done
	@trap 'rm -f cmd/aether/resource_windows_*.syso' EXIT; \
	for arch in amd64 arm64; do \
		if [ $$arch = arm64 ]; then armflag=-arm; else armflag=; fi; \
		echo "generating cmd/aether/resource_windows_$$arch.syso"; \
		go run $(GOVERSIONINFO) -64 $$armflag \
			-icon $(WINICON) -manifest $(WINMANIFEST) \
			-ver-major $(WIN_MAJOR) -ver-minor $(WIN_MINOR) -ver-patch $(WIN_PATCH) -ver-build 0 \
			-product-ver-major $(WIN_MAJOR) -product-ver-minor $(WIN_MINOR) -product-ver-patch $(WIN_PATCH) -product-ver-build 0 \
			-file-version '$(WIN_VERSION).0' -product-version '$(WIN_VERSION).0' \
			-comment '$(VERSION)' \
			-o cmd/aether/resource_windows_$$arch.syso $(WINVERSIONINFO) || exit 1; \
	done; \
	for platform in $(CLI_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=$$([ $$os = windows ] && echo .exe || echo ""); \
		out=$(DIST)/aether-$$os-$$arch$$ext; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/aether || exit 1; \
	done
	@$(MAKE) android

clean:
	rm -rf $(DIST) cmd/aether/resource_windows_*.syso \
		android/.gradle android/build android/app/build
