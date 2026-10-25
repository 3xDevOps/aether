# Harness layer: Omnigent vs Aether

How each project drives coding-agent CLIs (Claude Code, Codex, Cursor, OpenCode, Pi, Gemini and the rest).
Omnigent at `8b46a8a37961616e3e78d7f96c37ed8ccf08f07e` (2026-10-25), Aether at `77b4f3bb`.

## Verdict

1. **Omnigent's harness layer is broader and better specified than Aether's, but not more stable.** It declares each harness's capabilities as data, normalizes every harness into one typed event stream, and runs real pinned Claude Code, Codex and pi CLIs against a mock LLM on every PR. Aether has none of the three.
2. **It pays for breadth with constant fixing.** It made 351 harness fix commits in 61 days, 102 of them for Claude alone, rising to 58 a week. Its own docs say each native harness implements the shared flows separately, "so a fix for one harness does not reach the others".
3. **In both projects, flakiness tracks how input reaches the agent.** Where Omnigent drives a vendor protocol (Codex app-server, OpenCode server, Pi extension), it never types or reads the screen, and its bugs are about process lifecycle. Where it pastes into tmux and reads the screen (Claude, Cursor and six smaller agents), it has the edge cases Aether feels: lost Enters, unreported dialogs, readiness timeouts, breakage on vendor UI updates.
4. **Aether looks steadier mainly because it does less.** It never mirrors chat out of a terminal, and ACP adapters absorb vendor drift. Its real gaps: three unrelated event pipelines; no tool events in Standard runs; cost tracked only for Claude Background runs; an approvals inbox that never reaches the agent; four of nine agents with no status; no conformance test; CI that touches 3 of 9 real CLIs and only checks their arguments parse.
5. **Per harness:** Omnigent is better for Codex, OpenCode, Antigravity and the eight agents Aether lacks. For Claude, Omnigent's integration is richer and also the most fragile in either repo; Aether's is thinner and steadier. For Pi, both use the same design and Omnigent's covers more. For Cursor, Gemini and Copilot, Aether's ACP route is as good or better.
6. **Copy:** the capability record and its drift-checking bench; mock-LLM end-to-end tests with real pinned CLIs (the highest-value fix for flakiness); the Codex app-server and OpenCode serve/attach hybrids, which give chat and a live terminal in one run; approvals through hooks that wait on the server; per-message delivery tracking; central usage pricing.
7. **Replace outright:** Aether's Codex integration with an app-server hybrid; its ad-hoc capability flags with a declared record; `internal/approvals` and the Claude-only `internal/adapter` with one item model.
8. **Do not copy:** pasting into Claude's TUI through tmux, tmux itself (Aether's `ptyhost` is sturdier), or scraping vendors' private databases.
9. **Integration:** an Omnigent runner cannot run as a sidecar on its own, because it needs a full Omnigent server. Port the designs to Go, and lift only test assets (Apache-2.0).
10. **Investor read:** Omnigent has won on harness breadth (132 authors in 61 days against about 4). Aether's moat is not its harness layer.

---

## Method

- Omnigent cloned with `timeout 300 git clone --filter=blob:none`. Every cited file was read at the SHA above.
- Four read-only research passes covered Omnigent native wrappers, Omnigent SDK/ACP and the event model, Aether's harness layer, and fix-commit history in both repos. Load-bearing claims were then re-read directly; the citations below are the ones verified.
- Commit counts come from `git log --since=2026-08-25` (61 days), matched on conventional-commit scopes, with a hand-classified sample. They are approximate, and the sections below give their caveats.
- No test suite was run in either repo. **(inference)** marks a conclusion drawn rather than read.

## 1. Integration modes

### Omnigent: five integration modes, four ways to control a native TUI

`omnigent:omnigent/harness_capabilities.py:24-31` declares five modes: `sdk-in-process`, `cli-subprocess`, `acp-subprocess`, `native-tui` and `native-server`.

Every harness has a capability row (`omnigent:omnigent/harness_plugins.py:356-754`). There are 12 native harnesses, 11 SDK/CLI/ACP harnesses, the generic `acp` harness, and `open-responses`, whose row has no module behind it (vestigial).

**Native harnesses.** All 12 run the real vendor TUI in a tmux pane owned by the runner. Users watch and type into it from the web through xterm.js over `tmux -C`, and chat with the same session in parallel. In the feature map's words, they "chat with it in Omnigent while its own terminal runs alongside" (`omnigent:feature-map/native-harnesses.md:3-8`).

How Omnigent controls that TUI depends on what the vendor exposes. There are four tiers:

