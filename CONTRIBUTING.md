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
| Docker | recent | Integration tests, server runtime, and the Android APK build |
| git | recent | Integration tests and workspace transport |
| Node | 22+ | Next dashboard build and dev server, plus desktop installers from `desktop/` |

SQLite is pure Go and the project builds with `CGO_ENABLED=0`.

Node.js 22+ must be on `PATH` for the Next build and development server:
`make build` (or `make dashboard`) and `cd web && bun run dev`. It is a
source-build prerequisite, not only an optional desktop-installer tool.
Production uses a static Next export in `web/dist`, embedded into the Go
server and CLI, so running those binaries needs no Node.js or Next server.

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

The Android shell has its own build and tests, in a container:

```sh
make android
```

`make lint` runs golangci-lint under the toolchain pinned in `go.mod`, the same
Go version CI uses. Without that pin, a host whose default Go is newer fails
with `export data version 4 is greater than maximum supported version 2`
before linting anything.

The integration suite uses real Docker and git:

```sh
make test-integration
```

The dashboard end-to-end suite drives a real browser against a real
`aether gui` gateway and a real server. It needs Docker, real git, and
Playwright's browser:

```sh
(cd web && bunx playwright install chromium)   # once, from the repo root
make test-e2e
```

The dashboard checks run from `web/`:

```sh
bun install --frozen-lockfile
bun run typecheck
bun run test
```

### Desktop shell

The optional Electron shell in `desktop/` wraps `aether gui` in a window. The
production dashboard is a static Next export embedded in the `aether` CLI
(`web/embed.go`), so this package is just a sidecar launcher. Users build and
install it with `aether gui build`, which unpacks the sources embedded by
`desktop/embed.go` and runs electron-builder's unpacked target; see
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

Every icon Aether ships is generated from `web/public/aether-mark.png` - the
desktop app's into `desktop/build/`, the web app manifest's and the iOS
home-screen one into `web/public/icons/`, and the phone app's launcher icons
into `android/app/src/main/res/mipmap-*`. Regenerate them all with `python3
scripts/make-icons.py` after the mark changes, and commit what it wrote.

### Android shell

`android/` is a Kotlin WebView shell around the server-hosted dashboard; see
[docs/install.md](docs/install.md#android-app) for what it does. No Android
SDK is installed on any machine, contributor or runner. The APK is built in a
container the `Makefile` pins by digest (`ghcr.io/cirruslabs/android-sdk:36`:
JDK 21, the API 36 platform, build-tools 36.0.0, licenses pre-accepted), which
is what CI and the release job use too, so a local build and a released one
run the same toolchain:

```sh
make android
```

That runs the app's JUnit tests and writes `dist/aether-android-unsigned.apk`.
With signing variables in the environment it writes `dist/aether-android.apk`
instead and fails unless `apksigner verify` passes:

```sh
ANDROID_KEYSTORE_FILE=/path/outside/the/checkout/throwaway.jks \
ANDROID_KEYSTORE_PASSWORD=... ANDROID_KEY_ALIAS=... ANDROID_KEY_PASSWORD=... \
  make android
```

Generate a throwaway keystore with `keytool -genkeypair -keyalg RSA -keysize
2048` and keep it outside the checkout. `keytool` writes PKCS12, which holds
one password for the store and the key, so `ANDROID_KEY_PASSWORD` is the same
string as `ANDROID_KEYSTORE_PASSWORD` unless the keystore was made as JKS
(`-storetype JKS`). The release keystore is never on a developer machine: the
release workflow decodes it from `ANDROID_KEYSTORE_B64` into the runner's temp
directory and deletes it afterwards
([docs/install.md](docs/install.md#releases)).

A signed build also needs a release tag, because the APK's versionCode comes
from it (`scripts/android-version-code.sh`, and
[docs/install.md](docs/install.md#releases) for the formula): signing an
untagged tree stops with the tag the script could not parse. An unsigned or
debug APK builds versionCode 1 from any tree, which is honest - neither can
install over a release anyway. `sh scripts/android-version-code-test.sh`,
part of `make test-scripts`, covers the mapping and its ordering.

`make release` builds the APK as part of the matrix, so it needs Docker as
well as Go, Node and Bun.

Gradle's caches live in `~/.cache/aether/android-gradle`; set
`ANDROID_GRADLE_HOME` to move them. The wrapper and its distribution checksum
are committed, and every plugin and library version is pinned in
`android/gradle/libs.versions.toml`. AGP carries its own Kotlin compiler, so
there is no separate Kotlin plugin version. `androidx.core` is held at the
newest release that compiles against API 36, because that is the only platform
the container carries; a newer one fails the build with the SDK level it wants.

Android Studio opens `android/` directly and uses its own SDK. Nothing in the
app depends on the container beyond the platform version.

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

Release tags use `vX.Y.Z` with an optional prerelease suffix, for example
`v0.3.0-alpha.1`. Publish alpha tags as ordinary, non-draft GitHub releases:
do not mark them as GitHub prereleases. The installer and `aether update`
resolve `/releases/latest`, which requires the alpha release to be published
through the normal latest-release endpoint.

Release tags are built by the repository workflow. Keep the installer,
`aether update` (`internal/selfupdate`), release asset names, checksums, and
documentation synchronized when changing packaging. See
[docs/install.md](docs/install.md#releases) for the publish commands.
