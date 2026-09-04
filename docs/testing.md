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
| Gateway (`internal/localgw`) | The `aether gui` HTTP/WS surface, covered by unit tests against a stub backend rather than a server E2E: token-gated API round-trips (`api_test.go`), diff and disk proxies, capability reporting, and the `/ws/attach` mirror and steer channels (`ws_test.go`) |
| `TestIntegrationMultiMember` (`multimember_integration_test.go`) | Three clients: tailnet initial join and invite-code key joins, WhoIs-down fallback with banner, remote administration, steering another member's run, presence roster, handoff, approval inbox, budget cap and override, agent crash -> `failed` + `wip:` commit |
| `TestIntegrationProfileSyncAndLogins` (`profile_integration_test.go`) | Profile sync and harness logins: a login in the environment terminal persists into two runs, push -> next run sees it, mid-run push never touches a running agent, denylisted credential names refused from pushes (Docker only - it needs a real terminal) |
| `TestIntegrationCoordinationEndToEnd`, `TestIntegrationCoordinationKillSwitch` (`coordination_integration_test.go`) | Conflict radar and run-to-run coordination over the MCP bridge, including server restart with surviving containers and the kill switch |
| `TestIntegrationCoordinationInContainer` (`coordination_container_integration_test.go`) | The same bridge inside real containers: both binds realized and read-only, the staged binary executed as `/opt/aether/aether-server mcp` by a non-root agent, and a status/send/inbox round trip between two overlapping runs |
| `TestIntegrationChaosRebootSurvivingContainer`, `TestIntegrationChaosRebootLostContainer` (`chaos_reboot_integration_test.go`) | The server SIGKILLed mid-run: supervision reattaches to a surviving container (steer and finalize both still work) or, when the container went with it, commits `wip:`, publishes the branch, marks the run interrupted and relaunches it. SQLite and git are read back after the kill |
| `TestIntegrationChaosDiskPressure`, `TestIntegrationChaosStallUX` (`chaos_pressure_integration_test.go`) | Worktree TTL GC under load with the branches surviving, the gauge's three-way breakdown following the reclaim, new runs refused below the free-space floor, and a silent agent parking at needs-attention and coming back |

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
command from that config, the way a real harness would, and reports every
mode, refused write, and tool result on its terminal, where the test reads
them over a real attach. The daemon's own view of the two binds is checked
beside them.

The container user is the test process's own uid:gid unless that is root:
the scheduler chowns the run checkout and the member home to the container
user before creating the container, and an unprivileged test process can
only chown to itself.

## Failure-table coverage

Every row of the design spec's failure table has at least one covering
scenario; rows not exercised end to end are pinned by unit tests at the
layer that owns them.

| Failure | Covered by |
| --- | --- |
| Agent crashes or hangs | Multi-member E2E (crash -> `failed`, `wip:` commit); stall chaos E2E (park at needs-attention with a `stalled:` reason, surfaced on the run listing, then back to running when steered); stall detection matrix in `internal/scheduler` unit tests; the dashboard badge in `web`'s sidebar tests |
| Server reboot | Both reboot chaos E2Es (SIGKILL mid-run, surviving and lost container); coordination kill-switch E2E (restart with surviving containers); recovery matrix and the relaunch resume flag in `internal/scheduler`; the resume argv itself in `internal/harness` |
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
