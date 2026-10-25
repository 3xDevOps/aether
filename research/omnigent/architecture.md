# Omnigent vs Aether: philosophy, architecture, runtime, engineering health

Sources: Omnigent `8b46a8a37961616e3e78d7f96c37ed8ccf08f07e` (2026-10-25,
cloned to `/tmp/omnigent`), Aether `77b4f3bb670f8c4effe124a0e4487fd6c2c54fb6`.
Read on 2026-10-25. `omnigent:<path>:<line>` and `aether:<path>:<line>` cite
files at those commits. **(inference)** marks a conclusion drawn from evidence
rather than read directly. Omnigent design docs are often stale; where code
and docs disagree, the code is cited.

## Verdict

1. Different products. Omnigent is a control plane over agent CLIs that by default run on
   your laptop. Aether is a team server that runs every agent in its own container.
2. Overnight team swarms fit Aether's topology. Omnigent's default host is the laptop: when
   it sleeps, sessions stop, and a message sent meanwhile can be lost (section 4). Managed
   cloud sandboxes fix that, but return 403 under the default `accounts` team auth (section 3).
3. Aether is more durable. Omnigent keeps live turn, queue and approval state in process
   memory, streams with no sequence numbers or replay, and kills a dead runner's tmux agent.
   Aether persists a gapless, replayable event log and reattaches to surviving containers.
4. Omnigent's cross-harness layer is broader, not more consistent: 27 harness ids to
   Aether's 9, with a typed capability matrix and a drift bench. But it types messages into
   tmux panes, and its own lab records lost and out-of-order events. Aether's flakiness sits
   at the per-harness wake and hook edge, which is under a month old, not in its mailbox.
5. Aether is weaker in four places: one host is the capacity ceiling, every run holds the
   launcher's credentials, there is no egress control or tool policy, and its team and
   harness breadth are small.
6. Port these to Go as designs, not code: a harness capability matrix with a conformance
   bench, an egress allow-list proxy with credential swap-on-access, a narrow hook-based
   ALLOW/DENY/ASK policy, and "durable host id, ephemeral sandbox". Replace Aether's
   prose-only agent limits with a test-enforced capability matrix. Do not embed Omnigent's
   Python.
7. Investor view: Omnigent has momentum (11,024 stars, 307 authors, ~42 commits a day,
   Databricks-run) and the governance story, but is alpha, with half its commits fixes and
   1,505 open PRs. Aether's unique value is real but narrow: a self-hosted, multiplayer,
   container-per-run agent server whose swarms end as verified branches. Two people build it.

## 1. Philosophy

**Omnigent.** One harness-neutral session layer over every agent CLI and SDK,
so you can swap or mix vendors without rewriting, and govern them with
policies and sandboxes (`omnigent:README.md:7`). Agents run where the user
already is: the laptop host by default, or a cloud sandbox the server
provisions (`omnigent:deploy/README.md:236-240`,
`omnigent:omnigent/server/managed_hosts.py:1-23`). The server is a thin,
deployable coordinator, and agents are composable YAML specs; the runtime is
"a library, not a service" (`omnigent:omnigent/runtime/README.md:5`).

**Aether.** A self-hosted Linux server where a team runs and jointly steers
coding agents, each run in its own container with its own branch
(`aether:README.md:5`, `aether:docs/README.md:22-23`). The server owns compute,
state and coordination: the vendor's CLI is the agent, and Aether supplies
the environment, the durable record and the protocol between runs
(`aether:docs/agent-execution-separation.md:3-8`). It keeps one host and one
binary per role, and avoids plugin loaders and abstractions it does not need
(`aether:docs/adapters.md:172-174`).

## 2. Concept map

