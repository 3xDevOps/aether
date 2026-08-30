<div align="center">

<img src="docs/media/aether-mark.png" alt="Aether logo" width="96">

# Aether - Unified Agent Runtime

**A self-hosted development environment for AI coding agents running in the cloud.**

[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-6EE7D6?style=flat-square)](go.mod)
[![License: GPL-3.0](https://img.shields.io/badge/license-GPL--3.0-4A6FA5?style=flat-square)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/3xDevOps/aether?include_prereleases&style=flat-square&color=4A6FA5)](https://github.com/3xDevOps/aether/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/3xDevOps/aether/ci.yml?branch=main&style=flat-square)](https://github.com/3xDevOps/aether/actions/workflows/ci.yml)
![Status: pre-alpha](https://img.shields.io/badge/status-pre--alpha-FF9D6B?style=flat-square)

[Quickstart](docs/quickstart.md) · [Install](docs/install.md) · [Docs](docs/) · [Contributing](CONTRIBUTING.md)

<!-- docs/media/demo.gif does not exist yet. Shot list and recording recipe: docs/media/README.md. -->
![Aether demo](docs/media/demo.gif)

</div>

---

Aether is a **self-hosted** development environment for AI coding agents running in the cloud. It offers agent-agnostic sandboxed environments that can be hosted **anywhere** and be controlled by **anyone** on your team.

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh
aether link my-server
aether run "fix the flaky auth test" --agent claude
```

That is the shape of it, not the full setup - a workspace, a repo push, and
an agent login come first. The [quickstart](docs/quickstart.md) is the real
path. The install script covers Linux and macOS; Windows clients download the
release binary ([install.md](docs/install.md#manual-install)).

[10-minute quickstart →](docs/quickstart.md)

## Why

Laptops are bad hosts for agent fleets. Agents eat CPU, RAM and battery even
when the model is remote, and every workflow dies when the lid closes. The
hosted alternatives fix that by taking your source code and charging per seat.

Aether is the third option: **your hardware, your code, agents that keep working
while you sleep.**

- **Nothing local is running.** Agents run in containers on the server. Close
  the laptop; branches pile up; the sync daemon catches up when you reconnect.
- **Results arrive as git branches.** Every run gets its own worktree and
  branch. You pull, review, and merge - Aether never merges anything itself.
- **Your agents, not generic ones.** Your skills, plugins and custom commands
  are mirrored to the server per member. Logins happen through each vendor's own
  flow and are never extracted or proxied.
- **A team can share one machine.** Several people, one server, everyone's runs
  side by side - see and steer each other's agents in real time, with every act
  attributed.
- **Solo stays frictionless.** Team features are present, never in the way.
  Linking a fresh server makes you the admin; that is the entire account setup.

## Dashboard

`aether gui` serves the dashboard from your own machine: a loopback listener,
a browser tab carrying a per-process token, and your own SSH key as the
identity, so every command the CLI can run works from the page. The desktop
app is the same thing with the window supplied. Nothing is exposed to your
network, and there is no separate login.

Inside: a workspace switcher, a run board bucketed by what needs attention, a
live read-only terminal mirror of any run, per-run diff timelines, the event
feed, the shared approval inbox, presence indicators, the member roster, and a
disk gauge. Launch, inject, pause, kill, close, relaunch and handoff all call
the same methods the CLI does, with the same permission checks and timeline
attribution.

For the raw thing, `aether attach <run>` is a byte-for-byte PTY passthrough -
every native keybind, theme and mouse mode of the agent's own TUI, over SSH.

## Supported agents

Claude Code, Codex, and opencode ship in the harness registry, plus a
deterministic `fake` harness for testing the whole lifecycle without a vendor
account. Adding another is one map entry:
[docs/adapters.md](docs/adapters.md).

Aether does not install agents - the agent CLI lives in your workspace's
container image. See [docs/harnesses.md](docs/harnesses.md).

## Documentation

| Guide | |
| --- | --- |
| [Quickstart](docs/quickstart.md) | Zero to a finished run in ten minutes. |
| [Install](docs/install.md) | The install script, systemd, upgrades, data layout. |
| [Networking](docs/networking.md) | Tailscale-first keyless setup, plus LAN and VPN. |
| [Teams](docs/teams.md) | Joining, roles, workspaces, budgets, attribution. |
| [Harnesses](docs/harnesses.md) | Per-agent login and image requirements. |
| [Adapters](docs/adapters.md) | Adding a harness profile or an output adapter. |
| [Security](docs/security.md) | What the container boundary does and does not do. |
| [Local gateway](docs/local-gateway.md) | The HTTP/WS surface `aether gui` serves. |
| [Dashboard SPA](docs/dashboard-frontend.md) | The web client's structure. |
| [Coordination](docs/coordination.md) | How overlapping runs warn and message each other. |
| [MCP bridge](docs/mcp-bridge.md) | The in-container half of coordination. |
| [Failure handling](docs/failure-handling.md) | Reboots, disk pressure, stalls, dropped connections - and the knobs. |
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

The server targets Linux (amd64/arm64) and only Linux. The CLI is a supported
client on Linux, macOS, and Windows (amd64/arm64 each), and CI builds, vets,
and unit-tests the Windows client on a real Windows runner. Two commands are
deliberately not part of the Windows client: `aether init`, which prepares a
Linux server's data directory, and `aether update`, since Windows cannot
replace a running executable. [CONTRIBUTING.md](CONTRIBUTING.md) has the rest.

## License

[GPL-3.0](LICENSE)