| Tier | Harnesses | How input reaches the agent | How output is observed |
|---|---|---|---|
| A. Puppet the TUI | claude, cursor, devin, goose, hermes, kimi, kiro, antigravity (input only) | tmux `load-buffer` + `paste-buffer -p`, then Enter, verified by polling the pane | Transcript JSONL tailing, vendor hooks, private SQLite/protobuf stores, pane diffs |
| B. TUI as a remote client | codex | `codex app-server --listen ws://…`; the TUI runs `codex … --remote <url>`; Omnigent is a second JSON-RPC client sending `turn/start` / `turn/steer` | App-server notifications |
| C. Server + attach | opencode | `opencode serve` (HTTP/SSE) + `opencode attach`; input via `POST /session/{id}/prompt_async` | SSE `/event` |
| D. In-process channel | pi (JS extension, 3,334 lines in `omnigent/resources/pi_native/`), qwen (`--input-file`/`--json-file`) | Inbox file → `pi.sendUserMessage`; NDJSON submit line | Extension event POSTs; event file |

**SDK and CLI harnesses** (claude-sdk, codex, openai-agents, cursor, copilot, antigravity, pi, kimi, hermes):

- Each runs as a per-conversation subprocess serving FastAPI over a Unix socket.
- Each wraps an `Executor` that yields 12 internal event dataclasses (`omnigent:omnigent/inner/executor.py:116-375`, interface at `:652-733`).
- One adapter translates those events to the wire (`omnigent:omnigent/runtime/harnesses/_executor_adapter.py:996-1190`).

**ACP CLI harnesses** are pure data rows; only `grok` and `jcode` exist (`omnigent:omnigent/acp_cli_harnesses.py:89-123`).

- Goose and Qwen keep their own ACP clients.
- Gemini CLI has no built-in row; it is reachable only as a user-configured `acp:<slug>`.
- The generic ACP client is partial:
  - `terminal: False` (`omnigent:omnigent/inner/acp_executor.py:831`);
  - it never calls `session/load`, so resume replays the transcript as text;
  - it never calls `set_mode`;
  - it drops `plan` and `available_commands_update`.

**Custom YAML agents** (`omnigent:docs/AGENT_YAML_SPEC.md`) are portable specs that each pick a vendor harness. Sub-agents can use different harnesses. There is no Omnigent-owned agent loop in production.

### Aether: three modes, two of them harness-agnostic

`aether:docs/harnesses.md:12-16` defines the modes:

- **Standard** (`tui`): the vendor TUI in a container PTY owned by `internal/ptyhost`.
- **Enhanced** (`acp`): ACP over stdio.
- **Background** (`headless`): one unattended run.

All modes turn permission prompts off: "the container is the isolation boundary" (`aether:docs/adapters.md:39-45`, `aether:docs/harnesses.md:358-372`).

Standard deliberately never mirrors chat out of the TUI. The integration guide says: "Do not write to the agent's terminal. Aether has no terminal-input fallback" (`aether:docs/harness-integration.md:101-104`). Adapter events are optional by rule: "no feature is allowed to hard-require adapter events" (`aether:docs/adapters.md:5-7`).

## 2. Per-harness tables

### Omnigent native harnesses

