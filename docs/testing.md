# Testing and the E2E scenario suite

Layers, per the design spec's testing strategy:

- **Unit tests** live beside their packages and run with `make test`
  (race detector on). Permission matrices, budget math, configuration import
  and file revision rules, tailnet auth edge cases, scheduler transitions, and
  the local gateway's own behaviors are proven there, once, and the E2E suite
  does not restate them.
  Role changes belong to the same layer: `internal/sshd/role_test.go` and
  `internal/sshd/permissions_test.go` own promotion, demotion, the last-admin
  guard and what each role may do, and the SPA's half of it is in its rendered
  route tests. The multi-member E2E row below joins members and administers
  them; it does not re-prove the matrix.
- **Integration/E2E tests** are behind the `integration` build tag and run with
  `make test-integration` (real Docker, real git), which covers only the
  packages carrying integration-tagged tests. `INTEGRATION_PKGS` narrows that
  to one package and `INTEGRATION_SKIP` leaves some out, as in
  `make test-integration INTEGRATION_PKGS=./internal/server`. CI runs them on
  every PR from `.github/workflows/ci.yml`: the `integration` matrix shards
  them by package, one job each for `internal/server` and `internal/scheduler`
  and one for the rest, and the `smoke` job runs `internal/harness` on the
  images it builds. Those jobs are the merge gate the E2E suite owns.
- **Dashboard component tests** live beside their components in `web/src/`
  and run with `bun run test` from `web/` (vitest in jsdom). CI runs them in
  the `dashboard` job. jsdom has no layout, so `web/src/test/setup.ts`
  answers every media query with `false` and a component renders its widest
  branch; `atViewport` (`web/src/test/viewport.ts`) puts one test on one
  screen instead - width and pointer queries answer for it,
  `window.innerWidth`/`innerHeight` report it, and the returned resize fires
  `change` where an answer moved and `resize` on the window. It decides
  which branch renders and nothing more: real layout belongs to the browser
  suite below.
- **Dashboard end-to-end tests** live in `web/e2e/` and run with
  `make test-e2e`: a real browser driving the static Next export embedded by
  the shipped binary, through a real `aether gui` gateway and a real
  `aether-server`. They own the paths a person walks in the dashboard, which
  no Go test and no jsdom test reaches. CI runs them in the `dashboard-e2e`
  job.

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
| `TestIntegrationCoordinationInContainer` (`coordination_container_integration_test.go`) | The same bridge inside real containers: both binds realized and read-only, `mcp.json` and `co-authors` found at `0444` inside the container, the staged binary executed as `/opt/aether/aether-server mcp` by a non-root agent, and a status/send/inbox round trip between two overlapping runs |
| `TestIntegrationAgentStatusReporterInContainer` (`agentstatus_integration_test.go`) | The status reporter inside a real container, on the shipped `claude` and `pi` profiles in one server: each asset written at `0444` into the run's coordination directory, the argument pointing the harness at it, the staged binary running `aether-server report claude` and `report pi --event ...` against the run's own socket, and each run parking at needs-attention with `waiting for your input` seconds after the agent's turn ends - not after the stall threshold - then returning to running with `agent resumed` on the agent's next turn |
| `TestIntegrationOpenCodeStatusReporterInContainer` (`agentstatus_integration_test.go`) | The same path for a harness that has no flag to point at its reporter: the plugin written at `0444` into the run's coordination directory, `OPENCODE_CONFIG_CONTENT` naming it from inside the container with the launch command left exactly as it was, the staged binary running `aether-server report opencode --event session.idle` against the run's own socket, the run parking at needs-attention with `waiting for your input` seconds after the turn ends, and returning to running with `agent resumed` when the agent takes the steer |
| `TestIntegrationChaosRebootSurvivingContainer`, `TestIntegrationChaosRebootLostContainer` (`chaos_reboot_integration_test.go`) | The server SIGKILLed mid-run: supervision reattaches to a surviving container (steer and finalize both still work) or, when the container went with it, commits `wip:`, publishes the branch, marks the run interrupted and relaunches it. SQLite and git are read back after the kill |
| `TestIntegrationChaosDiskPressure`, `TestIntegrationChaosStallUX` (`chaos_pressure_integration_test.go`) | Worktree TTL GC under load with the branches surviving, the gauge's three-way breakdown following the reclaim, new runs refused below the free-space floor, and a silent agent parking at needs-attention, coming back when the agent answers a steer, and staying parked when it does not |

### The chaos scenarios

`chaos_reboot_integration_test.go` is the one place the suite runs
`aether-server` as a **child process**. An in-process server cannot be
SIGKILLed, and the whole point of that row is that nothing on the shutdown
path runs: the next boot only sees what SQLite and git had already made
durable. The child binary is built once per test binary, the store is seeded
before startup, and it binds a reserved loopback port so a restart can claim
the same address. It always builds its own Docker runtime, so those two
scenarios skip without a reachable daemon rather than falling back, and they
address containers by the name the runtime derives from the run ID.

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
`ServerBinary` names the binary staged as the in-container MCP bridge.

