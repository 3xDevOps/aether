# Installing Aether

Two binaries, no dependencies of their own. `aether-server` runs on Linux and
needs Docker and git on the host; `aether` is the client and runs anywhere.

For the fastest path from nothing to a finished run, follow
[quickstart.md](quickstart.md). This file is the reference: what the installer
does, how to run the server as a service, and what lives in the data directory.

## The install script

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh
```

It detects your OS and CPU, downloads the matching binaries from the latest
GitHub release, verifies each one against the release's `checksums.txt`, and
installs them into `/usr/local/bin` (via `sudo` if needed, falling back to
`~/.local/bin` when there is no sudo). On Linux it installs both binaries; on
macOS only `aether`, because the server is Linux-only.

The script is POSIX-only: it covers Linux and macOS. There is no Windows
installer and no PowerShell equivalent. Windows clients install by hand, which
is three steps: see [Manual install](#manual-install).

A checksum mismatch aborts the install. The script needs `curl` or `wget`, and
`sha256sum` or `shasum`.

Options, as flags or environment variables:

| Flag | Variable | Effect |
| --- | --- | --- |
| `--version <tag>` | `AETHER_VERSION` | Install a specific release instead of the latest. |
| `--bin-dir <dir>` | `AETHER_BIN_DIR` | Install somewhere else. |
| `--client` | `AETHER_COMPONENTS=client` | CLI only. |
| `--server` | `AETHER_COMPONENTS=server` | Server only. |
| | `AETHER_REPO` | Pull from a fork. |
| | `AETHER_BASE_URL` | Pull from a mirror of the release assets. |

Passing flags through a pipe needs `sh -s --`:

```sh
curl -fsSL .../install.sh | sh -s -- --client --bin-dir ~/.local/bin
```

**Upgrading:** `aether update` replaces the running CLI with the latest
release (or `--version <tag>`), verifying it against the release's
`checksums.txt`. On a Linux host with `aether-server` installed next to the
CLI it updates both and reminds you to
`sudo systemctl restart aether-server`. `aether update --check` only reports
whether a newer release exists. Binaries in `/usr/local/bin` need
`sudo aether update`. Re-running the installer does the same job; the data
directory is untouched either way. `aether update` is not a Windows command;
it refuses to run there. Upgrading a Windows client means downloading the new
release binary over the old one, exactly as below.

## Manual install

Every release publishes bare binaries plus `checksums.txt`:

```
aether-server-linux-amd64   aether-server-linux-arm64
aether-linux-amd64          aether-linux-arm64
aether-darwin-amd64         aether-darwin-arm64
aether-windows-amd64.exe    aether-windows-arm64.exe
```

`aether-server` is Linux-only. The Windows and macOS assets are the client.

**Linux and macOS.** Download the one you want, check it against
`checksums.txt`, `chmod +x`, and drop it on your `PATH` under the name
`aether` or `aether-server`.

**Windows.** In PowerShell, from the directory you downloaded into:

```powershell
# 1. Verify. Compare this against the matching line in checksums.txt.
Get-FileHash -Algorithm SHA256 .\aether-windows-amd64.exe

# 2. Put it somewhere on PATH under the name aether.exe.
$dir = "$env:LOCALAPPDATA\Programs\Aether"
New-Item -ItemType Directory -Force -Path $dir
Move-Item .\aether-windows-amd64.exe "$dir\aether.exe"

# 3. Add that directory to your user PATH (once), then open a new terminal.
[Environment]::SetEnvironmentVariable(
  "Path", "$([Environment]::GetEnvironmentVariable('Path','User'));$dir", "User")
