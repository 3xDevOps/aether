# Assistant

The assistant is a chat panel in the dashboard where you ask Aether for things
in plain language: "what's the status of my runs?", "launch a run to fix the
flaky login test", "install Rust in my environment". It acts with your
permissions and asks before it launches, messages or stops a run, or saves your
environment, unless you turn on [auto-approval](#auto-approval).

The assistant is not a run. It is an [Enhanced](enhanced-runs.md) session of
Claude Code or Codex with a sign-in of its own, which you make once for it. Its
usage counts against the account you sign it in to. It works before you have a
workspace.

## Where it runs

Each member's assistant runs in a container of its own,
`aether-assistant-<member>`, created from the server's standard image. Your
runs and your [environment](environments.md) (the persistent container
`aether terminal` opens) cannot see it:

- Its home is `<data>/assistant/<member>/home` on the server. It mounts
  nothing from your home or any other member's: no `CLAUDE.md`, `AGENTS.md`,
  `~/.claude`, `~/.codex`, `~/.local/bin` and no agent login.
- Its working directory is `/assistant`, which holds the server's
  instructions and is read-only.
- Its Enhanced adapter is in `<data>/assistant/<member>/install/<agent>`,
  mounted read-only under `/opt/aether-assistant` and first in its `PATH`.
- `/etc/claude-code` and `/etc/codex`, where the agents read managed
  settings, are empty and read-only.
- Claude Code's auto-memory is off for it.

Each start of the assistant replaces its container, so nothing the agent
changed outside its home survives. In the home it keeps only the sign-in file
and the files that resume the conversation: `~/.claude/projects` for Claude
Code; `~/.codex/sessions`, `archived_sessions`, `session_index.jsonl` and the
`state_*.sqlite` files for Codex. Settings, hooks, instructions, MCP servers,
skills and memories the agent wrote are deleted, and `~/.claude.json` is
written again with only the onboarding flag.

The sign-in file must hold only what a sign-in writes. Claude Code's
`.credentials.json` keeps `claudeAiOauth`; Codex's `auth.json` keeps
`auth_mode` (`chatgpt` or `apikey`), `OPENAI_API_KEY`, `tokens` and
`last_refresh`. Anything else, such as a gateway URL that would receive your
prompts, is removed, and that start fails with the keys it removed. **Retry**
starts with the rest. A sign-in file over 64 KiB is deleted.

The server also records, outside the home, which sign-in you made, and every
start checks the file against it. For Codex that is the mode Codex will use
(`auth_mode`, else an API key over tokens), the ChatGPT account the access
token names, and any API key. For Claude Code, whose file names no account, it
is the `scopes`, `subscriptionType` and `rateLimitTier` values, which a token
refresh keeps. A file that no longer matches makes the agent not ready, and a
start fails with `the assistant's sign-in changed since you signed in. Sign in
again`. A plan change on your account needs a new sign-in too.

On the first start, and after a server upgrade that pins a newer adapter, the
panel shows **Installing** while a short-lived container,
`aether-assistant-install-<member>`, runs `npm install` of the pinned adapter
into that directory. It needs network access to the npm registry. The adapter
carries the agent itself: the Claude Code binary of its Agent SDK, or Codex,
which the install pins to 0.159.3. Neither updates on its own; both move when a
server upgrade pins a newer version.

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
Codex. An agent is ready when you signed it in for the assistant; a sign-in in
your environment does not count, because your runs can change it. A row that
is not ready says `Not signed in for the assistant` and has **Sign in**.

**Sign in** opens a terminal running the agent's sign-in in the assistant's
container: `claude-agent-acp --cli auth login` for Claude Code, which prints a
URL and asks for the code the page shows, or `codex login --device-auth` for
Codex, which prints a URL and a code to enter there. It installs the adapter
first if needed. Starting a sign-in deletes that agent's earlier sign-in, so
what the server records is what the sign-in writes. The sign-in is written to
the assistant's home only. Starting
it stops a running assistant session; the next prompt starts it again, and a
prompt sent while the sign-in runs fails until it exits. Select **Done** when
it exits.

Aether checks only that the sign-in file exists, not that it still works. When
the sign-in expires or you want another account, use **Settings > Assistant >
Sign-in > Sign in again**.

The first conversation uses the first ready agent, Claude Code before Codex. A
conversation keeps its agent, because another agent cannot continue it. If that
agent stops being ready, a prompt fails with, for example, `Claude Code is not
ready: Not signed in for the assistant; sign in again or start with another
agent`. Sign in again, or select **New conversation**, which picks the first
ready agent.

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
removes the assistant's container and deletes everything in its home but the
sign-in files: sessions, history, todos, file history, shell snapshots and
logs. The installed adapter is outside the home and stays. What the assistant
already did stays done.

### Earlier assistant state in your home

Before the assistant had its own container it ran in your environment, in
`~/.aether/assistant`, and kept its sessions in your home. That state, and the
login it borrowed from your home, are not moved, because any run could have
changed them. Delete the old state from `aether terminal`; the session
directory is named after your home's path, `-root--aether-assistant` when it
is `/root`:

```sh
rm -rf ~/.aether/assistant ~/.claude/projects/*--aether-assistant
```

Codex sessions from that time are in `~/.codex/sessions` together with your
runs' sessions.

## Limits

- One assistant per member, acting only for that member.
- It uses the account you signed it in to.
  [Account sharing](security.md#account-sharing) does not apply to it.
- Sending a prompt needs the permission to launch runs (collaborator or
  admin).
- Nothing enforces that it treats text from runs, files and web pages as data.

## Troubleshooting

A failed start shows **The assistant stopped** with the error and **Retry**.

| Message | Fix |
| --- | --- |
| `assistant: no agent is ready; sign in to Claude Code or Codex for the assistant` | [Sign in for the assistant](#first-use) |
| `scheduler: not signed in for the assistant. Sign in for the assistant first` | No sign-in was recorded, or the sign-in wrote no usable file. [Sign in](#first-use) |
| `scheduler: the assistant's sign-in changed since you signed in. Sign in again` | The sign-in file is not the one you made; only the assistant's own agent writes it. [Sign in again](#first-use) |
| `scheduler: the assistant's sign-in held what Aether does not keep, and it was removed: ...` | The keys follow the colon. Only the assistant's own agent writes that file. **Retry**, and sign in again if the agent then reports no sign-in |
| `scheduler: install Claude Code for the assistant exited 1: ...` | The adapter install failed; npm's output follows the colon. Fix the cause (often network access), then **Retry** |
| `assistant: a turn is running; stop it first` | Press **Stop**, then send again |
| `assistant: open the claude session; stderr: ...` | The adapter failed to start. Read stderr, then **Retry** |
| `the member did not answer within 10m0s` | The card timed out. Ask again |
| `<method> was not approved: record the approval: <error>` | The approval could not be saved, so the action did not run. Try again |
| `the assistant is not configured on this server` | The server runs without the assistant |
| `assistant: the server is shutting down` | Try again after the restart |
