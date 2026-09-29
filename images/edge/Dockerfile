# The aether-edge image: docs/edge.md, "In a container". Build it from the
# repository root:
#
#   docker build -f images/edge/Dockerfile --build-arg VERSION=<tag> --build-arg COMMIT=<sha> .
#
# Dockerfile.dockerignore beside this file limits the context to the Go
# module, so nothing else in a working tree reaches the builder.

# The build stage runs on the builder's own platform and cross-compiles, so
# an arm64 image needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.8-trixie@sha256:bdca99a00bc16590cb1a0bb4e698f5fc5d6a64e4d5eef13d9f18a0ee08e5fa65 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/aether-edge cmd/aether-edge
COPY internal internal
# The same stamping as the Makefile, which filters both values the same way.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    version=$(printf '%s' "$VERSION" | tr -cd 'A-Za-z0-9.+_-') && \
    commit=$(printf '%s' "$COMMIT" | tr -cd 'A-Za-z0-9') && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/3xDevOps/Aether/internal/version.Version=${version:-dev} -X github.com/3xDevOps/Aether/internal/version.Commit=${commit:-unknown}" \
      -o /out/aether-edge ./cmd/aether-edge && \
    mkdir -p /out/state && mkdir -m 0700 /out/state/aether-edge

# CA certificates for GitHub's API, a passwd entry for uid 65532, and no
# shell or package manager.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
LABEL org.opencontainers.image.title="aether-edge" \
      org.opencontainers.image.description="Aether edge: GitHub sign-in and relay for Aether servers" \
      org.opencontainers.image.source="https://github.com/3xDevOps/Aether" \
      org.opencontainers.image.licenses="GPL-3.0"
COPY --from=build /out/aether-edge /usr/local/bin/aether-edge
# Copied as an entry of its parent: copying the directory itself would
# create the destination 0755 and drop its mode.
COPY --from=build --chown=65532:65532 /out/state/ /var/lib/
USER 65532:65532
VOLUME /var/lib/aether-edge
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
    CMD ["/usr/local/bin/aether-edge", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/aether-edge"]
CMD ["serve"]