| Harness | Input delivery | Output / tool events | Approvals / elicitation | Model, auth | Resume | Interrupt / steer | Cost | Sub-agents |
|---|---|---|---|---|---|---|---|---|
| claude-native | Bracketed paste. Polls until the draft renders, presses Enter, then re-presses Enter with backoff for up to 20 s (`omnigent:omnigent/harnesses/claude_native/bridge.py:336-355`) | Tails transcript JSONL every 0.25 s; 10 hook events; status from OSC 7501, then an undocumented `~/.claude/sessions/<pid>.json`, then pane diff | `PermissionRequest` hook long-polls the server for up to 86,400 s (`omnigent:omnigent/harnesses/claude_native/hook.py:59`); AskUserQuestion answered through it | `--model`; a live switch types `/model` (marked HACK, `bridge.py:4826`); vendor login, or `ANTHROPIC_BASE_URL` + `apiKeyHelper` | Rebuilds the JSONL, then `--resume <sid>` | Escape keystroke; steer = paste | statusLine shim plus priced transcript usage | Globs `subagents/agent-*.meta.json` → child sessions; 5 s quiet heuristic |
| codex-native | `turn/start` / `turn/steer` RPC; confirmed by the returned turn id | `item/*`, `turn/*`, `thread/*` notifications; hooks used for policy only | JSON-RPC `requestApproval`, MCP elicitation, `requestUserInput` → web card | `thread/settings/update`; `-c` provider overrides; falls back to ChatGPT login | Rebuilds the rollout + `thread/resume`; TUI runs `resume --remote` | `turn/interrupt`, `turn/steer`. Permission-mode switching still types `/permissions` and scrapes the popup (`omnigent:omnigent/runner/native_controls.py:176-193`) | `thread/tokenUsage/updated` | `thread/started` with a `subAgent` source → child sessions; `/side` = `thread/fork` |
| opencode-native | `prompt_async`; a 2xx counts as delivered | SSE; `message.part.delta` ignored, so no token streaming | `permission.asked` → policy → `/permission/{id}/reply` | Per-prompt provider/model; copies `auth.json`; version window 1.17.7 to <1.19 (`omnigent:omnigent/harnesses/opencode_native/client.py:33-36`) | Session id, else history replay | `POST /abort`; no live steer | Vendor `cost`/`tokens` | Own child sessions filtered out |
| pi-native | Inbox JSON file → extension `pi.sendUserMessage` | Extension `pi.on(...)` POSTs; no scraping | Extension wraps `ctx.ui.confirm/select`; fails closed | Per-session `models.json`; `pi.setModel` | `--session-dir` + `--session` | `ctx.abort()`; steer via `deliverAs:"steer"` | `message_end` usage | None |
| cursor-native | Paste + Enter; the only check is that the first 24 characters are visible | Polls the private `~/.cursor/chats/…/store.db` every 0.7 s; prose only, no tool calls | Byte-scans protobuf blobs for pending tool calls; answers with `y`, or Esc+Enter | Parses `cursor-agent models`; types `/model`; own login | `--resume <chatId>` | Escape; steer = paste | Undocumented `stop` hook token counts | None |
| antigravity-native | Typed into the TUI on purpose, because the RPC path produced a separate conversation | `StreamAgentStateUpdates` RPC to agy's language server | `HandleCascadeUserInteraction` + keys | Google OAuth / API key / proxy | `--conversation` | `CancelCascadeSteps` | `modelUsage` | Child cascades |
| devin, goose, hermes, kimi, kiro, qwen | Paste (qwen: file) | Hook logs, SQLite stores, `wire.jsonl`, recorder files | Hooks (devin, kimi), screen regex + keystroke menus (goose, hermes, kiro) | Mostly own login | Mixed; goose and hermes start fresh | Escape / C-c | None for goose, hermes, qwen; kiro passes credits through as USD | Devin only |

### Omnigent SDK, CLI and ACP harnesses

| Harness | Mechanism | Approvals | Resume | Interrupt / steer | Cost | Notes |
|---|---|---|---|---|---|---|
| claude-sdk | `claude-agent-sdk` `ClaudeSDKClient` | `can_use_tool` gate → policy + elicitation (declared `none`: declaration drift) | Cold (replay) | `client.interrupt()`; no live queue | `ResultMessage.usage` | Routes Agent-tool spawns; no child sessions |
| codex | `codex app-server` per session | `approvalPolicy:"never"`; only bridged tools are gated | Warm in-process | `turn/interrupt`, `turn/steer` | `tokenUsage` | |
| openai-agents | OpenAI Agents SDK in-process | Policy inside function tools | Cold | Cancel | Raw response usage | |
| cursor | `cursor-sdk` | Post-hoc DENY cancels the turn | Warm in-process | Close | Mapped tokens | |
| copilot | `github-copilot-sdk` | `on_permission_request` | Cold | `session.abort()` | Vendor cost (nano-AIU) | Only harness with vendor cost |
| pi | `pi --mode rpc` | None | Cold | RPC `abort` / `steer` | Summed per message | |
| goose, qwen, grok, jcode, `acp:<slug>` | ACP clients | `session/request_permission` → policy + elicitation | Cold | `session/cancel` | ACP `usage` | Partial ACP client (see §1) |
| kimi, hermes | One CLI process per turn | hermes: shell hook | kimi `-r`; hermes `--resume` (declared cold: drift) | Kill the process | kimi: parsed post hoc | |

Usage from every executor is normalized to non-cached input plus cache buckets, then priced centrally (`omnigent:omnigent/llms/context_window.py:250-272`).

### Aether, by mode

