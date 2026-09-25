# Build from repository root: docker build -f images/browser/Dockerfile .
# Manifest digest verified against registry-1.docker.io on 2026-09-25.
FROM node:24.20.0-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e

ENV PLAYWRIGHT_BROWSERS_PATH=/opt/playwright \
    HOME=/tmp/aether-browser-home \
    NODE_ENV=production
WORKDIR /opt/aether-browser
COPY images/browser/package.json images/browser/package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts \
    && npx --no-install playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/* /root/.npm \
    && chmod -R a+rX /opt/playwright
COPY images/browser/*.mjs ./
COPY web/public/fonts/jetbrains-mono-nfm-regular.woff2 web/public/fonts/jetbrains-mono-nfm-bold.woff2 web/public/fonts/LICENSE-jetbrains-mono-nfm.txt ./fonts/
# Runtime supplies private /tmp and /dev/shm, and the host-only control bind.
# Chromium uses its user-namespace sandbox and debugging pipe, never a TCP port.
USER 1000:1000
ENTRYPOINT ["node", "/opt/aether-browser/server.mjs"]
