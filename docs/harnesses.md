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

| `--agent` | CLI | Login state | Configuration root | API key env | Launch env | MCP | Status | Resume | Steering | Env setup |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `claude` | Claude Code | `~/.claude` | `~/.claude` | `ANTHROPIC_API_KEY` | `IS_SANDBOX=1` | yes (`--mcp-config`) | hooks (`--settings`) | by session ID (`--session-id`, `--resume`) | PTY | yes |
| `codex` | OpenAI Codex CLI | `~/.codex` | `~/.codex` | `OPENAI_API_KEY` | - | no | notify (`-c notify=[...]`) | no | PTY | yes |
| `pi` | pi | `~/.pi` | `~/.pi` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | - | no | extension (`-e`) | best effort (`--continue`) | PTY | yes |
| `omp` | oh-my-pi | `~/.omp` | `~/.omp` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | - | no | extension (`-e`) | best effort (`--continue`) | PTY | no |
| `opencode` | opencode | `~/.local/share/opencode` | `~/.local/share/opencode` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | - | no | plugin (`OPENCODE_CONFIG_CONTENT`) | no | HTTP TUI API | no |
| `fake` | a script you name | - | - | - | - | no | - | no | PTY | no |
| `custom` | deployment-supplied | - | - | - | - | no | - | no | PTY | no |

Paths are inside the run container, relative to the run user's home (`/root`,
or `/home/aether` for a non-root image user).

The `config.roots` response used by the dashboard carries a
`runtime_ignores` list for each configuration root. These are root-relative
paths, and the browser applies them before reading selected file bytes with
case-sensitive exact or component-prefix matching; trailing slashes are
presentation-only. This policy is destination-specific: a directory whose
basename is renamed or ambiguous must be assigned to a destination before
the import preview can be read. Credential names remain globally excluded,
independent of this runtime list.

The **Env setup** column marks harnesses that can participate in agent setup:
the dashboard can open the member's environment terminal for installation and
login. Exactly `claude`, `codex`, and `pi` qualify; everything else stays
launchable for runs but is not offered in that setup flow.

Only harnesses with an **MCP** column of `yes` can be pointed at the in-container
coordination bridge, so conflict coordination between overlapping runs works for
Claude Code and degrades to the advisory overlap notice for the rest. See
[coordination.md](coordination.md).

The **Status** column is how the agent itself tells Aether it is waiting for
you, rather than leaving the server to guess from silence. See "Status
reporting" below.

The **Launch env** column is what the server sets in the run container
because the CLI will not start without it. It is applied after the
workspace's own variables, so a workspace cannot leave the agent unable to
run. See the launch table below for why `claude` needs one. A reporter that
rides in the environment rather than on the command line (`opencode`) is
not in this column: it is set on interactive runs alone and is dropped by
the same things that drop the reporter, so it lives under "Status
reporting".

Either way, a variable the server sets itself replaces a workspace
environment variable of the same name rather than merging with it.

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
own, and not necessarily one from the same workspace. Neither `pi` nor its
fork `omp` has a launch-time session ID, so they stay here, and so does any
run row created before session pinning existed.

A harness with neither starts fresh, and a deployment-supplied argv override
never has any of these appended - nothing checks the override is still that
CLI. Relaunching a run that finished on its own never resumes; it gets a
session of its own. See [failure-handling.md](failure-handling.md).

## Status reporting

**Needs you** means the agent is waiting for you, or the run stalled. The
first half comes from the agent itself.

A harness with a **Status** entry can run a command on its own lifecycle
events. Aether points each one at the staged server binary inside the
container, through whatever the CLI's own mechanism is, for that launch
alone - by flag where the CLI has one, by environment where it does not -
and where that mechanism needs a file, the file is written into the run's
coordination directory beside the MCP config. The interactive launch of
each, with the session pin and the MCP registration it already carried:

```
claude --session-id <uuid> --dangerously-skip-permissions "<task>" \
  --mcp-config /run/aether/mcp.json \
  --settings /run/aether/claude-settings.json

codex --dangerously-bypass-approvals-and-sandbox "<task>" \
  -c 'notify=["/opt/aether/aether-server","report","codex"]'

pi "<task>" -e /run/aether/status.ts
omp --auto-approve "<task>" -e /run/aether/status.ts
```

`opencode` has no flag for a plugin, so its launch command is untouched and
the plugin is named in the environment:

```
OPENCODE_CONFIG_CONTENT={"plugin":["file:///run/aether/opencode-status.js"]}
opencode --prompt="<task>"
```

Every one of them ends up running the same command inside the container:

```
/opt/aether/aether-server report claude                # hook event JSON on stdin
/opt/aether/aether-server report codex '<payload>'     # the notify argument
/opt/aether/aether-server report pi --event <name>     # from the extension, pi and omp
/opt/aether/aether-server report opencode --event session.idle
```

