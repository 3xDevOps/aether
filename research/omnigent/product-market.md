# Omnigent vs Aether: product surface, team UX and market position

## Verdict

- **More UX-friendly: Omnigent.** Installed in 6 s and serving locally in 5 s here; one chat
  session follows you across terminal, browser and phone. Aether needs a Linux Docker server,
  a locally built Electron app, three sign-ins and 22 new terms before the first run.
- **Makes more sense: Omnigent** for most users, since laptop, team server and cloud sandbox
  share one model. Aether makes more sense only for teams running unattended agents on their
  own box, and it forces that shape on solo users too.
- **Omnigent overclaims.** Its "hard spend cap" is a downgrade gate, managed sandboxes fail
  under the default deploy auth, about ten features ship flag-off, and it has no team memory
  and no step that integrates multi-agent work.
- **Aether's edges:** a container per run, one team board with takeover and audit, budgets
  that refuse runs, and three things Omnigent lacks: team memory, integrator-verified swarm
  delivery, and subscription pooling with usage fallback.
- **Aether's gaps:** permission prompts off for every agent, cost metered for Background
  Claude only, GitHub only, and no Slack, SSO, gateways, cloud sandboxes, Android push or iOS.
- **Copy:** (1) laptop-first install with prebuilt apps, (2) a policy engine with
  ALLOW/DENY/ASK and approvals anywhere, (3) any-model credentials plus cost for every harness,
  (4) share links and fork-from-message, (5) a Slack bot and OIDC SSO.
- **Drop or replace:** local Electron builds, "skip permissions everywhere", the push-less
  sideloaded APK as the phone answer, and the swarm/mission double naming.
- **Fundable?** Not as a general meta-harness: Omnigent is Databricks-owned, Apache-2.0 and
  merged 4.5x more PRs in the last 30 days. Aether is at most a narrow, milestone-gated bet on
  verified overnight delivery for teams that refuse Databricks and SaaS, and only once it has
  SSO, policies and real cost data.

## Scope and evidence

Snapshot: 2026-10-25. Omnigent `8b46a8a37961616e3e78d7f96c37ed8ccf08f07e`
(PyPI 0.19.1, alpha). Aether `77b4f3bb670f8c4effe124a0e4487fd6c2c54fb6`
(v0.7.2-beta.1, beta). Citations are `omnigent:<path>:<line>` or
`aether:<path>:<line>` at those commits. "(inference)" marks a conclusion
that was not read directly.

## 1. Design intent

**Omnigent** calls itself "the open-source meta-harness for all your AI
agents": one orchestration layer over many vendor CLIs and SDKs. It promises
any device, mixed harnesses in one session, any model, real-time
collaboration, cloud sandboxes and policies (`omnigent:README.md:5-59`).

- The unit is a **session**. A local server stores state, and a **host**
  (your laptop or a sandbox) runs the agent (`omnigent:deploy/README.md:222-240`).
- Agents are YAML files that other agents can write
  (`omnigent:README.md:645-648`).
- Sessions are private until shared: public sharing defaults to Read
  (`/v1/info` on a fresh local server returned `"public_sharing_max_level":"read"`).
- The copyright holder is Databricks (`omnigent:NOTICE:1`). Databricks runs a
  managed "Omnigent on Databricks (Beta)" and recommends it to its customers
  over self-hosting (`omnigent:deploy/README.md:133-139`).

**Aether** calls itself "an open-source multiplayer runtime where engineering
teams run and jointly steer AI coding agents like Claude Code and Codex on
their own servers" (`aether:README.md:5`).

- The unit is a **run**: one agent, one task, one container, one branch
  (`aether:docs/README.md:22`).
- Everyone on a server sees every agent (`aether:README.md:22-25`). Every run
  ends as a branch or a pull request (`aether:README.md:29-32`).
- A solo user is "a team of one" (`aether:docs/teams.md:5-6`).
- The license is GPL-3.0 (`aether:README.md:124`).

The two projects start from opposite ends:

| | Omnigent | Aether |
| --- | --- | --- |
| Starts from | The individual developer | The team's server |
| Adds | Sharing and a server | A solo mode only by being a team of one |
| Isolation | An OS sandbox per agent terminal: `bwrap` on Linux, seatbelt on macOS (`omnigent:README.md:166-171`) | A Docker container per run (`aether:docs/README.md:11`) |
| Default trust | Ask-capable policies | No prompts; the container is the boundary (`aether:docs/harnesses.md:358-360`) |

## 2. Onboarding, tried for real (Omnigent) and as documented (Aether)

### Omnigent, measured on this host

All of this ran with a throwaway `HOME`, `DO_NOT_TRACK=1`, no credentials,
and no sandbox started. Nothing was signed in.

