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
home-screen one into `web/public/icons/`, the phone app's launcher icons
into `android/app/src/main/res/mipmap-*`, and the Play listing's feature
graphic into `android/listing/`. Regenerate them all with `python3
scripts/make-icons.py`, which needs Pillow and fontTools, after the mark
changes, and commit what it wrote.

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

That runs the app's JUnit tests and writes `dist/aether-android-unsigned.apk`
and `dist/aether-android-unsigned.aab`, the app bundle Google Play takes. With
signing variables in the environment it writes `dist/aether-android.apk` and
`dist/aether-android.aab` instead and fails unless both carry the certificate
of the keystore it was given
([`scripts/android-verify-signature.sh`](scripts/android-verify-signature.sh)):

```sh
ANDROID_KEYSTORE_FILE=/path/outside/the/checkout/throwaway.jks \
ANDROID_KEYSTORE_PASSWORD=... ANDROID_KEY_ALIAS=... ANDROID_KEY_PASSWORD=... \
  make android
```

Generate a throwaway keystore with `keytool -genkeypair -keyalg RSA -keysize
2048 -validity 10950` and keep it outside the checkout. `keytool` defaults to
90 days, and Google Play refuses an upload key whose certificate expires
before 22 October 2033. `keytool` writes PKCS12, which holds
one password for the store and the key, so `ANDROID_KEY_PASSWORD` is the same
string as `ANDROID_KEYSTORE_PASSWORD` unless the keystore was made as JKS
(`-storetype JKS`). The release keystore is never on a developer machine: the
release workflow decodes it from `ANDROID_KEYSTORE_B64` into the runner's temp
directory and deletes it afterwards
([docs/install.md](docs/install.md#releases)).

A signed build also needs a release tag, because the APK's versionCode comes
from it (`scripts/android-version-code.sh`, and
[docs/install.md](docs/install.md#releases) for the formula): signing an
untagged tree stops with the tag the script could not parse. An unsigned or debug APK takes the
same number when `git describe` returns a release tag, and versionCode 1 when
it does not - an untagged tree, a dirty one, or a `VERSION=` the script cannot
parse. Neither can install over a release either way. `sh scripts/android-version-code-test.sh`,
part of `make test-scripts`, covers the mapping and its ordering.

`make release` builds the APK and the bundle as part of the matrix, so it
needs Docker as well as Go, Node and Bun.

#### The build container

That image is published by Cirrus Labs from
<https://github.com/cirruslabs/docker-images-android>, built on Cirrus CI, and
it carries no signature or attestation. The digest pin makes every build get
the same bits; it does not make those bits anyone's but Cirrus Labs'. Their
JDK and build-tools produce and sign everything Play and the GitHub release
serve, so accepting the image is accepting a third party in the signing path.
That is the trade we take: the alternative is a repo-owned image built from an
official base plus Google's `sdkmanager`, which moves the same problem to
whoever maintains it.

To bump it, pull `ghcr.io/cirruslabs/android-sdk:<api level>`, read the digest
out of `docker image inspect`, put it in `ANDROID_IMAGE` in the `Makefile`, and
run `make android` and `make android-debug` before committing. The image and
the `compileSdk`/`buildToolsVersion` in `android/app/build.gradle.kts` move
together: the container carries one platform and one build-tools revision, and
a mismatch makes Gradle try to download an SDK package the container will not
let it install.

#### Dependency verification

`android/gradle/verification-metadata.xml` holds a sha256 for every artifact
the build resolves, so a compromised or swapped dependency fails the build
instead of being compiled into a signed APK. `gradle-wrapper.properties` pins
the Gradle distribution by checksum, and CI runs
`gradle/actions/wrapper-validation` against the committed wrapper jar.

Bumping a version in `android/gradle/libs.versions.toml` changes what resolves,
so the file has to be regenerated. Do it from a cold cache, or artifacts
already downloaded are not re-resolved and the file comes out incomplete:

```sh
rm -rf android/build android/app/build android/.gradle
GRADLE=$(mktemp -d) && docker run --rm -u "$(id -u):$(id -g)" \
  -v "$PWD/android":/src -w /src -v "$GRADLE":/gradle \
  -e HOME=/gradle -e GRADLE_USER_HOME=/gradle \
  ghcr.io/cirruslabs/android-sdk:36 \
  ./gradlew --write-verification-metadata sha256 \
    test assembleRelease bundleRelease assembleDebug
```

Then run `make android` and `make android-debug` with `ANDROID_GRADLE_HOME`
pointed at an empty directory, and commit the file if both pass. There is no
`gradle.lockfile` beside it: `libs.versions.toml` pins the direct versions
exactly, and a transitive version that moved resolves an artifact the metadata
does not list, which fails the build just the same.

The Play listing's texts, reviewer instructions, data safety answers and
feature graphic live in `android/listing/`
([its README](android/listing/README.md) maps each to a console field), and
the privacy policy the listing and the app link to is
[docs/privacy.md](docs/privacy.md). A change to what the app stores or sends
changes all three.

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
