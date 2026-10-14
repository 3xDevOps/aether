# Assistant

The assistant is a chat panel in the dashboard where you ask Aether for
things in plain language: "what's the status of my runs?", "launch a run to
fix the flaky login test", "install Rust in my environment". It acts with
your permissions and asks before it launches, messages or stops a run, or
saves your environment.

It is not a run. It is an [Enhanced](enhanced-runs.md) session of your own
Claude Code or Codex, started in your [environment](terminal.md) container
(the persistent container `aether terminal` opens, with your home at `$HOME`).
It uses the login in that home, so its usage counts against your own
subscription. It works before you have a workspace, so a new member can use it
as soon as one agent is set up. The server writes its instructions to
`~/.aether/assistant/AGENTS.md` and `CLAUDE.md` and starts the agent there.

## Open it

Every entry point is offered only when the server supports the assistant.

- The **Assistant** button in the sidebar beside **New run**, or in the top bar while the sidebar is hidden, or in the phone toolbar. Every entry point is hidden from members who cannot launch runs, because the assistant needs that capability. A dot on it means an action waits
  for your approval.
- The command palette entry **Ask Aether**.
- `Ctrl+Shift+Space` (`⌘Shift+Space` on macOS) opens and closes the panel. It is
  inactive while a text field, an editor or a terminal has focus.
- **Ask Aether** on the board when it is empty, and **Stuck? Ask Aether** in
  the onboarding steps.

On desktop the panel sits to the right of the board and can be resized; on a
phone it is a side sheet.

## First use

With no ready agent the panel shows **Needs setup** and lists Claude Code and
Codex. An agent is ready when all three hold:

- the agent is installed in your home;
- its Enhanced adapter is installed;
- its login file was found.

Each row that is not ready states the missing piece (`Not installed`,
`Enhanced adapter not installed`, `No sign-in found`) and
has **Set up**, which runs the same flow as the **Agents** page. From the CLI:

```sh
aether agent add claude --enhanced
aether terminal
```

