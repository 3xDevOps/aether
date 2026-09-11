# Agent harnesses

A **harness** is one agent CLI and everything Aether needs to know to launch
it: how to start it in interactive and headless mode, where its login state
lives, where its configuration lives, and which environment variables carry an
API key. The registry is `internal/harness`, a map and a few functions - not a
plugin system.

Two rules shape everything below:

1. **Aether does not install agents for you.** A member runs the displayed
   vendor install command in their environment terminal. The command should
   install the executable into `~/.local/bin`.
2. **Aether does not extract or sync vendor credentials.** Logins happen
   through the vendor's own flow in an Aether terminal. Credentials remain in
   the member home; an explicit account share mounts that whole home into a
   recipient's run.

## Shipped harnesses

| `--agent` | CLI | Login state | Configuration root | API key env | Launch env | MCP | Resume | Steering | Env setup |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `claude` | Claude Code | `~/.claude` | `~/.claude` | `ANTHROPIC_API_KEY` | `IS_SANDBOX=1` | yes (`--mcp-config`) | by session ID (`--session-id`, `--resume`) | PTY | yes |
| `codex` | OpenAI Codex CLI | `~/.codex` | `~/.codex` | `OPENAI_API_KEY` | - | no | no | PTY | yes |
| `pi` | pi | `~/.pi` | `~/.pi` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | - | no | best effort (`--continue`) | PTY | yes |
| `opencode` | opencode | `~/.local/share/opencode` | `~/.local/share/opencode` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | - | no | no | HTTP TUI API | no |
| `fake` | a script you name | - | - | - | - | no | no | PTY | no |
| `custom` | deployment-supplied | - | - | - | - | no | no | PTY | no |

Paths are inside the run container, relative to the run user's home (`/root`,
or `/home/aether` for a non-root image user).

The **Env setup** column marks harnesses that can participate in agent setup:
the dashboard can open the member's environment terminal for installation and
login. Exactly `claude`, `codex`, and `pi` qualify; everything else stays
launchable for runs but is not offered in that setup flow.

Only harnesses with an **MCP** column of `yes` can be pointed at the in-container
coordination bridge, so conflict coordination between overlapping runs works for
Claude Code and degrades to the advisory overlap notice for the rest. See
[coordination.md](coordination.md).

The **Launch env** column is what the server sets in the run container
because the CLI will not start without it. It is applied after the
workspace's own variables, so a workspace cannot leave the agent unable to
run. See the launch table below for why `claude` needs one.

The **Resume** column is what a relaunch uses when a server reboot
interrupted the run. The flags ride directly behind the executable.

**By session ID** is exact. The server generates one UUID per run and
launches with `claude --session-id <uuid>`, recording it on the run row; the
relaunch runs `claude --resume <uuid>`, which names that conversation
outright. `--session-id` is launch-only - Claude Code refuses an ID that
already names a conversation ("Session ID `<id>` is already in use.") - so
the two flags never appear together.

**Best effort** is `--continue`, which names no conversation: it continues
whichever conversation the harness spoke last in the working directory.
Every run mounts its checkout at the same container path and shares one
credential home per member, so what comes back is that member's *most
recent* conversation at that path - not necessarily the interrupted run's
own, and not necessarily one from the same workspace. `pi` has no
launch-time session ID, so it stays here, and so does any run row created
before session pinning existed.

A harness with neither starts fresh, and a deployment-supplied argv override
never has any of these appended - nothing checks the override is still that
CLI. Relaunching a run that finished on its own never resumes; it gets a
session of its own. See [failure-handling.md](failure-handling.md).

## Steering delivery

`run.inject` writes a message, then the harness's submit sequence, to the
run agent's PTY. Most TUIs send the message on one Enter (`\r`); `opencode`
accepts steered text into its editor on the first Enter and sends on the
second, so its profile ends the write with `\r\r`. The sequence lives in the
harness profile (`SteerSubmit` in `internal/harness`), not in the caller:
the scheduler and the coordination radar both resolve it from the run's
harness before writing. Aether records the delivery only after the complete
stdin write succeeds and renders the attribution without terminal control
bytes.

