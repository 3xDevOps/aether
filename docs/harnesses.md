# Agents

Aether does not install or bundle coding agents. You install the agent CLI in
your [environment](environments.md) (your own persistent container shell) and
sign in with the vendor's own login. Runs then launch that CLI inside a
container. This guide lists which agents work, how to sign in to each, and what
does not work. Terms such as run, swarm and workspace are in the
[glossary](README.md#concepts).

## Supported agents

Aether launches an agent in one of two modes. **Standard** shows the agent's
own terminal UI. **Enhanced** drives the agent over the Agent Client Protocol
and shows messages, tool calls and permission prompts as dashboard controls
([Enhanced runs](enhanced-runs.md)). A third mode, **Background**, runs the
agent once without input, commits the result and exits.

| `--agent` | CLI | Sign in | Standard | Enhanced | Mail reaches the agent | Known limits |
| --- | --- | --- | --- | --- | --- | --- |
| `claude` | Claude Code | `/login` inside `claude` | status reporting | yes, needs an adapter; can switch modes | hooks at turn boundaries, plus idle wake | Setup suggests Standard: the Enhanced adapter runs on the Claude Agent SDK |
| `codex` | OpenAI Codex CLI | `codex login`, device code | status at turn end only | yes, needs an adapter | hooks at boundaries after you trust them once; no idle wake | Trust prompt on the first Standard run; subagents do not nest in Enhanced |
| `pi` | pi | `/login` inside `pi` | status reporting | yes, needs an adapter | hooks at boundaries, plus idle wake | None known |
| `omp` | oh-my-pi | log in inside `omp` | status reporting | yes, built in; can switch modes | hooks at boundaries, plus idle wake | Sharing an account shares a whole directory |
| `opencode` | OpenCode | `opencode auth login` | status reporting | yes, built in | hooks at boundaries, plus idle wake | Never auto-updated; reports no usage |
| `copilot` | GitHub Copilot CLI | `copilot login --device-code` | no status; stalls are judged on silence | yes, built in | hooks at boundaries; no idle wake | Enhanced runs cannot take back `--allow-all` |
| `gemini` | Gemini CLI | API key, or `NO_BROWSER=true gemini` | no status | yes, built in | hooks only if you install them by hand | Serves paid API keys and Code Assist only |
| `cursor` | Cursor CLI (runs as `agent`) | `NO_OPEN_BROWSER=1 agent login` | no status | yes, built in | hooks only if you install them by hand | A Cursor admin policy can bring back approval prompts |
| `antigravity` | Antigravity CLI (runs as `agy`) | run `agy` | no status | none | none: the agent reads its inbox explicitly | No Enhanced mode, no packaged hooks |

Mail is a message to a run from a person or another run, which the agent reads
from its inbox ([Incoming coordination hooks](#incoming-coordination-hooks)).
In an Enhanced run, mail needs no hooks: when the agent is idle and mail is
waiting, the server sends it one prompt ([Mail](enhanced-runs.md#mail)). The
hooks and idle wake in the table apply to Standard runs.

**Set up** suggests Enhanced for Codex, oh-my-pi, OpenCode, Copilot, Gemini and
Cursor, and Standard for Claude Code, pi and Antigravity. The dashboard then
remembers your choice per agent
([Choose Enhanced](enhanced-runs.md#choose-enhanced)). `aether run` defaults to
Standard.

Agents with no status reporting in Standard mode are judged on silence alone: a
run that goes quiet for `--stall-threshold` parks as **Needs you** with a
reason starting `stalled:` ([failure-handling.md](failure-handling.md)).

Other CLIs work as [custom agents](#custom-and-unlisted-agents).

## Set up an agent

You do this once per agent, per person.

1. On your own computer, install the agent:

   ```sh
   aether agent add <name>
   ```

   This opens your environment's terminal, the same shell `aether terminal`
   opens, types the vendor's install command and leaves you in that shell. The
   agent installs into `~/.local/bin`, which persists in your home. Add
   `--enhanced` to install the Enhanced adapter too (see
   [Enhanced adapters](#enhanced-adapters)). In the dashboard, **Set up** on the
   **Agents** page or onboarding's **Agent** step does the same: it lets you
   pick Standard or Enhanced, installs the agent, and opens a terminal with the
   login command typed.

   The environment has no `aether` command (only `aether-internal`), so run
   `aether` commands on your computer, never in this shell.

2. When the install finishes, sign in from the same environment shell with the
   vendor's login, as described under [Per-agent setup](#per-agent-setup). To
   sign in later, open the shell with `aether terminal`. The environment has no
   browser: open the URL the CLI prints in your own browser. In the dashboard,
   clicking an OAuth URL with a loopback redirect starts the callback forward
   for you. From the CLI, start it yourself in a second terminal on your
   computer before you finish the browser flow:

   ```sh
   aether forward terminal <callback-port>
   ```

   Device-code logins need no forward.

3. Check the **Agents** page. It shows **Installed** or **Not installed** and
   **Login found** or **No login found**. These checks look for the executable
   and the login file. The agent itself verifies the login when it starts.

The login and executable live in your home, so every container you launch sees
them. If you skip the login, the agent's own login prompt appears in the run's
terminal. Attach with `aether attach <run>` and finish it there.

Installing `codex`, `pi`, `copilot` or `gemini` needs `npm`. If `npm` is not on
the environment's `PATH`, the install fails with, for example,
`npm is not in this environment's PATH, and codex installs through npm`. The
dashboard's **Set up** runs the install under `mktemp` and `timeout`, so the
image must provide both. Without them it fails with `timeout: not found` and
exit status 127.

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
login.

#### Claude Code

Start `claude` and use `/login`. It prints a URL to open in your own browser and
takes a code back. `/status` shows which credential is active. The login lands
in `~/.claude/.credentials.json`.

For an API key, set `ANTHROPIC_API_KEY` in the server's environment. Aether
passes through `ANTHROPIC_API_KEY` only. A `CLAUDE_CODE_OAUTH_TOKEN` from
`claude setup-token` is not passed, so use the terminal for subscription logins.

Claude Code refuses `--dangerously-skip-permissions` as root, so every `claude`
run gets `IS_SANDBOX=1`.

#### Codex

Run `codex login` and choose the device-code option. The login lands in
`~/.codex/auth.json`. `OPENAI_API_KEY` in the server's environment is the
API-key alternative.

Your first Standard run opens Codex's **Hooks need review** screen for Aether's
inbox hooks. Choose **Trust all and continue**, or open `/hooks` and press `t`.
One approval covers every workspace until Aether's hook definitions change.
**Continue without trusting** asks again at the next start. A swarm worker can
be the first Standard Codex run: open its terminal to answer. If the list shows
hooks you do not recognize, choose **Review hooks**. A copy of the hooks in
`~/.codex/hooks.json` would run every hook twice in an Aether run, so remove it.

#### pi

Start `pi` and use `/login` to pick a provider. The login lands in
`~/.pi/agent/auth.json`. `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` in the server's
environment is the API-key alternative.

#### oh-my-pi (omp)

oh-my-pi is a fork of pi with its own executable and home. The install command
is `curl -fsSL https://omp.sh/install | sh`, which puts `omp` in
`~/.local/bin`. Start `omp` and log in through its own flow. The login lands in
the agent database under `~/.omp/agent/`.

`omp` is a shipped name, and a shipped name always wins over a definition you
stored with `aether agent add omp`. To launch your own build, register it under
a name Aether does not ship.

#### OpenCode

Run `opencode auth login` and pick your provider. The login lands in
`~/.local/share/opencode/auth.json`. OpenCode settings and plugins live in
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
not work.

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
server's environment. Gemini reads only the first `.env` it finds walking up
from the run's directory, so a `.env` in the checkout wins over
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
`GEMINI_API_KEY` and put `"modelProvider":"gemini"` in
`~/.gemini/antigravity-cli/settings.json`. For Google Cloud application default
credentials, set `AGY_ADC_AUTH=true`.

Antigravity has no Enhanced mode, and an Enhanced launch is refused. `agy` has no
ACP command, and Google's separate `antigravity-acp` package cannot finish its
Google sign-in inside a run container.

### Install hooks by hand

Claude Code, Codex, Copilot, pi, oh-my-pi and OpenCode load Aether's inbox hooks
for you on a Standard run that has a task. Gemini CLI and Cursor CLI do not.
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
swarm choices and templates that name it. Removal only touches your own home. It
is refused while an install or an update runs in the home. A launch of a removed
agent you defined answers `unknown harness "x"; register it with: aether agent add x`.

### Updates before launch

Before a run launches, the server updates the shipped agent in your
`~/.local/bin`, using the agent's own update command or `npm`. It checks on the
first launch per agent and then at most every 6 hours (15 minutes after a
failed update). A launch waits at most 25 seconds. If the update is still
running, the agent starts on the installed version and the run's timeline says:

```text
starting the installed version 2.1.288 (Claude Code) while claude updates
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

`aether run` takes `--mode standard` (the default), `enhanced` or `background`.

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
| `codex` | `-c developer_instructions=...`, which replaces any `developer_instructions` in your `config.toml`. Aether copies the top-level value of `~/.codex/config.toml` in first. A value in a Codex profile, a project `.codex/config.toml` or a workspace `CODEX_HOME` is replaced for that launch: put such instructions in `AGENTS.md` |
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
| `--acp-mode <id>` | The session mode a new Enhanced run starts in. Pick one that acts without asking, or a swarm worker waits on the prompt. Needs `--acp`, no whitespace |
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

When agents work as a swarm or share files, other runs and people send them
messages. Aether keeps those in a durable mailbox and tells the agent to read it.
The reading itself is a command, `aether-internal inbox`. The agent learns mail
is waiting in one of two ways. A **hook** runs at a boundary such as a prompt or
a finished tool call. **Idle wake** starts a new turn in a session that has been
waiting. Which agents get which is in the table above. Both are managed for you,
except where [Install hooks by hand](#install-hooks-by-hand) says otherwise.

Hooks work only for a coordinated run that has a task, in Standard mode. A
taskless launch, a Background run, a run with `--conflict-coordination=false`,
and an agent whose command an administrator overrode do not get them. Enhanced
runs do not use hooks: the server prompts the idle session instead. Without
either, the agent reads mail only when it runs `aether-internal inbox`.

Claude Code is the one with a hook-based idle wake in Standard mode. Its wake
starts a turn with no human input, and the terminal shows **Stop hook feedback**.
That wording is Claude Code's own framing, not an error. After you press Esc, a
wake pauses until a turn completes. Codex and Copilot have no idle wake, so mail
waits for the next hook boundary. pi, oh-my-pi and OpenCode wake only their main
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
`claude`, `pi`, `omp` and `opencode` report both ends of a turn. `codex` reports
only when a turn ends, so its run returns to `running` on agent output or a file
change. That includes the echo of your own typing, so a long prompt typed into a
parked `codex` run can read as `running` before you send it. Copilot, Gemini,
Cursor and Antigravity have none.

Enhanced and Background-over-ACP runs need no reporter: the session reports both
ends of each turn. Background runs without ACP are never reported.

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
- No idle wake exists for Codex, Copilot, Gemini or Cursor, and Antigravity has
  no hooks at all.
- Only Claude Code and Codex show subscription usage and can move to another
  account on a usage limit.
- Sharing an `omp` account shares the whole `~/.omp/agent` directory. oh-my-pi
  loads extensions and MCP commands from there, so a recipient's run can plant
  code that runs in the owner's sessions. Share an `omp` account only with
  someone you would give your home to ([security.md](security.md#account-sharing)).
- Aether does not update OpenCode, and does not roll back any update.
- The Environment has no browser, so every OAuth login needs your own browser.

Next: [Environments](environments.md).
