# Set up and drive Cloudfield from a coding agent

This guide is for a coding agent, such as Claude Code, Codex or opencode,
working in a shell on a developer's computer. With it the agent installs
Cloudfield, sets up the server over ssh, and launches and follows sessions through the
`cloudfield` CLI. The human keeps every sign-in, approval and secret.

Cloudfield's terms: the **server** is the Linux machine where agents run
(`cloudfield-server`); this computer is a **client** (`cloudfield`). A **workspace** is
one repository on the server. A **session** is one agent working on one task in its
own sandbox (a container or virtual machine), on its own git branch. The **edge** is a relay at
`https://edge.cloudfield.ai` that the server and clients dial out to, so neither
needs an open port. The [glossary](README.md#concepts) has the rest.

`cloudfield <command> --help` prints any command's usage, flags and an example.
`cloudfield help agents` prints this guide.

## The prompt

The human pastes this into their coding agent:

```text
Set up Cloudfield on my server and this computer, or do this Cloudfield task: <task>.
Read the guide first: run `cloudfield help agents` (use `aether` wherever
`cloudfield` is not found), or if neither is installed, fetch
https://raw.githubusercontent.com/3xDevOps/cloudfield/main/docs/agents.md

Rules:
1. Run `cloudfield status` first, if Cloudfield is installed, and do only what
   is missing.
2. Before you change anything, ask me every question you need in one message:
   - the server as user@host, and whether that user is root or has
     passwordless sudo
   - edge or Tailscale
   - with the edge: is a GitHub sign-in enough (account), or must I approve
     each new device (approved-devices)
   - which coding agent, and whether it signs in with a subscription or an
     API key
   - which repository: a path on this computer or a GitHub URL
   - whether sessions may use Docker inside them, which installs Sysbox and
     changes /etc/docker/daemon.json on the server
   - CLI only, or the desktop app too
3. Never ask me for a password, sudo password, token or key in chat.
4. Hand every sign-in to me as "open this URL and enter this code", then wait
   for it to finish.
5. Never approve devices, members, invites or session permission requests
   yourself. Tell me the command and let me run it.
6. Ask before anything that costs money or publishes: firewall changes,
   deploy keys, pushes to GitHub, pull requests.
7. If a command fails, stop and show me the real error and the command it
   names. Do not work around a check.
```

## Before you start

- **Human** marks a step only the human does, in their own terminal or
  browser.
- `cloudfield login --json`, `cloudfield github connect` and `cloudfield agent login codex`
  print a URL and a code, then wait until the human finishes or the code
  expires after 10 to 15 minutes. Run them in the background with output to a
  file, give the human the URL and code, and wait for the command to exit 0.
- `cloudfield wait` runs until the session needs something or its `--timeout`. Run it
  in the background too, or give it a `--timeout` shorter than your shell
  tool's limit and repeat it.
- `cloudfield status` prints what this computer is linked to and a `next` line with
  the command for the first missing step. `an admin runs:` or
  `a human runs in a terminal:` before it means it is not yours to run.
  `cloudfield status --json` prints the same as one object.

## Set up

### 1. Install the CLI on this computer

CLI only, into `~/.local/bin`, without `sudo`:

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/cloudfield/main/scripts/install.sh | sh -s -- --client --role none --bin-dir "$HOME/.local/bin"
```

With the desktop app too (it downloads Electron and takes a few minutes):

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/cloudfield/main/scripts/install.sh | sh -s -- --role client
```

Check with `cloudfield version`. On Windows, follow
[install.md](install.md#windows-install-script).

### 2. Install the server over ssh

The server needs Linux, Docker Engine and git, and at least 2.5 GiB of
available memory ([install.md](install.md#server-prerequisites)). These need a
user that is root or has passwordless `sudo`. If `sudo` asks for a password,
**Human:** run each command below in their own terminal as
`ssh -t <user>@<host> '...'`.

The first `ssh` to a new server fails without a terminal to confirm its host
key. **Human:** ssh to it once and check the key. Never pass
`StrictHostKeyChecking=no` or `accept-new`.

Add `--sysbox=false` to the `cloudfield-server install` commands below unless the
human allowed Docker inside sessions.

```sh
ssh <user>@<host> 'curl -fsSL https://raw.githubusercontent.com/3xDevOps/cloudfield/main/scripts/install.sh | sh -s -- --role none'
```

With the edge, pass the access policy the human chose:

```sh
ssh <user>@<host> 'sudo cloudfield-server install --edge-url https://edge.cloudfield.ai --edge-access <account|approved-devices>'
ssh <user>@<host> 'sudo systemctl daemon-reload && sudo systemctl enable --now aether-server'
ssh <user>@<host> "timeout 300 sh -c 'until sudo cloudfield-server edge status >/dev/null; do sleep 5; done'"
ssh <user>@<host> 'sudo cloudfield-server edge claim-code'
```

`edge status` exits 0 when connected and 3 otherwise, while connecting or
when it cannot connect. If the wait times out, run
`sudo cloudfield-server edge status` once more and show the human its
`connection` line. `claim-code` prints the claim code, valid for 30 minutes.

With Tailscale on the server and this computer, leave the edge off:

```sh
ssh <user>@<host> 'sudo cloudfield-server install'
ssh <user>@<host> 'sudo systemctl daemon-reload && sudo systemctl enable --now aether-server'
```

If `install` prints a `warning:` line or `Docker does not answer`, show it to
the human before you go on.

### 3. Sign in and claim the server

With the edge:

```sh
cloudfield login --json
```

**Human:** open `verification_uri` from the first JSON line, sign in with
GitHub and enter `user_code`. The second line reads `"state":"signed_in"`.
Then claim the server, which makes the human its administrator:

```sh
cloudfield link --claim <claim code>
```

It ends with `linked to server <server id> through edge.cloudfield.ai as <name>
(admin)`. If the code expired, `sudo cloudfield-server edge claim-code` on the
server prints a new one.

With Tailscale, the first link becomes admin: `cloudfield link <host>:2222`. Run
it right after the server starts: until then any SSH key that reaches port 2222
becomes admin, so that port must stay closed to the internet
([networking.md](networking.md#the-first-admin)). Ask the human before you
change a firewall.

### 4. Add the repository

```sh
cloudfield workspace import ~/src/app
cloudfield workspace import https://github.com/acme/app
```

A path imports the clone and pushes its checked-out branch to the server. A
GitHub URL needs GitHub connected first ([Publish](#bring-results-back-and-publish)
shows `cloudfield github connect`). It prints `fetched <branch> at <commit>` and an
`aether workspace mirror adopt ... --yes` command: show the human the commit and
run the command once they confirm it.

### 5. Install and sign in the agent

```sh
cloudfield agent add claude
```

The agent signs in inside the human's environment on the server:

- Codex: `cloudfield agent login codex` prints a URL and a code. **Human:** open it
  and enter the code.
- Claude Code and the others need typed input. **Human:** run
  `cloudfield agent login <name>` in their own terminal.
- With an API key instead, **Human:** run
  `cloudfield workspace env set --secret ANTHROPIC_API_KEY` for Claude Code, which
  reads the key with input hidden. Other agents' keys are in
  [harnesses.md](harnesses.md#use-an-api-key).

Check with `cloudfield agent list --json` (`login_found`) and
`cloudfield account usage`, which asks the vendor whether the login works. Both
look only for a login file. A key set with `cloudfield workspace env set --secret`
writes none, so `login_found` stays false, `account usage` reports the agent
unauthenticated, and `cloudfield status` keeps naming `aether agent login`; go on
to step 6.

### 6. Launch the first session

```sh
cloudfield session "add a health check endpoint" --workspace app --agent claude
cloudfield wait run-d6ay38tj8j --timeout 30m
```

`cloudfield session` prints `session <session-id> running`. If it fails or is interrupted, it
prints the retry with `--idempotency-key`; that retry returns the same session
instead of starting another. `cloudfield session - < task.md` reads the task from
stdin.

## Add a repository

```sh
cloudfield workspace import <path|url>
```

Only an admin can add one. Re-running it for a clone already imported links the
clone to its workspace.

## Work across repos

```sh
cloudfield session "rename the /users endpoint in the API and the web client" --workspace web --repo api --agent claude
```

Each `--repo` workspace is checked out at `/repos/<name>` on its own branch.
`cloudfield pull <session-id>` fetches every repo's branch into the clone linked for
its workspace.

## Add another laptop

Install the CLI there (step 1), then:

```sh
cloudfield login --json
cloudfield servers
cloudfield link <server id>
```

Under `approved-devices`, `cloudfield link <server id>` exits 1 and prints the
`cloudfield device approve` command for this device. **Human:** approve it from the
dashboard's **Needs you** card, or run that command on an approved device; it
asks `y/N`. Then run `cloudfield link <server id>` again.

With Tailscale: `cloudfield link <host>:2222`.

## Add a teammate

Ask the human for the teammate's GitHub login and role, then:

```sh
cloudfield invite --github <login> --role collaborator
```

It prints an invite link once. The teammate runs `cloudfield login`, then
`cloudfield link '<invite link>'` (`aether link` on Windows). Under `approved-devices`, **Human:** approve
their device as above.

With Tailscale, the teammate runs `cloudfield link <host>:2222`; **Human:** run the
`cloudfield member approve <member-id>` it prints. Roles and invite codes are in
[teams.md](teams.md).

## Add a coding agent

```sh
cloudfield agent add codex
cloudfield agent login codex
cloudfield agent list --json
```

Sign-in works as in step 5. Agent names: `claude`, `codex`, `opencode`, `pi`,
`omp`, `copilot`, `gemini`, `cursor`, `antigravity`
([harnesses.md](harnesses.md)).

## Follow and answer sessions

```sh
cloudfield sessions --needs-you
cloudfield sessions <session-id> --json
cloudfield wait <session-id> --timeout 30m
cloudfield logs <session-id>
cloudfield message <session-id> "<reply>"
```

`cloudfield wait` exits 0 when the session finished, 1 when it failed, 3 when it needs
you, 4 when it is parked with nothing pending (its turn ended without a
report, or it went silent) and a message continues it, 5 when its wait broke
(a condition resolved and the wake never reached the agent, or its source
went unread for an hour), and 124 on
timeout. `cloudfield sessions <session-id>` shows what a session needs, each
with the command that answers it.

A permission request is the human's: show it with its options, and run the
`cloudfield answer <session-id> <request-id> <option-id>` they pick. It allows or
rejects once, never always. Relay a session's question to the human unless the
task they gave you already answers it.

## Bring results back and publish

```sh
cloudfield github connect
cloudfield publish <session-id>
cloudfield close <session-id> --outcome merged
cloudfield pull <session-id>
```

Publish before you close: a finished session needs `cloudfield reopen <session-id>` first.
Publish pushes commits only; if it reports uncommitted changes, ask the agent
to commit with `cloudfield message <session-id> "commit your changes"`, then publish
again.

`cloudfield pull` fetches the session's branch into the linked clone without merging.
The branch gets the agent's work when the session closes, so pull after close.
Ask the human before `cloudfield github connect`: it grants `repo`, `read:org`,
`gist` and `admin:ssh_signing_key`. **Human:** open the URL and enter the code.
Ask before `cloudfield publish`: it pushes the branch and opens a pull request.
`--outcome abandoned` closes a session without its result.

## Update

```sh
cloudfield server update --status
cloudfield update --check --json
```

Update the server before the clients. Ask the human first: `cloudfield server
update` (admin only) restarts the server, and `cloudfield update` replaces this
CLI. Sessions keep going through a server update.

## Remember Cloudfield

With the human's consent, add this line to their global agent instructions,
for example `~/.claude/CLAUDE.md` or `~/.codex/AGENTS.md`:

```text
This machine uses Cloudfield; for Cloudfield tasks run `cloudfield help agents` (`aether help agents` if `cloudfield` is not found).
```
