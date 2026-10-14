# Aether assistant

**Status:** In progress.

**Goal:** A member asks Aether in plain language — "what's the status of my
runs?", "launch a run to fix the flaky login test", "install Rust in my
environment" — and an agent does it with exactly that member's authority,
asking before anything destructive. It works for a new member as soon as one
agent is set up, before they have a workspace.

A **run** is one agent working in one container on a workspace's code. A
member's **environment** is their own container, opened with
`aether terminal`, with their home mounted at `$HOME`; their agent logins live
in that home. An **Enhanced** session is an agent CLI driven over ACP (the
Agent Client Protocol) and rendered in the dashboard.

## 1. Decisions

1. **The assistant is an Enhanced session of the member's own Claude Code or
   Codex, not a run.** A run needs a workspace and a checkout; a new member
   has neither. The session runs in the member's environment container, so it
   uses their login and account, and its shell changes the environment the
   member actually uses.
2. **One assistant per member**, persistent across page loads and server
   restarts (the harness session is resumed). **New conversation** discards
   it.
3. **Aether operations go through a member-scoped tool socket**, never
   through the agent's own credentials. The server binds the socket to the
   member; every call runs through `sshd.Server.Local(member).Call`, so the
   assistant can do exactly what the member can do and nothing more. No
   member id is accepted on the wire.
4. **Three tool tiers, enforced by the server:**
   - *Read* (status, runs, workspaces, agents, usage, docs): free.
   - *Reversible* (launch a run, message a run, pause, resume): free; the
     resulting events carry the member as actor.
   - *Confirmed* (stop a run, save the environment): the tool call blocks
     until the member approves a confirmation card naming the exact action.
     Denial, a 10-minute timeout or a closed session fails the call.
5. **Shell work uses the harness's own tools under its own permission
   prompts.** The session starts in the harness's asking mode (Claude Code
   `default`, Codex `auto`), never the bypass mode runs use; each prompt
   renders as a request card the member answers.
6. **Text from runs is data.** Transcripts and run output the assistant
   reads are untrusted; the instructions say so, and the confirmed tier holds
   even if a transcript tries to steer the assistant.
7. **Out of the box:** the panel is reachable from the top bar, the command
   palette and empty states. With no ready agent it shows agent setup in
   place (install, sign in), then the chat.

## 2. Server

### Session host

`internal/assistant.Service`, one session per member:

- Start: `EnsureTerminal(member)`, then `StartExecPipe` in the terminal
  container running the agent's ACP adapter (the same argv an Enhanced run
  uses), working directory `$HOME/.aether/assistant`, environment
  `AETHER_ASSISTANT=1`, `NO_BROWSER=1`. `acphost.Start` hosts the session with
  item log `<data>/assistant/<member>/items.jsonl` and one MCP server:
  `/opt/aether/aether-server mcp --assistant`.
- Before each start the server writes `AGENTS.md` and `CLAUDE.md` (same
  embedded text) into `<home>/.aether/assistant/`.
- The harness session id and agent are stored per member (table
  `assistant_sessions`) and resumed with `session/resume` or `session/load`.
- If the terminal stops or is reset, the adapter dies with it; the next
  prompt starts it again and resumes.
- Agent choice: the requested agent, else the stored one, else the first of
  `claude`, `codex` whose agent and Enhanced adapter are installed and whose
  login is found.

### Tool socket

`<data>/assistant/<member>/sock/assistant.sock`, the directory bind-mounted
read-only at `/run/aether-assistant` in the member's terminal container. The
server binary is mounted read-only at `/opt/aether/aether-server`. Both are
creation-time mounts; a terminal created by an older server reports
`needs_restart`.

Line-delimited JSON-RPC, the same framing as the run coordination socket
(`coordtransport.Call`). Allowed methods:

| Tier | Method |
| --- | --- |
| Read | `run.list`, `run.get`, `run.acp.history`, `workspace.list`, `workspace.get`, `agent.list`, `account.usage`, `terminal.status`, `assistant.docs` |
| Reversible | `run.launch`, `run.inject`, `run.pause`, `run.resume` |
| Confirmed | `run.kill`, `env.save` |

`assistant.docs {topic}` returns an embedded guide from `docs/`, or the topic
list when `topic` is empty.

### Control methods (member's dashboard and CLI)

| Method | Params | Result |
| --- | --- | --- |
| `assistant.status` | `{}` | `AssistantStatus` |
| `assistant.start` | `{agent?, restart_environment?}` | `AssistantStatus` |
| `assistant.prompt` | `{text, idempotency_key}` | `{}` |
| `assistant.cancel` | `{}` | `{}` |
| `assistant.answer` | `{request_id, option_id, values?}` | `{}` |
| `assistant.confirm` | `{confirmation_id, approve}` | `{}` |
| `assistant.reset` | `{}` | `AssistantStatus` |
| `assistant.history` | `{before_seq?, limit?}` | like `run.acp.history` |

`AssistantStatus`: `state` (`setup`, `needs_restart`, `idle`, `starting`,
`running`, `failed`), `agent`, `agents` (each `{name, display_name, ready,
reason}`), `error`, `confirmations`.

`AssistantConfirmation`: `{id, method, summary, params, created_at,
expires_at}`. `summary` is one plain sentence, for example
`Stop run run-8x2… ("Fix flaky login test")`.

### Stream

`/ws/assistant` (and the `aether-assistant` SSH subsystem): header
`{after_seq}`, then an `ACPStreamResponse` ack, then frames. A frame is an
`ACPFrame`, or `{confirmations: [...]}` whenever the pending set changes.

## 3. Dashboard

- **Assistant panel:** a right-hand `aside` beside `main` on desktop (width
  persisted), a side sheet on phones. Header: agent, state, **New
  conversation**. Body: the session transcript, reusing the run session's
  row renderer. Footer: composer with Send and Stop.
- **Request cards** for harness permission prompts and questions, and
  **confirmation cards** for confirmed tools: the summary, the exact action,
  **Approve** and **Decline**.
- **Setup state:** no ready agent → the agents setup flow in the panel;
  `needs_restart` → one button that restarts the environment, with the
  consequence stated.
- **Entry points:** top bar button, palette command **Ask Aether**, a
  keybinding, and a secondary action on the board's empty states and the
  onboarding steps.

## 4. Phases

1. Server: session host, control methods, stream, persistence.
2. Tool socket and MCP tools, embedded docs and instructions.
3. Dashboard panel, entry points and onboarding.
4. Later: account sharing for the assistant, an "assistant" actor tag on
   events, digests from the event stream, handing large requests to a swarm.
