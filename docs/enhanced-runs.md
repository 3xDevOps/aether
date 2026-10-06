# Enhanced runs

A **run** normally hosts the agent's own terminal UI (Standard mode, wire name
`tui`). An **enhanced run** (wire name `acp`) drives the agent over the
**Agent Client Protocol** (ACP): JSON-RPC on the agent's stdio, which carries
its messages, thoughts, tool calls with diffs and command output, plans,
permission requests, modes and config options. The server is the agent's
only ACP client. It records everything the agent sends in the run's
**session item log** and serves that log to every dashboard that opens the
run.

## Which agents

| Agent | How it serves ACP | Starts in mode |
| --- | --- | --- |
| `claude` | adapter `claude-agent-acp` | `auto` |
| `codex` | adapter `codex-acp` | `agent` (Auto review) |
| `pi` | adapter `pi-acp` | the agent's default |
| `omp` | `omp acp` | the agent's default |
| `opencode` | `opencode acp` | the agent's default |
| custom | the definition's `ACPArgs` / `acp_args` / `--acp` | the agent's default |

An adapter installs with the agent:

```sh
aether agent add codex --enhanced
```

[harnesses.md](harnesses.md#enhanced-mode-adapters) covers the pinned
versions, how updates reach them, and `agent.list`'s `enhanced_installed`.

## Launch one

```sh
aether run "fix the flaky login test" --agent codex --mode enhanced
```

`--mode` takes `standard`, `enhanced` or `background` (wire names `tui`,
`acp`, `headless`) on `aether run`, `aether template save`, and
`aether swarm create`, whose `--mode` sets the integrator and whose
`--worker codex:enhanced` sets a worker. The task, when given, is the
session's first prompt. An agent with no ACP command is refused:

```
scheduler: harness "custom" has no command for mode "acp"
```

## What runs in the container

The container is the one a Standard run gets: the same image, mounts, home,
retention, Pause and Close. Container PID 1 is the run supervisor; its child
is a login shell instead of the agent. The agent's ACP server runs beside it
as a managed exec in `/workspace` with `NO_BROWSER=1` added to the run's
environment, its stdio owned by the server. When coordination is on, the
session is given the `aether` MCP server (`/opt/aether/aether-server mcp`),
so the agent has the mailbox tools without vendor hook files.

The run's **Terminal** tab is that login shell, not the agent: use it to log
the agent in, inspect the checkout, or run commands next to the agent
([terminal.md](terminal.md)).

## Permissions and input

The session starts in the agent's automatic mode: routine actions proceed
and risky ones ask. Each question becomes pending input on the run (the run
shows **Needs you**) and a `request` item in the log. Answer it from the
dashboard, or with `run.input.answer` and the option id the agent offered.
The first answer wins; a later one gets `CodeConflict` with
`data.reason: "already_answered"`. Cancelling the turn (`run.acp.cancel`)
answers every pending request `cancelled`. `run.acp.set_option` changes a
config option the agent lists, such as its mode or model.

Answering, cancelling and changing options need **Steer** and the run's
**control lease**, the same authority as typing into a terminal. A dashboard
takes the lease on `/ws/acp/<run_id>` with the attach control fields
([local-gateway.md](local-gateway.md#get-wsacprun_id)).

Messages go through `run.inject` and the Run Room as for any run. A message
sent while no turn runs starts one (`outcome: "sent"`). During a turn it waits
for the turn to end (`queued`) unless `steer: true` is set and the agent
advertised steering when the session opened, which adds it to the running
turn (`injected`).

## Status

The session reports **working** when a turn starts and **idle** when it ends,
so an idle enhanced run shows Needs you exactly like a Standard run whose
agent reports its turns. Any frame from the agent counts as activity for
stall detection.

## Restarts and failures

Every reattach (a server restart, Reopen after Close, a failed Close) stops
the previous ACP server and starts a fresh one, then restores the agent
session with `session/resume`, or `session/load` with the replayed history
dropped because the log already holds it. The session id is stored in the
run (`harness_session_id`). A turn cut off by a restart ends with a **Turn
interrupted** notice. A session that cannot be restored starts a new one
and says so in the log.

An adapter that fails to start, or exits on its own, never fails the run.
The shell stays up, a notice with the real error and the end of the
adapter's stderr goes to the log, and the run parks at `needs-attention`:

```
enhanced session failed: open the agent session: acphost: session/new: {"code":-32000,"message":"Authentication required"}
enhanced session ended: the agent's ACP server exited with code 1; stderr: ...
```

Fix the cause (for a login, run the agent's login command in the Terminal
tab), then Close and Reopen the run to start a fresh session. A paused run's
adapter is frozen with its container; one that could not start while the
container was paused starts on Resume. See
[failure-handling.md](failure-handling.md#enhanced-runs).

## The session item log

Each enhanced run keeps its log at
`<data-dir>/transcripts/<run_id>.items.jsonl`, one JSON item per line, beside
the run's terminal transcripts and deleted with them. It holds prompts,
the agent's messages and thoughts, tool call inputs, output and diffs,
permission requests and answers, and notices. One item is capped at 256 KiB
and one run's log at 64 MiB, past which only requests, turn boundaries and
notices are recorded. Anything an agent or a tool prints, a secret included,
can be in it; [privacy.md](privacy.md#remote-development-data) covers who can
read it.