| | Standard (`tui`) | Enhanced (`acp`) | Background (`headless`) |
|---|---|---|---|
| Input | Raw `message + SubmitSequence` written to PTY stdin, with no bracketed paste and no delivery check (`aether:internal/ptyhost/session.go:927-944`, `aether:internal/harness/harness.go:661-666`). The steer flag is discarded (`aether:internal/scheduler/driver.go:51-52`). Mail arrives through hooks and idle wake | `session/prompt`; `_session/steering` when the agent advertises it (`aether:internal/acphost/conn.go:466,475`). Mail = one server prompt while the session is idle | The task only |
| Observation | PTY transcript (asciinema), OSC title, git diff. Status (`working`/`waiting`) comes from hooks via `agentstatus`. No tool events | `session/update` → `acphost.Item`, 17 kinds (`aether:internal/acphost/items.go:16-32`) | Claude only: stream-json adapter (`aether:internal/adapter/adapter.go:30-32`). All other CLIs unparsed, unless they run over ACP |
| Approvals | Off by flags; answered in the terminal | `request_permission` and `elicitation/create` become request cards (`conn.go:731,794`) | Auto `allow_once` |
| Model | `ModelArgs` flag | `session/set_model` / config option after open | Flag |
| Resume | Only on a mode switch or usage fallback | `session/resume` → `session/load` → new session with a notice (`aether:internal/acphost/session.go:343-370`) | Claude only |
| Interrupt | None programmatic | `session/cancel` (`conn.go:488`) | None |
| Cost | Unmetered (`aether:internal/cost/cost.go:1-10`) | `usage_update` is shown but never becomes `RunCostPayload` (`aether:internal/acphost/project.go:187-199`) | Claude metered; others unmetered |
| Sub-agents | Claude `SubagentStart` hook | Claude via `_meta.claudeCode`; others via the ACP subagents capability and tool-input heuristics | Claude adapter matches tool name `"Task"` only (`aether:internal/adapter/claude.go:105-107`). The ACP projector also accepts `"Agent"` (`project.go:466`), so headless subagents may be missed on current Claude Code **(inference)** |

### Aether, by harness

| Agent | Standard status / mail | Enhanced via | Switchable | Model list, quota, usage fallback | Notes |
|---|---|---|---|---|---|
| claude | Full hooks + asyncRewake idle wake | `claude-agent-acp` 0.86.0 (`aether:internal/harness/acp.go:39`) | yes | yes / yes / yes. Usage limits are detected from 12 English message prefixes (`aether:internal/agentstatus/agentstatus.go:243-258`) | Three Claude-only profile flags (`harness.go:412-415`); a 444-line Claude-only background tracker |
| codex | Turn end only (`notify`); one-time hook-trust screen; no idle wake | `codex-acp` 2.1.1 | no | yes / yes / Enhanced only | Subagents do not nest (`aether:docs/enhanced-runs.md:338`) |
| pi | Full, plus idle wake (`pi.ts`) | `pi-acp` 0.0.34 | no | none | Never asks for permission |
| omp | Full, plus idle wake | native `omp acp` | yes | none | |
| opencode | Full; a wrapper picks the v1 or v2 plugin by major version | native `opencode acp` | no | none | Submit is `\r\r` (`harness.go:550`); never auto-updated |
| copilot | No status; hooks; no wake | native `--acp --allow-all` | no | none | |
| gemini | No status; hooks only if installed by hand | native `--acp` | no | none | |
| cursor | No status; hooks only if installed by hand | native `agent --force acp` | no | none | |
| antigravity | No status, no hooks | none | no | none | |

Source: `aether:docs/harnesses.md:18-28,515-524,538-553`.

### Head to head

| Harness | More consistent | Why |
|---|---|---|
| Claude Code | **Split** | Omnigent shows chat and a live TUI together, with hook approvals, subagent child sessions and cost. It is also the most-fixed integration in either repo: 102 fixes in 61 days, about 35% from typing into and reading the TUI. Aether's Standard mode is narrow but steady (hook status and wake). Its Enhanced mode runs the Agent SDK adapter and loses the TUI |
| Codex | **Omnigent** | The app-server plus `--remote` TUI gives deterministic RPC steer, interrupt, approvals, token usage, subagents and side threads. Aether reports turn end only, needs a hook-trust screen, has no idle wake, and Enhanced subagents do not nest |
| OpenCode | **Omnigent** (design) | serve + attach gives a TUI and an HTTP/SSE control plane at once. Aether has v1/v2 plugins plus a separate `opencode acp`. Both pin versions |
| Pi | **Tie in design, Omnigent in coverage** | Both use an in-process extension. Omnigent's also carries approvals, model switching and steering |
| Cursor | **Aether** | Native ACP versus Omnigent's private SQLite protobuf scanning and keystroke approvals. Omnigent's separate `cursor` SDK harness is reasonable |
| Gemini CLI | **Aether** | Built-in `gemini --acp`; Omnigent has no built-in row |
| Copilot | **Comparable** | Aether: native ACP. Omnigent: SDK with vendor cost |
| Antigravity | **Omnigent** | Aether has Standard only, with no hooks or status. Omnigent has a native integration (11.5k lines) and an SDK harness |
| omp | Aether only | |
| Kimi, Kiro, Goose, Hermes, Qwen, Devin, Grok, jcode | Omnigent only | |

