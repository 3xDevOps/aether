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
the coordination scenarios force it because their agents must reach
container surfaces from the test process. Scenarios:

| Test | Scenario |
| --- | --- |
| `TestIntegrationEndToEnd` (`integration_test.go`) | Solo lifecycle, the acceptance gate: seed over git push -> launch -> attach -> detach -> reattach -> steer -> finish -> pull, with the bus traffic checked against the Wave 1 contract |
| `TestIntegrationDashboard` (`dashboard_integration_test.go`) | Dashboard lifecycle on the  gateway's own HTTP/WS wire: token mint over SSH, launch, board hydrate, live terminal over `/ws/attach`, steer from the card, kill from the card, diff patch, disk gauge |
| `TestIntegrationMultiMember` (`multimember_integration_test.go`) | Three clients: tailnet bootstrap/pending/approve and invite-code key joins, WhoIs-down fallback with banner, remote administration, steering another member's run, presence roster, handoff, approval inbox, budget cap and override, agent crash -> `failed` + `wip:` commit |
| `TestIntegrationProfileSyncAndLogins` (`profile_integration_test.go`) | Profile sync and harness logins: a login in the setup shell persists into two runs, push -> next run sees it, mid-run push never touches a running agent, denylisted credential names refused from pushes (Docker only - it needs a real shell) |
| `TestIntegrationCoordinationEndToEnd`, `TestIntegrationCoordinationKillSwitch` (`coordination_integration_test.go`) | Conflict radar and run-to-run coordination over the MCP bridge, including server restart with surviving containers and the kill switch |
| `TestIntegrationChaosRebootSurvivingContainer`, `TestIntegrationChaosRebootLostContainer` (`chaos_reboot_integration_test.go`) | The server SIGKILLed mid-run: supervision reattaches to a surviving container (steer and finalize both still work) or, when the container went with it, commits `wip:`, publishes the branch, marks the run interrupted and relaunches it. SQLite and git are read back after the kill |
| `TestIntegrationChaosDiskPressure`, `TestIntegrationChaosStallUX` (`chaos_pressure_integration_test.go`) | Worktree TTL GC under load with the branches surviving, the gauge's three-way breakdown following the reclaim, new runs refused below the free-space floor, and a silent agent parking at needs-attention and coming back |

### The chaos scenarios

`chaos_reboot_integration_test.go` is the one place the suite runs
`aether-server` as a **child process**. An in-process server cannot be
SIGKILLed, and the whole point of that row is that nothing on the shutdown
path runs: the next boot only sees what SQLite and git had already made
durable. The child binary is built once per test binary, the store is seeded
directly before the first boot (a child has no bootstrap path a test can
drive), and it binds a reserved loopback port so a restart can claim the
same address. It always builds its own Docker runtime, so those two
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

Two `server.Config` fields exist for this suite (and double as
deployment wiring): `WhoIs` overrides tailnet identity resolution so
join and fallback scenarios need no real tailnet, and `Harnesses`
overrides registry argv templates so a registered harness (with its real
profile root and credential mounts) can run a scripted agent.

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
| Disk pressure | Disk chaos E2E (TTL GC under load with the branches surviving, the gauge's breakdown, the free-space floor refusing launch and relaunch); checkout GC in `internal/scheduler`; disk gauge endpoint in the dashboard E2E |
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