| Omnigent | Aether | Same / different / missing |
| --- | --- | --- |
| **Session**: a conversation row plus its bound host and runner. A sub-agent is a child session (`omnigent:omnigent/tools/builtins/spawn.py:65-94`). | **Run**: one agent, one task, one container, one checkout, one branch (`aether:docs/README.md:22-23`). | Different. A session is a conversation that can run anywhere. A run is a unit of compute, checkout and branch. |
| **Agent**: a YAML spec with prompt, `executor.harness`, tools, sub-agents and policies (`omnigent:docs/AGENT_YAML_SPEC.md:39-65`). | Agent **profile** (argv, env, credential paths; `aether:docs/adapters.md:15-16`) plus **template** (a saved launch, optionally on cron; `aether:docs/teams.md:310-311`). | Missing in Aether: there is no language for defining agents. The vendor CLI is the agent. |
| **Harness**: 27 ids with a typed capability profile each (`omnigent:omnigent/harness_plugins.py:356-790`). | **Harness**: 9 built in plus `custom` (`aether:internal/harness/harness.go:404-653`). | Same concept. Omnigent's capabilities are machine-readable; Aether's are prose (`aether:docs/harnesses.md`). |
| **Host**: the `omnigent host` daemon on a laptop or in a sandbox. It dials the server and spawns runners (`omnigent:omnigent/host/connect.py:1-7`). | **Server**: `aether-server` is control plane and compute on one Linux box (`aether:docs/README.md:11`). | Different. Omnigent separates control from compute; Aether merges them. |
| **Runner**: a per-session FastAPI process that owns harness subprocesses (`omnigent:omnigent/runner/__init__.py:1-12`). | Scheduler supervision, `ptyhost` and `acphost` in the server, with a container per run (`aether:internal/ptyhost/ptyhost.go:1-2`, `aether:internal/acphost/session.go:1-2`). | Similar role, different placement. Omnigent's runner is a separate process that can crash on its own. |
| **Server**: coordinator only. "No agent code runs inside it" (`omnigent:deploy/README.md:236-240`). | Server: coordinator, Docker runtime and SQLite store. | Different. |
| **Managed host**: a server-provisioned cloud sandbox. The host id is durable, the sandbox generation is not (`omnigent:omnigent/server/managed_hosts.py:11-15`). | None. The run container always lives on the one server. | Missing in Aether. |
| **Sandbox**: bwrap, seatbelt or a Windows Job Object, an optional L7 egress proxy, or a provider sandbox. It is off for native wrappers (`omnigent:omnigent/harnesses/claude_native/main.py:3603-3606`). | **Run container**: the only boundary. Docker, or Sysbox for nested Docker; no egress control (`aether:docs/security.md:49-58,617-619`). | Different. Omnigent's is fine-grained but optional; Aether's is always on but coarse. |
| **Policy**: ALLOW/DENY/ASK over six phases at server, agent and session level (`omnigent:docs/POLICIES.md:3-21`). | None for agents: permission prompts are off in every mode (`aether:docs/harnesses.md:358-359`). Humans get roles, `protect` and an advisory budget. | Missing in Aether. |
| **Project**: an owner-private group of sessions with default settings (`omnigent:omnigent/entities/project.py:1-8`). | **Workspace**: repo plus base branch, runs, notes and history, shared with the team (`aether:docs/README.md:19`). | Different. Aether's is team-shared and bound to a repo; Omnigent has no team workspace. |
| **Extension**: UI pages and nav via entry points; plus harness, sandbox, git-provider and policy plugins (`omnigent:docs/extending/extensions.md:26-35`). | None, deliberately: "The registry is a map" (`aether:docs/adapters.md:172-174`). | Missing in Aether, by choice. |
| `OSEnvironment`: process-spawning interface (`omnigent:omnigent/environments/__init__.py:1-25`). | **Environment**: a member's long-lived container plus saved image, with a persistent **Home** (`aether:docs/README.md:25-26`). | Same word, different meaning. A persistent per-member dev image is missing in Omnigent. |
| **Tool snapshot**: none. | Removed. Migration v15 dropped `tool_snapshots`, superseded by Home plus Save environment (`aether:internal/store/migrate.go:599-608`). | Obsolete in Aether. |
| Configuration profile: preference snapshots (`omnigent:omnigent/configuration_profile.py:1`). | `aether config push` profile (`aether:internal/profile/profile.go:59`). | Same intent: portable agent config. |
| **Polly**: a 386-line YAML agent that delegates through `sys_session_create` (`omnigent:examples/polly/config.yaml:1-60`). | **Swarm / mission**: a server-owned protocol of integrator, tasks, revisions, attempts and candidate integration (`aether:docs/teams.md:461-466`, `aether:docs/integration.md:1-8`). | Different. Omnigent's orchestration lives in the prompt; Aether's is enforced by server state. |
| `sys_read_inbox`, `async_work_complete` (`omnigent:omnigent/tools/builtins/async_inbox.py`). | `aether-internal send/ask/inbox/ack/report`: a durable, at-least-once mailbox (`aether:docs/coordination.md:227-279`). | Similar intent. Aether's survives restarts and needs an ack. |
| MCP registry gateway, a prototype (`omnigent:designs/MCP_REGISTRY_GATEWAY.md:8`). | **Connectors** (Aether as an MCP server for ChatGPT and Claude) and **mcpbridge** (8 tools in each run) (`aether:docs/connectors.md:3-8`, `aether:docs/mcp-bridge.md:94-101`). | Opposite directions. Omnigent brokers outside tools for its agents; Aether exposes itself to outside assistants. |
| Share, co-drive, fork, with READ/EDIT/MANAGE/OWNER grants (`omnigent:omnigent/server/auth.py:123-126`). | Roles viewer/collaborator/admin, plus steer, handoff and protect (`aether:docs/teams.md:151-176`). | Similar. Omnigent's sharing is per session; Aether lets every collaborator steer by default. |
| Reachability: Cloudflare quick tunnel, Tailscale or a PaaS deploy (`omnigent:README.md:514-518`). | **Edge**: a relay with GitHub sign-in that both sides dial out to (`aether:docs/edge.md:3-7`). | Different. Aether operates its own relay. |
| Local server on `:6767`, started by the host daemon (`omnigent:omnigent/host/local_server.py:10`). | **Local gateway**: `aether gui` on loopback, proxied to the server over SSH (`aether:docs/local-gateway.md:1-15`). | Similar. |
| Memory: the optional `hindsight` extra, an external service (`omnigent:pyproject.toml:268-271`); project memory is Phase 3, pending (`omnigent:designs/PROJECTS_PRD.md:589-607`). | **Team memory**: FTS5 notes per workspace, with pinned briefs (`aether:docs/memory.md:9-12`). | Missing in Omnigent core. |

