# Quickstart

Zero to a finished agent run in about ten minutes, solo.

Two machines are involved, though they can be the same one:

- **the server box** - a Linux machine with Docker and git. Agents run here.
- **your machine** - Linux, macOS, or Windows. Where you type.

Your machine needs git and, unless both machines are on a tailnet
(see [step 3](#3-link-from-your-machine)), an SSH key. Aether uses
`~/.ssh/id_ed25519` and your ssh-agent, so `ssh-add` a passphrase-protected
key first. Windows paths and the OpenSSH agent service are in
[install.md](install.md#the-windows-client).

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

On **Windows** the client is a manual download - three PowerShell commands in
[install.md](install.md#manual-install). Everything else there is optional:
pinning a version, the data layout, the desktop app.

## 2. Start the server

Answering **server** in step 1 already ran the command below on the server
box. It writes the config and the systemd unit but deliberately starts
nothing, so the activation line it printed is still yours to run. Run setup by
hand if you skipped the question:

```sh
sudo aether-server setup
```

It asks for the listen address, data directory, and tailnet policy (Enter
accepts each default), then prints:

```sh
systemctl daemon-reload && systemctl enable --now aether-server
```

Run that and the server is live on `:2222`. The SSH host key is generated on
first start and nothing is exposed to the network except the SSH port. Change
any option later with `aether-server config set <key> <value>`, then restart.

To try it in the foreground first, `sudo aether-server serve` runs until
Ctrl-C. [install.md](install.md) covers unattended installs, running
unprivileged, and every serve option.

## 3. Link from your machine

```sh
aether link <server-host>:2222
```

Output:

```
linked to <server-host>:2222 as admin (admin)
```

**The first identity to link a fresh server becomes the admin.** That is the
whole account setup - there is no signup, no password, no config file to edit.
The link is saved to `~/.config/aether/config.json`, or
`%AppData%\aether\config.json` on Windows. (Joining over a tailnet,
the display name comes from your tailnet login instead of the literal
`admin`; the role is the same. Change any display color with
`aether member color <#rrggbb>`.)

How you were identified depends on the network:

- **On a tailnet:** Tailscale already knows who you are and the server asks it.
  No SSH key, no invite code, nothing to copy. See
  [networking.md](networking.md).
- **Anywhere else:** your SSH public key (`~/.ssh/id_ed25519`, or any key in
  your ssh-agent) is registered as the admin's key. Generate one first with
  `ssh-keygen -t ed25519` if you do not have one. On Windows that is
  `%USERPROFILE%\.ssh\id_ed25519` and the OpenSSH agent service; `ssh-keygen`
  ships with Windows OpenSSH.

On first contact `aether` records the server's host key in `~/.ssh/known_hosts`
(`%USERPROFILE%\.ssh\known_hosts` on Windows) and prints its fingerprint.
Compare that against what the server printed if you care to.

## 4. Create a workspace and push your repo

A **workspace** is a repo plus a server-owned environment. Creating one is an
admin operation. Start with the standard environment - a prebuilt image
published with each release that carries git, go, node, python with uv, rust,
and common build tools, so most projects work with zero setup:

```sh
aether workspace init myproject --standard
```

The dashboard's create-workspace form offers the same choice with the
standard environment preselected. Its onboarding wizard then adds an
Environment step: keep the standard environment, have a coding agent on
your machine mirror your local toolchains into the workspace image, or
have it read the repository's own files - devcontainer config included -
and build what the project needs. The
agent proposes a list of tools, you review and approve it, and the image
builds in the background while you finish onboarding - until it is ready,
runs use the image the workspace was created with and the dashboard shows a
banner saying so. An Agents step follows the Repository step and covers
step 5 below.

To change the environment later, open the workspace page in the dashboard.
Its Environment panel shows what is installed, keeps every previous version
for rollback, and takes a plain-language change request: a coding agent
registered on the server proposes the change, you review the Dockerfile
diff and the updated tool list, and approving rebuilds the image.
Environment changes are admin actions.

Without `--standard` the server uses its minimal neutral image. Pass
`--image <ref>` instead for an administrator-approved image when the project
needs something the standard one lacks, and `--base <branch>` to cut run
worktrees from something other than `main`.

Now point your local clone at it and seed the repo:

```sh
aether link <server-host>:2222 --repo ~/code/myproject
cd ~/code/myproject
git push -u aether main
```

`link --repo` adds an `aether` git remote - a normal git remote over the same
SSH port, no separate credentials. With multiple workspaces, add
`--workspace <name-or-id>` (`aether workspace list` shows them). The push
sends the workspace's base branch; replace `main` if you created the
workspace with `--base`.

In the dashboard, the onboarding wizard's Repository step does both for
you: it adds the remote, then its **Push now** button runs that push in
your clone and keeps git's own output on the page. It runs the same push
with `--no-follow-tags`, so the command above also sends your tags if you
have `push.followTags` set.

## 5. Set up your agent

Choose an agent once:

```sh
aether agent add claude
```

For a shipped agent, the command shows its vendor install script. Open the
environment terminal, run the script, install into `~/.local/bin`, and complete
the vendor login there:

```sh
aether terminal
```

For a name Aether does not ship, the command first asks for interactive and
headless launch templates. Install that executable into `~/.local/bin` using
the vendor's instructions, then complete its login in the environment
terminal. Return to the dashboard when finished.

The member home persists the executable, login state, and synced profile files
across containers. The terminal command ships in this release series.

Your own agent configuration - skills, custom commands, standing
instructions like `CLAUDE.md`, settings, plugins - is separate from the
login and syncs one way from your machine:

```sh
aether profile push --agent claude
```

The dashboard does both without a terminal. Its onboarding wizard's
**Agents** step opens the same setup shell in the page, and then shows what
a profile push would carry from each agent you have configured locally,
grouped as skills, commands, memory, settings, MCP config and plugins, with
every file the credential denylist or the secret scanner left behind and
why. Check the agents you want and approve. Where a setup-capable agent is
installed on your machine you can also let one read the inventory and
recommend what is worth bringing, with a sentence of reasoning per agent;
the recommendation is a checklist you edit, never something that acts on
its own. Both parts are optional - **Skip for now** moves on.

Secrets never sync, and the dashboard has no override: a scanner finding in
a file you wrote refuses the push and names the file so you can fix it
locally. One inside an installed plugin drops that file and imports the
rest, since there is nothing to fix in your own configuration.
[harnesses.md](harnesses.md) has the full rules, including the CLI-only
`--allow-secret`.

## 6. Launch a run

```sh
aether run "add a health check endpoint" --agent claude
```

The run gets its own container and checkout while using your persistent home.

```
run 01m04mhf114eap4k85n2mgcped running
```

A **run** is one agent execution with its own container, git worktree, and
branch; `aether runs` lists them. Scoped commands default to the only
workspace when there is exactly one, which is why nothing above named it.

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
  CLI binary. So install ([step 1](#1-install)) and link
  ([step 3](#3-link-from-your-machine)) first. When you update the CLI, the
  window picks up the new dashboard without rebuilding the app.

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

## 8. Pull the result

When the agent exits, its work is already committed to the run's branch and the
run parks in `needs-attention`.

```sh
aether pull <run-id>
```

```
fetched aether/run-add-a-health-check-endpoint-mgcped into refs/remotes/aether/aether/run-add-a-health-check-endpoint-mgcped (not merged)
```

A run branch is named `aether/run-<slug>-<short-id>`: the task slugified,
then the last six characters of the run ID. The branch is now in your local
clone as a remote-tracking ref. Review it, diff it, merge it - by hand.
**Aether never merges anything for you.**

```sh
git log --oneline aether/aether/run-add-a-health-check-endpoint-mgcped
git diff main...aether/aether/run-add-a-health-check-endpoint-mgcped
```

Then close the run out so it leaves the attention board:

```sh
aether close <run-id> --outcome merged      # or --outcome abandoned
```


To stop pulling by hand, run the local sync daemon - it fetches run branches as
they update and pushes your base branch up so new runs start from current
reality:

```sh
aether daemon install --server <server-host>:2222 --repo ~/code/myproject
systemctl --user daemon-reload && systemctl --user enable --now aether-daemon
```

That second line is the Linux one. `daemon install` prints the activation
command for whatever platform you are on: `launchctl load` on macOS,
`schtasks /Create` on Windows.

---

## Prove the plumbing without an agent subscription

No vendor login yet? Aether ships a deterministic `fake` harness for exactly
this: it runs a script from your repo instead of an agent, so you can drive the
whole lifecycle end to end and see a real branch come back.

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

Then run steps 3, 4, 6 and 8 above with `busybox` as the workspace image
and `--agent fake` instead of `--agent claude`. Skip step 5: the fake harness has
no agent login. Step 7 (`aether gui`) works too if you want to watch.

```sh
aether link <server-host>:2222
aether workspace init demo --image busybox
aether link <server-host>:2222 --repo "$PWD"
git push -u aether main
aether run "write a result file" --agent fake
aether runs
aether pull <run-id>
```

`aether runs` shows the run reaching `needs-attention` within seconds, and the
pulled branch carries a commit adding `result.txt`. That is the full path -
container, worktree, PTY, commit, fetch - with nothing mocked but the agent.

## When something does not work

| Symptom | Cause |
| --- | --- |
| `not linked; run aether link <addr>` | No `~/.config/aether/config.json` (`%AppData%\aether\config.json` on Windows) on this machine yet. |
| `no Aether member for this key` | The server already has an admin, so you are not the first member. Get an invite: [teams.md](teams.md). |
| `unable to authenticate, attempted methods [none]` | The CLI found no usable key: none at `~/.ssh/id_ed25519`, no ssh-agent, or a passphrase-protected key with no agent to unlock it. Run `ssh-add`, or generate an unencrypted key. On Windows, check `Get-Service ssh-agent` and look for the key at `%USERPROFILE%\.ssh\id_ed25519`. |
| `host key mismatch` / `REMOTE HOST IDENTIFICATION HAS CHANGED` on `aether link` | The server was reinstalled and generated a new host key, but your `known_hosts` still trusts the old one. Clear it: `ssh-keygen -R '[<server-host>]:2222'`. |
| `tailnet identity unavailable; key authentication required` | Informational, not an error. The server has Tailscale but this connection did not arrive over the tailnet, so it fell back to your SSH key. |
| `membership pending admin approval` | You joined over a tailnet on a server that requires approval. An admin runs `aether member approve <your-member-id>`. |
| `no workspace yet; skip git remote` | Run `aether workspace init` first, then re-run `aether link --repo`. |
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
- [environments.md](environments.md) - agent-built workspace images, verification, rollback
- [environment-home.md](environment-home.md) - member home, installed agents, and migration
- [networking.md](networking.md) - Tailscale-first, plus LAN and VPN
- [teams.md](teams.md) - joining, roles, workspaces
- [harnesses.md](harnesses.md) - login, profile sync, and launch definitions
- [security.md](security.md) - what the container boundary does and does not do