| Step | Command | Result |
| --- | --- | --- |
| Install the core package | `UV_TOOL_DIR=/tmp/omni-tools UV_TOOL_BIN_DIR=/tmp/omni-bin timeout 600 uv tool install omnigent` | 195 packages, 502 MB, **6 s**. Installs `omni` and `omnigent`. `omnigent --version` printed `omnigent 0.19.1 (built 2026-10-21T23:37:47Z)` |
| See the surface | `omnigent --help` | 13 harness launchers (agy, claude, codex, debby, devin, goose, hermes, kimi, kiro, opencode, pi, polly, qwen) and 22 commands, including `usage`, `integration` (Slack), `import`, `sandbox`, `attach`, `upgrade` and `uninstall` |
| First contact | bare `omnigent` in a TTY | Started a background host daemon and a server on `127.0.0.1:6767` in **5 s**, printed `Stop it with: omnigent stop`, and returned |
| First agent | `omnigent run` with no credentials | `! Found no harnesses configured.` and `tmux not found — native Claude/Codex need tmux`, then a picker of 15 harnesses plus "Import from OpenClaw" and "Custom ACP agent" |
| Missing CLI | Select Claude without the `claude` CLI | Offers `Yes — install (curl -fsSL https://claude.ai/install.sh \| bash)` |
| Credential | Select Claude, then **+ Add a credential** | Offers `Anthropic — API key`, `Claude — subscription (Pro/Max)`, `Gateway — custom base URL + key`, `Databricks — workspace`, `AWS Bedrock — API key`. **Stopped here** |

Notes on the run:

- The first `/v1/info` showed `single_user: true` and `accounts_enabled:
  false`, and feature flags `usage_page`, `custom_agents_settings_ui` and
  `harness_install` all `false`.
- The README says bare `omnigent` "picks a model with you and starts a session
  in your terminal" (`omnigent:README.md:268`). The code makes it an alias for
  `omnigent start` on a TTY (`omnigent:omnigent/cli.py:2275-2281`), so the
  README has drifted.
- With a Claude credential, a bare `omnigent run` launches Polly, the bundled
  orchestrator (`omnigent:omnigent/cli.py:9081-9091`).
- `omnigent start --help` prints raw Sphinx `:param` docstrings into the help
  text. This is a small polish gap.
- The default installer, `install_oss.sh`, adds the `databricks` extra and the
  Databricks CLI unless you pass `--no-databricks`
  (`omnigent:README.md:73-75`).
- Telemetry is on by default (`omnigent:README.md:692-697`).
- On Linux the native terminals need `tmux` and `bwrap`
  (`omnigent:README.md:162-171`).

Time to a first agent (inference): about two to five minutes on a machine that
already has `uv`, Node 22 and a logged-in `claude`.

### Aether, from the quickstart

I did not run it: this host has no Docker. Prerequisites:

- a Linux amd64 or arm64 machine with Docker and git;
- a client computer;
- a GitHub account;
- an agent login (`aether:docs/quickstart.md:9-13`).

The quickstart has eight steps (`aether:docs/quickstart.md:3-4`):

1. Run the install script on the server and on the client. The client builds
   the desktop app locally: "downloads Electron and takes a few minutes"
   (`aether:docs/quickstart.md:23-28`), about 100 MB
   (`aether:docs/install.md:220-222`).
2. Answer `sudo aether-server setup`: up to seven prompts, one with no default
   (`aether:docs/quickstart.md:48-56`). Then
   `sudo systemctl daemon-reload && sudo systemctl enable --now aether-server`
   (`aether:docs/quickstart.md:71-72`).
3. Claim the server, which needs a GitHub device-code sign-in at the edge
   (`aether:docs/quickstart.md:96-107`).
4. Create a workspace, which needs a second GitHub device-code flow that
   authorizes "GitHub CLI". Only an admin can do this
   (`aether:docs/quickstart.md:127-149`).
5. Install the agent inside the server container, then sign in to the
   vendor's login in that terminal (`aether:docs/quickstart.md:180-196`).
   Loopback OAuth logins need `aether forward`
   (`aether:docs/harnesses.md:72-82`).
6. Launch. The first run pulls the environment image, so it starts slower
   (`aether:docs/quickstart.md:242`).

Steps 7 and 8 (watch, review and publish) come after the first run.

The glossary defines 22 terms (`aether:docs/README.md:11-32`). Time to a first
agent (inference): 20 to 60 minutes for a developer who already has a Linux
box with Docker. Without one, you need a server first.

**Who wins:** Omnigent, clearly, for first contact. Aether's onboarding wizard
(`aether:docs/quickstart.md:130-133`) polishes a path that is too long to
begin with.

## 3. Feature catalog

Status values: yes, partial, flag-off (ships but disabled by default), no.

### Getting started and access

