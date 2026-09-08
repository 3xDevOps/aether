# Member environment home

Each member has one server-owned home directory under `<data>/homes/<member>`.
Aether mounts it read-write as `$HOME` in the member's environment terminal and
every run using that member's agent account, including the run's shell tabs.
Those containers also start from that member's saved image, or the standard
image when none is saved. An explicit account share permits another member's
run to use both; see [teams.md](teams.md#agent-accounts).

## What persists

The home is the member's durable environment:

- Executables installed in `~/.local/bin`
- Vendor login state and other files written by the agent
- Profile files synced with `aether profile push`
- The GitHub login written by `gh auth login`, in `~/.config/gh/hosts.yml`
- The commit signing key, `~/.ssh/aether_signing` and `~/.ssh/aether_signing.pub`
- `~/.gitconfig`, which carries the git identity, gh's credential helper, and
  the signing settings

Files outside the home live in the container layer. **Save environment** in the
terminal turns that layer into your member image so later runs get it; see
[environments.md](environments.md).

## Setting up an agent

The dashboard and CLI list the shipped harnesses and member-defined agents.
The launch form only offers agents whose executable is installed in the
selected account's `~/.local/bin`; the Agents page still lists uninstalled
shipped harnesses so you can set them up.

Discovery follows relative symlinks and absolute links under `/root` or
`/home/aether` within that account's home. Claude's native installer uses an
absolute link to its versioned executable. Broken links, links outside the
home, and files without executable permission are not marked installed.
Use **Refresh agents** or **Refresh harnesses** after installing in an open
terminal; no app or server restart is needed.

Choose an agent once:

```sh
aether agent add <name>
```

The command tells you what to run. Open the [environment terminal](terminal.md),
install the agent into `~/.local/bin`, and complete the vendor login there:

```sh
aether terminal
```

After setup, every run using that account sees the same executable and login
state. A member-defined agent also records its launch arguments for later runs.
The terminal command ships in this release series.

## Connect GitHub

Connect GitHub once and every later run of yours can push branches to the
workspace's upstream repository, open pull requests, and sign its commits.
Both halves happen in the member home, so no run needs its own credentials.

Open the [environment terminal](terminal.md) and log in there:

```sh
aether terminal
```

```sh
gh auth login --hostname github.com --git-protocol https --web \
  --scopes admin:ssh_signing_key
```

`--web` is GitHub's device flow: gh prints a one-time code and a URL, and
you finish in a browser on your own machine. Nothing is forwarded and no
password reaches the server. gh asks you to press Enter to open the browser,
then reports that it could not open one; that is expected inside a
container: press Enter, ignore the failure, and open the printed URL
yourself with the one-time code. `--scopes admin:ssh_signing_key` is on top
of gh's own defaults, and it is what lets the next command register your
signing key on your account without you pasting it into GitHub by hand.

Then, from your machine:

```sh
aether github connect
```

```
logged in to github.com as octocat
signing key SHA256:2E3v9x... registered on GitHub
```

That one command does five things, all on the server:

1. Checks `gh auth status` for `github.com` inside your terminal container
   to confirm the login took.
2. Runs `gh auth setup-git --hostname github.com` there, which writes gh's
   credential helper into `~/.gitconfig` so `git push` over HTTPS
   authenticates as you.
3. Generates an ed25519 key pair at `~/.ssh/aether_signing` and
   `~/.ssh/aether_signing.pub` if there is not one already, and rewrites the
   `.pub` from the private key every time, so the file it registers always
   belongs to the key it signs with.
4. Writes five keys into `~/.gitconfig`: `user.name`, `user.email`,
   `gpg.format=ssh`, `user.signingkey=~/.ssh/aether_signing`, and
   `commit.gpgsign=true`.
5. Registers the public key on your account with `gh ssh-key add --type
   signing`, then reads the account's keys back with `gh ssh-key list` and
   refuses unless the fingerprint it computed is among them:

   ```
   github: the signing key registered on the account does not match this member's key (fingerprint SHA256:2E3v9x... not listed)
   ```

Re-running it is safe. An existing key is reused rather than replaced, and
GitHub accepts a key it already holds without an error.

Four failures have their own messages. Without a terminal login:

```
github: not logged in to github.com in the environment terminal; run gh auth login there first: HTTP 401: Bad credentials
```

The line ends with gh's own reason for that account - `HTTP 401: Bad
credentials` when the token was revoked or expired - or with gh's whole
output when it reported no account at all.

A login made without `--scopes admin:ssh_signing_key` is refused before
anything in the home changes: no key is generated and no `.gitconfig` is
rewritten.

```
github: the gh login on github.com lacks the admin:ssh_signing_key scope; run gh auth refresh -h github.com -s admin:ssh_signing_key in the environment terminal
```

A saved environment built before `gh` shipped in the standard image answers
that `gh is not on PATH in the environment terminal`; install it there and
save again, or `aether env reset`. And when the **server host** has no
`ssh-keygen`, the command refuses before generating anything, because that
binary is what signs Aether's own commits; install the OpenSSH client
package on the server ([install.md](install.md#server-prerequisites)).

`aether member git` keeps `~/.gitconfig` in step: once a signing key
exists, changing your name or address rewrites the identity in the home's
`.gitconfig` too, so the signature and the author stay the same person.

Container root in every run using this account can read and replace the
credentials and the key. Read [security.md](security.md#github-credentials-and-signing-keys)
before connecting an account whose reach is wider than this workspace.

## Profile sync

Profile sync copies the declared profile files from a member's laptop into the
member home. Credential files are excluded by the harness denylist and content
scan. A profile push affects later containers, not one already running.

## Migration

On upgrade, legacy content in
`<data>/homes/<member>/<harness>/<home-relative-path>` is moved to
`<data>/homes/<member>/<home-relative-path>` for known harness names. Empty
harness directories are removed. Existing files win if two paths conflict.
Old per-workspace executable snapshots are removed. The migration is
idempotent and does not move files outside the member home root.