## 3. Deployment topology

Verified: Omnigent is local-first. Two corrections to the brief:

- The host daemon also starts the local server (`omnigent:omnigent/host/local_server.py:10`).
- bwrap or seatbelt is the default for YAML and SDK agents, not for the
  native wrappers. The README says isolation is mandatory on Linux
  (`omnigent:README.md:166-171`). The code sets `sandbox: {type: none}` for
  claude-native, because "Claude Code already operates on the user's
  workspace with full filesystem access"
  (`omnigent:omnigent/harnesses/claude_native/main.py:3596-3607`). Codex,
  pi, cursor and the other natives do the same
  (`omnigent:omnigent/harnesses/codex_native/main.py:631`).

The chain is CLI → host daemon (+ local server) → server sends
`host.launch_runner` → runner → harness subprocess
(`omnigent:omnigent/host/daemon_launch.py:1-8`). Runners fork from a zygote to
share a ~120 MB import (`omnigent:omnigent/host/runner_zygote.py:1-24`). Where
the agent runs:

- **Laptop:** for `omnigent claude`, for `omnigent run agent.yaml`, and for a
  laptop registered to a remote server.
- **Provider sandbox:** for managed hosts
  (`omnigent:omnigent/onboarding/sandboxes/base.py:1037-1067`).

Aether: a Linux server with a container per run, Docker's default runtime,
and `sysbox-runc` when registered (`aether:internal/runtime/docker.go:45-47,291,323-325`).