The report travels back over the run's own coordination socket, so no token
enters the container and nothing new is mounted - this is the same bridge
conflict coordination uses ([mcp-bridge.md](mcp-bridge.md)). The server
turns it into a run status straight away:

| The agent says | The run becomes | Reason shown |
| --- | --- | --- |
| the turn ended, or it has been idle at its prompt | `needs-attention` | `waiting for your input` |
| it is asking permission | `needs-attention` | `waiting for your permission` |
| it is asking a question | `needs-attention` | `waiting for your answer` |
| it started a turn, ran a tool, or got its answer | `running` | `agent resumed` |

Anything else the harness reports - a session opening, a reply streaming
in, a compaction - is ignored rather than guessed at, and so is a subagent's
own turn: opencode gives one a session of its own, and that session going
idle is not the run's turn ending. The rule holds the other way round too.
The run is `running` while any of its sessions is, so an opencode
background subagent still working after the turn that spawned it ended
keeps the run off your queue until it finishes - something there is still
working.

opencode never announces the resume after a permission or a question of its
own accord - its session stays busy for the whole tool call the prompt
interrupted - so the member's answer is what returns the run to `running`.
A run can have several prompts open at once, one per session, and only the
answer to the last of them returns it: until then the run stays parked. If
the turn that asked ended while the prompt was still open, that answer
parks the run at `needs-attention` instead - nothing is working any more -
and the next turn the agent starts is what returns it to `running`.

`codex` only says when a turn ends. It never says a new one started, so its
run comes back to `running` the way a harness with no reporter does: on
agent output or a file change. Everything drawn in the terminal counts
there, the echo of your own typing included, so a long prompt typed into a
parked `codex` run can read as `running` before you send it. `claude`, `pi`
and `omp` report both ends, and their runs stay parked until the agent
itself says it is working again - a TUI repainting while you type is not
work.

The last report is recorded with the run, so it survives a server restart:
a run the agent parked comes back parked, and only what would have released
it before releases it now. See [failure-handling.md](failure-handling.md).

For a harness with a **Status** of `-`, nothing changes: the run is judged
on silence alone and parks at `needs-attention` after `--stall-threshold`
with a reason that leads with `stalled:`. See
[failure-handling.md](failure-handling.md).

Four things turn the reporter off:

- **Headless runs.** `--mode headless` never gets the reporter: the agent
  exits when it is done and never waits for anyone.
- **`--conflict-coordination=false`.** There are no mounts, so there is no
  socket to report on and no directory to write the assets into.
- **An argv override.** A `--harness-definitions` entry that redefines a
  shipped harness drops the status arguments and the status environment
  exactly as it drops the MCP flag - nothing checks the overridden command
  is still that CLI.
- **`OPENCODE_PURE` in the workspace environment.** opencode loads no
  external plugin at all when that variable is set, Aether's included, and
  Aether does not take it away from you. The run launches and works
  normally; it reports nothing, and is judged on silence like a harness
  with no reporter.

The asset files are server-written, read-only, and live in `/run/aether`,
never in the worktree or the member's synced profile. Each applies for that
launch alone and merges over what the member already has: `--settings`
layers one settings document over Claude Code's own, `-e` loads one more pi
extension beside the ones you already have, `-c` overrides your
`~/.codex/config.toml` `notify` for this run alone - so if you use `notify`
for something of your own, it keeps working everywhere except in an Aether
run - and `OPENCODE_CONFIG_CONTENT` is merged into opencode's config with
the plugin lists concatenated, so the member's own plugins still load.

What merges is opencode's own config, not a second value of that variable:
an interactive `opencode` run reserves `OPENCODE_CONFIG_CONTENT` for the
plugin, and a workspace environment variable of that name is replaced
rather than combined. Inline config a workspace needs on every run goes in
a file the workspace names with `OPENCODE_CONFIG`, which Aether never
sets.

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
| `omp` | `omp --auto-approve {task}` | `omp -p --auto-approve {task}` |
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

For a shipped name, the local dashboard's Agents step opens the live
environment terminal dock and types the vendor install script for you. In the
CLI, run:

```sh
aether terminal
```

Install the executable into `~/.local/bin`, complete the vendor login in that
terminal, and return to the dashboard. The login and executable are in your
member home, so every container for that member sees them. A member-defined
name also records a launch definition under that member.

The local dashboard's **I've installed and logged in** button checks `agent.list`
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

### omp

oh-my-pi is a fork of pi with its own executable and its own home. Install
it with the vendor's command, `curl -fsSL https://omp.sh/install | sh`,
which puts `omp` in `~/.local/bin`. Inside `aether terminal`, start the CLI
and log in through its own flow; credentials land in the agent database
under `~/.omp/agent/`, which is excluded from profile sync. `ANTHROPIC_API_KEY`
or `OPENAI_API_KEY` in the server environment is the API-key alternative.

