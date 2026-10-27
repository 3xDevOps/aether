# Assistant

The assistant is a chat panel in the dashboard where you ask Aether for things
in plain language: "what's the status of my runs?", "launch a run to fix the
flaky login test", "install Rust in my environment". It acts with your
permissions and asks before it launches, messages or stops a run, or saves your
environment, unless you turn on [auto-approval](#auto-approval).

The assistant is not a run. It is an [Enhanced](enhanced-runs.md) session of
Claude Code or Codex signed in with your own login, so its usage counts against
your own subscription. It works before you have a workspace.

## Where it runs

Each member's assistant runs in a container of its own,
`aether-assistant-<member>`, created from the server's standard image. Your
runs and your [environment](environments.md) (the persistent container
`aether terminal` opens) cannot see it:

- Its home is `<data>/assistant/<member>/home` on the server, not your home.
  Nothing in your home configures it: no `CLAUDE.md`, `AGENTS.md`,
  `~/.claude/settings.json`, `~/.codex/config.toml` or `~/.local/bin`.
- Its working directory is `/assistant`, which holds the server's
  instructions and is read-only.
- From your home it mounts only the login file of the agent it runs, as a
  single read-write file: `~/.claude/.credentials.json` for Claude Code or
  `~/.codex/auth.json` for Codex. Whichever account that file holds receives
  the assistant's prompts; see [the login file](#the-login-file).
- Its Enhanced adapter is in `<data>/assistant/<member>/install/<agent>`, mounted
  read-only at `/opt/aether-assistant`, first in its `PATH`. The assistant
  cannot change it, and **New conversation** does not need to reinstall it.
- Claude Code's auto-memory is off for it.

On the first start, and after a server upgrade that pins a newer adapter, the
panel shows **Installing** while a short-lived container,
`aether-assistant-install-<member>`, runs `npm install -g` of the pinned
adapter into that directory. It needs network access to the npm registry. The
adapter carries the agent itself: the Claude Code binary of its Agent SDK, or
the Codex version its package depends on. Neither updates on its own; both move
when a server upgrade pins a newer adapter.

### The login file

The login file is bound into the assistant container when the container is
created, at the assistant's start. What reaches the other side:

- A file rewritten in place reaches both sides at once. Codex rewrites
  `auth.json` in place. Claude Code in the assistant rewrites its file in
  place, because the file is a mountpoint there.
- A file replaced by rename, as Claude Code does in your runs and environment,
  is a new file. The running assistant keeps the old one until its next start.
  Your containers created after your first assistant start, or while you share
  your account, mount the login the same way, so Claude Code rewrites it in
  place there too; older ones replace it.
- Signing in again replaces the file; the assistant uses the new login from
  its next start.

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
Codex. An agent is ready when its login file exists in your home. A row that is
not ready says `No sign-in found` and has **Set up**, which runs the same flow
as the **Agents** page. From the CLI:

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
| Stop or close a run | Stop a run, or close it as merged or abandoned | Yes |
| Save your environment | Save it as the image new runs and terminals start from | Yes |
| Run lifecycle | Relaunch a stopped run, switch its mode, protect or archive it, hand it to a teammate | Yes |
| Swarms | Replace a stopped integrator, release your control of a worker, archive or unarchive a swarm | Yes |
| Deliver and merge | Approve, reject or deliver an integration candidate; open or merge a run's pull request | Yes |
| Team memory | Add a note, pin or unpin one; it cannot remove notes | Yes |

Pausing and resuming run at once. The run's events name you as the actor.
After each action the assistant says what it did, to which run, and the result.

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
or secrets.

The page is pinned when you start typing. If you move to another run or page
before sending, the panel asks **You moved to Ship the docs. Ask about:** with
both pages, and sends nothing until you pick one.

To point a message at something specific, use **Fix this** on a failed session,
the button beside a failing check or beside the pull request number in a run's
header, **Ask the assistant** beside a swarm task's error, or the run menu's
**Ask the assistant to fix this run** and **Ask the assistant why this is
stuck**. Each opens the panel with that run, check, pull request, error or task
attached and a short draft to edit; nothing is sent until you press Send. The
attachment holds only ids and names, such as `acme/api#42 · check lint`; the
assistant reads the details with its tools.

Above the message box, a chip names each thing the message is about. Remove
one with its **x**, or Delete or Backspace. Removing the page also stops the
assistant reading it between messages until you send or add it back.
**Inspect** lists every field the next message sends, and **Raw JSON** shows
the exact payload. Clear **Share the page I am on with the assistant** there
to stop sending the page; things you attached are still sent. The choice is saved for you on every device.

The view a message carries is the one the assistant uses for that turn until
you open another page. Before acting on a run it resolves the target from ids
you wrote, then the page, then recent pages, and asks when more than one fits
or you moved away from the run it last discussed. Only this assistant sees your
page; assistants connected from outside Aether, such as ChatGPT, do not.

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
turn or session fails the action with the reason, for example `run.kill was
not approved: the member declined this action`. The assistant is told to report
the failure and not retry. The assistant cannot approve for you.

Every action is recorded in the transcript. The approval is written before the
action runs; if it cannot be written, the action does not run. The outcome
follows when the call returns:

| Record | When |
| --- | --- |
| `Approved: <summary>` | You approved the card |
| `Auto-approved: <summary>` | [Auto-approval](#auto-approval) passed it |
| `Declined`, `Expired`, `Withdrawn: <summary>` | You declined, did not answer in 10 minutes, or the turn ended first. Nothing ran |
| `Done: <summary>` | It succeeded, with the run it launched |
| `Failed: <summary>` | The server refused it, or the approval could not be read or counted, with the exact error. In the second case nothing ran |

Pausing and resuming are recorded as `Done` or `Failed` without an approval.
Each record carries the target's run and workspace id, and the owner when the
run is another member's; the [protocol reference](local-gateway.md) gives the
fields. Resetting the conversation drops the records of actions still in
flight instead of writing them into the new one.

## See what it did

Each action appears in the conversation as a card. The card names the action
and its target ("Launched run on api with Claude Code", "Merged PR #42") and
shows its state: running, done, failed, declined by you, expired after 10
minutes unanswered, withdrawn when the turn ended first, or did not finish. A
call waiting for your approval is one line above the approval card. It also
says whether you approved it or auto-approval passed it, and links to the run,
swarm, pull request or environment. A failure shows the server's error as is.
**Retry** adds `Retry: <what it tried>` to the message box for you to edit and
send. A run or swarm the assistant launched that is still going reads **Working
in the background**; it keeps running after you close the panel. Reads fold
into one line, such as **Read 3 runs**, that expands. The **Actions** button in
the panel header lists the conversation's actions, newest first.

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
progress; if the agent keeps going, press it again. While a turn runs you
cannot send another prompt.

There is one conversation per member. It survives page reloads and server
restarts. If the agent stops, the next prompt starts it again. **New
conversation** asks **Start a new conversation?**, then deletes the transcript,
removes the assistant's container and deletes its whole home: sessions,
history, todos, file history, shell snapshots and logs. The installed adapter
is outside the home and stays. What the assistant already did stays done.

### Earlier assistant state in your home

Before the assistant had its own container it ran in your environment, in
`~/.aether/assistant`, and kept its sessions in your home. That state is not
moved, because any run could have changed it. Delete it from `aether terminal`:

```sh
rm -rf ~/.aether/assistant ~/.claude/projects/-root--aether-assistant
```

Codex sessions from that time are in `~/.codex/sessions` together with your
runs' sessions.

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
| `assistant: no agent is ready; sign in to Claude Code or Codex in your environment terminal` | [Set up an agent](#first-use) |
| `scheduler: install Claude Code for the assistant exited 1: ...` | The adapter install failed; npm's output follows the colon. Fix the cause (often network access), then **Retry** |
| `assistant: a turn is running; stop it first` | Press **Stop**, then send again |
| `assistant: open the claude session; stderr: ...` | The adapter failed to start. Read stderr, then **Retry** |
| `the member did not answer within 10m0s` | The card timed out. Ask again |
| `<method> was not approved: record the approval: <error>` | The approval could not be saved, so the action did not run. Try again |
| `the assistant is not configured on this server` | The server runs without the assistant |
| `assistant: the server is shutting down` | Try again after the restart |