| Dimension | Omnigent | Aether |
| --- | --- | --- |
| Isolation of native coding CLIs | None by default on the laptop: full user filesystem and network. An admin can force one with the `enforce_sandbox` policy (`omnigent:omnigent/policies/builtins/safety.py:624-640`). | Always a container. The agent is root inside it, never `--privileged`, and has no host socket. Sysbox maps root to an unprivileged user (`aether:docs/security.md:49-58`). Shared kernel. |
| Isolation of SDK/YAML agents | bwrap: `$HOME` never mounted, cwd read-only unless allowed, fresh /proc and /dev, seccomp denylist (`omnigent:omnigent/inner/bwrap_sandbox.py:12-58`). Network is on by default (`omnigent:omnigent/inner/datamodel.py:630`). | Same container as above. |
| Egress control | L7 MITM proxy with default-deny rules (`omnigent:omnigent/inner/egress/rules.py:1-18`), and credential swap-on-access, marked IMPLEMENTED (`omnigent:designs/SANDBOX_CREDENTIAL_PROXY.md:3`). Opt-in, and needs bwrap or seatbelt. | None. "Nothing in Aether limits their outbound traffic", and runs can reach each other's ports (`aether:docs/security.md:617-619`). |
| Credentials in the agent's reach | Local: everything the user has. Managed: "the user's own credentials never enter the sandbox" (`omnigent:omnigent/server/managed_hosts.py:17-23`), but the broker vends the owner's full-scope GitHub token into it (`omnigent:omnigent/server/routes/host_credentials.py:1-28`). | The launcher's whole home, read-write: agent logins, gh token, signing key (`aether:docs/security.md:25-28,67`). |
| Ops burden | Python ≥3.12 with 40 core dependencies and 291 locked packages, Node 22 with npm and pnpm, tmux, bwrap (`omnigent:pyproject.toml:11,24-140`, `omnigent:README.md:148-171`). Server on 9 PaaS targets plus Kubernetes; Postgres for production (`omnigent:deploy/README.md:117-168`). Sandbox provider accounts. | One Linux box, Docker, optional Sysbox, root systemd unit, SQLite. Static Go binaries with signed self-update (`aether:docs/security.md:557-605`). |
| Scaling to many agents | Agent compute spreads across laptops and sandbox providers. The server is single-replica, because the runner registry is in memory (`omnigent:deploy/kubernetes/README.md:392-395`). Multi-replica is an experiment (`omnigent:deploy/kubernetes/multi_replica/README.md:22-34`). Load test: "a few dozen" hosts (`omnigent:dev/loadtest/README.md:13-27`). | Vertical only. Per-run ceilings default to 8 CPUs, 8 GiB and 4096 pids, with admission on host memory and disk (`aether:docs/environments.md:196-198`, `aether:docs/failure-handling.md:20-21`). One host is the ceiling. |
| Laptop closed | Local host: nothing runs (inference from `omnigent:omnigent/host/local_server.py:3-13`). Managed host: runs, but repos are re-cloned on each new sandbox generation (`omnigent:omnigent/server/managed_hosts.py:3888-3890`). Automations on `connected_host` fail with `host_offline` (`omnigent:docs/AUTOMATIONS.md:88`). | Runs continue; that is the design (`aether:README.md:26-28`). |
| Team server | Auth modes header, accounts and OIDC; admin and member roles; per-session grants. Managed sandboxes need header, OIDC or single-user auth: `accounts` returns 403 (`omnigent:deploy/README.md:446-470`). Co-drive executes "on **your** machine" with the owner's credentials (`omnigent:README.md:573-586`). | Viewer, collaborator and admin; edge with device approval; every collaborator may steer by default (`aether:docs/teams.md:151-176`, `aether:docs/edge.md`). |

## 4. State and durability

