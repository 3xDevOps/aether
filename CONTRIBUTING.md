# Contributing to Aether

Thanks for contributing. Aether is a self-hosted development environment for
coding agents, built as a Go server, a Go CLI, and an embedded web dashboard.

## Before you write code

1. Read the relevant guide in `docs/` and the package you will change.
2. Search for an existing helper or dependency before adding one.
3. Keep credentials and local server state outside the checkout.
4. Update public documentation when commands, defaults, or behavior change.

## Toolchain

| Need | Version | Use |
| --- | --- | --- |
| Go | 1.25+ | Server and CLI |
| GNU make | any recent | Build and checks |
| Bun | 1.3+ | Dashboard build and tests |
| Docker | recent | Integration tests and server runtime |
| git | recent | Integration tests and workspace transport |
| Node | 22+ | Building desktop installers from `desktop/` by hand (optional) |

SQLite is pure Go and the project builds with `CGO_ENABLED=0`.

```sh
git clone https://github.com/3xDevOps/Aether
cd Aether
make build
```

## Checks

Run the fast checks before opening a change:

```sh
make fmt-check
make vet
make lint
make test
make public-audit
```

The integration suite uses real Docker and git:

```sh
make test-integration
```

The dashboard checks run from `web/`:

```sh
bun install --frozen-lockfile
bun run typecheck
bun run test
```

### Desktop shell

The optional Electron shell in `desktop/` wraps `aether gui` in a window. The
dashboard SPA itself is embedded in the `aether` CLI (`web/embed.go`), so this
package is just a sidecar launcher. Users build and install it with
`aether gui build`, which unpacks the sources embedded by `desktop/embed.go`
and runs electron-builder's unpacked target; see
[docs/install.md](docs/install.md#desktop-app). That command needs no Node.js
on the user's machine - it downloads a pinned copy when there is none. A new
file in `desktop/` that the shell needs at runtime must be added to both
`desktop/embed.go` and the `files` list in `electron-builder.yml`.

Installers come from a checkout, which is where Node 22+ on `PATH` is
required:

```sh
cd desktop
npm install
npm run dist   # installer for this OS into desktop/dist/
```

`npm run dist` packages only the current platform's targets. Installers for
every platform need one machine per platform (a CI matrix) or the cross-build
routes below.

| Artifact | On Linux | On macOS | On Windows |
| --- | --- | --- | --- |
| Linux `.AppImage`, `.deb` | `npm run dist -- --linux` | Docker image | Docker image |
| Windows `.exe` (NSIS) | Docker image | native tooling | `npm run dist -- --win` |
| macOS `.zip` | `npm run dist -- --mac zip` | `npm run dist -- --mac` | Docker image |
| macOS `.dmg` | macOS only | `npm run dist -- --mac` | macOS only |

The Docker image is electron-builder's own Wine image, so a Linux box can
produce Linux and Windows installers plus an unsigned macOS zip:

```sh
npm run dist -- --linux --mac zip   # AppImage, deb, macOS zip

mkdir -p ~/.cache/aether-desktop-build
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/home/builder \
  -v "$PWD:/project" \
  -v "$HOME/.cache/aether-desktop-build:/home/builder" \
  electronuserland/builder:wine \
  npx electron-builder --win --publish never   # NSIS .exe
```

Run the container as your own user with a writable `HOME` on a cache directory:
as root it writes root-owned files into `dist/` and the electron-builder
download cache, breaking later builds. The cache mount also avoids
re-downloading the ~100 MB Electron runtime each time.

Two limits are not worked around. A `.dmg` needs macOS (its `dmg-license`
module is macOS-only), so other hosts produce only the macOS `.zip` - both
install the same `Aether.app`. And signing needs the target OS plus a
certificate: cross-built Windows and macOS artifacts are unsigned, trip
SmartScreen and Gatekeeper, and auto-update refuses them. Ship signed builds
from real runners; treat cross-builds as test artifacts.

The app icon is generated from `web/public/aether-mark.png` into
`desktop/build/`; regenerate with `python3 desktop/build/make-icons.py` after
the mark changes.

## Development deploy

`make deploy` is the fast loop for testing server changes on a real machine:
it builds the dashboard and the server binary for the target's architecture,
installs it to `/usr/local/bin`, and restarts the `aether-server` systemd
service. The first deploy to a machine without the unit installs and enables
`packaging/systemd/aether-server.service`.

```sh
make deploy                          # this machine
DEPLOY_HOST=user@server make deploy  # remote over SSH
```

A remote deploy authenticates SSH once (connection multiplexing) and prompts
for sudo at most once. It deliberately skips tests and CI; releases stay the
quality gate for
published binaries. Deployed dev builds report their `git describe` version
and pin the neutral bootstrap image of the nearest release tag.

## Testing

Prefer a test that follows the real user path. Integration tests are valuable
because the project risk is the connection between SSH, git, containers,
storage, and the dashboard. Do not add tests for language behavior, trivial
getters, or the same contract at several layers.

## Code style

- Keep files focused and Go files below 1000 lines where practical.
- Wrap errors with context and return them.
- Use comments for constraints and decisions, not for restating code.
- Prefer standard-library and existing dependency functionality.
- Do not add speculative abstractions, configuration, or compatibility paths.

## Documentation

Public documentation lives in `docs/`. Root-level `README.md` and this file
are entry points and should link to the detailed guides rather than duplicate
them. Never commit host keys, invite codes, profile homes, transcripts, runtime
state, or real credentials.

## Security

Do not open public issues for sensitive reports. Use the repository's private
security reporting channel and include a reproducible explanation without
including live credentials.

## Releases

Release tags are built by the repository workflow. Keep the installer,
`aether update` (`internal/selfupdate`), release asset names, checksums, and
documentation synchronized when changing packaging.