### The container coordination scenario

`coordination_container_integration_test.go` proves the half of
docs/mcp-bridge.md that an assertion on a container spec cannot: that the
mounts a run is given are real, and that the agent holding them can use
them. Two things make it possible.

The staged bridge has to be a binary that has the `mcp` subcommand, which
under `go test` `/proc/self/exe` is not. So the scenario points
`ServerBinary` at an `aether-server` it builds - the same one the chaos
scenarios run as a child process.

The agent has to be launched by the shipped `claude` profile, because a
`Harnesses` argv override is respected verbatim and takes the MCP
registration with it. So the scenario builds a run image whose `claude`
executable is the fixture agent in `internal/server/testdata/coordagent`,
running as a non-root user. The fixture knows no Aether paths: it takes the
coordination directory from the `--mcp-config` it was handed and the bridge
command from that config, the way a real harness would. What it found goes
on its terminal, where the test reads it over a real attach: the modes,
both binds read-only in the kernel's own mount table, a write the
coordination directory refuses with EROFS, and every tool result. The
daemon's own view of the two binds is checked beside it.

The container user is the test process's own uid:gid unless that is root:
the scheduler chowns the run checkout and the member home to the container
user before creating the container, and an unprivileged test process can
only chown to itself.

### The real-harness smoke tests

`internal/harness/smoke_integration_test.go` launches the vendors' actual
CLIs and checks that the argv Aether ships is still the argv they accept.
Nothing else catches a vendor renaming a flag or refusing a combination it
used to allow: a change like that breaks every run of that harness and no
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
that harness's login state, named by `AETHER_SMOKE_IMAGE_<NAME>`. CI has no
such credentials, so those stay a manual check. Every one of them skips when
its variable is unset, and the no-login test skips as a whole rather than
reporting a pass with nothing run.

`pi` and `omp` have no smoke test: neither is in the smoke image, so
nothing pins their templates. Adding one is an install line and a map
entry each.

The status extension those two load, `internal/agentstatus/status.ts`, is
the one shipped file the Go build never executes. `internal/agentstatus`
runs it under `bun` against a recording stand-in for the server binary, so
a turn's reports are proven to come out in order and to end exactly once.
Those tests skip where `bun` is not installed.

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
  wizard's Link step does.
- One `aether gui` per member, each with its own `HOME` and
  `AETHER_CONFIG_DIR`, so the SSH key the Link step generates, the
  `known_hosts` entry it writes, the saved link config and the member's
  persistent agent/configuration home all belong to that member and never touch
  the developer's own. `PATH` and `SHELL` are fixed too, because
  `env.harnesses` reports what is installed on this machine and that answer
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

### Scenarios