| Feature | Omnigent | Aether | Better and why |
| --- | --- | --- | --- |
| Install and first agent | Yes. One `curl \| sh` or `uv tool install`; local server in seconds (measured above) | Yes, but heavy: a Linux and Docker server, a locally built Electron app, three sign-ins (`aether:docs/quickstart.md:9-28`) | **Omnigent**: no server needed to start |
| Session handoff across devices | Yes. Terminal, browser and phone show one session (`omnigent:README.md:28-30`). The LAN phone tip conflicts with the default `127.0.0.1` bind (`omnigent:omnigent/host/local_server.py:828-829`, inference) | Yes. All state lives on the server and every client is a view (`aether:docs/README.md:11-13`). Terminals replay on reconnect (`aether:docs/terminal.md:378-383`) | **Tie**. Aether's is sturdier because runs never live on the laptop. Omnigent's is easier to reach |
| Mobile | Responsive web; WebView shells for iOS and Android; no PWA (`omnigent:web/ios/README.md:17-22`, `omnigent:web/android/README.md:3-5`, `omnigent:CHANGELOG.md:2187`) | Sideloaded Android APK with no push (`aether:docs/install.md:675-677`). PWA on a tailnet only. No iOS | **Omnigent** (iOS exists). Neither has push from its native shells |
| Editor integration | VS Code iframe extension, a VSIX that is not on the Marketplace (`omnigent:editors/vscode/README.md:3-12`) | Remote-SSH into a run for VS Code, Cursor and Zed (`aether:docs/terminal.md:208-262`) | **Aether**: you edit the real run checkout |
| SDKs and API | Python client and UI SDK (`omnigent:sdks/README.md:20-93`); OpenAPI spec (`omnigent:openapi.json`) | Internal HTTP/WS API documented as contributor reference only (`aether:docs/local-gateway.md:24-36`); no SDK | **Omnigent** |
| External assistants | No ChatGPT/Claude connector found (inference) | ChatGPT and Claude MCP connectors with scopes (`aether:docs/connectors.md:1-8`, `:84-122`) | **Aether** |
| Deploy targets | Docker compose, Render, Fly, HF Spaces, Modal, Cloudflare, Kubernetes, Databricks Apps (`omnigent:deploy/README.md:117-131`) | One Linux host plus a systemd unit (`aether:docs/install.md:10-13`) | **Omnigent** for breadth. Aether's single binary is simpler to run |
| Branding and white-label | Yes (`omnigent:deploy/README.md:635-668`) | No | **Omnigent** |

### Collaboration and governance

| Feature | Omnigent | Aether | Better and why |
| --- | --- | --- | --- |
| Multi-user accounts | Yes. Off locally, on in Docker deploys; single-use invite links (`omnigent:README.md:538-554`) | Yes. Viewer, collaborator and admin roles; tailnet, edge invite, or one-time code (`aether:docs/teams.md:17-21`, `:151-155`) | **Tie** |
| SSO | OIDC (Google, GitHub, Okta, Microsoft) and header-auth proxies (`omnigent:deploy/README.md:459-463`, `:529-580`) | GitHub at the edge, or a tailnet identity; SSO is listed under "Can wait" (`aether:docs/plans/2026-09-28-edge-access-policies.md:307-310`) | **Omnigent**. Enterprises require SSO |
| Share a session | Read, Edit, Manage and Owner levels plus general access; admins cap public access (`omnigent:web/src/components/PermissionsModal.tsx:59-64`, `omnigent:README.md:565-569`) | Every member sees every run; no share links (`aether:README.md:22-25`) | **Omnigent** for privacy and control; **Aether** for zero-setup team visibility |
| Co-drive | `omnigent attach <id>`: turns run on the owner's machine (`omnigent:README.md:573-579`). Non-owners' terminals are read-only (`omnigent:feature-map/sessions.md:153-154`) | One controller at a time, takeover by a 3 s hold with Accept or Deny and auto-transfer after 7 s, a watcher list, `attach --read-only` (`aether:docs/terminal.md:79-104`) | **Aether**: more rigorous control semantics |
| Fork | Fork a whole session or from a message, optionally to another agent, host or model (`omnigent:feature-map/sessions.md:54-62`) | No. Relaunch within 7 days only (`aether:docs/quickstart.md:321-328`) | **Omnigent** |
| Hand off ownership | No explicit handoff found (inference) | `aether handoff <run> <member>`, with an evidence packet (`aether:docs/teams.md:288-297`) | **Aether** |
| Audit and attribution | Policies evaluate the turn's initiating user (`omnigent:README.md:640-642`) | Workspace timeline of every privileged act, member colours, `Co-authored-by:` per messager (`aether:docs/teams.md:329-347`) | **Aether** |
| Conflict detection | Not found (inference) | Conflict radar across runs (`aether:docs/teams.md:349-353`) | **Aether** |
| Policies and approvals | Allow, deny and ask rules at server, agent and session level; many built-ins; approvals in the UI, the inbox, an `/approve` page and Slack (`omnigent:docs/POLICIES.md:3-21`, `:229-506`) | All agents launch with permission prompts off (`aether:docs/harnesses.md:358-367`). An Enhanced session can switch to an asking mode for three agents (`aether:docs/enhanced-runs.md:27-28`). Shared `aether inbox` (`aether:docs/teams.md:276`) | **Omnigent**, decisively |
| Spend caps | `cost_budget` per session and per user per day, but it is a **downgrade gate, not a hard stop** (`omnigent:docs/POLICIES.md:333`); the README calls it a "hard spend cap" (`omnigent:README.md:623`) | A workspace hard cap with a warning level: refuses new runs, lets running runs finish; counts metered spend only (`aether:docs/teams.md:360-372`) | **Aether's** cap is honest but blind to most spend. **Omnigent's** is mislabelled. Neither is enough for a finance team |
| Cost tracking | Per session with per-model breakdown, and `omnigent usage`. The web Usage page (cost timeline) is flag-off, and usage queries return only the caller's own spend (`omnigent:omnigent/server/routes/usage.py:253-263`) (`omnigent:omnigent/server/feature_flags.py:71-74`) | Insights page per member and team, but "Only Background Claude Code runs report cost and tokens" (`aether:docs/insights.md:88-91`); `aether cost --runs` | **Omnigent**: it meters native harnesses. Aether's numbers are a floor |