```

Use `aether-windows-arm64.exe` on an Arm device. Confirm it works with
`aether version` in a fresh terminal. Windows Defender SmartScreen may warn on
first run: the binaries are not code-signed, which is why verifying the hash
matters. To upgrade later, repeat this with the newer release; there is no
self-update on Windows.

## The Windows client

Windows runs the client only. There is no `aether-server` for Windows and
there will not be one: every run is a Linux container on a Linux host.

Where the client keeps its state:

| What | Path |
| --- | --- |
| Linked-server config | `%AppData%\aether\config.json` |
| Host-key trust store | `%USERPROFILE%\.ssh\known_hosts` |
| Default private key | `%USERPROFILE%\.ssh\id_ed25519` |

**SSH agent.** The client talks to the Windows OpenSSH agent over its named
pipe, `\\.\pipe\openssh-ssh-agent`, so a passphrase-protected key works the
same way it does on Linux. The agent is a Windows service that is not running
by default:

```powershell
Get-Service ssh-agent                       # is it running?
Start-Service ssh-agent                     # start it now (needs admin)
Set-Service ssh-agent -StartupType Automatic
ssh-add $env:USERPROFILE\.ssh\id_ed25519
```

`SSH_AUTH_SOCK` takes precedence when it is set: the client dials it as a unix
socket rather than using the pipe. Leave it unset unless you deliberately run
a different agent. When neither is reachable the client falls back to the key
file rather than failing; only with no usable key either does `aether link`
report `attempted methods [none]`.

**Console.** `aether attach` mirrors an agent's TUI byte for byte, so the
console needs ANSI escape processing. The client enables it on the console it
writes to and restores the previous mode on exit. Windows Terminal and current
conhost handle it; a console that refuses is a cosmetic degradation, not a
failed attach.

**Not available on Windows**, by design rather than oversight:

- `aether init` refuses to run. It prepares a Linux server's data directory,
  so run it on the server box.
- `aether update` refuses to run. Re-download the release binary instead.
- `scripts/install.sh` is a POSIX shell script. Install by hand, above.
- `aether-server` itself. Point the client at a Linux server.

Everything else is the same client: `link`, `run`, `attach`, `gui`,
`pull`, `daemon`, and the rest.

## Building from source

Needs Go 1.25+, GNU make, and Bun 1.3+ (the server embeds the dashboard SPA, so
the web build runs first).

```sh
git clone https://github.com/3xDevOps/Aether
cd Aether
make build      # dashboard SPA, then both binaries into dist/
```

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the rest of the toolchain.

## Desktop app

Optional, in `desktop/`: an Electron shell that launches `aether gui` for you
and shows the dashboard in its own frameless window, with desktop
notifications, a needs-attention badge, and `aether://run/<id>` deep links. It
is the same SPA with the same full SSH authority, just without a browser tab to
lose. No release publishes it - build it yourself (needs Node 22+):

```sh
cd desktop
npm install
npm run dist    # installer for this OS into desktop/dist/
npm run start   # or run it unpackaged during development
```

It requires the `aether` CLI installed and linked first; the app does not
bundle the binary. It looks for `aether` in `AETHER_BIN`, then `PATH`, then the
installer defaults (`/usr/local/bin`, `~/.local/bin`). If launch fails with
"aether CLI not found", set `AETHER_BIN` to the binary's full path.

**The dashboard ships inside the CLI, not inside this app.** The SPA is
embedded in the `aether` binary (`web/embed.go`) and served by `aether gui`, so
a dashboard change reaches the window only when the CLI is rebuilt and
reinstalled - not when the desktop package is rebuilt:

```sh
make build && sudo install -m 0755 dist/aether /usr/local/bin/aether
```