`omp` is a shipped name, and a shipped name always wins over a member's own
definition of the same name. If you ran `aether agent add omp` before Aether
shipped it, your stored definition is ignored from now on and runs use the
launch template in the table above. The row stays where it is - there is no
command that removes one, and `aether agent list` keeps printing it as
`agent omp member` next to `agent omp shipped`. To launch your own build,
register it under a name Aether does not ship.

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

For example, an administrator can point `omp` at a different build for every
member. A shipped name is the one case a member cannot register themselves,
so an administrator definition is the only way to change one. A definition
replaces the shipped profile rather than extending it, so it carries the
deny names too - omp keeps its provider keys in `agent.db`, which no
generic denylist knows about:

```json
{
  "omp": {
    "Name": "omp",
    "TUIArgs": ["omp", "{task}"],
    "HeadlessArgs": ["omp", "-p", "{task}"],
    "Executable": "omp",
    "ProfileRoot": "/home/aether/.omp",
    "CredentialPaths": ["/home/aether/.omp"],
    "DenyNames": ["agent.db", "agent.db-wal", "agent.db-shm"]
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

The local dashboard (`aether gui`) does not watch a laptop directory or run an
AI inventory. During the Agents step, choose one directory such as
`~/.claude`, `~/.codex`, `~/.pi`, or `~/.omp` with the browser directory picker.
The browser waits for `config.roots` and a known destination before it reads
any file bytes. A unique basename selects its destination automatically; an
unknown or ambiguous basename must be assigned explicitly. The preview then
shows the files that will be sent and the paths left out before upload. Import
is explicit and one-time: after it succeeds, the import control is gone. The
server-hosted dashboard has no onboarding picker; use local `aether gui` for
this step.

Credential names in any path component and `*.pem` files are always skipped
before upload. Runtime/history exclusions come from the selected root's
`runtime_ignores` metadata, which matches exact root-relative paths or
component prefixes case-sensitively after trailing slashes are trimmed. This
policy applies to renamed directories too. Changing an ambiguous destination
clears the prior preview and re-reads the local file handles with the newly
selected policy; a stale read cannot replace the current preview. These local
exclusions are not overridden by `.aether-profile-ignore` in browser import.
`agent/skills/`, `agent/extensions/`, and `agent/npm/` remain configuration and
are imported.
Remaining bytes are uploaded and scanned by the server; do not assume all
secret-looking content stays on the laptop. A complete response reports
accepted counts and server exclusions. If the server stops after writing files,
the dashboard reports an incomplete result with exact committed paths, counts
and the real error, and warns that copied files remain. If the RPC response is
lost, the outcome is unknown and some files may have been copied; inspect
**Files** before retrying. There is no watcher or automatic retry: selecting
the directory and importing again is explicit.
An import can include empty files and arbitrary binary bytes. It is limited to
**1 MiB per file**, **20 MiB decoded total**, and **2,000 files**. Browser
imports create new files with mode `0644`; the browser cannot preserve
executable mode or symlinks, so a script may need `chmod` in the remote
terminal.

The imported files are written into your authenticated member's persistent
configuration home. That home is mounted read-write in your environment
terminal and in runs using your account, so the change is immediately visible
to existing and future runs (an agent may need to reload its configuration).
An account share gives another member's run the same home; it does not create
an isolated per-run profile. A snapshot pin records launch provenance, not an
isolated writable copy or a promise that home changes wait for later runs.
Changing configuration does not rebuild the installed-agent image.

After import, open **Files** to browse your own member configuration alongside
workspace base and live-run files. The editor supports JSON, JavaScript,
TypeScript, Markdown, Python, and TOML syntax highlighting, plus find/replace.
Edits stay as in-memory dirty tabs while you navigate. Save explicitly with
**Save**, **Commit to <branch>**, or Ctrl/Cmd-S; there is no autosave or
force-save. Browser navigation warns before unloading dirty buffers.

Configuration files are full UTF-8 text up to **512 KiB**. Binary or truncated
files are read-only. New configuration files accept nested relative paths and
never overwrite an existing file. A stale save keeps the draft; reload from the
server only when you want to discard it and replace it with current content.
Every `config.*` method requires `Launch` and addresses only the authenticated
member's own home; an admin cannot select another member.

For workspace files, **Commit to <branch>** makes a one-file commit on the
workspace base branch and does not push upstream. Live-run writes change the
run's uncommitted checkout. Workspace saves require **Push**; live-run saves
require **Steer**. The [Files protocol](local-gateway.md#files-and-member-configuration)
defines revision and concurrency rules.

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

The registry is one map entry: argv templates for both modes, credential
paths, profile root, denylist, API key passthrough, the optional MCP,
session, and resume flags, and the status reporter - what the harness can
report, which is what declares a reporter at all, plus the arguments or
environment variables that point the harness at it and any asset files
those name. An adapter is a separate, optional file. Both are covered in
[adapters.md](adapters.md).