### Agents, models and automation

| Feature | Omnigent | Aether | Better and why |
| --- | --- | --- | --- |
| Harness breadth | 15 harnesses in the setup picker, plus OpenClaw and custom ACP (measured above) | 9 agents plus custom ones (`aether:docs/harnesses.md:18-28`) | **Omnigent** |
| Any-model gateway | API key, subscription, gateway base URL (OpenRouter, LiteLLM, Ollama, vLLM, Azure), Databricks, Bedrock, Vertex; `/model` mid-session (`omnigent:README.md:473-500`) | Subscription login or a server-wide API key for five fixed variables (`aether:internal/harness/harness.go:422-647`); no gateway base URL (inference) | **Omnigent** |
| Subscription pooling | Not found (inference) | Share your account with a teammate; usage fallback at 95% quota or on a mid-run limit (`aether:docs/teams.md:379-459`) | **Aether**. Unique, though check vendor terms (§7) |
| Custom agents | YAML with tools, MCP servers and sub-agents; agents write agents (`omnigent:README.md:645-686`). The settings UI is flag-off (`omnigent:feature-map/custom-agents.md:33-34`) | `aether agent add` wraps a CLI (`aether:docs/harnesses.md:435-471`); templates for saved launches (`aether:docs/teams.md:308-327`) | **Omnigent**: a composable agent spec |
| Skills | `SKILL.md` per agent; the `/` composer lists skills (`omnigent:feature-map/composer.md:58-63`) | Upload your `~/.claude` with `aether config push` (`aether:docs/quickstart.md:205-217`) | **Omnigent** for discovery. Aether reuses the vendor's own skills unchanged |
| Team memory | No. Hindsight is a per-agent bank defaulting to Hindsight Cloud (`omnigent:omnigent/tools/builtins/hindsight.py:1-31`). Their PRD says "no cross-session or project-level memory" (`omnigent:designs/PROJECTS_PRD.md:63-65`) | Yes. Workspace notes in SQLite full-text search with provenance, pinned launch brief, server-captured outcomes (`aether:docs/memory.md:9-55`) | **Aether** |
| Swarms and missions | Polly: plan, fan out to sub-agents in worktrees, cross-vendor review; each implementer opens its own PR, "you never merge" (`omnigent:examples/polly/config.yaml:43-46`) | An integrator splits the objective, launches workers, accepts work only with evidence, verifies a combined candidate and delivers one branch or proposal (`aether:docs/teams.md:461-549`) | **Aether**: the integration and verification step is the hard part. **Omnigent** wins on cross-vendor review |
| Automations | RRULE schedules, hourly minimum, REST API, agent-created schedules need approval. Missed fires are not replayed, there is no retry, and multi-replica servers fire each task once per replica (`omnigent:docs/AUTOMATIONS.md:41-60`, `:133-142`). `automations_v2` is flag-off | Templates plus five-field cron in UTC; a missed schedule waits for its next occurrence (`aether:docs/teams.md:308-327`) | **Tie**. Both are simple timers and neither has event triggers |
| Slack or bot | Slack Socket Mode bot: one thread is one session, approvals in the thread (`omnigent:integrations/slack/README.md:3-7`). The `[slack]` extra is not in the release pipeline yet (`omnigent:pyproject.toml:282-284`) | None. The dashboard assistant (`aether:docs/assistant.md:1-13`) and the connectors cover part of this | **Omnigent** |

### Git, execution and operations

| Feature | Omnigent | Aether | Better and why |
| --- | --- | --- | --- |
| PR tracking | Pull Requests tab, tracks PRs the agent opens (`omnigent:docs/git-pull-requests.md:73-76`) | **Publish…** opens a PR; checks and reviews on the run; **Send to agent** on a failing check; **Merge** (`aether:docs/quickstart.md:303-319`) | **Aether**: review-to-merge loop in one place |
| Git providers | GitHub, GitLab via `glab`, Azure DevOps Services, plus a plugin entry point (`omnigent:docs/git-providers.md:25-35`) | GitHub only for publishing; a non-github.com `GH_HOST` is refused (`aether:internal/scheduler/github_oauth.go:47-49`) | **Omnigent** |
| Cloud sandboxes | Modal, Daytona, Blaxel, Islo, E2B, Gensee, CoreWeave, Kubernetes, OpenShell, Boxlite, microsandbox, Databricks; per-session managed hosts. Caveats: fails under the default `accounts` auth, 24 h caps on Modal and E2B (`omnigent:deploy/README.md:330-345`, `:465-470`) | No. One Docker host; admission by free memory and disk (`aether:docs/environments.md:206-214`) | **Omnigent** for elasticity; **Aether** for one known box |
| Isolation | `bwrap` or seatbelt per agent terminal; Windows Job Object without filesystem or network isolation (`omnigent:README.md:166-204`) | A container per run, a persistent home, Docker-in-run through Sysbox, a server-side Chromium per run (`aether:docs/environments.md:136-160`, `aether:docs/quickstart.md:293`) | **Aether** for unattended swarms on a shared host |
| Unattended overnight work | Managed hosts keep a laptop optional (`omnigent:README.md:517-519`). Open resilience gaps: approval card lost after an outage, message lost while the host is unreachable, no offline indicator (`omnigent:docs/network-resilience.md:49-125`) | Server restart reattaches containers. A host reboot interrupts runs for good. A stall is flagged after 10 minutes (`aether:docs/failure-handling.md:17-18`, `:407`) | **Aether** on its own box; **Omnigent** when the laptop must sleep |
| Notifications | OS notifications and a dock badge in the desktop app; no web push, no APNs, no email (`omnigent:web/src/hooks/useIdleNotifications.ts:6-34`, `omnigent:web/ios/README.md:17-22`) | Desktop app only while open (`aether:desktop/notify.js:1-4`); web push only from the tailnet-hosted dashboard (`aether:docs/networking.md:248-256`); no Android push | **Tie**. Both are weak, which is the main gap for overnight swarms |
| Session import, upgrade, uninstall | `omnigent import --harness claude --last 10`; `omni upgrade` drains sessions first; uninstall previews first (`omnigent:README.md:211-262`) | `aether server update` with rollback (`aether:docs/failure-handling.md:19`); no session import | **Omnigent** |

