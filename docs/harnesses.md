# Agent harnesses

A **harness** is one agent CLI and everything Aether needs to know to launch
it: how to start it in interactive and headless mode, where its login state
lives, where its configuration lives, and which environment variables carry an
API key. The registry is `internal/harness`, a map and a few functions - not a
plugin system.

Two rules shape everything below:

1. **Aether never installs an agent.** The agent CLI has to be in the workspace
   image. If `claude` is not on `PATH` in that image, `aether run --agent
   claude` fails at launch with `executable file not found in $PATH`.
2. **Aether never handles your vendor credentials.** Logins happen through the
   vendor's own flow, in a terminal Aether hands you, exactly as on a new
   laptop. Tokens are never extracted, synced, or proxied.

## Shipped harnesses

| `--agent` | CLI | Login state | Profile sync root | API key env | MCP | Resume |
| --- | --- | --- | --- | --- | --- | --- |
| `claude` | Claude Code | `~/.claude` | `~/.claude` | `ANTHROPIC_API_KEY` | yes (`--mcp-config`) | yes (`--continue`) |
| `codex` | OpenAI Codex CLI | `~/.codex` | `~/.codex` | `OPENAI_API_KEY` | no | no |
| `aider` | Aider | `~/.aider` | `~/.aider` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | no | no |
| `opencode` | opencode | `~/.local/share/opencode` | `~/.local/share/opencode` | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | no | no |
| `fake` | a script you name | - | - | - | no | no |
| `custom` | deployment-supplied | - | - | - | no | no |

Paths are inside the run container, relative to the run user's home (`/root`,
or `/home/aether` for a non-root image user).

Only harnesses with an **MCP** column of `yes` can be pointed at the in-container
coordination bridge, so conflict coordination between overlapping runs works for
Claude Code and degrades to the advisory overlap notice for the rest. See
[coordination.md](coordination.md).

The **Resume** column is what a relaunch uses when a server reboot
interrupted the run: the flag rides directly behind the executable and asks
the harness to continue the conversation it last had in the working
directory. It names no session. Every run mounts its checkout at the same
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
persistent server-side PTY you can attach to. `--mode headless` runs its
machine-readable mode. Full-permission flags are applied by default in both -
the agent is in a container, and the container is the boundary
([security.md](security.md)).

| Harness | tui | headless |
| --- | --- | --- |
| `claude` | `claude --dangerously-skip-permissions {task}` | `claude -p --output-format stream-json --dangerously-skip-permissions {task}` |
| `codex` | `codex --dangerously-bypass-approvals-and-sandbox {task}` | `codex exec --json --dangerously-bypass-approvals-and-sandbox {task}` |
| `aider` | `aider --yes-always --message {task}` | `aider --yes-always --no-pretty --no-stream --message {task}` |
| `opencode` | `opencode --prompt {task}` | `opencode run {task}` |

These are the vendors' own flags, and vendors rename them. If a launch fails
with an unknown-flag error, the installed CLI has drifted from the registry -
pin the CLI version in your image, or update the registry.

## Logging in

Once per person, per harness:

```sh
aether setup <harness>
```

That starts a throwaway container from a workspace image, mounts your
persistent per-member home for that harness, and hands you its shell. Run the
vendor's login flow, then `exit`. What you wrote under the harness's credential
path is saved on the server at
`<data-dir>/homes/<member-id>/<harness>/` and mounted **read-write** into every
run you launch afterwards, so token refreshes made inside a run persist too.

Three things to know:

- **The image matters.** `aether setup` borrows a workspace image, so the agent
  CLI must be installed in it. With no workspace at all it fails with
  `scheduler: setup requires an image`.
- **There is no browser in the container.** Use the harness's headless or
  device-code login option - the one that prints a URL and a code you complete
  in your own browser.
- **Logins are per member and shared across that member's runs**, the same way
  two terminals on one laptop share a login. Never across members.

If you skip setup, the agent's own login prompt simply appears in the run's
PTY. Attach with `aether attach <run>` and complete it there; it persists the
same way.

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

### opencode

Inside `aether setup opencode`, run `opencode auth login` and pick your
provider. Credentials are written to
`~/.local/share/opencode/auth.json`, the directory Aether persists per member.

### Aider

Aider is API-key driven; there is no interactive login to complete. Put
`ANTHROPIC_API_KEY` or `OPENAI_API_KEY` in the server environment and they are
injected into aider runs. `~/.aider` is still persisted per member for anything
the tool caches there.

### `fake`

The deterministic test harness. It has no login and no fixed command: the
server reads its argv from the `AETHER_FAKE_AGENT` environment variable at
launch time, so it runs whatever you name - typically a script committed to the
repo, since the run's checkout is mounted at `/workspace`.

```sh
AETHER_FAKE_AGENT="sh /workspace/agent.sh" aether-server serve --data-dir /var/lib/aether
```

This is how the [quickstart](quickstart.md#prove-the-plumbing-without-an-agent-subscription)
proves the whole lifecycle without any vendor account, and it is the harness
the end-to-end tests drive.

### `custom`

A registry placeholder for a deployment-supplied agent command. It declares no
credentials, no key passthrough, and no argv of its own - the argv is expected
to come from the scheduler's harness override map, which the shipped
`aether-server serve` has no flag for. Until that is exposed, `fake` plus
`AETHER_FAKE_AGENT` is the way to run a scripted or in-house agent.

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
  <file>` overrides a false positive and records the override on the session
  timeline (it requires `--session`, so the override is always attributable).

## Adding a harness

The registry is one map entry: argv templates for both modes, credential paths,
profile root, denylist, API key passthrough, and the optional MCP and resume
flags. An adapter is a separate, optional file. Both are covered in
[adapters.md](adapters.md).
