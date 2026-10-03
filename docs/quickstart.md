# Quickstart

Aether runs coding agents on a **headless Ubuntu server** with Docker and git.
Repository fetches, builds, and run tools execute there; the server needs no
desktop session.

Choose where your code comes from:

| Starting point | Onboarding path |
| --- | --- |
| Public remote repository | [Import public HTTPS](#public-remote-repository); no local clone needed. |
| Private remote repository | [Import with a read-only deploy key](#private-remote-repository); a repository administrator must install the key. |
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
and browser-selected configuration, but cannot read an arbitrary path on your
computer or use its Git/SSH identity.

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
whatever is installed.

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
Use `-Role none` for the CLI alone. Desktop setup requires a release containing
the Windows installer fixes (`v0.4.0-alpha.6` or newer); the installer refuses
older desktop builds. See [install.md](install.md#windows-install-script)
for script-policy requirements, version selection, upgrades, and manual installation.

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
on, who may reach the server through it, a question with no default. Then it
prints:

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
server's id and a **claim code**, which makes whoever uses it first the
server's admin:

```
edge: https://edge.onaether.dev
edge access: approved-devices
server id: <server id>
edge key: pinned when the server first connects; `aether-server edge status` shows it
claim code: <code> (valid until 3:04PM, 5 attempts)
claim this server with:
  aether link --claim <code>
```

Run the activation line and the server is live on `:2222`. The SSH port is
the only thing it listens on unless you answered the dashboard question; the
edge connection is outbound. The claim code lasts 30 minutes;
`sudo aether-server edge claim-code` prints a new one. Change any option
later with `aether-server config set <key> <value>`, then restart.

To try it in the foreground first, `sudo aether-server serve` runs until
Ctrl-C. [install.md](install.md) covers unattended installs, running
unprivileged, and every serve option.

## 3. Link from your machine

**Through the edge**, with the claim code from step 2:

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
fallback is your display name at `<member-id>@aether.local`. Both dashboards
ask for these fields in onboarding's **Git identity** step; only the local
gateway can prefill them from this computer's `git config`. Change them later
at **Agents → Git commit identity**, or inspect them with `aether member git`.
Author identity is not repository authentication.

## 4. Create a workspace

In the dashboard, open **Onboarding**. After **Git identity**, the **Workspace**
step offers **Import repository** and **Create from local clone** under
**Add a workspace**, or lets you choose an existing workspace. The local
dashboard starts with **Link**. The authenticated hosted dashboard opens
onboarding for new members even when shared workspaces already exist, and
skips that machine-local step.

For another workspace, open **Manage workspaces** from the navigation,
workspace selector, or command palette (**Ctrl/Cmd+K**). The same public,
private, and local choices remain available. On an existing **Workspace**
page, use **Repository settings** or **Link local repository**; admins can
also reach **Repository settings** from **Workspace settings**. **Add another workspace**
returns to management, and **Set up agents / first run** resumes onboarding
for the selected workspace.

The **base branch** is the branch new runs start from. Use the repository's
actual branch, not `main` merely because the form defaults to it. Creating an
empty workspace does not upload code.

Keep these settings separate:

- **Source authentication** lets the server read an upstream branch into its
  **source mirror**. A deploy key is read-only and is not your Git/`gh` login.
- **Checkout Origin** is the publishing destination placed in new run
  checkouts. Remote import never infers it from the source URL; supply a
  writable repository or fork, or leave it blank.
- **Native Git/`gh` credentials** and upstream write permission let a run push
  and open a PR. Set them up in your environment terminal
  ([Connect GitHub](#connect-github)). Git author identity and agent vendor
  login are separate again.

### Public remote repository

In **Onboarding → Workspace** or **Manage workspaces**:

1. Choose **Import repository** under **Public or private remote repository**.
2. Fill **Workspace name**, a credential-free HTTPS **Source URL**, and the
   actual **Source / base branch**. Set **Checkout Origin (optional)**
   separately if runs should publish upstream.
3. Choose **Public HTTPS** under **Source authentication**, then **Import
   repository**. The server creates the workspace and fetches the source.
4. Read **Import outcome**, then **Continue to Source control**. Review the
   observed commit and generation, choose **Adopt candidate**, and confirm.
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
After reviewing the returned candidate, adopt its actual generation:

```sh
aether workspace mirror adopt --workspace myproject --generation <n> --yes
```

### Private remote repository

Use the same **Import repository** form, choosing **Read-only deploy key**.
Use a GitHub HTTPS source or a generic `ssh://` source. For generic SSH,
**Pinned known_hosts (required for generic SSH)** must contain the host key
verified with the host administrator; do not blindly trust `ssh-keyscan`.
GitHub uses Aether's pinned host key.

1. Import creates the workspace and a public deploy key, without fetching.
   Open **Continue to Source control**, then **Copy public key**.
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
aether workspace mirror adopt --workspace myproject --generation <n> --yes
```

Replace `<n>` with the reviewed generation from `status`. After installing a
key, use **Verify** / **Refresh**, not **Save source** or `configure`:
reconfiguration rotates the key and generation. To recover a misplaced public
key, reopen **Source control** or run `mirror status` on the same workspace.

### Remote import and source recovery

**Created: yes** means the workspace exists even if configuration or fetch
failed. Keep its name/ID and repair it in **Workspace → Source control**;
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

1. In **Onboarding → Workspace** or **Manage workspaces**, choose **Create from
   local clone**. Enter **Workspace name** and the clone's existing **Base
   branch**, then **Create workspace**.
2. The repository screen names the workspace ID and base branch. Enter the
   absolute **Repository path**, or use the desktop app's **Choose folder**,
   then **Add remote**.
3. Check the connected path and destination, then **Push now**. **What git
   did** retains Git's output. A fresh workspace receives the base branch; an
   existing one reports whether it is current, ahead, or diverged.
4. If offered, **Fast-forward my clone** catches up without a merge commit.
   Divergence shows commands to resolve it yourself; the dashboard never
   force-pushes. Continue to agent setup after the base is available.

For an existing workspace, choose **Link local repository** in **Manage
workspaces**, or **Workspace → Repository settings → Link local repository**.
Use **Use a different repository** to relink. Each local server profile keeps
one current clone, not one per workspace; check the displayed workspace and
path before pushing after a switch. Relinking leaves the previous clone and
its history in place.

The hosted dashboard shows these choices with a local-client/CLI handoff; it
cannot link a path on your laptop. A collaborator can link an existing
workspace but must ask an admin to create a new one. If source ownership
cannot be checked with your access, linking remains available but base-push
controls and commands are withheld. Ask an admin to verify the source and
accepted base before launching.

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
linking a clone there is for pulling run branches; use **Source control** to
refresh or adopt the server-owned base.

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

## 5. Set up your agent

In **Onboarding → Agents**, choose **Set up** beside an agent. Return later
through **Workspace → Set up agents / first run** or the **Agents** page.
Setup belongs to your member account, not to one workspace. CLI registration:

```sh
aether agent add claude
```

The setup screen opens your environment terminal and supplies the vendor
install command when one is known; otherwise it shows manual instructions.
Install the executable into `~/.local/bin` and complete its vendor login
there. **I've installed and logged in** checks the executable and saves the
environment image. It does not verify vendor login; the agent checks that
when it starts. To open the environment terminal from the CLI:

```sh
aether terminal
```

For a name Aether does not ship, the command first asks for interactive and
headless launch templates. Install that executable into `~/.local/bin` using
the vendor's instructions, then complete its login in the environment terminal.
Return to the dashboard when finished.

The member home persists the executable and vendor login state across
containers. Import configuration from the browser and edit it in **Files**.
See [the environment terminal guide](terminal.md) for tab and stop behavior.

### Import configuration

Configuration import is separate from installing an agent or logging in.
Use **Bring your configuration** in **Onboarding → Agents**, or open
**Agents → Configuration** (also in navigation and the command palette).
It works through either gateway, without a workspace, for members with
launch permission.

1. Click **Choose directory** and select the configuration root, such as
   `~/.claude`, `~/.codex`, `~/.pi`, `~/.omp`, or `~/.config/opencode`.
   The browser can read only the directory you explicitly select, including
   on a hosted dashboard; this does not grant local repository access.
2. Check **Configuration destination** and its remote path. A known basename
   selects one automatically; renamed or ambiguous directories need a choice.
   You can change it before import; that resets the file selection.
3. Under **Select files**, uncheck anything unwanted. Review **Left out before
   upload**. Known credential and runtime paths are excluded before reading
   their bytes and cannot be re-enabled.
4. Click **Import configuration**, then inspect the imported count, bytes,
   paths, and omissions. **Import finished with omissions** is not a claim
   that every file was copied. Use **Open remote files** to inspect the
   destination, or **Import another directory** to explicitly repeat import.

For OMP, root-level `stats.db`, `stats.db-wal`, and `stats.db-shm` are runtime
exclusions, even when larger than 64 MiB. History, caches, and known login
files are also excluded; settings, MCP configuration, skills, and extensions
remain eligible. This is not a blanket exclusion of databases or dependency
directories. See the [exact OMP exclusions](harnesses.md#agent-configuration-import-and-files).

An eligible file over **64 MiB (67,108,864 bytes)** stays visible and blocks
import until you uncheck it or click **Exclude unsupported files**. Review
the remainder before confirming. Directories have no total-size or file-count
ceiling; transfer uses bounded batches. To install a required larger asset,
use `aether terminal` and install or download it into the displayed remote
destination. **Files** is a text editor, not a large-file uploader.

Remaining selected bytes are uploaded and server-scanned; do not assume
all secret content stays local. Empty and binary regular files are preserved.
New files use mode `0644`; overwrites keep remote permissions. Local
executable bits and symlinks are not preserved, so a new script may need
`chmod` in the remote terminal.

**Import incomplete** preserves the actual error and separates **Imported
paths**, **Paths with unknown outcome**, and **Failed or unattempted paths**.
Copied files remain; there is no rollback or automatic retry. Use **Review
remaining files** to continue only failed/unattempted paths. After fixing a
local read failure, choose **Retry reading on import**, or uncheck that file.
Recovery does not resend confirmed or unknown-outcome paths: inspect **Files**
before deliberately reselecting uncertain files. Keep the browser window
open; navigating within the dashboard preserves the result, but reloading
loses the in-memory review.

Imports overwrite matching files in your authenticated member's persistent
home. They immediately affect that home in your environment terminal and
active/future runs using that home; a running agent may need to reload. They do not
rebuild the environment image, create an isolated per-run profile, or watch
the local directory. A snapshot pin records launch provenance, not an
isolated copy. An already-open **Files** editor keeps its draft after import;
**Reload from server** discards that draft and reads the new remote bytes.
See [configuration import and Files](harnesses.md#agent-configuration-import-and-files)
for permissions, interruption handling, and manual snapshot commands.

**OpenCode recovery:** its configuration destination is `~/.config/opencode`,
not `~/.local/share/opencode`. If you imported with an older destination,
explicitly choose your local `~/.config/opencode` directory again, select the
OpenCode destination, review, and re-import. Inspect the files under the new
root and reload/restart OpenCode as needed. Old imports are not automatically
moved. Leave login credentials at `~/.local/share/opencode/auth.json`;
do not move the data directory or import credentials to repair configuration.

### Agent coordination hooks

Coordination mail is durable: a successful send means stored, not read. An
agent actively awaiting a reply should use `aether-internal inbox --wait 30`
and explicitly acknowledge the batch only after handling it.

Loaded native pi, OMP, and version-matched OpenCode integrations can wake an
eligible live idle session without terminal keystrokes. Native wake respects
human protection, takeover, and Stop. Claude Code, Codex, Copilot CLI,
Gemini CLI, and Cursor CLI command hooks instead announce mail at their next
supported lifecycle boundary. Neither path restarts an exited run.

Inside a run, `aether-internal skill` checks configuration and prints setup
instructions. **Configured is not loaded, trusted, or executed**: restart or
reload as the harness requires and inspect its real hook/plugin errors.
See [per-harness setup](harnesses.md#incoming-coordination-hooks) for managed
loading, copyable files, versions, and disable controls; see
[delivery and acknowledgement](coordination.md#delivery-acknowledgement-and-retries)
for the runtime contract. An unlisted CLI can use the
[unsupported-harness authoring guide](harness-integration.md) without adding
a new daemon or terminal fallback.

### Connect GitHub

If runs should publish to GitHub, connect an account with write permission
to the checkout Origin from step 4. Source deploy keys do not grant that
permission, and enterprise policy may require additional authorization.

In **Onboarding → Agents** or **Agents**, **Connect GitHub** opens the
environment terminal with the login command. **I've logged in** finishes
setup and reports the account and registered signing key.

From the CLI it is two commands. In the environment terminal:

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

The dashboard's **First run** step names the workspace/base and checks source
readiness. If no agent is installed, use **Set up an agent**. For an empty or
unaccepted base, use **Review repository setup**, finish the push or source
adoption, then **Check source again**. Installation readiness does not prove
vendor login or upstream publishing permission.

```sh
aether run "add a health check endpoint" --workspace myproject --agent claude
```

The run gets its own container and checkout while using your persistent home.

```
run 01m04mhf114eap4k85n2mgcped running
```

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
the CLI can do works from the page, plus local verbs like pulling a run
branch into your clone. Leave it running; Ctrl-C stops the gateway and the
token dies with it. `aether gui --url` prints the URL instead of opening a
browser. See [local-gateway.md](local-gateway.md).

On a tailnet, the server can host the dashboard instead, so a phone or any
other tailnet device opens `https://<the server's MagicDNS name>/` with
nothing installed and no token. Set `web-port` and restart the server; see
[networking.md](networking.md#the-dashboard). Hosted onboarding supports remote
repository import, Git identity, agent setup, and configuration import/editing.
It has no machine-local link, clone, push, or pull operations; those require
the desktop app or `aether gui` on the computer holding the clone.

### Prefer a native window?

`aether gui` in a browser tab is the whole dashboard. If you would rather it
lived in its own window - with desktop notifications and a dock badge when a
run parks in `needs-attention`, plus `aether://run/<id>` deep links - build
the desktop app. Answering **client** in step 1 already did this. Nothing has
to be installed first - the CLI fetches its own Node.js copy when the machine
has none, which makes the first build longer:

```sh
aether gui build
```

That installs Aether into your application menu (Linux), your Applications
folder (macOS; `~/Applications` without administrator rights, and the command
prints the path), or the Start Menu (Windows). Open it like any other app.

Two things to know:

- **There is no download.** No release publishes an installer. `aether gui
  build` packages the Electron shell on your machine from sources carried in
  the CLI. Details in [install.md](install.md#desktop-app).
- **It is not a standalone client.** The app does not bundle `aether`; it
  launches `aether gui` from your `PATH`, and the dashboard lives inside that
  CLI binary. Install the CLI ([step 1](#1-install)); on an unlinked machine,
  the app opens its local onboarding wizard and links from there. When you
  update the CLI, the window picks up the new dashboard without rebuilding the
  app.

In the dashboard: a workspace switcher over the runs in scope, a board
bucketed by what needs attention, a live terminal mirror per run, the diff
timeline, the workspace feed. Read-only by default; typing into a run needs
the steer capability, which as the owner you have.

The terminal escape hatch is `aether attach <run-id>` - a raw byte-for-byte
passthrough where every native keybind and theme of the agent's own TUI works.
Detach without killing anything: the PTY lives on the server.

To nudge a running agent without attaching:

```sh
aether inject <run-id> "also update the README"
```

The message appears in the transcript as a banner in your member color, and
everyone watching sees who said it.

## 8. Review and publish the result

### Remote-only: commit, push and open a PR

Open the run's **Diff** tab and expand **Native changes & publish**. The
existing **Land**, candidate review and interval timeline remain separate:
a GitHub PR is not an internal candidate proposal.

1. Inspect **Run checkout**: the native branch and HEAD, **Selected run
   account**, **GitHub identity** (or its real authentication error), and
   changed, staged and untracked paths. Connect GitHub in that account's
   environment first; mirror deploy keys do not provide push credentials.
2. Check exact paths, then click **Review selected paths**. The view shows
   worktree and staged diffs separately, with content previews for selected
   untracked files. Enter a **Commit message** and click **Commit selected
   paths**. This commits those paths' current worktree contents, not only
   their staged hunks; unselected staged paths are preserved. Native identity
   and signing apply, but commit hooks do not run.
3. Inspect **Commit outcome** and native diagnostics. **Committed: yes** with
   **Index updated: no** means the commit exists but index reconciliation
   failed: inspect the checkout instead of repeating the commit. A branch/HEAD
   mismatch requires **Refresh native status** and a fresh review.
4. Under **Push branch**, choose **Push remote**, its exact **Writable push
   URL**, and **Push head branch**. Check the account/branch/HEAD/destination
   review box, then **Push reviewed branch**. Add any missing fork remote using
   native Git in the run terminal first. Push is non-force and does not switch
   branches or change workspace Origin, source mirror or accepted base.
5. Under **GitHub pull request**, explicitly enter **PR repository
   (owner/name)** and **PR base branch**, separately from **PR head repository
   (owner/name)** and **PR head branch**. For a fork, the PR repository is the
   upstream and the head repository is your fork; neither choice reconfigures
   the mirror or Origin. Click **Discover existing PR** to find that exact
   head/base, including PRs created using native `gh`.
6. If no PR exists, review the returned GitHub identity and exact target, enter
   **PR title** / **PR description**, optionally check **Draft PR**, check the
   identity/target review box, and click **Create reviewed PR**. If creation is
   uncertain, use **Reconcile PR read-only** rather than repeating creation.
   A PR failure never erases a successful **Pushed: yes** outcome.
7. Click **Refresh PR feedback** for typed checks, comments, reviews and inline
   feedback (including file/line and commit context). Check only the feedback
   you want to send, then **Send selected feedback to Run Room**. This creates
   a normal moderated steering request; its receipt is not proof of delivery
   unless it says sent. Open the run terminal and **Run Room** to inspect the
   durable message and delivery state.

Native status is read when this view opens and after explicit actions; GitHub
discovery and feedback refresh are explicit. No background PR watcher, automatic
merge, force push, branch switch or automatic mutation retry is performed.
GitHub discovery, creation and feedback need a working native `gh`, network
access, and the run's GitHub login's permissions on the explicit upstream/fork:
the `gh` login in the home the run's container mounts: its launcher's, also on
a shared agent account and after a handoff.
Review and merge on GitHub according to your repository's policy.

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
Branch aether/run-add-a-health-check-endpoint-mgcped is ready. Switch with: git switch aether/run-add-a-health-check-endpoint-mgcped
```

The branch name is `aether/run-<slug>-<short-id>`: the task slugified, then the
last six characters of the run ID. Aether never switches branches or merges
the run into your base branch. These examples use `trunk`, as in the local
clone setup above; substitute your workspace's actual base branch. Review
and diff the run branch:

```sh
git log --oneline aether/run-add-a-health-check-endpoint-mgcped
git diff trunk...aether/run-add-a-health-check-endpoint-mgcped
```

If the local checkout has uncommitted changes, the pull still fetches the run
branch and reports that the checkout is dirty. Commit or stash those changes
before switching to the run branch or merging it.

When the review is complete, merge locally. In a local-only workspace, push
the reviewed base back to Aether:

```sh
git switch trunk
git merge aether/run-add-a-health-check-endpoint-mgcped
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

Then close the run out so it leaves the attention board:

```sh
aether close <run-id> --outcome merged      # or --outcome abandoned
```

Closing retains the exact TUI container, checkout, run row, member account and
coordination surfaces for `--run-container-ttl` (default `7 days`). Before that
retention expires, reopen the same run with:

```sh
aether relaunch <run-id>
```

Relaunch does not create a new run or container and expired or unavailable runs
cannot be relaunched. Kill and Delete remain immediate cleanup operations.

Mission workers also retain their exact containers for the same default
7 days after a success/failure report or container exit, without continuing
work or holding attempt capacity after retention settles. Inspect their
transcript, diff, evidence and worker details normally. Unlike explicitly
closed ordinary TUI runs, completed workers cannot be relaunched.

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
contains `--sync-origin` will fail after upgrade; reinstall it with the
command above and activate the newly written unit. There is no recurring
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
offer only agents installed in the account, plus the `custom` harness a
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

`aether runs` shows the run reaching `needs-attention` within seconds, and the
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
| `not signed in` from `aether link --claim` or `aether servers` | This machine has no edge sign-in. Run `aether login`. |
| `claim code is wrong`, `claim code expired` or `claim code has no attempts left` | On the server, `sudo aether-server edge claim-code` prints a new code, valid 30 minutes. |
| `server is not connected to the edge` | The server is stopped or cannot reach the edge. On the server, `sudo aether-server edge status` shows the last connection error. |
| `not a member of this server` | Your edge account has no membership or open invitation there. An admin runs `aether invite --github <your-login>` ([teams.md](teams.md#through-an-edge)). |
| `device "<label>", signed in as <account>, is waiting for approval` | The server admits approved devices only (`edge-access approved-devices`), and this one is new. Run the `aether device approve <code>` the message prints from a device, SSH key or tailnet connection you already use, or give the code to an admin or to the machine's administrator (`sudo aether-server device approve <code>`). Either shows the member and role the code admits the device as and asks before approving. |
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
- [environment-home.md](environment-home.md) - member home, installed agents, and migration
- [networking.md](networking.md) - Tailscale-first, plus LAN and VPN
- [teams.md](teams.md) - joining, roles, workspaces
- [harnesses.md](harnesses.md) - login, configuration import, and launch definitions
- [security.md](security.md) - what the container boundary does and does not do
