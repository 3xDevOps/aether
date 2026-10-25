# Enhanced runs

A run normally shows the agent's own terminal UI. This is **Standard** mode. In
**Enhanced** mode, Aether drives the agent over the Agent Client Protocol (ACP),
a JSON protocol on the agent's stdin and stdout. The dashboard then shows the
agent's messages, tool calls, diffs and permission prompts as native controls
in a **Session** view, instead of a terminal. Read [Agents](harnesses.md) first
to install and sign in to an agent. Other terms are in the
[glossary](README.md#concepts).

## Which agents

| Agent | How it serves ACP | How it runs without asking |
| --- | --- | --- |
| `claude` | adapter `claude-agent-acp` | session mode `bypassPermissions` |
| `codex` | adapter `codex-acp` | session mode `agent-full-access` |
| `pi` | adapter `pi-acp` | pi never asks |
| `omp` | `omp acp --auto-approve` | the `--auto-approve` flag |
| `opencode` | `opencode acp` | `{"permission":"allow"}` in `OPENCODE_CONFIG_CONTENT` |
| `copilot` | `copilot --acp --allow-all` | the `--allow-all` flag, which the run cannot take back |
| `gemini` | `gemini --acp` | session mode `yolo` |
| `cursor` | `agent --force acp` | the `--force` flag |
| custom | the definition's `--acp` command | the definition's `--acp-mode` |

`antigravity` has no Enhanced mode ([Agents](harnesses.md#antigravity-cli)).

Like Standard, an Enhanced run acts without asking by default. You can switch
the session to a mode that asks (Claude Code, Codex and Gemini offer one).

`claude`, `codex` and `pi` need their adapter installed: `aether agent add
<name> --enhanced` installs the agent and its adapter
([Enhanced adapters](harnesses.md#enhanced-adapters)).

## Choose Enhanced

**Set up** on the **Agents** page, or onboarding's **Agent** step, shows both
modes before anything installs. Enhanced shows messages, tool activity, file
changes and approvals as controls. It runs through an adapter, so some
agent-specific commands and screens are missing, and it starts a few seconds
slower. Enhanced runs of Claude Code, OpenCode and omp report their cost to
[Insights](insights.md#cost) and budgets; Standard runs report none. Both modes
act without asking by default.

The dashboard remembers the mode you last chose for each agent, in **Set up**,
in **New run** or in the **Default mode** select on the **Agents** page, and
**New run** starts on it. For an agent with no remembered mode, **New run**
starts on Enhanced when the agent's Enhanced mode is installed, and on Standard
otherwise. **Set up** suggests Enhanced for Codex, oh-my-pi, OpenCode, Copilot,
Gemini and Cursor, and Standard for the rest. It suggests Standard for Claude
Code because its adapter runs on the Claude Agent SDK.

## Launch an Enhanced run

```sh
aether run "fix the flaky login test" --agent codex --mode enhanced
aether run "fix the flaky login test" --agent claude --mode enhanced --model opus
```

`--mode` takes `standard`, `enhanced` or `background` on `aether run` and
`aether template save`. On `aether swarm create`, `--mode standard|enhanced`
sets the integrator, and `--worker codex:enhanced` (or
`codex:enhanced:<model>`) allows Enhanced workers. The task is the session's
first prompt. An agent with no ACP command is refused:

```text
scheduler: harness "custom" has no command for mode "acp"
```

`--model` is set after the session opens. If the agent rejects it, the Session
view shows a **Model not set** error notice with the agent's error. A model you
pick later from the composer menu becomes the run's recorded model and survives a
reopen, a server restart and a switch to Standard. A model id Aether cannot
store (whitespace, a leading `-`, or over 128 bytes) shows a **Model not
recorded** notice.

The run's container is the same as a Standard run's, with the same image, home,
retention, Pause and Close. The agent's ACP server runs beside a login shell,
and the **Terminal** tab is that shell: use it to log the agent in or inspect
the checkout ([terminal.md](terminal.md)).

## The Session view

An Enhanced run opens on **Session**: one timeline, newest at the bottom.

- Your messages are filled blocks. The agent's replies and **Thought** traces
  render Markdown.
- Consecutive tool calls fold into one line ("Read 2 files, ran 1 command and
  edited 1 file"). Click it for one line per call, and click a call for its
  command, output or diff.
- The agent's plan, the files a turn changed (each opens **Changes** at that
  file), notices and a **Finished** line per turn sit in between. Messages
  between agents show inline. **Hide from Session**, under **Agent messages** in
  Details, turns them off.
- While a turn runs, one line says what the agent is doing. While it waits on you
  it reads "Waiting for your approval: <command>" or "Waiting for your answer".
  When a turn ends with no request, the run says **Waiting for your reply**, and
  the header's **Reply** focuses the composer.
- Scrolling near the top loads older items. A failed load shows the server's
  error with **Retry**.
- `Esc` in an empty composer moves focus to the timeline. `Esc` again leaves the
  run.

A success or failure report from the agent does not close the session: the
timeline shows **Done** or **Failed**, and you can keep sending messages.
**Close** ends the session and starts the container retention period (default
seven days).

The **Multiplayer** strip under the view switch shows who controls the run and
who watches, with **Take control** and **Release**. Control works as in a
Standard run ([Run control](terminal.md#run-control)).

### The composer

`Enter` sends and `Shift+Enter` adds a line. On a touch screen, Enter adds a line
and the button sends. The button changes with the turn:

| Button | When | What the message does |
| --- | --- | --- |
| **Send** | no turn is running | starts a turn |
| **Steer** | a turn runs and the agent supports steering | joins the running turn |
| **Queue** | a turn runs and you hold `Mod+Shift`, or the agent cannot steer | runs after this turn |
| **Interrupt** | a turn runs and the box is empty | stops the turn and cancels its requests |
| **Resume** | the run is paused | resumes it |

`Enter` never interrupts. `Mod+Shift+Enter` always queues. `/` lists the agent's
commands and `@` completes a path in the run's checkout. The menus under the box
set the agent's mode, model and effort. The footer shows context window use, such
as **43%**, and turns amber from 90%.

An unsent message stays with its run across pages, mode switches and reconnects.
Reloading the page discards it.

Steering, answering, interrupting and changing options need control of the run.
The owner's desktop tab takes it when it first shows Session and nobody holds
it. Without control you still get **Send**, and the message waits for the
controller as in a Standard run ([Messages and
notes](terminal.md#messages-and-notes)).

**Attach files** adds files to the composer, at most eight: images up to 8 MiB,
other files up to 64 MiB. Images go to the agent as image content and other files
as a path in the container. If the agent does not advertise image support, remove
the image before sending.

## Permissions and input

A new session starts in the agent's no-prompt setting, so a request arrives only
after you switch to a mode that asks, or from an agent whose own configuration
still asks. Each request marks the run **Needs you** and docks above the
composer, one at a time (`1/N`). The composer stays shut until you answer.
**Interrupt** stops the turn and cancels the request instead.

- Allow-once is the filled button. Keys `1` to `4` pick an option while the
  card has focus, and `1` never picks an allow-always option.
- A form question shows its fields. A link request shows the URL with **Copy
  link** and **Open**.
- An answered request stays in the timeline as one line, such as "Approved: run
  `go test`".
- A subagent's request is the run's request, answered the same way.
- From the CLI, `aether runs <run-id>` lists pending requests and `aether
  answer <run-id> <request-id> <option-id>` answers one: allow once or reject once,
  never always.
- Answering needs permission to steer the run and control of it. The first
  answer wins. Cancelling the turn answers every pending request `cancelled`.

A message sent during a turn waits for it to end (**Queued**), or joins it with
**Steer**. If the agent refuses it, the message reads `not_sent` with the agent's
error. If the connection closes first, the transcript shows `Message not
delivered: agent connection closed` and the message is not resent.

## Status

A turn start marks the run **working**. A turn end parks it as waiting, like a
Standard run whose agent reports its turns. The reason tells you how it ended:

| Reason | State | Cause |
| --- | --- | --- |
| `turn ended without a report` | `waiting` | The turn ended normally with no outcome report |
| `agent reported success` | `finished` | The run shows **Ready for review** ([failure-handling.md](failure-handling.md#review)) |
| `needs input: <question>` | `needs-you` | The agent ran `aether-internal report --outcome needs-input` and ended its turn |
| `blocked: <summary>` | `needs-you` | The agent reported an external blocker |
| the summary of a `waiting` report | `waiting-agents`, `waiting-ci` or `waiting-tools` | The agent waits on its own agents, CI or a process |
| `turn cancelled` | `waiting` | You or the agent cancelled the turn |
| `turn interrupted` | `needs-you` | The connection that ran the turn ended first, as after a server restart. Send a message to continue |
| `turn ended: <stop reason>` | `needs-you` | Any other ACP stop reason, such as `max_tokens` or `refusal`. Send a message to continue |
| `enhanced turn failed: <error>` | `failed` | The agent failed the prompt but stayed connected |
| `enhanced session ended: <cause>` | `disconnected` | The adapter exited. Reopening the session clears it |
| `enhanced session failed: <error>` | `disconnected` | The adapter could not start or open the session |

### Background work

Claude Code can end a turn while a background subagent, a workflow or a command
started with `run_in_background` keeps running. The Enhanced run stays working
until they finish, and when one finishes and Claude Code starts a follow-up turn
on its own, the Session view shows it as an autonomous turn. The run reason reads
`working in background: <n> tasks`, or `waiting-agents` or `waiting-tools`
between turns.

- A shell such as a dev server does not hold the run, because it may never end.
- Stop cancels an autonomous turn and parks the run with `turn cancelled`. The
  tasks keep running in Claude Code.
- A task that never ends keeps the run working until you press Stop, as long as
  the agent sends something within three times `--stall-threshold`.
- Other agents report no background tasks. Output they send with no prompt open
  is logged under their last turn and changes no state.

## Mail

Messages from other runs and people ([coordination.md](coordination.md)) reach an
Enhanced run without hooks. When mail arrives and the session has no turn running,
the server sends the agent one prompt telling it to run
`aether-internal inbox` and acknowledge the batch. Messages that arrive together
share one prompt, and each set of new unread messages starts at most one turn.

The prompt waits while a run is protected, a human holds a swarm worker, or the
run is paused or switching modes. Lifting protection, releasing control or
**Resume** offers the mail that arrived meanwhile at once; otherwise the server
retries a waiting prompt at least every 30 seconds. An Enhanced swarm
integrator is also prompted when a human answers its question, a worker
proposes a task or ends without a report, or a human cancels the swarm.

## Restarts and failures

A server restart, **Reopen** after Close, or a failed Close stops the old ACP
server and starts a fresh one. Aether then restores the agent's session. A turn
cut off by the restart ends with a **Turn interrupted** notice, its pending
permission requests are dropped, and the run parks with `turn interrupted`. Queued
messages read **Not sent**: send them again. Background tasks end with the old
adapter, and the log notes **Background work interrupted**. A session that cannot
be restored starts a new one and says `Started a new agent session`.

Restoration depends on the agent's own saved session and login. Check the
checkout before you retry interrupted work.

If the adapter fails to start or exits, the container and login shell stay
available. The log shows the real error and the end of the adapter's stderr, and
the run parks:

```text
enhanced session failed: open the agent session: acphost: session/new: {"code":-32000,"message":"Authentication required"}
enhanced session ended: the agent's ACP server exited with code 1; stderr: ...
```

Fix the cause (for a login, run the agent's login command in the Terminal tab).
Then choose **Retry Enhanced** in the Session view, or pause and resume the run,
to start a fresh ACP server. **Open in Standard** switches the run when the agent
can switch. Otherwise it starts a new Standard run with the same task and closes
this one without merging, after asking.

A turn that fails on the account's usage limit does not park with
`enhanced turn failed:`. The run moves to another account
([usage fallback](teams.md#usage-fallback)): its container is replaced by one on
the new account, the log gets a **Continued on `<B>`'s account** notice, and the
session is prompted to continue. The session must be restorable there, or the run
parks with `enhanced session failed: <error>`. With no account to move to, the run
parks with `Usage limit reached on <A>'s account; no fallback has usage left`.

## Switching a running agent

A running Standard or Enhanced run can move to the other mode in the same
container, keeping the agent's conversation:

```sh
aether run switch <run-id> --mode enhanced
aether run switch <run-id> --mode standard
```

In the dashboard, **More → Switch to Enhanced…** (or **Switch to Standard…**)
does the same after a confirmation. It needs permission to steer the run. While
someone controls the run only that session can switch, so the CLI switches only
a run nobody controls.

Only Claude Code and oh-my-pi can switch. Any other agent, and any agent whose
command a definition overrides, is refused:

```text
scheduler: this agent cannot move a running session between Standard and Enhanced: codex
```

A Background run cannot switch:

```text
scheduler: invalid run state transition: a background run cannot switch modes
```

A Standard run must have reported its session, which happens on its first turn,
or the switch is refused with `the agent has not reported its session yet; it does
on its first turn`. Switching to Enhanced needs the adapter installed, or it is
refused with `the agent's ACP server is not installed; install it with Enhanced
selected on the Agents page`. The dashboard menu says why it cannot switch:
**Enhanced adapter not installed · Set up**, or **Available after the agent's first
turn**.

The container, checkout, shell tabs, control lease and conversation carry over. A
turn in progress is interrupted, pending permission requests are dropped, the
login shell is replaced, and turns taken in Standard mode are not in the Session
timeline. A `Switched to Standard` or `Switched from Standard` notice marks the
gap. Anything the agent started that outlives it, such as a dev server, keeps
running.

A failed switch puts the previous mode back and shows the real error, for example:

```text
switch to Standard: the agent's terminal exited with code 1 as it started; the Terminal tab shows its output
```

While the switch runs, messages are refused and read `not_sent`. Pause, Close and
Kill wait for it, for about two minutes at most.

## Background runs

A Background run of `codex`, `omp`, `opencode`, `copilot`, `gemini` or `cursor`
uses the agent's ACP server when it is installed. Every other agent, and these
six without the server, use the agent's headless command line. Claude Code keeps
`claude -p`.

The Session view of such a run has no composer ("Background runs take no
input."). The task is the only prompt. A permission request that still arrives is
answered with `allow_once`, or the first `allow_*` option if there is none, and
never with `allow_always`.

When the turn ends, the run finishes like any Background run: commit, publish and
`completed` or `failed`, with the reason `agent exited; results committed` or
`agent exited 1: <cause>`. A swarm worker keeps its container and parks as
**Needs you** instead, unless the agent never got the task: then it fails and its
container frees the worker slot. Mail does not wake a Background run. A server
restart mid-turn resumes the session and prompts the agent to continue.

## Instructions and the session log

Every run gets the [agent instructions](coordination.md#agent-instructions) and the
[team memory brief](harnesses.md#agent-instructions-and-the-team-memory-brief).
Claude Code takes them as a system prompt. Other agents get them with the task, or
after the first message when there is no task.

The conversation is stored at `<data-dir>/transcripts/<run_id>.items.jsonl` with
no size or age limit, and is deleted with the run. Anything an agent or tool
prints, a secret included, can be in it
([privacy.md](privacy.md#remote-development-data)).

## Limits

- Enhanced mode does not isolate the agent from its tools. They share one
  container, so a build that runs out of memory can end the agent session.
- Codex subagents do not nest in the timeline yet.
- Only Claude Code reports background tasks and autonomous turns.
- Antigravity has no Enhanced mode.
- An interrupted turn is not resumed on its own. Send a message to continue.
- A mode switch discards the turn in progress.

Next: [Teams](teams.md).
