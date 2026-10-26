# Assistant

The assistant is a chat panel in the dashboard where you ask Aether for things
in plain language: "what's the status of my runs?", "launch a run to fix the
flaky login test", "install Rust in my environment". It acts with your
permissions and asks before it launches, messages or stops a run, or saves your
environment, unless you turn on [auto-approval](#auto-approval).

The assistant is not a run. It is an [Enhanced](enhanced-runs.md) session of
your own Claude Code or Codex, started in your
[environment](environments.md) (the persistent container `aether terminal` opens,
with your home at `$HOME`). It uses the login in that home, so its usage counts
against your own subscription. It works before you have a workspace.

## Open it

- Select **Assistant** in the sidebar next to **New run**, in the top bar when
  the sidebar is hidden, or in the phone toolbar. A dot on it means an action
  waits for your approval.
- Press `Ctrl+Shift+Space` (`⌘Shift+Space` on macOS), even while a text field or
  a terminal has focus.
- Run **Ask Aether** from the command palette.

Viewers do not see the assistant. On a wide screen the panel docks on the right
and can be resized. On a narrow window it floats over the page and `Esc` closes
it. On a phone it is a side sheet. Closing it keeps your unsent message.

## First use

With no ready agent the panel shows **Needs setup** and lists Claude Code and
Codex. An agent is ready when it is installed in your home, its Enhanced adapter
is installed and a login file exists. Each row that is not ready says which
piece is missing (`Not installed`, `Enhanced adapter not installed`,
`No sign-in found`) and has **Set up**, which runs the same flow as the
**Agents** page. From the CLI:

```sh
aether agent add claude --enhanced
aether terminal
```

Sign in to the agent inside `aether terminal`. The full flow is in
[quickstart.md](quickstart.md#5-set-up-your-agent). The panel updates when
setup finishes.

The first conversation uses the first ready agent, Claude Code before Codex. A
conversation keeps its agent, because another agent cannot continue it. If that
agent stops being ready, a prompt fails with, for example, `Claude Code is not
ready: No sign-in found; sign in again or start with another agent`. Sign in
again, or select **New conversation**, which picks the first ready agent.

### Restart the environment

An environment created before the assistant existed cannot be given the mounts
the assistant needs, so the panel shows **Restart your environment** with
**Restart environment**. Restarting closes any shell open in it. Files in
`$HOME` are kept. Anything installed outside `$HOME` is lost unless you
[saved the environment](environments.md#save-the-environment).

## What it can do

The assistant can do what you can do through the dashboard and nothing more. It
can also read Aether's own guides, so it can explain features of the server you
run.

| What | Examples | Asks first? |
| --- | --- | --- |
| Read | List runs, workspaces and agents. Read one run and its conversation. See your remaining agent quota and your environment's state. Search and show [team memory](memory.md) notes. List integration candidates. | No |
| Pause and resume | Pause or resume a run | No |
| Launch a run | Start a run with a workspace, agent and task | Yes |
| Message a run | Send a message to a run's agent | Yes |
| Stop a run | Stop a run | Yes |
| Save your environment | Save it as the image new runs and terminals start from | Yes |
| Run lifecycle | Relaunch a stopped run, switch its mode, protect or archive it, hand it to a teammate | Yes |
| Swarms | Replace a stopped integrator, release your control of a worker, archive or unarchive a swarm | Yes |
| Deliver and merge | Approve, reject or deliver an integration candidate; open or merge a run's pull request | Yes |
| Team memory | Add a note, pin or unpin one; it cannot remove notes | Yes |

Pausing and resuming run at once. The run's events name you as the actor.

An approved message reaches the agent at once when you control the run, or
when it is your own run and no one controls it. Otherwise it waits about 45
seconds for the member in control to approve or deny it.

Installing a toolchain or editing a file is not an Aether action. The assistant
does it in its own shell in your environment, then offers to save the
environment.

## What it sees

Each message carries a short description of what your dashboard shows, so "why
is this stuck?" works without ids: the page you are on, the workspace and run,
the open run view, the open file's path (never its contents) and the last 8
pages you visited. It never holds titles, file contents, drafts, terminal text
or secrets. The line above the message box, **Sharing: run run-8x2 · Changes**,
expands to the exact JSON the next message sends. Clear **Share the page I am on
with the assistant** to stop. The choice is saved for you on every device.

## Approve an action

An action that asks waits for you. The panel shows a card, **Approve this
action?**, with a one-sentence summary, the exact parameters and when it
expires:

```text
Launch claude (acp) in api: "Fix the flaky login test"
Stop run run-8x2... ("Fix flaky login test")
Save your environment as a new image; new runs and terminals start from it
```

**Approve** runs it. **Decline**, no answer within 10 minutes, or ending the
session fails the action with the reason, for example `run.kill was not
approved: the member declined this action`. The assistant is told to report the
failure and not retry. The assistant cannot approve for you. Each approval is
recorded in the transcript before the action runs.

## Auto-approval

**Settings → Assistant → Let the assistant act without asking** lets the
assistant launch runs, message and stop your own runs and save your environment
without a card. It is off by default and applies only to your assistant. While
it is on, the panel says **Acts without asking**. The dashboard asks you to
confirm before turning it on. The CLI does not ask:

```sh
aether member assistant                     # show the setting
aether member assistant --auto-approve on
aether member assistant --auto-approve off
```

```
auto-approve  off: each action below waits for your approval on a card
covers        launching runs, messaging and stopping your own runs, saving your environment
always asks   messaging or stopping another member's run
not covered   the assistant's own shell and file tools, which never ask
```

- Messaging or stopping another member's run always asks, with the owner named
  on the card.
- A change applies to the live session at once. The assistant cannot change the
  setting.
- Every auto-approved action is still recorded in the transcript as
  `Auto-approved: <summary>`. If the note cannot be written, the action does not
  run.

> [!WARNING]
> With auto-approval on, nothing checks a launch, a message to your own run or
> a save before it happens. Text the assistant reads from runs, files and web
> pages can carry injected instructions. The assistant is told to treat it as
> data, and nothing enforces that. Any program in your environment can also
> call the assistant's tools and gets the same pass. See
> [security.md](security.md#the-assistant).

After you approve three cards in a row that auto-approval would have passed, the
panel offers **Skip approval cards for the assistant?** when it is idle.
**Turn on auto-approval** turns it on. **Not now** asks again in 7 days, then 30,
then never. **Don't suggest again** stops it.

### Shell and file tools never ask

The assistant's own shell and file tools run without asking, with or without
auto-approval. The session starts in the same mode Enhanced runs use: Claude
Code `bypassPermissions`, Codex `agent-full-access`. Installing a tool, editing
a file or running a command in your environment needs no click. A question the
agent asks you still appears as a card.

## Notifications

Aether follows every run the assistant launches or messages for you. You do not
ask for updates. You are told when the run:

| Notice | When |
| --- | --- |
| Finished | The run completed, merged, failed, stopped or was interrupted, or a live run's reported success is ready for review |
| Needs input | The agent asks a question, a permission or a form |
| Needs attention | The run [needs you](failure-handling.md#needs-you) with no question pending: it reports `needs-input` or `blocked`, hit a usage limit, had its turn interrupted, disconnected, or failed and you have not seen it. Waiting on agents, CI or a process never notifies. The reason is worded as in [push notifications](networking.md#notifications) |

A notice appears in the assistant's transcript, as a toast with a badge on the
**Assistant** button, as an OS notification in the desktop app, and as a phone
push. On the phone, a finished run always notifies you. The other three notify
only for runs you do not own, since your own runs already notify you. Each state
is announced once, and the follow ends when the run finishes or is deleted.

## Conversations

Send with Enter (Shift+Enter inserts a newline). **Stop** cancels the turn in
progress. While a turn runs you cannot send another prompt.

There is one conversation per member. It survives page reloads and server
restarts. If the environment stops, the agent stops with it and the next prompt
starts it again. **New conversation** asks **Start a new conversation?** and
discards the transcript and the agent's session. What the assistant already did
stays done.

## Limits

- One assistant per member, acting only for that member.
- It uses your own agent account. [Account sharing](security.md#account-sharing)
  does not apply to it.
- Sending a prompt needs the permission to launch runs (collaborator or
  admin).
- Nothing enforces that it treats text from runs, files and web pages as data.

## Troubleshooting

A failed start shows **The assistant stopped** with the error and **Retry**.

| Message | Fix |
| --- | --- |
| `assistant: no agent is ready; install Claude Code or Codex with its Enhanced adapter and sign in` | [Set up an agent](#first-use) |
| `scheduler: the environment was created before the assistant existed; restart it to add the assistant's mounts` | [Restart the environment](#restart-the-environment) |
| `assistant: a turn is running; stop it first` | Press **Stop**, then send again |
| `assistant: open the claude session; stderr: ...` | The adapter failed to start. Fix the agent in `aether terminal`, then **Retry** |
| `the member did not answer within 10m0s` | The card timed out. Ask again |
| `<method> was not approved: record the approval: <error>` | The approval could not be saved, so the action did not run. Try again |
| `the assistant is not configured on this server` | The server runs without the assistant |
| `assistant: the server is shutting down` | Try again after the restart |
