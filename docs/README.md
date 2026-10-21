# Aether documentation

These guides cover installing, using and extending Aether, a self-hosted server
that runs coding agents for you and your team. If you are new, start with the
[quickstart](quickstart.md).

## Concepts

| Term | Meaning | Guide |
| --- | --- | --- |
| Server | `aether-server`, the Linux program that runs agents in Docker containers and stores workspaces, runs and members' homes. Clients reach it over SSH: on its port (`:2222` by default), over a tailnet, or through an edge. | [install.md](install.md) |
| Client | What you use to reach a server: the `aether` CLI (Linux, macOS, Windows), the desktop app, or the Android app. | [install.md](install.md) |
| Dashboard | The web app for runs, agents, files and settings. The desktop app and `aether gui` serve it from your computer, the Android app shows it on a phone, and the server can host it on a tailnet (`web-port`). | [networking.md](networking.md#the-dashboard) |
| Tailnet | A private network made with Tailscale. On a tailnet the server can sign people in by their Tailscale login and host the dashboard. | [networking.md](networking.md) |
| Edge | A relay with GitHub sign-in that servers and clients both dial out to, so neither needs an open port. The project runs one at `https://edge.onaether.dev`, and you can run your own. | [edge.md](edge.md) |
| Claim link | The link `aether-server setup` prints. Whoever uses it first, in the desktop app or with `aether link --claim`, becomes the server's administrator. | [quickstart.md](quickstart.md#2-start-the-server) |
| Member | A person with an account on a server. Each member has a role: viewer, collaborator (the default) or admin. | [teams.md](teams.md) |
| Administrator | A member with the admin role, who manages members, workspaces and settings. The first person to link a fresh server becomes one. | [teams.md](teams.md) |
| Workspace | One repository and its base branch (the branch new runs start from), plus the runs, notes and history that belong to it. | [teams.md](teams.md) |
| Workspace source and mirror | A workspace imported from GitHub or another Git host has a source: that repository and branch. The server's source mirror fetches it with read-only access and moves the base branch forward. An admin adopts the first revision and any rewrite of its history, and nobody pushes a mirrored base. A workspace without a source is local-only: collaborators push its base branch to the server. | [teams.md](teams.md) |
| Publish destination | The repository runs push their branches to, shown as **Publish changes to** (the code calls it the checkout Origin). It can be the source repository, a fork, or none. | [teams.md](teams.md) |
| Run | One agent working on one task in its own container, with its own checkout of the workspace. A closed run keeps its container for 7 days, so you can reopen it. | [quickstart.md](quickstart.md) |
| Branch and changes | Each run commits to its own branch, `aether/run-<task>-<id>`, in the workspace's repository on the server. The run's **Changes** view shows the diff against the base branch and has **Publish…**. `aether pull` fetches the branch into a local clone. | [quickstart.md](quickstart.md#8-review-and-publish-the-result) |
| Board | The dashboard page that groups runs into **Needs you**, **Working** and **Finished**. | [quickstart.md](quickstart.md#7-watch-it) |
| Environment | Your own container on the server, opened from **Environment** in the dashboard or with `aether terminal`. Your runs start from its image. Agents you install there reach your runs at once, because they live in your home; other tools reach runs only after **Save environment**. | [environments.md](environments.md) |
| Home | Your persistent directory on the server, mounted as `$HOME` in your environment and in every run you launch. Installed agents (`~/.local/bin`), their logins and their configuration live there. | [environment-home.md](environment-home.md) |
| Standard image | The container image an environment starts from until its member saves their own, `ghcr.io/3xdevops/aether-standard`. | [environments.md](environments.md) |
| Agent | A coding CLI a run starts: Claude Code, Codex, OpenCode, pi, oh-my-pi (`omp`), GitHub Copilot CLI, Gemini CLI, Cursor CLI, Antigravity CLI, or one you add. Aether's code calls these harnesses. | [harnesses.md](harnesses.md) |
| Standard, Enhanced and Background mode | How a run shows its agent. Standard is the agent's own terminal. Enhanced drives it over the Agent Client Protocol and shows messages, tool calls and approvals as dashboard controls. Background runs the task once with no interaction. | [enhanced-runs.md](enhanced-runs.md) |
| Swarm | One objective handed to a group of runs. An integrator run splits it into tasks and launches worker runs for them. The code and `aether-internal` call a swarm a mission. | [teams.md](teams.md) |
| Team memory | Short notes per workspace that runs of any agent search and add to. Collaborators and admins pin the notes every new run starts with. | [memory.md](memory.md) |
| Assistant | A chat panel in the dashboard that answers questions and acts for you, such as launching a run. It is your own Claude Code or Codex, running in Enhanced mode in your environment. | [assistant.md](assistant.md) |

```mermaid
flowchart LR
    client["Your computer or phone:<br>CLI, desktop or Android app"] -->|"SSH: direct,<br>tailnet or edge"| server
    git["GitHub or another<br>Git host"] -->|"source mirror"| ws
    subgraph server["Aether server"]
        ws["Workspace repository:<br>base and run branches"]
        env["Your environment:<br>image and home"]
        run["Run container:<br>agent and checkout"]
    end
    ws -->|"checkout on a new branch"| run
    run -->|"run branch"| ws
    env -->|"same image and home"| run
    run -->|"push to the publish destination,<br>open a pull request"| git
```

## Reading order

1. [Quickstart](quickstart.md): install, link, add a repository and finish
   your first run.

Daily use:

2. [Agents](harnesses.md): which agent CLIs work, how to log in to each, and
   what does not work.
3. [Environments](environments.md): install tools, save your image, or reset
   it. [Environment home](environment-home.md) covers what persists in your
   home and how to upload agent configuration.
4. [Terminals](terminal.md): your environment shell, a run's terminal and extra
   shells, and SSH or an editor into a run.
5. [Enhanced runs](enhanced-runs.md): when you run agents in Enhanced mode and
   want to know about permissions, restarts and the session log.
6. [Teams](teams.md): when you invite people, change roles, share agent
   accounts, set budgets or launch swarms.
7. [Networking](networking.md) and [edge](edge.md): when you choose how
   people reach the server, or host the dashboard for phones.
8. [Assistant](assistant.md): when you want to ask Aether for things in plain
   language.
9. [Team memory](memory.md): when runs should share what they learn.
10. [Connectors](connectors.md): when you want to run agents from ChatGPT or
    Claude, including their phone apps.
11. [Insights](insights.md): when you want statistics on runs, time, cost and
    pull requests.

Reference:

12. [Install](install.md): every platform, the desktop and Android apps,
    upgrades and removal.
13. [Security](security.md): what the container boundary protects and what it
    does not.
14. [Failure handling](failure-handling.md): troubleshooting reboots, full
    disks, stalled runs and dropped connections, and the server settings
    that tune them.
15. [Privacy](privacy.md): what the Android app, the dashboard and the
    project's edge store and send.
16. [Notices](notices.md): the licence and what the release artifacts bundle.

## Extend Aether

- [Agent integration](harness-integration.md): connect an agent CLI that
  Aether does not ship to the inbox runs use to message each other.
- [Adapters](adapters.md): add a built-in agent, or an adapter that turns an
  agent's output into timeline events.

## Contributor reference

- [CONTRIBUTING.md](../CONTRIBUTING.md): build, test and release Aether.
- [Testing](testing.md): the test layers and the end-to-end scenario suite.
- [Dashboard frontend](dashboard-frontend.md): how the web client in `web/` is
  built.
- [Styles](styles.md): the dashboard's visual language.
- [Dashboard gateways](local-gateway.md): the HTTP and WebSocket surface that
  `aether gui` and the server's tailnet dashboard serve.
- [Coordination](coordination.md): how overlapping runs and swarm runs warn
  and message each other.
- [Candidate integration](integration.md): how swarm results are verified
  and delivered.
- [MCP bridge](mcp-bridge.md): the coordination and memory tools inside a run's
  container.
- [Agent execution separation](agent-execution-separation.md): a design record
  on splitting an agent's session from where its tools run.
- [Demo recording](media/README.md): how to record the README's demo GIF.
- [plans/](plans/): dated design records. They describe what was planned at the
  time and are not kept up to date as guides.
