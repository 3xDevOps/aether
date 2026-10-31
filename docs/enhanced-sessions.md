# Enhanced sessions

A session normally shows the agent's own terminal UI. This is **Standard** mode. In
**Enhanced** mode, Aether drives the agent over the Agent Client Protocol (ACP),
a JSON protocol on the agent's stdin and stdout. The dashboard then shows the
agent's messages, tool calls, diffs and permission prompts as native controls
in a **Chat** view, instead of a terminal. Read [Agents](harnesses.md) first
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
| `copilot` | `copilot --acp --allow-all` | the `--allow-all` flag, which the session cannot take back |
| `gemini` | `gemini --acp` | session mode `yolo` |
| `cursor` | `agent --force acp` | the `--force` flag |
| custom | the definition's `--acp` command | the definition's `--acp-mode` |

`antigravity` has no Enhanced mode ([Agents](harnesses.md#antigravity-cli)).

Like Standard, an Enhanced session acts without asking by default. You can switch
the session to a mode that asks (Claude Code, Codex and Gemini offer one).

`claude`, `codex` and `pi` need their adapter installed: `aether agent add
<name> --enhanced` installs the agent and its adapter
([Enhanced adapters](harnesses.md#enhanced-adapters)).

## Choose Enhanced

**Set up** on the **Agents** page, or onboarding's **Agent** step, shows both
modes before anything installs. Enhanced shows messages, tool activity, file
changes and approvals as controls. It runs through an adapter, so some
agent-specific commands and screens are missing, and it starts a few seconds
slower. Enhanced sessions of Claude Code, OpenCode and omp report their cost to
[Insights](insights.md#cost) and budgets; Standard sessions report none. Both modes
act without asking by default.

The dashboard remembers the mode you last chose for each agent, in **Set up**,
in **New session** or in the **Default mode** select on the **Agents** page, and
**New session** starts on it. A launch from **New session** saves the
agent, mode and model on the server, so every device starts from them
([Saved launch choices](teams.md#saved-launch-choices)). For an agent with no remembered mode, **New session** starts on Enhanced when the agent's Enhanced mode is installed, and on Standard
otherwise; `aether session` and the connector do so
whenever no mode is named. **New session** lists Enhanced first and marks it
**Recommended**. Before an agent's Enhanced mode is installed, **Set up**
suggests Enhanced for Codex, oh-my-pi, OpenCode, Copilot, Gemini and Cursor,
and Standard for the rest; it suggests Standard for Claude Code because its
adapter runs on the Claude Agent SDK. Once it is installed, Set up starts on
Enhanced.

## Launch an Enhanced session

```sh
aether session "fix the flaky login test" --agent codex --mode enhanced
aether session "fix the flaky login test" --agent claude --mode enhanced --model opus --effort high
```

`--mode` takes `standard`, `enhanced` or `background` on `aether session` and
`aether template save`. The task is the session's
first prompt. An agent with no ACP command is refused:

```text
scheduler: harness "custom" has no command for mode "acp"
```

`--model` is set after the session opens. If the agent rejects it, the Session
view shows a **Model not set** error notice with the agent's error. A model you
pick later from the composer menu becomes the session's recorded model and survives a
reopen, a server restart and a switch to Standard. A model id Aether cannot
store (whitespace, a leading `-`, or over 128 bytes) shows a **Model not
recorded** notice.

`--effort` is set after the model, through the agent's `thought_level` session
option, so the first turn already runs at it. Copilot reads
`--reasoning-effort` on `copilot --acp` itself, so its Enhanced sessions get the
flag instead. If the agent rejects the effort or advertises no effort option,
the Chat view shows an **Effort not set** error notice, and a Background session
driven over ACP fails rather than run at the default. An effort you pick later
from the composer menu becomes the session's recorded effort and survives a reopen
and a switch to Standard, like the model.

The session's container is the same as a Standard session's, with the same image, home,
retention, Pause and Close. The agent's ACP server runs beside a login shell,
and the **Terminal** tab is that shell: use it to log the agent in or inspect
the checkout ([terminal.md](terminal.md)).

## The Chat view

An Enhanced session opens on **Chat**: one timeline, newest at the bottom.

- Your messages are bubbles on the right. Everything the agent writes to
  you renders Markdown under them; its last message is the answer, with the
  turn's outcome ("Finished in 12s") and **Copy the reply**.
- The work between two of its messages, its thoughts, tool calls, plan,
  subagents and answered requests, folds into one header ("Read 2 files, ran
  1 command and edited 1 file"). Click it for one step per call, and click a
  call for its command, output, diff, or input and result. Files the agent
  read are chips that open in **Files**.
- While the agent works, the newest header stays open and says what it is
  doing, with the seconds since it began. While it waits on you it reads
  "Waiting for your approval: <command>" or "Waiting for your answer". Once
  the agent writes again, or the turn ends, the steps fold away, unless you
  opened or closed them yourself or a subagent still runs. When a turn ends
  with no request, the session says **Waiting for your reply**, and the header's
  **Reply** focuses the composer.
- Notices, a turn that was cancelled or stopped, and messages between agents
  show inline. **Hide from Session**, under **Agent messages** in Details,
  turns agent messages off.
- Scrolling near the top loads older items. A failed load shows the server's
  error with **Retry**.
- `Esc` in an empty composer moves focus to the timeline. `Esc` again leaves the
  session.

A success or failure report from the agent does not close the session: the
timeline shows **Done** or **Failed**, and you can keep sending messages.
**Close** ends the session and starts the container retention period (default
seven days).

The **Multiplayer** strip under the view switch shows who controls the session and
who watches, with **Take control** and **Release**. Control works as in a
Standard session ([Session control](terminal.md#session-control)).

### The composer

`Enter` sends and `Shift+Enter` adds a line. On a touch screen, Enter adds a line
and the button sends. The button changes with the turn:

| Button | When | What the message does |
| --- | --- | --- |
| **Send** | no turn is running | starts a turn |
| **Steer** | a turn runs and the agent supports steering | joins the running turn |
| **Queue** | a turn runs and you hold `Mod+Shift`, or the agent cannot steer | runs after this turn |
| **Interrupt** | a turn runs and the box is empty | stops the turn and cancels its requests |
| **Resume** | the session is paused | resumes it |

`Enter` never interrupts. `Mod+Shift+Enter` always queues. `/` lists the agent's
commands and `@` completes a path in the session's checkout. The menus under the box
set the agent's mode, model and effort. The footer shows context window use, such
as **43%**, and turns amber from 90%; click it for the breakdown, and for
**Compact** when the agent has a `/compact` command.

An unsent message stays with its session across pages, mode switches and reconnects.
Reloading the page discards it.

Steering, answering, interrupting and changing options need control of the session.
The owner's desktop tab takes it when it first shows Chat and nobody holds
it. Without control you still get **Send**, and the message waits for the
controller as in a Standard session ([Messages and
notes](terminal.md#messages-and-notes)).

**Attach files** adds files to the composer, at most eight: images up to 8 MiB,
other files up to 64 MiB. Images go to the agent as image content and other files
as a path in the container. If the agent does not advertise image support, remove
the image before sending.

## Permissions and input

A new session starts in the agent's no-prompt setting, so a request arrives only
after you switch to a mode that asks, or from an agent whose own configuration
still asks. Each request marks the session **Needs you** and docks above the
composer, one at a time (`1 of N`). The composer stays shut until you answer.
**Interrupt** stops the turn and cancels the request instead.

- The card names what the agent wants ("Claude Code wants to run a command")
  and shows the command, or the files it changes with their diffs.
- When the agent offers allow once and allow always, **How long** chooses
  between **Just once** (the default) and **Always**, and **Allow** answers
  with that option; **Reject** sits beside it. Other option sets keep the
  agent's own buttons, allow once filled.
- Keys `1` to `4` pick an option while the card has focus, shown on each
  button, and `1` never picks an allow-always option.
- A form question shows its fields. A link request shows the URL with **Copy
  link** and **Open**.
- An answered request stays in the timeline as one line, such as "Allowed once
  · run `go test`".
- A subagent's request is the session's request, answered the same way.
- From the CLI, `aether sessions <session-id>` lists pending requests and `aether
  answer <session-id> <request-id> <option-id>` answers one: allow once or reject once,
  never always.
- Answering needs permission to steer the session and control of it. The first
  answer wins. Cancelling the turn answers every pending request `cancelled`.
- A Default session's `sudo` command is a request too, in Standard sessions as well. It
  shows under **Needs you** in the session's details, not in the dock: **Approve**
  runs it once as root, **Deny** refuses it. Only the session's owner or an admin
  may approve, and neither needs control
  ([Sudo in a Default session](security.md#sudo-in-a-default-session)).

A message sent during a turn waits for it to end (**Queued**), or joins it with
**Steer**. If the agent refuses it, the message reads `not_sent` with the agent's
error. If the connection closes first, the transcript shows `Message not
delivered: agent connection closed` and the message is not resent.

## Status

A turn start marks the session **working**. A turn end parks it as waiting, like a
Standard session whose agent reports its turns. The reason tells you how it ended:

| Reason | State | Cause |
| --- | --- | --- |
| `turn ended without a report` | `waiting` | The turn ended normally with no outcome report |
| `agent reported success` | `finished` | The session shows **Ready for review** ([failure-handling.md](failure-handling.md#review)) |
| `needs input: <question>` | `needs-you` | The agent ran `aether-internal report --outcome needs-input` and ended its turn |
| `blocked: <summary>` | `needs-you` | The agent reported an external blocker |
| the summary of a `waiting` report | `waiting-agents`, `waiting-ci` or `waiting-tools` | The agent waits on its own agents, CI or a process |
| the summary of an `answered` report | `answered` | The agent replied with nothing pending. It never needs you |
| `turn cancelled` | `waiting` | You or the agent cancelled the turn |
| `turn interrupted` | `needs-you` | The connection that ran the turn ended first, as after a server restart. Send a message to continue |
| `turn ended: <stop reason>` | `needs-you` | Any other ACP stop reason, such as `max_tokens` or `refusal`. Send a message to continue |
| `enhanced turn failed: <error>` | `failed` | The agent failed the prompt but stayed connected |
| `enhanced session ended: <cause>` | `disconnected` | The adapter exited. Reopening the session clears it |
| `enhanced session failed: <error>` | `disconnected` | The adapter could not start or open the session, or the agent lost its session twice within 5 minutes |

### Background work

Claude Code can end a turn while a background subagent, a workflow or a command
started with `run_in_background` keeps running. The Enhanced session stays working
until they finish, and when one finishes and Claude Code starts a follow-up turn
on its own, the Chat view shows it as an autonomous turn. The session reason reads
`working in background: <n> tasks`, or `waiting-agents` or `waiting-tools`
between turns.

- A shell such as a dev server does not hold the session, because it may never end.
- Stop cancels an autonomous turn and parks the session with `turn cancelled`. The
  tasks keep running in Claude Code.
- A task that never ends keeps the session working until you press Stop, as long as
  the agent sends something within three times `--stall-threshold`.
- Other agents report no background tasks. Output they send with no prompt open
  is logged under their last turn and changes no state.

## Mail

Messages from other sessions and people ([coordination.md](coordination.md)) reach an
Enhanced session without hooks. When mail arrives and the session has no turn running,
the server sends the agent one prompt telling it to run
`aether-internal inbox` and acknowledge the batch, ending with
`End this turn with exactly one /usr/local/bin/aether-internal report as your last action.`
A message steered into a running turn carries no such line. Messages that arrive together
share one prompt, and each set of new unread messages starts at most one turn,
even a turn the agent refuses. A session that opens afterwards gets the prompt
again.

### Report reminder

A turn that ends normally (`end_turn`) without an outcome report gets one more
prompt from the server:
`Report your state with aether-internal report (answered, waiting, needs-input, blocked, success or failure) before ending the turn.`,
naming only the outcomes the session's role may use. It appears in the Session
view as a message. The turn it starts is never
reminded, so an agent that still does not report parks with
`turn ended without a report`. A subsession's parent then gets the
last message of the reminded turn, not the reminder's.

No reminder follows a turn that was cancelled, interrupted, failed or stopped
on another stop reason, or one that background tasks or a request waiting on
you hold. A message you sent during the turn starts the next turn instead. A
session a member controls gets none: the next prompt is theirs. The reminder also
waits on the same admission as a mail prompt: a protected session, a subsession
a member controls, and a paused or switching session get none. Background
sessions get none either: their turn end finishes the session.

The reminder is a prompt, not a hook, so it works the same for every agent.
Claude Code's ACP adapter does forward `_meta.claudeCode.options.settings`,
where a Stop hook could live, but that setting replaces the adapter's own
settings and reaches only Claude Code.

The prompt waits while a session is protected, a member controls a subsession, or the
session is paused or switching modes. Lifting protection, releasing control or
**Resume** offers the mail that arrived meanwhile at once; otherwise the server
retries a waiting prompt at least every 30 seconds.

## Restarts and failures

The agent's ACP server runs under a keeper: a process in the session container,
started from the staged server binary as `aether-internal acp-keeper`, that
owns the adapter's stdin and stdout and serves them on a socket in
`/run/aether-acp/`. The server attaches to the keeper with a short-lived
`aether-internal acp-keeper attach` exec and a token only the server holds.
The keeper and that exec run as root, out of the agent's reach; the adapter
runs as the session's user, as before ([security](security.md#permission-tiers)).

A server restart or update (`systemctl restart aether-server`,
`aether server update`, a boot trial or a rollback) leaves the keeper and the
agent running. Terminals started with `aether-internal terminal` run under a
keeper of their own and keep their output, input and exit status through it
([terminal.md](terminal.md#reconnects-and-server-updates)). The next server
process reattaches and adopts the conversation: the turn in flight keeps
streaming, an open permission request or question stays open under the same
id, background tasks and commands the agent's shell tool started keep
running, and nothing is sent to the agent twice. A message queued behind the
turn is sent again once the server is back, unless it already reached the
agent; one queued for a paused session waits until the session resumes. An
answer, cancel or message sent while the new server is still recovering
sessions waits for recovery, up to 30 seconds, instead of failing with `the
Enhanced agent is not running`. The log records no notice for an adoption.
A stopped server detaches cleanly. A killed one can, rarely, record twice the
agent output it logged in the instant before it died. A server killed
after it recorded a new turn but before the turn's prompt reached the keeper
ends that turn with **Turn interrupted**: `Aether's server restarted before
this turn's prompt reached the agent. Send it again.`

The server cannot reattach when the keeper or the agent exited, when the agent
wrote more than 32 MiB while no server read it, when the keeper speaks another
protocol version, or when the server stopped before the agent's conversation
opened. It then logs **Agent restarted** with the reason, stops the keeper and
the agent, and restores the conversation in a fresh one, as **Reopen** after
Close, a failed Close and **Retry Enhanced** always do. A turn cut off that way
ends with a **Turn interrupted** notice, its pending permission requests are
dropped, and the session parks with `turn interrupted`. Queued messages read
**Not sent**: send them again. Background tasks end with the old adapter,
including commands the agent's own shell tool left running, and the log notes
**Background work interrupted**. Commands in a terminal started with
`aether-internal terminal` keep running, and the terminal stays usable
([terminal.md](terminal.md#reconnects-and-server-updates)). A conversation that
cannot be restored starts a new one and says `Started a new agent
conversation`. When another process attaches to the keeper, which only a
second server on the same data directory or root in the container can do, the
server logs **Agent connection taken over** and restores the conversation the
same way.

A session container keeps the server binary it was created with. A session
whose container a server without keepers created runs its adapter without
one, and every restart restores its conversation as above until the container
is recreated. **Reopen**, **Retry Enhanced**, Pause and resume reuse the
container; a new session, or an account switch at a usage limit, gets a new
container with a keeper.

Restoration depends on the agent's own saved conversation and login. Check the
checkout before you retry interrupted work.

Claude's adapter keeps running when its Claude Code process dies, but drops the
conversation and answers every later prompt with `Session not found`. Aether shows
that prompt's **Prompt failed** notice, then **Agent conversation lost**, and
restores the conversation in a fresh adapter as above. Send the failed message
again. If the agent loses the conversation again within 5 minutes, Aether stops the
adapter and the session parks with
`enhanced session failed: the agent lost its conversation again within 5 minutes: ...`.

If the adapter fails to start or exits, the container and login shell stay
available. The log shows the real error and the end of the adapter's stderr, and
the session parks:

```text
enhanced session failed: open the agent conversation: acphost: session/new: {"code":-32000,"message":"Authentication required"}
enhanced session ended: the agent's ACP server exited with code 1; stderr: ...
```

Fix the cause (for a login, run the agent's login command in the Terminal tab).
Then choose **Retry Enhanced** in the Chat view, or pause and resume the session,
to start a fresh ACP server. **Open in Standard** switches the session when the agent
can switch. Otherwise it starts a new Standard session with the same task and closes
this one without merging, after asking.

A turn that fails on the account's usage limit does not park with
`enhanced turn failed:`. The session moves to another account
([usage fallback](teams.md#usage-fallback)): its container is replaced by one on
the new account, the log gets a **Continued on `<B>`'s account** notice, and the
session is prompted to continue. The session must be restorable there, or the session
parks with `enhanced session failed: <error>`. With no account to move to, the session
parks with a `Usage limit reached on <A>'s account; ...` reason that says why
([Usage limit mid-session](failure-handling.md#usage-limit-mid-session)).

## Switching a running agent

A running Standard or Enhanced session can move to the other mode in the same
container, keeping the agent's conversation:

```sh
aether session switch <session-id> --mode enhanced
aether session switch <session-id> --mode standard
```

In the dashboard, **More → Switch to Enhanced…** (or **Switch to Standard…**)
does the same after a confirmation. It needs permission to steer the session. While
someone controls the session only that session can switch, so the CLI switches only
a session nobody controls.

Only Claude Code and oh-my-pi can switch. Any other agent, and any agent whose
command a definition overrides, is refused:

```text
scheduler: this agent cannot move a running session between Standard and Enhanced: codex
```

A Background session cannot switch:

```text
scheduler: invalid session state transition: a background session cannot switch modes
```

A Standard session must have reported its session, which happens on its first turn,
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

## Background sessions

A Background session of `codex`, `omp`, `opencode`, `copilot`, `gemini` or `cursor`
uses the agent's ACP server when it is installed. Every other agent, and these
six without the server, use the agent's headless command line. Claude Code keeps
`claude -p`.

The Chat view of such a session has no composer ("Background sessions take no
input."). The task is the only prompt. A permission request that still arrives is
answered with `allow_once`, or the first `allow_*` option if there is none, and
never with `allow_always`.

When the turn ends, the session finishes like any Background session: commit, publish and
`completed` or `failed`, with the reason `agent exited; results committed` or
`agent exited 1: <cause>`. Mail does not wake a Background session. A server
restart mid-turn resumes the session and prompts the agent to continue.

## Instructions and the session log

Every session gets the [agent instructions](coordination.md#agent-instructions) and the
[team memory brief](harnesses.md#agent-instructions-and-the-team-memory-brief).
Claude Code takes them as a system prompt. Other agents get them with the task, or
after the first message when there is no task, so compaction can drop them; the
[report reminder](#report-reminder) repeats the reporting rule when it matters.
The session's timeline records `session instructions: system prompt` or
`session instructions: first message` when the session opens.

The conversation is stored at `<data-dir>/transcripts/<run_id>.items.jsonl` with
no size or age limit, and is deleted with the session. Anything an agent or tool
prints, a secret included, can be in it
([privacy.md](privacy.md#remote-development-data)).

## Limits

- Enhanced mode does not isolate the agent from its tools. They share one
  container, so a build that runs out of memory can end the agent.
- Codex subagents do not nest in the timeline yet.
- Only Claude Code reports background tasks and autonomous turns.
- Antigravity has no Enhanced mode.
- An interrupted turn is not resumed on its own. Send a message to continue.
- A mode switch discards the turn in progress.

Next: [Teams](teams.md).