| | Omnigent | Aether |
| --- | --- | --- |
| Database | SQLAlchemy 2 and Alembic, 114 migrations. SQLite locally (`omnigent:omnigent/host/local_server.py:762`). Postgres in production and required for more than one replica (`omnigent:deploy/README.md:149-168`). Also CockroachDB, Lakebase and D1. No database-enforced foreign keys (`omnigent:omnigent/server/DBSPEC.md`, "Foreign key strategy"). | One SQLite file through pure-Go `modernc.org/sqlite`, WAL, IMMEDIATE transactions (`aether:internal/store/sqlite.go:41-58`). 80 append-only SQL migrations (`aether:internal/store/migrate.go:15-18,1955`). |
| Durable history | `conversation_items`: insert/delete-only, ordered by `position` (`omnigent:omnigent/db/db_models.py:913-954`). | `events`: append-only, `seq` autoincrement (`aether:internal/events/sqlitelog.go:18-34`). |
| Live state | In runner memory: `_active_turns` and `_session_message_buffers`, "never written to the DB" (`omnigent:omnigent/server/DBSPEC.md:17-22`, `omnigent:omnigent/runner/app.py:1413,1429`). Pending approvals are in server memory (`omnigent:docs/network-resilience.md:81-87`). | Run rows, per-run sidecar files (`aether:internal/scheduler/state.go:524-525`), and the mailbox in SQLite with ack tokens (`aether:docs/coordination.md:227-252`). |
| Stream to clients | SSE, live tail only: "no sequence numbers… no replay". Clients refetch a snapshot and drop duplicates (`omnigent:omnigent/server/API.md:1346-1348,1429-1448`). Events published before a subscriber connects are lost (`omnigent:omnigent/runtime/session_stream.py:1-11`). | WebSocket with `{"replay":true,"after_seq":N}`, gapless and duplicate-free (`aether:internal/events/bus.go:76-83`, `aether:docs/local-gateway.md:2393-2418`). |
| Server restart | Tunnels drop and clients reconnect to the snapshot. In-flight preview text is lost (`omnigent:omnigent/runtime/inflight_text.py:57-65`). Stale approval cards return 202 and do nothing (`omnigent:docs/network-resilience.md:81-87`). | Reattaches to live containers. An Enhanced session is restored in a fresh process. Kills accepted before the crash are reissued (`aether:docs/failure-handling.md:17`). |
| Runner or agent crash | The tmux agent is killed by the orphan sweep (`omnigent:omnigent/inner/terminal.py:901-915`). A new runner replays history (SDK) or runs `claude --resume <id>` (native) (`omnigent:omnigent/runner/app.py:3053-3137`, `omnigent:omnigent/runner/native/orchestration.py:8853-8887`). | A Standard agent that exits drops to a login shell, and the run stays live (`aether:docs/failure-handling.md:23`). After a host reboot the run becomes `interrupted` and its work is committed as `wip:` and published (`:18`). |
| Host disconnect | 90 s grace; a turn waits up to 24 h for an offline host (`omnigent:omnigent/server/routes/_sessions/orchestration.py:8217-8237`). Own lab: R3, a message lost after `202`; R8, out-of-order replay in 4 of 30 Codex runs (`omnigent:docs/network-resilience.md:98-104,150-156`). | Not applicable: the agent runs on the server. A dropped client loses only input that never arrived, and the transcript replays on attach (`aether:docs/failure-handling.md:28`). |
| Multi-node | In-memory registries; the hosts table is shared; `WRONG_REPLICA` re-addressing (`omnigent:omnigent/server/host_registry.py:1-8`, `omnigent:omnigent/errors.py:185-193`). | Single node. The bus interface is a seam: "The in-process implementation is the only one today" (`aether:internal/events/bus.go:115-117`). |

Aether's known durability gaps:
- Commits are not fsynced (`aether:docs/failure-handling.md:449-451`).
- A Background Enhanced run is asked to continue after a crash, so an action
  can run twice (`aether:docs/failure-handling.md:41-44`).
- A Standard agent survives a restart only if its own process survived
  (`aether:docs/failure-handling.md:452-453`).

## 5. Extensibility

