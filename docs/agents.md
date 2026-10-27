# Set up and drive Aether from a coding agent

This guide is for a coding agent, such as Claude Code, Codex or opencode,
working in a shell on a developer's computer. With it the agent installs
Aether, sets up the server over ssh, and launches and follows runs through the
`aether` CLI. The human keeps every sign-in, approval and secret.

Aether's terms: the **server** is the Linux machine where agents run
(`aether-server`); this computer is a **client** (`aether`). A **workspace** is
one repository on the server. A **run** is one agent working on one task in its
own container, on its own git branch. The **edge** is a relay at
`https://edge.onaether.dev` that the server and clients dial out to, so neither
needs an open port. The [glossary](README.md#concepts) has the rest.

`aether <command> --help` prints any command's usage, flags and an example.
`aether help agents` prints this guide.

## The prompt

The human pastes this into their coding agent:

```text
Set up Aether on my server and this computer, or do this Aether task: <task>.
Read the guide first: run `aether help agents`, or if aether is not installed,
fetch https://raw.githubusercontent.com/3xDevOps/Aether/main/docs/agents.md

Rules:
1. Run `aether status` first, if aether is installed, and do only what is
   missing.
2. Before you change anything, ask me every question you need in one message:
   - the server as user@host, and whether that user is root or has
     passwordless sudo
   - edge or Tailscale
   - with the edge: is a GitHub sign-in enough (account), or must I approve
     each new device (approved-devices)
   - which coding agent, and whether it signs in with a subscription or an
     API key
   - which repository: a path on this computer or a GitHub URL
   - whether runs may use Docker inside them, which installs Sysbox and
     changes /etc/docker/daemon.json on the server
   - CLI only, or the desktop app too
3. Never ask me for a password, sudo password, token or key in chat.
4. Hand every sign-in to me as "open this URL and enter this code", then wait
   for it to finish.
5. Never approve devices, members, invites or run permission requests
   yourself. Tell me the command and let me run it.
6. Ask before anything that costs money or publishes: firewall changes,
   deploy keys, pushes to GitHub, pull requests.
7. If a command fails, stop and show me the real error and the command it
   names. Do not work around a check.
```

## Before you start

- **Human** marks a step only the human does, in their own terminal or
  browser.
- `aether login --json`, `aether github connect` and `aether agent login codex`
  print a URL and a code, then wait until the human finishes or the code
  expires after 10 to 15 minutes. Run them in the background with output to a
  file, give the human the URL and code, and wait for the command to exit 0.
- `aether wait` runs until the run needs something or its `--timeout`. Run it
  in the background too, or give it a `--timeout` shorter than your shell
  tool's limit and repeat it.
- `aether status` prints what this computer is linked to and a `next` line with
  the command for the first missing step. `an admin runs:` or
  `a human runs in a terminal:` before it means it is not yours to run.
  `aether status --json` prints the same as one object.

## Set up

### 1. Install the CLI on this computer

CLI only, into `~/.local/bin`, without `sudo`:

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh -s -- --client --role none --bin-dir "$HOME/.local/bin"
```

With the desktop app too (it downloads Electron and takes a few minutes):

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh -s -- --role client
```

Check with `aether version`. On Windows, follow
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

Add `--sysbox=false` to the `aether-server install` commands below unless the
human allowed Docker inside runs.

```sh
ssh <user>@<host> 'curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh -s -- --role none'
```

With the edge, pass the access policy the human chose:

```sh
ssh <user>@<host> 'sudo aether-server install --edge-url https://edge.onaether.dev --edge-access <account|approved-devices>'
ssh <user>@<host> 'sudo systemctl daemon-reload && sudo systemctl enable --now aether-server'
ssh <user>@<host> "timeout 300 sh -c 'until sudo aether-server edge status >/dev/null; do sleep 5; done'"
ssh <user>@<host> 'sudo aether-server edge claim-code'
```

`edge status` exits 0 when connected and 3 otherwise, while connecting or
when it cannot connect. If the wait times out, run
`sudo aether-server edge status` once more and show the human its
`connection` line. `claim-code` prints the claim code, valid for 30 minutes.

With Tailscale on the server and this computer, leave the edge off:

```sh
ssh <user>@<host> 'sudo aether-server install'
ssh <user>@<host> 'sudo systemctl daemon-reload && sudo systemctl enable --now aether-server'
```

If `install` prints a `warning:` line or `Docker does not answer`, show it to
the human before you go on.

### 3. Sign in and claim the server

With the edge:

```sh
aether login --json
```

**Human:** open `verification_uri` from the first JSON line, sign in with
GitHub and enter `user_code`. The second line reads `"state":"signed_in"`.
Then claim the server, which makes the human its administrator:

```sh
aether link --claim <claim code>
```

It ends with `linked to server <server id> through edge.onaether.dev as <name>
(admin)`. If the code expired, `sudo aether-server edge claim-code` on the
server prints a new one.

With Tailscale, the first link becomes admin: `aether link <host>:2222`. Run
it right after the server starts: until then any SSH key that reaches port 2222
becomes admin, so that port must stay closed to the internet
([networking.md](networking.md#the-first-admin)). Ask the human before you
change a firewall.

### 4. Add the repository

```sh
aether workspace import ~/src/app
aether workspace import https://github.com/acme/app
```

A path imports the clone and pushes its checked-out branch to the server. A
GitHub URL needs GitHub connected first ([Publish](#bring-results-back-and-publish)
shows `aether github connect`). It prints `fetched <branch> at <commit>` and an
`aether workspace mirror adopt ... --yes` command: show the human the commit and
run the command once they confirm it.

### 5. Install and sign in the agent

```sh
aether agent add claude
```

The agent signs in inside the human's environment on the server:

- Codex: `aether agent login codex` prints a URL and a code. **Human:** open it
  and enter the code.
- Claude Code and the others need typed input. **Human:** run
  `aether agent login <name>` in their own terminal.
- With an API key instead, **Human:** run
  `aether workspace env set --secret ANTHROPIC_API_KEY` for Claude Code, which
  reads the key with input hidden. Other agents' keys are in
  [harnesses.md](harnesses.md#use-an-api-key).

Check with `aether agent list --json` (`login_found`) and
`aether account usage`, which asks the vendor whether the login works. Both
look only for a login file. A key set with `aether workspace env set --secret`
writes none, so `login_found` stays false, `account usage` reports the agent
unauthenticated, and `aether status` keeps naming `aether agent login`; go on
to step 6.

### 6. Launch the first run

```sh
aether run "add a health check endpoint" --workspace app --agent claude
aether wait run-d6ay38tj8j --timeout 30m
```

`aether run` prints `run <run-id> running`. If it fails or is interrupted, it
prints the retry with `--idempotency-key`; that retry returns the same run
instead of starting another. `aether run - < task.md` reads the task from
stdin.

## Add a repository

```sh
aether workspace import <path|url>
```

Only an admin can add one. Re-running it for a clone already imported links the
clone to its workspace.

## Work across repos

```sh
aether run "rename the /users endpoint in the API and the web client" --workspace web --repo api --agent claude
```

Each `--repo` workspace is checked out at `/repos/<name>` on its own branch.
`aether pull <run-id>` fetches every repo's branch into the clone linked for
its workspace.

## Add another laptop

Install the CLI there (step 1), then:

```sh
aether login --json
aether servers
aether link <server id>
```

Under `approved-devices`, `aether link <server id>` exits 1 and prints the
`aether device approve` command for this device. **Human:** approve it from the
dashboard's **Needs you** card, or run that command on an approved device; it
asks `y/N`. Then run `aether link <server id>` again.

With Tailscale: `aether link <host>:2222`.

## Add a teammate

Ask the human for the teammate's GitHub login and role, then:

```sh
aether invite --github <login> --role collaborator
```

It prints an invite link once. The teammate runs `aether login`, then
`aether link '<invite link>'`. Under `approved-devices`, **Human:** approve
their device as above.

With Tailscale, the teammate runs `aether link <host>:2222`; **Human:** run the
`aether member approve <member-id>` it prints. Roles and invite codes are in
[teams.md](teams.md).

## Add a coding agent

```sh
aether agent add codex
aether agent login codex
aether agent list --json
```

Sign-in works as in step 5. Agent names: `claude`, `codex`, `opencode`, `pi`,
`omp`, `copilot`, `gemini`, `cursor`, `antigravity`
([harnesses.md](harnesses.md)).

## Follow and answer runs

```sh
aether runs --needs-you
aether runs <run-id> --json
aether wait <run-id> --timeout 30m
aether logs <run-id>
aether message <run-id> "<reply>"
```

`aether wait` exits 0 when the run finished, 1 when it failed, 3 when it needs
you, 4 when it is parked with nothing pending (its turn ended without a
report, or it went silent) and a message continues it, 5 when its wait broke
(a condition resolved and the wake never reached the agent, or its source
went unread for an hour), and 124 on
timeout. `aether runs <run-id>` shows what a run needs, each
with the command that answers it.

A permission request is the human's: show it with its options, and run the
`aether answer <run-id> <request-id> <option-id>` they pick. It allows or
rejects once, never always. Relay a run's question to the human unless the
task they gave you already answers it.

## Bring results back and publish

```sh
aether github connect
aether publish <run-id>
aether close <run-id> --outcome merged
aether pull <run-id>
```

Publish before you close: a finished run needs `aether reopen <run-id>` first.
Publish pushes commits only; if it reports uncommitted changes, ask the agent
to commit with `aether message <run-id> "commit your changes"`, then publish
again.

`aether pull` fetches the run's branch into the linked clone without merging.
The branch gets the agent's work when the run closes, so pull after close.
Ask the human before `aether github connect`: it grants `repo`, `read:org`,
`gist` and `admin:ssh_signing_key`. **Human:** open the URL and enter the code.
Ask before `aether publish`: it pushes the branch and opens a pull request.
`--outcome abandoned` closes a run without its result.

## Update

```sh
aether server update --status
aether update --check --json
```

Update the server before the clients. Ask the human first: `aether server
update` (admin only) restarts the server, and `aether update` replaces this
CLI. Runs keep going through a server update.

## Remember Aether

With the human's consent, add this line to their global agent instructions,
for example `~/.claude/CLAUDE.md` or `~/.codex/AGENTS.md`:

```text
This machine uses Aether; for Aether tasks run `aether help agents`.
```