## 3. The common event model

| | Omnigent | Aether |
|---|---|---|
| Internal layer | 12 executor event dataclasses, the same for every SDK harness (`omnigent:omnigent/inner/executor.py:116-375`) | None shared |
| Wire stream | `ServerStreamEvent`: a Pydantic discriminated union of 56 variants (`omnigent:omnigent/server/schemas.py:4962`), shaped like the OpenAI Responses API (`response.output_text.delta`, `response.output_item.done`, `turn.*`, `session.*`) | Three separate pipelines: (1) ACP → `acphost.Item`, 17 kinds; (2) hooks → `agentstatus.Report`, working/waiting only; (3) Claude stream-json → bus `run.agent` with 5 kinds (`aether:internal/events/event.go:420-429`) |
| Persisted items | 11 typed item kinds (`omnigent:omnigent/entities/conversation.py:786-798`) | `transcripts/<run>.items.jsonl` for Enhanced runs only; Standard has an asciinema transcript |
| Specification | Generated `openapi.json`: OpenAPI 3.2.0, 106 paths, 185 schemas, 57 `*Event` schemas, plus drift tests | Go structs; client wire `protocol.Version = "3"` (`aether:internal/protocol/protocol.go:16`); item kinds listed in prose |
| Coverage | Every harness, native or SDK, lands in the same stream | Tool-level structure exists only in Enhanced runs, and in Background runs only for Claude |
| Leaks | `session.codex_approval_mode`, `session.permission_mode`, `session.btw_sidechat`, 27 `external_*` ingress types including vendor-named ones; `item` inside `OutputItemDoneEvent` is `dict[str, Any]`; no stream protocol version | Claude-only branches in `acphost` (13 sites), Cursor (3) |

**Verdict.** Omnigent's model is richer, more uniform and better specified. Aether has a good ACP projection, but it is one of three pipelines, and the default mode (Standard) emits no tool events at all.

## 4. Capability model and conformance

**Omnigent**

- **Capability record.** `HarnessCapabilities` is a frozen dataclass with these fields:
  - integration mode, elicitation, resume, effort family, model family, auth model, fork-history mode, instruction delivery;
  - `subagents`, `interrupt`, `streaming`;
  - tri-state `steering`, `live_queue`, `images`, `compaction`;
  - the bench's shell-tool prompt (`omnigent:omnigent/harness_capabilities.py:106-162`).

  It is served on `/v1/harnesses`, and guard tests derive some fields from code.
- **Bench.** `tests/harness_bench/` (about 7.4k lines) derives its expected results from `harness_capabilities()` (`omnigent:tests/harness_bench/manifest.py:6-9,59`) and reports DRIFT when a declaration and a live observation disagree. The design doc calls it "shipped and in use" (`omnigent:docs/harness-bench-design.md:9-11`). It also lists what is not yet wired: "Steering, live queue, resume, images, and compaction probes" (`:303-307`).
- **What is verified live.** Only 4 of 25 rows: "Only the four P0 SDK harnesses (claude-sdk, codex, pi, openai-agents) have these verified live by the harness bench today" (`omnigent:omnigent/harness_plugins.py:352-355`).

  Declarations already drift from code:
  - claude-sdk, cursor and copilot declare `elicitation=none` but wire elicitation;
  - hermes declares cold resume but uses `--resume`.

  The seam doc still says "No bench code has been changed yet" (`omnigent:designs/harness-capabilities-bench-seam.md:5-7`), which the code contradicts.
