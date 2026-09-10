# Testing and the E2E scenario suite

Layers, per the design spec's testing strategy:

- **Unit tests** live beside their packages and run with `make test`
  (race detector on). Permission matrices, budget math, profile push
  rules, tailnet auth edge cases, scheduler transitions, and the
  local gateway's own behaviours are proven there, once, and the E2E
  suite does not restate them. Role changes belong to the same layer:
  `internal/sshd/role_test.go` and `internal/sshd/permissions_test.go`
  own promotion, demotion, the last-admin guard and what each role may
  do, and the SPA's half of it (admin affordances gated on role, the
  read-only roster) is in `web/src/routes/members/members.test.tsx`,
  `web/src/components/shell/sidebar.test.tsx` and
  `web/src/components/palette/palette.test.tsx`. The multi-member E2E row
  below joins members and administers them; it does not re-prove the
  matrix.
- **Integration/E2E tests** are behind the `integration` build tag and
  run with `make test-integration` (real Docker, real git). CI runs them
  on every PR in the `integration` job of `.github/workflows/ci.yml`;
  that job is the merge gate the E2E suite owns.
- **Dashboard end-to-end tests** live in `web/e2e/` and run with
  `make test-e2e`: a real browser driving the shipped SPA against a real
  `aether gui` gateway and a real `aether-server`. They own the paths a
  person walks in the dashboard, which no Go test and no jsdom test
  reaches. CI runs them in the `dashboard-e2e` job.

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
| Gateway (`internal/localgw`) | The `aether gui` HTTP/WS surface, covered at this layer by unit tests against a stub backend: token-gated API round-trips (`api_test.go`), diff and disk proxies, capability reporting, and the `/ws/attach` mirror and steer channels (`ws_test.go`). A real gateway against a real server is the dashboard suite below |
| `TestIntegrationMultiMember` (`multimember_integration_test.go`) | Three clients: tailnet initial join and invite-code key joins, WhoIs-down fallback with banner, remote administration, steering another member's run, presence roster, handoff, approval inbox, budget cap and override, agent crash -> `failed` + `wip:` commit, and the finished branch authored as the run's owner after the handoff, committed by Aether, and carrying one `Co-authored-by:` trailer per other steerer |
| `TestIntegrationProfileSyncAndLogins` (`profile_integration_test.go`) | Profile sync and harness logins: a login in the environment terminal persists into two runs, push -> next run sees it, mid-run push never touches a running agent, denylisted credential names refused from pushes (Docker only - it needs a real terminal) |
| `TestIntegrationGitHubConnect` (`github_integration_test.go`) | Connecting GitHub end to end in one environment terminal, with the stub `gh` swapped in the bind-mounted member home so it comes first on that container's `PATH`: three `github.probe` round trips over the same container - no gh, gh 2.45, then a current one - report `missing` with a remedy, `outdated` naming the version it found, and `ok` with no remedy, and the first two are refused by `github.connect` by name, before the login is asked about at all; then `github.connect` sets up git credentials, generates the signing key and registers it, a run pushes its branch to a bare `origin` repository inside the member home and commits with the signing config that home carries, and Aether's own end-of-run commit verifies against the member's public key through an allowed-signers file |
| `TestIntegrationMemberEnvironmentImage` (`environment_image_integration_test.go`) | The saved environment image: what the container layer keeps, and that a container started from it **without** the member home mounted has no signing key, no `.gitconfig` and no gh token - Docker's commit never captures a bind mount |
| `TestIntegrationCoordinationEndToEnd`, `TestIntegrationCoordinationKillSwitch` (`coordination_integration_test.go`) | Conflict radar and run-to-run coordination over the MCP bridge, including server restart with surviving containers and the kill switch |
| `TestIntegrationCoordinationInContainer` (`coordination_container_integration_test.go`) | The same bridge inside real containers: both binds realized and read-only, `mcp.json` and `co-authors` found at `0444` inside the container, the staged binary executed as `/opt/aether/aether-server mcp` by a non-root agent, and a status/send/inbox round trip between two overlapping runs |
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
the provider is proof the CLI accepted the flags. The `integration` job
builds `images/standard/Dockerfile`, then `images/smoke/Dockerfile` on top
of it to add the agent CLIs at whatever version their vendors ship that
day, and points the gate variables at the result.

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

`pi` has no smoke test: it is not in the smoke image, so nothing pins its
template. Adding it is one more install line and one more map entry.

## The dashboard end-to-end suite

`web/e2e/` drives the dashboard the way a person does: a Chromium browser on
the SPA the CLI embeds, talking to a real `aether gui` gateway, which proxies
every call over a real SSH connection to a real `aether-server`. Playwright
is the runner, pinned to an exact version in `web/package.json`.

```sh
(cd web && bunx playwright install chromium)   # once, from the repo root
make test-e2e
```

`make test-e2e` builds the dashboard and both binaries first. The CLI serves
the SPA out of its own embedded `web/dist`, so a stale binary would test a
stale dashboard.