Only `claude` has a **structured-output adapter** today, so its headless runs
produce typed tool-call and token events. Everything else degrades to the PTY
transcript plus the diff timeline, which is always enough. Adding an adapter is
[adapters.md](adapters.md).

## How Aether launches them

`--mode tui` (the default) runs the agent's native interactive TUI in a
persistent server-side PTY: `aether attach <run>` puts you in it from the
CLI, and the dashboard navigates there automatically on launch. The native
TUI process is supervised directly: Aether never replaces it with a login
shell in the run container. A clean exit commits the result to the run branch,
destroys the run container, and marks the run `completed`; an unsuccessful
exit is likewise handled by supervision rather than leaving a shell behind.
`--mode headless` runs the agent's machine-readable mode and exits with it.
Full-permission flags are applied by default in both - the agent is in a
container, and the container is the boundary ([security.md](security.md)).

The task prompt is optional in tui mode: launch without one and you land in
the agent's bare interactive TUI, exactly as if you had started the CLI
yourself, and type the first prompt there. Every argv token that carries the
prompt is then dropped, so `opencode --prompt={task}` leaves whole rather than
dangling an empty flag. Headless mode has no interactive surface, so it still
requires a task.

Where conflict coordination is on, the server appends the co-author rule to
the task before substituting `{task}`, so the agent is told to read
`/run/aether/co-authors` before each commit. Only the prompt the harness
receives changes: the stored task, the branch slug, and every CLI and
dashboard surface keep what the member typed. See
[coordination.md](coordination.md).

| `claude` | `claude --dangerously-skip-permissions {task}` | `claude -p --output-format stream-json --verbose --dangerously-skip-permissions {task}` |
| `codex` | `codex --dangerously-bypass-approvals-and-sandbox {task}` | `codex exec --json --dangerously-bypass-approvals-and-sandbox {task}` |
| `pi` | `pi {task}` | `pi -p {task}` |
| `opencode` | `opencode --prompt={task}` | `opencode run {task}` |
Every `claude` **run** also gets `IS_SANDBOX=1`. Runs execute as root on the
standard image, and Claude Code refuses `--dangerously-skip-permissions` as
root; that variable is a vendor internal, not a supported interface, and the
vendor's own answer is to run the container as a non-root user. It is a
stopgap until the standard image ships one. Environment terminals carry no
harness, so they get none of this - the member's own shell is not launching
an agent.

These are the vendors' own flags, and vendors rename them and tighten how
they combine - `claude` now refuses `--output-format stream-json` unless
`--verbose` comes with it. If a launch fails with the CLI rejecting its own
arguments, the installed CLI has drifted from the registry.
Update the registry or install a compatible CLI in the member's environment
terminal. The installed executable lives in that member's environment home.
An argv override replaces the shipped template wholesale, so a registry fix
never reaches it: a deployment's `--harness-definitions` entry that redefines
a shipped harness has to be updated on its own. It keeps the registry's key
passthrough and launch env for that name, since neither is part of the
command line.

## Setting up an agent

Once per person, per agent:

```sh
aether agent add <name>
```

For a shipped name, the dashboard's Agents step opens the live environment
terminal dock and types the vendor install script for you. In the CLI, run:

```sh
aether terminal
```

Install the executable into `~/.local/bin`, complete the vendor login in that
terminal, and return to the dashboard. The login and executable are in your
member home, so every container for that member sees them. A member-defined
name also records a launch definition under that member.