- **What CI runs.** No workflow runs the live bench; `harness_bench` appears in no `.github/workflows` file. What does run, on every PR and daily (`omnigent:.github/workflows/e2e.yml:3-16`), is `tests/e2e` against a mock LLM (`omnigent:tests/server/integration/mock_llm_server.py`). That mock is 1,630 lines and serves:
  - `/v1/messages` (`:1190`), `/v1/responses` (`:1112`), `/v1/chat/completions` (`:1296`);
  - request ledgers and gates (`:1600-1607`).

  The e2e jobs use pinned real CLIs: `@anthropic-ai/claude-code 2.1.283`, `@openai/codex 0.142.0` and `pi-coding-agent 1.0.4` (`omnigent:.github/ci-deps/package.json:5-9`). The round-trip matrix covers claude-sdk, codex and pi (`omnigent:tests/e2e/_harness_probes.py:80-112`). Native harnesses have one journey test each, and live tests for most of them need vendor credentials (`omnigent:feature-map/native-harnesses.md:166-177`).
- **Acceptance checklist.** It is honest about its limits: "not another test runner or a claim that every harness has passed"; "The offline matrix is declarations only" (`omnigent:feature-map/harness-acceptance.md:3-6,66-67`).

**Aether**

- **No capability record.** The `Profile` struct (`aether:internal/harness/harness.go:248`) mixes about 45 launch fields with booleans that stand in for capabilities:
  - `ACPClaudeCode` is vendor identity, not a capability;
  - `SwitchVerified` is set by hand after a manual live test.

  Derived capabilities are `EnhancedSupport()` and `Switchable()` (`aether:internal/harness/acp.go:44-56`). Model discovery, quota and cost are keyed by harness name in code.
- **No behavioral conformance.**
  - CI runs real CLIs for claude, codex and opencode only, and only `TestSmokeHeadlessNoLogin`. It checks that headless argv parses and the run dies of missing credentials (`aether:internal/harness/smoke_integration_test.go:14-27`, `aether:.github/workflows/ci.yml:250-274`).
  - Live tests are manual: `ACP_LIVE=1` for `TestLiveEnhancedRunsWithoutPermissionPrompts` and `TestLiveSwitch` (`aether:internal/harness/permission_live_test.go:21-23`, `aether:internal/harness/switch_live_test.go:81-83`).
  - The ACP fixtures are synthesized, not recorded.
  - No CI test touches the real pi, omp, copilot, gemini, cursor or antigravity binaries.

**Verdict.** Omnigent has a capability model and a real (partial) conformance seam. Aether has neither. The gap that matters most for the flakiness the team feels is the mock-LLM end-to-end test with pinned real CLIs **(inference)**.

## 5. Special cases and edge cases

**Code size** (non-test):

| | Lines |
|---|---|
| Omnigent `omnigent/harnesses/*_native` | about 97k (claude 29.1k, codex 21.5k, antigravity 11.5k) |
| Omnigent `inner/` | 62k |
| Omnigent `runner/` | 67k (`runner/app.py` alone is 8,472) |
| Aether harness, acphost, adapter, ptyhost, coordhooks, agentstatus, approvals | about 17k Go, plus 1.8k JS/TS hook assets |

**Harness-name branches:**

- Omnigent: 98 `harness == "…"`-style comparisons across `omnigent/`. The feature map: "each implements them separately, so a fix for one harness does not reach the others" (`omnigent:feature-map/native-harnesses.md:6-8`).
- Aether: about 61 name-keyed conditionals in non-test Go, plus 13 Claude-only sites in `acphost`.

**Fragility markers per Omnigent native package** (raw `rg -c`):

| Package | sleeps | `capture-pane` | send-keys / paste |
|---|---|---|---|
| claude | 32 | 53 | 39 |
| antigravity | 17 | 11 | 23 |
| cursor | 16 | 11 | 22 |
| codex, opencode, pi | ≤12 | 0 | 0 |

**Fix commits** (2026-08-25 to 2026-10-24):

| | Omnigent | Aether |
|---|---|---|
| Commits/day | 42.1 | 8.0 |
| Harness-scoped fixes | 351, about 478 counting other scopes (claude-native 102, codex-native 64, codex 41, pi-native 30, terminal/tmux 20) | 19 strictly harness-specific, plus about 21 ACP/terminal-transport fixes |
| Harness fixes per 100 commits | 13.7 (about 18.6) | 3.9 (about 8.1) |
| Authors in window | 132 | about 4 |
| Issues | 2,583 (572 labelled `comp:harness-t1`, 220 open) | 9 |

Aether squash-merges large PRs, so its fix count is likely an undercount by 1.5 to 2 times **(inference)**.

**Same failure classes in both repos:**

