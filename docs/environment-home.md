# Member environment home

Each member has one server-owned home directory under `<data>/homes/<member>`.
Aether mounts it read-write as `$HOME` in the member's environment - the
container shell on the dashboard's **Environment** page - and in every run
the member launches, including the run's shell tabs and runs on another
member's shared agent account. Those containers also start from that
member's saved image, or the standard image when none is saved. A share of
your own account exposes the launched agent's login to the recipient's runs
and, when the recipient has no installation of that agent, your
`~/.local/bin`, `~/.local/lib`, and the agent's install directory, read-only;
nothing else of this home. See [teams.md](teams.md#agent-accounts).

## What persists

The home is the member's durable environment:

- Executables installed in `~/.local/bin`
- Vendor login state and other files written by the agent
- Configuration imported explicitly from the browser or edited in **Files**
- The GitHub login written by `gh auth login`, in `~/.config/gh/hosts.yml`
- The commit signing key, `~/.ssh/aether_signing` and `~/.ssh/aether_signing.pub`
- `~/.gitconfig`, which carries the git identity, gh's credential helper, and
  the signing settings
- The remote server an editor installs when it attaches to a run over SSH
  (`~/.vscode-server`, `~/.cursor-server`, `~/.zed_server`), so it is
  downloaded once; see [terminal.md](terminal.md#editors)

Files outside the home live in the container layer. **Save environment** on the
**Environment** page turns that layer into your member image so later runs get it; see
[environments.md](environments.md).

## Automatic tool caches

Aether keeps reconstructible tool caches outside this durable home, at
`<data>/home-caches/<member>/{runs,terminal}/data`, mounted at `/aether-cache`.
The run pool belongs to the immutable launching member, including launches
on someone else's shared account. The environment shell and automatic agent
updater use the separate terminal pool, so an open environment does not pin
all completed-run caches.
Candidate verification also uses that launcher's run pool (the originating
run's immutable home owner for an agent request). Its durable creation key
protects both cache data and legacy home caches through startup and failed
runtime lookup/destruction; only confirmed cleanup releases that ownership.
Verification resolves the container UID:GID before creating its runtime and
grants that user access only to the managed cache, not the persistent home.
Its journal preserves the same home-user reservation across server restart;
conflicting users cannot change cache ownership beneath a live verification.
Malformed published ownership journals stop startup rather than guessing that
the cache is unused; unpublished temporary files cannot own a runtime.

Only tool variables unset by the image, workspace and profile receive defaults:

| Variable | Default |
| --- | --- |
| `npm_config_cache` | `/aether-cache/npm` |
| `PIP_CACHE_DIR` | `/aether-cache/pip` |
| `UV_CACHE_DIR` | `/aether-cache/uv` |
| `GOCACHE` | `/aether-cache/go-build` |
| `GOMODCACHE` | `/aether-cache/go-mod` |

`AETHER_CACHE_DIR` identifies the mount. Explicit tool cache paths remain
unchanged and outside automatic cleanup; Aether does not replace `HOME`,
`PATH` or `XDG_CACHE_HOME` wholesale. Installed CLIs, vendor logins, GitHub
credentials and signing keys remain in the persistent home.

No housekeeping is required. After recovery, hourly, and once before
refusing new work for disk pressure, Aether tries eligible inactive caches,
oldest first. Soft reclaim targets are 4 GiB per pool, 16 GiB total and 7 days
since last use/release. Live, retained, finalizing or uncertain owners always
win over these targets. They are not hard quotas, and no waiting shell or
silent agent is killed for cache space. Failed cleanup and deleted-member
resources retain small server-owned retry metadata outside the writable
mount; normal maintenance retries them even if a partial removal brought the
remaining data below its age or size target. Successful retries clear only
their own diagnostic; image or legacy-cache cleanup cannot clear a pending
cache-data failure. An open environment protects its cache data, not an exited
updater container whose cleanup needs retrying.

Before legacy cleanup, Aether re-reads the current image, workspace and harness
cache settings, including configurations that have never launched or were edited
while idle. Explicit paths inside the resolved container home protect overlapping
allowlisted directories (including configured ancestors or descendants); a
similarly named sibling does not. Protection observed by planning or cleanup is
remembered in server-owned metadata across restart and subsequent configuration
edits. Unavailable configuration or image inspection defers legacy cleanup.
Ambiguous paths or symlink aliases are preserved conservatively, without following
links. These protections do not prevent reclaiming inactive managed cache pools.
Before the first managed cache is created, an absent cache root is an empty
inventory. Existing broken or unreadable roots remain measurement errors.

For homes created before managed caches, cleanup waits until all users of
the old home are gone, then removes only `.npm/_cacache`, `.npm/_logs`,
`.cache/pip`, `.cache/uv`, `.cache/go-build` and `go/pkg/mod`. It never clears
all of `.npm`, `_npx`, `.cache`, `.local`, `.config`, `.ssh`, vendor stores or
arbitrary home files. Symlink targets and external hardlink contents are
not mutated. Saved-image maintenance removes only obsolete exact member
tags, preserving current member, terminal and configured base/browser
references; it does not run broad Docker image or volume prune.


## Setting up an agent

Both dashboards and the CLI list the agents Aether ships and the ones members
define. The launch form, which onboarding's First run step reuses, only offers
agents whose executable is installed in your own
`~/.local/bin` or, when launching on a shared account, in the owner's, since a
run there uses the owner's installation when you have none. You need not
install an agent to launch it on a shared account. With none installed they
say so and offer **Set up an agent** rather than a launch the server would
refuse; the Agents page still lists uninstalled shipped agents so
you can set them up. On a shared account, an agent whose owner has no login
for it is not offered either, nor is your own member-defined agent, which
runs only on your own account.

In either dashboard, **Set up** on **Onboarding → Agent** or the **Agents**
page compares Standard and Enhanced, then **Install <agent>** runs the install
command in this home through `agent.install`, with the pinned adapter when
Enhanced is chosen, and shows the command's output and any failure. Your
environment then opens with the agent's login command typed, and
**Check** reads `agent.list` back: **Installed**, **Enhanced installed**, and
**Login found** when the agent's login file exists in this home. It does not
save the environment image; the home already persists the install.

Discovery follows relative symlinks and absolute links under `/root` or
`/home/aether` within your home. Claude's native installer uses an
absolute link to its versioned executable. Broken links, links outside the
home, and files without executable permission are not marked installed.
The launch dialog reads the agent list each time it opens, so an install in an
open terminal shows up the next time you open it; no app or server restart is
needed.

Choose an agent once:

```sh
aether agent add <name>
```

The command tells you what to run. Open your [environment](terminal.md),
install the agent into `~/.local/bin`, and complete the vendor login there:

```sh
aether terminal
```

`aether agent add <name> --enhanced` also installs the agent's pinned
enhanced-mode adapter into `~/.local` (see
[harnesses.md](harnesses.md#enhanced-mode-adapters)).

After setup, every run you launch sees the same executable and login state.
A member-defined agent also records its launch arguments for later runs.
The terminal command ships in this release series.

Aether keeps a shipped agent installed in `~/.local/bin` current: before a
launch it runs the agent's own update command against this home, at most every
6 hours. See [harnesses.md](harnesses.md#updates-before-launch).

## Connect GitHub

Connect GitHub once so your runs can use your native GitHub credentials to
push branches to a writable checkout Origin, open pull requests, and sign
commits. This also applies when you use another member's shared agent
account; sharing your agent account does not give their runs your GitHub login.
GitHub connection is separate from agent vendor login and edge/server sign-in.
See [credentials and signing](security.md#github-credentials-and-signing-keys)
for the home-sharing boundary and GitHub's requirements for a **Verified**
signature; registering a signing key alone does not guarantee that label.

For admins, use **Settings → GitHub → Connect GitHub**, or **Onboarding →
Repository → From GitHub → Add GitHub repository**. Choose **Copy code and
open GitHub**, enter the code and approve on GitHub, then return to Aether.
Use the visible **Open GitHub** link and copy the code manually if needed.
Completion automatically configures native Git and signing; no terminal,
deploy key, or **I've logged in** confirmation is needed. **Check connection**
is read-only and does not perform that setup.

To import a repository, follow the [browser walkthrough](quickstart.md#github-repository):
choose it, **Review repository**, then **Use repository** to accept the fetched
revision. Later **Settings → GitHub → Add repository** reuses the connection.
Readable private repositories need no repository-admin permission, subject to
organization/SSO policy. For shared source access and its dependence on the
authorizing account, see [workspace source mirrors](security.md#workspace-source-mirrors).

The login runs in your environment, so that container needs
`gh` 2.81.0 or newer - the release that added `gh auth status --json`,
which is how the server reads your login back. The server also needs its
normal Docker runtime, Git, and host `ssh-keygen` for signing setup. The
standard image ships a current gh. Older saved environments may not; see
[No gh in the environment](#no-gh-in-the-environment) below.

Collaborators can use **Onboarding → Agent → GitHub → Connect GitHub**:
the embedded terminal supplies the login command, and **I've logged in**
finishes native setup. For the manual CLI alternative, open your
[environment](terminal.md) and log in there:

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

Admin OAuth completion and the explicit `aether github connect` command both
perform this setup on the server (reading connection status does not):

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
collaborator dashboard flow checks all of this before it prints the login
command, so it shows the remedy instead of a login that container cannot
run. The admin browser connection also reports prerequisite failures before
asking you to authorize.

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

Container root in every run you launch can read and replace the credentials
and the key. Read [security.md](security.md#github-credentials-and-signing-keys)
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

Open **Agent config files** on the **Agents** page or onboarding's **Agent**
step in either dashboard and use **Choose directory**. Import is explicit and repeatable. A known unique basename
automatically selects its **Configuration destination**; an unknown or
ambiguous basename requires a choice. Any destination can be changed before
import. The browser waits for root metadata, then previews paths without
reading file bytes. Changing the destination resets file checkboxes and
recomputes policy from retained handles.

Under **Select files**, uncheck unwanted configuration. **Left out before
upload** shows every local omission and its reason. The server advertises
effective `credential_names` and `runtime_ignores` for each destination.
Credential names match any path component case-insensitively, and `*.pem`
files are always excluded. Runtime paths use exact, case-sensitive,
root-relative component-prefix matching. These exclusions cannot be
re-enabled. `.aether-profile-ignore` is also excluded; its CLI rules do not
apply to browser imports.

Directories have no file-count or aggregate-size ceiling; the browser
transfers bounded batches. Eligible files larger than **64 MiB (67,108,864
bytes)** block confirmation, not the rest of the directory: uncheck them or
choose **Exclude unsupported files**, then **Import configuration**. Known
runtime files are excluded before their size can block import, including
OMP's statistics database and SQLite sidecars. See the
[runtime policy](harnesses.md#agent-configuration-import-and-files).
For a required larger asset, run `aether terminal` and install or download it
directly into the displayed destination in your persistent home. Neither
**Files** nor `aether profile push` is a large-file upload alternative.

Remaining bytes are uploaded and scanned by the server, so secret content is
not guaranteed to stay on the browser machine. Empty files and binary assets
are preserved. The result lists server and local exclusions and explicitly
reports completion with omissions. Failures preserve the original error and
separate confirmed writes, unknown-outcome paths, and failed or unattempted
paths. **Review remaining files** lets you retry a read or explicitly omit a
failure before continuing; it does not replay confirmed or unknown paths.
Inspect **Files** before deliberately reimporting unknown paths.
Navigation preserves the operation and review, but a page reload loses this
in-memory state. Changing members or servers discards the review and stops
further batches; an in-flight request can still finish for its original owner.
New browser-imported files use mode `0644`; existing files retain their current
modes, including restrictive server-side umask modes. Executable mode and
symlinks cannot be represented by the browser. Server-side validation rejects
unsafe paths, symlink components, hardlinks, and nonregular files, while
preserving directory and staged-file ownership.

The import writes the authenticated member's own persistent home. Because that
home is mounted read-write in your environment and in every run the
member launches, imported or edited files are visible immediately, including
to active runs; the agent may need to reload. It is not an isolated per-run
profile, and a share of your agent account does not expose it, except the
`~/.omp/agent` directory an `omp` share mounts. A snapshot
pin is audit metadata, not a private writable copy, and changing configuration
does not rebuild an installed-agent image.

Use **Files** to browse your configuration beside workspace base and live-run
files. Configuration edits are complete UTF-8 text up to 64 MiB; binary and
oversized files are read-only. New configuration files accept nested relative
paths and refuse to overwrite an existing file. Save explicitly with **Save** or
Ctrl/Cmd-S. Dirty tabs remain in memory across routes, and the browser warns
before unloading them. There is no autosave or force-save. A failed or stale
save keeps the draft. Repeat imports also leave open buffers intact;
**Reload from server** deliberately discards the draft and loads remote bytes.
All `config.*` methods require `Launch` and target only the authenticated
member's own home; there is no admin/member selector.

For the separate workspace explorer, base-branch saves are **Commit to
<branch>**, creating one file commit without pushing upstream; live-run saves
modify the uncommitted checkout. Base writes require **Push**, and run writes
require **Steer**. The [Files protocol](local-gateway.md#files-and-member-configuration)
defines revision and concurrency rules.

## Migration

On upgrade, legacy content in
`<data>/homes/<member>/<harness>/<home-relative-path>` is moved to
`<data>/homes/<member>/<home-relative-path>` for known agent names. Empty
per-agent directories are removed. Existing files win if two paths conflict.
Old per-workspace executable snapshots are removed. The migration is
idempotent and does not move files outside the member home root.