| Spec | Scenario |
| --- | --- |
| `board-card` | What a board card gives up without opening the run: the branch name's `title` resolving under the card's click overlay, the name selectable, and the copy control copying rather than navigating - all of which only a browser that hit-tests can check |
| `onboarding-first-member` | A fresh server: link (first identity becomes admin, SSH key generated), set the git identity from what this machine's `git config` offers, create the workspace, point the step at a local repository, push, and read git's own `[new branch]` in the "What git did" panel |
| `onboarding-second-member` | A second member joining on an invite code, onto a workspace someone else seeded: the workspace is picked rather than created, and the push offer is replaced by "already has main at ..." with nothing pushed |
| `onboarding-agents` | The Agents step's setup screen: the install command, the environment container starting, Back closing the sub-screen without leaving the step, and "I've installed and logged in" saving the environment to a member image |
| `onboarding-github` | The Agents step's Connect GitHub screen against the member's own environment container, in two acts. First with no gh in it: the screen names both halves of the remedy - the admin's `docker pull` of the standard image and the member's `aether terminal stop` - and shows no `gh auth login` command at all. Then Back, a stub `gh` installed into the member's environment home, and the screen reopened: the screen reporting the login command ready - the state, because the command block alone is also what a failed check shows - the stub's own log proving the dock typed that login into the container, the account and signing-key fingerprint the connect reports, the key on disk and registered through gh, the home's `.gitconfig` carrying both gh's credential helper and the signing settings, and Back closing the sub-screen without leaving the step |
| `onboarding-configuration` | An explicit one-time browser directory import: unknown basename destination selection, switching from OMP exclusions to Claude's narrower policy without losing valid files, an empty file preserved, a server-side secret exclusion shown, accepted files written to the member's persistent home, and the `config.read`/`config.write` revision path |
| `onboarding-first-run` | Launching the first run on an agent installed into the member's environment home, and watching it reach needs-attention with its work committed; and, with nothing installed, the step offering "Set up an agent" instead of a picker and sending the reader back to Agents |
| `onboarding-navigation` | Back from every step, with the workspace and the connected clone still settled on the way through, and the Git identity step reached in both directions between Link and Workspace |
| `run-attach-retry` | The terminal tab while it waits out a missing PTY session: sockets that drop and then a `-32004`, the shape a server restart makes, and the tab reports the wait rather than painting itself offline |
| `run-provisioning` | Opening a run while its container is still being built: the terminal tab waits behind "Starting the run's container" instead of showing the gateway's refusal as a dead terminal, and attaches by itself once the run turns running |
| `run-switch` | Opening a second run from the sidebar while the first run's terminal is on screen, with the second attach left unanswered: the pane holds no output from the run before it |
| `terminal-tools` | The board's terminal dock: closed until the header strip is used, a real environment container behind it, `Ctrl+=` resizing the live terminal and surviving a reload, native `Ctrl+Shift+V` paste through the terminal's input path, `Ctrl+Shift+F` searching shell output, and new shell output after collapsing and reopening the dock |
| `terminal-geometry` | A newly launched cursor-addressed agent with differently sized writers: correct shared-grid growth, zoom, observe/steer, and reattach; a long redraw log opens as a compact current screen with responsive input, while History can display the first output and return to the unchanged live terminal |
| `terminal-images` | Choosing a PNG in the terminal dock's file chooser, previewing it, checking the generated `terminal.image` path, and verifying the exact uploaded bytes by SHA-256 in both the member environment shell and a live run shell; the path is safely quoted and not submitted until the test presses Enter |
| `window-sizing` | The update notices at the smallest window `desktop/main.js` allows, and at one smaller browser viewport: controls remain on their own first row, bounded technical output does not push the shell away, and the status actions stay reachable |
| `status-bar-sizing.spec.ts` | A real linked member followed by a stopped server: primary actions stay visible at compact desktop widths, full secondary readouts open by keyboard, and the mobile details menu keeps every control inside the viewport; the phone behavior is covered by `status-bar.mobile.spec.ts` below |
| `files-browser.mobile.spec.ts` | At a narrow viewport, opening a real repository file, returning with Browse, and opening another file without losing the tree; the explorer/editor's workspace base, live-run and member-configuration writes are covered by focused regressions |
| `sidebar-drawer.spec.ts` | In a 600px desktop window, the sidebar drawer answering `Mod+B` itself and handing the palette back once it closes |
| `keyboard-focus` | Real browser checks that Escape closes a dialog on a run without leaving the run, and that a focused control paints the app's outline with computed style and contrast against the actual background |

`board-card`, `keyboard-focus`, `onboarding-agents`, `onboarding-github`,
`onboarding-first-run`'s launch scenario, `run-attach-retry`,
`run-provisioning`, `run-switch`, `run-views.mobile.spec.ts`,
`shell-drawer.mobile.spec.ts`, `terminal-geometry`, `terminal-images` and `terminal-tools` need a
reachable Docker daemon and skip without one. That skip is specific to the
dashboard suite: `make test-integration` requires its real Docker setup and
fails when Docker is unavailable. The rest need only git, except
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
- `web/src/routes/onboarding/agents-step.test.tsx` covers the import UI's
  explicit action, destination selection and excluded-file reporting.
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
| `files-browser.mobile.spec.ts` | On a phone, opening a real repository file from the sidebar rail, returning with Browse, and opening another file without losing the tree - every control tapped |
| `onboarding-link.mobile.spec.ts` | The Link step at the height a keyboard leaves: the focused field stays on screen, typing lands, the page does not grow, and the submit can still be scrolled into reach |
| `status-bar.mobile.spec.ts` | A phone-width status bar after the server has gone: the details popup opens on a tap and keeps every control, the long member name and the unreachable notice inside the viewport; on a screen too short for its own readouts it scrolls to them rather than cutting them off, and the theme toggle answers a tap on the bottom edge |
| `shell-drawer.mobile.spec.ts` | On a phone, the run list as a modal drawer: it opens from the rail, its rows are finger-sized, and tapping a run leaves the drawer closed with that run on screen |
| `dialog-anchor.mobile.spec.ts` | On a phone, a confirm short enough to tell centred from top-anchored sitting at the top of the screen, and the launch form keeping its Launch button on screen on a viewport as short as a soft keyboard leaves |
| `toast-clearance.mobile.spec.ts` | On a phone, a toast settling above the 44px status bar rather than over it, which is what `sonner` needs `mobileOffset` for |
| `run-views.mobile.spec.ts` | On a phone, steering a real run from the one Actions menu the run header keeps, and then reading its diff: the menu items are finger-sized, protecting the run shows on the header, and the file section that holds a line wider than the screen scrolls sideways only once the wrap toggle is off |
| `terminal-phone.mobile.spec.ts` | A real run's Terminal tab on a phone, against the real gateway: a desktop-sized writer attached straight to the gateway's WebSocket sets the session to 132x43, the phone opens as a mirror rather than steering, renders every one of those rows at that width and pans over them, and the session is still 132x43 after the phone takes control, taps Esc from the key bar and loses half its screen to a keyboard - read back from the server through a fresh attach ack, not inferred from what the phone sent. Then the writer's window changes, and the phone follows it there |

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
`run-views.mobile.spec.ts` and `terminal-phone.mobile.spec.ts` also need
Docker, because they open a real run, and skip without it. Run them alone
against the binaries `make build` produced:

