# Agent harnesses

A **harness** is one agent CLI and everything Aether needs to know to launch
it: how to start it in interactive and headless mode, where its login state
lives, where its configuration lives, and which environment variables carry an
API key. The registry is `internal/harness`, a map and a few functions - not a
plugin system.

Two rules shape everything below:

1. **Aether does not run vendor installer logic.** A member can install an agent
   executable into the workspace's bootstrap staging directory with the
   vendor's documented procedure. Aether snapshots it under `~/.local` and
   makes it available to later runs. An administrator may instead choose a
   custom image that already contains the executable.
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
everything else stays launchable for runs but is never offered there.

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
CLI installed by bootstrap is selected by the active tool snapshot.

## Setting up an agent

Once per person, per agent:

```sh
aether agent add <name> --workspace <workspace>
```

This opens the unified workspace shell in agent-setup mode: tool staging is
mounted writable at `~/.local` and your per-harness credential home is
mounted writable at `$HOME`. For a shipped name the vendor's documented
install command runs first (claude and opencode use the official curl
installers; codex, pi, and amp use npm with `--prefix ~/.local` when the
image has npm) -
these are the vendors' own commands and vendors move them, so a failed
install leaves you in the shell to install into `~/.local/bin` manually. For
other names, install the executable into `~/.local/bin` with the vendor's
documented procedure yourself. Then run the vendor's login flow and `exit`
cleanly. Aether verifies the executable, snapshots the tools (vendor
installers that symlink `~/.local/bin` entries to absolute paths are
normalized to relative links), persists the login state, and - for a name
outside the shipped table - registers a launch definition under your
membership. `aether agent list` shows shipped and member-registered agents.

Launching a shipped or member-registered agent on a neutral-image workspace
before this setup exists is refused up front with the `aether agent add`
command to run, instead of failing inside the run container.

For an unshipped name the command asks for the interactive and headless
launch templates first (`<name> {task}` and `<name> -p {task}` by default;
Enter accepts, or pass `--tui` / `--headless`). `{task}` is replaced as one
argv value, never through a shell. Aether discovers where the login flow
wrote its state by comparing your credential home before and after the setup
shell, so the registered definition mounts exactly those paths into later
runs.

The individual steps remain available when you need only one of them:

- `aether workspace bootstrap <workspace>` re-installs tools without
  touching login state (`--resume` / `--reset` manage a disconnected
  shell's pending staging).
- `aether setup <harness> --workspace <workspace>` re-runs a login without
  reinstalling. The active tool snapshot is mounted read-only at `~/.local`;
  the harness credential home is mounted per its definition.

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

Inside `aether setup claude`, start the CLI and use its `/login` slash command,
which prints a URL to open in your own browser and takes a code back. `/status`
shows which credential is active. Credentials land in `~/.claude` and are
excluded from profile sync.

For an API key instead of a subscription, set `ANTHROPIC_API_KEY` in the
server's environment (`/etc/aether/aether-server.env` with the shipped systemd
unit) and skip setup entirely. Note that Aether passes through
`ANTHROPIC_API_KEY` only; a `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`
is not in the passthrough list, so use `aether setup` for subscription auth.

### Codex

Inside `aether setup codex`, run the CLI's login command and choose the
device-code option - the container has no browser to hand a redirect to. Codex
writes `auth.json` under `~/.codex` (its `CODEX_HOME`), which is exactly the
directory Aether persists and the file profile sync refuses to upload.

`OPENAI_API_KEY` in the server environment is the API-key alternative.

### pi

Inside `aether setup pi`, start the CLI and use its `/login` command to pick
a provider - the OAuth options print a URL and a code you complete in your
own browser. Tokens land in `~/.pi/agent/auth.json` under the directory
Aether persists per member, and the token files are excluded from profile
sync. `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` in the server environment is
the API-key alternative.

### Amp

Inside `aether setup amp`, run `amp login`, which prints a URL to open in
your own browser. Settings live under `~/.config/amp` and secrets under
`~/.local/share/amp`; Aether persists both per member and profile sync
refuses the secret files. `AMP_API_KEY` in the server environment is the
API-key alternative.

### opencode

Inside `aether setup opencode`, run `opencode auth login` and pick your
provider. Credentials are written to
`~/.local/share/opencode/auth.json`, the directory Aether persists per member.

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
server startup; an invalid member registration is refused at the RPC. Tool
installation, login state, profile sync, and launch definitions remain
separate concerns even when `aether agent add` drives them in one shell:
bootstrap installs the executable, login writes credential state, profile
push syncs only the declared profile, and the launch definition only
resolves argv. All shell modes use the `aether-workspace-shell` subsystem.

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
`--no-profile-sync` opts a machine out.

- The synced directory is the harness's profile root from the table above.
- A run **pins** the latest snapshot when it is provisioned. Pushing mid-run
  never mutates a running agent - the next run picks it up.
- The snapshot is materialized in the container as a writable copy. Whatever
  the agent writes there is discarded with the container. **Nothing ever syncs
  back down.**
- **Secrets never sync.** Two independent guards, both on by default: a
  per-harness credential denylist (`.credentials.json`, `auth.json`,
  `.claude.json`, ...) and a client-side content scan that blocks any push
  containing key material, naming the file and the match. `--allow-secret
  <file>` overrides a false positive and records the override on the workspace
  timeline. It requires `--workspace` outright - no single-workspace default -
  so the override always names the timeline it is attributable on.

## Adding a harness

The registry is one map entry: argv templates for both modes, credential paths,
profile root, denylist, API key passthrough, and the optional MCP and resume
flags. An adapter is a separate, optional file. Both are covered in
[adapters.md](adapters.md).
