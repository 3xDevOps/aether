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
2. **Aether never handles your vendor credentials.** Logins happen through the
   vendor's own flow, in a terminal Aether hands you, exactly as on a new
   laptop. Tokens are never extracted, synced, or proxied.

## Shipped harnesses

| `--agent` | CLI | Login state | Profile sync root | API key env | MCP | Resume | Env setup |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `claude` | Claude Code | `~/.claude` | `~/.claude` | `ANTHROPIC_API_KEY` | yes (`--mcp-config`) | yes (`--continue`) | yes |
| `codex` | OpenAI Codex CLI | `~/.codex` | `~/.codex` | `OPENAI_API_KEY` | no | no | yes |
| `pi` | pi | `~/.pi` | `~/.pi` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | no | yes (`--continue`) | yes |
| `amp` | Amp | `~/.config/amp`, `~/.local/share/amp` | `~/.config/amp` | `AMP_API_KEY` | no | no | yes |
| `opencode` | opencode | `~/.local/share/opencode` | `~/.local/share/opencode` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | no | no | no |
| `fake` | a script you name | - | - | - | no | no | no |
| `custom` | deployment-supplied | - | - | - | no | no | no |

Paths are inside the run container, relative to the run user's home (`/root`,
or `/home/aether` for a non-root image user).

The **Env setup** column marks the harnesses that can set up a workspace
environment for you (for example, building an image that mirrors your own
machine during onboarding). Exactly claude, codex, pi, and amp qualify;
everything else stays launchable for runs but is never offered there. The
same four power later environment edits from the workspace page's
Environment panel; those run on the server, so the chosen agent must be
registered there with `aether agent add`.

Only harnesses with an **MCP** column of `yes` can be pointed at the in-container
coordination bridge, so conflict coordination between overlapping runs works for
Claude Code and degrades to the advisory overlap notice for the rest. See
[coordination.md](coordination.md).

The **Resume** column is what a relaunch uses when a server reboot
interrupted the run: the flag rides directly behind the executable and asks
the harness to continue the conversation it last had in the working
directory. It names no conversation. Every run mounts its checkout at the same
container path and shares one credential home per member, so what is resumed
is that member's *most recent* conversation at that path - which is not
necessarily the interrupted run's own, and not necessarily one from the same
workspace. A harness without a resume flag starts fresh, and a
deployment-supplied argv override never has one appended - nothing checks the
override is still that CLI. Relaunching a run that finished on its own never
resumes. See [failure-handling.md](failure-handling.md).

Only `claude` has a **structured-output adapter** today, so its headless runs
produce typed tool-call and token events. Everything else degrades to the PTY
transcript plus the diff timeline, which is always enough. Adding an adapter is
[adapters.md](adapters.md).

## How Aether launches them

`--mode tui` (the default) runs the agent's native interactive TUI in a
persistent server-side PTY: `aether attach <run>` puts you in it from the
CLI, and the dashboard navigates there automatically on launch. `--mode
headless` runs its machine-readable mode. Full-permission flags are applied by default in both -
the agent is in a container, and the container is the boundary
([security.md](security.md)).

The task prompt is optional in tui mode: launch without one and you land in
the agent's bare interactive TUI, exactly as if you had started the CLI
yourself, and type the first prompt there. Every argv token that carries the
prompt is then dropped, so `opencode --prompt={task}` leaves whole rather than
dangling an empty flag. Headless mode has no interactive surface, so it still
requires a task.

| Harness | tui | headless |
| --- | --- | --- |
| `claude` | `claude --dangerously-skip-permissions {task}` | `claude -p --output-format stream-json --dangerously-skip-permissions {task}` |
| `codex` | `codex --dangerously-bypass-approvals-and-sandbox {task}` | `codex exec --json --dangerously-bypass-approvals-and-sandbox {task}` |
| `pi` | `pi {task}` | `pi -p {task}` |
| `amp` | `amp --dangerously-allow-all {task}` | `amp --dangerously-allow-all -x {task}` |
| `opencode` | `opencode --prompt={task}` | `opencode run {task}` |

