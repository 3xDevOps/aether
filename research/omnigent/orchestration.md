# Omnigent vs Aether: multi-agent orchestration and cross-harness messaging

Omnigent at `8b46a8a37961616e3e78d7f96c37ed8ccf08f07e` (2026-10-25, #11515). Aether at `77b4f3bb` (#524).
Run `run-1n3yc096fn`, task `task-6psj38xpqf`. Citations: `omnigent:<path>:<line>`, `aether:<path>:<line>`, `omnigent#N`, `aether#N`.

## Verdict

1. **Omnigent's cross-harness layer is not more reliable than Aether's. It is simpler and the same on every harness, which is why it feels consistent.** It has one dispatch tool (`sys_session_send`) and one way back: the child's last answer goes into the parent's inbox, and the runtime posts a `[System: sub-agent … finished]` message into the parent the same way a user message arrives. There are no ack tokens, no report call, no size cap and no peer graph.
2. **Omnigent has open P1 bugs in the same class Aether fights:** parents never woken (omnigent#10196, omnigent#10979), a steer reported as delivered when it was only buffered (omnigent#10471), and lost first messages on native harnesses (omnigent#10955, omnigent#8782).
3. **Omnigent is less durable.** The parent's inbox is an in-memory `asyncio.Queue` inside the runner process (omnigent:omnigent/runner/subagent_work.py:1218). Turn buffers, steer buffers and approvals live only in memory too (omnigent:omnigent/server/DBSPEC.md:17-23). A message sent while the host is unreachable is lost (omnigent:docs/network-resilience.md:98-104). Aether's SQLite mailbox, with idempotency keys and explicit acks, is the better store.
4. **Aether's flakiness comes from waking agents, not from storing mail.** Five wake paths differ by harness and mode, and each keeps its own dedup state. Every swarm wake is dropped, not queued, when the global authorization lock is busy (aether:internal/server/svc_coord_wake.go:67-69). Standard runs of Codex, Copilot, Gemini, Cursor and Antigravity have no idle wake at all (aether:docs/harnesses.md:18-28).
5. **Where the server drives the session, Omnigent's model removes the cause. Where it types into terminals, the cause only moves.** Aether's Enhanced mode is already Omnigent's model: the server prompts an idle agent session over ACP (aether:internal/coord/acp_wake.go:62-112). Omnigent's native mode types into tmux panes and mirrors transcript files, and that is where its lost and duplicated messages come from (omnigent#6650, P0).
6. **Aether's candidate integration is unique, and it is the defensible asset.** Omnigent has no step that merges several agents' work. Each worker opens its own PR, and "cross-PR conflicts surface when the human merges" (omnigent:examples/polly/skills/fanout/SKILL.md:60). Aether assembles one candidate, freezes it, verifies it in a container and delivers it after approval.
7. **Omnigent is easier to use as an orchestrator.** The same tool set is exposed to every harness: in-process for SDK harnesses, through an MCP relay for native ones. Aether has a full CLI, plus an eight-tool MCP bridge that has no mission tools and acks differently from the CLI.
8. **Investor view.** Omnigent has the distribution: 11,023 stars, 61 authors in 30 days, 14+ harnesses, cloud sandboxes, a phone client. Its orchestration is an LLM prompt (polly) on top of a session tree. Aether has 6 stars, and two people wrote about 94% of its commits. Its messaging is not a moat. A server-owned path from swarm to one verified candidate, built to run overnight, is a moat, provided the wake layer stops dropping work.
9. **Replace** Aether's hook-delivered mail notice for swarm runs with delivery driven by the session driver:
   - ACP `session/prompt` wherever Enhanced exists;
   - for Codex, Omnigent's `codex app-server --listen` plus a `codex --remote` TUI, steered with `turn/start` and `turn/steer`.
10. **Replace** the lock-guarded wake that gives up when the lock is busy with a durable "wake owed" state that is retried on every edge.
11. **Copy from Omnigent:**
    - turn-end result capture when a worker never reports;
    - one dispatch-and-await verb for the integrator;
    - telling the integrator when a child is blocked on an approval;
    - per-turn spawn caps and policies inherited from root to children;
    - cross-vendor review as a task type;
    - a harness-readiness preflight.
12. **Keep** in Aether: the mailbox store, idempotency, socket identity, candidate integration, and the rule that agent mail is never typed into a terminal.
13. **Converge on one input queue per run, one driver that delivers it, and one tool surface.** Details are in [section 9](#9-the-design-aether-should-converge-on).

## 1. Method

- Cloned with `timeout 300 git clone --filter=blob:none https://github.com/omnigent-ai/omnigent /tmp/omnigent`.
- Read in Omnigent:
  - `omnigent/runner/{subagent_work,subagent_routing,acp_subagent_sessions,turn_routing,subagent_recovery,app,tool_dispatch}.py`
  - `omnigent/tools/builtins/{spawn,async_inbox}.py`
  - `omnigent/runtime/prompt.py`
  - `omnigent/server/{_runner_ws_tunnel,_runner_transport}.py`
  - `omnigent/runner/transports/ws_tunnel/*`
  - `omnigent/host/git_worktree.py`
  - `omnigent/runner/{session_prs,pr_observer}.py`
  - `omnigent/inner/{claude_sdk,codex_native}_executor.py`
  - the docs `QUEUE_STEER_DESIGN`, `AGENT_YAML_SPEC`, `AUTOMATIONS`, `POLICIES`, `network-resilience`, `NATIVE_INPUT_DELIVERY_DIAGNOSTICS` and `DBSPEC`
  - `examples/{polly,debby,scribe,sentinel,deep-research,remy,aws_analyst}`
- Read in Aether:
  - `docs/{coordination,integration,mcp-bridge,failure-handling,harnesses,enhanced-runs}.md`
  - `internal/{coord,coordcli,coordhooks,coordtransport,mission,integration,overlap,collab,mcpbridge}`
  - `internal/server/svc_coord_wake.go`
  - `internal/scheduler/launch.go`
- GitHub queries were read-only and capped at `--limit 100`, about 30 calls. Counts came from `gh issue list/pr list --search`, GraphQL `search{issueCount}`, and `git log --oneline -n 2000 | grep -iE …`.
- Four research subagents traced the code in parallel. Every claim in the verdict was re-checked against the source with `sed -n`.
- No tests were run.
- "(inference)" marks anything not read directly.

## 2. Two different intents

| | Omnigent | Aether |
| --- | --- | --- |
| Unit of work | A **session**: a conversation with one agent. Sub-agents are child sessions in a tree under the session the human chats with (omnigent:omnigent/tools/builtins/spawn.py:128-160). | A **mission** (swarm): an objective. An **integrator** run proposes tasks, starts **worker** runs and assembles their submissions (aether:docs/coordination.md:1-18). |
| Who orchestrates | An LLM "brain" told by its prompt how to plan, fan out, review and record progress in `.polly/registry.json` (omnigent:examples/polly/config.yaml:40-300). No mission object exists in the code; there are no hits for swarm or mission in `omnigent/`. | The server. It holds mission state: tasks, revisions, attempts, integrator generation, phase. A reconcile loop runs every 5 s (aether:internal/mission/service.go:180-194). The integrator is also an LLM, but server state bounds what it may do. |
| Communication shape | A tree. A parent writes to its direct children only (omnigent:omnigent/runner/tool_dispatch.py:3200). A child's result returns to its parent. Siblings can read but not write to each other. | A graph. Runs message any authorized peer: swarm peers, overlap ("radar") peers, workspace peers. There is an 8-peer cap per run (aether:internal/coord/mailbox.go:46-49). |
| Deliverable | One PR per worker; a human merges (omnigent:examples/polly/skills/fanout/SKILL.md:41-43). | One frozen, verified candidate, delivered as a proposal ref or a ref update (aether:docs/integration.md:116-220). |
| Mixing harnesses | Per sub-agent spec: `executor.harness` (omnigent:docs/AGENT_YAML_SPEC.md:796-828). | Per worker: an `execution_choices` tuple of account, harness, mode and model (from `aether-internal status` in this run). |

Omnigent is built for a human chatting with one lead agent that delegates. Aether is built for a server-run batch of agents producing one mergeable result.

## 3. Omnigent: how agents cooperate

### 3.1 Dispatch

- **The tool.** `sys_session_send(agent, title, args)` spawns or continues a named child (omnigent:omnigent/tools/builtins/spawn.py:128-160). With `spawn: true`, the agent also gets `sys_session_create` for agents it defines itself (omnigent:examples/polly/config.yaml:14-17).
- **Native harnesses** see the same tools through an `omnigent` stdio MCP relay: `mcp__omnigent__sys_session_send`. Relayed calls time out after 300 s (subagent report: `omnigent/harnesses/claude_native/bridge.py:232,245`).
- **The call returns immediately** with `"status": "launching"` (omnigent:omnigent/runner/tool_dispatch.py:3138-3146). The tool description still says "Returns the child's output when its turn completes" (omnigent:omnigent/tools/builtins/spawn.py:138), so the description and the behaviour disagree.
- **The child's harness** comes from its spec. A per-call `args.harness` override is allowed only if the spec allowlists it (omnigent:omnigent/tools/builtins/spawn.py:185).
- **Placement.** Children inherit the parent's runner, the per-machine process that runs agent turns. Parent and children therefore share one machine (subagent report: `omnigent/server/routes/_sessions/orchestration.py:11549`).

### 3.2 Result return and wake: the reason it feels consistent

1. **At turn end, the child's last assistant text becomes its result.** Nothing has to be called (omnigent:omnigent/runner/app.py:3957-3962): `output=_extract_last_assistant_text(conv_id)`.
2. **The result goes into the parent's in-memory inbox.** It is put on an `asyncio.Queue` with no size cap. The 150-character limit applies only to the preview in the wake notice (omnigent:omnigent/runner/subagent_work.py:778-797,1102,1218).
3. **The runtime posts a wake notice into the parent as an ordinary message,** starting a turn if the parent is idle (omnigent:omnigent/runtime/prompt.py:29-42; omnigent:omnigent/runner/app.py:4449-4470). The parent then calls `sys_read_inbox`.
4. **One wake is in flight per parent.** The wake POST is retried 3 times. After a failed wake, the result stays in the inbox and is retried "after the next tunnel reconnect or parent turn" (omnigent:omnigent/runner/app.py:4440-4447).

Steps 1-3 run the same way for every harness, because they act on the parent's input queue, not on the parent harness. That uniformity is the design win.

### 3.3 Message to a running agent (steer or queue)

This is per harness (omnigent:docs/QUEUE_STEER_DESIGN.md:78-96), corrected against the code:

| Harness | Mid-turn delivery | Mechanism |
| --- | --- | --- |
| claude-native | yes, best-effort timing | `tmux send-keys` into the live pane |
| codex-native | yes | `turn/steer` RPC to `codex app-server`, which a `--remote` TUI attaches to (omnigent:omnigent/inner/codex_native_executor.py:535-560; omnigent:omnigent/harnesses/codex_native/launch_args.py:122-127) |
| claude-sdk (the brain of polly, debby, scribe and sentinel) | **no, next turn** | "claiming this capability caused the one-turn-behind desync described in #3472" (omnigent:omnigent/inner/claude_sdk_executor.py:2270-2273). The design doc's "deterministic" row is stale. |
| cursor / copilot SDK, opencode-native | next turn | buffer and drain; OpenCode has no steer endpoint |
| cursor, hermes, pi, qwen, goose, kimi, kiro, antigravity (native) | "not yet verified live" | paste, file or RPC (omnigent:docs/QUEUE_STEER_DESIGN.md:89-96) |

Other conditions:

- **Waiting on an approval:** no injection while the agent waits on a human (`not _awaiting_approval`, omnigent:docs/QUEUE_STEER_DESIGN.md:151-155).
- **Native delivery is matched by text:** "The queue remains in memory. A server restart can lose its correlation state. Native transcript matching remains text-based; identical prompts and FIFO fallback retain their existing uncertainty" (omnigent:docs/NATIVE_INPUT_DELIVERY_DIAGNOSTICS.md:144-148).
- **The steer receipt can be wrong:** `sys_session_send` reports "Steered … injected" even when the message was only buffered (omnigent#10471, P2, open).

### 3.4 Child failure, hang, approval, cancel

These come from the subagent code read, with line numbers spot-checked.

- **Launch timeout.** A child with no start acknowledgement is failed after 180 s, but "The child may still be running" (`subagent_work.py:41,857`).
- **No limit after start.** Once started, a child has no run-time limit; the only remedy is `sys_cancel_task` (inference: no other timer was found).
- **Cancel is uneven.** `claude_code` is hard-stopped. Codex and antigravity cancellation is "best-effort" (omnigent:examples/polly/config.yaml:288-296).
- **Child waiting on an approval.** The card is mirrored into the ancestors' chats. After 120 s the parent agent is told to surface the situation to the human (`omnigent/runtime/subagent_block_notifier.py:55`). The parent agent cannot answer the approval itself.
- **Runner restart.** The inbox dies with the process. Recovery rebuilds missing results by comparing each child's `dispatch_id` label with its `delivered_id` receipt and reading the child's transcript. This is at-least-once delivery: "a lost receipt costs one duplicate delivery after a restart" (`tool_dispatch.py:8246`; `subagent_recovery.py:146`).
- **Open bug, cross-runner children.** When a child runs apart from its parent, or stops without a turn end, the parent is never woken. The child's runner refused 1,837 forwards in one observed case (omnigent#10196, P1, open, fix PR omnigent#10318 open). The bug body says "an orchestrator needs an outside watcher."
- **Open bug, silent Codex child.** A codex-native child that accepts `turn/start` and then emits nothing stays `running` forever (omnigent#10979, P1, open).
- **What the orchestrator prompt has to defend against.** Polly's prompt has to warn: "a turn whose entire content is an intent sentence with no tool call is a dropped turn that stalls the whole run, since nothing fans out and no inbox wake will ever arrive to revive you" (omnigent:examples/polly/config.yaml:241-244).

### 3.5 No peer messaging

- There are no swarm, mailbox, mission or broadcast concepts in `omnigent/`, `docs/` or `examples/`.
- Siblings see each other through `sys_session_list` but cannot write to each other.
- In debby, debate partners "have no shared memory of each other"; the parent relays between them (subagent report: `examples/debby/skills/debate/SKILL.md:39`).

### 3.6 Parallel work on one repo, and git

- **Worktree per top-level session** is optional and enforced by the server. A branch that already exists is refused, because "two sessions sharing one working tree would clobber each other" (omnigent:omnigent/host/git_worktree.py:470-477).
- **Worktree per sub-agent** happens only if the orchestrator LLM runs `git worktree add .worktrees/<task_id> -b polly/<task_id>` itself and tells the child to stay inside it (omnigent:examples/polly/skills/fanout/SKILL.md:12-20). Nothing enforces this. Children otherwise share the parent's runner and cwd (inference from the runner affinity at `orchestration.py:11543-11559`).
- **Fan-out size** is capped only by the opt-in `spawn_bounds` policy: 6 dispatches per turn in polly, default 5 (omnigent:examples/polly/config.yaml:372-380). No global cap on concurrent sessions was found (inference: searched `server/`, `host/` and `onboarding/`).
- **PR tracking.**
  - `pr_observer` parses completed shell and MCP tool calls for PR URLs.
  - `session_prs` stores the links as host-local JSON.
  - PR state is polled every 5 s while the panel is open; there are no webhooks.
  - The merge button runs `gh pr merge --match-head-commit` (subagent report: `omnigent/runner/pr_observer.py:1-6`; `session_prs.py:72-78`; `github_resource.py:1625-1640`).
- **No integration step.** Cross-PR conflicts are left for the human to resolve at merge (omnigent:examples/polly/skills/fanout/SKILL.md:60-62).

### 3.7 Policies on orchestration

- **Inheritance.** Children inherit the root's policies and guardrails first, so a child "cannot weaken the parent's fence". Cost approvals stored on the root cover the whole tree. An optional per-dispatch `cost_budget` is available (subagent report: `omnigent/runtime/policies/builder.py:732-754`; `omnigent/policies/schema.py:100`).
- **Orchestration policies** are opt-in (omnigent:omnigent/policies/builtins/orchestration.py:425-545):
  - `spawn_bounds` caps dispatches per turn;
  - `headless_subagent_purpose_guard` restricts a headless child to the purposes `implement`, `review`, `explore` and `search`.
- **Approvals unattended.** Polly sets `ask_timeout: 86400` and `gate_pushes: false` so an unattended run does not wait on pushes (omnigent:examples/polly/config.yaml:360,370).

### 3.8 Transport and durability

- **Topology.** Server, then host daemon, then one runner per session group. Runners dial out over WebSocket. HTTP calls are tunnelled as frames, each with a fresh `req_id`.
  - There is no sequence number, ack or replay at the tunnel layer.
  - A reconnect aborts in-flight requests (subagent report: `omnigent/runner/transports/ws_tunnel/registry.py:313-341`).
- **The one acknowledged path** is native transcript events.
  - They travel as `event.batch` and are answered with `event.ack`.
  - A lost ack is retryable: "An ACK can disappear after the server committed the item. All durable events admitted here carry a stable source_id" (omnigent:omnigent/runner/transports/ws_tunnel/event_delivery.py:245-250).
  - The server dedups by uuid5 of `source_id`.
- **Not acknowledged:** the runner's session stream (no cursor), turn and steer buffers, and pending approvals. All are in memory (omnigent:omnigent/server/DBSPEC.md:17-23).
- **The project's own resilience matrix** (omnigent:docs/network-resilience.md:49-156):
  - R1/R2: approvals are lost or delayed on restart;
  - R3: a message sent while the host is unreachable is lost;
  - R6: repeated short drops add up to a failure;
  - R8: Codex status is stuck on `running` after a reconnect.
- **Disconnects at scale.** One week of fleet data showed 4,632 `runner_disconnected_mid_turn` events across 3,029 sessions, and 61% of those runners reconnected afterwards (omnigent#8412, fixed by omnigent:984cbf413).

### 3.9 Unattended operation

Automations (omnigent:docs/AUTOMATIONS.md:135-141):

- schedules are RRULEs with at least an hour between fires;
- missed fires are not replayed;
- failed runs are not retried;
- overlapping fires are dropped without a run row;
- each server replica fires every task.

There are no webhook or PR-event triggers. The database-backed `automations_v2` is off by default (subagent report).

## 4. Aether: how agents cooperate

### 4.1 Mailbox: sound

- **Path.** One per-run Unix socket, `/run/aether/coord3.sock`, whose connection is the run's identity. Both the CLI and the MCP bridge call `coordtransport.Call` (aether:docs/coordination.md:20-67; aether:internal/coordtransport/client.go:103-184).
- **Storage.** SQLite, at-least-once delivery, oldest-first batches with a durable `ack_token`. A batch stays frozen until it is acked (aether:docs/coordination.md:225-240).
- **Idempotency.** Every mutation (send, ask, reply, report) requires an idempotency key. A repeat returns the original receipt (aether:docs/coordination.md:242-252).
- **Limits** (aether:internal/protocol/coord.go:37; aether:internal/coord/mailbox.go:33-49):
  - body 4 KiB;
  - 100 unacked messages per inbox;
  - inbox reads: burst 10, then 1/s;
  - transport budget: burst 30, then 1/s;
  - 8 peers per run, durable, so finished peers keep counting (aether:internal/store/migrate.go:855-861, from the subagent report).
- **Reports.** A worker's report is forwarded to the integrator as an ordinary inbox message, through a durable outbox (aether:docs/coordination.md:307-312).

### 4.2 Wake paths: the problem

"There are four ways to notice mail" (aether:docs/coordination.md:254-286). Counting Claude's waiter separately makes five:

| Path | Who gets it | Gap |
| --- | --- | --- |
| `inbox --wait` (agent polls) | anyone | `wait_seconds` is capped at 30; the agent has to loop (hit in this run: `-32602 wait_seconds must be between 0 and 30`) |
| Command hooks at lifecycle boundaries | Standard Claude, Codex, Copilot (managed); Gemini, Cursor (manual only) | Nothing happens once the agent is idle. Codex needs a one-time trust step, repeated after any hook change (aether:docs/harnesses.md:134-138). There is a 3 s hook timeout on a status call whose cost grows with swarm size (aether:internal/coordcli/hooks.go:113). |
| Claude `asyncRewake` waiter | managed Standard Claude | Mail already announced in a mid-turn hook never wakes the agent: `if input.Name == "Stop" { notified = readHookNotices(record).Mail }` (aether:internal/coordcli/hook_claude_wake.go:104-108). The waiter exits after about 31 s of transport errors (aether:internal/coordcli/hook_claude_wake.go:26,92-94). |
| pi, omp, OpenCode extensions | Standard pi, omp, OpenCode | The extension halts for good after a non-retryable error or more than 5 failures (`halted = true`, aether:internal/coordhooks/pi.ts:210-214). The OpenCode plugin is pinned to a version and switched off by `OPENCODE_PURE`. |
| Enhanced ACP `session/prompt` | every Enhanced run | Fires only when the session is idle. It is retried at 1, 2, 4 and 8 s and then dropped until the next mail, turn end, resume or swarm change (aether:internal/coord/acp_wake.go:19-55). Its dedup state is in memory. |

On top of that:

- **Hooks only for task-bearing Standard runs.** Hooks and native wake are loaded only when `coordination.enabled && run.Mode == domain.LaunchTUI && run.Task != ""` (aether:internal/scheduler/launch.go:475-476). Background and taskless runs get neither.
- **No wake path at all** for Standard Codex, Copilot, Gemini, Cursor and Antigravity: no idle wake (aether:docs/harnesses.md:18-28,502-507).
- **Every swarm wake gives up when a lock is busy.** It first tries the global authorization lock without waiting: `if !a.authorizationMu.TryLock() { return errors.New("coord wake: mission authorization is busy") }` (aether:internal/server/svc_coord_wake.go:67-69). Then it tries mission control without waiting (aether:internal/server/svc_coord_wake.go:105-120).
- **That lock is held through git work.** `integration.prepare` keeps the same lock until `resumePrepare` returns, which is after the cherry-pick assembly (aether:internal/mission/integration.go:179-190; aether:internal/integration/candidates.go:97-136). Wakes are therefore most likely to be dropped during dispatch bursts and integration, which is exactly when a swarm is busiest.

### 4.3 Mission lifecycle gaps

- **A dead integrator is not brought back.** It is relaunched only when its run row is missing. A row that exists but is terminal just has the error recorded (aether:internal/mission/service.go:507-543). The issue aether#225, "nothing detects or announces a dead integrator run", is open.
- **Standard integrators miss worker endings.** Their hook notice keys only on `mission|phase|openQuestions|generation` (aether:internal/protocol/coord.go:132-138). They are not told when a worker ends or proposes a task; only Enhanced integrators track `change_seq` (aether:internal/coord/acp_wake.go:137-161).
- **A crashed Standard worker looks alive (inference).** The agent drops to a login shell, so the run is not terminal and its attempt stays `running` (aether:docs/failure-handling.md:23).
- **Stale workers lose status, send and report.** After their task is revised, those calls fail with `-32001` (aether:internal/coord/mailbox.go:96-101).

### 4.4 CLI and MCP divergence

- **Ack semantics differ.**
  - The CLI acks only with an explicit `ack <token>`.
  - The MCP bridge carries the last token forward automatically: "If the caller does not provide an explicit token, carry forward the last response" (aether:internal/mcpbridge/tools.go:125-130).
  - The CLI `ack` is itself a `coord.inbox` call. It stamps the next batch as delivered and then throws that batch away (aether:internal/coordcli/cli.go:732-752).
- **The MCP bridge is a subset.** It has 8 tools and no task, worker, mission or integration tools (aether:docs/mcp-bridge.md:84-95). An MCP-first integrator still has to use the CLI.
- **Error codes are overloaded.** `-32004` means finished peer, unreachable socket, closing run, coordination disabled, or hook rate limit. Yet agents are told "A finished peer returns -32004 … continue without retrying" (subagent report: aether:internal/coord/mailbox.go:1138-1142, coord.go:425-459).

### 4.5 Integration: the strong part

- **Inputs.** `SubmissionRef` is an ordered 4-tuple. It is validated against a durable evidence packet.
- **Assembly.** The candidate is assembled by ordered cherry-pick onto `expected_target_revision`.
- **Lifecycle.** States are `preparing` → `conflicted` | `frozen`. Verification runs as a server-built container command. Delivery is `update_ref` or `proposal`, after an approval step.
- **Retries are safe.** Every step is idempotent and journaled (aether:docs/integration.md:116-273).
- **Conflicts.** The integrator delivers the subset that applies cleanly and has the conflicting task redone (aether:docs/integration.md:205-209).

Omnigent has nothing equivalent.

## 5. Head to head

| Dimension | Omnigent | Aether | Edge |
| --- | --- | --- | --- |
| Message reaches an **idle** agent | Always the same way: posted into the parent as an input. Fails when the parent and child are on different runners (omnigent#10196). | Enhanced: yes. Standard: only Claude, pi, omp and OpenCode; Codex, Copilot, Gemini, Cursor and Antigravity never. | Omnigent (uniform) |
| Message reaches a **busy** agent | Steered mid-turn on claude-native, codex-native and codex/pi SDK. Next turn on claude-sdk, OpenCode and Cursor. Unverified on 8 natives. | A boundary hook adds a pointer at the next tool call (Standard only). Enhanced waits for the turn to end. | Omnigent |
| Agent **waiting on an approval** | Held, not injected. After 120 s the parent is told the child is blocked. | Held: hooks do not override approval waits, and wake admission requires a live interactive run. The integrator is not told. | Omnigent (parent notice) |
| Agent **crashed or finished** | Recovery from labels plus transcript; at-least-once. Still has open P1 holes. | Mail is durable; sends to a finished run return `-32004`. No relaunch of a terminal integrator. | Tie, different holes |
| **Durability** | In-memory parent inbox, buffers and approvals; messages lost while the host is unreachable (R3). | SQLite mailbox, durable tokens and receipts. | **Aether** |
| **Same behaviour on every harness** | One input path; differences show up only in how mid-turn steering lands. | Five mechanisms; behaviour depends on harness × mode × task-bearing × managed-or-manual. | **Omnigent** |
| **Agent ergonomics** | One tool family on every harness. The result is the child's last message; no ack, no report call, no 4 KiB limit. | A CLI and an MCP bridge with different coverage and ack rules. A report call is required. 4 KiB bodies. The inbox wait is capped at 30 s. | **Omnigent** |
| **Human ergonomics** | Sub-agent tree panel (depth 3), canvas of up to 5,000 sessions, `/inbox` for approvals, phone client. | Board, Session view, takeover holds, read-only worker terminals (aether:docs/coordination.md:333-358). | Omnigent (breadth); not compared in depth |
| **Results into git** | One PR per worker; the human merges and resolves conflicts. | One verified, frozen candidate; approval-gated delivery. | **Aether** |
| **Overnight run of 10+ agents** | A wake bug needs "an outside watcher" (omnigent#10196). No retry for automations. Six dispatches per turn, in waves. The brain can stall on a dropped turn. | Server mission state and a 5 s reconcile loop. Wakes are dropped under lock contention. A dead integrator is not detected (aether#225). | Neither is ready; Aether has the better skeleton |
| **Field evidence** | Since 2026-09-25: 1,248 commits (617 fixes), 799 issues opened and 444 closed. Real fleet telemetry. | 507 commits in all (173 fixes), 9 issues. Little outside usage, so reliability is unproven. | Omnigent is battle-tested *and* visibly failing |

## 6. Where Aether's flakiness comes from, and whether Omnigent's approach fixes it

Ranked by likely impact. "Removes" means Omnigent's design does not have the failure. "Moves" means it has an equivalent.

1. **No idle wake for most Standard harnesses** (aether:docs/harnesses.md:18-28).
   - *Omnigent:* removes it for codex-native, because the runner drives `codex app-server`. It moves it into `tmux send-keys` for the claude, cursor and hermes natives.
   - *Fix:* drive input through the session API, never through hooks.
2. **Wake gives up when the lock is busy** (aether:internal/server/svc_coord_wake.go:67-69), and the lock is held across integration git work (aether:internal/integration/candidates.go:97-136).
   - *Omnigent:* removes it. Its wake is a plain POST, retried, with one wake in flight per parent (omnigent:omnigent/runner/app.py:4440-4470).
   - *Fix:* record that a wake is owed, durably, and retry it on every edge.
3. **Two notifications for one mail, and dedup that suppresses wakes.** A hook announces the mail as context; the wake is a separate step and skips anything already announced (aether:internal/coordcli/hook_claude_wake.go:104-108).
   - *Omnigent:* removes it. The notice *is* the input that starts the turn.
4. **The worker must call `report`, or the integrator learns nothing.**
   - *Omnigent:* removes it, because the child's last message is the result (omnigent:omnigent/runner/app.py:3957-3962).
5. **Standard integrators miss worker endings, and a dead integrator is not detected** (aether:internal/protocol/coord.go:132-138; aether:internal/mission/service.go:507-543).
   - *Omnigent:* it moves. Omnigent has the same "parent never woken" bug (omnigent#10196, omnigent#10979).
6. **Parallel CLI and MCP surfaces with different ack rules** (aether:internal/mcpbridge/tools.go:125-130 vs aether:internal/coordcli/cli.go:732-752).
   - *Omnigent:* removes it. There is one tool family and no ack tokens.
7. **4 KiB cap, 30 s wait cap, rate buckets, 8-peer cap.**
   - *Omnigent:* removes these at the cost of having no backpressure (inference: no inbox cap found in `subagent_work.py`).
   - *Fix:* keep the caps for notices, but carry long results by reference.
8. **Receivers stop for good after a transport failure** (aether:internal/coordhooks/pi.ts:210-214; aether:internal/coordcli/hook_claude_wake.go:26,92-94).
   - *Omnigent:* it moves. Omnigent's runners reconnect forever, but its wake waits for the next reconnect.
9. **Overloaded error codes plus "don't retry" guidance.**
   - *Omnigent:* not comparable.
   - *Fix:* split the codes.

Bottom line: moving to Omnigent's architecture would remove causes 1-4, 6 and 7. Aether can remove them itself by finishing its own Enhanced path. Causes 5 and 8 exist in Omnigent too.

## 7. Friction in this run (first-hand)

This run is Enhanced Claude: `AETHER_HARNESS=claude`, `AETHER_ENHANCED=1`.

1. `aether-internal skill` prints `Inbox hooks (claude): missing (missing registrations: SubagentStart)`. That is noise here: Enhanced runs silence every hook (aether:internal/coordcli/hooks.go:74-79). An agent cannot tell from the output whether its mail path works.
2. `aether-internal status` cut my own task off at 512 bytes (`"task_truncated":true`). I needed a second call, `task show`.
3. `inbox --wait 90` failed with `-32602 wait_seconds must be between 0 and 30`. Waiting two minutes took a shell loop of 30 s waits.
4. `status` listed 24 peers, 4 of them swarm siblings. The rest were unrelated workspace runs, many in `needs-attention`.
5. The deliverable has to be split into 2-4 messages under 4 KiB to reach the integrator, because there is no way to send a result by reference. In Omnigent, a child's final message is the result, with no size cap.
6. No mail arrived during the run, so I could not observe a wake. Following the instructions, I polled before each step.

## 8. Component verdicts

| Aether component | Verdict | Why / what |
| --- | --- | --- |
| SQLite mailbox, idempotency keys, receipts (`internal/coord`, `internal/store/coord.go`) | **Keep** | It is more durable than Omnigent's in-memory inbox (omnigent:omnigent/runner/subagent_work.py:1218). |
| Socket-as-identity (`coord3.sock`) | **Keep** | Simple and secure, with no tokens in the container. |
| Candidate integration (`internal/integration`, `internal/gitengine`) | **Keep** | It is unique; Omnigent leaves conflicts to the human merge. |
| Mission state, reconcile loop (`internal/mission`) | **Keep, fix** | Relaunch or announce a terminal integrator (aether#225). Make Standard integrators key on `change_seq`. |
| Hook-delivered mail notice for swarm runs (`internal/coordhooks`, the hook half of `internal/coordcli`) | **Replace** | Use delivery driven by the session driver (section 9). Keep hooks only as optional context for non-swarm runs. |
| Codex Standard mail path | **Replace with Omnigent's** | Run `codex app-server --listen` and attach the TUI with `--remote`, then deliver with `turn/start`/`turn/steer` (omnigent:omnigent/harnesses/codex_native/launch_args.py:122-127; omnigent:omnigent/inner/codex_native_executor.py:535-560). Mail no longer depends on hook trust, and Codex gets both an idle wake and a mid-turn steer. Apache-2.0 allows reuse. Today Aether runs `app-server` only to list models (aether:internal/scheduler/agent_models.go:38). |
| Wake admission (`internal/server/svc_coord_wake.go`) | **Replace** | Drop TryLock-and-give-up. Keep a durable per-run `wake_owed` state and retry it on turn end, release, resume, unprotect and lock release, plus a periodic sweep. Take the wake out of `AuthorizationMu`, and stop holding that lock across `AssembleCandidate`. |
| Enhanced ACP wake (`internal/coord/acp_wake.go`) | **Keep, extend** | This is the right model, and it is the same as Omnigent's. Persist `enhancedWoken`. Remove the four-retry limit. |
| Claude asyncRewake, pi/omp/OpenCode extensions | **Keep for Standard non-swarm runs** | Re-arm after transport failure instead of halting. Do not suppress a wake for mail that was announced but never read. |
| Worker results | **Copy** | When a worker's turn ends idle without a report, record its last assistant message as a provisional report and notify the integrator (omnigent:omnigent/runner/app.py:3957-3962). |
| Integrator dispatch | **Copy** | Add one verb that starts a worker and delivers its result into the integrator's inbox, the equivalent of `sys_session_send`. |
| Approval-blocked children | **Copy** | Tell the integrator when a worker has been waiting on a human for more than N seconds (`subagent_block_notifier.py`, 120 s). |
| Spawn caps, policy inheritance | **Copy** | `spawn_bounds` per turn (omnigent:omnigent/policies/builtins/orchestration.py:425), and root-to-child policy inheritance. |
| Cross-vendor review | **Copy** | Make review a task type whose harness must differ from the implementer's (omnigent:examples/polly/config.yaml:188-200). Today this is a convention in Omnigent's prompt; Aether can enforce it on the server. |
| Harness readiness | **Copy** | Expose a `configured_harnesses`-style readiness map (installed, signed in, version) to the integrator before it dispatches (omnigent:examples/polly/config.yaml:74-90). |
| MCP bridge (`internal/mcpbridge`) | **Keep, unify** | Expose the same methods as the CLI, task, worker, mission and integration included, with the same explicit-ack rule. Register it for every MCP-capable run. |
| 4 KiB body cap | **Keep for notices, add references** | Allow `evidence_refs` or an artifact for long results so agents do not split messages. |
| No PTY typing for agent mail | **Keep** | Omnigent's `send-keys` path is its least verified and most bug-prone delivery (omnigent:docs/QUEUE_STEER_DESIGN.md:89-96; omnigent#6650). |
| Omnigent's runtime as a replacement for Aether's coordination layer | **Do not adopt** | It is in-memory, tree-only and has no integration step, and its parent-wake class is still open at P1. Aether would trade durability for uniformity it can build itself. |

## 9. The design Aether should converge on

**One durable input queue per run, one driver that delivers it, one tool surface.**

1. **A run input queue on the server.** Human messages, peer mail notices, mission-change notices and worker results all become items with an id and a state: `queued` → `offered` → `consumed`. An item is consumed when the agent's inbox read acks it, or when a turn starts whose prompt carried it. This is Omnigent's "everything is an input" model with Aether's durability.
2. **A driver capability declared per run** and shown in status and the dashboard: `steer` (mid-turn), `next_turn` (prompt when idle), `boundary` (hook context only) or `none`.
   - Swarm workers and integrators require `next_turn` or better. `worker start` refuses `boundary` and `none`, with the real reason.
   - This makes Omnigent's honest per-harness steer table (omnigent:docs/QUEUE_STEER_DESIGN.md:78-96) a contract that is enforced.
3. **Drivers:**

   | Runs | Delivery | Capability |
   | --- | --- | --- |
   | Enhanced, all harnesses | ACP `session/prompt` | `next_turn` |
   | Codex Standard | app-server and `--remote` TUI, `turn/start` and `turn/steer` | `steer` |
   | Claude Standard | the asyncRewake waiter, fixed per section 8 | `next_turn` |
   | pi, omp, OpenCode Standard | native session APIs | `next_turn` |
   | Copilot, Gemini, Cursor, Antigravity Standard | hooks | `boundary` |

   The last row runs as an Enhanced swarm worker, or not as a swarm worker at all.
4. **Wake is a state, not an event.** A durable `wake_owed` record for each run is retried on every edge: turn end, release, resume, unprotect, lock release. A periodic sweep catches anything missed. A wake is never dropped because a lock was busy, and wake is not coupled to `AuthorizationMu`.
5. **Results without ceremony.** At turn end, a worker's last assistant text is captured. If no `report` was made, it becomes a provisional report to the integrator. `report` remains the authoritative success path.
6. **One agent surface.** The MCP bridge and the CLI expose the same methods with the same explicit-ack rule. Error codes are split into unavailable, finished, rate-limited and denied. `inbox --wait` takes longer waits, or the hint tells the agent to end its turn and rely on the wake.
7. **Keep and lead with integration.** Mission → workers → submissions → one frozen, verified candidate → approval → delivery. Neither Omnigent nor its examples have this. With the wake fixed, it is what makes overnight swarms of 10 or more agents produce one mergeable result instead of N PRs for a human to untangle.

## 10. Inferences and open questions

- *(inference)* The Omnigent claim "no global concurrency cap" is based on searches of `server/`, `host/` and `onboarding/`, not an exhaustive read.
- *(inference)* The claim that a crashed Standard Aether worker leaves its attempt `running` comes from the documented login-shell fallback (aether:docs/failure-handling.md:23). It was not reproduced.
- *(inference)* The impact of wakes dropped under `AuthorizationMu` contention is reasoned from the code. No measurement was taken; the lock-wait findings in #512 suggest it is real.
- Whether Codex's `--remote` TUI attach works in Aether's container images and Codex versions needs a live check before committing to the replacement.
- Omnigent's issue volume is inflated by AI-filed issues and a resolve bot (98 bot-merged PRs since 09-25). The bodies carry real logs, so the failure classes cited are real, but the counts are not directly comparable with Aether's 9 issues.