### What each test gets

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
  `known_hosts` entry it writes, the saved link config and the agent
  configuration a profile push reads all belong to that member and never
  touch the developer's own. `PATH` and `SHELL` are fixed too, because
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
| `onboarding-configuration` | Bringing a member's own agent configuration across, from a fixture home holding an empty file and a file the secret scanner flags: the flagged file is named on the row and left out, everything else imports |
| `onboarding-first-run` | Launching the first run on an agent installed into the member's environment home, and watching it reach needs-attention with its work committed; and, with nothing installed, the step offering "Set up an agent" instead of a picker and sending the reader back to Agents |
| `onboarding-navigation` | Back from every step, with the workspace and the connected clone still settled on the way through, and the Git identity step reached in both directions between Link and Workspace |
| `run-attach-retry` | The terminal tab while it waits out a missing PTY session: sockets that drop and then a `-32004`, the shape a server restart makes, and the tab reports the wait rather than painting itself offline |
| `run-provisioning` | Opening a run while its container is still being built: the terminal tab waits behind "Starting the run's container" instead of showing the gateway's refusal as a dead terminal, and attaches by itself once the run turns running |
| `run-switch` | Opening a second run from the sidebar while the first run's terminal is on screen, with the second attach left unanswered: the pane holds no output from the run before it |
| `terminal-tools` | The board's terminal dock: closed until the header strip is used, a real environment container behind it, `Ctrl+=` resizing the live terminal and surviving a reload, and `Ctrl+Shift+F` finding what the shell printed and saying "No matches" when it did not |
| `window-sizing` | The update prompts at the smallest window `desktop/main.js` allows, and at one smaller than that: every control the prompt carries sits on its first row in each state that offers one, and neither the app nor the status bar leaves the window |
| `status-bar-sizing` | The status bar carrying every readout the width allows, on a server that is then stopped so its longest notice appears: the palette, shortcuts and theme controls stay in the window and the readouts give way inside their own group |
| `keyboard-focus` | The two accessibility claims a jsdom test cannot make: Escape on a dialog over a run closes the dialog without also leaving the run, which turns on an ordering only a real key press produces; and a focused control paints the app's own outline, measured as computed style against the `--ring` token rather than as a class name |

`board-card`, `keyboard-focus`, `onboarding-agents`, `onboarding-github`,
`onboarding-first-run`'s launch scenario, `run-attach-retry`,
`run-provisioning`, `run-switch` and `terminal-tools` need a reachable
Docker daemon and skip without one, the way the Go suite skips its container
scenarios. The rest need only git, except `window-sizing`, which needs
neither: it starts a gateway of its own rather than taking the `aether`
fixture, because the CLI half of `update.check` is answered on the member's
own machine and no server is involved.

### Adding a step to the wizard

`web/e2e/pages/wizard.ts` is the page-object layer, and a new wizard step is
one class and one field. Give the class the `aria-label` of the step's
`<section>` and the actions that step offers, add its label to `stepNames` in
the order the header lists it, and hang it off `OnboardingWizard`. Every
locator a step builds is scoped to its own section, so nothing else in the
suite changes.

## Failure-table coverage

Every row of the design spec's failure table has at least one covering
scenario; rows not exercised end to end are pinned by unit tests at the
layer that owns them.

| Failure | Covered by |
| --- | --- |
| Agent crashes or hangs | Multi-member E2E (crash -> `failed`, `wip:` commit); stall chaos E2E (park at needs-attention with a `stalled:` reason, surfaced on the run listing, then back to running when the steered agent answers, and still parked when it does not); stall detection matrix in `internal/scheduler` unit tests; the dashboard badge in `web`'s sidebar tests |
| Server reboot | Both reboot chaos E2Es (SIGKILL mid-run, surviving and lost container); coordination kill-switch E2E (restart with surviving containers); recovery matrix in `internal/scheduler`, plus the launch-to-relaunch session pin there (`--session-id` at launch, `--resume <uuid>` on the relaunch, `--continue` for a row that predates pinning); the argv construction itself in `internal/harness` |
| Laptop offline | `internal/syncd` daemon tests (refs-only catch-up) |
| SSH drop mid-attach | Solo E2E detach/reattach; `FuzzAttachDropMidInput`, the post-unwind straggler test and the reattach-leak test in `internal/ptyhost` |
| Live overlay conflict | `internal/sshd` sync overlay tests |
| Disk pressure | Disk chaos E2E (TTL GC under load with the branches surviving, the gauge's breakdown, the free-space floor refusing launch and relaunch); checkout GC in `internal/scheduler`; disk gauge proxy in `internal/localgw` (`TestDiskProxies`) |
| Profile push fails / stale | Profile E2E (runs pin the last good snapshot; bad pushes refused) |
| Harness login expired | Profile E2E's login-home persistence (re-login writes persist the same way) |
| Budget cap hit | Multi-member E2E (refusal, running run untouched, override); full matrix in `internal/sshd` cost tests |
| Scheduled run on stale base | `internal/templates` schedule tests |
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
