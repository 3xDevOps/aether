# Aether build and release automation.
#
# Requires GNU make and Go 1.25+. Release cross-compilation needs nothing
# beyond the Go toolchain (pure Go, CGO_ENABLED=0 throughout).

MODULE  := github.com/3xDevOps/Aether
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT)

DIST := dist
BUN  := bun

# Release matrix. The server is Linux-only by design (see the v1 cut-line);
# the CLI additionally ships for macOS and Windows clients.
SERVER_PLATFORMS := linux/amd64 linux/arm64
CLI_PLATFORMS    := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build test test-integration vet lint vulncheck fmt-check public-audit dashboard release deploy clean

all: build

build: dashboard
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(DIST)/ ./cmd/aether-server ./cmd/aether

test:
	go test -race ./...

# Integration tests are opt-in (they need real Docker / real git); they are
# tagged `integration` and skipped by the plain `test` target.
test-integration:
	go test -race -tags integration ./...

vet:
	go vet ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.4.0 run

# Advisory: the two Moby CVEs reachable through the Docker SDK have no fixed
# release, and govulncheck has no suppression flag, so this target exits
# non-zero until upstream ships a fix. See docs/security.md.
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

public-audit:
	sh scripts/public-audit.sh

# The server binary embeds web/dist (web/embed.go), so the SPA is built before
# Go compiles. Bun installs and runs the scripts; Vite is the bundler.
dashboard:
	@command -v $(BUN) >/dev/null 2>&1 || { \
		echo "make dashboard: $(BUN) not found - install Bun 1.3+ (https://bun.sh) to build the dashboard SPA in web/"; \
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
	@for platform in $(CLI_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=$$([ $$os = windows ] && echo .exe || echo ""); \
		out=$(DIST)/aether-$$os-$$arch$$ext; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/aether || exit 1; \
	done

clean:
	rm -rf $(DIST)
