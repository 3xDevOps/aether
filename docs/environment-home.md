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
- Configuration imported once from the browser or edited in **Files**
- The GitHub login written by `gh auth login`, in `~/.config/gh/hosts.yml`
- The commit signing key, `~/.ssh/aether_signing` and `~/.ssh/aether_signing.pub`
- `~/.gitconfig`, which carries the git identity, gh's credential helper, and
  the signing settings

Files outside the home live in the container layer. **Save environment** in the
terminal turns that layer into your member image so later runs get it; see
[environments.md](environments.md).

## Setting up an agent

The dashboard and CLI list both the agents Aether ships and the ones members
define. The launch form and the onboarding wizard's First run step only offer
agents whose executable is installed in the selected account's `~/.local/bin`,
and with none installed they say so and offer **Set up an agent** rather than
a launch the server would refuse; the Agents page still lists uninstalled
shipped agents so you can set them up.

Discovery follows relative symlinks and absolute links under `/root` or
`/home/aether` within that account's home. Claude's native installer uses an
absolute link to its versioned executable. Broken links, links outside the
home, and files without executable permission are not marked installed.
Use **Refresh agents** after installing in an open terminal; no app or server
restart is needed.

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

The login runs in your environment terminal, so that container needs
`gh` 2.81.0 or newer - the release that added `gh auth status --json`,
which is how the server reads your login back. The standard image ships a
current one. An environment saved, or a standard image pulled, before the
image started shipping gh has none; see [No gh in the
environment](#no-gh-in-the-environment) below.

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

Seven failures have their own messages. An environment whose gh cannot do
the login is refused before the login is asked about at all, naming the
way out and ending with what gh - or the container that could not run it -
actually printed. With no gh at all, on the server's standard image:

```
github: gh is not on PATH in the environment terminal; ask a server admin to run aether server update, then run aether terminal stop and open the terminal again: OCI runtime exec failed: exec failed: unable to start container process: exec: "gh": executable file not found in $PATH
```

Both halves are there because a newer image on the server reaches you only
when you open the terminal again
([environments.md](environments.md#the-standard-image)). Which command the
admin gets depends on the server's image; see [No gh in the
environment](#no-gh-in-the-environment) below.

With a gh too old to answer the check - Ubuntu 24.04 packages 2.45.0 - in
an environment you saved yourself, the way out is yours alone:

```
github: the gh in the environment terminal is too old to check the login: 2.81.0 is the oldest gh that answers auth status --json; install a current gh there and save the environment again, or run aether env reset to remove the saved image and return to the standard one: gh version 2.45.0 (2025-07-18 Ubuntu 2.45.0-1ubuntu0.3)
```

When the image you should be on has already moved - after a reset, or a
server update - the container is the only thing left behind, and the
refusal asks for nothing but `aether terminal stop` and a reopen. A gh that
is on `PATH` but exits non-zero is refused the same way, as `gh in the
environment terminal would not run`, ending with what it printed. The
dashboard's GitHub step checks all of this before it prints the login
command, so it shows the remedy instead of a login that container cannot
run.

Without a terminal login:

```
github: not logged in to github.com in the environment terminal; run gh auth login there first: HTTP 401: Bad credentials
```

The line ends with gh's own reason for that account - `HTTP 401: Bad
credentials` when the token was revoked or expired - or with gh's whole
output when it reported no account at all. A gh that fails the check
outright, rather than reporting on an account, is not a missing login and
does not read as one: `gh auth status exited <code>` ends with what it
printed.

A login made without `--scopes admin:ssh_signing_key` is refused before
anything in the home changes: no key is generated and no `.gitconfig` is
rewritten.

```
github: the gh login on github.com lacks the admin:ssh_signing_key scope; run gh auth refresh -h github.com -s admin:ssh_signing_key in the environment terminal
```

When the **server host** has no `ssh-keygen`, the command refuses before
generating anything, because that binary is what signs Aether's own
commits; install the OpenSSH client package on the server
([install.md](install.md#server-prerequisites)).

`aether member git` keeps `~/.gitconfig` in step: once a signing key
exists, changing your name or address rewrites the identity in the home's
`.gitconfig` too, so the signature and the author stay the same person.

Container root in every run using this account can read and replace the
credentials and the key. Read [security.md](security.md#github-credentials-and-signing-keys)
before connecting an account whose reach is wider than this workspace.

## No gh in the environment

The standard image has shipped `gh` since the v0.2.0-alpha.6 release. Three
kinds of environment can still be without a usable one.

A **saved environment** committed before then keeps whatever was installed
when it was saved. Install gh in the terminal and save again, or drop back
to the standard image:

```sh
aether env reset
```

A **standard image** is the server's, not yours, so only an admin can move
it. Each release publishes its own tag and the server defaults to the tag
matching its own build, so a server still on an image without gh is a
server on a release without gh, and the answer is a server update
([install.md](install.md#upgrading)):

```sh
aether server update
```

That assumes the server takes the default; a pinned or rebuilt
`--standard-image` is refreshed some other way
([environments.md](environments.md#the-standard-image)). Aether picks the
command from the image the server is configured with, so the one to run is
the one in the refusal.

Either way the new image is not yours until you open the terminal again:

```sh
aether terminal stop
```

A **gh you installed yourself** into your environment home is the third,
and no image remedy reaches it: `~/.local/bin` comes first on `PATH` and
the home is kept across every image. Aether names the file it found when
that is what answered, so the way out is to remove it and let the image's
own gh take over, or to replace it with 2.81.0 or newer:

```sh
rm ~/.local/bin/gh
```

## Importing and editing configuration

In the Agents step, choose one local directory with the browser's directory
picker, preview it, and explicitly import it once. The picker recognizes
known basenames such as `~/.claude`, `~/.codex`, and `~/.pi`; an unknown or
ambiguous basename needs an explicit destination. There is no directory
watcher and no AI-generated inventory.

Known credential names in any path component and runtime/history defaults are
skipped locally. Remaining bytes are uploaded and scanned by the server, so
secret content is not guaranteed to stay on the browser machine. Empty files
and arbitrary binary assets are preserved. The limits are **1 MiB per file**,
**20 MiB decoded total**, and **2,000 files**. New browser-imported files use
mode `0644`; executable mode and symlinks cannot be represented by the browser.
Server-side validation rejects unsafe paths, symlink components, hardlinks, and
nonregular files, while preserving directory and staged-file ownership.

The import writes the authenticated member's own persistent home. Because that
home is mounted read-write in the environment terminal and in every run using
the account, imported or edited files take effect immediately, including in
active runs; the agent may need to reload. An account share intentionally gives
another member's run the same home, not an isolated per-run profile. A snapshot
pin is audit metadata, not a private writable copy, and changing configuration
does not rebuild an installed-agent image.

Use **Files** to browse your configuration beside workspace base and live-run
files. Configuration edits are full UTF-8 text up to **512 KiB**; binary and
truncated files are read-only. New configuration files accept nested relative
paths and refuse to overwrite an existing file. Save explicitly with **Save** or
Ctrl/Cmd-S. Dirty tabs remain in memory across routes, and the browser warns
before unloading them. There is no autosave or force-save. A failed or stale
save keeps the draft; **Reload from server** deliberately discards it.
`config.*` always targets the authenticated member's own home, with no
admin/member selector.

For the separate workspace explorer, base-branch saves are **Commit to
<branch>**, creating one file commit without pushing upstream; live-run saves
modify the uncommitted checkout. Base writes require **Push**, and run writes
require **Steer**. Revisions hash the exact original bytes; Aether checks the
revision immediately before rename under its root lock, not against arbitrary
live agent filesystem writers.

## Migration

On upgrade, legacy content in
`<data>/homes/<member>/<harness>/<home-relative-path>` is moved to
`<data>/homes/<member>/<home-relative-path>` for known harness names. Empty
harness directories are removed. Existing files win if two paths conflict.
Old per-workspace executable snapshots are removed. The migration is
idempotent and does not move files outside the member home root.