| Class | Omnigent | Aether |
|---|---|---|
| Lost or garbled input | `3670fac49` (#5494) Enter read as a newline mid-paste; `73c4b90ce` (#10122) refocus the input box; `dd4b9d126` (#9932) copy-mode swallowed keys | `d67aab00` (#147) bracketed-paste mode lost from the replay ring; `40dd88f5` (#95) OpenCode needs `\r\r` |
| Vendor UI and CLI drift | `434acbe84` (#10692) Escape quits a Claude 2.1.299 dialog; `2be4c6f63` (#11379) a new 2.1.308 startup question fires no hook; `b77990a1c` (#8828) Codex renamed `--remote` | `e3efa36c` (#83) headless Claude needs `--verbose` |
| Claude config state | #9583, #4585, #11379 | `b01816e1` (#280) a borrowed login lands in the onboarding wizard |
| Resume picks the wrong or no conversation | `b4d63e860` (#10961) "no rollout found" | `2a41a5cd` (#60) `--continue` picked another run's conversation |
| Protocol races and lifecycle | `d27105007` (#10556) silent app-server respawn | `97d2cc46` (#490) cancel sent before prompt |
| Status detection | #11484 a lost StopFailure leaves the session "running" | `2d37db09` (#64) echo counted as output; `15edec58` (#424) |

**Is the native approach more robust?** No. Moving to a protocol shifts the failures rather than removing them. Codex-native trades paste bugs for app-server readiness, resume and lifecycle bugs. The worst class, typing into and reading a live TUI, is about 35% of claude-native's fixes. Omnigent pays it because it mirrors chat out of a TUI, a job Aether's Standard mode does not attempt.

Omnigent's delivery ledger makes the uncertainty explicit. For example, `claude_native_delivery_finished` with `draft_cleared` "does not prove native transcript persistence" (`omnigent:docs/NATIVE_INPUT_DELIVERY_DIAGNOSTICS.md:47`). Its tmux layer has its own failure diagnostics for a disappearing tmux server (`omnigent:docs/terminal-probe-error-capture.md:3-10`).

Aether's equivalents are cruder:
- Standard input is fire-and-forget.
- Usage-limit detection depends on English Claude message prefixes.
- Four agents are judged on silence alone.

## 6. What Aether should copy

1. **A declared capability record per harness**, modeled on `omnigent:omnigent/harness_capabilities.py`.
   - Make it a Go struct on `Profile`, and serve it in the agents API so the dashboard composer uses it (steer vs queue, images, approvals) instead of name checks.
   - Add a guard test that derives fields from code where possible.
2. **Mock-LLM end-to-end tests against real pinned CLIs.**
   - Pin claude, codex, pi, opencode, gemini and copilot versions in CI, and point `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` at a mock inside a real run container.
   - Per agent and mode, assert: one turn, a tool call, an interrupt then continue, mail wake, and status settling.
   - `omnigent:tests/server/integration/mock_llm_server.py` is Apache-2.0, self-contained and test-only, so it can be vendored as a test helper with NOTICE attribution.
   - This is the highest-value item for "flaky, many edge cases" **(inference)**.
3. **Protocol-backed hybrids for Codex and OpenCode.**
   - One `codex app-server` per run, the TUI attached with `--remote`, and Aether as a JSON-RPC client.
   - `opencode serve` + `opencode attach` likewise.
   - A run then has chat and a live terminal at once, which ends the Standard/Enhanced split for these agents and gives deterministic steer, interrupt, approvals, usage and subagents.
4. **Approvals through hooks that wait on the server.**
   - Claude's `PermissionRequest` hook (and Codex JSON-RPC approvals) block on the server.
   - Policy outcomes: ALLOW = no opinion, so the vendor's own prompt still applies; ASK = held on the server; DENY = fail closed (`omnigent:omnigent/native/native_policy_hook.py:1-18`).
   - Aether's per-run socket and `coordhooks` can carry this.
5. **Message delivery identity and settlement.** A stable input id, an attempt id, and explicit `accepted`, `refused`, `ambiguous` and `undelivered` outcomes, which Standard runs lack today.
6. **Central usage normalization and pricing.** Turn ACP `usage_update` (and future app-server usage) into `RunCostPayload` so Enhanced runs are metered.
7. **One two-layer event model.** A small internal event set per driver, one translator, and one versioned, typed wire union. Avoid Omnigent's `dict[str, Any]` items and vendor-named events.

## 7. What to replace outright

| Replace Aether's | With | Evidence |
|---|---|---|
| Codex integration: Enhanced via `codex-acp`, Standard `notify`-only | Omnigent's codex-native design: app-server + `--remote` TUI + JSON-RPC (port it) | Aether's subagents do not nest, status covers turn end only, and there is no idle wake (`aether:docs/harnesses.md:21,543-545`). Omnigent's codex fixes are lifecycle bugs, not input bugs |
| Ad-hoc capability flags (`ACPClaudeCode`, `SwitchVerified`, `Reporter`, name-keyed model, quota and cost maps) | A `HarnessCapabilities`-shaped record | `aether:internal/harness/harness.go:410-416`, `aether:internal/harness/acp.go:44-56` |
| `internal/approvals` inbox | Approvals that reach the agent (hook long-poll in Standard, ACP or JSON-RPC in Enhanced), all on one request-item model | Its only source is Claude headless `ExitPlanMode` (`aether:internal/approvals/inbox.go:208-213`); `Decide` stores a decision that never reaches the agent (`inbox.go:343-365`) |
| `internal/adapter` (Claude-only stream-json) | The same item model as Enhanced runs, fed by ACP for Background runs | Only one adapter exists (`aether:internal/adapter/adapter.go:30-32`); possible `"Task"` vs `"Agent"` drift (§2) |
| CI that only checks headless arguments parse with no login | Pinned-CLI mock-LLM end-to-end tests (§6.2) | `aether:internal/harness/smoke_integration_test.go:14-27` |

Keep:
- **`internal/ptyhost`, not tmux.** Omnigent maintains its own tmux-death diagnostics, polls `capture-pane` once a second per pane, and has 20 terminal/tmux fixes in the window.
- **`internal/acphost`** as the ACP client. It is more complete than Omnigent's generic one: `session/resume`/`load`, set mode and model, steering, elicitation forms and URLs.
- **The rule "do not write chat into the TUI" for Claude**, until Claude ships a remote-control protocol.

## 8. What not to copy

- Claude tmux paste puppeting: the submit loop, re-pressing Enter for 20 s, and recognizing dialogs by glyphs and English strings (`omnigent:omnigent/harnesses/claude_native/bridge.py:336-355`). This is where most of claude-native's 102 fixes come from.
- Scraping vendors' private stores: Cursor's `store.db` protobuf, Hermes and Goose SQLite, Claude's `~/.claude/sessions/<pid>.json`. Each breaks silently when the vendor upgrades.
- Keystroke menu navigation for approvals and modes. Even codex-native does this for `/permissions` (`omnigent:omnigent/runner/native_controls.py:176-193`).
- An 8.5k-line runner module with 46 harness branches (`omnigent/runner/app.py`).

## 9. Integration feasibility

**Running an Omnigent runner as a sidecar in Aether containers: not viable.**

- The runner is not a library. It "reads … `RUNNER_SERVER_URL`: Omnigent server base URL for outbound calls (spec fetch, response resolution, and WS tunnel registration)" (`omnigent:omnigent/runner/_entry.py:1-7`).
- A sidecar therefore means an Omnigent server too (FastAPI, SQLAlchemy and Alembic, Python 3.12+, tmux, `claude-agent-sdk`, `openai-agents`; `omnigent:pyproject.toml`) per run or per Aether server.
- Aether would then bridge Omnigent's 56-event stream, approvals, sessions and credential model (`OMNIGENT_CREDENTIAL` gateways) into its own. That is a second control plane, plus Omnigent's churn of roughly six harness fixes a day **(inference)**.

**Porting the designs to Go: viable, and recommended.** Rough sizes **(inference)**:

| Piece | Estimate |
|---|---|
| Capability record + API + guard test | a few hundred lines |
| Codex app-server JSON-RPC client | 1-2k lines; reuses `acphost`'s JSON-RPC connection patterns |
| OpenCode HTTP/SSE client | about 1k lines |
| Hook long-poll approvals | extends `coordhooks` and the per-run socket |
| Delivery ledger | an extension of the existing `not_sent` handling |

**Directly liftable under Apache-2.0** (keep NOTICE):
- the mock LLM server and the pinned-CLI CI pattern (`omnigent:.github/actions/setup-harness-cli`), as test infrastructure;
- possibly parts of the Pi extension (`omnigent/resources/pi_native/`, JS), against Aether's `pi.ts`;
- not the Python runtime.

## 10. Doc drift found in Omnigent

- `designs/harness-capabilities-bench-seam.md:5-7` says no bench code changed; `tests/harness_bench/manifest.py` already derives from capabilities.
- `docs/harness-bench-design.md` counts its P1 probes inconsistently, and says the native-TUI driver lacks ALLOW/ASK in one place but has them in another.
- `omnigent/harnesses/antigravity_native/main.py:10-13` says input goes over RPC; the code types into the TUI.
- Capability declarations versus code: see §4.