| | Omnigent | Aether |
| --- | --- | --- |
| Agent definition | YAML or a directory bundle with sub-agents, each on its own harness, plus tools of type function, MCP, agent, skill and handoff (`omnigent:omnigent/inner/loader.py:390-565`, `omnigent:docs/AGENT_YAML_SPEC.md:798-825`). | None. Templates save a launch, not an agent. |
| Harness plugins | `omnigent.community.harness` entry point (`omnigent:omnigent/harness_plugins.py:50-51,116-138`). Community *native terminal* harnesses are rejected (`:1025-1029`); "Phase 2 (community + web) pending" (`omnigent:designs/harness-modular-registry-proposal.md:3`). | A Go map with one entry per harness; `aether agent add` for custom CLIs (`aether:docs/harness-integration.md:25-33`). One event adapter, for `claude` (`aether:internal/adapter/adapter.go:30-32`). |
| Capability model | Typed: integration mode, elicitation, resume, instruction delivery, steering, live queue and more (`omnigent:omnigent/harness_capabilities.py:24-162`). Checked by `tests/harness_bench` with `DRIFT` verdicts (`omnigent:docs/harness-bench-design.md:1-36`). | Prose limits in `aether:docs/harnesses.md:538-555`. |
| Tool policy | Hooks on PreToolUse, PostToolUse and UserPromptSubmit post to `/policies/evaluate` (`omnigent:omnigent/harnesses/claude_native/bridge.py:2544-2600`). The MCP relay enforces policy on Omnigent tools. | None. |
| Agent-facing tools | 34 `sys_*` tools: sessions, terminals, timers, schedules, policies, models (`omnigent:omnigent/tools/builtins/`). | 8 mcpbridge tools, plus the `aether-internal` CLI (`aether:docs/mcp-bridge.md:94-101`, `aether:internal/coordcli/cli.go:110-139`). |
| Outside tools | MCP registry gateway prototype: one worker, Streamable HTTP only, no OAuth discovery, no retries (`omnigent:designs/MCP_REGISTRY_GATEWAY.md:429-441`). | Connectors: 26 tools exposing Aether to ChatGPT and Claude (`aether:docs/connectors.md:107-122`). |
| SDKs | `python-client` (9.9k lines), `ui` (6.6k), `web-extension` (576 lines, private) (`omnigent:sdks/README.md:20-36`). | The gateway HTTP and WebSocket protocol is documented (`aether:docs/local-gateway.md`); no client SDK. |
| Other plugin points | Sandbox providers (11 built in; `omnigent:docs/extending/sandbox_providers.md:3-7`), git providers, UI extensions in a sandboxed iframe (`omnigent:docs/extending/browser_extensions.md:24-31`), policy modules. | None, by design. |

Cross-harness delivery is the source of flakiness on both sides:

- **Omnigent.** It delivers web input into native TUIs with tmux `send-keys`
  and then verifies the submit against the transcript
  (`omnigent:omnigent/harnesses/claude_native/submit_verification.py`). It
  has a diagnostics guide for `native_prompt_not_recorded`
  (`omnigent:docs/NATIVE_INPUT_DELIVERY_DIAGNOSTICS.md:1-10`). Steering is
  mid-turn for some harnesses, next-turn for others and unverified for the
  rest (`omnigent:docs/QUEUE_STEER_DESIGN.md:79-87`).