If the window renders an older dashboard than your checkout, an older `aether`
is on your `PATH`; `aether version` prints the commit it was built from.
Packaging installers for other platforms and code signing are in
[CONTRIBUTING.md](../CONTRIBUTING.md#desktop-shell).

## Server prerequisites

- **Linux.** Windows and macOS are client platforms.
- **Docker**, running, with the server's user able to reach its socket. Every
  bootstrap shell, login shell, and run is a container.
- **git** on the host. Bare repos, run checkouts, and diffs are real git.
- **A server-owned neutral image** is selected automatically when a workspace
  has no custom image. An administrator may configure a custom image when
  system dependencies are required. Agent installation can instead happen in
  the workspace bootstrap shell; it is not a prerequisite for every image.
- Optionally **Tailscale**, which is the recommended way to make the SSH port
  reachable and the recommended identity layer. See
  [networking.md](networking.md).

## Images and containers

An image is a read-only package used to create containers. A container is one
runtime instance of that image and is discarded after a run or shell session.
The server opens shells only inside containers, never on the host. Aether never
mounts the Docker socket into a workspace container.

When a workspace has no custom image, the server selects the neutral bootstrap
image:

```
ghcr.io/3xdevops/aether-bootstrap:<release-tag>
```

It contains a shell, certificates, curl, Git, and common file-search tools. It
does not contain an agent vendor or execute a vendor installer. A member can
install an executable into `~/.local` with
`aether workspace bootstrap <name> --command <executable>`. Those files are
captured in a per-member, per-workspace immutable tool snapshot.

A release also publishes a prebuilt standard environment image:

```
ghcr.io/3xdevops/aether-standard:<release-tag>
```

It extends the neutral image with build tools (build-essential, pkg-config,
unzip), ripgrep, jq, and pinned toolchains usable by every container user: Go,
Node LTS via fnm, Python 3 with uv, and Rust via rustup. The versions are
pinned in `images/standard/Dockerfile` and change only with a release; a
workspace created with this image keeps its exact ref until an explicit image
change. Both images are tagged with the release tag, the commit hash, and
`latest`.

User-installed tools under `~/.local` persist across containers. System packages
installed into `/usr` or `/etc`, edits elsewhere in the container filesystem,
and container process state do not persist. Put required system dependencies in
an administrator-approved custom image.

Use `aether workspace init <name>` for the neutral default, or
`aether workspace init <name> --image <image>` when the project needs system
dependencies that the neutral image does not provide. Workspace image
references are administrator-owned configuration, not input to a shell session.

Workspaces can also carry a server-built environment image: an admin-saved
Dockerfile the server builds, verifies, and swaps in (see
[bootstrap.md](bootstrap.md)). These images live only in the server's Docker
daemon as `aether/ws-<workspace-id>:<version>` tags and are never pushed to
or pulled from a registry. Retention keeps two tags per workspace - the
active version and the most recent previously active one - and removes older
tags after a successful swap. `aether env rollback` re-activates the
previous version, rebuilding its image from the stored Dockerfile if
retention already removed the tag, so disk usage stays bounded at roughly
two images per workspace.

To prepare a normal Dockerfile and optional standard Dev Container metadata for
review and later image publication:

```
aether image init
aether image init --devcontainer
```

The command writes `Dockerfile`, `.dockerignore`, and, when requested,
`.devcontainer/devcontainer.json` in the current directory. It does not run a
build, log in to a registry, or install a vendor agent. Existing files are
preserved unless `--force` is supplied.

## First boot

`aether-server setup` walks you through the install: it asks for the listen
address, data directory, and tailnet policy (Enter accepts each default),
writes the systemd unit and the config file, and prints the command that
starts the service.

```sh
sudo aether-server setup
```

For an unattended install, `aether-server install` writes the same files from
flags instead of questions - any serve option below is accepted, and options
you leave off keep tracking the binary's defaults across upgrades:

```sh
sudo aether-server install --addr :2222 --tailnet-auto-join
```

Neither command starts anything; both print the activation line so an install
never restarts a live server behind your back:

```sh
systemctl daemon-reload && systemctl enable --now aether-server
systemctl status aether-server
journalctl -u aether-server -f
```

The unit runs the server as root and creates `/var/lib/aether` through
`StateDirectory=`. Root is deliberate: Docker socket access is already
root-equivalent on the host, and workspace images with a non-root user make the
server chown run checkouts to that UID, which needs `CAP_CHOWN`. The header
comment in the unit spells out how to run unprivileged instead, and what you
give up.

To run the server in the foreground instead - handy the first time - skip
setup and serve directly:

```sh
aether-server serve --data-dir /var/lib/aether --addr :2222
```

Serve options, which are also the config-file keys:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--data-dir` | `/var/lib/aether` | Everything the server owns. |
| `--addr` | `:2222` | The SSH listener. This is the only port that must be reachable. |
| `--neutral-image` | `ghcr.io/3xdevops/aether-bootstrap:<build-version>` | Server-owned image selected for workspaces without a custom image. A release build defaults to the image published with that release; a dev build tracks `latest`. Set this to a pinned deployment-approved image to override it. |
| `--standard-image` | `ghcr.io/3xdevops/aether-standard:<build-version>` | Standard environment image that clients recommend at workspace creation; `server.info` reports it alongside the neutral image. Same tagging rules as `--neutral-image`. |
| `--tailnet-auto-join` | off | Tailnet identities join approved instead of pending. |
| `--tailnet-require-key` | off | Tailnet connections must also present a registered SSH key. |
| `--conflict-coordination` | on | Let overlapping runs message each other; see [coordination.md](coordination.md). |
| `--stall-threshold` | `10m` | Silence after which a run parks needs-attention; see [failure-handling.md](failure-handling.md). |
| `--poll-interval` | `30s` | How often stalls are checked. |
| `--checkout-ttl` | `72h` | How long a finished run's worktree is kept. Negative disables the GC. |
| `--min-free-disk` | `1GiB` | Free bytes below which new runs are refused. Negative disables the floor. |
| `--harness-definitions` | none | Path to a custom harness registry file; see [harnesses.md](harnesses.md). |

Three things happen on the first start and never need attention again:

1. **The SSH host key** is generated into `<data-dir>/ssh/host_ed25519_key`.
   Clients record its fingerprint on first link and print it. Do not delete it:
   clients that already trust it refuse to connect until you clear the entry
   from their `known_hosts`.
2. **The first identity to link becomes the admin** - the SSH key, or the
   tailnet login, of whoever runs `aether link` first. There is no other
   account creation step.
3. **The SQLite store and the git repo root** are created under the data
   directory.

Options live in `/etc/aether/server.conf`, not in `ExecStart`. The file is
operator-owned, so binary updates and unit reinstalls never rewrite it, and
re-running `aether-server setup` or `install` keeps an existing config and
unit unless you pass `--force`. Change the config with
`aether-server config set <key> <value>` (or `config edit`), then restart.
`aether-server config show` prints every option with the value that would be
used and where it came from; `config path` prints the file's location.

An option removed in a later release does not stop the server: it logs one
warning naming the key and the file, and boots on the remaining settings.
`config show` flags the same keys so you can drop the lines with
`aether-server config edit` when convenient. A key that was never an option
is still an error, because a typo means a setting you believe is in force
never was.

Key-driven harnesses read the documented API-key environment variable names
from `/etc/aether/aether-server.env`, which the unit loads if it exists. Provide
those values through your deployment's secret manager; do not commit them or
paste them into public configuration examples.

Subscription logins do **not** go there. They live in the per-member,
per-harness server-side home that `aether agent add` (or `aether setup`)
writes. See
[harnesses.md](harnesses.md).

## The client-side sync daemon

Optional, on your machine, once per repo. It fetches run branches as agents
commit and pushes your base branch up so new runs start from current reality.

```sh
# Linux; the second line is whatever `daemon install` printed on your platform.
aether daemon install --server <server-host>:2222 --repo ~/code/myproject
systemctl --user daemon-reload && systemctl --user enable --now aether-daemon
```

`daemon install` writes a user-level service definition for your platform and
prints the command that activates it: a systemd user unit
(`~/.config/systemd/user/aether-daemon.service`) on Linux, a launchd agent
(`~/Library/LaunchAgents/com.aether.daemon.plist`) on macOS, and a Scheduled
Task XML (`%USERPROFILE%\aether-daemon.xml`, registered with
`schtasks /Create`) on Windows. `aether daemon run --server ... --repo ...`
does the same work in the foreground on any of them. The daemon also watches
your local agent-profile directories and pushes changes up; `--no-profile-sync`
turns that half off.

## What lives in the data directory

| Path | Contents |
| --- | --- |
| `aether.db` | SQLite: members, workspaces, runs, event log, and snapshot metadata. |
| `ssh/` | The server's SSH host key. |
| `repos/` | One bare git repo per workspace. |
| `checkouts/` | Per-run worktrees, garbage-collected after a TTL once a run finishes. |
| `transcripts/` | Per-run PTY recordings (asciicast v2). |
| `homes/<member>/<harness>/` | Per-member, per-harness login state. |
| `profiles/` | Content-addressed agent-profile snapshots. |
| `toolenv/staging/` | Pending per-member, per-workspace bootstrap staging. |
| `toolenv/snapshots/` | Immutable per-member, per-workspace tool snapshots. |
| `invites/` | Outstanding one-time invite codes. |
| `coord/` | Per-run conflict-coordination sockets, recreated each run. |
| `scheduler/`, `runtime/` | Scheduler state and the staged MCP bridge binary. |

Tool snapshots and pending staging are server-owned state. Back up the database
and `toolenv/` when recovery of installed workspace tools matters. Pending
staging can be resumed after a client disconnect, while stale unreferenced
staging is removed by the server's bounded cleanup policy. Active snapshots and
snapshots pinned by running work remain available. See
[bootstrap.md](bootstrap.md) for rollback, reset, and the read-only normal-run
mount.

Three consequences worth knowing:

- **Back up `aether.db`, `repos/`, `toolenv/`, and the homes you need to
  recover.** The database and git repos are core state. Tool snapshots,
  profile snapshots, and per-member login homes are also persistent workspace
  state.
- **Three of these grow without bound**: `checkouts/` (reclaimed by the TTL
  GC), `transcripts/`, and `aether.db` (the event log). The dashboard's disk
  gauge breaks the data directory down across exactly those three, and new
  runs are refused below `--min-free-disk`. See
  [failure-handling.md](failure-handling.md).
- **Keep the path short.** Per-run coordination sockets live under
  `coord/<run-id>/coord2.sock`, and unix socket paths have a hard length limit
  (about 100 characters). A very deep data directory makes the server log
  `coordination unavailable for this run` and fall back to the overlap notice.
  `/var/lib/aether` is nowhere near the limit.

If you run agents you do not trust, put the data directory on a filesystem
mounted `nosuid,nodev` - the reasoning is in [security.md](security.md).

## Uninstalling

Nothing here is automated: there is no uninstall script and no `make uninstall`,
because removing a server means deleting agent logins and git history that no
script should decide to throw away. The order below is the order that avoids
surprises, and it is also the way to get a clean slate for testing a fresh
install end to end.

### Server

```sh
# 1. Stop the service.
sudo systemctl disable --now aether-server
sudo rm -f /etc/systemd/system/aether-server.service
sudo systemctl daemon-reload

# 2. Containers. Stopping the server does NOT remove them.
sudo docker rm -f $(sudo docker ps -aq --filter label=aether.managed=true)
sudo docker rmi $(sudo docker images -q ghcr.io/3xdevops/aether-bootstrap)

# 3. State, config, binary.
sudo rm -rf /var/lib/aether /etc/aether
sudo rm -f /usr/local/bin/aether-server
sudo rm -rf /tmp/aether-patch-*
```

Step 2 is the one people miss. The scheduler deliberately leaves run containers
alive across a server restart so it can reattach to them, so they outlive the
unit. Every container the server creates carries `aether.managed=true` and is
named `aether-run-<id>`, so either the label filter or
`--filter name=^/aether-run-` finds them. Use `docker ps -a`, not `docker ps`:
a crashed run leaves an exited container behind.

The server writes no log files. Its output goes to the journal, so
`sudo journalctl --rotate && sudo journalctl --vacuum-time=1s` is what clears
the history if you want a silent baseline.

`/etc/aether` only exists if you used `aether-server setup`, `install`,
`config set`, or `config edit`, or if you created `aether-server.env` by hand
for API-key harnesses. No system user or group is ever created, so there is nothing to
`userdel`.

### Client

```sh
rm -rf ~/.config/aether ~/.config/aether-desktop
ssh-keygen -R '[<server-host>]:2222'
sudo rm -f /usr/local/bin/aether        # or ~/.local/bin/aether
```

On macOS the desktop state is `~/Library/Application Support/aether-desktop`;
on Windows it is `%APPDATA%\aether-desktop`, and the client binary is wherever
you put `aether.exe` on PATH.

The `ssh-keygen -R` line matters more than it looks. A reinstalled server
generates a new host key, so a stale `known_hosts` entry makes the next
`aether link` fail with a host key mismatch that reads like a bug. Clear the
entry for every address you linked through, including a tailnet name and a raw
IP for the same host.

The client never generates an SSH key of its own. It uses your existing
`~/.ssh/id_ed25519` or whatever your ssh-agent holds, so leave your keys
alone.

If you installed the sync daemon, remove it before the binary:

```sh
systemctl --user disable --now aether-daemon
rm -f ~/.config/systemd/user/aether-daemon.service
systemctl --user daemon-reload
```

macOS: `launchctl unload -w ~/Library/LaunchAgents/com.aether.daemon.plist`
then delete the plist. Windows: `schtasks /Delete /TN aether-daemon` then
delete `%USERPROFILE%\aether-daemon.xml`.

### Linked repositories

Each repo you linked has an `aether` git remote and one local branch per run
you pulled. Repoint the base branch **before** removing the remote:

```sh
cd ~/code/myproject
git branch --set-upstream-to=origin/main main   # link may have set this to aether
git branch -D $(git branch --list 'aether/run-*')
git remote remove aether
```

`aether link` sets `branch.<base>.remote` to `aether`. Drop the remote without
repointing and a plain `git push` on that branch starts failing for a reason
that is not obvious. Removing the remote cleans up its remote-tracking refs and
per-branch merge config on its own.

Run `aether sync --live` and left it interrupted? Look for `*.aether-conflict` files
next to your originals: those are your local edits, preserved when a sync
paused. Delete them once you have salvaged what you want.

## Releases

Pushing a `vX.Y.Z` tag runs
[`.github/workflows/release.yml`](../.github/workflows/release.yml): it vets,
runs the unit tests, cross-compiles the full matrix with `make release`, writes
`checksums.txt`, and publishes a GitHub release with generated notes. Those
assets are exactly what the install script and `aether update` download, so
the release workflow, the installer, and `internal/selfupdate` must stay in
step: renaming an asset breaks every `curl | sh` and every deployed binary's
self-update.

If a tag workflow fails after building, rerun the workflow for that tag. The
publisher uploads missing assets to the existing release and replaces
same-named assets without changing its release notes.

The version the binaries report comes from `git describe`, so tags must be
pushed to the repo the workflow checks out, and the checkout uses full history.