Tally across the four tables: Omnigent wins 16 rows, Aether wins 11, and 8
are ties or split. Aether's wins cluster in team operations: control, audit,
memory, integration, isolation and PR loop. Omnigent's cluster in reach:
install, models, harnesses, providers, SSO, policies and devices.

## 4. Where each product is worse than it claims

**Omnigent**

- "Hard spend cap" (`omnigent:README.md:623`) versus "a downgrade gate, not a
  hard stop" (`omnigent:docs/POLICIES.md:333`).
- Managed sandboxes 403 under the deploy-default `accounts` auth
  (`omnigent:deploy/README.md:465-470`).
- Bare `omnigent` starts a background server (`omnigent:omnigent/cli.py:2275-2281`);
  it does not "pick a model with you" (`omnigent:README.md:268`).
- About ten features ship flag-off, some long past their review-by release.
  `usage_page` and `harness_install` were due for review at 0.11.0, and the
  current release is 0.19.1 (`omnigent:omnigent/server/feature_flags.py:71-80`).
- Polly delivers N pull requests and leaves the merge to a human
  (`omnigent:examples/polly/config.yaml:45-46`). No combined, verified result
  exists (inference).
- The Slack extra is not wired into the release pipeline
  (`omnigent:pyproject.toml:282-284`).
- Policies, budgets, Slack, the mobile shells and sandbox providers have no UI
  test lane (`omnigent:feature-map/README.md:228-229`).

**Aether**

- "Your whole team sees every agent... answer their approvals"
  (`aether:README.md:22-25`). In practice no agent asks by default
  (`aether:docs/harnesses.md:358-360`).
- Budgets and Insights cover one mode of one agent
  (`aether:docs/insights.md:88-91`), so "at least $X" is the honest label.
- By default every collaborator can type into or kill runs that hold your
  home, gh token and signing key (`aether:docs/security.md:24-26`).
- "Proposed does not mean landed" (`aether:docs/teams.md:546-547`). A swarm on
  a mirrored workspace still ends with you pushing a proposal ref.
- Phone access depends on the network path:
  - an edge-only user needs the sideloaded APK (`aether:docs/edge.md:384-390`);
  - the APK has no push (`aether:docs/install.md:675-677`).

## 5. Investor lens: traction, backing, business model

### Traction

All figures are from 2026-10-25. Each was queried as shown, and the
first three rows were re-run by hand.

| Metric | Omnigent | Aether | Query |
| --- | --- | --- | --- |
| Stars, forks, watchers | 11,024, 1,801, 42 | 6, 0, 0 | `gh api repos/<o>/<r>` |
| Created | 2026-06-11 | 2026-08-19 | same |
| Merged PRs, last 30 days | 1,264 | 284 | `search/issues?q=repo:<r>+is:pr+is:merged+merged:2026-09-25..2026-10-25` |
| Merged PRs, last 8 days | 424 | 55 | `…merged:2026-10-18..2026-10-25` |
| Merged PRs, all time | 5,373 | 491 | `…is:pr+is:merged` |
| Open PRs | 1,505 | 6 | `…is:pr+is:open` |
| Issues, open and closed | 897 and 1,686 | 9 and 0 | `…is:issue+is:open` and `is:closed` |
| Contributors | 281. Top 10 hold 72% | 4. Top 2 hold 96% | `repos/<r>/contributors?per_page=100`, 3 pages |
| Releases | 22, v0.2.0 to v0.19.1, a minor release about weekly | 87, v0.1.0-alpha to v0.7.2-beta.1 | `repos/<r>/releases?per_page=100` |
| Distribution | PyPI: 39,359 downloads in the last month (includes mirrors and CI) | `aether-server-linux-*` assets: 239 downloads ever, 2 to 5 per recent release | `pypistats.org/api/packages/omnigent/recent`; release asset counts |
| Community | Discord about 1,694 members. Best Hacker News thread: 15 points, 4 comments | No Discord link in the README (`aether:README.md:1-124`); no Hacker News thread found | Discord invite API; `hn.algolia.com` |

### Reading the numbers