The dashboard's **I've installed and logged in** button checks `agent.list`
before confirming installation. The Agents page shows **Installed** or
**Not installed** for your account. These checks verify the executable;
the agent verifies its vendor login when it starts. Shipped agents need no
separate registration record. Other members can use the installation only
after you share your account; see [teams.md](teams.md#agent-accounts).

For an unshipped name the command asks for interactive and headless launch
templates first (`<name> {task}` and `<name> -p {task}` by default). Install the
executable into `~/.local/bin` using the vendor's documented procedure, then
complete its login.

The environment terminal has no browser. Open the URL it prints in your own
browser. In the dashboard, clicking an OAuth URL with a loopback redirect
starts the matching callback forward before the authorization page opens. With
the CLI, start it explicitly before completing the browser flow:

```sh
aether forward terminal <callback-port>
```

Device-code flows do not need a callback forward.

### Setup details

The login commands below run in the environment terminal:

Three things to know:

- **There is no browser in the container.** Open the printed URL on the machine
  running `aether gui` or the CLI.
- **Logins belong to one member account.** They reach another member's run only
  through the account owner's explicit grant described in
  [teams.md](teams.md#agent-accounts).
- If you skip the login part, the agent's own login prompt simply appears in
  the run's PTY. Attach with `aether attach <run>` and complete it there; it
  persists the same way.

### Claude Code

Inside `aether terminal`, start the CLI and use its `/login` slash command,
which prints a URL to open in your own browser and takes a code back. `/status`
shows which credential is active. Credentials land in `~/.claude` and remain
in the member's persistent home. The browser configuration import skips known
credential names before upload.

For an API key instead of a subscription, set `ANTHROPIC_API_KEY` in the
server's environment (`/etc/aether/aether-server.env` with the shipped systemd
unit) and skip setup entirely. Note that Aether passes through
`ANTHROPIC_API_KEY` only; a `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`
is not in the passthrough list, so use the terminal for subscription auth.

### Codex

Inside `aether terminal`, run the CLI's login command and choose the
device-code option. Codex writes `auth.json` under `~/.codex`, which remains
in your member home. The browser configuration import skips known credential
names before upload.

`OPENAI_API_KEY` in the server environment is the API-key alternative.

### pi

Inside `aether terminal`, start the CLI and use its `/login` command to pick
a provider. Tokens land in `~/.pi/agent/auth.json` under the member's home.
The browser configuration import skips known credential names before upload.
`OPENAI_API_KEY` in the server environment is the API-key alternative.

### opencode

Inside `aether terminal`, run `opencode auth login` and pick your provider.
Credentials are written to `~/.local/share/opencode/auth.json` in the member
home.

### `fake`

The deterministic test harness. It has no login and no fixed command: the
server reads its argv from the `AETHER_FAKE_AGENT` environment variable at
launch time, so it runs whatever you name - typically a script committed to the
repo, since the run's checkout is mounted at `/workspace`.

```sh
AETHER_FAKE_AGENT="sh /workspace/agent.sh {task}" aether-server serve --data-dir /var/lib/aether
```

The `{task}` placeholder expands to the run's task text; omit it if the
script does not need it.

This is how the [quickstart](quickstart.md#prove-the-plumbing-without-an-agent-subscription)
proves the whole lifecycle without any vendor account, and it is the harness
the end-to-end tests drive.

### Custom agents

Custom launch definitions come from two places, resolved in this order:

1. **Server configuration** (administrator): `--harness-definitions` or the
   `AETHER_HARNESS_DEFINITIONS` environment variable. These pin a name for
   every member and always win.
2. **Member registration**: `aether agent add <name>` stores a definition
   scoped to the registering member, over the normal control channel; no
   server restart. A member's definition shapes argv only inside that
   member's own containers and never affects anyone else. Shipped names and
   the reserved names `custom` and `fake` cannot be registered.

Both forms carry the same fields and pass the same validation. The
administrator JSON is an object keyed by harness name. Each definition must
name the executable and provide both interactive and headless argv. `{task}`
is replaced as one argv value, never passed through a shell. Profile and
credential paths are explicit absolute container paths under `/root` or
`/home/aether`; credentials must be inside the profile root when one is
configured. Deny names are basenames only.

For example, an administrator can pin OMP without adding vendor logic to
Aether (a member would instead just run `aether agent add omp`):

```json
{
  "omp": {
    "Name": "omp",
    "TUIArgs": ["omp", "{task}"],
    "HeadlessArgs": ["omp", "-p", "{task}"],
    "Executable": "omp",
    "ProfileRoot": "/home/aether/.omp",
    "CredentialPaths": ["/home/aether/.omp"],
    "DenyNames": ["auth.json", "token.json"]
  }
}
```

The server validates that the executable is a name rather than a host path,
that argv starts with that executable, and that profile, credential, and
deny-name policies are safe. An invalid administrator definition rejects
server startup; an invalid member registration is refused at the RPC.
Agent installation, login state, configuration import, and launch definitions
remain separate concerns: installation and login state live in the member home,
the one-time browser import writes selected configuration there, and the
definition resolves argv for that member. The terminal is the only setup
transport for installation and login.

## Agent configuration: import and Files

The dashboard does not watch a laptop directory or run an AI inventory. During
the Agents step, choose one directory such as `~/.claude`, `~/.codex`, or
`~/.pi` with the browser directory picker. A preview shows the files that will
be sent and the paths left out before upload. Import is explicit and one-time:
after it succeeds, the import control is gone.

The picker normally matches the selected directory basename to a known harness.
If the basename is unknown or matches more than one destination, choose the
destination explicitly. The browser skips known credential names (including
credential names in nested paths) and runtime/history defaults before upload.
Remaining bytes are uploaded and scanned by the server; do not assume all
secret-looking content stays on the laptop. An import can include empty files
and arbitrary binary bytes. It is limited to **1 MiB per file**, **20 MiB
decoded total**, and **2,000 files**. Browser imports create new files with
mode `0644`; the browser cannot preserve executable mode or symlinks, so a
script may need `chmod` in the remote terminal.

The imported files are written into your authenticated member's persistent
configuration home. That home is mounted read-write in your environment
terminal and in runs using your account, so the change is immediately visible
to existing and future runs (an agent may need to reload its configuration).
An account share gives another member's run the same home; it does not create
an isolated per-run profile. A snapshot pin is audit metadata, not an isolated
writable copy and not a promise that future home changes affect only new runs.
Changing configuration does not rebuild the installed-agent image.

After import, open **Files** to browse your own member configuration alongside
workspace base and live-run files. The editor supports JSON, JavaScript,
TypeScript, Markdown, Python, and TOML syntax highlighting, plus find/replace.
Edits stay as in-memory dirty tabs while you navigate. Save explicitly with
**Save**, **Commit to <branch>**, or Ctrl/Cmd-S; there is no autosave or
force-save. Browser navigation warns before unloading dirty buffers.

Configuration files are full UTF-8 text up to **512 KiB**. Binary or truncated
files are read-only. New configuration files accept nested relative paths and
never overwrite an existing file. A stale save keeps the draft; reload from
the server only when you want to discard it and replace it with current
content. `config.*` always addresses the authenticated member's own home; an
admin cannot select another member.

For workspace files, **Commit to <branch>** makes a one-file commit on the
workspace base branch and does not push upstream. Live-run writes change the
run's uncommitted checkout. Workspace saves require **Push**; live-run saves
require **Steer**. File revisions are SHA-256 hashes of the exact original
full bytes. The server checks the revision immediately before rename while
holding Aether's root lock; this is not an exclusive lock against arbitrary
live agent filesystem writers.

### Manual profile commands

The explicit CLI profile surface remains available for operators who need
content-addressed snapshots or rollback:

```sh
aether profile push --agent claude
aether profile status --agent claude
aether profile rollback --agent claude <snapshot-id>
```

`profile push` is manual; it is not run by a local watcher. Its exact optional
flags are repeatable `--skip-secret <file>` and
`--allow-secret <file>`. The latter requires an explicit
`--workspace <workspace>` for audit attribution:

```sh
aether profile push --agent claude --skip-secret <file>
aether profile push --agent claude --allow-secret <file> --workspace <workspace>
```

These commands read the local harness root named by the selected agent. They
are separate from browser directory import and from editing the persistent
member home in **Files**. Use the member-home editor when a change should be
visible immediately to the shared home.

## Adding a harness

The registry is one map entry: argv templates for both modes, credential paths,
profile root, denylist, API key passthrough, and the optional MCP, session,
and resume flags. An adapter is a separate, optional file. Both are covered in
[adapters.md](adapters.md).
