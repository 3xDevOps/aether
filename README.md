# Aether

Aether is a self-hosted runtime for AI coding agents. Every agent runs in a
sandboxed container on hardware you own, and anyone on your team can watch or
steer what it is doing.

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh
aether link my-server
aether run "fix the flaky auth test" --agent claude
```

The install script covers Linux and macOS. Windows clients download the release
binary instead ([install.md](docs/install.md#manual-install)).

**Status:** alpha, under active development.
[10-minute quickstart →](docs/quickstart.md)

<!-- A demo recording belongs here once one exists. Shot list and recipe:
     docs/media/README.md. Replace this comment with:
     ![Aether demo](docs/media/demo.gif) -->

## Why

Laptops are bad hosts for agent fleets. Agents eat CPU, RAM and battery even
when the model is remote, and every workflow dies when the lid closes. The
hosted alternatives solve that by taking your source code onto their own
infrastructure and charging per seat for it.

Aether is the third option: your hardware, your code, and agents that keep
working while you sleep.

- **Nothing local is running.** Agents run in containers on the server, so you
  can close the laptop and let the branches pile up while the sync daemon
  catches up when you reconnect.
- **Results arrive as git branches.** Every run gets its own worktree and
  branch for you to pull, review and merge. Aether never merges anything
  itself.
- **Your agents, not generic ones.** Your skills, plugins and custom commands
  mirror to the server per member, and vendor logins go through each vendor's
  own flow rather than being extracted or proxied.
- **A team can share one machine.** Several people run on one server with their
  work side by side, and anyone can watch or steer another member's agent in
  real time, with every act attributed.
- **Solo needs no setup.** Nothing here assumes a team. Linking a fresh server
  makes you its admin, and that is the entire account setup.

## Dashboard

`aether dash` opens an SSH port-forward and a browser tab carrying a token
minted over that SSH connection. There is no separate login, and by default no
HTTP port is exposed to your network at all.

Inside are a sidebar of sessions and their runs, colored by member and
groupable; a run board bucketed by what needs attention; a live terminal mirror
of any run, read-only unless you hold the steer capability; a per-run diff
timeline; the session event feed; the shared approval inbox; presence and
watcher indicators; and a disk gauge. Launch, inject, pause, kill, close,
relaunch and handoff all call the same methods the CLI does, with the same
permission checks and the same timeline attribution.

`aether gui` serves that same dashboard from your own machine over your own SSH
key, which reaches the full method map rather than the remote gateway's
allowlist. See [local-gateway.md](docs/local-gateway.md).

For the raw thing, `aether attach <run>` is a byte-for-byte PTY passthrough,
carrying every native keybind, theme and mouse mode of the agent's own TUI over
SSH.

## Supported agents

Claude Code, Codex, Aider and opencode ship in the harness registry, alongside
a deterministic `fake` harness for driving the whole lifecycle without a vendor
account and a `custom` slot for whatever a deployment supplies. Adding another
is one map entry: [docs/adapters.md](docs/adapters.md).

Aether installs no agents itself. The agent CLI arrives with your workspace's
container image, or through a bootstrap shell where you install it yourself.
See [docs/harnesses.md](docs/harnesses.md).

## Documentation

| Guide | |
| --- | --- |
| [Quickstart](docs/quickstart.md) | Zero to a finished run in ten minutes. |
| [Install](docs/install.md) | The install script, systemd, upgrades, data layout. |
| [Networking](docs/networking.md) | Tailscale-first keyless setup, plus LAN and VPN. |
| [Teams](docs/teams.md) | Joining, roles, sessions, budgets, attribution. |
| [Bootstrap](docs/bootstrap.md) | Workspace environments, tool snapshots, recovery. |
| [Harnesses](docs/harnesses.md) | Per-agent login and image requirements. |
| [Adapters](docs/adapters.md) | Adding a harness profile or an output adapter. |
| [Security](docs/security.md) | What the container boundary does and does not do. |
| [Dashboard API](docs/dashboard-api.md) | The HTTP/WS surface and its token model. |
| [Dashboard SPA](docs/dashboard-frontend.md) | The web client's structure. |
| [Local gateway](docs/local-gateway.md) | Serving the dashboard from your own machine. |
| [Coordination](docs/coordination.md) | How overlapping runs warn and message each other. |
| [MCP bridge](docs/mcp-bridge.md) | The in-container half of coordination. |
| [Failure handling](docs/failure-handling.md) | Reboots, disk pressure, stalls, dropped connections, and the knobs for each. |
| [Testing](docs/testing.md) | The E2E scenario suite and its failure-table coverage. |
| [Contributing](CONTRIBUTING.md) | Build, test, and change the thing. |

## Building from source

Requires Go 1.25+, GNU make, and Bun 1.3+ (the server embeds the dashboard SPA,
so the web build runs first).

```sh
make build            # dashboard SPA, then both binaries into dist/
make test             # unit tests, race detector on
make test-integration # integration tests; needs real Docker and git
make release          # cross-compile the full release matrix
```

The server targets Linux and only Linux, on amd64 and arm64. The CLI is a
supported client on Linux, macOS and Windows, amd64 and arm64 each, and CI
builds, vets and unit-tests the Windows client on a real Windows runner. Two
commands are deliberately absent from the Windows client: `aether init`, which
prepares a Linux server's data directory, and `aether update`, because Windows
cannot replace a running executable. [CONTRIBUTING.md](CONTRIBUTING.md) has the
rest.

## License

[GPL-3.0](LICENSE)
