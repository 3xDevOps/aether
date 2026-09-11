<div align="center">

<img src="docs/media/aether-mark.png" alt="Aether logo" width="96">

# Aether - Multiplayer Cloud Agent Runtime

**A self-hosted development environment for AI coding agents running in the cloud, for teams & *multiplayer* control.**

[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-6EE7D6?style=flat-square)](go.mod)
[![License: GPL-3.0](https://img.shields.io/badge/license-GPL--3.0-4A6FA5?style=flat-square)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/3xDevOps/aether?include_prereleases&style=flat-square&color=4A6FA5)](https://github.com/3xDevOps/aether/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/3xDevOps/aether/ci.yml?branch=main&style=flat-square)](https://github.com/3xDevOps/aether/actions/workflows/ci.yml)
![Status: alpha](https://img.shields.io/badge/status-alpha-FF9D6B?style=flat-square)

[Quickstart](docs/quickstart.md) · [Install](docs/install.md) · [Docs](docs/) · [Contributing](CONTRIBUTING.md)

<!-- Goes live once docs/media/demo.gif exists. Shot list and recording recipe: docs/media/README.md.
![Aether demo](docs/media/demo.gif)
-->

</div>

---

Aether is a **self-hosted** development environment for long-running AI coding agents in the cloud. It offers agent-agnostic sandboxed environments that can be hosted **anywhere** and be controlled by **anyone** on your team.  
</br>
Leaving your laptop open for your agents is no longer needed. 

# Installation 

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh
```
Aether installs in two places: a **desktop client** & CLI for your laptop/PC, and a **server framework** for where you intend to run your agents - a home server, PC, or an old laptop repurposed to be on all day long. 

The server framework will run on any Linux device. The install script covers Linux and macOS clients, while Windows clients can download the release binary instead. The install script handles installations on both sides and asks whether the current machine is your server or a client. 

**Next, see [docs/quickstart.md](docs/quickstart.md) to set up your first workspace and link your codebase, tools, and CLI agents.** 

For manual installation or uninstallation, see [install.md](docs/install.md#manual-install).

## Well, why is this needed? 

Laptops are bad hosts for agent fleets. Agents eat a lot of resources even
when the model is served from remote, and every workflow dies or freezes when you close the laptop lid. 

Alternatively, you could also pay money in addition to your (already massive) inference bill by using commercialized hosting services to keep your agents running. They take your source code and charge you on a per-seat basis.

Aether is the third option: **your hardware, your code, agents that keep working
while you sleep.**

- **Multiplayer by design.** Several people, one server. **See and steer each other's agents in real time, with every act
  attributed.**
- **Agents run continuously in remote containers.** Agents run in containers on the server. Launch them once, close your laptop laptop and they still continue to work. 
- **Results arrive as git branches.** Every run gets its own worktree and
  branch. You can pull, review, and merge - or set your agent up with Git & Github on Aether
  to let them handle Git operations autonomously. Run-branch pulls remain
  available whether a workspace is local-only or mirror-backed.
- **Your agents, your setup.** Your skills, plugins and custom commands
  are mirrored to the server on a per-user basis. Logins stay on the remote that you own, through each vendor's own
  authentication flow, and are never extracted or proxied.
- **Full support for solo developers.** Team features are present, never in the way.
  Linking a fresh server makes you its administrator, giving you full control for your solo workflow. 

## Dashboard

The desktop app builds on top of the CLI and serves the dashboard and control entrypoint from your own client machine. 

Inside: a workspace switcher, a board bucketed by what needs attention, a
live read-only terminal mirror of any run, per-run diff timelines, the event
feed, the shared approval inbox, presence indicators, the member roster, and a
disk gauge. Launch, inject, pause, kill, close, relaunch and handoff all call
the same methods the CLI does, with the same permission checks and timeline
attribution.
Launch freshness is server-owned: before a run row exists, the server captures
the workspace base. A configured mirror refreshes that base; a local-only
workspace reads its local base. A failed pre-run check creates no run row.
Workspace pages expose admin-only Source control for mirror status and
controls; launch freshness is handled on the server rather than by a client
pre-launch operation.

## Supported agents

Aether does not install agents - install the agent CLI in your member
environment terminal. See [docs/harnesses.md](docs/harnesses.md) and
[docs/environments.md](docs/environments.md).

## Documentation

| Guide | |
| --- | --- |
| [Quickstart](docs/quickstart.md) | Zero to a finished run in ten minutes. |
| [Install](docs/install.md) | The install script, systemd, upgrades, data layout. |
| [Environments](docs/environments.md) | Member images, saving, resetting, and persistence. |
| [Networking](docs/networking.md) | Tailscale-first keyless setup, plus LAN and VPN. |
| [Teams](docs/teams.md) | Joining, roles, workspaces, budgets, attribution. |
| [Harnesses](docs/harnesses.md) | Per-agent login, profile sync, and launch requirements. |
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

Requires Go 1.25+, GNU make, Bun 1.3+, and Node.js 22+. Bun installs the web
dependencies and drives the scripts; Node.js runs the Next build and
development server.

```sh
make build            # static dashboard export, then both binaries into dist/
make test             # unit tests, race detector on
make test-integration # integration tests; needs real Docker and git
make release          # cross-compile the full release matrix
```

The production web build is a Next static export into `web/dist`, which Go
embeds through `web/embed.go`. Running the installed server or CLI needs no
Node.js and no Next server. For dashboard development, run
`cd web && bun run dev` with Node.js 22+ available on `PATH`.

The server targets Linux (amd64/arm64) and only Linux. The CLI is a supported
client on Linux, macOS, and Windows (amd64/arm64 each), and CI builds, vets,
and unit-tests the Windows client on a real Windows runner. Two commands are
deliberately not part of the Windows client: `aether init`, which prepares a
Linux server's data directory, and `aether update`, since Windows cannot
replace a running executable. [CONTRIBUTING.md](CONTRIBUTING.md) has the rest.

## License

[GPL-3.0](LICENSE)
