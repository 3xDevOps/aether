# Agents

Aether does not install or bundle coding agents. You install the agent CLI in
your [environment](environments.md) (your own persistent container shell) and
sign in with the vendor's own login. Runs then launch that CLI inside a
container. This guide lists which agents work, how to sign in to each, and what
does not work. Terms such as run, child run and workspace are in the
[glossary](README.md#concepts).

## Supported agents

Aether launches an agent in one of two modes. **Standard** shows the agent's
own terminal UI. **Enhanced** drives the agent over the Agent Client Protocol
and shows messages, tool calls and permission prompts as dashboard controls
([Enhanced runs](enhanced-runs.md)). A third mode, **Background**, runs the
agent once without input, commits the result and exits.

| `--agent` | CLI | Sign in | Known limits |
| --- | --- | --- | --- |
| `antigravity` | Antigravity CLI (runs as `agy`) | run `agy` | None known |
| `claude` | Claude Code | `claude auth login`, or `/login` inside `claude` | Set up suggests Standard until the Enhanced adapter is installed: it runs on the Claude Agent SDK |
| `codex` | OpenAI Codex CLI | `codex login --device-auth` | Standard runs need Codex 0.131.0 or newer; subagents do not nest in Enhanced |
| `copilot` | GitHub Copilot CLI | `copilot login --device-code` | Enhanced runs cannot take back `--allow-all` |
| `cursor` | Cursor CLI (runs as `agent`) | `NO_OPEN_BROWSER=1 agent login` | A Cursor admin policy can bring back approval prompts |
| `gemini` | Gemini CLI | API key, or `NO_BROWSER=true gemini` | Serves paid API keys and Code Assist only |
| `omp` | oh-my-pi | log in inside `omp` | Sharing an account shares a whole directory |
| `opencode` | OpenCode | `opencode auth login` | Never auto-updated; reports no usage |
| `pi` | pi | `/login` inside `pi` | None known |

What each agent can do:

| `--agent` | Standard status | Standard mail | Enhanced | Mode switch | Background tasks | Set up suggests |
| --- | --- | --- | --- | --- | --- | --- |
| `antigravity` | none | none | none | no | no | Standard |
| `claude` | turn start and end | hooks and idle wake | adapter | yes | yes | Standard |
| `codex` | turn start and end | idle wake and mid-turn steer | adapter | no | no | Enhanced |
| `copilot` | none | hooks | built in | no | no | Enhanced |
| `cursor` | none | hooks you install | built in | no | no | Enhanced |
| `gemini` | none | hooks you install | built in | no | no | Enhanced |
| `omp` | turn start and end | hooks and idle wake | built in | yes | no | Enhanced |
| `opencode` | turn start and end | hooks and idle wake | built in | no | no | Enhanced |
| `pi` | turn start and end | hooks and idle wake | adapter | no | no | Standard |

- **Standard status** is what a Standard run reports of its turns
  ([Status reporting](#status-reporting)). With none, a run that goes quiet for
  `--stall-threshold` parks as **Waiting** with a reason starting `stalled:`
  ([failure-handling.md](failure-handling.md)).
- **Standard mail** is how a Standard run learns that mail, a message from a
  person or another run, is waiting in its inbox: hooks at turn boundaries,
  plus idle wake, which starts a turn in a waiting session
  ([Incoming coordination hooks](#incoming-coordination-hooks)). Enhanced runs
  need neither: the server prompts the idle session
  ([Mail](enhanced-runs.md#mail)). **Mid-turn steer** means mail and your
  messages reach a working session as a steer instead of waiting for its turn
  to end.
- **Enhanced** is built into the agent's CLI, served by a separate
  [adapter](#enhanced-adapters), or missing.
- **Mode switch** moves a running session between Standard and Enhanced
  ([Switching a running agent](enhanced-runs.md#switching-a-running-agent)).
- **Background tasks**: an Enhanced run stays working while the agent's
  background subagents and commands run
  ([Background work](enhanced-runs.md#background-work)).
- **Set up suggests** is the mode **Set up** picks first. The dashboard then
  remembers your choice per agent
  ([Choose Enhanced](enhanced-runs.md#choose-enhanced)), and `aether run`
  defaults to Enhanced where it is installed, else Standard. A Background run of an agent it suggests Enhanced for
  uses the agent's ACP server once it is installed
  ([Background runs](enhanced-runs.md#background-runs)).

Other CLIs work as [custom agents](#custom-and-unlisted-agents).

## Set up an agent

You do this once per agent, per person.

1. On your own computer, install the agent:

   ```sh
   aether agent add <name>
   ```

   On a terminal, this opens your environment's terminal, the same shell
   `aether terminal` opens, types the vendor's install command and leaves you in
   that shell. Without a terminal (an agent's shell, CI), it runs the install
   on the server, waits up to 10 minutes, prints the end of the install log and
   `installed <name>`, then `sign in with: aether agent login <name>`. A failed
   install exits 1 after the log with
   `aether: agent <name>: the install command exited <n>; its output is above`.
   The agent installs into `~/.local/bin`, which persists in your home. Add
   `--enhanced` to install the Enhanced adapter too (see
   [Enhanced adapters](#enhanced-adapters)). In the dashboard, **Set up** on the
   **Agents** page or onboarding's **Agent** step does the same: it lets you
   pick Standard or Enhanced, installs the agent, and opens a terminal with the
   login command typed.

   The environment has no `aether` command (only `aether-internal`), so run
   `aether` commands on your computer, never in this shell.

2. Sign in:

   ```sh
   aether agent login <name>
   ```

   On a terminal, this opens the environment's terminal with the agent's
   sign-in command typed (the **Sign in** column above); finish it there. The
   environment has no browser: open the URL the CLI prints in your own browser.
   In the dashboard, clicking an OAuth URL with a loopback redirect starts the
   callback forward for you. From the CLI, start it yourself in a second
   terminal on your computer before you finish the browser flow:

   ```sh
   aether forward terminal <callback-port>
   ```

   Device-code logins need no forward.

   Without a terminal, only a device-code sign-in, which needs nothing typed
   back, can run. Today that is Codex: `aether agent login codex` runs
   `codex login --device-auth` in a new terminal tab, `login-codex-<id>`,
   prints its output as plain text as it arrives, so the URL and code reach
   you, and waits for it to end. It then prints
   `codex: login file found; aether account usage checks that the vendor accepts it`,
   or exits 1 naming Codex's exit status. Running it again starts a new
   sign-in with a new code. Ctrl-C stops only the CLI; the sign-in runs on in
   its tab until it ends or the code expires. The token stays in your home on
   the server.

   Every other agent's sign-in needs typed input (a pasted code, a picker, a
   y/N), so without a terminal the command exits 1 at once:

   ```text
   aether: claude signs in with claude auth login, which needs typed input, so it needs a terminal. The human runs this in their own terminal:
     aether agent login claude
   An API key stored as a workspace secret works instead: docs/harnesses.md#use-an-api-key
   ```

3. Check the agent:

   ```sh
   aether agent list
   ```

   ```text
   AGENT        SOURCE   INSTALLED  ENHANCED       LOGIN FILE
   claude       shipped  yes        not installed  not found
   codex        shipped  yes        installed      found
   myagent      member   yes        none           -
   ```

   `LOGIN FILE` only looks for the login file in your home; `-` means the agent
   declares none. `--json` prints the agents as `agent.list` returns them
   ([local-gateway.md](local-gateway.md)). The **Agents** page shows the same
   checks as **Installed** and **Login found**. The agent itself verifies the
   login when it starts. For Claude Code and Codex, ask the vendor whether it
   accepts the login:

   ```sh
   aether account usage
   ```

   ```text
   PROVIDER  STATUS           DETAIL
   claude    ok               5 hours: 12% used, 7 days: 40% used
   codex     unauthenticated  Codex OAuth credential not found
   ```

   The statuses are those in [Subscription quota](#subscription-quota).
   `--json` prints the `account.usage` result.

The login and executable live in your home, so every container you launch sees
them. If you skip the login, the agent's own login prompt appears in the run's
terminal. Attach with `aether attach <run>` and finish it there.

Installing `codex`, `pi`, `copilot` or `gemini` needs `npm`. If `npm` is not on
the environment's `PATH`, the install fails with, for example,
`npm is not in this environment's PATH, and codex installs through npm`. The
dashboard's **Set up** and `aether agent add` without a terminal run the
install under `mktemp` and `timeout`, so the image must provide both. Without
them it fails with `timeout: not found` and exit status 127.

### Use an API key

A key stored as a workspace secret replaces the agent's login in that
workspace's runs. The human runs this on their own computer, for example:

```sh
aether workspace env set --secret ANTHROPIC_API_KEY
```

The value is read from stdin; on a terminal the command prompts with input
hidden. Codex ignores `OPENAI_API_KEY` and reads `CODEX_API_KEY` in
Background runs only; for every Codex mode, run `codex login --with-api-key`
in `aether terminal`. Each agent's key is under
[Per-agent setup](#per-agent-setup). Pick another workspace with `--workspace
<name-or-id>`. Never pass a key as `NAME=value`: that puts it on the command
line and stores a plain variable that `aether workspace env list` shows.

> [!WARNING]
> On a team server, anyone who can launch a run in the workspace or message one
> can read the key ([environments.md](environments.md#secrets)).

### Enhanced adapters

Some agents serve ACP from their own CLI (`omp acp`, `opencode acp`,
`copilot --acp`, `gemini --acp`, `agent --force acp`). Installing the agent
installs that. Claude Code, Codex and pi need a separate npm adapter package:
`claude-agent-acp`, `codex-acp` and `pi-acp`. `aether agent add <name>
--enhanced` installs the agent and its adapter, pinned to the version Aether
tests against. The dashboard's **Set up** does the same when you pick Enhanced.

### Per-agent setup

Run each login in your environment's shell, which `aether terminal` opens from
your computer. API keys set in the server's environment
(`/etc/aether/aether-server.env` with the shipped systemd unit) replace the
login. To send an agent through a model gateway such as LiteLLM or OpenRouter,
also set the base-URL variable its section names. A variable Aether does not
pass through, such as Claude Code's `ANTHROPIC_AUTH_TOKEN`, goes in a
[workspace secret](environments.md#secrets).

#### Claude Code

Run `claude auth login`, or start `claude` and use `/login`. Either prints a URL
to open in your own browser and takes a code back. `/status` shows which
credential is active. The login lands in `~/.claude/.credentials.json`.

For an API key, set `ANTHROPIC_API_KEY` in the server's environment, and
`ANTHROPIC_BASE_URL` for a gateway. A `CLAUDE_CODE_OAUTH_TOKEN` from
`claude setup-token` is not passed through, so use the terminal for
subscription logins.

Claude Code refuses `--dangerously-skip-permissions` as root, so every `claude`
run gets `IS_SANDBOX=1`.

#### Codex

Run `codex login --device-auth`. It prints a URL and a one-time code and needs
nothing typed back. Device code login must be on first: in ChatGPT's security
settings for a personal account, or in the workspace permissions for a ChatGPT
workspace ([OpenAI](https://learn.chatgpt.com/docs/auth)). The login lands in
`~/.codex/auth.json`. For an API key, `codex login --with-api-key` reads one
from stdin into the same file, which every run mode uses. `CODEX_API_KEY` in
the server's environment signs in Background runs only. Codex ignores
`OPENAI_API_KEY` there, except that its sign-in screen's **Use an OpenAI API
key** comes filled in with it. Codex reads no base-URL variable; for a gateway,
set `openai_base_url` in `~/.codex/config.toml`.

A Standard Codex run starts `codex app-server` in the run's container and
attaches the Codex terminal to it with `codex --remote`. Aether follows the same
session through that app-server: Codex reports the start and end of each turn,
and mail and your messages from the dashboard or `aether message` arrive as a
new turn when Codex is idle, or as a steer added to the running turn. They show
in the terminal as prompts, under Aether's attribution banner. Press Esc in the
terminal to stop a turn. Aether loads no hooks into Standard Codex, so remove
any copy of its hooks from `~/.codex/hooks.json`: it would announce mail twice.

Standard runs need Codex 0.131.0 or newer. With an older one, the run's terminal
prints:

```text
codex-cli 0.130.0 is too old: Aether attaches Codex's terminal to its app-server, which needs Codex 0.131.0 or newer.
Update it here, then start a new run:
  npm install -g --prefix "$HOME/.local" @openai/codex@latest
```

If the app-server exits before it listens, the terminal prints
`codex app-server exited before listening on /tmp/aether-codex/app-server.sock:`
with its exit status and the end of its log, `/tmp/aether-codex/app-server.log`.
If Aether cannot reach the app-server, the run shows **Needs you** with a reason
starting `codex app-server unavailable:` and the real error.

The app-server ends with the Codex terminal. In the login shell that follows,
`codex resume <session>` continues the session, but without Aether's status
reports and message delivery. The `codex --remote ... resume` command Codex
prints on exit no longer works.

#### pi

Start `pi` and use `/login` to pick a provider. The login lands in
`~/.pi/agent/auth.json`. `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` in the server's
environment is the API-key alternative. pi reads no base-URL variable; for a
gateway, set the provider's `baseUrl` in `~/.pi/agent/models.json`.

#### oh-my-pi (omp)

oh-my-pi is a fork of pi with its own executable and home. The install command
is `curl -fsSL https://omp.sh/install | sh`, which puts `omp` in
`~/.local/bin`. Start `omp` and log in through its own flow. The login lands in
the agent database under `~/.omp/agent/`. `ANTHROPIC_API_KEY` or
`OPENAI_API_KEY` in the server's environment is the API-key alternative, and
`ANTHROPIC_BASE_URL` sends Anthropic requests to a gateway.

`omp` is a shipped name, and a shipped name always wins over a definition you
stored with `aether agent add omp`. To launch your own build, register it under
a name Aether does not ship.

#### OpenCode

Run `opencode auth login` and pick your provider. The login lands in
`~/.local/share/opencode/auth.json`. `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` in
the server's environment is the API-key alternative, with `ANTHROPIC_BASE_URL`
or `OPENAI_BASE_URL` for a gateway. OpenCode settings and plugins live in
`~/.config/opencode`, the root `aether config` and **Files** use. That root does
not follow `XDG_CONFIG_HOME`, `OPENCODE_CONFIG_DIR` or `OPENCODE_CONFIG`.

Aether never updates OpenCode, because an upgrade can cross a major version that
Aether's launch wrapper refuses. Update it yourself in `aether terminal`.

#### GitHub Copilot CLI

Log in with `copilot login --device-code`, open the printed URL in your own
browser and enter the code. A container has no system keychain, so Copilot asks
`Store token in plaintext config file? (y/N)`. Answer `y`; the token lands in
`~/.copilot/config.json`. Copilot also writes that file on its first start,
before any login, so **Login found** only means Copilot has started once.

`COPILOT_GITHUB_TOKEN` in the server's environment is the API-key alternative
and takes precedence over a stored login. It must be a fine-grained personal
access token with the "Copilot Requests" permission. Classic `ghp_` tokens do
not work. For an OpenAI-compatible gateway, set `COPILOT_PROVIDER_BASE_URL` and
`COPILOT_PROVIDER_API_KEY` instead; Copilot then needs no GitHub login.

Runs set `COPILOT_ALLOW_ALL=true`, which trusts the working directory, so
Copilot loads that directory's skills, plugins, MCP servers and hooks. Enhanced
runs start `copilot --acp --allow-all`, and a run cannot take those permissions
back.

#### Gemini CLI

Gemini CLI serves only paid Gemini API keys and Gemini Code Assist Standard or
Enterprise. Google moved Google AI Pro, Ultra and free personal accounts to
Antigravity CLI
([announcement](https://developers.googleblog.com/an-important-update-transitioning-gemini-cli-to-antigravity-cli/)).

With an API key, put `GEMINI_API_KEY=<key>` in `~/.gemini/.env`, or set it in the
server's environment. For a gateway, set `GOOGLE_GEMINI_BASE_URL` the same way
and put `{"security":{"auth":{"selectedType":"gemini-api-key"}}}` in
`~/.gemini/settings.json`; without it a Background run fails with
`Invalid auth method selected.` Gemini reads only the first `.env` it finds
walking up from the run's directory, so a `.env` in the checkout wins over
`~/.gemini/.env`.

With Code Assist, run `NO_BROWSER=true gemini`, choose Sign in with Google, open
the printed URL and paste the code back. Also put `GOOGLE_CLOUD_PROJECT=<project>`
in `~/.gemini/.env`. The login lands in `~/.gemini/oauth_creds.json`.

Runs set `GEMINI_CLI_TRUST_WORKSPACE=true`. Without it, Gemini in a folder it
does not trust silently drops `--approval-mode=yolo` to `default` and ignores
`~/.gemini/.env`. An Enhanced run starts in session mode `yolo`; you can change
the mode in the run.

#### Cursor CLI

Cursor's CLI runs as `agent`. Pick `cursor` at launch.

Log in with `NO_OPEN_BROWSER=1 agent login` and open the printed URL. The login
lands in `~/.config/cursor/auth.json`. `CURSOR_API_KEY` in the server's
environment is the API-key alternative.

Aether passes `--force`, which approves everything. In an Enhanced run, a Cursor
team whose admin enables auto-run controls without "Run Everything" overrides
`--force`, and the agent asks again.

#### Antigravity CLI

Antigravity CLI replaces Gemini CLI for personal Google accounts. Its executable
is `agy`; pick `antigravity` at launch. The install command is
`curl -fsSL https://antigravity.google/cli/install.sh | bash`.

`agy` has no login subcommand. Run `agy`, choose Google OAuth or a Google Cloud
project, open the printed URL and paste the code back. The login lands in
`~/.gemini/antigravity-cli/antigravity-oauth-token`. For an API key, set
`GEMINI_API_KEY` (and `GOOGLE_GEMINI_BASE_URL` for a gateway) and put
`"modelProvider":"gemini"` in `~/.gemini/antigravity-cli/settings.json`. For
Google Cloud application default credentials, set `AGY_ADC_AUTH=true`.

Antigravity has no Enhanced mode, and an Enhanced launch is refused. `agy` has no
ACP command, and Google's separate `antigravity-acp` package cannot finish its
Google sign-in inside a run container.

### Install hooks by hand

Claude Code, Copilot, pi, oh-my-pi and OpenCode load Aether's inbox hooks for
you on a Standard run that has a task. Codex needs none: its app-server hands it
mail. Gemini CLI and Cursor CLI do not load hooks.
Without hooks, mail reaches those agents only when they run
`aether-internal inbox`.

To install hooks for Gemini or Cursor, run these inside a run and merge the
output into the agent's settings, keeping any hooks you already have:

```sh
aether-internal hook file gemini.json    # merge into ~/.gemini/settings.json
aether-internal hook file cursor.json    # merge into ~/.cursor/hooks.json
```

Restart the agent afterwards. In Gemini, `/hooks list` shows the hook, named
`aether-inbox`. Do not install a copy for an agent whose run already loads
hooks. `aether-internal skill` prints `Inbox hooks (<agent>): loaded by this
run's launch; do not install a manual copy.` when the launch loaded them.

### Remove an agent

```sh
aether agent remove <name>
```

For a shipped agent this deletes its links in `~/.local/bin`, so the launcher
stops offering it. For an agent you defined, it deletes the definition and
leaves the executable alone. The dashboard has **More → Remove…** on the agent's
row.

Removal leaves the package files, the login, the agent's settings and your runs'
history. Runs already going keep going. A mode switch, resume, reattach or usage
fallback that needs the removed agent fails until you set it up again, as do
templates that name it. Removal only touches your own home. It
is refused while an install or an update runs in the home. A launch of a removed
agent you defined answers `unknown harness "x"; register it with: aether agent add x`.

### Updates before launch

When a run launches, the server updates the shipped agent in your
`~/.local/bin`, using the agent's own update command or `npm`. It checks on the
first launch per agent since the server started, and then at most every 6 hours
(15 minutes after a failed update). A launch never waits for the update: the
agent starts on the installed version and the update finishes in the
background. A new version shows on the timeline of the run that started it:

```text
updated <agent> from <old> to <new> for the next launch
```

A failed update never stops a launch. The timeline shows the real cause:

```text
could not update claude from 2.1.288 (Claude Code): the updater exited 1: <updater output>
```

Updates skip OpenCode, agents you defined, shipped names an administrator
overrides, agents installed in the image, and an account owner's installation
that a recipient's run borrows. A `codex`, `pi`, `copilot` or `gemini` in
`~/.local/bin` that `npm` did not install is not updated and reports, for
example, `codex in ~/.local/bin was not installed with npm, so Aether cannot
update it`.

Aether does not roll an update back. To turn updates off for the whole server,
run `sudo aether-server config set agent-update false`, then restart it.

## Usage limits

### Subscription quota

The dashboard shows how much of a subscription is used for Claude Code and
Codex. The server reads the native login in your home and asks the vendor's
usage endpoint. Credentials and the vendor's responses never reach the browser
or a run. Only native subscription logins count: API keys, pi, oh-my-pi,
OpenCode, Copilot, Gemini, Cursor and Antigravity have no quota display. A row
can read `unauthenticated`, `unsupported`, `unavailable`, `stale` or `error`.
Missing data is never shown as zero. Re-authenticating is up to you, in your
environment. Aether does not refresh OAuth files.

Results stay cached for 60 seconds. The vendor endpoints are not public APIs and
can change without notice.

### Usage-limit signals

A run moves to another account mid-run only when its agent reports that the
account's subscription usage limit stopped it
([usage fallback](teams.md#usage-fallback); [shared accounts](teams.md#agent-accounts)).
An account counts as exhausted when a window the run draws on is at 95% used or
more: the five-hour and seven-day windows for Claude Code, the primary and
secondary windows for Codex. A quota row that is not `ok` means unknown usage,
not exhausted.

| Agent | Moves to another account | Does not |
| --- | --- | --- |
| `claude` | Enhanced and Background runs; Standard runs while lifecycle reporting is on | Transient 429s ("Server is temporarily limiting requests (not your usage limit)") |
| `codex` | Enhanced runs, and Background runs over ACP | Background `codex exec`, and Standard runs: Aether reads neither |
| `opencode` | Nothing qualifies: Aether cannot read OpenCode usage, so the run stops with the no-fallback reason | Everything else |
| `pi`, `omp`, `copilot`, `gemini`, `cursor`, `antigravity` | Never | All usage-limit errors |

If a vendor changes its signal, the run fails or ends its turn with the vendor's
error instead of moving.

## Launching

### Modes and what the run container does

`aether run` takes `--mode enhanced`, `standard` or `background`. Without
`--mode`, the server starts Enhanced where the agent's Enhanced mode is
installed on the launch's account, and Standard otherwise.

- Standard runs the agent's terminal UI in a persistent PTY. `aether attach
  <run>` puts you in it. After the agent exits, the container opens a login
  shell, and the run stays open until you Close, Kill or Delete it.
- Enhanced runs the agent over ACP next to a login shell. Close and reopen work
  like Standard.
- Background runs the agent once. On exit Aether commits and publishes the
  branch, records `completed` or `failed`, and destroys the container. It cannot
  be reopened. `codex`, `omp`, `opencode`, `copilot`, `gemini` and `cursor` run
  the task over ACP when their server is installed
  ([Background runs](enhanced-runs.md#background-runs)). Claude Code keeps
  `claude -p`.

In all modes the agent runs with its permission prompts turned off. The
container is the boundary ([security.md](security.md)). Standard and Background
launches pass these flags:

| `--agent` | Standard | Background |
| --- | --- | --- |
| `claude` | `claude --dangerously-skip-permissions {task}` | `claude -p --output-format stream-json --verbose --dangerously-skip-permissions {task}` |
| `codex` | `codex --dangerously-bypass-approvals-and-sandbox {task}` | `codex exec --json --dangerously-bypass-approvals-and-sandbox {task}` |
| `pi` | `pi {task}` | `pi -p {task}` |
| `omp` | `omp --auto-approve {task}` | `omp -p --auto-approve {task}` |
| `opencode` | `opencode --prompt={task}` | `opencode run {task}` |
| `copilot` | `copilot --allow-all --interactive={task}` | `copilot --allow-all --prompt={task}` |
| `gemini` | `gemini --approval-mode=yolo --prompt-interactive={task}` | `gemini --approval-mode=yolo --prompt={task}` |
| `cursor` | `agent --force --trust {task}` | `agent -p --force --trust {task}` |
| `antigravity` | `agy --dangerously-skip-permissions --prompt-interactive={task}` | `agy --dangerously-skip-permissions --print={task}` |

A Standard Codex run attaches its command to `codex app-server`
([Codex](#codex)).

OpenCode has no such flag, so every OpenCode launch gets
`{"permission":"allow"}` in `OPENCODE_CONFIG_CONTENT`. Your workspace's own
value of that variable is merged over it key by key, so a workspace that sets
`permission` keeps its rules.

The task is optional in Standard and Enhanced mode. Launch without one and you
land in the agent's terminal with an empty prompt, or an Enhanced run waits for
your first message. Background mode requires a task.

### Choose a model

`aether run --model <model>` or the **Model** field in the dashboard passes the
model to the agent as is. Empty keeps the agent's default. Every shipped agent
takes a model. For Enhanced runs the model is set over ACP after the session
opens.

A custom agent with no model flag refuses a Standard or Background launch with
a model:

```text
scheduler: agent "custom" has no model flag; launch it without a model
```

A model that is not a single argument is refused before launch:

```text
model "x y" must be a single argument: no whitespace or control characters, not starting with "-", at most 128 bytes
```

The dashboard offers a model list for Claude Code and Codex, read from the
installed CLI on your login. Falling back, Claude Code lists `opus`, `sonnet`
and `haiku`; Codex lists models from `~/.codex/models_cache.json`. For other
agents, type the model name. The list is cached for an hour. On a shared
account, discovery runs in the account owner's environment and never starts it.
If the owner's terminal is not running, the result is the fallback with an error
saying so. A failed discovery shows the CLI's error, for example
`codex exited 127: sh: 3: codex: not found`.

### Choose an effort

`aether run --effort <level>` or the **Effort** field in the dashboard sets the
agent's effort or thinking level for the run. Empty keeps the agent's default.
The dashboard shows the field only for an agent that has levels, and offers
only those:

| Agent | Flag | Levels | Enhanced |
| --- | --- | --- | --- |
| Claude Code | `--effort <level>` | `low`, `medium`, `high`, `xhigh`, `max` | `effort` session option |
| Codex | `-c model_reasoning_effort=<level>` | `low`, `medium`, `high`, `xhigh`, `max`, `ultra` | `reasoning_effort` session option |
| pi | `--thinking <level>` | `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` | `thought_level` session option |
| oh-my-pi | `--thinking=<level>` | pi's, plus `auto` | `thinking` session option |
| GitHub Copilot CLI | `--reasoning-effort <level>` | `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` | the same flag on `copilot --acp` |

OpenCode, Gemini CLI, Cursor CLI and Antigravity CLI have no effort setting.
Not every Codex model takes every level; `codex debug models` lists each
model's `supported_reasoning_levels`, and Codex reports a level its model
refuses. A level the agent does not list is refused before launch:

```text
scheduler: agent "codex" takes effort low, medium, high, xhigh, max, ultra, not "minimal"
```

An agent with no effort flag refuses a Standard or Background launch with an
effort:

```text
scheduler: agent "gemini" has no effort flag; launch it without an effort
```

For Enhanced runs the effort is set over ACP after the model; see
[enhanced-runs.md](enhanced-runs.md#launch-an-enhanced-run).

If a launch fails because the CLI rejects its own arguments, the installed CLI
has drifted from what Aether expects. Install a compatible version in your
environment.

### Agent instructions and the team memory brief

Aether gives each run its role card and the co-author rule
([agent instructions](coordination.md#agent-instructions)), followed by the
[team memory brief](memory.md#what-runs-see-automatically). How the text
reaches the agent depends on the agent:

| Agent | Delivery |
| --- | --- |
| `claude`, `pi`, `omp` | `--append-system-prompt` |
| `codex` | `-c developer_instructions=...`, passed to `codex app-server`, which replaces any `developer_instructions` in your `config.toml`. Aether copies the top-level value of `~/.codex/config.toml` in first. A value in a Codex profile, a project `.codex/config.toml` or a workspace `CODEX_HOME` is replaced for that launch: put such instructions in `AGENTS.md` |
| `opencode`, `copilot` | An instructions file the agent loads through its config or `COPILOT_CUSTOM_INSTRUCTIONS_DIRS` |
| `gemini`, `cursor`, `antigravity` | After the task prompt. A launch with no task gets none |
| Enhanced runs | Over ACP ([Enhanced runs](enhanced-runs.md#instructions-and-the-session-log)) |

System-prompt delivery survives compaction. Instructions after the task do not.
A launch with no instructions has no brief either. In that case the agent has
to run `aether-internal skill` and `aether-internal memory brief` itself.

### Custom and unlisted agents

Any CLI can be a run's agent. Register one for yourself:

```sh
aether agent add mycli --standard "mycli {task}" --background "mycli -p {task}"
```

Without flags the command prompts for `Standard command [<name> {task}]:` and
`Background command [<name> -p {task}]:`. `{task}` becomes one argument, never
passed through a shell. Then install the executable into `~/.local/bin` and sign
in.

| Flag | What it does |
| --- | --- |
| `--standard <argv>` | The Standard-mode command |
| `--background <argv>` | The Background-mode command |
| `--acp <argv>` | The command that serves ACP. Without it the agent has no Enhanced mode |
| `--acp-mode <id>` | The session mode a new Enhanced run starts in. Pick one that acts without asking, or the run waits on the prompt. Needs `--acp`, no whitespace |
| `--login <path>` | A login file, such as `--login ~/.cursor/auth.json`. With it the Agents page shows **Login found**. Without it setup never shows the agent as logged in |

The name is what you choose at launch, and it need not match the executable.
`aether agent add mycursor --standard "agent {task}"` runs `agent`. Aether ships
inbox hooks for a fixed set of names, so a differently named agent gets none and
`aether-internal skill` says `no packaged integration`.

Your definition shapes launches only in your own containers, and runs only on
your own account. Shipped names and the reserved names `custom` and `fake` cannot
be registered. An administrator can pin a definition for every member with
`--agent-definitions` (or `AETHER_AGENT_DEFINITIONS`), which wins over member
registrations and is the only way to change a shipped agent. An override
replaces the shipped definition, so Aether stops passing its status reporter,
inbox hooks and instructions flags to it, because nothing checks that the
override is still that CLI.

For inbox support, status reporting and the contract the CLI has to follow, see
[Integrating an unsupported agent](harness-integration.md).

#### The `fake` agent

`fake` has no login and no fixed command. The server reads its command from
`AETHER_FAKE_AGENT` at launch, so it runs whatever script you name. The run's
checkout is mounted at `/workspace`:

```sh
sudo AETHER_FAKE_AGENT="sh /workspace/agent.sh {task}" aether-server serve --data-dir /var/lib/aether
```

The [quickstart](quickstart.md#prove-the-plumbing-without-an-agent-subscription)
uses it to prove the whole lifecycle without a vendor account.

## Incoming coordination hooks

When agents run child runs or share files, other runs and people send them
messages. Aether keeps those in a durable mailbox and tells the agent to read it.
The reading itself is a command, `aether-internal inbox`. The agent learns mail
is waiting in one of two ways. A **hook** runs at a boundary such as a prompt or
a finished tool call. **Idle wake** starts a new turn in a session that has been
waiting. Which agents get which is in the table above. Both are managed for you,
except where [Install hooks by hand](#install-hooks-by-hand) says otherwise.

Hooks work only for a coordinated run that has a task, in Standard mode. A
taskless launch, a Background run, a run with `--conflict-coordination=false`,
and an agent whose command an administrator overrode do not get them. Enhanced
runs do not use hooks: the server prompts the idle session instead. So does a
Standard Codex run, through its app-server, with or without a task: the server
prompts the session with the inbox notice, as a new turn when idle and as a
steer mid-turn. Without any of these, the agent reads mail only when it runs
`aether-internal inbox`.

Claude Code is the one with a hook-based idle wake in Standard mode. Its wake
starts a turn with no human input, and the terminal shows **Stop hook feedback**.
That wording is Claude Code's own framing, not an error. After you press Esc, a
wake pauses until a turn completes. Copilot has no idle wake, so mail waits for
the next hook boundary. pi, oh-my-pi and OpenCode wake only their main
session, and only while idle.

Hooks that you installed yourself and Aether's managed ones do not double up for
Claude Code. If an OpenCode, pi or oh-my-pi setting disables extensions, Aether
leaves it alone and skips the managed hooks. For OpenCode, `OPENCODE_PURE=1` or
`true` in the workspace environment disables Aether's plugins too. The run still
launches but reports nothing, and is judged on silence.

## Status reporting

An agent with status reporting tells Aether when it is working and when it is
waiting. The dashboard shows a waiting run as **Needs you**
([failure-handling.md](failure-handling.md#needs-you)). Standard runs of
`claude`, `codex`, `pi`, `omp` and `opencode` report both ends of a turn.
While Codex asks you a question in its terminal, the run waits with the reason
`waiting for your answer in the terminal`. Copilot, Gemini, Cursor and
Antigravity have none.

Enhanced and Background-over-ACP runs need no reporter: the session reports both
ends of each turn. Background runs without ACP are never reported.

A Standard Claude Code turn that ends without an outcome report is held once:
its Stop hook blocks the stop and Claude Code shows **Stop hook feedback**
asking the agent to report
([coordination.md](coordination.md#lifecycle-status-is-separate)). Enhanced
runs get a [report reminder](enhanced-runs.md#report-reminder) prompt
instead. Standard runs of other agents are not held: Codex runs load no
hooks, and the app-server reports an idle thread the same way after a
finished turn and after you press Esc.

## Agent configuration: import and Files

`aether config push` uploads an agent's configuration directory from your
computer to your home on the server, and **Files → Home** edits it there
([environment-home.md](environment-home.md#upload-and-edit-configuration)).
Existing and future runs see the change at once. A shared account does not
expose your configuration, except the `~/.omp/agent` directory an `omp` share
mounts.

## Limits

- Antigravity has no Enhanced mode.
- Only Claude Code and oh-my-pi can switch a running session between Standard
  and Enhanced ([Switching a running agent](enhanced-runs.md#switching-a-running-agent)).
- Codex subagents do not nest in the Enhanced timeline.
- No idle wake exists for Copilot, Gemini or Cursor, and Antigravity has no
  hooks at all. A [child run](teams.md#child-runs) refuses them Standard, so they run
  Enhanced there.
- Standard Codex needs Codex 0.131.0 or newer. Its app-server ends with its
  terminal, so a `codex resume` in the shell runs without Aether's status and
  message delivery.
- Only Claude Code and Codex show subscription usage and can move to another
  account on a usage limit.
- Sharing an `omp` account shares the whole `~/.omp/agent` directory. oh-my-pi
  loads extensions and MCP commands from there, so a recipient's run can plant
  code that runs in the owner's sessions. Share an `omp` account only with
  someone you would give your home to ([security.md](security.md#account-sharing)).
- Aether does not update OpenCode, and does not roll back any update.
- The Environment has no browser, so every OAuth login needs your own browser.

Next: [Environments](environments.md).