**Stars look like launch publicity, not engagement** (inference). Omnigent
has 42 watchers for 11k stars, and its most-commented open issue has 10
comments (#503).

**Users ask for what Aether already has.** Omnigent's most-reacted asks:

- multi-repo workspaces (#2303), which Aether shipped in #522;
- oh-my-pi support (#6714), which Aether ships;
- per-user identity in shared sessions (#2150);
- a fleet config control plane (#7134);
- mobile push (#8865).

**Bots and agents do a lot of Omnigent's work.**

- `omni-resolve-agent[bot]` authored 1,069 PRs, 321 of them merged. It wrote
  16 of the last 100 merged PRs; dependabot wrote 5.
- "Isaac <no-reply@databricks.com>", apparently an internal Databricks agent
  (inference), co-authors 26% of all commits.
- Aether is just as agent-made: 95 of its last 97 merged PRs say "made with
  the help of Claude Code".
- So throughput per human developer is of the same order (inference).
  Omnigent's lead is headcount: about 280 contributors against Aether's
  four.

### Backing and business model

**Omnigent is a Databricks product, not a startup.**

- Ownership: `NOTICE` reads "Copyright (2026) Databricks, Inc."
  (`omnigent:NOTICE:1`), and the PyPI author is Databricks, Inc.
  (`omnigent:pyproject.toml:13-15`).
- Authorship:
  - 601 of 5,381 commits use an `@databricks.com` author email
    (`git log --format=%ae`, re-run by hand).
  - Matching author names to Databricks addresses that appear in
    `Co-authored-by` trailers raises that to about 75% of all commits and 82%
    of non-bot commits. This is a heuristic.
  - Top GitHub profiles read "Databricks" or "SWE @ Databricks, Agent Platform
    Team".
- Launch: a [Databricks blog post on 2026-06-13](https://www.databricks.com/blog/introducing-omnigent-meta-harness-combine-control-and-share-your-agents)
  by Matei Zaharia et al., then a showing at Data + AI Summit 2026.
- Contributions are taken under a DCO, not a CLA (`omnigent:CONTRIBUTING.md:391-400`).
- Merges are checked against "the Databricks PyPI proxy used for internal
  builds" (`omnigent:CONTRIBUTING.md:138-150`). "Lakebox launcher ships only
  internally" (`omnigent:omnigent/cli_sandbox.py:7`).

**A managed product already exists.** The [Omnigent on Databricks](https://docs.databricks.com/aws/en/omnigent/)
page, fetched today, describes:

- a Beta, run as "a Databricks-operated Omnigent server that integrates with
  your workspace's identity provider";
- Foundation Model APIs, Unity Gateway and Databricks Sandboxes behind it;
- no published price.

The repo calls this "the recommended path" for Databricks users
(`omnigent:deploy/README.md:133-139`). `omnigent setup --internal-beta`
configures "Databricks internal-beta defaults" (from `omnigent setup --help`).

**The business model is the MLflow playbook** (inference).

- A free Apache-2.0 layer becomes the standard.
- Revenue arrives as Databricks consumption: the managed server, Unity Gateway
  for model traffic and cost governance, Foundation Models, Sandboxes,
  Lakebase and MLflow Tracing.
- Telemetry is on by default. Its config host is `config.omnigent-telemetry.io`
  (`omnigent:omnigent/telemetry/client.py:41-43`), which mirrors MLflow's
  telemetry setup.
- The installer's Databricks default (`omnigent:README.md:73-75`) is the
  funnel.
- Omnigent is not investable on its own. Its strategic role is to make the
  orchestration layer free.

**Aether's signals:**

- License and copyright: GPL-3.0, with no CLA or DCO, so copyright stays with
  four contributors. A dual license is still feasible (inference).
- Commercial surface: the project runs the edge relay at `edge.onaether.dev`
  (`aether:docs/edge.md:1-7`). That is the only natural paid surface, and no
  pricing exists.
- Usage data: there is no telemetry (`aether:docs/privacy.md:115-124`). Good
  for privacy, but nothing to show a fund.
- Android: it is not on Play. The APK and AAB in the latest release have 3 and
  2 downloads.
- Desktop: macOS builds get an ad-hoc signature only
  (`aether:desktop/electron-builder.yml:40`).

## 6. Market map

Gathered by a subagent through web search. A row marked "not verified" rests
on press reports only.

| Product | Shape | Multi-harness | Status and traction |
| --- | --- | --- | --- |
| Omnigent | Vendor-backed OSS control plane; local, self-hosted or Databricks-managed | 15 harnesses | Above |
| [GitHub Agent HQ](https://thenewstack.io/github-agent-hq/) | SaaS "mission control" inside GitHub | Copilot, Claude, Codex | Bundled with Copilot Pro+ and Enterprise |
| [Cursor cloud agents](https://cursor.com/blog/self-hosted-cloud-agents) | SaaS; self-hosted workers since 2026-03-31, inference stays with Cursor | Cursor only | n/a |
| [Codex cloud](https://www.getpanto.ai/blog/codex-ai-statistics) | SaaS | Codex only | 4M+ weekly developers (Apr 2026) |
| [Claude Managed Agents](https://itbrief.co.uk/story/anthropic-launches-claude-managed-agents-in-public-beta) | Hosted runtime | Claude only | $0.08 per runtime hour, public beta |
| [Warp Oz](https://warp.dev/newsroom/2026/2/10/warp-launches-oz-the-orchestration-platform-for-cloud-coding-agents) | Cloud orchestration with triggers | Claude Code, Codex, Warp | Launched 2026-02-10 |
| [Devin](https://cryptobriefing.com/cognition-900m-arr-800m-cash-burn/) | SaaS, own agent | No | About $900M ARR reported (not verified) |
| [Factory](https://cryptobriefing.com/factory-200m-raise-5b-valuation/) | Enterprise SaaS, own agent | No | $5B valuation reported (not verified) |
| [Ona, formerly Gitpod](https://ona.com/stories/gitpod-is-now-ona) | Agents plus environments in your VPC | Partial | Commercial |
| OpenHands | OSS agent plus cloud and enterprise | Own agent | MIT, 91.8k stars |
| Conductor, Claude Squad | Local Mac app / local TUI with worktrees | Claude, Codex | Conductor raised a seed, Series A reported (not verified). Claude Squad: AGPL, 8.7k stars |
| [Vibe Kanban](https://vibekanban.com/shutdown), Terragon | Local OSS with a cloud team tier / cloud sandbox SaaS | Yes | **Both shut down**: Vibe Kanban on 2026-04-10, "couldn't find a business model"; Terragon on 2026-01-16 |

Three facts shape where the market is going:

- The two independent products closest to Aether's model both died: Terragon,
  a cloud sandbox per task with PRs out on your own subscription, and Vibe
  Kanban's team tier.
- Model vendors (Claude Managed Agents, Codex cloud) and platforms (GitHub,
  Cursor, Databricks) are absorbing orchestration (inference).
- Omnigent is how Databricks makes the vendor-neutral, self-hostable slice
  free.

## 7. Aether's wedge: is it defensible?

| Candidate wedge | Unique today? | Valuable to buyers? | Will Omnigent subsume it? (inference) |
| --- | --- | --- | --- |
| Self-hosted server | No. Omnigent self-hosts on Docker compose, Kubernetes and five platforms, with OIDC (`omnigent:deploy/README.md:117-131`) | Yes, but Omnigent already serves it | Already has |
| Container per run on one box | Partly. Omnigent has OS sandboxes, cloud sandboxes and a Kubernetes provider. Aether's persistent home, Docker in the run and server Chromium on a single box is simpler (`aether:docs/environments.md:136-160`) | Moderate: it matters for overnight swarms on shared hardware | Unlikely to prioritize single-box simplicity |
| Missions: integrator, evidence, verified candidate, delivery | **Yes**. Polly stops at one PR per implementer (`omnigent:examples/polly/config.yaml:45-46`) | **High**, if it is reliable. Merging N agent PRs is the real overnight cost | Possible, since Polly already plans and reviews. No roadmap evidence found |
| Team memory with provenance | **Yes**. Their PRD says there is none (`omnigent:designs/PROJECTS_PRD.md:63-65`) | Moderate | Easy for them to add with Hindsight: low moat |
| Team-wide board, takeover, audit, conflict radar | Mostly. Omnigent has presence and sharing, not one shared board | High for teams | Likely. #2150 and #7134 point there |
| Subscription sharing and usage fallback | **Yes** (`aether:docs/teams.md:379-459`) | High for small teams on Pro/Max plans | They may avoid it: pooling personal plans may conflict with vendor consumer terms (inference, terms not checked) |
| Edge: no open port, GitHub identity, end-to-end SSH | Partly. Omnigent uses Cloudflare tunnels or Tailscale | Moderate. It is the only obvious paid surface | Low priority for Databricks |
| Android app | No. Omnigent has Android and iOS shells (`omnigent:web/android/README.md:3-5`) | Low: sideloaded, no push | Already has |

**Conclusion.** The defensible core is narrow. A team gives an objective to
its own box overnight and gets back one verified, integrated result, with the
team's memory, audit trail and pooled subscriptions.

The buyer who most wants that (regulated, on-prem, non-Databricks) also
requires SSO, policies, approvals and real cost data. Aether has no SSO or
policies and only partial approvals and cost data; Omnigent ships all four. So the wedge cannot be sold until Aether closes the
governance gap, and closing it means rebuilding features Omnigent gives away
under Apache-2.0.

**As an investor**, I would not fund Aether as a general meta-harness. I would
consider a small, milestone-gated bet only if three things are proven:

1. Measured overnight swarm reliability: tasks delivered, verified and merged
   with no human repair.
2. Three or more design partners in that segment.
3. A licensing plan, either Apache-2.0 or a CLA plus a commercial edition.
   GPL-3.0 with no CLA blocks both enterprise embedding and relicensing.

**As an engineer running overnight swarms**, I would use Aether's container
model and integration pipeline today, and add Omnigent-style policies around
them.

## 8. Copy, replace, drop

### Licensing and porting constraints

- **License.** Apache-2.0 code can be included in a GPL-3.0 project; the
  reverse is not allowed ([FSF license list](https://www.gnu.org/licenses/license-list.html#apache2)).
  Keep Omnigent's `NOTICE` text for anything copied (`omnigent:NOTICE:1-3`).
- **Server code.** Omnigent's core is about 554k lines of Python, not counting
  tests (`find omnigent -name '*.py' -not -path '*/tests/*'`). Aether's is
  about 191k lines of Go (`cmd/` and `internal/`, not counting tests). Server
  logic must therefore be ported, not imported.
- **Web components.** Both dashboards are React with Tailwind 4: Omnigent's
  `web/` uses React 18 and Vite, Aether's uses React 19 and Next 16. Their UI
  components, such as `PermissionsModal` and `ApprovalCard`, can be adapted
  directly.

### Copy these five

1. **A laptop-first start.** `omnigent` reaches a running local server in 5 s,
   with no server to provision. Aether should offer:
   - a single-machine mode, `aether` with a local server on the same Linux
     box, or Docker Desktop on macOS;
   - prebuilt, signed desktop apps.

   Replace Aether's local `aether gui build` Electron build
   (`aether:docs/quickstart.md:26-27`) with downloadable artifacts.
2. **A policy engine.** Replace Aether's "permission prompts off for every
   agent" (`aether:docs/harnesses.md:358-367`) with Omnigent's model: ALLOW,
   DENY or ASK at server, agent and session level, built-ins for tool caps and
   per-session or per-user budgets, and approvals answerable from any surface
   (`omnigent:docs/POLICIES.md:3-21`, `:229-506`). Port the built-in catalog.
   Unlike Omnigent, make the budget a real hard stop.
3. **Any-model credentials and full cost capture.**
   - Credentials: gateway base URL plus key, Bedrock and Vertex, a default per
     agent, and `/model` mid-session (`omnigent:README.md:473-500`).
   - Cost: meter native harnesses the way Omnigent does
     (`omnigent:omnigent/cli.py:7251-7287`). Replace Aether's Background-Claude-only
     cost (`aether:docs/insights.md:88-91`), which makes budgets a floor.
4. **Share links and fork-from-message.**
   - Fork a run's conversation from any message, to another agent or model
     (`omnigent:feature-map/sessions.md:54-62`). Aether can only relaunch.
   - Optional private runs with Read, Edit or Manage share levels
     (`omnigent:web/src/components/PermissionsModal.tsx:59-64`) alongside
     Aether's team-visible default.
5. **A Slack bot and OIDC SSO.**
   - Slack: one thread per run, approvals and questions in the thread
     (`omnigent:integrations/slack/README.md:3-7`).
   - SSO: OIDC or header auth (`omnigent:deploy/README.md:459-463`).

   This is the cheapest way to fix overnight notifications, where both
   products are weak today, and SSO is a hard requirement for team buyers.

Runners-up:

- `omnigent import` of local Claude and Codex sessions;
- `omni upgrade` drain semantics;
- GitLab and Azure DevOps providers through a plugin interface
  (`omnigent:docs/git-providers.md:25-35`);
- a cloud-sandbox overflow target when the box is full.

### Drop or replace

- **Local Electron builds at install.** Replace them with signed, prebuilt
  apps (above).
- **The push-less, sideloaded Android APK as the phone answer.** Serve the
  dashboard through the edge with web push instead. Today the edge serves no
  dashboard (`aether:docs/edge.md:384-390`), and web push exists only on a
  tailnet (`aether:docs/networking.md:248-256`). Keep the APK frozen until
  then.
- **The swarm/mission double naming** ("`aether-internal` calls a swarm a
  mission", `aether:docs/README.md:30`). Also fold environment, home and
  standard image into one user-facing concept. 22 terms before the first run
  is too many.
- **"Everyone can steer everyone's run" as the default** for runs that mount
  your gh token and signing key (`aether:docs/security.md:24-26`). Flip it,
  or adopt Omnigent's explicit share levels.

### Keep and double down

- Integrator-verified delivery, which Omnigent lacks.
- Team memory.
- Container-per-run isolation.
- Takeover and audit.
- Subscription fallback, after a vendor-terms review.

Publish reliability numbers for overnight swarms. That is the claim Omnigent
cannot make today: its own matrix lists lost approvals and lost messages after
an outage (`omnigent:docs/network-resilience.md:49-125`).

## Appendix: commands behind the measurements

```sh
timeout 300 git clone --filter=blob:none https://github.com/omnigent-ai/omnigent /tmp/omnigent
git -C /tmp/omnigent rev-parse HEAD      # 8b46a8a37961616e3e78d7f96c37ed8ccf08f07e
UV_TOOL_DIR=/tmp/omni-tools UV_TOOL_BIN_DIR=/tmp/omni-bin UV_CACHE_DIR=/tmp/omni-uvcache \
  timeout 600 uv tool install omnigent   # 6 s, 502 MB
# all of the following with: env -i HOME=/tmp/omni-homeN DO_NOT_TRACK=1 \
#   OMNIGENT_DISABLE_TELEMETRY=true OMNIGENT_HOST_NO_OPEN=1 OMNIGENT_NO_UPDATE_CHECK=1
omnigent --help; omnigent <setup|start|run|usage|integration|import|sandbox|session|agent|host|diagnose> --help
omnigent                                 # in a pty: starts host + server on 127.0.0.1:6767 in 5 s
curl -s http://127.0.0.1:6767/v1/info    # single_user, feature flags
omnigent run                             # in a pty: harness picker -> credential kinds; stopped there
omnigent setup                           # in a pty: Claude -> + Add a credential; stopped there
omnigent stop                            # "Stopped 1 daemon(s) and the background server."
```