`aether agent add` installs the agent and, with `--enhanced`, its adapter.
Then sign in to the agent inside `aether terminal`. See
[quickstart.md](quickstart.md#5-set-up-your-agent) for the full flow. The
panel updates when setup finishes.

With no conversation yet, the assistant uses the first ready of Claude Code and
Codex. Afterwards it keeps the agent the conversation began with and never
switches unasked, because another agent cannot continue the conversation. If
that agent stops being ready, a prompt fails with, for example, `Claude Code is
not ready: No sign-in found; sign in again or start with another agent`. Sign
in again, or start with the other agent explicitly (`assistant.start` with
`agent`), which clears the conversation.

## Restart the environment

An environment container created before the assistant existed lacks the two
read-only mounts it needs (`/run/aether-assistant` and
`/opt/aether/aether-server`). Mounts can only be added when a container is
created, so the panel shows **Restart your environment** with **Restart
environment**. Restarting closes any shells open in it. Files in `$HOME` are
kept; anything installed outside `$HOME` is lost unless you
[saved the environment](environments.md#save-the-environment). Over the
API the same step is `assistant.start` with `restart_environment: true`.

## What it can do

The assistant calls Aether through tools served on a socket bound to you. Each
tool maps to a control method, so it can do what you can do and nothing more.
`aether_docs` reads these guides, so it can explain Aether features from the
documentation of the server you run.

| Tier | Tool | Method | Effect |
| --- | --- | --- | --- |
| Read | `aether_list_runs` | `run.list` | Runs you can see, optionally only yours (`mine`), active only or per workspace |
| Read | `aether_get_run` | `run.get` | One run's record |
| Read | `aether_run_history` | `run.acp.history` | A page of an Enhanced run's conversation |
| Read | `aether_list_workspaces` | `workspace.list` | Workspaces you can launch in |
| Read | `aether_get_workspace` | `workspace.get` | One workspace |
| Read | `aether_list_agents` | `agent.list` | Agents, logins and Enhanced support |
| Read | `aether_usage` | `account.usage` | Remaining agent quota |
| Read | `aether_environment_status` | `terminal.status` | Your environment's state and saved image |
| Read | `aether_docs` | `assistant.docs` | Aether's own guides |
| Reversible | `aether_pause_run` | `run.pause` | Pauses a run |
| Reversible | `aether_resume_run` | `run.resume` | Resumes a run |
| Confirmed | `aether_launch_run` | `run.launch` | Starts a run, naming the agent, workspace and task |
| Confirmed | `aether_message_run` | `run.inject` | Sends a message to a run's agent, naming the run and its owner |
| Confirmed | `aether_stop_run` | `run.kill` | Stops a run |
| Confirmed | `aether_save_environment` | `env.save` | Saves your environment as the image new runs and terminals start from |

Read tools, pause and resume run immediately; the events name you as the actor.
Confirmed tools wait for your approval, including launching a run and
messaging one.

Environment changes, such as installing a toolchain, are not an Aether tool.
The assistant runs them in its own shell in your environment container, then
offers `aether_save_environment`.

## Confirmations

A confirmed call blocks until you answer. The panel shows a card, **Approve
this action?**, with a one-sentence summary, the method, its exact parameters
and when it expires. For example:

```text
Launch claude (acp) in api: "Fix the flaky login test"
Stop run run-8x2... ("Fix flaky login test")
Save your environment as a new image; new runs and terminals start from it
```

**Approve** runs the call. **Decline**, no answer within 10 minutes, or ending
the session fails the call with the reason, for example `run.kill was not
approved: the member declined this action`. The assistant is told to report the
failure and not retry. The server enforces this; the assistant cannot
approve for you. If the agent's call goes away, for example because you
stopped the turn, its card is withdrawn.

## Shell permission prompts

The assistant's own shell and file tools are the agent's, under the agent's
permission prompts. It starts in the agent's asking mode (Claude Code
`default`, Codex `agent`), never the bypass mode runs use. Each prompt appears
as a card with the command and the agent's options; answer it there, or press
the number shown on a button. If the mode cannot be set, the start fails with
`assistant: set the "<mode>" mode, without which the assistant does not run`.

## Conversations

Send with Enter (Shift+Enter inserts a newline). **Stop** cancels the turn in
flight. While a turn runs you cannot send another prompt; stop it first.

There is one conversation per member. It survives page reloads and server
restarts: the transcript is stored on the server and the agent's session is
resumed on the next prompt. If the environment stops, the agent stops with
it and the next prompt starts it again.

**New conversation** (the header button) asks **Start a new conversation?**
and discards the transcript and the agent's session. What the assistant
already did stays done. The server keeps the transcript in an item log that
grows for the life of a conversation; **New conversation** clears it, and
removing a member deletes the assistant's files for that member.

The server also writes `.claude/settings.json` next to those instructions,
allowing the Aether MCP tools so Claude Code does not prompt for each read.

## Limits

- One assistant per member, acting only for that member. It cannot see or act
  for anyone else.
- It uses your own agent account. [Account sharing](security.md#account-sharing)
  does not apply to it.
- Sending a prompt needs the Launch permission, the same as launching a run.
- Text from runs and files is treated as data, not instructions.

## Errors

The panel and the API show these as written.

| Message | Meaning and fix |
| --- | --- |
| `assistant: no agent is ready; install Claude Code or Codex with its Enhanced adapter and sign in` | Set up an agent, [above](#first-use) |
| `scheduler: the environment was created before the assistant existed; restart it to add the assistant's mounts` | [Restart the environment](#restart-the-environment) |
| `assistant: a turn is running; stop it first` | Press **Stop**, then send again (code `-32002`) |
| `assistant: no live session; send a prompt or start it` | Answering or cancelling needs a live session; send a prompt |
| `assistant: open the claude session; stderr: ...` | The adapter failed to start. The agent's stderr is appended; fix it in `aether terminal`, then **Retry** |
| `assistant: no such pending confirmation: <id>` | The card was already answered or expired |
| `the member did not answer within 10m0s` | A confirmation timed out |
| `the assistant session ended before the member answered` | The session stopped while a card was open |
| `the assistant is not configured on this server` | The server runs without the assistant |
| `assistant: the server is shutting down` | Try again after the restart |

A failed start shows **The assistant stopped** with the error and **Retry**.

## Example

```text
You:       What's running?
Assistant: run-3k9 "Fix flaky login test"  working
           run-8x2 "Add retry to uploader"  needs you
You:       Stop run-8x2.
           [card] Approve this action?
           Stop run run-8x2... ("Add retry to uploader")   [Approve] [Decline]
You:       (Approve)
Assistant: Stopped run-8x2.
You:       Launch a run in api to fix the flaky login test.
           [card] Approve this action?
           Launch claude (acp) in api: "Fix the flaky login test"   [Approve] [Decline]
You:       Install Rust in my environment.
           [card] Run `curl ... | sh -s -- -y`?   [Allow] [Reject]
Assistant: rustc 1.xx installed. Save the environment so new runs have it?
```
