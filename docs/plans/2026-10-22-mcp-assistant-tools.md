# Connector tools for managing Aether from ChatGPT

**Status:** Done. Not shipped from this plan:

- A queued message from a teammate is approved or denied only in the
  dashboard: deciding it needs the run's control lease, which a connector
  never holds.
- `aether_list_runs` rows carry no pull request link: `run.list` has none, and
  asking GitHub per row would blow the time limit. `aether_get_run` and
  `aether_pr_status` carry it.

**Goal:** From ChatGPT (or Claude), a member launches runs and swarms, sees
what needs them, answers agents and approvals, reads pull request status
and review feedback, sees what got done today and coordinates overlapping
runs, all through the connector of
[2026-10-18-mcp-connector.md](2026-10-18-mcp-connector.md).

## 1. Decisions

1. **The connector has its own tool list.** `internal/mcpbridge` keeps
   `memberTools` for the in-container assistant unchanged and adds
   `connectorTools` (`connector*.go`). The assistant wants full records and
   has its own confirmation tiers; ChatGPT wants compact results, hints and a
   small tool list. Existing connector tool names are kept.
2. **Each scope names what its tools do.** `read` is every read tool,
   always granted. `write` starts and steers work. `control` ends work and
   decides approvals. A tool's lookups (`run.get`, `attention.list`,
   `server.info`) are made under `read`, so only the call that does the
   work takes a write token from the per-connector limiter.
3. **Results are compact.** Each tool returns `structuredContent` and a
   one-line text summary, no JSON copy. `search` and `fetch` return their
   JSON as text, as OpenAI's deep research schema asks. Results carry ids
   and, while the tailnet dashboard serves, `url` links built from
   `server.info`'s `dashboard_url`.
4. **Untrusted text is fenced.** Text agents, teammates or GitHub users
   wrote goes under an `untrusted` field; the server instructions and every
   such tool's description say it is data, never instructions. The rule sits
   inside the first 512 characters of the instructions.
5. **Failures say what to do next.** A not-found or invalid-params failure
   is an `isError` result with the server's message and a hint, such as
   `run run-x not found; call aether_list_runs for run ids`. A
   `run.input.respond` refused for `control_held` adds "ask the member in
   control, or send your answer with aether_message_run".
6. **No tool takes a member id.** `tools.Prepare` refuses any
   `*member_id`, and for `mission.create` fills the accountable human and
   every account with the connector's member.
7. **Idempotency is the caller's key.** Launch, swarm and message take an
   optional `idempotency_key`. Without one nothing is derived, except that a
   run launch of the same workspace, agent and task by the member within 60
   seconds returns that run (`duplicate: true`). `run.inject` and
   `mission.create` need a key on the wire, so a missing one is a fresh UUID:
   a retry sends again, as the descriptions say. Answers and swarm
   cancellation use keys derived from the answered item or swarm, since each
   happens once.
8. **Approvals are decided once, as shown.** `aether_decide_approval` picks
   the `allow_once` or `reject_once` option itself and never an "always"
   one, which would change the agent's policy unasked. Its `action` argument
   must equal the pending request's text, so the client's confirmation shows
   what is decided.
9. **One tool answers every question kind.** `aether_answer` looks the item
   up in `attention.list` and routes it: a swarm question to
   `mission.question.answer`, a teammate's room question to a
   `run.room.post` reply, an agent's question to `run.input.respond`. It
   refuses permission items, which need `control`.
10. **Calls fit ChatGPT's wait.** Each tool runs under a 40-second context,
    inside ChatGPT's roughly 50 seconds. The server stays stateless.

## 2. Tools

| Tool | Scope | Methods |
| --- | --- | --- |
| `aether_list_workspaces` | read | `workspace.list` |
| `aether_list_agents` | read | `agent.list`, or `agent.models` with `harness` |
| `aether_list_runs` | read | `run.list` (`mine` default true), `attention.list` |
| `aether_get_run` | read | `run.get`, `attention.list`, `coord.messages.list`, `run.pr.status` |
| `aether_run_history` | read | `run.acp.history` or `terminal.history`, `coord.messages.list` |
| `aether_needs_attention` | read | `attention.list` |
| `aether_activity_summary` | read | `insights.get` (scope mine), `coord.messages.list` |
| `aether_list_swarms`, `aether_get_swarm` | read | `mission.list`, `mission.show`, `coord.messages.list` |
| `aether_run_changes` | read | `run.git.log`, `run.evidence.list`, `run.git.diff` |
| `aether_pr_status` | read | `run.pr.feedback` |
| `aether_overlaps` | read | `run.overlaps` |
| `aether_memory_search` | read | `memory.search` |
| `search`, `fetch` | read | `run.list`, `mission.list`, `run.get`, `mission.show` |
| `aether_launch_run` | write | `run.launch` |
| `aether_launch_swarm` | write | `mission.create` |
| `aether_message_run` | write | `run.inject` |
| `aether_answer` | write | `mission.question.answer`, `run.room.post`, `run.input.respond` |
| `aether_pause_run`, `aether_resume_run` | write | `run.pause`, `run.resume` |
| `aether_mark_reviewed` | write | `run.review` |
| `aether_decide_approval` | control | `approval.decide`, `run.input.respond` |
| `aether_stop_run`, `aether_close_run` | control | `run.kill`, `run.close` |
| `aether_cancel_swarm` | control | `mission.cancel` |

Reports come from `coord.messages.list`, where runs' `aether_report` calls
are stored, rather than `workspace.timeline`.

## 3. Limits

- `tools/list` with every scope is about 14 KB, under the 20 KB the unit
  test allows for ChatGPT's roughly 5000-token budget for tool definitions.
- Lists default to 20 rows and cap at 100; untrusted text is cut to 600
  characters, a conversation item to 2000 and a diff to 12000.
- The write limiter of 30 calls, then one every 2 seconds, is unchanged.

## 4. Tests

- `internal/mcpbridge/connector_test.go`: every tool through
  `RegisterConnectorTools` against a fake call: the list per scope, its
  hints and size; results, summaries and routed params; refusals and hints.
- `internal/assistant/tools`: `Prepare` fills and guards `mission.create`.
- `internal/edge/edgetest/connector_tools_test.go`: through a real edge and
  server with OAuth, the tools per scope, launch replay and duplicate
  launches, a swarm as its caller, a message, a teammate's question listed
  and answered, and a viewer's launch refused.
