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

**Upgrading is re-running the installer.** Binaries are replaced in place; the
data directory is untouched. Stop the server first if it is running under
systemd, then `systemctl restart aether-server`.

## Manual install

Every release publishes bare binaries plus `checksums.txt`:

```
aether-server-linux-amd64   aether-server-linux-arm64
aether-linux-amd64          aether-linux-arm64
aether-darwin-amd64         aether-darwin-arm64
aether-windows-amd64.exe    aether-windows-arm64.exe
```

Download the one you want, check it against `checksums.txt`, `chmod +x`, and
drop it on your `PATH` under the name `aether` or `aether-server`. Windows is a
client platform only.

## Building from source

Needs Go 1.25+, GNU make, and Bun 1.3+ (the server embeds the dashboard SPA, so
the web build runs first).

```sh
git clone https://github.com/3xDevOps/Aether
cd Aether
make build      # dashboard SPA, then both binaries into dist/
```

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the rest of the toolchain.

## Server prerequisites

- **Linux.** Windows and macOS are client platforms.
- **Docker**, running, with the server's user able to reach its socket. Every
  run and every `aether setup` session is a container.
- **git** on the host. Bare repos, run checkouts, and diffs are real git.
- **A container image** with the agent CLI you intend to run inside it. Aether
  never installs an agent for you; see [harnesses.md](harnesses.md).
- Optionally **Tailscale**, which is the recommended way to make the SSH port
  reachable and the recommended identity layer. See
  [networking.md](networking.md).

## First boot

```sh
aether init --data-dir /var/lib/aether
```

`init` creates the directory (mode 0700), reports whether Tailscale is present
and what the tailnet hostname is, and prints the serve and link commands. It
starts nothing and writes no configuration - there is no config file.

```sh
aether-server serve --data-dir /var/lib/aether --addr :2222 --dashboard-port 8080
```

Serve flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--data-dir` | `/var/lib/aether` | Everything the server owns. |
| `--addr` | `:2222` | The SSH listener. This is the only port that must be reachable. |
| `--dashboard-port` | `0` (deny) | Loopback dashboard listener that `aether dash` forwards to. |
| `--dashboard-addr` | empty | Additionally expose the dashboard directly on this address. Plain HTTP - see [security.md](security.md). |
| `--tailnet-auto-join` | off | Tailnet identities join approved instead of pending. |
| `--tailnet-require-key` | off | Tailnet connections must also present a registered SSH key. |
| `--conflict-coordination` | on | Let overlapping runs message each other; see [coordination.md](coordination.md). |
| `--stall-threshold` | `10m` | Silence after which a run parks needs-attention; see [failure-handling.md](failure-handling.md). |
| `--poll-interval` | `30s` | How often stalls are checked. |
| `--checkout-ttl` | `72h` | How long a finished run's worktree is kept. Negative disables the GC. |
| `--min-free-disk` | `1GiB` | Free bytes below which new runs are refused. Negative disables the floor. |

Three things happen on the first start and never need attention again:

1. **The SSH host key** is generated into `<data-dir>/ssh/host_ed25519_key`.
   Clients record its fingerprint on first link and print it.
2. **The first identity to link becomes the admin** - the SSH key, or the
   tailnet login, of whoever runs `aether link` first. There is no other
   account creation step.
3. **The SQLite store and the git repo root** are created under the data
   directory.

Do not delete the host key: clients that already trust it will refuse to
connect until you clear the entry from their `known_hosts`.

## Run it under systemd

A ready unit lives at
[`packaging/systemd/aether-server.service`](../packaging/systemd/aether-server.service).

```sh
sudo cp packaging/systemd/aether-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now aether-server
sudo systemctl status aether-server
journalctl -u aether-server -f
```

The unit runs the server as root and creates `/var/lib/aether` through
`StateDirectory=`. Root is deliberate: Docker socket access is already
root-equivalent on the host, and workspace images with a non-root user make the
server chown run checkouts to that UID, which needs `CAP_CHOWN`. The header
comment in the unit spells out how to run unprivileged instead, and what you
give up.

Edit `ExecStart` to change ports or the data directory. API keys for
key-driven harnesses go in `/etc/aether/aether-server.env`, which the unit
loads if it exists:

```sh
ANTHROPIC_API_KEY=<anthropic-api-key>
OPENAI_API_KEY=<openai-api-key>
```

Subscription logins do **not** go there - they live in the per-member
server-side home that `aether setup` writes. See [harnesses.md](harnesses.md).

## The client-side sync daemon

Optional, on your machine, once per repo. It fetches run branches as agents
commit and pushes your base branch up so new runs start from current reality.

```sh
aether daemon install --server <server-host>:2222 --repo ~/code/myproject
systemctl --user daemon-reload && systemctl --user enable --now aether-daemon
```

`daemon install` writes `~/.config/systemd/user/aether-daemon.service` and
prints the activation command. `aether daemon run --server ... --repo ...`
does the same thing in the foreground. The daemon also watches your local
agent-profile directories and pushes changes up; `--no-profile-sync` turns that
half off.

## What lives in the data directory

| Path | Contents |
| --- | --- |
| `aether.db` | SQLite: members, workspaces, sessions, runs, event log. |
| `ssh/` | The server's SSH host key. |
| `repos/` | One bare git repo per workspace. |
| `checkouts/` | Per-run worktrees, garbage-collected after a TTL once a run finishes. |
| `transcripts/` | Per-run PTY recordings (asciicast v2). |
| `homes/<member>/<harness>/` | Per-member harness login state, mounted into that member's runs. |
| `profiles/` | Content-addressed agent-profile snapshots. |
| `invites/` | Outstanding one-time invite codes. |
| `coord/` | Per-run conflict-coordination sockets, recreated each run. |
| `scheduler/`, `runtime/` | Scheduler state and the staged MCP bridge binary. |

Three consequences worth knowing:

- **Back up `aether.db` and `repos/`.** The rest is reconstructible; those two
  are the state and the code.
- **Three of these grow without bound**: `checkouts/` (reclaimed by the TTL
  GC), `transcripts/`, and `aether.db` (the event log). The dashboard's disk
  gauge breaks the data directory down across exactly those three, and new
  runs are refused below `--min-free-disk`. See
  [failure-handling.md](failure-handling.md).
- **Keep the path short.** Per-run coordination sockets live under
  `coord/<run-id>/coord.sock`, and unix socket paths have a hard length limit
  (about 100 characters). A very deep data directory makes the server log
  `coordination unavailable for this run` and fall back to the overlap notice.
  `/var/lib/aether` is nowhere near the limit.

If you run agents you do not trust, put the data directory on a filesystem
mounted `nosuid,nodev` - the reasoning is in [security.md](security.md).

## Releases

Pushing a `vX.Y.Z` tag runs
[`.github/workflows/release.yml`](../.github/workflows/release.yml): it vets,
runs the unit tests, cross-compiles the full matrix with `make release`, writes
`checksums.txt`, and publishes a GitHub release with generated notes. Those
assets are exactly what the install script downloads, so the release workflow
and the installer must stay in step: renaming an asset breaks every
`curl | sh`.

If a tag workflow fails after building, rerun the workflow for that tag. The
publisher uploads missing assets to the existing release and replaces
same-named assets without changing its release notes.

The version the binaries report comes from `git describe`, so tags must be
pushed to the repo the workflow checks out, and the checkout uses full history.
