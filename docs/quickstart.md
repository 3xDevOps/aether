# Quickstart

This guide takes you from nothing to an agent's change published as a pull
request, in eight steps. Each step ends with how you know it worked. Aether's
terms are defined in the [glossary](README.md#concepts).
A coding agent can do these steps from the CLI: paste the prompt in
[agents.md](agents.md) into it.

You need:

- A Linux machine (amd64 or arm64) with Docker Engine and git, and at least
  2.5 GiB of available memory and 6 GiB of free disk: the **server**, where
  agents run. It needs no desktop and no open port
  ([prerequisites](install.md#server-prerequisites)).
- Your own computer (Linux, macOS or Windows): the **client**, where you work.
  The `aether` CLI and the desktop app run here and talk to the server.
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
curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh
```

At the `role` prompt, answer `server` on the server. It installs
`aether-server` and `aether` in `/usr/local/bin` with `sudo`, then starts the
setup in step 2. Answer `client` on your computer. It installs the `aether` CLI
in `~/.local/bin` without `sudo`, then builds the desktop app with `aether gui
build`, which downloads Electron and takes a few minutes. If `~/.local/bin` is
not on your `PATH`, the script prints the line that adds it.

On Windows, download and review the PowerShell installer, then run it. It
installs the CLI and the desktop app without administrator rights:

```powershell
Invoke-WebRequest -UseBasicParsing -Uri https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.ps1 -OutFile "$env:TEMP\aether-install.ps1"
powershell -NoProfile -ExecutionPolicy Bypass -File "$env:TEMP\aether-install.ps1"
```

It worked if `aether version` prints the release on your computer, and
`aether-server version` on the server. [install.md](install.md) covers every
platform, `--role none` (binaries only), upgrades and removal.

## 2. Start the server

Do this on the server. Answering `server` already ran setup. If you skipped it,
run `sudo aether-server setup`.

The **edge** is a relay the Aether project runs at `https://edge.onaether.dev`.
The server and your computer both dial out to it, so neither needs an open
port. SSH runs end to end through it: the edge cannot read your traffic, but
sees host names, IP addresses and who signs in ([edge.md](edge.md)). Setup turns
it on unless you answer yes to the tailnet question below;
`aether-server config set edge-url ""` turns it off.

Setup asks these questions. Enter accepts the default shown in brackets:

| Prompt | Answer |
| --- | --- |
| `Continue without Docker` | Asked only when Docker does not answer. Enter stops with nothing written. Install or start Docker, then run setup again. |
| `SSH listen address` | Enter keeps `:2222`. |
| `Data directory` | Enter keeps `/var/lib/aether`. |
| `Will people reach this server over the tailnet ...` | Asked only when Tailscale runs on this host. A **tailnet** is a private network made with Tailscale. Enter (no) uses the edge. Yes signs people in by their tailnet login ([networking.md](networking.md)). |
| `Choose 1 or 2` | Who may reach the server through the edge. `1`: anyone you invite, by signing in with GitHub. `2`: each new device also waits for someone to approve it; setup recommends it for a team workspace holding code or credentials that must stay protected. No default. |
| `Write it` | Enter writes `/etc/aether/server.conf` and the systemd unit. |
| `Let agents use Docker inside their runs?` | Enter installs Sysbox, the container runtime that lets agents run Docker inside their runs (Ubuntu and Debian only). |

Setup ends with a claim link and the commands left for you:

```
claim link: aether://claim?edge=https%3A%2F%2Fedge.onaether.dev&server=<server id>#<secret> (valid for 30 min, until 2026-10-25 15:04 UTC, 5 attempts)

next:
  1. start the server on this machine:
       sudo systemctl daemon-reload && sudo systemctl enable --now aether-server
  2. on your computer, open the claim link above with the Aether app, or paste it
     into the app's Connect page; in a terminal:
       aether login
       aether link --claim <server id>-<secret>
  3. to run agents from ChatGPT or Claude, on that computer:
       aether mcp
```

> [!WARNING]
> Whoever uses the claim link first becomes the server's administrator. It is
> valid for 30 minutes and 5 attempts. `sudo aether-server edge claim-code`
> prints a new one until someone has joined.

Item 1 starts the server: run it now, on the server. Item 2 links your
computer: that is step 3, so keep the claim link. Item 3 is optional
([connectors.md](connectors.md)); skip it for now.

Without the edge, item 2 is `aether link <this-host>:2222`. With the edge,
linking by address before the claim works only on the server or through an SSH
tunnel ([networking.md](networking.md#the-first-admin)).

It worked if `sudo aether-server edge status` shows `connection  connected since
<time>`, or without the edge, `systemctl status aether-server` shows `active`.

### Set up over ssh, for agents and scripts

To drive setup from an agent over ssh, with no terminal on the server (`sudo`
there must not ask for a password), run the steps non-interactively:

```sh
ssh <user>@<server> 'curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh -s -- --role none'
ssh <user>@<server> 'sudo aether-server install --edge-url https://edge.onaether.dev --edge-access <account|approved-devices>'
ssh <user>@<server> 'sudo systemctl daemon-reload && sudo systemctl enable --now aether-server'
ssh <user>@<server> "timeout 300 sh -c 'until sudo aether-server edge status >/dev/null; do sleep 5; done'"
ssh <user>@<server> 'sudo aether-server edge claim-code'
```

The access policy is the owner's choice (`account` or `approved-devices`, as
in the table above). If the wait times out, `sudo aether-server edge status`
prints the `connection` line that says why; fix that before you claim. The
claim code goes to step 3: `aether login`, then `aether link --claim <code>`.

## 3. Link from your machine

Do this on your computer, with the claim link from step 2 (without the edge, see
the end of this step). Linking saves the server in your client config. The first
person to link a fresh server becomes its **administrator** (admin); there is no
password. In the desktop app:

1. Open **Aether**. Until it is linked, it opens at **Onboarding → Connect**.
2. Paste the claim link into **Have an invitation or claim link?** and click
   **Open link**. Opening the `aether://claim` link on your computer does the
   same.
3. Click **Sign in**, open the address shown, sign in with GitHub, and confirm
   the code.
4. Click **Claim and link**.

Or in a terminal:

1. Run `aether login`, open the address it prints, sign in with GitHub, and
   enter the code on that edge page.
2. Run `aether link --claim '<claim link>'`, or
   `aether link --claim <server id>-<secret>`.

It worked if `aether link` ends with `linked to server <server id> through
edge.onaether.dev as <your name> (admin)`.

`aether status` shows where you are:

- What this computer is linked to: the edge it is signed in to, the server and
  your role.
- The workspaces, and which agents your environment has installed and whether
  their login file is there.
- The command for the next missing step. It is prefixed `an admin runs:` or
  `a human runs in a terminal:` when it is not yours or needs a terminal.

`aether status --json` prints the same as one JSON object. When the server
cannot be reached, `aether status` prints what this computer holds and the
error, then exits 1.

On a tailnet, or to an SSH port you can reach, link by address instead:
`aether link <server-host>:2222`. On a tailnet the server knows you by your
tailnet login. Elsewhere it registers your SSH key: `~/.ssh/id_ed25519` (created
when you have no key), a key in your ssh-agent, or `--key <path>`
([networking.md](networking.md)).

Optional: set the name and email your agents commit as (otherwise they commit
as your display name at `<member-id>@aether.local`):

```sh
aether member git --name "Ada Lovelace" --email ada@example.com
```

## 4. Create a workspace

A **workspace** is one repository, its **base branch** (the branch new runs
start from), and the runs started from it. Only an admin can create one.

Open the **dashboard**, the web app where you work with Aether. The desktop
app shows it, and so does `aether gui`, which serves it in a browser tab from
your computer. It shows **Onboarding** (**Connect**, **Repository**, **Agent**,
**First run**, which match steps 3 to 6 here) until you finish. If you linked
in a terminal, it opens at the **Board** instead; click **Add your repository**
there. **Settings → General → Setup guide → Open guide** reopens onboarding.

### GitHub repository

1. In **Onboarding → Repository**, under **From GitHub**, click **Add GitHub
   repository**.
2. Click **Connect GitHub**, then **Copy code and open GitHub**. Enter the code
   on GitHub and authorize **GitHub CLI**, which Aether uses to connect. The
   dialog shows **Connected as `<login>`** by itself.
3. Choose a repository. Private and organization repositories need no deploy
   key.
4. Check the workspace name, the branch, and **Publish changes to** (where runs
   push). **Advanced** changes them: pick a fork, or **None** to not publish.
5. Click **Review repository**. The server creates the workspace and fetches
   the branch.
6. Check the **Revision**, then click **Use repository**. That exact commit
   becomes the workspace's base.

It worked if onboarding moves on to **Agent**, and the workspace is in the
sidebar's workspace switcher.

The GitHub connection is stored in your home on the server (step 5 explains
it); runs you launch push with it, and later fetches use it. Members see the
code, not your credentials. If the fetch fails, the workspace still exists:
click **Retry fetch**, or **Source settings** to reconnect. **Settings → GitHub
→ Add repository** adds another repository later.

### Other sources

| Source | In **Add a workspace** |
| --- | --- |
| Public Git URL | **From a Git URL → Import repository**, then **Public HTTPS**. |
| Private Git URL | Same form with **Read-only deploy key**. Install the public key it shows at the Git host, then **Verify** and **Adopt candidate**. |
| A clone on your computer | **From a local clone → Create from local clone**, then push the base branch. Needs the desktop app or `aether gui` on that computer. Or [from the CLI](#from-the-cli). |

[teams.md](teams.md) covers each source. A workspace can also run a setup script
(such as `npm ci`) and set variables in new runs: **Environment** on its
**Repository** page, or `aether workspace env`
([environments.md](environments.md#workspace-environment)).

### From the CLI

```sh
aether workspace import ~/code/myproject
aether workspace import https://github.com/acme/myproject
```

A path creates a workspace named after the clone, with its checked-out branch as
the base branch, adds the `aether` remote and pushes the base branch, ending
with `pushed <branch> to workspace <name>`. A GitHub URL makes the server fetch
the default branch with your GitHub connection and print the
`aether workspace mirror adopt` command that makes the fetched commit the base;
review the commit first. Both are safe to re-run: a clone already imported is
linked to its existing workspace.

## 5. Set up your agent

An **agent** is the coding CLI a run starts: Claude Code, Codex, OpenCode, pi,
oh-my-pi, GitHub Copilot CLI, Gemini CLI, Cursor CLI or Antigravity CLI. Agents
run on the server, not on your computer. You install each one in your
**environment**, your own container there, with the vendor's own login. Your
**home** there persists, so this is done once.

1. In **Onboarding → Agent**, or on the **Agents** page, click **Set up**
   beside an agent.
2. Choose how runs show it. **Standard** is the agent's own terminal, as on
   your computer. **Enhanced** shows its messages, tool calls, file changes and
   approvals as dashboard controls; it misses some agent-specific commands and
   starts a few seconds slower ([enhanced-runs.md](enhanced-runs.md)). In both,
   the agent acts without asking by default; the Enhanced card says whether it
   can be made to ask.
3. Click **Install `<agent>`**. It runs the vendor's install command in your
   environment. A failure shows **Install failed** and the command's output.
4. Log in, in the terminal that opens. The login command is already typed, for
   example `claude` (then `/login`) or `codex login --device-auth`.
5. Click **Check**.

It worked if the dialog shows **Installed** and **Login found**, and after
**Done** the agent's button reads **Run**. Aether only looks for the login file;
the agent proves the login when a run starts.

From the CLI, on your computer. The environment has no `aether` command, so do
not run these in its terminal:

```sh
aether agent add codex
aether agent login codex
aether agent list
aether account usage
```

Without a terminal (an agent's shell, CI), `agent add` installs the agent and
prints the end of the install log. `agent login` prints a device code for
Codex; Claude Code and the others need the human at a terminal, or an API key
([harnesses.md](harnesses.md#use-an-api-key)). `aether account usage` asks the
vendor whether the login works. Logins and limits per agent:
[harnesses.md](harnesses.md).

### Upload agent configuration

Optional: copy your own agent settings to the server.

Settings, instruction files such as `CLAUDE.md`, commands, skills, hooks and MCP
configuration live in a directory such as `~/.claude`; runs read the same path
in your home on the server. To copy yours, open **Agents → Agent config files**,
click **Copy prompt**, and paste it into an agent on the computer that has your
configuration (it needs the `aether` CLI, linked). That agent previews with
`aether config push --dry-run`, asks you, then runs `aether config push`.
Logins, tokens, history, and files the secret scanner flags are never uploaded,
and only `--overwrite` replaces a different server file. Edit uploaded files on
**Files → Home**. [Upload and edit
configuration](environment-home.md#upload-and-edit-configuration) lists the
commands, what is left out and the errors.

### Connect GitHub for publishing

Runs push and open pull requests with the GitHub login in your home. If you
connected GitHub in step 4, skip this. Otherwise click **Connect GitHub** under
**Agents → GitHub**, or run `aether github connect` on your computer. It prints
a code, waits while you approve it at github.com/login/device, then finishes
the setup ([environment-home.md](environment-home.md#from-the-cli)).

## 6. Launch a run

A **run** is one agent on one task, in its own container and on its own git
branch. It uses your home, so your agent login and configuration come along.

In the dashboard, click **New run**, or continue to onboarding's **First run**.
Enter the **Task**, pick the **Agent** and the **Mode**, and click **Launch**.
**Background** mode runs the task once with no interaction. From the CLI:

```sh
aether run "add a health check endpoint" --workspace myproject --agent claude
```

It prints `run run-d6ay38tj8j running`. `--mode` picks `standard`, `enhanced`
or `background`; without it, the run is Enhanced when the agent's Enhanced
mode is installed, and Standard otherwise. `--model` picks the model and
`--effort` the effort or thinking level
([harnesses.md](harnesses.md#choose-an-effort)).
The first run on a server pulls the environment image, so it starts slower. To
give one objective to a group of agents, see swarms in [teams.md](teams.md).

Every launch carries an idempotency key. If the call fails or you press
Ctrl-C, the CLI prints the exact retry with `--idempotency-key`; running it
returns the same run instead of starting another.

It worked if the run is under **Working** on the **Board**, the dashboard's
page of runs, and `aether runs` lists it.

### Working across repos

Optional: skip it on your first run, and go on to step 7.

A run belongs to one **primary** workspace, checked out at `/workspace`. Add
`--repo <workspace>` (repeatable) to check out other existing workspaces beside
it:

```sh
aether run "rename the /users endpoint in the API and the web client" \
  --workspace web --repo api --repo shared-types --agent claude
```

Each extra repo is cloned to `/repos/<workspace name>` (characters outside
`A-Za-z0-9._-` become `-`), on its own run branch `aether/run-<task>-<id>` in
its own workspace. The agent's startup prompt lists the extra paths. The
dashboard shows each repo on the run page, and the per-run Git, pull request
and **Changes** methods take a `repo` (the repo's workspace ID) to pick one;
without it they act on the primary repo.

A launch is all or nothing. If any repo cannot be checked out, no run starts
and the error names that repo:

```
run run-d6ay38tj8j failed: provisioning: create checkout: repo "api" (workspace 01m2xwb3es63c0cs0e7aewqpeq): gitengine: base commit "..." is unavailable
```

Limits: extras must already exist as workspaces and cannot repeat or include the
primary one. The setup script, environment, agent account and budget are the
primary workspace's. `aether pull <run>` fetches each repo's branch into the
clone linked for its workspace and skips, naming it, an extra repo with no
linked clone. Evidence packets cover the primary repo only, and the dashboard's
Compare menu is the primary repo's.
Extra branches are published to their own workspaces; closing the run commits
and publishes every repo. Each pull request is per repo; there is no cross-repo
merge. A swarm takes extra repos the same way, for its integrator and every
worker ([teams.md](teams.md#launch-a-swarm)); templates use one workspace.

## 7. Watch it

The **Board** groups runs into **Needs you**, **Working** and **Finished**.
Open a run to see its views:

| View | Shows |
| --- | --- |
| **Session** | An Enhanced run's messages, tool calls and approvals, with a message box |
| **Terminal** | A Standard run's agent terminal, and extra shells in its container |
| **Changes** | The diff against the base branch |
| **Files** | The run's checkout; **Save** writes your edit into it, and **Upload files…** or dropping files adds files to it |
| **Browser** | A Chromium on the server beside the run, where `localhost` is the run's own app |

From the CLI, on your computer:

- `aether message <run-id> "also update the README"` messages the agent and
  prints the receipt (`delivered`, or `queued` for the member in control).
- `aether logs <run-id>` prints what the agent did as plain text.
- `aether attach <run-id>` attaches your terminal to a Standard run's agent.
  Every key, Ctrl-C included, goes to the agent; close the terminal to detach,
  and the run keeps going. `aether attach --read-only <run-id>` only watches.

For VS Code, Cursor or Zed, run `aether ssh-config` once, then **Open in
editor…** in the run's **More** menu ([terminal.md](terminal.md)). On a phone,
use the [Android app](install.md#android-app) or the tailnet dashboard
([networking.md](networking.md#the-dashboard)).

## 8. Review and publish the result

1. Open the run's **Changes**. To ask for changes, click **+** beside a line
   number, write what should change and click **Comment**. **Send to agent**
   sends all your comments as one message.
2. Click **Publish…**. The first line shows the branch, the commit, and the
   GitHub login the push uses, or that login's error.
3. If the agent left uncommitted work, select the paths, click **Review
   selected paths**, write a **Commit message**, click **Commit selected**,
   then **Continue to push**.
4. Check the **PR title** and **PR description**, and click **Push and open
   pull request**. It pushes the run's branch without force and opens a pull
   request into the base branch.

It worked if the dialog shows **Pushed: yes** and the pull request. The run page
then shows the pull request number and its checks, such as **2 failing**; a
failing check has **Send to agent**. Review and merge on GitHub as usual.

`aether publish <run-id>` is the CLI form of **Publish…**.

When you are done, close the run: **Close run…** in its **More** menu, or
`aether close <run-id> --outcome merged` (or `abandoned`). Closing commits the
agent's latest work to the run's branch and keeps the container 7 days
(`--run-container-ttl`). Until then the **Relaunch** button where the message
box sat, **Relaunch** in the **More** menu, or `aether reopen <run-id>` brings
the run back with its chat and files. The 7 days stop counting while the run is
live again and start over when you close it.

`aether pull <run-id>` fetches the branch, `aether/run-<task>-<id>`, into the
clone linked for its workspace, without merging.

### From a script or agent

For scripts and coding agents. Skip it when you work by hand.

```sh
aether run - --workspace myproject --agent claude < task.md
aether wait run-d6ay38tj8j --timeout 30m
aether logs run-d6ay38tj8j
aether runs run-d6ay38tj8j
aether answer run-d6ay38tj8j req-7 allow-1
aether publish run-d6ay38tj8j
aether close run-d6ay38tj8j --outcome merged
aether pull run-d6ay38tj8j
```

`aether wait` exits 0 when the run finished, 1 when it failed, 3 when it needs
you by the [Needs you](failure-handling.md#needs-you) rule (a question, a
permission request, a `needs-input` or `blocked` report, or a disconnected
agent), 4 when it is parked with nothing pending, because its turn ended with
an `answered` report or no report or it went silent, and a message continues it (`aether message
<run-id> "<reply>"`), 5 when its wait broke (a condition it waited on resolved
and the wake never reached the agent, or its source went unread for an hour; see [Waits Aether can
check](failure-handling.md#waits-aether-can-check)), and 124 on timeout. Before exiting it prints `run
<run-id> <state>: <reason>`, with the state as `aether runs` labels it. It keeps waiting
while the run waits on agents, CI or a process, and on a swarm worker's
question, which its integrator answers. When Aether cannot vouch for that wait
it says so once on stderr: `run <run-id> still waits, but <condition>: status
unknown (last read 12m ago): <error>`. It also exits 1 on its
own error, such as an unknown run id, with the error on stderr and nothing on
stdout. `aether runs --needs-you` lists the runs waiting on you, and `aether
runs <run-id>` shows what one needs with the command that answers it. `aether
answer` allows a permission request once or denies it once, never always.
`--json` on `aether runs <run-id>` and `aether wait` prints the run as one JSON
object.

## Prove the plumbing without an agent subscription

The CLI-only agent `fake` runs a script from your repository, so you can try
the whole path without a vendor login. On the server, stop the service and run
it in the foreground with the script (a run's checkout is at `/workspace`):

```sh
sudo systemctl stop aether-server
sudo AETHER_FAKE_AGENT="sh /workspace/agent.sh" aether-server serve
```

On your computer, create a throwaway repository. `aether link --repo` adds an
`aether` remote to it, and the push gives the workspace its base branch. Give
the script a few seconds before you close the run:

```sh
mkdir demo && cd demo && git init -b main
echo 'echo hello > result.txt' > agent.sh
git add -A && git commit -m seed
aether workspace add demo --base main
aether link <server-address-or-id> --workspace demo --repo "$PWD"
git push aether main
aether run "write a result file" --workspace demo --agent fake
aether close <run-id> --outcome merged
aether pull <run-id>
```

It worked if the pulled branch has a commit that adds `result.txt`. Stop the
foreground server with Ctrl-C, then `sudo systemctl start aether-server`.

## Troubleshooting

| Error | Fix |
| --- | --- |
| `writing the unit and the config file needs root; re-run with sudo` | Run `sudo aether-server setup`. |
| `Docker does not answer: ...`, then `Continue without Docker` | Install or start Docker on the server (`sudo systemctl start docker`), then run setup again. |
| `cli: dial <host>:2222: dial tcp <ip>:2222: connect: connection refused` | The server is not running, or the port is blocked. On the server, run `sudo systemctl enable --now aether-server` and check `systemctl status aether-server`. Open TCP 2222, or use the edge. |
| `not signed in to edge.onaether.dev; run: aether login --edge https://edge.onaether.dev` | Run `aether login`, then link again. |
| `edgeproto: claim code must look like <26-character server id>-xxxxxxxxxxxxxxxx` | The code was cut short. Copy the whole `<server id>-<secret>`, or pass the claim link in single quotes: `aether link --claim '<claim link>'`. |
| `claim code is wrong`, `claim code expired` or `claim code has no attempts left` | On the server, `sudo aether-server edge claim-code` prints a new link. |
| `... refused: server is not connected to the edge (HTTP 503)` | On the server, `sudo aether-server edge status` says why: the service is stopped, still starting, or cannot reach the edge. |
| `device "<label>", signed in as <account>, is waiting for approval` | The server admits approved devices only. An admin approves it from its **Needs you** card, or runs the command the message prints. |
| `no Aether member for this key` | No member has this SSH key: someone else linked first, or you joined through the edge. Ask an admin to invite you ([teams.md](teams.md)), or link through the edge. |
| `gitengine: mirror no-candidate`, or **The server's copy is pending** in the dashboard | The workspace's first revision was never accepted. On its **Repository** page, open **Advanced → Review source mirror** and click **Adopt candidate**. |
| `gitengine: workspace branch "main" has no commit` | A local-only workspace has no base yet. Push one: `git push aether main`. |
| `workspace "myproject" not found` or `--workspace is required when more than one workspace exists` | Pass a name that `aether workspace list` prints. |
| `[aether] harness exited with code 127` in a Standard run | The agent is not installed in your environment; set it up as in step 5. The run stays open in a shell. Any other code is the agent's own exit status. |
| **The agent is not signed in** in an Enhanced run | Log the agent in from a terminal in the run, then send your message again. |
| `scheduler: insufficient host memory headroom: ...` | The server keeps 2 GiB (or a tenth of its memory) free, plus 512 MiB for each run that is starting. Free memory, or wait for other runs to start. |
| `runrepo: gh exited <n>: ...` in **Publish…** | The run's account has no working `gh` login. Connect GitHub as in step 5, then click **Refresh status**. |
| `run <id> not found; aether runs lists runs` | Pass a run id that `aether runs` prints. |
| `no clone of workspace <id> is linked; in your clone of it run: aether link <server> --repo . --workspace <id>` | `aether pull` writes to the clone linked for the run's workspace, not the current directory. Run the printed command in your clone, or `aether workspace import <path>`. |

Later failures, such as reboots, full disks and stalls, are in
[failure-handling.md](failure-handling.md).

## Starting over

[install.md](install.md) has the removal order for the server, the client and
your clones. Run containers outlive the server unit; remove them separately. A
reinstalled server has a new host key, so linking by address fails with
`knownhosts: key mismatch` until you run `ssh-keygen -R '[<server-host>]:2222'`
and the same with its IP address.
