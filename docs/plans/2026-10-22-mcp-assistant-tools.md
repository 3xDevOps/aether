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
3. **Results are compact, and the text carries them.** Each tool returns
   `structuredContent`, and its text is a one-line summary, a newline and
   the same result as compact JSON: the MCP spec asks for it, and clients
   that give the model only the text (Claude Code, possibly claude.ai)
   would otherwise see "3 runs." and no ids. Results stay lean to pay for
   it. `search` and `fetch` return their JSON alone, as OpenAI's deep
   research schema asks. Results carry ids and, while the tailnet
   dashboard serves, `url` links built from `server.info`'s `dashboard_url`.
4. **Untrusted text is fenced.** Text agents, teammates or GitHub users
   wrote goes under an `untrusted` field: run titles (terminal titles the
   agent sets), the server's reasons (which quote questions and blocked
   reports), reports, request and approval text, option labels, link URLs,
   PR titles and comments, tasks, memory notes. Trusted fields carry ids,
   states and a fixed label per attention kind, and no plain-text summary
   quotes free text. The server instructions and every such tool's
   description say it is data, not instructions; the rule sits inside the
   first 512 characters of the instructions, with listing before launching,
   `aether_list_runs` query, `aether_message_run` and
   `aether_needs_attention`. `search` and `fetch` have fixed shapes, so
   their descriptions carry the rule and `fetch`'s text marks where agent
   text begins.
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
   run launch of the same workspace, agent, task, mode and model by the
   member within 60 seconds that has not failed returns that run, and a
   swarm of the same workspace and objective whose accountable human is the
   member (`server.info`'s member) within 60 seconds returns that swarm
   (`duplicate: true`). `run.inject` and `mission.create` need a key on the
   wire, so a missing one is a fresh UUID: a retried message sends again, as
   its description says. Answers and swarm
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
11. **A call never fails after its action.** Write and control tools make
    every lookup that can fail (`server.info`, `run.get`, `attention.list`)
    before they act. The run state read after a pause, resume, stop, close
    or review is reported as `state_error` in a successful result.
12. **`write` cannot end a live run.** `run.review complete` closes any
    live run, so `aether_mark_reviewed` completes only a run whose review is
    `ready` and otherwise points at `aether_close_run` under `control`.
13. **Read tools stay read-only.** `aether_list_agents` lists no models:
    `agent.models` needs Launch and may start the member's container. A
    launch's `model` is free text, as the agent names it.
14. **Finding a run takes one call.** `aether_list_runs` `query` matches
    title, task, workspace name and branch, and rows carry the workspace
    name and the last report; tools taking `run_id` or `swarm_id` strip the
    `run:` and `swarm:` prefixes `search` ids carry. `search` points chat
    use at `aether_list_runs`.
15. **Schemas are small.** Fixed values are enums (launch and worker mode
    `tui`/`headless`/`acp`, integrator mode `tui`/`acp`, period, decision,
    close outcome, review action, memory kind). The root
    `additionalProperties: false` is dropped and the handler refuses an
    unknown argument itself, naming the ones the tool takes; descriptions
    say when to call a tool, not what it returns.

## 2. Tools

| Tool | Scope | Methods |
| --- | --- | --- |
| `aether_list_workspaces` | read | `workspace.list` |
| `aether_list_agents` | read | `agent.list` |
| `aether_list_runs` | read | `workspace.list`, `run.list` (`mine` default true), `attention.list`, `workspace.timeline` |
| `aether_get_run` | read | `run.get`, `attention.list`, `workspace.timeline`, `run.pr.status` |
| `aether_run_history` | read | `run.acp.history` or `terminal.history`, `workspace.timeline` |
| `aether_needs_attention` | read | `attention.list` |
| `aether_activity_summary` | read | `insights.get` (scope mine), `workspace.timeline` |
| `aether_list_swarms`, `aether_get_swarm` | read | `mission.list` (paged), `mission.show`, `coord.messages.list` |
| `aether_run_changes` | read | `run.git.log`, `run.evidence.list`, `run.git.diff` |
| `aether_pr_status` | read | `run.pr.feedback` |
| `aether_overlaps` | read | `run.overlaps`, `run.list`, `workspace.list` |
| `aether_memory_search` | read | `memory.search` |
| `search`, `fetch` | read | `run.list`, `mission.list`, `run.get`, `workspace.timeline`, `mission.show` |
| `aether_launch_run` | write | `run.launch` |
| `aether_launch_swarm` | write | `mission.list`, `mission.create` |
| `aether_message_run` | write | `run.inject` |
| `aether_answer` | write | `mission.question.answer`, `run.room.post`, `run.input.respond` |
| `aether_pause_run`, `aether_resume_run` | write | `run.pause`, `run.resume` |
| `aether_mark_reviewed` | write | `run.get`, `run.review` |
| `aether_decide_approval` | control | `approval.decide`, `run.input.respond` |
| `aether_stop_run`, `aether_close_run` | control | `run.kill`, `run.close` |
| `aether_cancel_swarm` | control | `mission.cancel` |

Reports come from `workspace.timeline` entries of kind `report`: every
run's `aether_report` is projected there, while `coord.messages.list` holds
only the report messages a swarm worker sends its integrator, so a
standalone run's reports never appear in it. A run's report is found when it
is among its workspace's last 100 timeline entries (or the run's own, for
`aether_get_run`).

## 3. Limits

- `tools/list` with every scope is about 12.9 KB, under the 15 KB the unit
  test allows for ChatGPT's roughly 5000-token budget for tool definitions.
- Lists default to 20 rows and cap at 100, and report `total` and
  `truncated` when they cut rows ("20 of 57 runs."); needs-attention items
  cap at 30. Swarm listing follows `mission.list`'s cursor for up to 5 pages
  of 100 per workspace.
- Untrusted text is cut to 600 characters (reports, reasons, questions,
  request and approval text, memory notes), 200 for titles and a row's last
  report, 300 for each PR review or comment, 2000 for a conversation item
  and 12000 for a diff. `aether_pr_status` passes `run.pr.feedback`'s
  `truncated` on and qualifies its counts; `aether_run_changes` reports
  `run.git.diff`'s error as `diff_error`.
- The write limiter of 30 calls, then one every 2 seconds, is unchanged.

## 4. Tests

- `internal/mcpbridge/connector_test.go`: every tool through
  `RegisterConnectorTools` against a fake call: the list per scope, its
  hints, enums and size; text that ends with the result's JSON; results,
  summaries and routed params; an injection string that never leaves
  `untrusted`; truncation, paging, prefix stripping and duplicate guards;
  no action after a failed lookup and no failure after an action; refusals
  and hints.
- `internal/assistant/tools`: `Prepare` fills and guards `mission.create`.
- `internal/edge/edgetest/connector_tools_test.go`: through a real edge and
  server with OAuth, the tools per scope, launch replay and duplicate
  launches, a swarm as its caller, a message, a teammate's question listed
  and answered, a standalone run's report read from the persisted timeline,
  a write-only connector refused completing a running run, and a viewer's
  launch refused.
