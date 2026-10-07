# Testing and the E2E scenario suite

Layers, per the design spec's testing strategy:

- **Unit tests** live beside their packages and run with `make test`
  (race detector on). `TEST_PKGS` narrows it to some packages and
  `TEST_SKIP` leaves some out. CI's `build-and-test` job runs the whole
  suite. A test that needs a database outside `internal/store` opens it with
  `storetest.Open` (`internal/store/storetest`), which starts a new file as a
  copy of one migrated once per test binary: applying every migration costs
  seconds per database under the race detector. The `scripts` job runs
  `make test-scripts` and `make test-native-hooks`, and `lint` runs the
  advisory `make vulncheck`. Permission matrices, budget math, configuration import
  and file revision rules, tailnet auth edge cases, scheduler transitions, and
  the local gateway's own behaviors are proven there, once, and the E2E suite
  does not restate them.
  Role changes belong to the same layer: `internal/sshd/role_test.go` and
  `internal/sshd/permissions_test.go` own promotion, demotion, the last-admin
  guard and what each role may do, and the SPA's half of it is in its rendered
  route tests. The multi-member E2E row below joins members and administers
  them; it does not re-prove the matrix.
  `make test TEST_PKGS=./internal/store` covers concurrent database opens
  against real SQLite files. The migration contention regressions keep a
  competing writer active while checking startup resumption from committed
  progress, the newer-schema error and its version bounds, foreign-key
  restoration, and refusal to trust an uncommitted version.
  `TestConcurrentOpen` still races eight opens on a fresh database.
