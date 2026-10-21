# Member environment home

Your home is a directory on the server that Aether mounts read-write as `$HOME`
in your environment (see [environments.md](environments.md)) and in every run
you launch, including runs on another member's shared agent account. Agent
installs, logins, GitHub credentials and agent configuration live here. This
guide covers what survives, how to set up an agent and GitHub, and how to edit
files in the home.

A share of your agent account exposes only that agent's login to the
recipient's runs and, when they have no installation of that agent, your
`~/.local/bin`, `~/.local/lib` and the agent's install directory, read-only
([teams.md](teams.md#agent-accounts)).

## What persists

The home survives every stop, save, reset and server restart:

- Executables in `~/.local/bin`. `npm install -g` and `cargo install` write to
  `~/.local` because Aether sets `NPM_CONFIG_PREFIX` and `CARGO_INSTALL_ROOT`
  to it, unless the image, workspace or profile sets them. nvm refuses to
  switch versions while `NPM_CONFIG_PREFIX` is set, so put `unset
  NPM_CONFIG_PREFIX` before nvm's lines in `~/.bashrc` if you use it.
- Agent logins and other files the agent writes.
- Agent configuration, uploaded with `aether config push` or edited in
  **Files**.
- The GitHub login in `~/.config/gh/hosts.yml`, the signing key
  `~/.ssh/aether_signing` and `~/.ssh/aether_signing.pub`, and `~/.gitconfig`
  (git identity, gh's credential helper, signing settings).
- The remote server an editor installs when it attaches over SSH
  (`~/.vscode-server`, `~/.cursor-server`, `~/.zed_server`), so it downloads
  once ([terminal.md](terminal.md#editors)).

Files outside the home live in the environment container. They survive a stop
until **Reset to standard**, and reach runs only after **Save environment**
([environments.md](environments.md#save-the-environment)).

`/tmp` is scratch on disk, not memory. A run's `/tmp` is deleted with the run,
the environment's is emptied each time the environment starts, and **Save
environment** never captures it.

## Tool caches

Package caches live outside the home, at `/aether-cache`, so they never fill
it and Aether can clean them. These variables default there unless the image,
workspace or profile sets them:

| Variable | Default |
| --- | --- |
| `npm_config_cache` | `/aether-cache/npm` |
| `PIP_CACHE_DIR` | `/aether-cache/pip` |
| `UV_CACHE_DIR` | `/aether-cache/uv` |
| `GOCACHE` | `/aether-cache/go-build` |
| `GOMODCACHE` | `/aether-cache/go-mod` |
| `BUN_INSTALL_CACHE_DIR` | `/aether-cache/bun` |
| `YARN_CACHE_FOLDER` | `/aether-cache/yarn` |
| `npm_config_store_dir` (pnpm) | `/aether-cache/pnpm` |

You do not need to clean them. Aether removes the oldest inactive caches
hourly and before refusing work for disk pressure, aiming for 4 GiB per member
pool, 16 GiB in total and 7 days since last use. A cache path you set
yourself is never cleaned. Logins, CLIs and keys stay in the home.

## Set up an agent

[harnesses.md](harnesses.md#set-up-an-agent) has the steps: **Set up** on the
**Agents** page, or `aether agent add <name>` on your computer, which installs
the agent and leaves you in your environment's shell for the vendor login. The
agent, its login and its settings land in your home, so you do not save the
environment afterwards.

The launch form offers an agent only when its executable is in your
`~/.local/bin`. Aether follows relative symlinks, and absolute ones under
`/root` or `/home/aether`, that stay inside your home. Broken links, links
outside the home and files without execute permission do not count. On a shared
account the form uses the owner's installation when you have none, and the
owner must have logged in. With none installed, the form offers **Set up an
agent**.

Aether updates a shipped agent in `~/.local/bin` before a launch, at most every
6 hours ([harnesses.md](harnesses.md#updates-before-launch)). `aether agent
remove <name>` deletes only its `~/.local/bin` links. Packages, login and
settings stay ([harnesses.md](harnesses.md#remove-an-agent)).

## Connect GitHub

Connect GitHub once so your runs can push branches, open pull requests and sign
commits with your credentials. Sharing an agent account does not give the other
member's runs your GitHub login, and the GitHub connection is separate from an
agent's vendor login and your server sign-in.

An admin connects in the dashboard: **Settings → GitHub → Connect GitHub**, or
**Onboarding → Repository → From GitHub → Add GitHub repository**. Choose **Copy
code and open GitHub**, enter the code on GitHub, approve and return. Aether
sets up Git and signing for you. **Check connection** only reads the state. To
import a repository, follow the
[browser walkthrough](quickstart.md#github-repository).

A collaborator uses **Onboarding → Agent → GitHub → Connect GitHub**, which
embeds the terminal with the login command typed in. Press Enter, finish the
device login in your browser, then select **I've logged in**.

### From the CLI

Your environment needs `gh` 2.81.0 or newer (the standard image has one). Open
it and log in:

```sh
aether terminal
```

```sh
gh auth login --hostname github.com --git-protocol https --web \
  --scopes admin:ssh_signing_key
```

`--web` is GitHub's device flow: gh prints a one-time code and a URL, and you
finish in a browser on your own machine. gh may say it could not open a
browser. That is expected in a container. Open the URL yourself. The
`admin:ssh_signing_key` scope lets the next command register your signing key.

Then, from your machine:

```sh
aether github connect
```

```
logged in to github.com as octocat
signing key SHA256:2E3v9x... registered on GitHub
```

This command, and the admin dashboard flow, confirm the `gh` login, write gh's
credential helper and your identity into `~/.gitconfig`, create an ed25519
signing key at `~/.ssh/aether_signing` if there is none, turn on commit signing
(`gpg.format=ssh`, `commit.gpgsign=true`), and register the public key on your
account. It is safe to run again. `aether member git` keeps the identity in
`~/.gitconfig` in step with your name and address.

> [!WARNING]
> Container root in every run you launch can read and replace the GitHub
> credential and the signing key. Read
> [security.md](security.md#github-credentials-and-signing-keys) before
> connecting an account whose reach is wider than this workspace.

### GitHub errors

| Error | Fix |
| --- | --- |
| `github: not logged in to github.com in the environment terminal; run gh auth login there first: HTTP 401: Bad credentials` | Log in again. The line ends with gh's own reason. |
| `github: the gh login on github.com lacks the admin:ssh_signing_key scope; run gh auth refresh -h github.com -s admin:ssh_signing_key in the environment terminal` | Run the command it names. Nothing in the home was changed. |
| `github: the signing key registered on the account does not match this member's key (fingerprint SHA256:... not listed)` | Run `aether github connect` again. |
| `github: gh is not on PATH in the environment terminal; ask a server admin to run aether server update, then run aether terminal stop and open the terminal again: ...` | See [No gh in the environment](#no-gh-in-the-environment). |
| `github: the gh in the environment terminal is too old to check the login: 2.81.0 is the oldest gh that answers auth status --json; ...` | See [No gh in the environment](#no-gh-in-the-environment). |

A missing `ssh-keygen` on the server host refuses the command before
anything is generated. Install the OpenSSH client there
([install.md](install.md#server-prerequisites)).

## No gh in the environment

Three kinds of environment can lack a usable `gh`:

- A saved environment from before the standard image shipped `gh`. Install gh
  in the terminal and save again, or run `aether env reset`.
- A standard image without `gh`. Only an admin can move it, by updating the
  server (`aether server update`, [install.md](install.md#upgrading)). The
  refusal names the exact command for the server's image.
- A `gh` you installed into `~/.local/bin`. It comes first on `PATH` and the
  home survives every image, so remove it with `rm ~/.local/bin/gh` or replace
  it with 2.81.0 or newer.

After either image fix, run `aether terminal stop` and open the terminal
again, because a container keeps the image it started from.

## Upload and edit configuration

An agent's configuration (settings, `CLAUDE.md`, commands, skills, hooks, MCP
configuration) lives in a directory such as `~/.claude`, and runs read the same
path in your home. To copy yours from a computer, open **Agents → Agent config
files**, click **Copy prompt** and paste it into an agent on that computer
([quickstart](quickstart.md#upload-agent-configuration)). That agent uses
`aether config`, which you can also run yourself:

```sh
aether config ls                                # each agent, its directory, what is always left out
aether config ls [-r] <agent> [path]            # what the server has
aether config cat <agent> <path>                # print one server text file
aether config push [--dry-run] [--overwrite] <agent> <path>...
aether config push [--dry-run] [--overwrite] <agent> <path> --from <file>
```

A path is a file or directory inside the agent's directory, relative to it,
and `.` is the whole directory. `--from` uploads `<file>` as `<path>`, for a
copy you edited for the server. `--dry-run` sends nothing and prints the same
lines with `would create`, `would skip`, `would refuse`, `would replace` and
`would leave out`.

A push prints one line per file, then a total:

```text
left out .credentials.json: credential
left out history.jsonl: runtime or history path
left out skills/linked: symlink, not followed
left out skills/deploy/notes.md: secret detected at 0:27 (generic-api-key)
created CLAUDE.md
skipped commands/review.md: identical on the server
refused settings.json: the server has a different file (1108 bytes, 40769 here); --overwrite replaces it
created 1, replaced 0, skipped 1 identical, refused 1, left out 4 in ~/.claude (42 bytes uploaded)
```

- Credential files, `*.pem`, `.env`, `.env.*`, runtime and history paths
  (sessions, caches, plugin caches), symlinks, special files and
  `.aether-profile-ignore` are never read. `aether config ls` lists each
  agent's credential and runtime paths.
- A file the secret scanner flags is left out on your computer, so its bytes
  never leave it. There is no override.
- A file over 64 MiB is left out with `<size> bytes is over the 64 MiB upload
  limit; copy it onto the server from aether terminal`.
- A file the server has with different content is refused and the server's
  copy stays. `--overwrite` replaces it, and applies to every path in the
  command, so name only the files you mean to replace.
- A push deletes nothing and is safe to repeat.

A push with a refused file uploads the rest, then exits non-zero:

```text
aether: refused 1: the server has a different file at each of those paths; read it with aether config cat claude <path>, then push with --overwrite to replace it
```

If the server stops answering mid-push, the files it may not have are printed
as `unconfirmed <path>`, and the error ends with `aether config ls -r <agent>
shows what the server has`. A server older than the CLI fails with `the server
is older than this CLI, and an admin updates it with aether server update`.

The upload writes your own home, so your environment and active runs see it at
once. The agent may need to reload. Nothing watches the local directory: push
again after you change it.

**Files** in the dashboard has a **Home** tree beside the workspace and live
run files. It shows your whole home, dotfiles included. Each row's **…** menu
(or right click) creates, uploads, downloads, renames, copies the path and
deletes. Drag a row onto a folder to move it, or drag files from your desktop
to upload. Deleting, moving and replacing ask first, and there is no trash.

- Nothing is filtered in **Files**. The upload exclusions apply to `aether
  config push` only.
- Credential files show a lock and cannot be opened, edited, downloaded,
  renamed or moved: agent logins such as `~/.claude/.credentials.json`,
  `~/.ssh` except `*.pub`, `~/.config/gh/hosts.yml`, `~/.git-credentials`,
  `~/.netrc`, `*.pem` and `.env*`. You can delete or upload over one.
- Text files up to 64 MiB open in the editor. Binary and larger files are
  read-only. Save with **Save** or Ctrl/Cmd-S. There is no autosave. A failed
  or stale save keeps your draft, and **Reload from server** discards it.

The workspace explorer is separate. Saving a base-branch file there is **Commit
to <branch>** (one commit, not pushed) and needs permission to push. Saving a
live run's file edits its uncommitted checkout and needs permission to steer the
run
([local-gateway.md](local-gateway.md#files-and-member-configuration)).