- **Aether.** It never types coordination mail into terminals
  (`aether:docs/coordination.md:254`). Codex, Copilot, Gemini and Cursor get
  mail only at the next hook boundary, and Antigravity only when it reads the
  inbox (`aether:docs/harnesses.md:544-545`). This hook-based delivery landed
  between 2026-09-26 (#246) and 2026-10-16 (#414, #416, #420).
  **(inference)** Its youth explains much of the reported flakiness.

## 6. Stated intent and roadmap (Omnigent)

**CUJ-MAP and CUJ-ANALYSIS** (#1613, 2026-06-30).
- An agent-generated reliability inventory over Claude and Codex, SDK and
  native, and Polly (`omnigent:designs/CUJ-MAP.md:11`,
  `omnigent:designs/CUJ-ANALYSIS.md:9-12`).
- Admitted gaps include:
  - The idle reaper kills active turns (`omnigent:designs/CUJ-ANALYSIS.md:482`).
  - Native sub-agent completions never reach the orchestrator (`:501`).
  - Managed sandboxes are broken under OIDC/accounts auth (`:510`).
  - Hard runner affinity with no failover (`:506`).
- Pinned to v0.3.0 (`:478`) and never refreshed, so treat these as history.
  Still true at HEAD: `sys_cancel_task` returns `task_not_found`
  (`omnigent:omnigent/tools/builtins/async_inbox.py:103-119`), and the
  accounts-mode 403 is documented
  (`omnigent:deploy/README.md:465-470`).

**PROJECTS_PRD.** Projects are owner-private session groups, with sharing
kept per session (`omnigent:designs/PROJECTS_PRD.md:9-23`). Memory and context
wait for "customer evidence" (`:589-607`).

**Prioritization.**
- An LLM grades severity and arithmetic turns it into a score, because 60% of
  open bugs were P1 (`omnigent:designs/prioritization/issue-prioritization-v2.md:5-9,81-84`).
- CONTRIBUTING calls it "a triage aid, not a delivery promise or roadmap
  commitment" (`omnigent:CONTRIBUTING.md:12`).
- There is no roadmap file.

**Verification culture.**
- `feature-map/`: "A fix is verified only when every entry point listed for
  the feature has proof" (`omnigent:feature-map/README.md:62-64`). It covers
  13 features and lists many unmapped ones (`:167-229`).
- The harness bench's live probes appear unwired in CI: no workflow
  references `harness_bench` (inference).

**Stated intent, inferred from the above.** Breadth of harnesses, sandboxes
and deploy targets, plus governance, delivered from any device. Reliability
is pursued by measurement documents rather than structural durability.

## 7. Engineering health

| Metric | Omnigent | Aether |
| --- | --- | --- |
| Language and runtime | Python 3.12, uv, Node 22, pnpm; Rust in `omnidev` | Go (static binaries); bun for the dashboard |
| Production code | 977 `.py` files, 553,559 lines | 781 `.go` files, 191,104 lines |
| Test code | 2,683 `.py` files, 1,159,420 lines | 673 `_test.go` files, 206,824 lines; web tests 65,008 lines |
| Web TS/TSX | 402,602 lines | 140,356 lines |
| Files over 1,000 lines | 128 (largest: `omnigent:omnigent/cli.py`, 14,550; `omnigent:omnigent/server/routes/_sessions/orchestration.py`, 13,623) | 16 (largest: `aether:internal/store/migrate.go`, 2,144) |
| Commits | 5,381 since 2026-06-13; 1,248 in 30 days | 507 since 2026-08-18; 284 in 30 days |
| Authors | 307 overall, 61 in 30 days. 76.5% of commits from the 29-login `.github/MAINTAINER` list; community share fell to 6.8% in 30 days (inference from name mapping) | 8 overall, 5 in 30 days. The top two have 270 and 168 commits |
| 30-day mix | fix 49%, feat 12% | fix 38%, feat 45% |
| Releases | 125 tags; 24 stable through v0.19.1 (2026-10-21); about one minor a week plus nightlies; alpha (`omnigent:pyproject.toml:17`) | ~40 prereleases from 2026-09-19 to 2026-10-23 (`gh release list`); beta |
| GitHub | 11,024 stars, 1,801 forks, 897 open issues, 1,505 open PRs, 5,373 merged | 6 stars, 0 forks, 9 open issues, 491 merged PRs |
| CI | 92 workflows; 64 required checks (17 pytest, 14 E2E, 10 E2E-UI shards) on a mock LLM; real LLMs nightly; reruns built in (`flaky(reruns=2)`, `omnigent:pyproject.toml:515-516`) | 5 workflows; `ci.yml` runs lint, unit, real-agent smoke on amd64 and arm64, 5 integration shards and 4 Playwright shards (`aether:.github/workflows/ci.yml`) |
| Review gate | Maintainer Approval passes automatically for maintainers and the `omni-resolve-agent[bot]` (`omnigent:.github/scripts/maintainer_approval.py:63-66`) | Merge queue (`aether:CONTRIBUTING.md`) |

**Reading.**
- Omnigent invests heavily in tests and measurement, and moves very fast.
- Its docs drift from the code. The README's sandbox claim contradicts
  `omnigent:omnigent/harnesses/claude_native/main.py:3603-3606`. The Windows
  Job Object "enforces resource limits" claim (`omnigent:README.md:202-204`)
  contradicts `omnigent:omnigent/inner/windows_jobobject_sandbox.py:253-268`.
- Very large modules are a maintenance risk.
- Aether is a third the size and keeps its files small.
- Its bus factor is two people.

## 8. Where Aether is structurally stronger or weaker

**Stronger**
1. Execution placement. Agents run on the server by default, so overnight
   swarms need no laptop, provider account or auth-mode exception.
2. Durability. The gapless event log replays, live containers are reattached
   after a restart, and the mailbox is durable with acks. Omnigent has none
   of the three (section 4).
3. A default boundary for native CLIs. Aether always uses a container;
   Omnigent's native wrappers have no sandbox.
4. Team-shared workspaces bound to a repo, plus team memory. Omnigent's
   projects are owner-private, and its memory is an optional external service.
5. A swarm protocol owned by the server, with tasks, attempts, candidate
   verification and delivery (`aether:docs/integration.md:1-30`). Omnigent's
   Polly is a prompt.
6. A smaller, simpler codebase that ships as static binaries.

**Weaker**
1. One host is the compute ceiling, and there is no remote sandbox provider
   interface.
2. Every run holds the launcher's credentials read-write
   (`aether:docs/security.md:25-28`).
3. No egress control, no tool policy, and permission prompts are off.
4. 9 harnesses against 27; one structured event adapter; harness
   capabilities exist only in prose, with no drift test.
5. Linux server only. A solo user must run a box that stays on.
6. Team size, community and distribution. GPL-3.0 limits enterprise
   embedding, while Omnigent's Apache-2.0 invites it (inference).

## 9. What Aether should adopt

Licence: Apache-2.0 code can enter GPL-3.0 Aether if Omnigent's NOTICE file
is kept (`omnigent:NOTICE:1-5`). The cost below is about the language, not the
licence.

| Omnigent subsystem | How to adopt | Cost (Go vs Python) | Call |
| --- | --- | --- | --- |
| Harness capability matrix (`omnigent:omnigent/harness_capabilities.py`, 184 lines) and bench (`tests/harness_bench`) | Design port: a typed `Capabilities` struct per entry in `aether:internal/harness/harness.go`. The dashboard and docs read from it, and the integration suite fails on drift using the real-agent smoke job CI already runs. | Small: about 0.5–1k lines of Go plus probes. The Python cannot be reused. | **Replace Aether's prose-only agent limits (`aether:docs/harnesses.md:538-555`) with Omnigent's capability-matrix-plus-bench approach.** |
| Egress rule DSL and MITM proxy (`omnigent:omnigent/inner/egress/`, ~2.7k lines, mostly stdlib) and credential swap-on-access (`omnigent:omnigent/inner/credential_proxy.py`) | Design port as a per-run proxy owned by `aether-server`, in Go (`net/http` plus `crypto/tls`). Keep the gh token and signing key out of the container. | Medium: about 2–3k lines of Go, plus CA injection into images. Agent OAuth refresh flows may resist swapping (inference). | Adopt. It closes Aether's top two security gaps. |
| Hook-based policy (ALLOW/DENY/ASK at three levels; `omnigent:docs/POLICIES.md`) | Design port onto Aether's existing hooks (`aether:internal/coordhooks`) and approvals (`aether:internal/approvals`). Start with deny/ask on shell and cost caps. | Medium. Harness reach is limited by the same hook gaps listed in section 5. | Adopt a narrow version. |
| Managed hosts: durable host id, ephemeral sandbox generation (`omnigent:omnigent/server/managed_hosts.py:11-15`) and the provider interface (`omnigent:omnigent/onboarding/sandboxes/base.py:477-1004`) | Design port behind `aether:internal/runtime`, when Aether needs more than one host. | Large; it changes the single-node assumption in store, bus and scheduler. | Defer. Take the model, not the code. |
| Network-resilience lab with scenario ids and measured outcomes (`omnigent:docs/network-resilience.md`) | Process port: measured scenario rows behind each row of `aether:docs/failure-handling.md:13-40`. | Small. | Adopt. |
| Omnigent itself as an agent inside a run | A sidecar: install `omnigent` in a saved environment and launch it as a custom agent to get YAML agents such as Polly. | Zero Go. Adds Python, Node and 291 packages to the image (inference: not tested). | Optional experiment, not core. |

Do not copy:
- Live state held only in memory, or a live-tail stream with no cursor.
- tmux `send-keys` delivery for automated messages.
- Native agents with no sandbox.
- Modules over 10,000 lines.
- Automatic approval for maintainers' own PRs.
- Postgres and multi-replica work before Aether is multi-node.

## 10. Limits of this review

- Read-only source review. Neither project's tests were run, and no live
  failure was reproduced.
- Omnigent behaviour on managed sandboxes was read from code and docs, not
  exercised.
- Author affiliation is inferred from emails and the maintainer list.
- Line counts include generated and vendored files that live inside the
  source directories.