- **Integration/E2E tests** are behind the `integration` build tag and run with
  `make test-integration` (real Docker, real git), which covers only the
  packages carrying integration-tagged tests. `INTEGRATION_PKGS` narrows that
  to one package and `INTEGRATION_SKIP` leaves some out. `INTEGRATION_RUN` and
  `INTEGRATION_SKIP_PATTERN`, when set, append `-run` and `-skip`. CI runs on
  GitHub-hosted runners, with `GOFLAGS=-v` so each test's duration is in the
  job log. Six browser-independent shards in the `integration` matrix in
  `.github/workflows/ci.yml` start without waiting for browser images:
  `server-chaos` (`INTEGRATION_RUN=^TestIntegrationChaos`),
  `server-coordination` (`INTEGRATION_RUN=^TestIntegrationCoordination`),
  `server-mission` (`INTEGRATION_RUN=^TestIntegrationMission`),
  `server-heavy`
  (`INTEGRATION_RUN='^TestIntegration(EndToEnd|MultiMember|ServerUpdate)'`),
  `scheduler` (`INTEGRATION_PKGS=./internal/scheduler`), and `rest`.
  The four named server shards use `INTEGRATION_PKGS=./internal/server`.
  `rest` dynamically discovers integration packages, excluding
  `./internal/harness`, `./internal/scheduler`, and `./internal/server`;
  it skips `^TestDockerBrowser`, whose coverage belongs to the native
  amd64/arm64 `browser` jobs. The separate `integration-server-rest` job
  loads `browser-image-amd64` and runs `./internal/server` with
  `INTEGRATION_SKIP_PATTERN='^TestIntegration(Chaos|Coordination|Mission|EndToEnd|MultiMember|ServerUpdate)'`.
  This catch-all retains tests that do not start with `TestIntegration`.
  Both integration job families use `sudo env`, preserving `PATH`, `HOME`,
  `GOFLAGS`, `AETHER_BROWSER_TEST_IMAGE`, and `AETHER_BROWSER_IMAGE`.
  The separate unit suites remain unprivileged: the root-only
  `TestApplyRunOwnershipHardlinkSafe` and both privilege branches of
  `TestRecoveredTerminalUsesCapturedUserAndHomeForImages` must stay covered.
  The `smoke` job runs `internal/harness` on the images it builds natively
  on amd64 and arm64, alongside `scripts/standard-image-smoke.sh` toolchain
  and native Git checks. A docs-only pull request (only `docs/**` and root `*.md`) runs
  `audit` and skips every other job, including these, the dashboard jobs, and
  the release matrix. A markdown file anywhere else, including a dashboard
  end-to-end fixture, does not. The `changes` job runs
  `scripts/ci-classify-changes.sh` from the pull request's base revision, so
  a change to that script cannot make the decision itself, and a rename is
  classified by both its old path and its new one. The `edge-image` job
  calls `.github/workflows/edge-image.yml`, which builds
  [`images/edge/Dockerfile`](../images/edge/Dockerfile) on an amd64 and an
  arm64 runner and runs `scripts/edge-image-smoke.sh` on each
  ([The edge image](#the-edge-image)); it pushes nothing and uses no build
  cache. A pull request runs it only when `scripts/ci-classify-edge.sh`,
  also from the base revision, says a changed path can affect the image; a
  push to `main`, the merge queue and a failed classification always run
  it. A rerun classifies the same files, so to force it on a branch the
  classifier skips, start the workflow by hand, which needs write access to
  the repository:

  ```sh
  gh workflow run edge-image.yml --ref <branch>
  gh workflow run edge-image.yml --ref main -f ref=refs/pull/<n>/head   # a pull request from a fork
  ```

  These jobs are the merge gate the E2E suite owns.
- **Dashboard component tests** live beside their components in `web/src/`
  and run with `bun run test` from `web/` (vitest in jsdom). CI runs them in
  the `dashboard` job. jsdom has no layout, so `web/src/test/setup.ts`
  answers every media query with `false` and a component renders its widest
  branch; `atViewport` (`web/src/test/viewport.ts`) puts one test on one
  screen instead - width and pointer queries answer for it,
  `window.innerWidth`/`innerHeight` report it, and the returned resize fires
  `change` where an answer moved and `resize` on the window. It decides
  which branch renders and nothing more: real layout belongs to the browser
  suite below. `setup.ts` also empties the rules of every stylesheet whose
  `<style>` element has been detached. jsdom unregisters a sheet only while
  that element is still connected, and xterm discards its three style
  elements with the terminal around them, so each terminal a file mounts
  strands three sheets of roughly 800 rules. The entries themselves stay in
  `document.styleSheets` and their count still grows; emptying their rules
  is what keeps the cost flat, because `getComputedStyle` - which every
  testing-library visibility query runs - re-cascades only the rules that
  are still there.
- **Dashboard end-to-end tests** live in `web/e2e/` and run with
  `make test-e2e`: a real browser driving the static Next export embedded by
  the shipped binary, through a real `aether gui` gateway and a real
  `aether-server`. They own the paths a person walks in the dashboard, which
  no Go test and no jsdom test reaches. CI runs four isolated
  `dashboard-e2e` runners, each with its own Docker daemon and Playwright
  `workers: 1`; `fullyParallel` remains `false`. Each runner loads the tested
  browser and standard images and runs `make test-e2e` with
  `E2E_ARGS='--shard=1/4'` (or `2/4`, `3/4`, `4/4`). Local
  `make test-e2e` remains unsharded and still builds the embedded dashboard
  and binaries first. The four shards partition the desktop and mobile cases
  once each. File boundaries can make shard sizes unequal.
  The `chromium` project excludes `**/*.mobile.spec.ts`; only the `mobile`
  project owns those specs. The opt-in real-GitHub case keeps its existing
  credential gate; listing or sharding it does not prove it ran.
  Each shard retains `playwright-report-<shard>` (from
  `web/playwright-report/`) and `playwright-results-<shard>` (from
  `web/test-results/`) for seven days on success or failure, including phone
  screenshots and failure evidence. Only shard 1 may save the shared
  Playwright browser cache, on a non-cancelled push to `main`; a cache miss
  never skips browser installation or tests.
  Remote-development scenarios live in `web/e2e/development-browser/`
  (shared real companion, login, live app update, popups, takeover and phone
  viewport input), `web/e2e/development-terminal/` (agent-created shared TUI,
  protocol replies, control, geometry and process lifetime), and
  `web/e2e/remote-development-git/` (remote import, native selected-path commit
  and push through the Changes view's Publish dialog, plus opt-in actual
  GitHub PR publication). These use deterministic harness fixtures, not
  authenticated vendor-agent loops.

CI's native `windows` job builds, vets, and tests the full Windows client
package closure (`./cmd/aether` and its repository dependencies). It owns
the selected `internal/localops` regressions rather than repeating them in
the installer lanes. `TestInstallDesktopWindowsPreservesCLI` requires install
and reinstall to preserve the CLI and unrelated files in the documented CLI
directory and detect only the desktop directory as an installed app.
`TestDesktopLayoutWindowsUsesProgramsKnownFolder` covers standard and redirected
Programs locations independently of missing, relative, or ordinary `APPDATA`.
`TestInstallDesktopWindowsProgramsLookupFailurePreservesInstall` checks that a
failed lookup leaves an existing installation intact. Unit installation tests
inject a temporary Programs destination and never overwrite the developer's
real Start entry.
`TestShellLinkLaunchesNativeConsumer` separately exercises Windows' real
shortcut launcher with a target path containing spaces, an ampersand, and
non-ASCII characters, and checks the launched process's working directory.
These tests do not establish shell catalogue discovery or keyboard Search behavior.

CI calls the reusable, `workflow_call`-only `Windows install` workflow under
the same code-change condition as the native `windows` job. Installer results
are therefore part of the main CI run required by the release gate, not a
separate workflow whose result can be omitted from that gate. Docs-only changes
skip both jobs; code changes, including Go/Bun cache action changes, include both.
The installer workflow runs `scripts/install-test.ps1` under Windows PowerShell
5.1 and PowerShell 7. Those scenarios cover checksum rejection before
replacement, upgrade and locked-file boundaries, `PATH` preservation, CLI-only
installation, unsupported releases, and desktop-build failures. They use
temporary files and restore the user's environment after running.

The same workflow builds the real CLI with Windows release metadata and the
embedded dashboard, removes the hosted runner's inherited exclusions, and
enables Defender realtime, script, archive, and first-seen cloud scanning
before running `scripts/install-smoke.ps1`. A local release mirror labels the
checkout build `v0.5.1-alpha.4` and serves its exact bytes and checksum; it does
not download or claim to test the historical published release with that tag.
The smoke refuses to run unless both `GITHUB_ACTIONS=true` and
`RUNNER_ENVIRONMENT=github-hosted`, before changing files or registry state.
It installs and reinstalls into an isolated app/config tree and verifies the
unchanged CLI. It retains the real hosted user's `USERPROFILE`, `HOME`, and
`APPDATA` so child-process Known Folder expansion matches the shell.
`LOCALAPPDATA` is redirected only through installation and the private-Node
check, then restored before creating `Shell.Application`, querying AppsFolder,
or activating either launch. The captured installed paths, Aether configuration,
npm/Electron build caches, and explicit Electron user-data directory remain
isolated; restoring the environment does not move the installed executables.
Unlike the unit test's injected destination, it resolves the real current-user
Programs Known Folder through `Environment.SpecialFolder.Programs`, backs up any
existing `Aether.lnk`, and restores it in `finally` before deleting the temporary
app. It never redirects permanent shell-folder registry settings.

For each install, the smoke inspects the real shortcut's executable target and
working directory. It then waits up to 90 seconds for `Shell.Application`'s
`shell:AppsFolder` catalogue to enumerate Aether with the installed executable
as its link target, rather than accepting a filename or display-name match.
Discovery and activation timeout diagnostics include the catalogue item count,
the total number of Aether candidates, and at most ten candidate names, paths,
AppUserModelIDs, and targets. They also report the real `LOCALAPPDATA`, Programs
and fixture paths, the caller's session and interactive status, and at most eight
Explorer process IDs/session IDs with same-session and total Explorer counts.
There are then two launches, with the caller's pre-install `PATH` and no
`AETHER_BIN` override:

1. The smoke invokes `InvokeVerb('open')` on the exact discovered AppsFolder item.
   Because this verb has no argument parameter, the fixture shortcut temporarily
   carries only an added `--user-data-dir` isolation argument; its installed target
   and working directory are rechecked. The smoke waits for that exact installed
   executable's main window and CLI child, closes the window normally, and requires
   both processes to exit within bounded waits before restoring the shortcut arguments.
2. It launches the matching Programs shortcut with explicit remote-debugging and
   isolated user-data arguments. Playwright checks that window's onboarding screen,
   saves a screenshot, and closes it; both desktop and CLI child must exit.

The protocol registration is backed up before either launch and restored in
`finally`, which also stops any surviving fixture desktop/CLI processes.
Explorer-mediated catalogue activation may inherit Explorer's environment rather
than the caller's config overrides: the real profile is deliberately a disposable
hosted profile, never a developer's profile. The explicit Electron data directory
applies to both launches. This proves native shell catalogue discovery, catalogue
activation, and shortcut onboarding, not keyboard-driven Start Search indexing or
ranking. Shell COM works in both supported PowerShell lanes without relying on a
PowerShell-5-only module.
Windows PowerShell 5.1 uses system Node; PowerShell 7 (`pwsh`) hides system Node
to exercise the verified private download.
The seven-day screenshot artifacts are `windows-desktop-powershell-system`
and `windows-desktop-pwsh-downloaded`, uploaded even after failure when a
screenshot exists. Defender scans the download and install tree without
exclusions or disabled remediation. The gate rejects new detections even when
Defender has already remediated them, requires automatic safe sample submission
(`SubmitSamplesConsent=1`) before and after installation, and checks that
installation changed neither protection settings nor exclusions.
This is a detection gate, not a guarantee that an unsigned release will never
receive a false positive on another machine.

## Workflow and release-build gates

`make lint-workflows` runs pinned actionlint v1.7.12 and requires ShellCheck
on `PATH` so embedded shell commands are checked locally as well as in CI.
On Linux, install it with `sudo apt-get install shellcheck`; GitHub's Ubuntu
runners already provide it. CI invokes this target in the `lint` job.
`make test-scripts` includes
`sh scripts/release-ci-check-test.sh`, which exercises the real release
checker's run selection and rejection behavior with only the GitHub API
boundary replaced. The checker requires the latest matching run/current
attempt of this repository's `.github/workflows/ci.yml` to be a completed,
successful `push` on `main` for the exact full release commit SHA. Missing,
malformed, or failed API responses and a newer pending or failed run reject
publication; an older success, PR run, or merge-group run cannot substitute.
See [release instructions](install.md#releases) for the publishing gate.

CI's `release-build` matrix has six Go lanes: Linux, Darwin, and Windows,
each for amd64 and arm64. They call `make release-binaries` with
`SERVER_PLATFORMS`, `EDGE_PLATFORMS`, and `CLI_PLATFORMS` selecting one
target: Linux builds server, edge, and CLI; Darwin and Windows build CLI
only. Each lane uploads `binaries-go-<goos>-<goarch>`. The independent
`android` job validates the Gradle wrapper and runs `make android`, uploading
`aether-android-unsigned.apk` and `aether-android-unsigned.aab` in
`binaries-android`. All seven artifacts have seven-day retention and missing
outputs fail upload. Locally, `make release-binaries` builds all ten Go
assets by default without Android or Docker; `make release` still adds
Android. Go builds still need the dashboard's Bun and Node toolchain.

`windows-defender` depends on the Go matrix, not Android. It downloads only
the current run's `binaries-go-windows-*` artifacts, merges their contents,
and requires both `aether-windows-amd64.exe` and `aether-windows-arm64.exe`.
CI and release publication share `scripts/windows-defender.ps1`: prepare the
hosted runner before downloading artifacts, remove inherited antivirus
exclusions, enable and verify effective realtime/cloud protection, automatic
safe sample submission, and all required scanning flags, and update definitions.
The helper retains each binary's resolved absolute path for hashing and the
native scan, scans with remediation enabled, records each binary's SHA-256 and
Defender status/definitions in the logs and job summary, and rejects missing or
quarantined files, changed hashes, scan failures, and new detections even when
already remediated. Scan or pre-scan hash failures include the Defender log path
and up to its last 200 lines for diagnosis. The release workflow scans the final
Windows artifacts from its own `release-binaries` build without rebuilding them;
publication depends
on that exact-byte scan succeeding. A scan of CI's earlier build cannot stand
in for this release scan. Windows executables remain unsigned.
These static scans supplement, rather than replace, the runtime installer
Defender scenarios above. Android and every Go lane remain required CI coverage.

Go caches separate module downloads from compiled objects. Both are scoped
to host OS/architecture, runner environment, resolved toolchain, and module
digest; compiler caches also isolate the lane and commit, with fallback only
within the same lane. Only pushes to `main` write: `build-and-test`,
`windows`, and arm64 `smoke` own their respective host's module/native
compiler archives; each Go release-build lane owns its
`release-<goos>-<goarch>` compiler archive. PRs and releases restore only.
The installer uses a separate `windows-install` compiler lane; only its
PowerShell/system-Node main lane saves that cache and its Bun dependency
cache, leaving Windows module writes to CI's `windows` job. It saves them
right after the build, before arming Defender, whose realtime scan made
archiving the Bun cache take eight minutes. Before compressing
an archive, each writer checks whether its exact key already exists and skips
the save on a hit. Cache reuse does not replace any build or validation gate.

## Headless browser and remote-development acceptance

These are commands and acceptance requirements, not a record of a completed
smoke run. Use a stock headless Ubuntu Docker host with the source-build
toolchain from [install.md](install.md#building-from-source). No display
session, X11/Wayland, Xvfb, host Chromium, or host browser libraries are
needed by the companion. Build and exercise the exact native image:

```sh
docker info
docker build -f images/standard/Dockerfile -t aether-standard:ci .
make browser-image
make browser-smoke

# Match the installed server's authority over its private UID-1000 bind.
sudo env "PATH=$PATH" "HOME=$HOME" \
  AETHER_BROWSER_TEST_IMAGE=aether/browser:test \
  go test -race -timeout=10m -tags=integration ./internal/runtime \
    -run '^TestDockerBrowser' -v

# Load/build that image in this daemon before the complete integration gate.
AETHER_BROWSER_TEST_IMAGE=aether/browser:test \
AETHER_BROWSER_IMAGE=aether/browser:test make test-integration

# Install the dashboard driver's Chromium once; it is separate from the companion.
(cd web && bunx playwright install chromium)

# Real dashboard interaction through the built gateway/server, as root like the
# installed service. Without sudo the suite runs the server as your uid; a
# passing test's teardown chowns what root containers left back to you with
# the scenario's standard image before deleting its scratch directory. A failed
# scenario keeps /tmp/aether-e2e-*, root-owned files included: remove it with
# sudo rm -rf.
sudo env "PATH=$PATH" "HOME=$HOME" \
  "PLAYWRIGHT_BROWSERS_PATH=$HOME/.cache/ms-playwright" \
  AETHER_E2E_STANDARD_IMAGE=aether-standard:ci \
  AETHER_BROWSER_TEST_IMAGE=aether/browser:test \
  AETHER_BROWSER_IMAGE=aether/browser:test make test-e2e
```

`make browser-smoke BROWSER_IMAGE=<reference>` exercises another exact image.
The image contains the scripts, Playwright, Chromium, OS dependencies and
fonts; no source or member-home bind participates. The smoke reads Linux
`/proc/<pid>/status` to verify nested renderer PID namespaces, no-new-privileges
and Chromium seccomp filters beyond Docker's filters. It interacts with a
loopback app, captures a browser PNG and a
bounded JPEG frame, exercises a popup and console report, renders a terminal
PNG with screen metadata, and resets the browser context. The companion's
additional behavior checks run in the same image. A Docker spec or requested
launch flag alone is not proof of a working Chromium sandbox.

The runtime integration command separately exercises real namespace sharing,
the private control mount, immutable image identity, lifecycle recovery and
the absence of a CDP TCP listener. Run it with the installed server's root
authority; do not make its control directory world-writable to get a pass.
Sandbox failures must fail the gate. Inspect the host's kernel/Docker/custom
AppArmor diagnostic; do not retry unconfined, with `--no-sandbox`, or after a
global security-policy relaxation.

CI's browser jobs use native Ubuntu amd64 and arm64 runners, building locally
on pull requests without registry writes. Releases smoke the exact
architecture images before publishing and require anonymous pulls of the
versioned multiarchitecture manifest. An administrator must make the new
GHCR package public before that gate can succeed; neither this guide nor a
workflow definition proves publication or a successful run.

The E2E native Git scenarios require the real `aether-standard:ci` image built
from `images/standard/Dockerfile` (or the exact image selected by
`AETHER_E2E_STANDARD_IMAGE`), with native Git and `gh`; a fake executable is not
a substitute. Browser scenarios require the real companion image in the same
Docker daemon and the loopback app image `node:22.14.0-alpine3.21` available to
pull or already loaded. The Playwright dashboard driver has its own Chromium
and host dependencies; the companion's lack of host-browser prerequisites
does not waive the driver's setup. Build/load prerequisites and run commands
are not evidence that these suites have passed.

`web/e2e/remote-development-git/github.spec.ts` is a separately gated, externally
visible smoke. It runs only with `AETHER_E2E_GITHUB_PUBLISH=1`,
`AETHER_E2E_GITHUB_TOKEN`, `AETHER_E2E_GITHUB_BASE_REPOSITORY` (a public
`owner/name`) and `AETHER_E2E_GITHUB_HEAD_REPOSITORY` (a writable fork
`owner/name`); `AETHER_E2E_GITHUB_BASE_BRANCH` defaults to `main`. Supply the
token through the test environment, never documentation or logs. Its authority
must allow pushing the fork and creating, commenting on and closing upstream
PRs. The scenario creates and closes a real PR and removes its temporary head
branch. An absent credential, disabled opt-in or skipped smoke is an explicit
GitHub coverage gap; native/local publication tests do not replace it.

**Native Git boundary for commit coverage.** Managed selected-path commits use
native prepared `git update-ref --stdin` transactions inside the run. The
standard image uses Ubuntu 24.04's distribution Git, not a special source
build. Stock Git 2.43 supports the required native transaction. No custom Git
build or new Ubuntu host Git upgrade is required.

```sh
go test ./internal/runrepo
```

This suite exercises native Git and native `gh` against an isolated TLS API
fixture. It does not replace Docker, authenticated GitHub, or real-agent
acceptance.

Verify rejection of a changed symbolic HEAD and expected branch OID, including
a branch switch to another branch at the same OID. Exercise native lock
contention and abort behavior, selected-path exactness, unrelated staging
preservation, native identity/signing/coauthors, and the distinct
`committed=true, index_updated=false` outcome. Custom environments lacking
transaction support must fail closed and retain native diagnostics; see
[install.md](install.md#git-inside-run-environments). Do not label selected-path
commits commit-hook-aware: their result is `hooks_run=false`; use native
`git commit` in the run terminal when commit hooks are required. Native
`reference-transaction` hooks must remain active, including a preparation
veto, and hook output must not be mistaken for protocol acknowledgements.

### End-to-end acceptance matrix

Exercise the following against **both** a local `aether gui` gateway and the
server-hosted HTTPS dashboard at desktop and phone viewport widths. Record
separately whether real tailnet identity, mobile operating-system keyboard,
and WebView behavior were exercised; viewport emulation does not prove those.

- Start a real app in a named development terminal in the live run. Observe
  initial output, reconnect/replay, screen and geometry changes, normal and
  alternate-screen rendering, terminal capture, exit and explicit restart.
  Confirm a phone follows the shared terminal without silently resizing it.
- Open the app's run-local URL in the companion, use a real DOM snapshot and
  action, navigate and switch pages, inspect console/request failures, view
  streamed frames, change viewport, and capture evidence. Join a static page
  after another observer and confirm the latest complete frame appears.
  After navigation, reset, closure, or restart, old page/node/viewport
  references must fail rather than act on another target.
- Let a run agent own an app surface, explicitly take it over as a human,
  confirm stale agent input is rejected, then release/reacquire it. A second
  terminal and the browser must remain independently controlled; the primary
  agent's swarm hold must remain intact.
  Hold browser input across release, revocation and controller disconnect;
  confirm server-side cleanup and refusal of replacement control if cleanup
  fails. Observer disconnect must not clear a live controller's input.
- Confirm read/capture/stream and write authorization on both gateways,
  including a member without the `steer` permission, backing-account revocation, lifecycle
  stop/pause, stale control generations, and reconnect after server restart.
  Browser loss must not silently replace an authenticated session.
- Exercise real Git status/diff and an explicit selected-path commit in the
  run environment; verify signing and branch identity, concurrent branch
  rejection, commit-hook disclosure, native reference-transaction hook
  behavior, and fail-closed transaction diagnostics. Confirm no browser
  profile or capture enters the source tree or commit.
- Inspect deliberately selected evidence, its attribution and retention.
  Retain reviewed captures and bounded notes before headless report/cleanup,
  read back the packet and download its exact bytes using `evidence_packet_id`.
  Confirm workspace View can read retained evidence while private transient
  access still requires `steer`/account-use. Exercise queued revocation and busy
  publication admission with explicit retry, retained-plus-staged quotas,
  transient deletion, and the separate capture-time and later packet Git
  boundaries. Keep credential entry out of recordings and confirm nothing
  uploads images to a public PR by itself.
- Cancel or revoke idle browser streams and capture downloads through both
  transports, including a stalled SSH peer. Source cleanup must not wait for
  blocked status/close writes; responsive connections must preserve sibling
  channels. Interrupted downloads must not look like complete captures.
- Complete two genuine authenticated vendor-agent loops through the shared
  development tools, with observed terminal/app changes and reviewed evidence.
  Record which vendors, image identities, gateways and phone were exercised,
  and any missing prerequisites or unexercised rows.

Public CI has no authenticated real-agent credentials. Its deterministic
fake agents and scripted `claude`, `pi`, or `omp` fixtures prove their stated
broker/transport paths, **not two genuine vendor loops**. No-login vendor
smokes prove argument acceptance and login failure, not authenticated work.
A skipped test, unavailable Docker daemon, fake runtime, or absent phone is
an explicit coverage gap, never a passing real-agent or phone acceptance.

## Local configuration in tests

Tests that read or write the linked-server config through `cli.Load`,
`cli.Save`, or gateway handlers must call `internal/testhome.Isolate(t)`.
It sets `AETHER_CONFIG_DIR`, the platform home/config variables, and clears
`SSH_AUTH_SOCK`. Setting only `XDG_CONFIG_HOME` and `AppData` leaves the real
macOS config at `~/Library/Application Support/aether/config.json` exposed.
The gateway regression in `internal/localgw/config_isolation_test.go` runs
config refresh and repository linking against a temporary user config and
checks that its contents and modification time stay unchanged, including
when the test process inherits `AETHER_CONFIG_DIR`.

## The E2E scenario suite

`internal/server`'s `*_integration_test.go` files are the owned
end-to-end suite: every scenario drives the fully wired server
(`server.New`) over real SSH, real git transport, and - when the daemon
is reachable - real Docker containers. `pickRuntime` falls back to the
in-process `e2eRuntime` (`e2eruntime_test.go`) on hosts without Docker;
the two host-half coordination scenarios force it because their agents must
reach container surfaces from the test process, and the container
coordination scenario skips without a daemon rather than falling back.
Scenarios:

| Test | Scenario |
| --- | --- |
| `TestIntegrationEndToEnd` (`integration_test.go`) | Solo lifecycle, the acceptance gate: seed over git push -> launch -> attach -> detach -> reattach -> steer -> finish -> pull, with the bus traffic checked against the Wave 1 contract |
| Gateway (`internal/localgw` and `internal/webgate`) | The `aether gui` HTTP/WS surface, covered at this layer by unit tests against a stub backend: token-gated API round-trips (`api_test.go`), diff and disk proxies, capability reporting, and the `/ws/attach` mirror and steer channels (`ws_test.go`). Shared `internal/webgate` framing and route dispatch are exercised through this surface; the server-hosted gateway's WhoIs/HTTPS boundary is covered by the row below |
| `TestIntegrationServerGateway` (`servergw_integration_test.go`) | The server's own dashboard gateway, driven the way a phone on the tailnet drives it: a stub WhoIs resolver identifies an `httptest` client, which reads `capabilities` (no `local` field) and `server.info`, launches a run, follows its events over `/ws/events`, and types into its PTY over `/ws/attach`; then a tagged node is refused `403` and a failing resolver `503`, and the HTTPS listener is started against a stand-in tailscaled that issues the certificate - a tailnet without HTTPS certificates refuses to start. The `WebIdentity` refusals and the in-process `Local` client the gateway serves each member through are unit-tested in `internal/sshd/local_test.go` |
| `TestIntegrationMultiMember` (`multimember_integration_test.go`) | Three clients: tailnet initial join and invite-code key joins, WhoIs-down fallback with banner, remote administration, steering another member's run, presence roster, handoff, approval inbox, budget cap and override, agent crash -> `failed` + `wip:` commit, and the finished branch authored as the run's owner after the handoff, committed by Aether, and carrying one `Co-authored-by:` trailer per other steerer |
| `TestIntegrationProfileSyncAndLogins` (`profile_integration_test.go`) | Explicit profile operations and harness logins: a login in the environment terminal persists into two runs, a manual profile push updates the shared persistent member home for a later run and an already-running run, and denylisted credential names are refused (Docker only - it needs a real terminal). CLI profile `push`, `status`, and `rollback` remain separate manual operations |
| `TestIntegrationMemberEnvironmentImage` (`environment_image_integration_test.go`) | The saved environment image: what the container layer keeps, and that a container started from it **without** the member home mounted has no signing key, no `.gitconfig` and no gh token - Docker's commit never captures a bind mount |
| `TestIntegrationCoordinationEndToEnd`, `TestIntegrationCoordinationKillSwitch` (`coordination_integration_test.go`) | Conflict radar and run-to-run coordination over the MCP bridge, including server restart with surviving containers and the kill switch |
| `TestIntegrationCoordinationInContainer` (`coordination_container_integration_test.go`) | The same bridge inside real containers: the run socket and both verified read-only executable binds are realized, no Aether-managed `mcp.json` is installed, `co-authors` is found at `0444`, the staged binary executes as `/opt/aether/aether-server mcp` by a non-root agent when manually configured, and a status/send/inbox round trip works between two overlapping runs |
| `TestIntegrationAgentStatusReporterInContainer` (`agentstatus_integration_test.go`) | The status reporter inside a real container, on the shipped `claude` and `pi` profiles in one server: each asset written at `0444` into the run's coordination directory, the argument pointing the harness at it, the staged binary running `aether-server report claude` and `report pi --json ...` against the run's own socket, and each run moving to `needs-attention` immediately after the turn ends, then back to `running` on the agent's next turn |
| `TestIntegrationOpenCodeStatusReporterInContainer` (`agentstatus_integration_test.go`) | The plugin written at `0444` into the run's coordination directory, `OPENCODE_CONFIG_CONTENT` naming it from inside the container with native hosting unchanged, the staged binary running `aether-server report opencode --json ...` against the run's own socket, and the run moving from `needs-attention` to `running` when the agent takes the steer |
| `TestIntegrationRunInputReports` (`agentstatus_integration_test.go`) | The assembled server receives real socket `run.report` calls and exposes `running` with independent input through SSH get/list and mutation snapshots; exact session/kind/id closes, last-close, duplicate suppression, `needs-attention` without input, and durable `run.input` replay are checked without requiring Docker or a vendor CLI |
| `TestIntegrationChaosRebootSurvivingContainer`, `TestIntegrationChaosRebootRetainedTUI`, `TestIntegrationChaosRebootLostContainer` (`chaos_reboot_integration_test.go`) | The server SIGKILLed mid-run: supervision reattaches to an active surviving container; an explicitly closed TUI run survives with the same row, paused container, and checkout and can relaunch that exact retained identity; a lost active container becomes `interrupted` after its `wip:` commit and published branch, with no replacement relaunch |
| `TestIntegrationChaosDiskPressure`, `TestIntegrationChaosStallUX` (`chaos_pressure_integration_test.go`) | Worktree TTL GC under load with branches surviving, the gauge's three-way breakdown following reclaim, new runs refused below the free-space floor while an eligible retained TUI relaunch uses no new admission, and a silent agent parking at needs-attention, returning when the agent answers a steer, and staying parked when it does not |

### The chaos scenarios

`chaos_reboot_integration_test.go` is the one place the suite runs
`aether-server` as a **child process**. An in-process server cannot be
SIGKILLed, and the whole point of that row is that nothing on the shutdown
path runs: the next boot only sees what SQLite and git had already made
durable. The child binary is built once per test binary, the store is seeded
before startup, and it binds a reserved loopback port so a restart can claim
the same address. It always builds its own Docker runtime, so these scenarios
skip without a reachable daemon rather than falling back, and they address
containers by the name the runtime derives from the run ID.

Disk pressure is driven through **configuration, never by filling the host
disk**: `--min-free-disk` above what the machine has free makes the real
`statfs` path refuse for real, and `--checkout-ttl` turned down makes the
real GC sweep on the next boot. No injected filesystem, no fake statfs.

The SSH-drop boundary is fuzzed where it lives, in `internal/ptyhost`
(`drop_fuzz_test.go`): the connection is cut at every byte offset and the
agent's stdin must always be an exact prefix of what the transport
delivered, which catches a byte from past the cut as well as any reorder or
duplication. Cutting the stream cannot reach the other half of that row -
the read loop stops at the first error and never asks a dead connection for
more - so the case where the attach unwinds first and the socket coughs up a
straggler afterwards has its own scenario in the same file.

The deterministic fake agent is the scheduler's `fake` harness: its argv
comes from `AETHER_FAKE_AGENT` at launch (typically
`sh /workspace/agent.sh {task}`, with the script committed to the seed
repo and dispatching on the task). On the fallback runtime the same
behaviours are registered per task key via `e2eRuntime.script`.

Three `server.Config` fields exist for this suite: `WhoIs` overrides
tailnet identity resolution so join and fallback scenarios need no real
tailnet, `Harnesses` overrides registry argv templates so a registered
harness (with its real profile root and credential mounts) can run a
scripted agent - the first two double as deployment wiring - and
`ServerBinary` names the executable staged for the CLI and optional MCP bridge.

### The container coordination scenario

`coordination_container_integration_test.go` proves the half of
docs/mcp-bridge.md that an assertion on a container spec cannot: that the
mounts a run is given are real, and that the agent holding them can use
them. Two things make it possible.

The staged bridge has to be a binary that has the `mcp` subcommand, which
under `go test` `/proc/self/exe` is not. So the scenario points
`ServerBinary` at an `aether-server` it builds - the same one the chaos
scenarios run as a child process.

The scenario uses the shipped `claude` profile with a non-root fixture agent
from `internal/server/testdata/coordagent`. It explicitly invokes the optional
bridge from the canonical executable mount; no automatic MCP registration is
required. The fixture reports directory modes, read-only mounts from the
kernel's mount table, EROFS on attempted writes, and tool results over a real
attach. The daemon's mount view is checked alongside those observations.

`TestIntegrationMissionCompositionInDocker` composes swarm dispatch,
proactive coordination, accepted retained submissions, combined verification,
a delivery request approved without a human decision, and exact delivery. It
also checks failed verification, stale-target rejection, and integrator
replacement. Its `claude`, `pi`, and `omp` executables are scripted fixtures,
not genuine vendor-agent runs.
`web/e2e/mission-candidate-review.spec.ts` drives launch, progress, and worker
control in a real browser. After a successful worker report, it checks that the
same Docker container remains paused. The integrator then prepares a candidate
through its coordination CLI, and the already open swarm page must list the
worker's report under **Agent messages** and show the candidate read-only
under **Integration**, without a reload. It attaches a successful screenshot for visual
inspection.

`web/e2e/run-room.spec.ts` sends structured request snapshots through the
staged reporter in a real container. Both members' browsers must add and
clear the request; while it is open the owner's run header reads
**Question: answer in the terminal** and the other member's reads
**Waiting for** the owner. The callback payloads are scripted fixtures, not
evidence of a live vendor agent emitting them.

The container user is the test process's own uid:gid unless that is root:
the scheduler chowns the run checkout and the member home to the container
user before creating the container, and an unprivileged test process can
only chown to itself.

### The real-agent smoke tests

`internal/harness/smoke_integration_test.go` launches the vendors' actual
CLIs and checks that the argv Aether ships is still the argv they accept.
Nothing else catches a vendor renaming a flag or refusing a combination it
used to allow: a change like that breaks every run of that agent and no
amount of internal testing sees it coming.

`TestSmokeHeadlessNoLogin` is the one that runs in CI. It launches each
headless template with no credentials at all and requires the run to fail
for want of a login, never for want of a parseable command line - reaching
the provider is proof the CLI accepted the flags. The `smoke` job builds
`images/standard/Dockerfile`, then `images/smoke/Dockerfile` on top of it to
add the agent CLIs at whatever version their vendors ship that day, and
points the gate variables at the result.

To run it locally, build the same image and name it:

```sh
docker build -f images/standard/Dockerfile -t aether-standard:local .
docker build -f images/smoke/Dockerfile --build-arg BASE=aether-standard:local \
  -t aether-smoke:local .
AETHER_SMOKE_IMAGE_CLAUDE_NOLOGIN=aether-smoke:local \
AETHER_SMOKE_IMAGE_CODEX_NOLOGIN=aether-smoke:local \
AETHER_SMOKE_IMAGE_OPENCODE_NOLOGIN=aether-smoke:local \
  go test -tags integration -run TestSmokeHeadlessNoLogin ./internal/harness/
```

The `_NOLOGIN` images must carry no credentials. The other smoke tests -
`TestSmokeClaude`, `TestSmokeOpencode`, `TestSmokeCodexFlags` - drive the
agent far enough to produce output, so they need an image that *does* carry
that agent's login state, named by `AETHER_SMOKE_IMAGE_<NAME>`. CI has no
such credentials, so those stay a manual check. Every one of them skips when
its variable is unset, and the no-login test skips as a whole rather than
reporting a pass with nothing run.

`pi` and `omp` have no template smoke in that image. The native mailbox
lifecycle checks below are separate from template acceptance and authenticated
TUI smoke.

The status extension those two load, `internal/agentstatus/status.ts`, is
the one shipped file the Go build never executes. `internal/agentstatus`
runs it under `bun` against a recording stand-in for the server binary, so
a turn's reports are proven to come out in order and to end exactly once.
Those tests skip where `bun` is not installed.

`TestACPAdapterInstall` installs the pinned Claude Code and Codex
[enhanced-mode adapters](harnesses.md#enhanced-mode-adapters) into a home
mounted at `/home/aether` in the standard image, as uid 1000, and completes
the ACP `initialize` handshake with each, logging the cold start. It needs
the npm registry and skips without it:

```sh
AETHER_ACP_IMAGE=aether-standard:local \
  go test -tags integration -run TestACPAdapterInstall -v ./internal/harness/
```

With `ACP_LIVE=1` it also opens a session with each adapter through the
session host (`initialize` then `session/new`, no prompt), so no login is
needed; a logged-out Codex skips at `session/new`.

[Enhanced runs](enhanced-runs.md) are proven without a real agent:
`internal/acphost/acpmock` replays conversations recorded from the real
adapters and answers prompts by their text, so tests can drive a permission
request (`ask permission`), a form question (`ask form`), a turn that runs
until cancelled (`wait`) or a paced turn with a plan, tool calls, command
output and a diff (`demo`). The
scheduler's unit tests run it behind the fake runtime;
`TestIntegrationEnhancedRunDocker` (scheduler) and
`TestIntegrationEnhancedRunGateway` (server) build it as a static binary and
run it as the ACP server of a real container, the second through
`/ws/acp/<run_id>` on the server gateway:

```sh
make test-integration INTEGRATION_PKGS='./internal/scheduler ./internal/server' \
  INTEGRATION_RUN='TestIntegrationEnhancedRun'
```

`web/e2e/run-session.spec.ts` drives the dashboard's Session view against
the same agent: the e2e fixture `installACPMock` builds it (`go build`, so
the e2e host needs Go) into the member's `~/.local/bin` and registers it as
the member agent `mock`. The spec launches an Enhanced run, watches the
stream, answers a permission with the digit key, expands a work entry's
diff and interrupts a turn:

```sh
make build && (cd web && bunx playwright test run-session.spec.ts)
```

[Mode switching](enhanced-runs.md#switching-a-running-agent) is proven the
same way. `TestIntegrationModeSwitchDocker` switches a run in a real
container both ways, with acpmock as omp's ACP server and a script as its
terminal. `TestIntegrationSupervisorSwapsInContainer` swaps the run
supervisor's child under busybox's `sh` and, with `AETHER_ACP_IMAGE` set,
under the standard image's `dash`. `TestLiveSwitch` is the check behind an
agent's `switchable`: with `ACP_LIVE=1` it runs the agents and adapters on
the host's `PATH` with their own logins, starts a session over ACP and asks
about it from the terminal, then the reverse. `ACP_LIVE_OMP_MODEL` picks
omp's model when its default cannot answer:

```sh
ACP_LIVE=1 go test -tags integration -run TestLiveSwitch -v ./internal/harness/
```

### Native mailbox lifecycle and idle-wake smoke

Run the shipped adapters against deterministic SDK-shaped lifecycle fixtures
with Node 22.13 or newer:

```sh
make test-native-hooks
node --experimental-vm-modules --test --test-name-pattern='omp:' \
  internal/coordhooks/native_pi_omp_lifecycle_test.mjs
```

The OMP fixture separates extension `agent_end`, public terminal `agent_end`,
and the session's asynchronous `waitForIdle()` drain. OMP 18.3.1 does not emit
pi's `agent_before_settle` or `agent_settled`. Regressions exercise repeated
busy consumption/ack followed by fresh idle mail, unread busy mail deferred
through cleanup and fresh admission, native automatic continuations,
approval resolution during work, Stop during cleanup, replacement, and
admission crossing a busy transition. The shared suite retains rejected-send,
reentrant acceptance, duplicate-loading and owning-root cases. These are
adapter checks, not evidence of an authenticated model turn.

For real native proof, use an isolated coordinated TUI run with an authorized
peer and the exact CLI/server/extension versions under investigation. Do not
edit a live production hook, credentials, trust state or disable settings to
obtain evidence. Record the managed launch arguments separately from manual
file inspection; see [activation](harnesses.md#installation-and-activation).

1. Finish or block outstanding agent todos and confirm no native retry,
   approval prompt or pending input remains. End the normal model turn without
   a terminal worker report, active `inbox --wait`, polling loop or scheduled
   prompt. Observe at least two 30-second receiver waits with no new model turn.
2. Send one uniquely identified peer message using
   `aether-internal send --to <receiver-run> --body <nonce> --idempotency-key <key>`.
   Record the durable message ID, fresh admitted helper response, native API
   acceptance, model turn start, exact inbox body, and explicit
   `aether-internal inbox --ack <ack_token>`. After settlement, two more quiet
   observer waits must produce no extra model turn.
3. During ordinary foreground work, send another message. Let the agent read
   and acknowledge it before its final response. Repeat this sequence; each
   fresh idle snapshot must find no remaining mail and schedule no follow-up.
   In a separate round, leave mail unread through completion and require one
   deferred native wake after successful cleanup and fresh admission.
4. Test frozen batches separately: read a batch without acknowledging it, send
   another message, and read again. The old batch must repeat. Acknowledge its
   token only after handling it; the next batch must expose the new message. This is
   expected inbox behavior, not evidence of a missed native wake.
5. Exercise approval waits, Stop, protection/takeover and release, replacement,
   duplicate manual/managed loading, rejected native input and process exit.
   No busy or stale root may receive an automatic turn. Stop requires accepted
   human input to resume; releasing server control alone does not undo it.

Capture session/root identity, generation, message IDs, acknowledgement,
helper admission, native turn boundaries and visible output. A final-looking
assistant message, hidden inbox pointer, status reporter event, or empty inbox
alone cannot identify the initiator. OMP's own todo/retry continuation may
encounter legitimate integrator swarm-refresh context; that context does
not start the turn. Name missing instrumentation and unexercised cases.

In the actual dashboard, open an owned swarm worker's terminal without touching
control: it must remain a read-only mirror and request no write lease. Explicit
**Take control** must acquire through its acknowledged control response;
**Release** (**Release control** on the swarm page) must return to viewing,
including after navigation. Check
ordinary and integrator owner defaults, phone mirrors, foreign/protected runs
and stale generations separately. The authoritative control/hold contract is
in [Run control](terminal.md#run-control), not duplicated by a
test-only permission model.

## The edge suite

`internal/edge/edgetest` runs edge remote access end to end in one process,
under plain `make test`: a real edge (sign-in service and relay, serving its
sign-in origin on `localhost` and its relay origin on `127.0.0.1`) against a
fake GitHub, real servers (sshd over a real store, with the edge agent)
under each access policy, the real client dialer and the local gateway, and
a browser played by an HTTP client with a cookie jar for the edge's own
pages. A proxy in front of the edge records the control channels and, with
the edge's own signing key, plays a compromised edge: it forges, replays,
alters, drops and injects control messages and grants, answers the data
socket of an open it forged, and routes a client's connection to another
server. Tests read each server's store to check what an attack changed.
[edge.md](edge.md) describes the edge. It needs no Docker and no network:

```sh
go test -race ./internal/edge/edgetest/
```

| Test | Proves |
| --- | --- |
| `TestAccountAccess` | Under `account` an invited person works with no approval, the device is recorded `registered`, and a registered device approves nothing |
| `TestApprovedDevices` | Under `approved-devices` every new device, a member's first included, waits: approved from the member's approved device, an admin's, or the console; a waiting device, an admin's included, opens no control channel |
| `TestMaliciousEdgeApprovedDevices` | With the edge's key, against `approved-devices`: a grant for the admin with the attacker's key, whose code an approver's lookup shows admits alice's admin member; a grant naming another account on a victim's approved key and on a victim's new key, refused with nothing recorded; a forged invitation acceptance, a replayed open, an altered policy and directory, and forged claimed, transferred, ownerless and account-deleted messages end without access and without an approved admin credential; after the forged acceptance no member or administrator exists and the invitation is open |
| `TestMaliciousEdgeSubstitutesTheClaimingAccount` | Under both policies a claim whose grant names another account is refused before the code is tried; no member, owner or spent attempt |
| `TestMaliciousEdgeAccountAccess` | Against `account` the same forged admin grant is admitted with the admin's role: what that policy trusts the edge with. A key registered to one account still serves no other |
| `TestTakenOverProviderAccount` | A taken-over GitHub account signs in on its own machine: access under `account`, a waiting device under `approved-devices` |
| `TestClaim` | Under both policies: an edge that routes a claim to another server never delivers the code; expired, wrong and exhausted codes; the claiming device approved; another account's claim refused |
| `TestTransferStandsOnlyOnceTheEdgeRecordsIt` | `server.owner.transfer` whose answer is lost, which the edge refuses, or with the edge unreachable fails with the reason and keeps the server's owner; after the lost answer the edge records the server's owner again when it reconnects |
| `TestFailedClaimRecordRecovers` | The edge's database refuses to record the owner; the edge closes the server's control channel, the server drops its owner, and a fresh code claims again for the same account only |
| `TestClaimKeepsTheDirectoryPushedWithIt` | A server with an admin and an open invitation is claimed through the admin's link, and the invitee joins |
| `TestInvitations` | By login and email; revoked, expired and a demoted creator's invitations refused; an edge still listing an expired one overruled by the server |
| `TestRoleChange` | A new role reaches the edge's list and the member's live connection |
| `TestCrossServerIsolation` | A member of one server is refused on another, by the edge and, for forged grants, by the server |
| `TestRevocationClosesLiveConnections` | Under both policies: member removal, device revocation, console revocation, `aether logout` and account deletion each close a live connection; the test logs how long each took |
| `TestRevocationWhileTheEdgeIsDown` | Revocations made on the server while the edge is down hold once it is back |
| `TestDeleteAccountAfterTransfer` | `server.owner.transfer`, then deletion: the server keeps its new owner and every member and role |
| `TestDeleteAccountLeavesTheServerOwnerless` | Under both policies: deletion without a transfer leaves the server enrolled and ownerless with its members, roles and workspaces; a collaborator's claim makes no admin; the console recovers it |
| `TestAccountDeletionReachesAnOfflineServer` | A deletion reaches a server that was offline when it next enrolls |
| `TestLostAccountDeletionIsSentAgain` | A deletion dropped on its way to the server stays owed at the edge, is sent again when the server reconnects and is forgotten only on the server's answer; the identity and edge devices are then gone, the device key no longer connects directly, and a repeated notice changes nothing |
| `TestConsoleRecoveryOfAnAdminWithoutAnAccount` | Under both policies: the only admin, reachable only through the edge, deletes their account; a claim code from `claim-code --admin` binds the account they sign in with again to the same member and approves the device; a code naming a collaborator claims nothing; no member or admin is added |
| `TestRelayHostNameChangeKeepsPinAndOwner` | The edge moves to other host names with the same key; the server, its `edge-url` changed, keeps its pin and owner, the edge keeps it claimed, and an admin transfers ownership |
| `TestPolicySwitchToApprovedDevices` | Switching `account` to `approved-devices` refuses registered devices until reviewed; approved devices, member SSH keys and the tailnet dashboard keep working |
| `TestExistingPathsWithoutTheEdge` | With the edge down, an invite code with an SSH key and the tailnet dashboard's in-process client work |
| `TestServerVerifiesGrants` | The server refuses forged, expired, replayed, misdirected, other-issuer, other-kind and wrong-connection grants, and a grant for a Google account with the reason; `aether-server edge trust` fetches the signing key |
| `TestEnrollmentSignatureIsNotAHostSignature` | A server posing with a captured enrollment signature fails the client's handshake |
| `TestNoBootstrapOverTheRelay` | An unclaimed server takes no member from the relay, and an invite code over the relay joins nobody |
| `TestRelayedFloodDoesNotBlockDirect` | Relayed connections that never finish their handshake fill the relayed budget only; a direct connection still works |
| `TestEdgeRestartAndDirectFallback` | A clean edge stop drops relayed connections, the server re-enrolls, and a link with an address uses it while the edge is down |
| `TestServerRemovedWhileOffline` | A server removed on the edge's Servers page while disconnected comes back unclaimed and is claimed again |
| `TestBlockedServerStatusSaysWhy` | A blocked server's status for `aether-server edge status` carries the edge's reason |
| `TestBehindTrustedProxies` | Under both policies, with the edge behind `--trusted-proxies` and its proxy reaching it from a non-loopback address of this machine (skipped on a machine with none): GitHub sign-in, a claim, an invitation and relayed `server.info` calls work; the edge records the forwarded client address, not the proxy's; `/signin/google` answers 404; a request from loopback past the proxy is refused with `403` and counted |
| `TestGatewaySignsInClaimsAndLinks` | The desktop onboarding wizard signs in, claims and links a server through the local gateway alone |

Beside the suite, `internal/sshd` checks each server rule on its own,
among them `TestInvitationWaitsUntilOneDeviceIsApproved` (devices wait on
an invitation until one is approved, which creates the member and removes
the others), `TestRevokedInvitationDropsItsWaitingDevices`,
`TestMemberIdentityListAndRemove`,
`TestClaimCodeForAnAdminRecoversThatAdmin`,
`TestApprovalShowsWhomTheCodeAdmits` (a lookup names the member and role
before approving, and an approval naming another device commits nothing),
`TestRelayedConnectionThroughAnEdgeThatSubstitutesTheAccount` and
`TestWaitingDevicesAreBounded`; `internal/edge/agent` has
`TestPinAndOwnerFollowTheEdgeKey`, `TestEarlierLayoutPinIsNotSilentlyReplaced`,
`TestTransferOwner` and `TestOwnerIsReportedAgainAtEnrollment`. The integration suite's
`TestIntegrationUpgradeFromMainWithoutTheEdge` starts this build on a
database at main's schema version and a configuration without edge keys:
any outbound HTTP request fails it, and its SSH key and tailnet members
sign in as before.

Every request reaches the edge from 127.0.0.1, so each test moves its
edge's clock forward to refill the per-address rate limits. Tests run in
parallel, each with its own edge; the client calls that read the config
directory from `AETHER_CONFIG_DIR` take turns.

### Behind nginx

`scripts/edge-nginx-test.sh`, part of `make test-scripts`, runs
`TestBehindNginx` (build tag `nginx`) when `nginx` is on `PATH` and skips
otherwise. It renders
[`packaging/nginx/aether-edge.conf.example`](../packaging/nginx/aether-edge.conf.example)
for the host names `localhost` and `127.0.0.1` on a free port with a
self-signed certificate, checks it with `nginx -t`, and runs a real nginx
in front of the edge wired as `aether-edge serve --proxy-listen` wires it. A
real server enrolls through nginx; a client signs in, claims, runs
`server.info`, holds the relayed connection silent for 75 seconds (past
nginx's default 60-second read timeout) and runs it again; and requests
carrying a forged `X-Forwarded-For` are recorded under the address nginx
saw. It takes about 80 seconds.

```sh
sh scripts/edge-nginx-test.sh
```

### The edge image

`scripts/edge-image-smoke.sh` runs a built
[`images/edge/Dockerfile`](../images/edge/Dockerfile) image with fake
GitHub credentials behind `curl` as its proxy, on a port published on
127.0.0.1. It checks that the image runs as a non-root numeric uid, has no
shell and declares its `HEALTHCHECK`; that a start without configuration
fails naming `--signin-origin`; that a GitHub-only configuration passes
`aether-edge healthcheck` and exits 0 on `SIGTERM` within 30 seconds
without logging the secret; and, through `/v1/edge`, that a replacement
container on the same volume presents the same edge key fingerprint and one
on a new volume a different one. It needs a container runtime, so it is not
part of `make test-scripts`; the second argument names the runtime, docker
by default:

```sh
make edge-image
sh scripts/edge-image-smoke.sh aether/edge:test
```

With podman, build with `podman build --format docker -f
images/edge/Dockerfile .`: the default OCI format drops `HEALTHCHECK`, and
the smoke test fails on it.

`TestServeAsAContainerRunsIt` in `cmd/aether-edge` covers the same start,
healthcheck, stop and key checks on the binary without a runtime, with a
read-only working directory, `HOME` and `TMPDIR`.

`scripts/ci-classify-edge.sh` decides from a pull request's changed paths
whether the image can change. Its first `case` pattern lists every
repository package `aether-edge` imports. `scripts/ci-classify-edge-test.sh`,
part of `make test-scripts`, runs `go list -deps ./cmd/aether-edge` for
linux/amd64 and linux/arm64 and fails when a package it imports is
classified false. When the edge starts importing a new package, the test
prints the line to add:

```
ci-classify-edge-test: aether-edge imports internal/<package> on linux/amd64, which ci-classify-edge.sh classifies false; add internal/<package>/* to its first case pattern
```

Add that pattern, then run `sh scripts/ci-classify-edge-test.sh` again.

The golang base image in `images/edge/Dockerfile` sets `GOTOOLCHAIN=local`,
so the image builds with that image's Go whatever go.mod's `toolchain` line
says. `scripts/edge-go-version-test.sh`, part of `make test-scripts`, fails
when the two differ and names both versions:

```
edge-go-version-test: images/edge/Dockerfile builds with golang:1.26.8, but go.mod pins go1.26.9; change the golang tag and its @sha256 digest in images/edge/Dockerfile to 1.26.9 (or go.mod's toolchain line to go1.26.8)
```

## The dashboard end-to-end suite

`web/e2e/` drives the dashboard the way a person does: a Chromium browser on
the static Next export the CLI embeds, talking to a real `aether gui` gateway,
which proxies every call over a real SSH connection to a real
`aether-server`. Both local and server-hosted paths dispatch through the shared
`internal/webgate`; the server gateway's HTTPS/WhoIs boundary is covered by
the integration row below and by the real-phone check. Playwright is the
runner, pinned to an exact version in `web/package.json`.

Two projects share that one Chromium install. `chromium` uses the desktop
descriptor and skips every `*.mobile.spec.ts`; `mobile` uses a Pixel-class
descriptor - `isMobile` and `hasTouch`, so `pointer: coarse` matches and
`tap()` sends real touch events - and runs those files alone. No spec runs
under both. Run one with `bunx playwright test --project=mobile` from `web/`.
A real phone reaches the same dashboard through the server gateway's URL.
That path carries no browser token: Tailscale WhoIs identifies the source
address on every request.

```sh
(cd web && bunx playwright install chromium)   # once, from the repo root
make test-e2e
```

`make test-e2e` builds the static dashboard export and both binaries first. The
CLI serves the SPA out of its own embedded `web/dist`, so a stale binary would
test a stale dashboard. The dashboard itself has no production Next server.

The browser suite owns behavior that jsdom cannot observe: actual hit testing,
computed layout, responsive overflow, painted focus outlines and event ordering
across document listeners. Component tests remain responsible for rendered
roles, labels, state transitions, navigation and real gateway error text; a
CSS class or source-pattern assertion is not a substitute for either layer.

The `aether` fixture (`web/e2e/fixtures.ts`) builds one stack per test and
tears it down with everything it created:

- One `aether-server` child process on its own loopback SSH port, with a
  temporary data directory, `AETHER_FAKE_AGENT="sh /workspace/agent.sh"` in
  its environment and `--standard-image busybox:1.36` - the tag
  `internal/runtime`'s integration tests already pin, so a run of either
  suite warms the other's pull. Nothing is seeded into the store: the
  first identity to authenticate becomes the admin, which is what the
  wizard's Connect step does.
- One `aether gui` per member, each with its own `HOME` and
  `AETHER_CONFIG_DIR`, so the SSH key the Connect step generates, the
  `known_hosts` entry it writes, the saved link config and the member's
  persistent agent/configuration home all belong to that member and never touch
  the developer's own. `PATH` and `SHELL` are fixed too, because
  `env.agents` reports what is installed on this machine and that answer
  has to be the same on a laptop and on a runner.
- Real git repositories on disk, seeded with the `agent.sh` the fake harness
  runs.

Each gateway's git runs under an explicit `GIT_SSH_COMMAND`: OpenSSH resolves
`~` from the password database rather than from `HOME`, so without it git
would look for the member's key and `known_hosts` in the real user's home and
fail host key verification.

The server under test is the shipped binary, so its containers carry only the
production `aether.managed` label - there is no test label to sweep on.
Teardown asks the server for its members and runs, then removes those
containers by name, along with the `aether/member-<member-id>` images an
environment save commits. A failed test keeps its scratch directory and
attaches the server's output to the report.

### Command palette performance

From `web/`, build the ordinary static export with `bun run build`. Use
`bun run build --profile` for a separate React production-profiling export;
do not substitute a development-server measurement for either.

Compare the same browser, viewport, pointer mode and datasets on both
revisions: 50 and 500 runs, with short tasks and varied natural-prose tasks
around 1,600 characters. Keep complete task bodies in the fixtures: the palette
scores only each run's label, branch, agent (`run.harness`), workspace name and ID with
cmdk's default scorer, and long tasks must not slow a keystroke past 16 ms.
Record synthetic fixtures separately from live workspace data, and keep
fixtures and raw traces outside the source tree.

Measure the first opening separately from at least 20 warm reopens. Use trusted
keyboard and search-button input, repeated queries, backspacing and clearing.
Report scorer time separately from input-to-results and opening latency, plus
result counts, long tasks, requests and focus/selection behavior. An
event-to-`requestAnimationFrame`-plus-timer measurement is a paint-opportunity
proxy, not compositor latency. Compare ordinary and profiling builds separately.

Check full-text membership and ranking against cmdk's scorer, live run updates,
offscreen keyboard selection, modal focus and Escape restoration. Filtering
must not issue a search RPC. Score-disabled or hidden-Board ablations can locate
a bottleneck; they do not prove the shipped behavior or its speed.

### Scenarios

This inventory describes authored scenarios and their report attachments, not
evidence that they have been executed or passed on a particular checkout.

| Spec | Scenario |
| --- | --- |
| `account-sharing` | One member shares their agent account from **Profile > Account sharing** with a teammate. Before the share, the teammate's **New run** offers no account choice; after it, choosing the shared account lists the agents installed in the owner's home and disables each one the owner has no login for, with a status sentence naming the missing logins. The owner's already running Environment prompts **Stop environment**. Once the owner has a login, the teammate's run launches with the owner's login mounted, and **Details** shows the teammate as Owner and the sharer under Agent account |
| `activity` | A real run's log in Activity: rows in product words with no wire names, one **Filter** popover narrowing to Run state, **Raw events** from the page's More menu printing wire types and payloads, and **Show** > Agent messages switching to the empty agent-message history with its search box. A second scenario creates a swarm over RPC, sends real agent messages between its runs, and narrows them by search and to one **Thread** |
| `admin-pages-focus` | Keyboard focus through admin pages: the Members and Devices tabs keep focus on activation, Escape from **Invite…** returns focus to it, and the Manage workspaces row's **More actions** menu and its **Delete…** confirm both return focus to the row button |
| `agent-outcome` | An agent that reports success lands in **Needs you** reading "Finished, review the result", offers **Review** on hover and opens on Changes; opening it clears the server's unseen outcome and the card moves to Finished |
| `board-card` | A run whose agent reports an idle turn lands in Needs you; its **Reply** stays hidden until the card is hovered, sits above the card's open target, and posts through `run.inject` into the run's message history (`run.room.list`); `o` on the focused card opens the run - hit testing and hover only a real browser does |
| `candidate-delivery` | Two retained runs reviewed in **More > Captures… > Candidate review**: packets selected, a candidate prepared and its combined patch loaded, a source-mutating verification fenced as `source_changed` with **Request delivery** disabled, a passing verification in a real container, delivery requested, **Approve delivery** disabled while offline, then approved and delivered after a reload, landing the exact candidate revision. Reloading a delivered candidate reopens its receipt without delivering again. A second scenario prepares a conflicted candidate and applies only the selected resolution, leaving the other file conflicted until its own resolution is applied. Attachments: `candidate delivery review and landed receipt`, `candidate review with one selected conflict resolution` |
| `development-browser/browser.spec.ts` | Shared login, live app update, agent/member control, popups and stale authority through the real companion. **Page tools** owns page/viewport selection, Screenshot and confirmed Close page/Reset session; cancelling close preserves the page and returns keyboard focus, observers cannot close/reset, and reset requires explicit reacquisition before opening another page. The scenario attaches `shared authenticated app` |
| `development-terminal/shared-terminal.spec.ts` | A TUI an agent starts in a named development terminal while the container is still provisioning: a second, phone-sized viewer of the same member cannot stop it, control moves by **Take control** (with its confirm) and **Release**, alternate-screen Unicode output and protocol responses reach both viewers, **Take a screenshot** captures the terminal with its metadata, and **Stop this shell** ends the process. Attachments: `shared-alternate-screen`, `shared-terminal-capture`, `shared-terminal-capture-metadata` |
| `keyboard-focus` | Escape closes a dialog or the sidebar footer menu on a run without leaving the run; focused shell controls paint the app's outline with computed style and 3:1 contrast against the actual background; the sidebar resizes by pointer delta and by keyboard, and at its minimum width keeps Search, New run and Mine inside it |
| `mission-candidate-review` | A swarm created from **Swarms > New swarm** with a shell-fixture integrator: the integrator's question answered from **Questions for you** and folded to `Answered by`, a task proposed and started without a human plan gate, a worker taken with **Take control** on its run and freed with **Release control** on the swarm page, and, without a reload, the worker's report listed under **Agent messages** and the integrator-prepared candidate shown read-only under **Integration**. Attaches `mission candidate progress surface` |
| `onboarding-agents` | The Agent step's setup against the member's real environment container, with a stub `npm` in the environment home standing in for the registry: the comparison starting on Enhanced from `enhanced_default` before the adapter is installed, `agent.install` running the real Codex install command, the terminal opening with `codex login` typed, the check reporting "No login found" and never "signed in", then "Login found" once the login file exists, and the row turning to **Run Codex**. An Enhanced pass installs the adapter in the same call and seeds First run in Enhanced; a failed install shows "Install failed" with the command's own output |
| `onboarding-configuration` | An explicit browser directory import from the Agent step's collapsed **Agent config files** disclosure: unknown basename destination selection, switching from OMP exclusions to Claude's narrower policy without losing valid files, an empty file preserved, a server-side secret exclusion shown, accepted files written to the member's persistent home, and the `config.read`/`config.write` revision path |
| `onboarding-first-member` | A fresh server through the steps Connect, Repository, Agent and First run: Connect (first identity becomes admin, SSH key generated, the git identity this machine's `git config` offers saved at the bottom of the step), then Repository: create the workspace, point it at a local repository, push, and read git's own `[new branch]` in the "What git did" panel |
| `onboarding-first-run` | Launching the first run from the launch form on an agent installed into the member's environment home, watching its work complete, using the reusable shell after the agent exits, and closing the run as **Merged** from **More > Close run…**; and, with nothing installed, the step saying "No agent is installed yet" instead of the form and sending the reader back to Agent |
| `onboarding-github` | The Agent step's **Connect GitHub** screen, opened from its GitHub disclosure, against the member's own environment container, in two acts. First with no gh in it: the screen says "There is no gh in your Environment", names both halves of the remedy - the admin's `docker pull` of the standard image and the member's `aether terminal stop` - and shows no `gh auth login` command at all. Then Back, a stub `gh` installed into the member's environment home, and the screen reopened: "The login command is ready in your Environment" - the state, because the command block alone is also what a failed check shows - the stub's own log proving the terminal typed that login into the container, the account and signing-key fingerprint the connect reports, the key on disk and registered through gh, the home's `.gitconfig` carrying both gh's credential helper and the signing settings, and Back closing the sub-screen without leaving the step |
| `onboarding-navigation` | Back from every step and the header's jump to a reached step, with the workspace and the connected clone still settled on the way through, and picking the same workspace again keeping its clone |
| `onboarding-second-member` | A collaborator joining on an invite code, onto a workspace someone else created: Repository offers no way to add a workspace and the workspace is picked rather than created. A local-only workspace is seeded by the collaborator's push, with the push command under Advanced; a mirrored workspace says its server copy is pending outside Advanced, shows its source under Advanced, and offers no push |
| `palette-navigation` | The command palette at a short touch viewport with 25 workspaces: the active option is announced and kept scrolled into view through Home, End and arrows, filtering and clearing restore browse order, a workspace deleted mid-search drops out live, Enter opens the chosen workspace (named in the top bar), Tab stays inside the dialog and Escape returns focus to Search |
| `reconnect` | The events socket dropped until the sidebar reads Offline, then a `visibilitychange` reopening it to Live at once instead of waiting out the backoff; and a rejected gateway token reading "This dashboard link has expired" with the gateway's own refusal, not a network guess |
| `remote-development-git/github.spec.ts` | Opt-in real GitHub publication; see [Headless browser and remote-development acceptance](#headless-browser-and-remote-development-acceptance). Attaches `safe-github-acceptance-evidence` |
| `remote-development-git/import.spec.ts` | **Import repository** from Manage workspaces against a real HTTPS source: the outcome reads `Created: yes`, and **Continue to Repository** opens Workspace Source with the observed commit pending until **Adopt candidate**; a failed fetch keeps the created workspace, offers no second import and leads to the same repair dialog. It needs network access to `AETHER_E2E_GIT_SOURCE_URL` (default `https://github.com/3xDevOps/Aether.git`) |
| `remote-development-git/native.spec.ts` | On the real standard image: **Changes > Publish…** commits only a selected path while unrelated staging survives, reports `Index updated: no` against a real `index.lock`, pushes the reviewed branch to a local bare remote, shows the native `gh` failure without erasing the push, and rejects a non-fast-forward without forcing. It runs with or without sudo: every run container trusts `/workspace` as Git's `safe.directory`, so the root standard image accepts a checkout owned by your uid |
| `run-attach-retry` | The terminal tab while it waits out a missing PTY session: sockets that drop and then a `-32004`, the shape a server restart makes, and the tab reports the wait rather than painting itself offline |
| `run-deep-link` | The gateway's own tokened URL with `&run=<id>` appended, which is what both shells load for an `aether://run/<id>` link: the run opens on hydration with the token gone and `?run=<id>` kept, a reload reopens it, Settings pushes `?page=settings`, back returns to the run, and Escape leaves for the Board with a bare address |
| `run-evidence.spec.ts` | Retained finish evidence after run cleanup through **More > Captures…**: retained Summary/Patch/Transcript bytes and source availability, controls reachable on a short desktop, and close/Escape returning focus to More |
| `run-provisioning` | Opening a run while its container is still being built: the terminal tab waits behind "Starting the run's container" instead of showing the gateway's refusal as a dead terminal, and attaches by itself once the run turns running |
| `run-room.spec.ts` | Two members on separate gateways share notes in Details, send moderated messages from the Session composer (the controller denies one and approves another from Details > Needs you; the sender's rows read the countdown, then Denied and Sent), see a reported native question as "Question: answer in the terminal" for the owner and "Waiting for" the owner for the other member, and transfer occupied control. Details is a 320px panel beside the terminal, not an overlay: header and toolbar controls stay hit-testable, and `Mod+.` hiding it widens the PTY, showing it narrows it again. More supports keyboard dismissal and focus return. The scenario attaches `desktop-run-details` and `desktop-run-observer` screenshots |
| `run-session` | An Enhanced run's Session view against acpmock; see [The real-agent smoke tests](#the-real-agent-smoke-tests) |
| `run-switch` | Opening a second run from the sidebar while the first run's terminal is on screen, with the second attach left unanswered: the pane holds no output from the run before it. Busy screens show current output again after a trip to the Board; an owner returning within the reconnect window keeps control; and an owner opening a run on Changes and Session leaves its control free, taking it once Terminal is shown |
| `sidebar-drawer.spec.ts` | In a 600px desktop window, the sidebar sheet opened from the top bar answers `Mod+B` itself, returns focus to the opener and hands the palette back once it closes |
| `templates` | A template saved over RPC listed as one row, **Schedule…** from its row's More menu setting a cron the real server answers with the next launch, and **Launch** opening the run |
| `terminal-geometry` | A newly launched cursor-addressed agent with differently sized writers: shared-grid growth, the same pinned row and relative pixel offset through shared font zoom, return-live in mirror mode and reattach; a large redraw archive opens at a bounded current screen rather than replaying older output |
| `terminal-images` | **Terminal tools > Upload image…** in the Environment terminal and in a live run shell: previewing a PNG, checking the generated `terminal.image` path, and verifying the exact uploaded bytes by SHA-256 in the target shell; the path is safely quoted and not submitted until the test presses Enter |
| `terminal-streaming` | Taking and releasing control without replacing the output socket; scrolling alone through more than 12,000 retained lines across more than 60 pages, with bounded rendered rows, stable cursor/text/pixel anchors during delayed prepend, keyboard browsing and the explicit archive/screen boundary; run A/B switches restore the same rows and horizontal/partial-row offsets under continuing output, close inactive sockets, and refresh the newest archive only after return-live and a new upward-reading episode |
| `terminal-tools` | The Board has no terminal; the Environment page's terminal opens on request with a real environment container. **Terminal tools** lists find, copy, paste and upload at every width and from the keyboard; a touch viewport gets 44px rows. The scenario searches from the menu at two widths and on touch, checks `Ctrl+=` zoom across reload, native `Ctrl+Shift+V` paste, `Ctrl+Shift+F` search and new shell output after leaving the page and coming back |
| `window-sizing` | The Updates dialog, opened from the sidebar's update row, at the smallest window `desktop/main.js` allows and at one smaller browser viewport: every prompt's actions stay on screen and in place through Updating…, Rebuilding… and each end state, bounded technical output does not push the dialog away, and the sidebar stays whole behind it |

`account-sharing`, `activity`, `agent-outcome`, `board-card`,
`candidate-delivery`, `development-browser/browser.spec.ts`,
`development-terminal/shared-terminal.spec.ts`, `keyboard-focus`,
`onboarding-agents`, `onboarding-github`, `onboarding-first-run`'s launch
scenario, `remote-development-git/github.spec.ts`, `run-attach-retry`,
`run-deep-link`, `run-evidence.spec.ts`, `run-provisioning`,
`run-room.spec.ts`, `run-session`, `run-switch`, `templates`,
`terminal-geometry`, `terminal-images`, `terminal-streaming` and
`terminal-tools` need a reachable Docker daemon and skip without one.
`mission-candidate-review` and `remote-development-git/native.spec.ts` also
need Docker but fail without it instead of skipping. That skip is specific to
the dashboard suite: `make test-integration` requires its real Docker setup
and fails when Docker is unavailable. The rest need only git, except
`window-sizing`, which needs neither: it starts a gateway of its own rather
than taking the `aether` fixture, because the CLI half of `update.check` is
answered on the member's own machine and no server is involved.
The terminal image component tests separately pin File type/size validation,
safe insertion without submission, native image-paste registration cleanup,
and stale callback rejection after a terminal target remounts. The clipboard
unit tests pin native `Ctrl+Shift+V` when the async clipboard API is denied.

The focused regressions own the editor and configuration edges without
duplicating the browser smoke path:

- `internal/memberhome/config_test.go` covers member isolation, omitted-root
  reads, revision conflicts, import exclusions and unsafe-file preflight.
- `internal/sshd/config_test.go` covers authenticated own-member config RPC
  authorization and lifecycle behavior.
- `web/src/routes/onboarding/agent-step.test.tsx` covers the import UI's
  explicit action, destination selection and excluded-file reporting, and the
  Agent step's setup, Run and Add agent paths.
- `web/src/components/agents/agent-setup.test.tsx` covers the Standard and
  Enhanced comparison, `agent.install` with and without the adapter, the
  install failure, and the login wording.
- `web/src/store/files.test.ts` covers drafts surviving live-run cache
  invalidation and newer typing surviving an in-flight save.
- `web/e2e/onboarding-configuration.spec.ts` covers the browser import and
  config read/write path; `web/e2e/files-browser.mobile.spec.ts` covers the
  narrow viewport tree/viewer round trip.

### The phone project

`web/playwright.config.ts` defines two projects over the one Chromium install
CI has. `chromium` runs every spec except `*.mobile.spec.ts`, and `mobile`
runs only those, under Playwright's `Pixel 7` descriptor: `isMobile`,
`hasTouch`, a 412x839 viewport, a 2.625 device scale and a mobile user agent.
Nothing runs twice, and no second browser engine is needed. iOS Safari is not
covered - WebKit is not installed.

| Spec | Scenario |
| --- | --- |
| `files-browser.mobile.spec.ts` | On a phone, opening Files through the top bar's sidebar sheet, opening a real repository file, returning with Browse, and opening another file without losing the tree - every control tapped |
| `onboarding-link.mobile.spec.ts` | The Connect step at the height a keyboard leaves: the focused field stays on screen, typing lands, the page does not grow, and the submit can still be scrolled into reach; after linking and pushing a real clone at 390px from the repository page, opened from the Manage workspaces row's **More actions > Repository**, the page has no horizontal overflow and its heading and introductory text stay inside the viewport |
| `shell-drawer.mobile.spec.ts` | On a phone, the sidebar as a modal sheet: it opens from the top bar, its rows are finger-sized, and tapping a run closes the sheet onto that run with focus on its heading, the top bar naming it and no page overflow |
| `dialog-anchor.mobile.spec.ts` | On a phone, a template's Delete confirm, opened from the template row's More menu and short enough to tell a sheet from a centred box, opening as a full-width sheet along the bottom edge; the launch form, opened from **New run** in the top bar (the unnamed `banner` landmark), keeping its Launch button on screen on a viewport as short as a soft keyboard leaves; and a launch refusal sitting below the Mode choice with Launch still more than half the sheet wide |
| `swarm.mobile.spec.ts` | On a phone, a swarm created over RPC with a shell-fixture integrator: its card names the question, the detail repeats the objective in the body, the question is answered from its own card and folds to an `Answered by` row at least 44px tall, and two messages from a real worker fold into `2 messages` and expand, with no sideways scroll |
| `toast-clearance.mobile.spec.ts` | On a phone, deleting a template from its row's More menu and the resulting toast settling 8px clear of the bottom edge, which is what `sonner` needs `mobileOffset` for |
| `run-views.mobile.spec.ts` | On a phone, protecting a real run through the header's More menu, keeping the selected Browser, Session and Changes views fully visible in the switch after touch navigation, then reading the changes: menu items are finger-sized, protection shows by the title, the first file sits right under the Changes strip, and a file section wider than the screen scrolls sideways only once wrap is off |
| `run-room.mobile.spec.ts` | The Details bottom sheet leaves the desktop-controlled PTY geometry unchanged, contains keyboard focus and returns it to **Show details**. A short tap on Take control does not request occupied control. Separate scenarios read Captures as a full-width sheet and use two real sessions to deny incoming control over Details and Captures: the decision remains visible and keyboard/pointer-operable at 390×524 and across the 700→960 breakpoint, then restores the interrupted focus and note draft without transferring control. Screenshots: `phone-run-details`, `phone-captures`, and `holder-over-{details,captures}-{390,700}` |
| `run-evidence.mobile.spec.ts` | Captures at 390x600 with coarse-pointer touch input: open them from More, tap through Patch and Summary, read retained file content, and close with focus returned to More. The scenario attaches `short phone evidence sheet` |
| `development-browser/browser.mobile.spec.ts` | Shared login and live app update on a phone viewport, control handoff, cancellation of Reset session from Page tools without losing the login, expanded browser input, Chromium composition and multi-touch without horizontal page overflow. The scenario attaches `phone shared app`; viewport and CDP input do not prove a physical phone keyboard |
| `terminal-phone.mobile.spec.ts` | A real run's Terminal tab against the real gateway: a desktop writer sets 132x43, and the phone reaches the bottom-row prompt in normal and alternate screens, pans vertically, takes control by holding **Take control** in the run's presence controls and types with the viewport reduced to keyboard height, without resizing the shared PTY. It then follows the desktop writer's resize. A long-output run exercises continuous touch handoff into history, older-page prefetch and exact visible cursor/text/pixel/horizontal anchor preservation across a delayed prepend; horizontal panning does not raise a keyboard or send input |
| `home-screen.mobile.spec.ts` | Everything a phone fetches before it offers to install the dashboard, served by the gateway to an unauthenticated request: the manifest linked from the page, served as `application/manifest+json` and naming a 192px and a 512px icon plus a maskable one, every icon and the `apple-touch-icon` behind it, and the shell laying out whole in a phone viewport with no browser chrome: the top bar in view with its sidebar button and New run. A second scenario sets a 59px `--safe-top` inset: the top bar grows by it rather than moving down, Search sits below it, and the command palette drops with it |

The installed window itself is not in the suite. Chromium exposes no
`display-mode` override - not through `emulateMedia`, not through CDP's
`Emulation.setEmulatedMedia` features, and not through the `PWA` domain, which
headless does not carry - so a standalone window stays a manual check on a
phone (`docs/dashboard-frontend.md` has that path). What the suite can prove is
that Chrome is served a manifest it will accept, and that the shell needs
nothing the browser's chrome was providing.

Mobile specs tap rather than click. `locator.tap()` dispatches touch events,
and a control that answers only a mouse would still pass a click-driven test.
They import `test` from `web/e2e/mobile.ts`, which attaches a full-page
screenshot to every mobile test, passing or failing: a phone layout can be
wrong while every DOM assertion holds, and a green run otherwise leaves
nothing to look at.

`shrinkToKeyboardHeight(page)` in the same file takes the layout viewport
down by 320px, what a keyboard leaves of a portrait phone, and returns the
call that restores it. The shell asks for
`interactive-widget=resizes-content`, so on a browser that honours it - Chrome
and the Android WebView - that is what a real keyboard does to the layout
viewport, and the helper reproduces the shape that ships rather than standing
in for it. iOS Safari ignores the setting and shrinks only the visual
viewport, so for that browser the helper proves the narrower claim: the shell
survives a short screen. Playwright cannot raise a platform keyboard either
way, so content stranded behind a real iOS keyboard stays a manual check on a
phone (`docs/dashboard-frontend.md` has that path).

The phone specs need git; `shell-drawer.mobile.spec.ts`,
`run-views.mobile.spec.ts`, `run-room.mobile.spec.ts`,
`run-evidence.mobile.spec.ts`, `terminal-phone.mobile.spec.ts` and
`development-browser/browser.mobile.spec.ts` also need Docker, because they
open a real run, and skip without it. `swarm.mobile.spec.ts` drives its
integrator's CLI through `docker exec` and fails without Docker. Run them
alone against the binaries `make build` produced:

```sh
cd web && bunx playwright test --project=mobile
```

These specs run in `make test-e2e` and the `dashboard-e2e` job. The job uploads
its `playwright-report` artifact on a pass as well as a failure, so the phone
screenshots are on every run.

Reference screenshots from a Chromium touch audit use synthetic API data
and a 132x43 terminal: [terminal before/after and keyboard-height input](media/mobile-terminal-scroll.webp),
and [short-screen page and dialog layouts](media/mobile-layout-audit.webp).
The audit exercised 390x844, 360x740 and 390x524 viewports, including the
bottom actions of long pages and dialogs. These images show browser layout,
not a physical keyboard or a live vendor session.

### Adding a step to the wizard

`web/e2e/pages/wizard.ts` is the page-object layer, and a new wizard step is
one class and one field. Give the class the `aria-label` of the step's
`<section>` and the actions that step offers, add its label to `stepNames` in
the order the header lists it, and hang it off `OnboardingWizard`. Every
locator a step builds is scoped to its own section, so nothing else in the
suite changes.

## Availability regression commands

These focused commands map the retained regressions in the availability
paths. They are useful before running the full gates; the package tests still
belong in the normal CI jobs.

```sh
# Scheduler wait errors, cancellation, and durable terminal cleanup.
go test -race ./internal/scheduler -run \
  'TestSuperviseWaitRetriesTransportErrorUntilExit|TestSuperviseWaitCancellationDuringRetryLeavesRunLive|TestSuperviseTerminalRetriesTransportErrorUntilExit|TestExitedTerminalCleanupRetainsStateForRetry'

# PTY session isolation, cancellation, and stopped-session replay.
go test -race ./internal/ptyhost -run \
  'TestActiveSessionsDoesNotBlockUnrelatedAttach|TestReserveDoesNotBlockUnrelatedStart|TestAttachContextCancel|TestAttachDrainHonorsContext|TestStoppedSessionClosesAttachmentAndPreservesReplay'

# Direct forwarding: half-close drains; full disconnect cancels.
go test -race ./internal/sshd -run \
  'TestDirectTCPIPOwnerEchoAndHalfClose|TestDirectTCPIPFullDisconnectReleasesBackend|TestDirectTCPIPDisconnectCancelsAddressResolution'

# Scalar cost summaries preserve metered/unmetered budget semantics.
go test -race ./internal/store ./internal/cost -run \
  'TestSummarizeRunCostsMatchesRollupSemantics|TestBudgetReflectsCostHistoryAcrossUpdatesAndWorkspaces|TestUnmeteredSpendNeverCountsTowardTheCap'

# Real git: ignored-tree pruning, live ignore changes, and pack cancellation.
go test -race -tags integration ./internal/gitengine -run \
  'TestDiffWatch|TestUploadPackReturnsOnCtxCancel'
```

The Docker runtime regressions must run against a reachable, real Docker
daemon; the in-process E2E runtime is not a substitute for these checks:

```sh
docker info
go test -race -tags integration ./internal/runtime -run \
  'TestDockerStartCancelDuringSetup|TestDockerStartKillsAfterLostStartReply|TestDockerStartTwiceSkipsSetup|TestDockerWaitBeforeStart|TestDockerInitReapsOrphanedDescendants'
```

`make test-integration` is the merge gate for the complete real-Docker,
real-git integration suite. It must fail rather than silently pass when
Docker is unavailable; run it in CI when the local host has no daemon.

## Failure-table coverage

Every row of the design spec's failure table has at least one covering
scenario; rows not exercised end to end are pinned by unit tests at the
layer that owns them.

| Failure | Covered by |
| --- | --- |
| Agent crashes or hangs | Multi-member E2E (crash -> `failed`, `wip:` commit); stall chaos E2E (park at needs-attention with a `stalled:` reason, surfaced on the run listing, then back to running when the steered agent answers, and still parked when it does not); `TestAgentCrash`, `TestTUIWrapperKeepsNormalShellsAndForwardsStop`, and `TestTUIWrapperForwardsTERMToHarness` in `internal/scheduler`; stall detection matrix in `internal/scheduler` unit tests; the dashboard badge in `web`'s sidebar tests |
| Agent becomes idle or stalls | Status reporter container E2Es; scheduler report/activity/restart tests distinguish explicit idle from terminal repaint and silence-based stalls |
| Outstanding human request | `TestIntegrationRunInputReports` covers socket reports, exact request correlation, independent execution, snapshots, and durable replay; `run-room.spec.ts` crosses the real reporter, server, gateway, and two browser stores; native adapter regressions cover request resolution and interrupted cleanup |
| Server reboot | `TestIntegrationChaosRebootSurvivingContainer`, `TestIntegrationChaosRebootRetainedTUI`, and `TestIntegrationChaosRebootLostContainer` (SIGKILL, active-container reattachment, exact retained TUI identity, and lost-container interruption); `TestRebootRecoveryResumesSupervision`, `TestRebootRecoveryContainerGone`, `TestRecoveryProbeErrorRetainsRunAndContainer`, `TestRecoveryReissuesPersistedKill`, `TestTUICloseRelaunchKeepsExactRunAndContainer`, `TestRetainedExpiryDestroysContainerAndHidesRelaunch`, `TestNegativeRetentionDestroysAndRejectsRelaunch`, `TestBootRetainedDestroyFailureRetriesOnSweep`, `TestRetainedTUIRebootsAndReopensSameContainer`, and `TestStartCancelledDuringRecoveryIsACleanStop` (a shutdown landing inside recovery is a clean stop, not a failed start) in `internal/scheduler`; recovery also exercises runtime error paths |
| Agent update fails before launch | `TestHarnessUpdateFailureStillLaunches`, `TestHarnessUpdateTimeoutStillLaunches`, `TestHarnessUpdateCreateFailureStillLaunches`, `TestHarnessUpdateWaitExpiresAndResultLandsLater`, `TestHarnessUpdateKillDoesNotStopUpdate`, and `TestHarnessUpdateSurvivesCancelledLaunch` in `internal/scheduler` |
| Laptop offline | `internal/syncd` daemon tests (refs-only catch-up) |
| SSH drop mid-attach | Solo E2E detach/reattach; `FuzzAttachDropMidInput`, the post-unwind straggler test and the reattach-leak test in `internal/ptyhost` |
| Live overlay conflict | `internal/sshd` sync overlay tests |
| Disk pressure | Disk chaos E2E (TTL GC under load with branches surviving, the gauge's breakdown, the free-space floor refusing new launch and allowing retained relaunch without new admission); checkout GC in `internal/scheduler`; disk gauge proxy in `internal/localgw` (`TestDiskProxies`) |
| Profile/configuration update fails or is stale | Profile E2E (manual pushes and shared-home updates); `internal/memberhome/config_test.go` (config revisions and import exclusions); browser onboarding configuration E2E |
| Harness login expired | Profile E2E's login-home persistence (re-login writes persist the same way) |
| Budget cap hit | Multi-member E2E (refusal, running run untouched, override); `TestSummarizeRunCostsMatchesRollupSemantics` and full matrix in `internal/sshd` cost tests |
| Scheduled run on stale base | `internal/templates` schedule tests |
| Container wait transport error | `TestSuperviseWaitRetriesTransportErrorUntilExit`, `TestSuperviseWaitCancellationDuringRetryLeavesRunLive`, and the terminal equivalent in `internal/scheduler` |
| Exited environment cleanup | `TestExitedTerminalCleanupRetainsStateForRetry`, `TestRecoveredTerminalAttachFailurePreservesAndRetries`, and `TestRecoveredTerminalPutFailurePreservesAndRetries` in `internal/scheduler` |
| Unprivileged server, root run image | `TestIntegrationRootContainerTrustsUnprivilegedCheckout` in `internal/runrepo`: root git in a real container refuses a checkout another uid owns without `safe.directory`, and both `git status` and the Git panel's status read succeed with it |
| Unprivileged server, root-owned run files | `TestDockerRemoverDeletesRootOwnedFiles` in `internal/runtime`, run as a non-root user: `os.RemoveAll` fails with `permission denied` on files a root container wrote into a bind-mounted directory, and the server's remover deletes them through a root container |
| Docker init and orphan reaping | `TestDockerInitReapsOrphanedDescendants` in `internal/runtime` against a real Docker daemon |
| SSH port forwarding disconnect | `TestDirectTCPIPOwnerEchoAndHalfClose`, `TestDirectTCPIPFullDisconnectReleasesBackend`, and `TestDirectTCPIPDisconnectCancelsAddressResolution` |
| Git ignored-tree watch pressure | `TestDiffWatchPrunesGitIgnoredTrees`, live-rule/tracked/negated-path regressions, `TestDiffWatchIgnoresDirectoryCreatedAfterStart`, and `TestDiffWatchPrunesExistingTreeAfterIgnoreUpdate` against real git; kernel watch counts are checked on Linux |
| Kernel refuses a file watcher | `TestDiffWatchPollsWhenTheKernelRefusesAWatcher` and `TestDiffWatchPollsForASubtreeItCannotWatch`: the watch starts and keeps publishing snapshots by polling, against real git |
| Git pack cancellation | `TestUploadPackReturnsOnCtxCancelWithBlockedOutputAfterReap` (Linux process-exit boundary) and `TestUploadPackReturnsOnCtxCancel` |
| tailscaled down | Multi-member E2E (key members connect, tailnet-only refused with banner) |

## Rules

- Prefer a scenario on the real user path over a pile of internal tests;
  never restate a behaviour already proven at another layer.
- Bug fixes start with an E2E reproduction.
- Keep the suite fast enough to gate merges: agents are scripted and
  deterministic, containers are seconds-lived, and every test sweeps and
  checks for leaked containers via its `aether.test` label.
- Never let a test depend on real time passing. A cache or a deadline takes
  an injectable clock the test winds by hand; a sleep or a tiny TTL is a
  test that fails on whichever platform has the coarsest timer, and
  Windows' is coarse enough that two reads of the clock can return the same
  instant.
- Behaviour that differs by platform gets a test per platform, not one that
  skips. The client packages run on a Windows runner too
  (`.github/workflows/ci.yml`), so a test whose subject refuses on Windows -
  the self-update swap, say - goes in a `//go:build !windows` file with a
  `_windows_test.go` counterpart asserting the refusal. Assert the status
  before the body: an error envelope decodes into a result struct just as
  happily, and a test that reads only the body can pass on the platform
  that refused.
