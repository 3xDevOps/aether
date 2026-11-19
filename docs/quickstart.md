# Quickstart

This guide takes you from nothing to an agent's change published as a pull
request, in eight steps. Each step ends with how you know it worked.
Cloudfield's terms are defined in the [glossary](README.md#concepts).
A coding agent can do these steps from the CLI: paste the prompt in
[agents.md](agents.md) into it.

You need:

- A Linux machine (amd64 or arm64) with Docker Engine and git, and at least
  2.5 GiB of available memory and 6 GiB of free disk: the **server**, where
  agents run. It needs no desktop and no open port
  ([prerequisites](install.md#server-prerequisites)).
- Your own computer (Linux, macOS or Windows): the **client**, where you work.
  The `cloudfield` CLI and the desktop app run here and talk to the server.
- A GitHub account, and a login for at least one coding agent, such as a Claude
  or ChatGPT subscription.

Steps 1 to 3 use both machines, and each says which one it runs on. From step 4
on you work only from your computer. If someone else already runs your server,
do step 1 on your computer only, join the way
[teams.md](teams.md#join-the-server) describes instead of step 3, then continue
at step 5.

## 1. Install

Run the install script on the server, then on your computer (Linux or macOS):

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/cloudfield/main/scripts/install.sh | sh
```

At the `role` prompt, answer `server` on the server. It installs
`cloudfield-server` and `cloudfield` in `/usr/local/bin` with `sudo`, then starts the
setup in step 2. Answer `client` on your computer. It installs the `cloudfield` CLI
in `~/.local/bin` without `sudo`, then builds the desktop app with `cloudfield gui
build`, which downloads Electron and takes a few minutes. If `~/.local/bin` is
not on your `PATH`, the script prints the line that adds it.

On Windows, download and review the PowerShell installer, then run it. It
installs the CLI and the desktop app without administrator rights:

```powershell
Invoke-WebRequest -UseBasicParsing -Uri https://raw.githubusercontent.com/3xDevOps/cloudfield/main/scripts/install.ps1 -OutFile "$env:TEMP\aether-install.ps1"
powershell -NoProfile -ExecutionPolicy Bypass -File "$env:TEMP\aether-install.ps1"
```

It worked if `cloudfield version` prints the release on your computer, and
`cloudfield-server version` (or `--version`) on the server.
[install.md](install.md) covers every platform, `--role none` (binaries only),
upgrades and removal.

## 2. Start the server

Do this on the server. Answering `server` already ran setup. If you skipped it,
run `sudo cloudfield-server setup`.

Setup decides how your computer reaches the server. **Tailscale** is the
simplest: on a host where Tailscale runs, Enter at the tailnet question picks
it, and people link by this host's tailnet name with their tailnet login
([networking.md](networking.md#tailscale)). Otherwise setup turns on the
**edge**, a relay the Cloudfield project runs at `https://edge.cloudfield.ai`.
The server and your computer both dial out to it, so neither needs an open port.
SSH runs end to end through it: the edge cannot read your traffic, but sees
host names, IP addresses and who signs in ([edge.md](edge.md));
`cloudfield-server config set edge-url ""` turns it off.

Setup asks these questions. Enter accepts the default shown in brackets:

| Prompt | Answer |
| --- | --- |
| `Continue without Docker` | Asked only when Docker does not answer. Enter stops with nothing written. Install or start Docker, then run setup again. |
| `SSH listen address for Cloudfield, not the system sshd` | Enter keeps `:2222`. |
| `Data directory` | Enter keeps `/var/lib/aether`. |
| `Use Tailscale to reach this server` | Asked only when Tailscale runs on this host. A **tailnet** is a private network made with Tailscale. Enter (yes) signs people in by their tailnet login; answer no when your computer is not on this tailnet, and setup uses the edge. |
| `Let anyone on the tailnet join ...`, `Also require a registered SSH key ...`, `Dashboard HTTPS port` | Asked after a yes. Enter keeps admin approval for new members, no key requirement, and the dashboard on port 443 for phones ([networking.md](networking.md#who-is-let-in)). |
| `Turn on the edge too` | Asked after a yes. Enter (no) keeps the edge off; yes also lets devices off the tailnet in. |
| `Choose 1 or 2` | Who may reach the server through the edge. `1`: anyone you invite, by signing in with GitHub. `2`: each new device also waits for someone to approve it; setup recommends it for a team workspace holding code or credentials that must stay protected. No default. |
| `Write it` | Enter writes `/etc/aether/server.conf` and the systemd unit. |
| `Let agents use Docker inside their sessions?` | Enter installs Sysbox, the container runtime that lets agents run Docker inside their sessions (Ubuntu and Debian only). |

Setup ends with the commands left for you. With the edge on, it prints a claim
link first:

```
claim link: aether://claim?edge=https%3A%2F%2Fedge.cloudfield.ai&server=<server id>#<secret> (valid for 30 min, until 2026-10-25 15:04 UTC, 5 attempts)

next:
  1. start the server on this machine:
       sudo systemctl daemon-reload && sudo systemctl enable --now aether-server
  2. on your computer, install Cloudfield if it is not there yet (Windows: docs/install.md):
       curl -fsSL https://raw.githubusercontent.com/3xDevOps/cloudfield/main/scripts/install.sh | sh -s -- --client
  3. on your computer: in the Cloudfield app's Connect step choose Cloudfield relay, sign in
     with GitHub and paste the claim link above; in a terminal:
       aether login
       aether link --claim <server id>-<secret>
  4. optional, to run agents from ChatGPT or Claude, on that computer:
       aether mcp
```

On a tailnet, item 3 names this host instead, and the dashboard address when
setup turned it on. With the edge on too, the claim follows as the
alternative for a computer off the tailnet; do one or the other, because the
first link makes its account the super admin:

```
  3. on your computer, on the same tailnet: in the Cloudfield app's Connect step choose
     Tailscale and enter my-server.tail1234.ts.net; in a terminal:
       aether link my-server.tail1234.ts.net
     or, with nothing installed, open https://my-server.tail1234.ts.net/ in a browser there
```

> [!WARNING]
> Whoever uses the claim link first becomes the server's super admin. It is
> valid for 30 minutes and 5 attempts. `sudo cloudfield-server edge claim-code`
> prints a new one until someone has joined.

Item 1 starts the server: run it now, on the server. Item 2 is step 1 on your
computer, if you have not done it. Item 3 links your computer: that is step 3,
so keep the claim link or host name. Item 4 is optional
([connectors.md](connectors.md)); skip it for now.

With neither, item 3 is `cloudfield link <this-host>:2222`. With the edge, linking
by address before the claim works only on the server or through an SSH tunnel
([networking.md](networking.md#the-first-admin)).

It worked if `sudo cloudfield-server edge status` shows `connection  connected since
<time>`, or without the edge, `systemctl status aether-server` shows `active`.

### Set up over ssh, for agents and scripts

To drive setup from an agent over ssh, with no terminal on the server (`sudo`
there must not ask for a password), run the steps non-interactively:

```sh
ssh <user>@<server> 'curl -fsSL https://raw.githubusercontent.com/3xDevOps/cloudfield/main/scripts/install.sh | sh -s -- --role none'
ssh <user>@<server> 'sudo cloudfield-server install --edge-url https://edge.cloudfield.ai --edge-access <account|approved-devices>'
ssh <user>@<server> 'sudo systemctl daemon-reload && sudo systemctl enable --now aether-server'
ssh <user>@<server> "timeout 300 sh -c 'until sudo cloudfield-server edge status >/dev/null; do sleep 5; done'"
ssh <user>@<server> 'sudo cloudfield-server edge claim-code'
```

The access policy is the owner's choice (`account` or `approved-devices`, as
in the table above). If the wait times out, `sudo cloudfield-server edge status`
prints the `connection` line that says why; fix that before you claim. The
claim code goes to step 3: `cloudfield login`, then `cloudfield link --claim <code>`.

## 3. Link from your machine

Do this on your computer, with what step 2 printed: a tailnet host name or a
claim link. Linking saves the server in your client config. The first person to
link a fresh server becomes its **super admin**; there is no
password. In the desktop app, which opens at **Onboarding → Connect** until it
is linked, answer **How does this computer reach your server?**:

| Choice | When | Then |
| --- | --- | --- |
| **Tailscale** (recommended) | This computer is on the server's tailnet. | Enter the name setup printed in **Server's tailnet name**, and click **Link**. |
| **Cloudfield relay** | Setup printed a claim link, or someone sent you an invitation. | Click **Sign in with GitHub**, open the address shown and confirm the code. Paste the claim link into **Claim or invite link** and click **Claim and link**. Or paste either link into **Have an invitation or claim link?** first. |
| **SSH address** | This computer reaches the server's SSH port directly. | Enter `<server-host>:2222`, and an **Invite code** if an admin gave you one, then click **Link**. |

Opening the `aether://claim` link on your computer opens the relay choice with
the link filled in.

Or in a terminal:

- On a tailnet: `cloudfield link <server-host>`.
- Through the edge: run `cloudfield login`, open the address it prints, sign in
  with GitHub, and enter the code on that edge page. Then run
  `cloudfield link --claim '<claim link>'`, or
  `cloudfield link --claim <server id>-<secret>`.

On Windows, type `aether` instead of `cloudfield` for a command that takes a
link: `cloudfield.cmd` passes it to `cmd.exe`, which splits it at the `&`
([install.md](install.md#old-names)).

It worked if the step shows **Connected**, or `cloudfield link` ends with `linked
to <server>` and your role, such as `linked to server <server id> through
edge.cloudfield.ai as <your name> (super_admin)`.

`cloudfield status` shows where you are:

- What this computer is linked to: the edge it is signed in to, the server and
  your role.
- The workspaces, and which agents your environment has installed and whether
  their login file is there.
- The command for the next missing step. It is prefixed `an admin runs:` or
  `a human runs in a terminal:` when it is not yours or needs a terminal.

`cloudfield status --json` prints the same as one JSON object. When the server
cannot be reached, `cloudfield status` prints what this computer holds and the
error, then exits 1.

On a tailnet the server knows you by your tailnet login. Linking by SSH address
registers your SSH key instead: `~/.ssh/id_ed25519` (created when you have no
key), a key in your ssh-agent, or `--key <path>` ([networking.md](networking.md)).

Optional: set the name and email your agents commit as (otherwise they commit
as your display name at `<member-id>@aether.local`):

```sh
cloudfield member git --name "Ada Lovelace" --email ada@example.com
```

## 4. Create a workspace

A **workspace** is one repository, its **base branch** (the branch new sessions
start from), and the sessions started from it. Only an admin or super admin can create one, and the creator owns it ([workspace access](teams.md#workspace-access)).

Open the **dashboard**, the web app where you work with Cloudfield. The desktop
app shows it, and so does `cloudfield gui`, which serves it in a browser tab from
your computer. It shows **Onboarding** (**Connect**, **Repository**, **Agent**,
**First session**, which match steps 3 to 6 here) until you finish. If you linked
in a terminal, it opens at the **Board** instead; click **Add your repository**
there. **Settings → General → Setup guide → Open guide** reopens onboarding.

### GitHub repository

1. In **Onboarding → Repository**, under **From GitHub**, click **Add GitHub
   repository**.
2. Click **Connect GitHub**, then **Copy code and open GitHub**. Enter the code
   on GitHub and authorize **GitHub CLI**, which Cloudfield uses to connect. The
   dialog shows **Connected as `<login>`** by itself.
3. Choose a repository. Private and organization repositories need no deploy
   key.
4. Check the workspace name, the branch, and **Publish changes to** (where sessions
   push). **Advanced** changes them: pick a fork, or **None** to not publish.
5. Click **Review repository**. The server creates the workspace and fetches
   the branch.
6. Check the **Revision**, then click **Use repository**. That exact commit
   becomes the workspace's base.

It worked if onboarding moves on to **Agent**, and the workspace is in the
sidebar's workspace switcher.

The GitHub connection is stored in your home on the server (step 5 explains
it); sessions you launch push with it, and later fetches use it. Members see the
code, not your credentials. If the fetch fails, the workspace still exists:
click **Retry fetch**, or **Source settings** to reconnect. **Settings → GitHub
→ Add repository** adds another repository later.

### Other sources

| Source | In **Add a workspace** |
| --- | --- |
| Public Git URL | **From a Git URL → Import repository**, then **Public HTTPS**. |
| Private Git URL | Same form with **Read-only deploy key**. Install the public key it shows at the Git host, then **Verify** and **Adopt candidate**. |
| A clone on your computer | **From a local clone → Create from local clone**, then push the base branch. Needs the desktop app or `cloudfield gui` on that computer. Or [from the CLI](#from-the-cli). |

[teams.md](teams.md) covers each source. A workspace can also run a setup script
(such as `npm ci`) and set variables in new sessions: **Environment** on its
**Repository** page, or `cloudfield workspace env`
([environments.md](environments.md#workspace-environment)).

### From the CLI

```sh
cloudfield workspace import ~/code/myproject
cloudfield workspace import https://github.com/acme/myproject
```

A path creates a workspace named after the clone, with its checked-out branch as
the base branch, adds the `aether` remote and pushes the base branch, ending
with `pushed <branch> to workspace <name>`. A GitHub URL makes the server fetch
the default branch with your GitHub connection and print the
`cloudfield workspace mirror adopt` command that makes the fetched commit the base;
review the commit first. Both are safe to re-run: a clone already imported is
linked to its existing workspace.

## 5. Set up your agent

An **agent** is the coding CLI a session starts: Claude Code, Codex, OpenCode, pi,
oh-my-pi, GitHub Copilot CLI, Gemini CLI, Cursor CLI or Antigravity CLI. Agents
run on the server, not on your computer. You install each one in your
**environment**, your own sandbox there (a container or virtual machine), with
the vendor's own login. Your **home** there persists, so this is done once.

1. In **Onboarding → Agent**, or on the **Agents** page, click **Set up**
   beside an agent.
2. Choose how sessions show it. **Standard** is the agent's own terminal, as on
   your computer. **Enhanced** shows its messages, tool calls, file changes and
   approvals as dashboard controls; it misses some agent-specific commands and
   starts a few seconds slower ([enhanced-sessions.md](enhanced-sessions.md)). In both,
   the agent acts without asking by default; the Enhanced card says whether it
   can be made to ask.
3. Click **Install `<agent>`**. It runs the vendor's install command in your
   environment. A failure shows **Install failed** and the command's output.
4. Log in, in the terminal that opens. The login command is already typed, for
   example `claude` (then `/login`) or `codex login --device-auth`.
5. Click **Check**.

It worked if the dialog shows **Installed** and **Login found**, and after
**Done** the agent's button reads **Run**. Cloudfield only looks for the login
file; the agent proves the login when a session starts.

From the CLI, on your computer. The environment has no `cloudfield` command, so do
not run these in its terminal:

```sh
cloudfield agent add codex
cloudfield agent login codex
cloudfield agent list
cloudfield account usage
```

Without a terminal (an agent's shell, CI), `agent add` installs the agent and
prints the end of the install log. `agent login` prints a device code for
Codex; Claude Code and the others need the human at a terminal, or an API key
([harnesses.md](harnesses.md#use-an-api-key)). `cloudfield account usage` asks the
vendor whether the login works. Logins and limits per agent:
[harnesses.md](harnesses.md).

### Upload agent configuration

Optional: copy your own agent settings to the server.

Settings, instruction files such as `CLAUDE.md`, commands, skills, hooks and MCP
configuration live in a directory such as `~/.claude`; sessions read the same path
in your home on the server. To copy yours, open **Agents → Agent config files**,
click **Copy prompt**, and paste it into an agent on the computer that has your
configuration (it needs the `cloudfield` CLI, linked). That agent previews with
`cloudfield config push --dry-run`, asks you, then runs `cloudfield config push`.
Logins, tokens, history, and files the secret scanner flags are never uploaded,
and only `--overwrite` replaces a different server file. Edit uploaded files on
**Files → Home**. [Upload and edit
configuration](environment-home.md#upload-and-edit-configuration) lists the
commands, what is left out and the errors.

### Connect GitHub for publishing

Sessions push and open pull requests with the GitHub login in your home. If you
connected GitHub in step 4, skip this. Otherwise click **Connect GitHub** under
**Agents → GitHub**, or run `cloudfield github connect` on your computer. It prints
a code, waits while you approve it at github.com/login/device, then finishes
the setup ([environment-home.md](environment-home.md#from-the-cli)).

## 6. Launch a session

A **session** is one agent on one task, in its own sandbox and on its own git
branch. It uses your home, so your agent login and configuration come along.

In the dashboard, click **New session**, or continue to onboarding's **First session**,
which always launches at Default. In **New session**, **Permission** at the top
starts on **Default**: the agent runs unprivileged and every `sudo` waits for
you to allow it. **Root access** lets `sudo` run
without asking, and **Isolated** keeps every change outside the checkout
inside the session ([permission tiers](security.md#permission-tiers)). Enter the
**Task**, pick the **Agent** and the **Mode**, and click **Launch**.
**Background** mode runs the task once with no interaction. On a phone, tap
**New**, type the task and tap **Launch**: the repository and agent start on
your last session's unless you saved a default agent, and Permission and Mode
are under **More options**. From the CLI:

```sh
cloudfield session "add a health check endpoint" --workspace myproject --agent claude
```

It prints `session run-d6ay38tj8j running`. `--mode` picks `standard`, `enhanced`
or `background`; without it, the session is Enhanced when the agent's Enhanced
mode is installed, and Standard otherwise. `--model` picks the model,
`--effort` the effort or thinking level
([harnesses.md](harnesses.md#choose-an-effort)), and `--permission` what the
agent may change outside its checkout: `root`, `default` (the default) or
`isolated` ([permission tiers](security.md#permission-tiers)).
The first session on a server pulls the environment image, so it starts slower. To
let an agent hand parts of a task to other agents, see [subsessions](teams.md#subsessions).

Every launch carries an idempotency key. If the call fails or you press
Ctrl-C, the CLI prints the exact retry with `--idempotency-key`; running it
returns the same session instead of starting another.

It worked if the session is under **Working** on the **Board**, the dashboard's
page of sessions, and `cloudfield sessions` lists it.

### Working across repos

Optional: skip it on your first session, and go on to step 7.

A session belongs to one **primary** workspace, checked out at `/workspace`. Add
`--repo <workspace>` (repeatable) to check out other existing workspaces beside
it:

```sh
cloudfield session "rename the /users endpoint in the API and the web client" \
  --workspace web --repo api --repo shared-types --agent claude
```

Each extra repo is cloned to `/repos/<workspace name>` (characters outside
`A-Za-z0-9._-` become `-`), on its own session branch `cloudfield/run-<task>-<id>` in
its own workspace. The agent's startup prompt lists the extra paths. The
dashboard shows each repo on the session page, and the per-session Git, pull request
and **Changes** methods take a `repo` (the repo's workspace ID) to pick one;
without it they act on the primary repo.

A launch is all or nothing. If any repo cannot be checked out, no session starts
and the error names that repo:

```
session run-d6ay38tj8j failed: provisioning: create checkout: repo "api" (workspace 01m2xwb3es63c0cs0e7aewqpeq): gitengine: base commit "..." is unavailable
```

Limits: extras must already exist as workspaces and cannot repeat or include the
primary one. The setup script, environment, agent account and budget are the
primary workspace's. `cloudfield pull <session>` fetches each repo's branch into the
clone linked for its workspace and skips, naming it, an extra repo with no
linked clone. Evidence packets cover the primary repo only, and the dashboard's
Compare menu is the primary repo's.
Extra branches are published to their own workspaces; closing the session commits
and publishes every repo. Each pull request is per repo; there is no cross-repo
merge. A subsession inherits its parent's extra repos ([teams.md](teams.md#subsessions)); templates use one workspace.

## 7. Watch it

The **Board** groups sessions into **Needs you**, **Working** and **Finished**.
Open a session to see its views:

| View | Shows |
| --- | --- |
| **Chat** | An Enhanced session's messages, tool calls and approvals, with a message box |
| **Terminal** | A Standard session's agent terminal, and extra shells in its container |
| **Changes** | The diff against the base branch |
| **Files** | The session's checkout; **Save** writes your edit into it, and **Upload files…** or dropping files adds files to it |
| **Browser** | A Chromium on the server beside the session, where `localhost` is the session's own app |

From the CLI, on your computer:

- `cloudfield message <session-id> "also update the README"` messages the agent and
  prints the receipt (`delivered`, or `queued` for the member in control).
- `cloudfield logs <session-id>` prints what the agent did as plain text.
- `cloudfield attach <session-id>` attaches your terminal to a Standard session's agent.
  Every key, Ctrl-C included, goes to the agent; close the terminal to detach,
  and the session keeps going. `cloudfield attach --read-only <session-id>` only watches.

For VS Code, Cursor or Zed, run `cloudfield ssh-config` once, then **Open in
editor…** in the session's **More** menu ([terminal.md](terminal.md)). On a phone,
use the [Android app](install.md#android-app) or the tailnet dashboard
([networking.md](networking.md#the-dashboard)).

## 8. Review and publish the result

1. Open the session's **Changes**. To ask for changes, click **+** beside a line
   number, write what should change and click **Comment**. **Send to agent**
   sends all your comments as one message.
2. Click **Publish…**. The first line shows the branch, the commit, and the
   GitHub login the push uses, or that login's error.
3. If the agent left uncommitted work, select the paths, click **Review
   selected paths**, write a **Commit message**, click **Commit selected**,
   then **Continue to push**.
4. Check the **PR title** and **PR description**, and click **Push and open
   pull request**. It pushes the session's branch without force and opens a pull
   request into the base branch.

It worked if the dialog shows **Pushed: yes** and the pull request. The session page
then shows the pull request number and its checks, such as **2 failing**; a
failing check has **Send to agent**. Review and merge on GitHub as usual.

`cloudfield publish <session-id>` is the CLI form of **Publish…**.

When you are done, close the session: **Close session…** in its **More** menu, or
`cloudfield close <session-id> --outcome merged` (or `abandoned`). Both first list
what the close affects, such as an open pull request or a session waiting on
this one, and ask ([failure-handling.md](failure-handling.md#before-a-close)).
Closing commits the
agent's latest work to the session's branch and keeps the sandbox 30 days
(`--run-container-ttl`; 7 for a Standard or mode-switched session on a Docker host). Until then the **Relaunch** button where the message
box sat, **Relaunch** in the **More** menu, or `cloudfield reopen <session-id>` brings
the session back with its chat and files
([Relaunching a closed session](failure-handling.md#relaunching-a-closed-session)). The days stop counting while the session is
live again and start over when you close it.

`cloudfield pull <session-id>` fetches the branch, `cloudfield/run-<task>-<id>`
(`aether/run-<task>-<id>` for older sessions), into the clone linked for its
workspace, without merging.

### From a script or agent

For scripts and coding agents. Skip it when you work by hand.

```sh
cloudfield session - --workspace myproject --agent claude < task.md
cloudfield wait run-d6ay38tj8j --timeout 30m
cloudfield logs run-d6ay38tj8j
cloudfield sessions run-d6ay38tj8j
cloudfield answer run-d6ay38tj8j req-7 allow-1
cloudfield publish run-d6ay38tj8j
cloudfield close session-d6ay38tj8j --outcome merged
cloudfield pull run-d6ay38tj8j
```

`cloudfield wait` exits 0 when the session finished, 1 when it failed, 3 when it needs
you by the [Needs you](failure-handling.md#needs-you) rule (a question, a
permission request, a `needs-input` or `blocked` report, or a disconnected
agent), 4 when it is parked with nothing pending, because its turn ended with
an `answered` report or no report or it went silent, and a message continues it (`cloudfield message
<session-id> "<reply>"`), 5 when its wait broke (a condition it waited on resolved
and the wake never reached the agent, or its source went unread for an hour; see [Waits Cloudfield can
check](failure-handling.md#waits-cloudfield-can-check)), and 124 on timeout.
Before exiting it prints `run
<session-id> <state>: <reason>`, with the state as `cloudfield sessions` labels it. It keeps waiting
while the session waits on agents, CI or a process. When Cloudfield cannot vouch for that wait
it says so once on stderr: `session <session-id> still waits, but <condition>: status
unknown (last read 12m ago): <error>`. It also exits 1 on its
own error, such as an unknown session id, with the error on stderr and nothing on
stdout. `cloudfield sessions --needs-you` lists the sessions waiting on you, and `cloudfield
sessions <session-id>` shows what one needs with the command that answers it. `cloudfield
answer` allows a permission request once or denies it once, never always.
`--json` on `cloudfield sessions <session-id>` and `cloudfield wait` prints the session as one JSON
object.

## Prove the plumbing without an agent subscription

The CLI-only agent `fake` runs a script from your repository, so you can try
the whole path without a vendor login. On the server, stop the service and run
it in the foreground with the script (a session's checkout is at `/workspace`):

```sh
sudo systemctl stop aether-server
sudo CLOUDFIELD_FAKE_AGENT="sh /workspace/agent.sh" cloudfield-server serve
```

On your computer, create a throwaway repository. `cloudfield link --repo` adds an
`aether` remote to it, and the push gives the workspace its base branch. Give
the script a few seconds before you close the session:

```sh
mkdir demo && cd demo && git init -b main
echo 'echo hello > result.txt' > agent.sh
git add -A && git commit -m seed
cloudfield workspace add demo --base main
cloudfield link <server-address-or-id> --workspace demo --repo "$PWD"
git push aether main
cloudfield session "write a result file" --workspace demo --agent fake
cloudfield close <session-id> --outcome merged
cloudfield pull <session-id>
```

It worked if the pulled branch has a commit that adds `result.txt`. Stop the
foreground server with Ctrl-C, then `sudo systemctl start aether-server`.

## Troubleshooting

| Error | Fix |
| --- | --- |
| `writing the unit and the config file needs root; re-run with sudo` | Run `sudo cloudfield-server setup`. |
| `Docker does not answer: ...`, then `Continue without Docker` | Install or start Docker on the server (`sudo systemctl start docker`), then run setup again. |
| `cli: dial <host>:2222: dial tcp <ip>:2222: connect: connection refused` | The server is not running, or the port is blocked. On the server, run `sudo systemctl enable --now aether-server` and check `systemctl status aether-server`. Open TCP 2222, or use the edge. |
| `not signed in to edge.cloudfield.ai; run: aether login --edge https://edge.cloudfield.ai` | Run `cloudfield login`, then link again. |
| `edgeproto: claim code must look like <26-character server id>-xxxxxxxxxxxxxxxx` | The code was cut short. Copy the whole `<server id>-<secret>`, or pass the claim link in single quotes: `cloudfield link --claim '<claim link>'`. |
| `claim code is wrong`, `claim code expired` or `claim code has no attempts left` | On the server, `sudo cloudfield-server edge claim-code` prints a new link. |
| `... refused: server is not connected to the edge (HTTP 503)` | On the server, `sudo cloudfield-server edge status` says why: the service is stopped, still starting, or cannot reach the edge. |
| `device "<label>", signed in as <account>, is waiting for approval` | The server admits approved devices only. An admin approves it from its **Needs you** card, or runs the command the message prints. |
| `no Cloudfield member for this key` | No member has this SSH key: someone else linked first, or you joined through the edge. Ask an admin to invite you ([teams.md](teams.md)), or link through the edge. |
| `gitengine: mirror no-candidate for branch "<branch>": revision <sha> is waiting for an admin to adopt it; ...`, or **The server's copy is pending** in the dashboard | The mirror's current revision was never adopted. Check that `<sha>` is the upstream commit you expect, then an admin runs the `cloudfield workspace mirror adopt` command the error ends with, or, on the workspace's **Repository** page, opens **Advanced → Review source mirror** and clicks **Adopt candidate**. |
| `gitengine: workspace branch "main" has no commit` | A local-only workspace has no base yet. Push one: `git push aether main`. |
| `workspace "myproject" not found` or `--workspace is required when more than one workspace exists` | Pass a name that `cloudfield workspace list` prints. |
| `[aether] harness exited with code 127` in a Standard session | The agent is not installed in your environment; set it up as in step 5. The session stays open in a shell. Any other code is the agent's own exit status. |
| **The agent is not signed in** in an Enhanced session | Log the agent in from a terminal in the session, then send your message again. |
| `scheduler: insufficient host memory headroom: ...` | The server keeps 2 GiB (or a tenth of its memory) free, plus 512 MiB for each session that is starting and, for each saved session VM that wakes, the memory it saved. Free memory, or wait for other sessions to start. The error can instead say `the server stalled on memory for <n>% of the last 60s`: the server is blocked on memory even though some is free. See [Recover from memory pressure](failure-handling.md#recover-from-memory-pressure). |
| `runrepo: gh exited <n>: ...` in **Publish…** | The session's account has no working `gh` login. Connect GitHub as in step 5, then click **Refresh status**. |
| `session <id> not found; aether sessions lists sessions` | Pass a session id that `cloudfield sessions` prints. |
| `no clone of workspace <id> is linked; in your clone of it run: aether link <server> --repo . --workspace <id>` | `cloudfield pull` writes to the clone linked for the session's workspace, not the current directory. Run the printed command in your clone, or `cloudfield workspace import <path>`. |

Later failures, such as reboots, full disks and stalls, are in
[failure-handling.md](failure-handling.md).

## Starting over

[install.md](install.md) has the removal order for the server, the client and
your clones. Session containers outlive the server unit; remove them separately. A
reinstalled server has a new host key, so linking by address fails with
`knownhosts: key mismatch` until you run `ssh-keygen -R '[<server-host>]:2222'`
and the same with its IP address.
