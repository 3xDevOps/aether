# Quickstart

Zero to a finished agent run in about ten minutes, solo. Everything below was
run against a real server; nothing is aspirational.

Two machines are involved, though they can be the same one:

- **the server box** - a Linux machine with Docker. This is where agents run.
- **your machine** - laptop or desktop, any OS. This is where you type.

You need on the server box: Linux, Docker (running, and your user able to talk
to it), and git. On your machine: git, and an SSH key unless both machines are
on a tailnet (see [step 3](#3-link-from-your-machine)). Aether reads
`~/.ssh/id_ed25519` and your ssh-agent; a **passphrase-protected key only works
through the agent**, so `ssh-add` it first.

---

## 1. Install

On both machines:

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh
```

On Linux that installs `aether` and `aether-server`; on macOS just `aether`
(the server is Linux-only). Details, pinning a version, and the systemd unit
are in [install.md](install.md).

## 2. Start the server

On the server box:

```sh
sudo aether init --data-dir /var/lib/aether
```

`init` creates the data directory, tells you whether Tailscale was detected
and what the server's tailnet hostname is, and prints the two commands that
come next. It does not start anything.

```sh
sudo aether-server serve --data-dir /var/lib/aether --addr :2222 --dashboard-port 8080
```

(`sudo` because `/var/lib` is root-owned and the server needs the Docker
socket. To try it without root, point `--data-dir` at a directory in your home
and make sure your user is in the `docker` group.)

That runs in the foreground until Ctrl-C, which is what you want the first
time. To make it permanent, install the systemd unit from
[install.md](install.md#run-it-under-systemd).

The SSH host key is generated into `<data-dir>/ssh/` on first start; there is
nothing to configure. `--dashboard-port 8080` gives the dashboard a loopback
listener that `aether dash` can forward to. Nothing is exposed to the network
except the SSH port.

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
The link is saved to `~/.config/aether/config.json`. (Joining over a tailnet,
the display name comes from your tailnet login instead of the literal
`admin`; the role is the same. Change any display color with
`aether member color <#rrggbb>`.)

How you were identified depends on the network:

- **On a tailnet:** Tailscale already knows who you are and the server asks it.
  No SSH key, no invite code, nothing to copy. See
  [networking.md](networking.md).
- **Anywhere else:** your SSH public key (`~/.ssh/id_ed25519`, or any key in
  your ssh-agent) is registered as the admin's key. Generate one first with
  `ssh-keygen -t ed25519` if you do not have one.

On first contact `aether` records the server's host key in `~/.ssh/known_hosts`
and prints its fingerprint. Compare that against what the server printed if you
care to.

## 4. Initialize a workspace and push your repo

A **workspace** is a repo plus a server-owned environment plan. With no image
option, the server selects its neutral bootstrap image. Workspace creation is
an administrator operation:

```sh
aether workspace init myproject
```

Use a custom, administrator-approved image only when the project needs system
packages that the neutral image does not provide:

```sh
aether workspace init myproject --image registry.example.invalid/team/base:version
```

The custom image is workspace configuration. A member cannot replace it from a
bootstrap or login shell. See [install.md](install.md) for image policy.

Now point your local clone at it and seed the repo:

```sh
aether link <server-host>:2222 --repo ~/code/myproject
cd ~/code/myproject
git push -u aether main
```

Re-running `link` with `--repo` adds an `aether` git remote to that clone.
(Linking before any workspace existed skipped that step and said so; this is
the re-run it asked for.) The remote is a normal git remote over the same SSH
port - no separate credentials.

## 5. Bootstrap the workspace tools

Bootstrap opens a shell **inside a server-created container**. It mounts a
private, per-member and per-workspace staging directory at `~/.local` with
read-write access. Install the agent executable using its vendor documentation,
then leave the shell:

```sh
aether workspace bootstrap myproject --command omp
```

The `--command` value is only an optional executable check. It does not select
an installer or grant the shell access to the host. On a clean exit, Aether
verifies the executable, creates an immutable tool snapshot, and activates it.
User-local files under `~/.local` persist; system packages and other container
filesystem changes do not. Use `aether workspace tools verify` later to report
whether the active snapshot records the requested executable.

If the SSH client disconnects before you exit, the pending staging directory is
kept for this member and workspace. Resume it with:

```sh
aether workspace bootstrap myproject --resume --command omp
```

Discard that pending staging state instead:

```sh
aether workspace bootstrap myproject --reset
```

See [bootstrap.md](bootstrap.md) for snapshot inspection, rollback, reset,
recovery, and the image escape hatch.

Now run:

```sh
aether workspace tools list myproject
aether workspace tools verify myproject --command omp
```

The list shows snapshot IDs, active state, executable, version, and creation
time. It never prints host paths or tool contents.

## 6. Log the agent in

Login is separate from tool installation. Aether gives you a terminal in a
container using the active tool snapshot mounted read-only:

```sh
aether setup omp --workspace myproject
```

Run the harness vendor's login flow in that shell, then `exit`. Login state is
saved in a separate per-member credential home and mounted only according to
the registered harness definition. Profile snapshots are separate again; they
are not credentials and never become part of a tool snapshot. See
[harnesses.md](harnesses.md).

## 7. Launch a run

The `omp` name above is only an example. An administrator must register a
generic harness definition for it before `aether run` can launch it:

```sh
aether session new myproject --workspace myproject
aether run "add a health check endpoint" --agent omp
```

The run pins the complete environment plan, including the active tool
snapshot. Later bootstrap or rollback operations affect later runs, not this
one.

```
run 01m04mhf114eap4k85n2mgcped running
```

A **session** is the shared context runs live in (members, feed, budgets); a
**run** is one agent execution with its own container, its own git worktree,
and its own branch. `aether runs` lists them.

## 8. Watch it

```sh
aether dash
```

This opens an SSH port-forward to the dashboard and a browser tab already
carrying a token minted over that SSH connection. Leave it running - it holds
the tunnel open, and Ctrl-C both closes the tunnel and revokes the token.
`aether dash --url` prints the URL instead of opening a browser.

In the dashboard: a sidebar of sessions and runs, a board bucketed by what
needs attention, a live terminal mirror per run, the diff timeline, the session
feed. Read-only by default; typing into a run needs the steer capability, which
as the owner you have.

The terminal escape hatch is `aether attach <run-id>` - a raw byte-for-byte
passthrough where every native keybind and theme of the agent's own TUI works.
Detach without killing anything: the PTY lives on the server.

To nudge a running agent without attaching:

```sh
aether inject <run-id> "also update the README"
```

The message appears in the transcript as a banner in your member color, and
everyone watching sees who said it.

## 9. Pull the result

When the agent exits, its work is already committed to the run's branch and the
run parks in `needs-attention`.

```sh
aether pull <run-id>
```

```
fetched aether/run-01m04...-add-a-health-check into refs/remotes/aether/aether/run-01m04...-add-a-health-check (not merged)
```

The branch is now in your local clone as a remote-tracking ref. Review it, diff
it, merge it - by hand. **Aether never merges anything for you.**

```sh
git log --oneline aether/aether/run-<id>-<slug>
git diff main...aether/aether/run-<id>-<slug>
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

---

## Prove the plumbing without an agent subscription

No vendor login yet? Aether ships a deterministic `fake` harness for exactly
this: it runs a script from your repo instead of an agent, so you can drive the
whole lifecycle end to end and see a real branch come back.

Start the server with the fake agent's command in its environment:

```sh
AETHER_FAKE_AGENT="sh /workspace/agent.sh" \
  aether-server serve --data-dir /var/lib/aether --addr :2222 --dashboard-port 8080
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

Then run steps 3, 4, 7, 8 and 9 above with `busybox` as the workspace image
and `--agent fake` instead of `--agent omp`. Skip steps 5 and 6 because the
fake harness has no agent login.

```sh
aether link <server-host>:2222
aether workspace add demo --image busybox
aether link <server-host>:2222 --repo "$PWD"
git push -u aether main
aether session new demo --workspace demo
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
| `not linked; run aether link <addr>` | No `~/.config/aether/config.json` on this machine yet. |
| `no Aether member for this key` | The server already has an admin, so you are not bootstrapping. Get an invite: [teams.md](teams.md). |
| `unable to authenticate, attempted methods [none]` | The CLI found no usable key: none at `~/.ssh/id_ed25519`, no ssh-agent, or a passphrase-protected key with no agent to unlock it. Run `ssh-add`, or generate an unencrypted key. |
| `tailnet identity unavailable; key authentication required` | Informational, not an error. The server has Tailscale but this connection did not arrive over the tailnet, so it fell back to your SSH key. |
| `membership pending admin approval` | You joined over a tailnet on a server that requires approval. An admin runs `aether member approve <your-member-id>`. |
| `no workspace yet; skip git remote` | Run `aether workspace init` first, then re-run `aether link --repo`. |
| `multiple workspaces available; specify --workspace` | Pass `--workspace <name>` to `aether setup`, or select one explicitly for bootstrap and tools commands. |
| `workspace tools verify` reports failure | The active snapshot does not contain the requested executable, or it is not executable. Bootstrap again with `--command <executable>`. |
| Run reaches `failed` immediately | The agent started and exited. `aether timeline --run <run-id>` shows the exit code; `aether attach` only works while a run is alive. |
| `this server forwards no dashboard port` | The server was started without `--dashboard-port` or `--dashboard-addr`. |

## Next

- [install.md](install.md) - systemd, upgrades, data layout
- [bootstrap.md](bootstrap.md) - bootstrap shells, snapshots, recovery, and images
- [networking.md](networking.md) - Tailscale-first, plus LAN and VPN
- [teams.md](teams.md) - joining, roles, sessions
- [harnesses.md](harnesses.md) - login, profile sync, tool snapshots, and launch definitions
- [security.md](security.md) - what the container boundary does and does not do