```sh
cd web && bunx playwright test --project=mobile
```

They add about 15 seconds to `make test-e2e` and to the `dashboard-e2e` job,
which stays inside the suite's 30-minute `globalTimeout` unchanged. That job
uploads its `playwright-report` artifact on a pass as well as a failure, so
the phone screenshots are on every run.

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
| Agent crashes or hangs | Multi-member E2E (crash -> `failed`, `wip:` commit); stall chaos E2E (park at needs-attention with a `stalled:` reason, surfaced on the run listing, then back to running when the steered agent answers, and still parked when it does not); stall detection matrix in `internal/scheduler` unit tests; the dashboard badge in `web`'s sidebar tests |
| Agent waiting for a human | Both status reporter E2Es in a real container (`claude` pointed at its hooks and `pi` at its extension by flag, `opencode` at its plugin by environment); the park/un-park matrix in `internal/scheduler` unit tests (a waiting report replaces a stall reason, a repaint does not un-park a harness that reports both ends, activity still un-parks one that only reports turn ends, silence still parks a working one, and a waiting report survives a server restart); the per-harness mapping tables in `internal/agentstatus`, where the opencode plugin is also driven through a turn under `node` (skipped where there is none); `run.report` dispatch and its refusals in `internal/coord`; the `report` subcommand's four callback shapes against a real socket in `cmd/aether-server` |
| Server reboot | Both reboot chaos E2Es (SIGKILL mid-run, surviving and lost container); coordination kill-switch E2E (restart with surviving containers); recovery matrix in `internal/scheduler`, plus the launch-to-relaunch session pin there (`--session-id` at launch, `--resume <uuid>` on the relaunch, `--continue` for a row that predates pinning); the argv construction itself in `internal/harness` |
| Laptop offline | `internal/syncd` daemon tests (refs-only catch-up) |
| SSH drop mid-attach | Solo E2E detach/reattach; `FuzzAttachDropMidInput`, the post-unwind straggler test and the reattach-leak test in `internal/ptyhost` |
| Live overlay conflict | `internal/sshd` sync overlay tests |
| Disk pressure | Disk chaos E2E (TTL GC under load with the branches surviving, the gauge's breakdown, the free-space floor refusing launch and relaunch); checkout GC in `internal/scheduler`; disk gauge proxy in `internal/localgw` (`TestDiskProxies`) |
| Profile/configuration update fails or is stale | Profile E2E (manual pushes and shared-home updates); `internal/memberhome/config_test.go` (config revisions and import exclusions); browser onboarding configuration E2E |
| Harness login expired | Profile E2E's login-home persistence (re-login writes persist the same way) |
| Budget cap hit | Multi-member E2E (refusal, running run untouched, override); `TestSummarizeRunCostsMatchesRollupSemantics` and full matrix in `internal/sshd` cost tests |
| Scheduled run on stale base | `internal/templates` schedule tests |
| Container wait transport error | `TestSuperviseWaitRetriesTransportErrorUntilExit`, `TestSuperviseWaitCancellationDuringRetryLeavesRunLive`, and the terminal equivalent in `internal/scheduler` |
| Exited environment cleanup | `TestExitedTerminalCleanupRetainsStateForRetry`, `TestRecoveredTerminalAttachFailurePreservesAndRetries`, and `TestRecoveredTerminalPutFailurePreservesAndRetries` in `internal/scheduler` |
| Docker init and orphan reaping | `TestDockerInitReapsOrphanedDescendants` in `internal/runtime` against a real Docker daemon |
| SSH port forwarding disconnect | `TestDirectTCPIPOwnerEchoAndHalfClose`, `TestDirectTCPIPFullDisconnectReleasesBackend`, and `TestDirectTCPIPDisconnectCancelsAddressResolution` |
| Git ignored-tree watch pressure | `TestDiffWatchPrunesGitIgnoredTrees`, live-rule/tracked/negated-path regressions, `TestDiffWatchIgnoresDirectoryCreatedAfterStart`, and `TestDiffWatchPrunesExistingTreeAfterIgnoreUpdate` against real git; kernel watch counts are checked on Linux |
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