These are the vendors' own flags, and vendors rename them. If a launch fails
with an unknown-flag error, the installed CLI has drifted from the registry.
Pin it in an administrator-approved custom image or update the registry. A
member's installed CLI lives in that member's environment home.

## Setting up an agent

Once per person, per agent:

```sh
aether agent add <name>
```

For a shipped name, the command shows the vendor's install script. Open the
environment terminal and run it:

```sh
aether terminal
```

Install the executable into `~/.local/bin`, complete the vendor login in that
terminal, and return to the dashboard. The login and executable are in your
member home, so every container for that member sees them. A member-defined
name also records a launch definition under that member.

For an unshipped name the command asks for interactive and headless launch
templates first (`<name> {task}` and `<name> -p {task}` by default). Install the
executable into `~/.local/bin` using the vendor's documented procedure, then
complete its login.

The environment terminal has no browser. Use the harness's headless or
device-code login option, which prints a URL and a code to complete in your own
browser. The terminal command ships in this release series.

### Setup details

The login commands below run in the environment terminal:

Three things to know:

- **There is no browser in the container.** Use the harness's headless or
  device-code login option, the one that prints a URL and a code you complete
  in your own browser.
- **Logins are per member and shared across that member's runs**, the same way
  two terminals on one laptop share a login. Never across members.
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

### Amp

Inside `aether terminal`, run `amp login`, which prints a URL to open in your
own browser. Settings live under `~/.config/amp` and secrets under
`~/.local/share/amp`; Aether persists both in the member home and profile sync
refuses the secret files. `AMP_API_KEY` in the server environment is the
API-key alternative.

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
behind, so an unattended push never drops a file silently.

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
  read.
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
- **Third-party plugin content.** `claude` installs marketplace plugins under
  `plugins/cache/<marketplace>/<plugin>/<version>/`, and a plugin often ships
  its own test suite. A scanner finding in there is a string in somebody
  else's package, not a secret you can remove, so it drops that one file and
  reports it as `vendored-secret` - the rest of the plugin, and the rest of
  your profile, still sync. It is matched on the `plugins/cache/` prefix
  alone, so a plugin update that moves the version segment changes nothing.
  Everywhere else, a finding still refuses the push.
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

`--allow-secret` has no dashboard equivalent, deliberately. A scanner finding
in a file you wrote refuses the push there and names the file and the line:
the fix is on the machine the file lives on. Overriding a false positive stays
a CLI act, where `--workspace` records who overrode what, and on which
timeline. `--allow-secret` also carries a file the plugin-cache rule dropped,
if you want that one on the server.

- The synced directory is the harness's profile root from the table above.
- A run **pins** the latest snapshot when it is provisioned. Pushing mid-run
  never mutates a running agent - the next run picks it up.
- The snapshot is materialized in the container as a writable copy. Whatever
  the agent writes there is discarded with the container. **Nothing ever syncs
  back down.**
- **Secrets never sync.** Two independent guards, both on by default: a
  per-harness credential denylist (`.credentials.json`, `auth.json`,
  `.claude.json`, ...) and a client-side content scan that blocks any push
  containing key material, naming the file and the match. A flagged file is
  never uploaded, whether the finding refuses the push or - inside
  `plugins/cache/` - only drops that file. `--allow-secret <file>` overrides a
  false positive and records the override on the workspace timeline. It
  requires `--workspace` outright - no single-workspace default - so the
  override always names the timeline it is attributable on.

## Adding a harness

The registry is one map entry: argv templates for both modes, credential paths,
profile root, denylist, API key passthrough, and the optional MCP and resume
flags. An adapter is a separate, optional file. Both are covered in
[adapters.md](adapters.md).
