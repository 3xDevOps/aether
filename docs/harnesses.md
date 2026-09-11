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

| `--agent` | CLI | Login state | Profile sync root | API key env | Launch env | MCP | Status | Resume | Steering | Env setup |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `claude` | Claude Code | `~/.claude` | `~/.claude` | `ANTHROPIC_API_KEY` | `IS_SANDBOX=1` | yes (`--mcp-config`) | hooks (`--settings`) | by session ID (`--session-id`, `--resume`) | PTY | yes |
| `codex` | OpenAI Codex CLI | `~/.codex` | `~/.codex` | `OPENAI_API_KEY` | - | no | - | no | PTY | yes |
| `pi` | pi | `~/.pi` | `~/.pi` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | - | no | - | best effort (`--continue`) | PTY | yes |
| `opencode` | opencode | `~/.local/share/opencode` | `~/.local/share/opencode` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | - | no | plugin (`OPENCODE_CONFIG_CONTENT`) | no | HTTP TUI API | no |
| `fake` | a script you name | - | - | - | - | no | - | no | PTY | no |
| `custom` | deployment-supplied | - | - | - | - | no | - | no | PTY | no |

Paths are inside the run container, relative to the run user's home (`/root`,
or `/home/aether` for a non-root image user).

The **Env setup** column marks harnesses that can participate in onboarding:
running the local `profile` scan and opening the agent setup shell. Exactly
claude, codex, and pi qualify; everything else stays launchable for runs
but is not offered in those onboarding flows.

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
environment variable of the same name rather than merging with it, and the
run's timeline names the variable on launch.

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

## Status reporting

**Needs you** means the agent is waiting for you, or the run stalled. The
first half comes from the agent itself.

A harness with a **Status** entry can run a command on its own lifecycle
events. Aether writes the asset that arranges it into the run's coordination
directory beside the MCP config, and points the harness at it for that
launch alone - by flag where the CLI has one, by environment where it does
not.

`claude` takes a settings document registering a hook on every event that
says something. The interactive launch in full, with the session pin and
the MCP registration it already carried:

```
claude --session-id <uuid> --dangerously-skip-permissions "<task>" \
  --mcp-config /run/aether/mcp.json \
  --settings /run/aether/claude-settings.json
```

`opencode` has no flag for a plugin, so its launch command is untouched and
the plugin is named in the environment:

```
OPENCODE_CONFIG_CONTENT={"plugin":["file:///run/aether/opencode-status.js"]}
opencode --prompt="<task>"
```

Either asset ends up running the same command inside the container:

```
/opt/aether/aether-server report claude     # the hook, with the event JSON on stdin
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

The last report is recorded with the run, so it survives a server restart:
a run the agent parked comes back parked, and only the agent's own next
turn releases it. See [failure-handling.md](failure-handling.md).

For a harness with a **Status** of `-`, nothing changes: the run is judged
on silence alone and parks at `needs-attention` after `--stall-threshold`
with a reason that leads with `stalled:`. See
[failure-handling.md](failure-handling.md).

Four things turn the reporter off:

- **Headless runs.** `--mode headless` never gets the asset: the agent
  exits when it is done and never waits for anyone.
- **`--conflict-coordination=false`.** There are no mounts, so there is no
  socket to report on and no directory to write the asset into.
- **An argv override.** A `--harness-definitions` entry that redefines a
  shipped harness drops the status arguments and the status environment
  exactly as it drops the MCP flag - nothing checks the overridden command
  is still that CLI.
- **`OPENCODE_PURE` in the workspace environment.** opencode loads no
  external plugin at all when that variable is set, Aether's included, and
  Aether does not take it away from you. The run launches and works
  normally; it reports nothing, and is judged on silence like a harness
  with no reporter.

Both assets are server-written, read-only, and live in `/run/aether`, never
in the worktree or the member's synced profile. Each applies for that launch
alone and merges over what the member already has: `--settings` layers one
settings document over Claude Code's own, and `OPENCODE_CONFIG_CONTENT` is
merged into opencode's config with the plugin lists concatenated, so the
member's own plugins still load.

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
shows which credential is active. Credentials land in `~/.claude` and are
excluded from profile sync.

For an API key instead of a subscription, set `ANTHROPIC_API_KEY` in the
server's environment (`/etc/aether/aether-server.env` with the shipped systemd
unit) and skip setup entirely. Note that Aether passes through
`ANTHROPIC_API_KEY` only; a `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`
is not in the passthrough list, so use the terminal for subscription auth.

### Codex

Inside `aether terminal`, run the CLI's login command and choose the
device-code option. Codex writes `auth.json` under `~/.codex`, which is
persisted in your member home and excluded from profile sync.

`OPENAI_API_KEY` in the server environment is the API-key alternative.

### pi

Inside `aether terminal`, start the CLI and use its `/login` command to pick
a provider. Tokens land in `~/.pi/agent/auth.json` under the member home, and
the token files are excluded from profile sync. `ANTHROPIC_API_KEY` or
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
server startup; an invalid member registration is refused at the RPC. Agent
installation, login state, profile sync, and launch definitions remain
separate concerns: installation and login state live in the member home, while
the definition resolves argv for that member. The terminal is the only setup
transport.

## Agent configuration (profile sync)

Separate from logins. Your skills, plugins, custom commands, and settings are
mirrored **one way** from your laptop to the server, so agents on the server are
*your* agents:

```sh
aether profile push --agent claude
aether profile status --agent claude
aether profile rollback --agent claude <snapshot-id>
```

The local daemon (`aether daemon run`) does the push automatically on change;
`--no-profile-sync` opts a machine out. It logs one line per file it left
behind, so an unattended push never drops a file silently. Where
`aether profile push` refuses over a finding in a file you wrote, the daemon
logs that file and syncs the rest, because nobody is there to answer.

The dashboard does the same push without a terminal. Its onboarding wizard has
an **Agents** step, and it runs on the same two guards: for each harness
configured on your machine it shows what a push would carry, grouped as
memory, skills, commands, settings, MCP config, plugins, and other, plus every
file the denylist or the scanner left behind and why. You check the harnesses
you want and approve. Nothing is uploaded to produce that preview, and it
reads nothing until you ask it to - walking a configuration directory that
holds months of transcripts is not instant, so it is a button, and it can be
stopped.

- A push carries at most **1 MiB per file** and **20 MiB per snapshot**.
  Files over either limit are left behind rather than failing the push, and
  the preview, `aether profile push`, and the daemon's log name each one.
  They are decided from the file size alone, so an oversized file is never
  read. An empty file syncs as an empty file.
- The snapshot budget is spent by category, in this order: memory, skills,
  commands, settings, MCP config, plugins, then everything else. Directory
  order would otherwise decide it, and a plugin cache that sorts early would
  crowd out the skills and commands the sync exists to carry.
- Only regular files sync. A socket, named pipe, or device node inside a
  profile root is reported and skipped without being opened - a named pipe
  would otherwise block the read until something wrote to it.
- A **symlink pointing out of the profile root** is skipped and reported,
  not followed. Symlinking `skills/` entries into a shared directory is an
  ordinary setup; the link is left behind and everything else still syncs.
  The target is never opened, so nothing outside the root is uploaded.
- **Third-party plugin content.** `claude` keeps installed plugins in two
  trees: `plugins/cache/<marketplace>/<plugin>/<version>/` is the installed
  copy, and `plugins/marketplaces/<marketplace>/` is the clone of the
  marketplace repository, which carries the plugin sources inline. A plugin
  often ships its own test suite. A scanner finding in either tree is a
  string in a package, not a secret you can edit out of your profile root,
  so it drops that one file and reports it as `vendored-secret` - the rest
  of the plugin, and the rest of your profile, still sync. Both are matched
  on the directory prefix alone, so a plugin update that moves the version
  segment changes nothing. Everywhere else a finding is reported as
  `secret`, and drops that one file too.
- **Default excludes.** Aether skips what a harness writes for itself as it
  runs - transcripts, telemetry, scratch trees - rather than anything you
  configured:

  | Harness | Skipped by default |
  | --- | --- |
  | `claude` | `projects/`, `shell-snapshots/`, `statsig/`, `todos/`, `file-history/`, `history.jsonl`, `daemon/` |
  | `codex` | `tmp/`, `.tmp/`, `sessions/` |

  A skipped directory is reported once, as the directory. These are applied
  before your `.aether-profile-ignore`, so that file has the last word: a
  line `!projects/` in it syncs the directory anyway.

A scanner finding never keeps the rest of a profile off the server. It drops
the one file it named and reports it, so the dashboard lists that file on the
harness row before the import button and you import the rest in one click.

`aether profile push` has no screen to show a finding on before it uploads,
so it refuses while a finding in a file you wrote is unacknowledged, and
prints the path, what the scanner matched, and both ways forward:

```
profile push: the secret scanner flagged a file you wrote. Remove the secret, or say what to do with it:
  skills/deploy/README.md: secret detected (curl-auth-header) at 12:2
    leave it out:   aether profile push --agent claude --skip-secret skills/deploy/README.md
    send it anyway: aether profile push --agent claude --allow-secret skills/deploy/README.md --workspace <workspace>
```

Both flags are repeatable. `--skip-secret <file>` leaves that file out and
pushes everything else. `--allow-secret <file>` carries it. A
`vendored-secret` finding is never counted here - nobody can edit a
secret-shaped string out of a package the harness installed - so it is
reported and skipped with no flag needed. `--allow-secret` carries one of
those too, if you want that file on the server; `aether profile push` prints
the exact command next to each such file.

`--allow-secret` has no dashboard equivalent, deliberately. Removing the
secret happens on the machine the file lives on, and overriding a false
positive stays a CLI act, where `--workspace` records who overrode what, and
on which timeline.

- The synced directory is the harness's profile root from the table above.
- A run **pins** the latest snapshot when it is provisioned. Pushing mid-run
  never mutates a running agent - the next run picks it up.
- The snapshot is materialized in the container as a writable copy. Whatever
  the agent writes there is discarded with the container. **Nothing ever syncs
  back down.**
- **Secrets never sync.** Two independent guards, both on by default: a
  per-harness credential denylist (`.credentials.json`, `auth.json`,
  `.claude.json`, ...) and a client-side content scan that names the file
  and the match. A flagged file is never uploaded unless `--allow-secret
  <file>` names it, which records the override on the workspace timeline.
  It requires `--workspace` outright - no single-workspace default - so the
  override always names the timeline it is attributable on.

## Adding a harness

The registry is one map entry: argv templates for both modes, credential
paths, profile root, denylist, API key passthrough, the optional MCP,
session, and resume flags, and the status reporter - what the harness can
report, the arguments or environment variables that point it at the
reporter asset, and the asset files themselves. An adapter is a separate,
optional file. Both are covered in [adapters.md](adapters.md).
