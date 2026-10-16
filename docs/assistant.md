# Assistant

The assistant is a chat panel in the dashboard where you ask Aether for
things in plain language: "what's the status of my runs?", "launch a run to
fix the flaky login test", "install Rust in my environment". It acts with
your permissions and asks before it launches, messages or stops a run, or
saves your environment, unless you turn on [auto-approval](#auto-approval).

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
- `Ctrl+Shift+Space` (`⌘Shift+Space` on macOS) opens and closes the panel,
  including while a text field, an editor or a terminal has focus. A
  terminal loses nothing to it: `Ctrl+Space` without Shift still sends NUL.
  It is inactive while a dialog or menu is open.
- **Ask Aether** on the board when it is empty, and **Stuck? Ask Aether** in
  the onboarding steps.

On desktop the panel docks to the right of the page and can be resized, as
long as the page keeps 560px beside it. A run's Details and Subagent panels
move to overlays to make that room, and Details returns when the assistant
closes. In a window too narrow for that, the panel floats over the right of
the page instead, without dimming it: the run underneath keeps its size and
stays usable, and `Esc` inside the panel closes it. On a phone it is a side
sheet. Closing the panel keeps your unsent message and your place in the
conversation.

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
messaging one, unless auto-approval is on.

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

An approved call is noted in the transcript before it runs, as a notice
titled `Approved: <summary>` with `<method> passed after you approved its card.`, for example `Approved: Stop run run-8x2...
("Add retry to uploader")`. The server log gets an `assistant approval` line
with your member id, the method, the summary and `auto=false`.

## Auto-approval

**Settings > Assistant > Let the assistant act without asking** lets the
confirmed tools run at once, as if you had approved their card. It is off by
default, and it is yours alone: it applies only to your assistant. Turning it
on in the dashboard asks first, with a warning that the assistant will launch,
message and stop runs and save your environment on its own. The CLI does not
ask:

```sh
aether member assistant                     # show the setting
aether member assistant --auto-approve on
aether member assistant --auto-approve off
```

Over the API it is `assistant.settings` (`{"auto_approve":true}`), and
`assistant.status` reports it as `auto_approve`. While it is on, the panel
says **Acts without asking** under its header.

- **The server decides.** It reads the setting on every confirmed call, so a
  change applies to the live session at once, with no restart. A call and a
  change of the setting never overlap: each call sees the setting from before
  or after the change, and the transcript shows them in that order. Cards
  already open when you turn it on stay open until you answer them.
- **The assistant cannot change it.** Only `assistant.settings`, called as
  you from the dashboard or the CLI, changes it. The assistant's tool socket
  does not serve that method, and no tool reads or sets it.
- **Every action is still recorded.** An auto-approved call is noted in the
  transcript before it runs, as `Auto-approved: <summary>` with `<method> passed
  without a card: auto-approval is on.`, and the server log line has
  `auto=true`. If the note cannot be written, the call does not run. Turning
  the setting on or off is noted too. The run's own events name you as the
  actor, as with an approved card.
- **Notifications are unchanged.** Runs the assistant launches or messages
  are [followed](#notifications) the same way.

With auto-approval on, nothing checks a launch, message, stop or save before
it happens. Text the assistant reads from runs, files and web pages can carry
injected instructions; the assistant is told to treat it as data, but that is
an instruction, not a control. Any program in your environment can also call
the tool socket directly, and gets the same pass. See
[security.md](security.md#the-assistant).

## Shell and file tools

The assistant's own shell and file tools are the agent's, and they run without
asking. The session starts in the same mode Enhanced runs use: Claude Code
`bypassPermissions` (the ACP form of `--dangerously-skip-permissions`), Codex
`agent-full-access`. Installing a tool, editing a file or running a command in
your environment therefore needs no click. Only the Aether actions above
(launch, message, stop, save the environment) wait for a card, and with
auto-approval on not even those. If the mode
cannot be set, the start fails with `assistant: set the "<mode>" mode: <error>`.
A question the agent asks you still appears as a card.

## Notifications

Aether follows every run the assistant launches (`run.launch`) or messages
(`run.inject`) for you. The assistant does not poll or ask to be reminded; the
server tells you itself when the run:

| Kind | When | Body |
| --- | --- | --- |
| `finished` | The run is completed, merged, failed, stopped or interrupted | The run's reason, if any |
| `needs_input` | The agent asks a question, a permission or a form | `Question: <text>`, `Permission: <title>` or `Form: <title>` |
| `needs_approval` | An approval is requested for the run | The requested action |
| `needs_attention` | The run reaches `needs-attention` | Why, worded as in [push notifications](networking.md#notifications) |

The title names the run, for example `Run "Fix flaky login test" finished`. A
notice reaches you four ways:

- **Assistant transcript.** A notice item, `info` for `finished` and `warning`
  for the rest. It is appended while the assistant is idle too.
- **Dashboard.** The `assistant.notice` event
  ([local-gateway.md](local-gateway.md#assistant-notices)) raises a toast and
  badges the Assistant button.
- **Desktop app.** The same event shows an OS notification.
- **Phone.** A Web Push notification to your subscribed devices, for
  `finished` always, and for the `needs_*` kinds only on runs you do not own,
  since your own runs already notify you when they need you. It replaces any
  earlier notification for that run and waits while you are using a dashboard
  ([networking.md](networking.md#notifications)); if the run no longer needs
  you when the wait ends, nothing is sent.

Each state is announced once. Aether stores the last state it announced for
each followed run, after it is in your transcript, so answering a question, a
server restart or a repeated status event does not repeat a notice, and a
notice still queued when the server stops is announced after it starts. The
next question or a new `needs-attention` reason is a new state. The follow
ends when the run finishes, and a run that finished while the server was down
is announced at startup. A run that finishes between the call and the moment
Aether records the follow is announced at once. Deleting the run or removing
the member removes the follow, and a member merged into another keeps their
follows on the surviving member.

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
| `<method> was not approved: record the approval: <error>` | The approval could not be noted in the transcript, so the call did not run |
| `<method> was not approved: read the auto-approval setting: <error>` | The server could not read the setting, so the call did not run |
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
Assistant: (runs `curl ... | sh -s -- -y` without asking)
           rustc 1.xx installed. Save the environment so new runs have it?
```
