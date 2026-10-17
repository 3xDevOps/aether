# Quickstart

Aether runs coding agents on a **headless Ubuntu server** with Docker and git.
Repository fetches, builds, and run tools execute there; the server needs no
desktop session.

Choose where your code comes from:

| Starting point | Onboarding path |
| --- | --- |
| GitHub repository (public or private) | [From GitHub](#github-repository): connect in the browser, choose a repository, and accept its revision. |
| Other public remote repository | [Import public HTTPS](#public-remote-repository); no local clone needed. |
| Private remote with a deploy key | [Manual deploy-key import](#private-remote-repository); a repository administrator must install the key. |
| Existing local clone | [Create or link a workspace](#local-clone) from the desktop app, `aether gui`, or CLI on the computer holding the clone. |

A **workspace** holds one repository, its base branch, and its runs. A **run**
is one agent execution with its own container, git worktree, and branch.
Creating a workspace or managing a remote source requires an Aether admin.
Collaborators can link a clone to an existing workspace, push a local-only
base, configure their own agents, and launch runs; viewers cannot.

If a server is already available, open its authenticated dashboard and start
with [workspace onboarding](#4-create-a-workspace). Otherwise install and link
below. A local client works on Linux, macOS, or Windows and needs git for
clone linking and pushing. The hosted dashboard can import remote repositories
and edit agent configuration, but cannot read a path on your computer or use
its Git/SSH identity.

---

## 1. Install

On the server box and on a Linux or macOS machine:

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh
```

The script asks what this machine is. Answer **server** on the server box and
**client** on your own machine; Enter takes the sensible default.

That answer decides what you get. A server gets `aether` and `aether-server`
in `/usr/local/bin`, with `sudo`. A client gets the `aether` CLI alone in
`~/.local/bin`, without `sudo`, so the desktop app can replace it when it
updates; if that directory is not on your `PATH`, the script prints the one
line that adds it, and the app finds it either way. macOS is a client
platform, so it only ever gets `aether`. Later, `aether update` upgrades
a client and `sudo aether-server update` a server.

It then finishes that side's setup: [step 2](#2-start-the-server) on the
server, the desktop app ([step 7](#prefer-a-native-window)) on a client. To
install the binaries and stop there, add `--role none`; see
[install.md](install.md#the-install-script).

On **Windows**, download and review the PowerShell installer, then run it:

```powershell
Invoke-WebRequest -UseBasicParsing -Uri https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.ps1 -OutFile "$env:TEMP\aether-install.ps1"
& "$env:TEMP\aether-install.ps1"
```

It installs the CLI and desktop app by default, without administrator access.
Use `-Role none` for the CLI alone; that does not create a desktop shortcut.
Desktop setup requires `v0.5.1-alpha.4` or newer for the Windows build and
recorded-CLI fixes; older desktop builds are refused. The desktop stays in
`%LOCALAPPDATA%\Programs\Aether Desktop`, separate from the CLI. **Aether** is
registered in the current user's Windows Programs known folder, including
folder redirection, not a guessed path under `%APPDATA%`. See
[install.md](install.md#windows-install-script) for fresh installs, reinstalls,
script policy and version selection, and
[shortcut diagnosis](install.md#windows-start-menu-shortcut-diagnosis) to
inspect the real `Aether.lnk` and repair it with the explicit installed CLI.
Unsigned downloads and locally built desktops can still trigger SmartScreen
or Defender; inspect the actual warning/publisher and organization policy
rather than bypassing protection.

## 2. Start the server

Answering **server** in step 1 already ran the command below on the server
box. It writes the config and the systemd unit but deliberately starts
nothing, so the activation line it printed is still yours to run. Run setup by
hand if you skipped the question:

```sh
sudo aether-server setup
```

It asks for the listen address, data directory, and tailnet policy - plus, on
a host that already runs tailscaled, the dashboard's HTTPS port on the tailnet
and whether to use the edge (Enter accepts each default) - and, with the edge
on, who may reach the server through it, a question with no default. It also
offers to install Sysbox on Ubuntu and Debian, which lets agents run Docker
inside their runs ([environments.md](environments.md#docker-inside-runs)).
Then it prints:

```sh
systemctl daemon-reload && systemctl enable --now aether-server
```

On a host without tailscaled, setup turns on the **edge**: a relay the Aether
project runs at `https://edge.onaether.dev` that the server and your machine
both dial out to, so neither needs an open port ([edge.md](edge.md)). Setup
prints what the edge can see and `aether-server config set edge-url ""`,
which turns it off. It then asks for the **access policy**: `1` (account
access) lets people you invite in by signing in with GitHub; `2`
(approved devices) makes each new device wait until a person approves it.
[edge.md](edge.md#access-policies) compares them. Setup then prints the
server's id and a **claim link**, which makes whoever uses it first the
server's admin. The claim code inside it, `<server id>-<secret>`, is what
`aether link --claim` takes:

```
edge: https://edge.onaether.dev
edge access: approved-devices
server id: <server id>
edge key: pinned when the server first connects; `aether-server edge status` shows it
claim link: aether://claim?edge=https%3A%2F%2Fedge.onaether.dev&server=<server id>#<secret> (valid until 3:04PM, 5 attempts)
open it on your computer with the Aether app, or paste it into the app's Connect page; in a terminal:
  aether link --claim <server id>-<secret>
```

Run the activation line and the server is live on `:2222`. The SSH port is
the only thing it listens on unless you answered the dashboard question; the
edge connection is outbound. The claim code lasts 30 minutes;
`sudo aether-server edge claim-code` prints a new link until someone has
joined the server. Change any option
later with `aether-server config set <key> <value>`, then restart.

To try it in the foreground first, `sudo aether-server serve` runs until
Ctrl-C. [install.md](install.md) covers unattended installs, running
unprivileged, and every serve option.

## 3. Link from your machine

**Through the edge**, open the claim link from step 2 on your computer. The
desktop app ([install.md](install.md#desktop-app)) opens at its Connect
step with the link filled in: sign in with GitHub when it asks, check the
account it shows, and click **Claim and link**. The Connect step also
accepts the link pasted. In a terminal, use the claim code:

```sh
aether login
aether link --claim <code>
```

`aether login` prints an address and a short code; open it, sign in with
GitHub, and confirm the code:

```
auth.onaether.dev signs you in for the edge edge.onaether.dev
open https://auth.onaether.dev/device and enter the code <user code>
waiting for you to confirm the code...
signed in to edge.onaether.dev as <login> (github); this device is "<host name>"
the device token in <config dir>/edge-tokens.json does not expire and is not refreshed. aether logout revokes it at
the edge and deletes it here; the edge's Devices page and deleting the account revoke it too. Your links stay:
the edge path refuses with the edge's reason, and a link's --addr still reaches the server with this device's
key until the account is deleted, which removes the account's devices on every server. SSH keys and tailnet
identities are not affected.
claiming server <server id> as <login> (github), the account edge.onaether.dev reported when this device signed in
claimed server <server id>
linked to server <server id> through edge.onaether.dev as <your name> (admin)
```

This machine now holds its own device key; no SSH key is involved. The
claim code travels inside SSH, only to a server whose host key derives the
id in the code. The server's host key is checked against the server id on
every connection, so nothing is written to `known_hosts`.

**By address**, over a tailnet or to a reachable SSH port:

```sh
aether link <server-host>:2222
```

```
linked to <server-host>:2222 as admin (admin)
```

**The first identity to link a fresh server becomes the admin**, and
through the edge the claim code decides who that is. That is the whole
account setup - there is no password and no config file to edit. The link is
saved to `~/.config/aether/config.json`, or `%AppData%\aether\config.json`
on Windows. (Joining over a tailnet, the display name comes from your
tailnet login instead of the literal `admin`; the role is the same. Change
any display color with `aether member color <#rrggbb>`.)

How you were identified by address depends on the network:

- **On a tailnet:** Tailscale already knows who you are and the server asks it.
  No SSH key, no invite code, nothing to copy. See
  [networking.md](networking.md).
- **Anywhere else:** your SSH public key (`~/.ssh/id_ed25519`, or any key in
  your ssh-agent) is registered as the admin's key. With no key to offer,
  `aether link` creates `~/.ssh/id_ed25519` itself. For a key somewhere else,
  pass `--key <path>`; for a passphrase-protected one, `ssh-add` it first. On
  Windows the path is `%USERPROFILE%\.ssh\id_ed25519` with the OpenSSH agent
  service ([install.md](install.md#the-windows-client)).

On first contact by address `aether` records the server's host key in
`~/.ssh/known_hosts` (`%USERPROFILE%\.ssh\known_hosts` on Windows) and
prints its fingerprint. Compare that against what the server printed if you
care to.

Then tell Aether who to put on your commits:

```sh
aether member git --name "Ada Lovelace" --email ada@example.com
```

Commits Aether makes for your runs use that name and address. Without it, the
fallback is your display name at `<member-id>@aether.local`. The local
dashboard shows these fields at the bottom of onboarding's **Connect** step,
prefilled from this computer's `git config`; the hosted dashboard shows them at
the top of **Repository**, prefilled from your member record. **Save identity**
stores them. Change them later in **Profile**, from the menu under your name at
the bottom of the sidebar (**Agents → Git identity** links there), or inspect
them with `aether member git`. Author identity is not repository
authentication.

## 4. Create a workspace

The dashboard opens **Onboarding** until you finish it; later, open it from
**Settings → Onboarding → Open guide**, which resumes at the furthest step
you reached. The local dashboard lists four steps: **Connect**,
**Repository**, **Agent** and **First run**, starting at **Connect**. The hosted
dashboard omits the machine-local **Connect** step and starts at
**Repository**. It opens onboarding for new members even when shared
workspaces already exist.

A **workspace** is one repository and base branch, and the runs started from
it. **Repository** lists your workspaces with **Use**. Under **Add a workspace**,
admins can choose **From GitHub**, or the alternatives **Import a remote
repository** and **From a local clone**. A member who is not an admin and finds
no workspace sees **Ask an admin to add a workspace** and can still **Continue
to Agent**. Once a workspace is chosen, **Change** returns to the list.

For another repository, open **Settings → GitHub → Add repository**; it reuses
your connection. Or open **Manage workspaces** from Settings, the workspace
selector, or command palette (**Ctrl/Cmd+K**), then **Add workspace → Add
GitHub repository**. Manual remote and local choices remain available.
An existing workspace's repository page (**Repository** in the workspace
selector) links a clone and manages the source.

The **base branch** is the branch new runs start from. The GitHub picker uses
the repository's actual default branch; manual forms need you to supply the
correct branch. Creating an empty workspace does not upload code.

### GitHub repository

On an already connected server, an Aether admin can do this entirely in either
dashboard:

1. Open **Onboarding → Repository** and choose **Add GitHub repository** under
   **From GitHub**. To reopen onboarding, use **Settings → Onboarding → Open
   guide**.
2. If GitHub is not already connected, choose **Connect GitHub**, then **Copy
   code and open GitHub**. Enter the displayed code and approve **GitHub CLI**,
   which Aether uses to connect this server. If the clipboard or new tab is
   blocked, copy the visible code and use **Open GitHub**. Completion is automatic.
   Wait for **Connected as …**; there is no terminal or **I've logged in** step.
3. Choose a repository. **Find a repository** filters the loaded list; use
   **Load more** if needed. Private repositories you can read, including
   collaborator and organization repositories, do not require repository-admin
   permission or a deploy key. Organization approval and SSO policy still apply.
4. The workspace name and base branch come from the repository. When you can
   write to it, it is also shown as the publishing destination. Review that
   destination before continuing. Under **Advanced**, change the name or branch,
   choose another writable repository or fork, or select **None — do not
   publish**. Read-only sources have no publishing destination by default.
5. Choose **Review repository** to create the workspace and fetch the branch.
   Review the displayed branch and actual **Revision**, then choose **Use
   repository** to accept that exact revision as the initial base. The
   repository is now ready, and onboarding continues to **Agent**.

Your agent vendor login and edge/server sign-in remain separate from this
GitHub connection. The server needs its normal Docker runtime and Git, and
your member Environment needs `gh` 2.81.0 or newer; the current standard image
includes it. See [Environment prerequisites and setup](environment-home.md#connect-github).
Imported code is shared with workspace members, and future source fetches
depend on the authorizing member's connected GitHub account; see
[source sharing and account dependence](security.md#workspace-source-mirrors).

**If something goes wrong:** use **Cancel connection** to stop a pending
authorization; after cancellation or expiry, **Connect GitHub** gives a new
attempt. Use **Check connection** and **Reload repositories** after access
changes. For an empty repository, add an initial commit on GitHub and reload.
If a workspace was created but fetching failed, keep it: use **Retry fetch**,
then review and **Use repository**. An authentication failure offers **Source
settings** to reconnect the authorizing account; deliberately save source
settings to change the bound account or branch. If an operation's result is
uncertain, use **Check source status**, or close the dialog and inspect
**Manage workspaces** before creating anything again.

### Manual source and publishing settings

The manual import form's **Advanced** settings keep these separate:

- **Source authentication** lets the server read an upstream branch into its
  **source mirror**. A deploy key is read-only and is not your Git/`gh` login.
- **Checkout Origin** is the publishing destination placed in new run
  checkouts. Supply a writable repository or fork, or leave it blank.
- **Native Git/`gh` credentials** and upstream write permission let a run push
  and open a PR. The admin GitHub connection configures these automatically;
  collaborators and CLI users can use [native setup](#connect-github).
  Git author identity and agent vendor login are separate again.

### Public remote repository

In **Onboarding → Repository** or **Manage workspaces**:

1. Choose **Import repository** on the **Import a remote repository** card.
2. Fill **Workspace name**, a credential-free HTTPS **Source URL**, and the
   actual **Source / base branch**. Set **Checkout Origin (optional)**
   separately if runs should publish upstream.
3. Choose **Public HTTPS** under **Source authentication**, then **Import
   repository**. The server creates the workspace and fetches the source.
4. Read **Import outcome**, then **Continue to Repository**, which opens
   **Workspace Source**. Review the observed commit and generation, choose
   **Adopt candidate**, and confirm.
   The first fetched candidate is not automatically accepted.

CLI alternative for a new workspace; replace the repository and branch:

```sh
aether workspace init myproject --base main
aether workspace mirror configure --workspace myproject \
  --source https://github.com/acme/myproject.git --branch main --auth public
aether workspace mirror refresh --workspace myproject
aether workspace mirror status --workspace myproject
```

Unlike dashboard import, CLI `configure` does not fetch until `refresh`.
After reviewing the returned candidate, adopt its generation and full observed commit:

```sh
aether workspace mirror adopt --workspace myproject --generation <n> --commit <reviewed-sha> --yes
```

Use the generation and full observed commit from the state you reviewed. If
adoption reports a conflict, keep the workspace, refresh its status, and explicitly
review the current candidate before adopting again; never substitute a newer SHA
without review.

### Private remote repository

For GitHub, prefer [From GitHub](#github-repository) above. For manual key-based
access, use the **Import repository** form, choosing **Read-only deploy key**.
Use a GitHub HTTPS source or a generic `ssh://` source. For generic SSH,
**Pinned known_hosts (required for generic SSH)** must contain the host key
verified with the host administrator; do not blindly trust `ssh-keyscan`.
GitHub uses Aether's pinned host key.

1. Import creates the workspace and a public deploy key, without fetching.
   Open **Continue to Repository**, then **Copy public key**.
2. For GitHub, follow **Install this key in GitHub deploy keys**, or open the
   repository's **Settings → Deploy keys → Add deploy key**. Paste the key and
   leave **Allow write access** off. Other hosts need their corresponding
   read-only repository access setup.
3. Return to **Workspace Source** and click **Verify** (or **Refresh** once
   ready). Review **Observed SHA** and **Accepted SHA**, then **Adopt
   candidate** and confirm the initial candidate.

An Aether admin role does not grant permission to install keys at the source.
Your repository administrator must approve the key and any enterprise policy;
the Aether server must be able to reach that Git host.

CLI alternative for a new private workspace:

```sh
aether workspace init myproject --base main
aether workspace mirror configure --workspace myproject \
  --source https://github.com/acme/private.git --branch main --auth deploy-key
```

For generic SSH, use this `configure` command instead, with a verified file:

```sh
aether workspace mirror configure --workspace myproject \
  --source ssh://git@git.example.com/acme/myproject.git \
  --branch main --auth deploy-key --known-hosts-file ~/.ssh/known_hosts
```

Install the public key printed by `configure`, then:

```sh
aether workspace mirror refresh --workspace myproject
aether workspace mirror status --workspace myproject
aether workspace mirror adopt --workspace myproject --generation <n> --commit <reviewed-sha> --yes
```

Replace `<n>` and `<reviewed-sha>` with the generation and full observed commit
you reviewed from `status`. A conflict requires fresh status and explicit review
before another adoption. After installing a key, use **Verify** / **Refresh**,
not **Save source** or `configure`:
reconfiguration rotates the key and generation. To recover a misplaced public
key, reopen the source mirror on the workspace's repository page or run
`mirror status` on the same workspace.

### Remote import and source recovery

**Created: yes** means the workspace exists even if configuration or fetch
failed. Keep its name/ID and repair it on the workspace's repository page;
do not import a duplicate. If the response was lost, inspect **Manage
workspaces** or `aether workspace list` before trying creation again.
Switching members or servers, or closing the import dialog, does not cancel
a pending import on the original server. Its late result cannot update the
new dashboard context; return to the original server and check its workspaces
before importing again.

For a source branch named `main`, failures include:

| Error | Recovery |
| --- | --- |
| `gitengine: mirror auth-failed for branch "main": upstream authentication failed; check source access and the mirror deploy key` | Install or restore the current public key at the source, then **Verify**. |
| `gitengine: mirror source-missing for branch "main": upstream branch was not found; verify the configured branch` | Check the repository and branch. Restore the branch or deliberately correct the source configuration; reconfiguration rotates a deploy key. |
| `gitengine: mirror auth-failed for branch "main": host key verification failed; verify the mirror's pinned host key` | Verify the host identity with its administrator. Do not disable host checking. Restore the trusted endpoint or deliberately reconfigure the verified pin and install the newly generated deploy key. |

A failed authenticated fetch or host-pin check reports the failure while
retaining known accepted/observed commit metadata. That is not a successful
refresh: new launches do not silently use the retained base. Restoring source
access and refreshing the unchanged configuration keeps the same workspace,
key, and generation.

After initial adoption, forward-only source updates advance the mirrored
base. Rewrites or divergence require another reviewed **Adopt candidate**.
Every launch refreshes a configured source. If a refusal reports an accepted
commit, an explicit one-request override is available:

```sh
aether run "add a health check endpoint" --workspace myproject --agent claude \
  --cached-base <accepted-commit>
```

The SHA must still match the accepted commit and unmoved workspace base.
Later launches do not inherit the override. **Disable source** returns the
workspace to local-only mode; it does not revoke the key at the Git host.
See [workspace source operations](teams.md#workspaces) and the
[gateway API](local-gateway.md#control-channel-methods-this-gateway-calls).

### Local clone

On the computer holding the clone, open the desktop app or run `aether gui`
after [linking to the server](#3-link-from-your-machine).

1. In **Onboarding → Repository** or **Manage workspaces**, choose **Create
   from local clone** on the **From a local clone** card. Enter **Workspace
   name** and the clone's existing **Base branch**, then **Create workspace**.
2. Under **Local clone**, enter the absolute **Repository path**, or use the
   desktop app's **Choose folder**, then **Add remote**. For a workspace you
   picked with **Use**, choose **Link local repository** first.
3. Check the connected path and destination, then **Push now**. **What git
   did** retains Git's output. A fresh workspace receives the base branch; an
   existing one reports whether it is current, ahead, or diverged.
4. If offered, **Fast-forward my clone** catches up without a merge commit.
   Divergence shows commands to resolve it yourself; the dashboard never
   force-pushes. **Continue** to the **Agent** step after the base is available.

For an existing workspace, open its repository page (**Repository** in the
workspace selector) and choose **Link local repository**.
Use **Use a different repository** to relink. Each local server profile keeps
one current clone, not one per workspace; check the displayed workspace and
path before pushing after a switch. Relinking leaves the previous clone and
its history in place.

The hosted dashboard shows these choices with a local-client/CLI handoff; it
cannot link a path on your laptop. A collaborator can link an existing
workspace and push its base when source status confirms it is local-only.
Creating a workspace or configuring, refreshing, adopting, or disabling a
mirror still requires an admin. All admitted members can inspect source
status.

If source ownership cannot be checked, linking remains available but base-push
controls and commands are withheld. Resolve the displayed error before
pushing; a configured mirror's base remains server-owned.

CLI equivalent below assumes the clone's intended base is `trunk`. Replace
the path, workspace, and `trunk` with your actual values. Replace
`<server-address-or-id>` with the server's SSH address, including its SSH port,
or its server ID, not the hosted dashboard's HTTP address:

```sh
server='<server-address-or-id>'
git -C "$HOME/code/myproject" branch --list &&
aether link "$server" &&
aether workspace add myproject --base trunk &&
aether link "$server" --workspace myproject --repo "$HOME/code/myproject" &&
git -C "$HOME/code/myproject" push --no-follow-tags aether trunk:trunk
```

For an existing workspace, omit `workspace add` and use the base branch
shown in its settings. `link` requires the server argument even if the client
is already linked. With `--repo`, it adds or updates the clone's `aether`
remote; it does not change its `origin`.
The push is non-force and does not send tags. Do not push a mirrored base:
linking a clone there is for pulling run branches; use the source mirror on
the repository page to refresh or adopt the server-owned base.

On the first eligible link, Aether records the clone's `origin` as the
workspace's checkout Origin if none is set, printing `workspace origin ->
<url>`. A later link does not overwrite it. GitHub SSH URLs are normalized to
HTTPS. Inspect or change it deliberately for new run checkouts:

```sh
aether workspace origin --workspace myproject
aether workspace origin --workspace myproject https://github.com/my-account/myproject.git
```

Neither linking nor recording Origin supplies upstream credentials. See
[workspaces](teams.md#workspaces) for local push conflicts and source policy,
and [member environments](environments.md) for installing tools and saving
the image new containers use.

### Setup script and variables

A workspace can prepare every run that starts in it. Its **setup script**
runs in each new run's container before the agent does, in the run's
checkout. Its **variables** are set in that container; one marked **secret**
is never shown again once saved. Only an admin can change either.

```sh
printf 'npm ci\n' > setup.sh
aether workspace env script --workspace myproject --file setup.sh
aether workspace env set --workspace myproject NODE_ENV=test
aether workspace env set --workspace myproject --secret NPM_TOKEN < token.txt
aether workspace env import --workspace myproject .env
```

`set --secret` reads the value from stdin, and `import` stores the values of
a `.env` file as secrets. The dashboard has the same settings under
**Environment** on the workspace's Repository page. Runs launched afterwards
get them; a container that already exists does not. If the script fails, the
launch fails and shows what the script printed.
[environments.md](environments.md#workspace-environment) covers how the
script runs and who can read a secret.

## 5. Set up your agent

An **agent** is the coding CLI a run starts. **Onboarding → Agent** opens
with two cards comparing **Standard** and **Enhanced**, the two ways a run
can show an agent; on the **Agents** page, **What's the difference?** shows
the same cards. Both list each agent with **Installed** or **Not
installed**, **Login found** or **No login found**, and the modes it
supports (**Standard · Enhanced**, or **Standard**), and one button: **Set
up**, **Log in** or **Run**. Once one agent is installed, the Agents page
folds the others under **More agents**. Setup belongs to your member
account, not to one workspace. **Set up** opens three numbered steps:

1. **Choose how runs show it.** Two cards draw the same moment of one run:
   - **Standard** - the agent's own terminal, exactly as on your machine. It
     acts without asking; anything it asks is answered in the terminal.
   - **Enhanced** - Aether reads what the agent is doing: messages, tool
     activity, file changes, approvals and progress appear as native
     controls. It runs through an adapter, misses some agent-specific
     commands and screens, and starts a few seconds slower. Like Standard,
     it acts without asking by default; for Claude Code and Codex, switch
     its mode in the run to be asked. The card's note says how the chosen
     agent runs without asking ([enhanced-runs.md](enhanced-runs.md)).

   Below the cards, one line each says how the agent supports Enhanced, how
   it is installed, whether a running agent can switch, what happens when the
   adapter fails, and for Claude Code which login pays. An agent without
   Enhanced shows that card disabled with the reason. The choice starts on
   Enhanced for Codex, oh-my-pi, OpenCode, Copilot, Gemini and Cursor and on
   Standard for the others,
   and becomes the agent's default in **New run**.
2. **Install and log in.** **Install <agent>** runs the vendor's install
   command in your **environment**, the container Aether keeps for you on the
   server, followed by the pinned adapter when Enhanced is chosen. A failed
   command shows **Install failed** with the exit status and the command's
   output. Once the agent is installed, your Environment's terminal opens
   with its login command typed: `claude` then `/login`, `codex login`, `pi` then
   `/login`, `omp`, `opencode auth login`, `copilot login --device-code`,
   `NO_BROWSER=true gemini`, `NO_OPEN_BROWSER=1 agent login`, or `agy`.
3. **Check** reads `agent.list` again and shows **Installed**, **Enhanced
   installed**, and **Login found** or **No login found**. Aether only looks
   for the agent's login file; the agent itself proves the login when it
   starts. **Done** returns to the list, where **Run** starts the agent. For
   Codex, **Check** also shows **Trust Aether's hooks once**: your first
   Standard run opens Codex's **Hooks need review** screen, where you choose
   **Trust all and continue**. Codex remembers it in `~/.codex/config.toml`
   and asks again only if Aether's hooks change. `aether agent add codex`
   prints the same note.

From the CLI:

```sh
aether agent add claude --enhanced
aether terminal
```

**Add agent…** registers a CLI Aether does not ship: a **Name**, a
**Standard command** whose first word is the executable, a **Background
command**, and an optional **Enhanced command** that serves the Agent Client
Protocol. Install that executable into `~/.local/bin` from the terminal in its
setup.

Your environment's home persists the executable and vendor login state across
containers. **Environment** in the header (or **More navigation**) opens the same shell;
[terminal.md](terminal.md) covers its tabs and **Stop environment**.

Once one agent is ready, the dashboard's **Assistant** button (or `Ctrl+Shift+Space`)
opens a chat that can list your runs, launch one, or install tools in your
environment. See [assistant.md](assistant.md).

No vendor login yet? [Prove the plumbing without an agent
subscription](#prove-the-plumbing-without-an-agent-subscription) runs the
whole path with a scripted stand-in.

### Upload agent configuration

An agent's configuration - settings, instruction files such as `CLAUDE.md`,
commands, subagents, skills, hooks, MCP and plugin configuration - lives in a
directory such as `~/.claude`. Runs read the same path in your **home**, the
persistent directory Aether keeps for you on the server. Uploading
configuration is separate from installing an agent or logging in.

A coding agent on the computer that has the configuration decides what to
upload, asks you, and uploads it with the `aether config` CLI:

1. Open **Agents > Agent config files** (or **Onboarding > Agent**) and click
   **Copy prompt**.
2. Paste the prompt into Claude Code, Codex, opencode, or another agent on
   that computer. It needs the `aether` CLI linked to the server
   ([step 3](#3-link-from-your-machine)).
3. Approve or change its plan: what it will upload, which server files it
   would replace, what it leaves behind, and the commands it will run on the
   server afterwards.

The prompt:

```text
Move my coding-agent configuration from this computer to my Aether server, so agents I run there behave the way they do here. The server runs Linux. Use the `aether` CLI, which is linked to it.

1. Run `aether config help`. If `config` is an unknown command or the help does not list `config cat`, run `aether update`, which prints the path of the binary it updated. If the help still lacks it, a different `aether` comes first in PATH; update that one.
2. Run `aether config ls`. It lists each agent Aether can hold configuration for, its directory (the same path here and on the server, such as ~/.claude), whether the agent is installed on the server, and what Aether always leaves out. Run `aether terminal exec 'echo $HOME; uname -sm'` for the server's home directory and platform.
3. For each listed directory that exists here, look through it and decide what belongs on the server:
   - Upload: settings, instruction files such as CLAUDE.md or AGENTS.md, custom commands, subagents, skills, hooks, and MCP or plugin configuration.
   - Leave behind: logins, tokens, and API keys; session history, transcripts, caches, and logs; anything that only works on this computer, such as hooks or MCP servers that run programs at local absolute paths, or macOS-only tools.
4. Run `aether config push --dry-run <agent> <path>...` on what you chose. Each path is a file or directory inside the agent's directory, relative to it; a directory takes everything in it. The dry run uploads nothing and says what a push would do with each file: `would create` it, `would skip` it because the server's copy is identical, `would refuse` it because the server has a different file, or `would leave out` a file Aether never uploads. Read the server's copy of every refused file with `aether config cat <agent> <path>` and compare it with mine.
5. Show me the plan and wait for my approval. List separately: new files; files that would replace a different server file, with what differs and whether you recommend replacing it; what you leave behind; and the commands you will run on the server afterwards. Give a one-line reason for each. When a file mixes settings that belong on the server with ones that only work here, write an edited copy to a temporary file and show me the change. Do not edit my local files.
6. Upload what I approved with `aether config push <agent> <path>...`. A push never replaces a different server file without `--overwrite`; pass it only in a push that names exactly the replacements I approved. Upload an edited copy with `aether config push <agent> <path> --from <temporary file>`.
7. Finish on the server with `aether terminal exec '<command>'`, which runs one command in the Environment where my agents are installed: for example, reinstall a plugin or MCP server whose configuration you uploaded, then check that it loads.
8. Report what was created, replaced, skipped, and left out, with Aether's reason for each file it left out. End with what I still need to do myself and its exact command, such as logging in to an agent, which needs `aether terminal`.

Never print, copy, or upload a credential.
```

The agent runs these commands; you can run them yourself:

```sh
aether config ls
aether config ls -r claude
aether config cat claude settings.json
aether config push --dry-run claude settings.json CLAUDE.md skills commands
aether config push claude CLAUDE.md skills commands
aether config push --overwrite claude settings.json
aether config push claude settings.json --from /tmp/settings.server.json
```

`aether config ls` prints one entry per agent: its name for the other
commands, its directory (the same home-relative path on this computer and on
the server), whether that directory exists on this computer, whether the
agent is installed on the server, and what is always left out:

```text
claude (Claude Code): ~/.claude, exists on this computer, installed on the server
  always left out, credentials: .credentials.json, credentials.json, credentials, .claude.json, auth.json, keychain, token.json, tokens.json, oauth.json, agent.db, agent.db-wal, agent.db-shm, *.pem, .env, .env.*
  always left out, runtime and history: projects/, shell-snapshots/, statsig/, todos/, file-history/, history.jsonl, daemon/, transcripts/, sessions/, session-env/, cache/, backups/, plugins/cache/, plugins/marketplaces/, plugins/synced/, skills/synced/
codex (Codex): ~/.codex, does not exist on this computer, not installed on the server
...
always left out, every agent: symlinks, special files, files over 64 MiB, .aether-profile-ignore, and files the secret scanner flags
```

An agent you registered is listed when its definition sets `ProfileRoot`
([custom agents](harnesses.md#custom-agents)).

`aether config ls <agent> [path]` lists what the server has in that
directory, in the `aether files ls` format: `dir<TAB><name>` and
`file<TAB><size><TAB><name>`. With `-r` it also lists every directory below,
with names relative to the one you asked for. `aether config cat <agent>
<path>` prints one of the server's text files. Neither shows a path that is
always left out.

`aether config push <agent> <path>...` uploads files and directories. A
relative path resolves against the agent's directory, not the current one; an
absolute path such as `~/.claude/skills` must be inside it; `.` uploads the
whole directory. Directories are walked recursively. Before reading a file,
the push leaves out:

- credential names in any path component, matched case-insensitively, and
  every `*.pem`, `.env`, and `.env.*`
- `.aether-profile-ignore`
- the agent's runtime and history paths, matched case-sensitively from the
  directory root, where `*` stands for any run of characters in one name
- symlinks, which are not followed, and sockets, FIFOs, and devices; a path
  you name that runs through a symlink is left out whole, as in
  `left out skills/linked/SKILL.md: symlink skills/linked, not followed`
- files over **64 MiB (67,108,864 bytes)**; install or download those in
  `aether terminal`

It then runs the server's secret scanner on each remaining file and leaves
out every flagged file, so its bytes never leave your computer. There is no
override. What is left is compared with the server's copy:

- a file the server does not have is created
- a file that is identical on the server is skipped
- a file the server has with different content is refused, and the server's
  file stays; `--overwrite` replaces it instead
- a file whose path on the server is a folder, a symlink, or a hard link, or
  runs through one, is left out

The server applies the same rules, so a dry run lists exactly what a push
does. Each file gets one line:

```text
left out .credentials.json: credential
left out history.jsonl: runtime or history path
left out projects/: runtime or history path
left out skills/linked: symlink, not followed
created CLAUDE.md
skipped commands/review.md: identical on the server
refused settings.json: the server has a different file (1108 bytes, 40769 here); --overwrite replaces it
created skills/deploy/SKILL.md
left out skills/deploy/notes.md: secret detected at 0:27 (generic-api-key)
created 2, replaced 0, skipped 1 identical, refused 1, left out 5 in ~/.claude (42 bytes uploaded)
```

A push that refused a file still uploads the rest, then exits non-zero. Read
the server's file with `aether config cat`, then push that path again with
`--overwrite`, which prints `replaced settings.json`. `--overwrite` applies
to every path in the command, so name only the files you mean to replace.

`--dry-run` uploads nothing and prints the same lines as `would create`,
`would skip`, `would refuse` (`would replace` with `--overwrite`), and
`would leave out`, then
`would create 2, replace 0, skip 1 identical, refuse 1, leave out 5 in ~/.claude (42 bytes to upload)`.

`--from <file>` uploads that file as the one `<path>` you name; the path need
not exist on this computer, and your own file at that path is not read. Use
it for a copy you edited for the server, such as a `settings.json` without
the hooks that only work locally. Every rule above applies to it.

A push deletes nothing. A new file gets the local permission bits, so
scripts stay executable; a replaced file keeps its server mode. From Windows,
new files are `0644`. Files travel in requests of at most 2,000 files and
about 20 MiB; the directory itself has no size or file-count limit. If the
server fails partway through a request, the push prints `created <path>` or
`replaced <path>` for what it wrote and exits with the server's error. If a
request gets no answer, the push prints `unconfirmed <path>` for each file in
it and exits with the error; `aether config ls -r <agent>` shows what the
server has. Running the same push again is safe. The command exits 0 when
the server confirmed every request and nothing was refused, even if files
were left out.

An upload writes your home at once: your Environment and active and future
runs see it, and a running agent may need to reload. Nothing watches the
local directory; push again after changing it. Edit uploaded files in
**Files** ([harnesses.md](harnesses.md#agent-configuration-import-and-files)).

For OMP, root-level `stats.db`, `stats.db-wal`, and `stats.db-shm` are
runtime exclusions; settings, MCP configuration, skills, and extensions are
not. See the [exact OMP exclusions](harnesses.md#agent-configuration-import-and-files).

**OpenCode:** its configuration directory is `~/.config/opencode`, not
`~/.local/share/opencode`. Files an older Aether release imported under
`~/.local/share/opencode` are not moved; upload the configuration again with
`aether config push opencode <path>...`, then reload OpenCode. Leave the login
at `~/.local/share/opencode/auth.json`.

### Agent coordination hooks

**Agent messages** are what agents in different runs send each other. They
are durable: a successful send means stored, not read. An agent actively
awaiting a reply should use `aether-internal inbox --wait 30` and explicitly
acknowledge the batch only after handling it.

Managed Claude Code launches and loaded native pi, OMP, and version-matched
OpenCode integrations can wake an eligible live idle session without terminal
keystrokes. Native wake respects human protection, takeover, and Stop.
Manually installed Claude Code hooks and the Codex, Copilot CLI, Gemini CLI,
and Cursor CLI command hooks instead announce new messages at their next
supported lifecycle boundary. Managed Standard Codex launches load their
hooks automatically, but Codex runs them only after you [trust them
once](harnesses.md#codex-inbox-integration). Neither path restarts an exited run.

Inside a run, `aether-internal skill --hooks` checks configuration and prints
setup instructions. **Configured is not loaded, trusted, or executed**:
restart or reload as the agent requires and inspect its real hook/plugin
errors.
See [per-agent setup](harnesses.md#incoming-coordination-hooks) for managed
loading, copyable files, versions, and disable controls; see
[delivery and acknowledgement](coordination.md#delivery-acknowledgement-and-retries)
for the runtime contract. An unlisted CLI can follow
[harness-integration.md](harness-integration.md) without adding a new daemon
or terminal fallback.

### Connect GitHub

If runs should publish to GitHub, connect an account with write permission
to the checkout Origin from step 4. Source deploy keys do not grant that
permission, and enterprise policy may require additional authorization.

Admins who connected under **From GitHub** are already set up: **Onboarding →
Agent** recognizes that connection. To connect later, use **Settings → GitHub →
Connect GitHub**, or the admin GitHub controls in the Agent step.
**Check connection** only reads status; it does not configure Git or signing.

Collaborators retain their own native setup: in **Onboarding → Agent** or
**Agents**, open **GitHub**, then **Connect GitHub**. This member flow opens
the Environment terminal with the login command typed; **I've logged in**
finishes setup and reports the account and registered signing key.

From the CLI it is two commands. In `aether terminal`:

```sh
gh auth login --hostname github.com --git-protocol https --web \
  --scopes admin:ssh_signing_key
```

Then, back on your machine:

```sh
aether github connect
```

What each step writes, how to re-run it, and how to revoke are in
[environment-home.md](environment-home.md#connect-github) and
[security.md](security.md#github-credentials-and-signing-keys).

## 6. Launch a run

The dashboard's **First run** step is the **New run** form below, with the
same rules: task, agent and mode. It names the workspace and base branch and
checks that the server can read the base. If no agent is installed it says
**No agent is installed yet** and offers **Set up an agent**. For an empty or
unaccepted base, use **Review repository**, finish the push or source
adoption, then **Check again**. After **Launch** the run starts its
container, pulls the image the first time the server uses it, then starts
the agent (about 5 s more in Enhanced). Installation does not prove vendor
login or upstream publishing permission.

```sh
aether run "add a health check endpoint" --workspace myproject --agent claude
```

The run gets its own container and checkout while using your persistent home.
In the dashboard, **New run** asks for the task, the agent and the **Mode**:
**Standard** opens the agent's own terminal, **Enhanced** shows its
messages, approvals and progress as native controls
([enhanced-runs.md](enhanced-runs.md)), and **Background** runs the task once
with no interaction. The CLI takes `--mode enhanced` or `--mode background`.
`--model <model>` picks the agent's model; omit it for the agent's default.

```
run run-d6ay38tj8j running
```

Every ID Aether generates names its kind, then ten random characters, such
as `run-` for a run, `ws-` workspace, `mem-` member, `mis-` swarm, `task-`,
`att-` worker attempt, `sub-` submission, `ques-` swarm question, `msg-`
agent message, `ack-` inbox delivery token, `rpt-` agent report. Servers
upgraded from earlier versions keep their older 26-character IDs, and both
forms are accepted.

`aether runs` lists your visible runs. Scoped commands can omit `--workspace`
only when exactly one workspace exists; keep it explicit when adding projects.
To hand one objective to a team of agents instead, see
[Launching a swarm](teams.md#launching-a-swarm).

## 7. Watch it

```sh
aether gui
```

This serves the dashboard from your own machine and opens a browser tab
already carrying a per-process token. It rides your SSH key, so everything
the CLI can do works from the page, plus local actions like pulling a run
branch into your clone. Leave it running; Ctrl-C stops the gateway and the
token dies with it. `aether gui --url` prints the URL instead of opening a
browser. See [local-gateway.md](local-gateway.md).

On a tailnet, the server can host the dashboard instead, so a phone or any
other tailnet device opens `https://<the server's MagicDNS name>/` with
nothing installed and no token. Set `web-port` and restart the server; see
[networking.md](networking.md#the-dashboard). On a phone the sidebar folds
away: the top bar holds **Open sidebar** (with a dot when a run needs you),
the current page's name, **Search** and **New run**. To hear about that run
with the dashboard closed, turn on **Settings > Notify this device when a run
needs me** ([networking.md](networking.md#notifications)). Hosted onboarding supports remote
repository import, Git identity, agent setup, the configuration prompt, and
editing configuration in **Files**.
It has no machine-local link, clone, push, or pull operations; those require
the desktop app or `aether gui` on the computer holding the clone.

### Prefer a native window?

`aether gui` in a browser tab is the whole dashboard. If you would rather it
lived in its own window - with desktop notifications and a dock badge when a
run needs you, plus `aether://run/<id>` deep links - build
the desktop app. Answering **client** in step 1 already did this. Nothing has
to be installed first. On Windows the CLI downloads the release's prebuilt
app; on Linux and macOS it builds the app, fetching its own Node.js copy when
the machine has none, which makes the first build longer:

```sh
aether gui build
```

That installs Aether into your application menu (Linux), your Applications
folder (macOS; `~/Applications` without administrator rights, and the command
prints the path), or the Start Menu (Windows). Open it like any other app.

Two things to know:

- **There is no installer.** On Windows `aether gui build` installs the
  prebuilt app the release's signed manifest lists; elsewhere it packages the
  Electron shell on your machine from sources carried in the CLI. Details in
  [install.md](install.md#desktop-app).
- **It is not a standalone client.** The app does not bundle `aether`; it
  launches `aether gui` from your `PATH`, and the dashboard lives inside that
  CLI binary. Install the CLI ([step 1](#1-install)); on an unlinked machine,
  the app opens its local onboarding wizard and links from there. When you
  update the CLI, the window picks up the new dashboard without rebuilding the
  app.

In the dashboard, the sidebar holds the workspace switcher, **New run**, your
runs, and the pages: **Board**, **Swarms**, **Activity**, **Files**,
**Environment**, **Agents** and **Templates**. The **Board** groups runs into
**Needs you**, **Working** and **Finished**. A run opens on **Session**
(Enhanced) or **Terminal** (Standard), with **Changes**, **Files** and
**Browser** beside them, and a **Details** panel holding **Needs you** and
**Notes**. Watching is
read-only by default; typing into a run needs the `steer` capability, which as
the owner you have.

**Browser** is a real Chromium that the server runs beside the run, so
`localhost` there is the run's own app. It lists the ports the run is
listening on: click `localhost:5173` to open it, or type any address and
press Enter. Nothing has to be forwarded to your machine. A `localhost` link
printed in the run's terminal opens there too. The agent can drive the same
page, and the toolbar says so when it does.

To use your own browser instead, click **Open on this computer** beside the
port. It forwards the port and opens `http://localhost:5173` on your machine.
That needs `aether gui` or the desktop app; the dashboard your server hosts
shows the command to run instead:

```sh
aether forward <run-id> 5173
```

A forward reaches a server listening on `0.0.0.0`, not one bound to
`127.0.0.1`; the Browser view reaches both.

**Files** is the run's checkout: a tree and an editor, where **Save** writes
your edit into the checkout the agent works in. A file's path in **Changes**
opens it there. So does a path the agent names in **Session** or prints in
the terminal, such as `web/src/app.ts:42`, at that line: point at the path
and click it once it is underlined. Only a path that is a file in the
checkout becomes a link. The **Files** page keeps the base branch and
**Home**, every file in your home on the server: there you create, rename,
move, upload, download and delete from each row's **…** menu
([environment-home.md](environment-home.md#uploading-and-editing-configuration)).

The terminal escape hatch is `aether attach <run-id>` - a raw byte-for-byte
passthrough where every native keybind and theme of the agent's own TUI works.
Detach without killing anything: the PTY lives on the server.

To message a running agent without attaching:

```sh
aether message <run-id> "also update the README"
```

The message appears in the run's **Session** under your name, so everyone
watching sees who sent it.

### Open a run in your editor

A live run is a machine your own editor attaches to over SSH. Run this once
on your computer:

```sh
aether ssh-config
```

It writes a `Host *.aether` block to `~/.ssh/aether_config` and adds one
`Include` line for it at the top of `~/.ssh/config`; nothing else in your
config changes. From then on every run is an ssh host named after its id:

```sh
ssh <run-id>.aether                          # a login shell in the run's checkout
ssh <run-id>.aether git status               # one command, with its exit status
scp notes.md <run-id>.aether:                # into the checkout; sftp works the same way
ssh -L 3000:localhost:3000 <run-id>.aether   # a port on the run's own loopback
```

In the dashboard, a live run's **More** menu has **Open in editor…**, with
**VS Code**, **Cursor** and **Zed** buttons that open the checkout at
`/workspace`. In the editor itself, connect to the SSH host
`<run-id>.aether`. `aether ssh <run-id> [command]` works without the setup
step.

You can connect to a run you could open a shell in: your own, or a
teammate's when you may control it and they have shared their agent account
with you. Any other attempt is refused with the server's reason. The editor
downloads its remote server into the run's home directory on first
connection. An attached editor trusts its remote, and this remote is a
container an agent controls, so attach only to runs you would run on your
own computer; see [security.md](security.md#ssh-into-a-run).
[terminal.md](terminal.md#ssh-and-editors) has the details.

## 8. Review and publish the result

### Comment on the diff

Open the run's **Changes** view. Point at a line and click the **+** beside
its number, write what should change and click **Comment**. Drag across the
numbers, or Shift-click a second line, to comment on a range. Comments wait
in a bar under the toolbar until **Send to agent** delivers all of them as one
message, each with its `path:line`, the quoted diff lines and your text. They
stay in this browser tab until you send them.

### Remote-only: commit, push and open a PR

Open the run's **Changes** view and click **Publish…**. The dialog runs
native Git and `gh` in the run's checkout as the run's account. **Land**,
candidate review and the interval menu stay in the Changes view: a GitHub PR
is not an internal candidate proposal.

1. Read the line at the top: the checkout's branch, its commit, the agent
   account and the GitHub login, or that login's real authentication error.
   Connect GitHub in that account's environment first; mirror deploy keys do
   not provide push credentials.
2. With nothing left to commit, the dialog opens on step 2: skip to step 4.
   Otherwise check exact paths, then click **Review selected paths**. The view
   shows worktree and staged diffs separately, with content previews for
   selected untracked files. Enter a **Commit message** and click **Commit
   selected**. This commits those paths' current worktree contents, not only
   their staged hunks; unselected staged paths are preserved. Native identity
   and signing apply, but commit hooks do not run.
3. Inspect **Commit outcome** and native diagnostics. **Committed: yes** with
   **Index updated: no** means the commit exists but index reconciliation
   failed: inspect the checkout instead of repeating the commit. A branch/HEAD
   mismatch requires **Refresh status** and a fresh review. Then click
   **Continue to push**.
4. **2 · Push and pull request** says where the work goes in two sentences:
   the commit, the push URL and the branch, then the pull request's target
   and the GitHub login it is opened as. The push goes to the branch's
   upstream or the workspace's checkout Origin. The pull request goes from
   that repository and branch into the run's base branch of the repository
   the workspace's source came from, which for a fork Origin is the upstream.
   Neither choice reconfigures the mirror or Origin.
5. Check **PR title**, which holds the last commit's subject, add a **PR
   description**, optionally check **Draft PR**, and click **Push and open
   pull request**. It pushes, without force and without switching branches,
   then opens the pull request. If the branch already has a pull request,
   including one the agent created using native `gh`, the dialog shows it
   and the button is **Push branch**.
6. Read the outcomes under the sentences. **Pushed: yes** stays whatever the
   pull request did; if the pull request failed, its real error is under the
   second sentence and the button becomes **Open pull request**. If creation
   is uncertain, use **Reconcile PR read-only** rather than repeating it.

To publish somewhere else, open **Change destination**: the push remote, its
URL and branch, and the pull request's repository, base and head. It opens by
itself when nothing could be chosen, for example in a workspace without a
checkout Origin, or when a destination refused. After changing a pull request
field, click **Discover existing PR** to look that exact target up. **Push
only** pushes without opening a pull request.

The server refuses a push or a creation when the branch, HEAD, push URL or
GitHub login is no longer the one on screen, and says what it found instead.
No automatic merge, force push, branch switch or mutation retry is performed.
GitHub discovery, creation and feedback need a working native `gh`, network
access, and the run's GitHub login's permissions on the explicit upstream/fork:
the `gh` login in the home the run's container mounts: its launcher's, also on
a shared agent account and after a handoff.
Review and merge on GitHub according to your repository's policy.

### Follow the pull request from the run

Once the run's branch has a pull request, whoever opened it, the run page
shows it at the right of the view switch: `#412` opens it on GitHub, and
beside it is the state of its checks, such as **2 failing** or **Checks
passed**. Aether reads this from GitHub every minute while the run is live
and its page is visible.

Click the check state for the checks and review comments. A failing check or
a comment has **Send to agent**, which sends the agent one message with the
check's name, conclusion and link, or the comment with its file and lines.
It is a normal moderated message: the row says whether it was sent or
queued, and **Open the session** shows it in the conversation.

### Optional: pull into a local clone

When the TUI agent exits, the run stays alive in a login shell. Start another
installed agent in the same checkout if needed; exiting that shell opens another
login shell, so the run remains live until you explicitly close it. Close
commits and publishes the latest work to the run's branch.

```sh
aether pull <run-id>
```

If your checkout is already on the run branch, Aether fast-forwards it. If not,
Aether creates or updates the local run branch without switching your checkout:

```
Branch aether/run-add-a-health-check-endpoint-d6ay38tj8j is ready. Switch with: git switch aether/run-add-a-health-check-endpoint-d6ay38tj8j
```

The branch name is `aether/run-<slug>-<short-id>`: the task slugified, then the
random part of the run ID (the last six characters of an older 26-character
ID). Aether never switches branches or merges
the run into your base branch. These examples use `trunk`, as in the local
clone setup above; substitute your workspace's actual base branch. Review
and diff the run branch:

```sh
git log --oneline aether/run-add-a-health-check-endpoint-d6ay38tj8j
git diff trunk...aether/run-add-a-health-check-endpoint-d6ay38tj8j
```

If the local checkout has uncommitted changes, the pull still fetches the run
branch and reports that the checkout is dirty. Commit or stash those changes
before switching to the run branch or merging it.

When the review is complete, merge locally. In a local-only workspace, push
the reviewed base back to Aether:

```sh
git switch trunk
git merge aether/run-add-a-health-check-endpoint-d6ay38tj8j
git push --no-follow-tags aether trunk:trunk
```

In a mirrored workspace, the Aether base is protected. Push the reviewed
result to the configured source branch using credentials for that upstream:

```sh
git push <source-remote> trunk:<configured-source-branch>
```

Here `<source-remote>` is a local remote (or URL) for the configured source;
the mirror's server-side credentials are read-only. The checkout Origin is
optional and independent of the source mirror. It may be a different
repository, and is a review destination for publishing the run branch (for
example, to open a pull request); it is not the destination for a mirrored
base update.

An agent's completion report leaves a Standard or Enhanced run open. Send a
follow-up in the message box or type into the Standard agent's terminal;
neither requires reopening. Reporting does not start an expiry timer.

When finished with the session, close it:

```sh
aether close <run-id> --outcome merged      # or --outcome abandoned
```

Closing retains the exact Standard or Enhanced container, checkout, run row,
member account and coordination surfaces for `--run-container-ttl` (default
`7 days`). Before retention expires, reopen the same run with **Reopen run** in its **More**
menu, or:

```sh
aether reopen <run-id>
```

Reopening does not create a new run or container, and expired or unavailable
runs cannot be reopened. **Kill run** and **Delete run** remain immediate
cleanup; **Free container…** removes the retained container early, after
which the run cannot be reopened.

Swarm workers also retain their exact containers for the same default
7 days after a success/failure report or container exit, without continuing
work. Inspect their Session, Changes, Captures and worker details normally.
Unlike explicitly closed ordinary Standard or Enhanced runs, completed
workers cannot be reopened.

The local daemon is optional. It fetches server-owned run branches as agents
commit and can push your local base branch in **local-only** workspaces. It
does not watch agent configuration directories; configuration is imported
explicitly in either dashboard and edited in **Files**. It
does not fetch a mirror source or forward a checkout's `origin` into the
workspace. In a mirrored workspace, a base push attempt is rejected because
the mirror owns that branch; use `aether workspace mirror refresh` and, when
needed, explicit `aether workspace mirror adopt` instead. `aether pull` remains
available when you prefer reviewing in a local clone.

```sh
aether daemon install --server <server-host>:2222 --repo ~/code/myproject
systemctl --user daemon-reload && systemctl --user enable --now aether-daemon
```

That second line is the Linux one. `daemon install` prints the activation
command for whatever platform you are on: `launchctl load` on macOS,
`schtasks /Create` on Windows. A unit installed by an older release that still
contains `--sync-origin` keeps restarting after upgrade;
reinstall it with the command above and activate the newly written unit. The
reinstalled unit shows `failed` in `systemctl --user status aether-daemon`
when its arguments are bad. There is no recurring
dashboard **Sync from origin** action.

---

## Prove the plumbing without an agent subscription

No vendor login yet? Aether ships a deterministic agent named `fake` for
exactly this: it runs a script from your repo instead of an agent, so you can
drive the whole lifecycle end to end and see a real branch come back.

Start the server with the fake agent's command in its environment. If the
systemd unit from step 2 is already running, stop it first
(`sudo systemctl stop aether-server`) - two servers cannot share `:2222`:

```sh
AETHER_FAKE_AGENT="sh /workspace/agent.sh" \
  aether-server serve --data-dir /var/lib/aether --addr :2222
```

`/workspace` is where the run's checkout is mounted, so `agent.sh` is just a
file in your repo. Create a throwaway one:

```sh
mkdir demo && cd demo
git init -b main
git config user.name "You" && git config user.email you@example.com   # if git has no identity yet
cat > agent.sh <<'EOF'
echo "agent starting"
printf 'hello from the agent\n' > result.txt
echo "agent done"
EOF
echo "# demo" > README.md
git add -A && git commit -m seed
```

Then run steps 3, 4, 6 and 8 above with the default standard image and
`--agent fake` instead of `--agent claude`. Skip step 5: `fake` has no agent
login. Step 7 (`aether gui`) works too if you want to watch.

Launching it is the CLI's job, though. `fake` is a server-side registration
rather than an executable installed in your account, and the local dashboard's
two launch surfaces - the launch form and the wizard's **First run** step -
offer only agents installed in the account, plus the `custom` agent a
deployment pins, so `fake` never appears in either.

```sh
aether link <server-host>:2222
aether workspace init demo --base main
aether link <server-host>:2222 --workspace demo --repo "$PWD"
git push --no-follow-tags aether main:main
aether run "write a result file" --workspace demo --agent fake
aether runs
aether pull <run-id>
```

`aether runs` prints `ID`, `STATUS`, `AGENT`, `MEMBER`, `ACCOUNT`, `OVERLAP`,
`TITLE` and `TASK` columns; within seconds the run's `STATUS` reads `Working`
and `AGENT` reads `fake`. The script exits into the run's shell, which stays
open until you close the run, so after `--stall-threshold` with no output
`STATUS` reads `Waiting`. A success the agent reported reads
`Ready for review` until you mark it complete or later; a reported failure
reads `Failed`. The
pulled branch carries a commit adding `result.txt`. That is the full path -
container, worktree, PTY, commit, fetch - with nothing mocked but the agent.

## When something does not work

| Symptom | Cause |
| --- | --- |
| `not linked; run aether link <addr>` | CLI commands that need a server have no saved link. The desktop app opens its onboarding wizard and can link from there. |
| `no Aether member for this key` | The server already has an admin, so you are not the first member. Get an invite: [teams.md](teams.md). |
| `unable to authenticate, attempted methods [none]` | The CLI had no key to offer: none at `~/.ssh/id_ed25519` and no ssh-agent. The same error names the key when one was found but could not be used - read the rest of the line. On Windows, check `Get-Service ssh-agent` and look for the key at `%USERPROFILE%\.ssh\id_ed25519`. |
| `<path> is passphrase-protected; add it to ssh-agent (ssh-add <path>) or pass --key <unencrypted key>` | The key exists but the CLI cannot decrypt it; it does not prompt for a passphrase. Run `ssh-add <path>`, or point at an unencrypted key with `aether link <addr> --key <path>`. |
| `parse ssh key <path>` | The file at that path is not an SSH private key (a public key, or a truncated file). Pass the private key with `aether link <addr> --key <path>`. |
| `link --key: stat <path>` / `ssh key <path>: open <path>: no such file` | The key path you chose is not there. A chosen key is never skipped in favor of the agent, so re-link with the right `--key`. Re-linking without `--key` keeps the saved one; to go back to `~/.ssh/id_ed25519`, delete the `key` line from `~/.config/aether/config.json`. |
| `host key mismatch` / `REMOTE HOST IDENTIFICATION HAS CHANGED` on `aether link` | The server was reinstalled and generated a new host key, but your `known_hosts` still trusts the old one. Clear it: `ssh-keygen -R '[<server-host>]:2222'`. |
| `not signed in` from `aether link --claim`, `aether link <join link>`, `aether edge connect` or `aether servers` | This machine has no edge sign-in. Run `aether login`, with `--edge <url>` for an edge other than the default. |
| `claim code is wrong`, `claim code expired` or `claim code has no attempts left` | On the server, `sudo aether-server edge claim-code` prints a new claim link, valid 30 minutes. Once the server has members it needs `--admin <member id>`; an admin connects from the dashboard instead ([edge.md](edge.md#server-id-and-claiming)). |
| `the server has members, so a claim without --admin would be refused; ...` | Someone already joined, by SSH key or over the tailnet. That admin runs **Connect my GitHub account** in Settings › Remote access, or `aether edge connect` from a linked terminal. |
| `server is not connected to the edge` | The edge holds no control connection from the server: it is stopped, exits during startup, started before `edge-url` was set, or cannot reach the edge. On the server, `sudo aether-server edge status` shows the last connection error, or `stale` or `never connected` with the commands that show the server's log. |
| `not a member of this server` | Your edge account has no membership or open invitation there, or the invitation was revoked or expired. An admin runs `aether invite --github <your-login>` and sends you the new link ([teams.md](teams.md#through-an-edge)). |
| `<account> is invited to this server; open the invitation link the admin sent, or run: aether link <link>` | You linked by server id. Open the link the admin sent, or run `aether link '<invite link>'`. |
| `the invitation link is not valid for this server: wrong, revoked or expired; ask the admin for a new one` | The link's secret does not match an open invitation. Ask the admin for a new link. |
| `device "<label>", signed in as <account>, is waiting for approval` | The server admits approved devices only (`edge-access approved-devices`), and this one is new. An admin clicks **Approve** on its Needs you card in the dashboard, or you do from a device you already use. When the message carries an approval code instead, nobody can approve it in the app: run one of the commands it prints, such as `sudo aether-server device approve <code>` on the server. |
| `10 devices of <account> are waiting for approval on this server already, so no new one is recorded` | An admin approves or revokes the waiting devices with `aether device list` and `aether device revoke <device-id>`, or `sudo aether-server device review` on the server. |
| `this device connects as "github:<user id>", but the edge signed the connection in as <account> ...; nothing was recorded` | The edge vouched for another account than the one this device signed in as. Nothing changed on the server; tell the edge's operator. |
| `... was admitted by signing in alone and is waiting for approval: this server now admits approved devices only` | The server switched from `account` to `approved-devices`. Approve it the same way. |
| `tailnet identity unavailable; key authentication required` | Informational, not an error. The server has Tailscale but this connection did not arrive over the tailnet, so it fell back to your SSH key. |
| `membership pending admin approval` | You joined over a tailnet on a server that requires approval. An admin runs `aether member approve <your-member-id>`. |
| `no workspace yet; skip git remote` | Run `aether workspace add myproject --base main`, then `aether link '<server-address-or-id>' --repo /absolute/path/to/clone --workspace myproject` with your server and clone path. |
| `multiple workspaces available; specify --workspace` | Pass `--workspace <name>` to `aether link` or another command that accepts a workspace selector. Agent setup is member-scoped. |
| Run reaches `failed` immediately | The agent started and exited. `aether timeline --run <run-id>` shows the exit code; `aether attach` only works while a run is alive. |
| `self-update is not supported on Windows` | Expected. Re-download the release binary: [install.md](install.md#manual-install). |

## Starting over

Testing the whole path from a clean slate, or handing the box to someone else?
[install.md](install.md#uninstalling) has the full removal order for the
server, the client, and your linked repos. Two things bite people: run
containers outlive the server unit and must be removed separately, and a
reinstalled server gets a new host key, so stale `known_hosts` entries have to
go or the next `aether link` fails.

## Next

- [install.md](install.md) - systemd, upgrades, data layout
- [environments.md](environments.md) - member images, saving, resetting, and persistence
- [environment-home.md](environment-home.md) - your environment's home, installed agents, and migration
- [networking.md](networking.md) - Tailscale-first, plus LAN and VPN
- [teams.md](teams.md) - joining, roles, workspaces
- [harnesses.md](harnesses.md) - agents: login, configuration, and launch definitions
- [security.md](security.md) - what the container boundary does and does not do
