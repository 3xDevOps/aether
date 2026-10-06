# Security posture

Operational guidance for people running an Aether server. This file records the
stances that are deliberate, so they are not repeatedly re-raised as findings.

## The agent container

The container **is** the isolation boundary. Aether does not try to build a
second sandbox inside it.

- **Agents run as root by default.** An agent profile may map a run to a non-root
  UID/GID, but the default image runs as root and nothing in Aether forces
  otherwise.
- **Docker's default capability set is retained deliberately.** Agents install
  packages, run build tooling, and use `sudo` in images that ship a non-root
  user. Dropping capabilities, setting `no-new-privileges`, or making the root
  filesystem read-only each break one of those in practice, including Aether's
  own setup-script sentinel, which needs a writable `/tmp`.
- The consequence: **treat container root as capable of anything the container
  can reach.** Security comes from what the container is given, not from
  restrictions applied inside it: the mount policy, the network it can see, and
  the credentials mounted into it.
- **Git trusts the checkout regardless of owner.** Every container with a run
  checkout gets `safe.directory=/workspace` through `GIT_CONFIG_COUNT`,
  `GIT_CONFIG_KEY_n` and `GIT_CONFIG_VALUE_n`, appended after any of the
  workspace's own entries. An unprivileged server owns the checkout on the
  host while the agent runs as root; git's ownership check guards against
  another user's repository on a shared machine, which the container is not.
  Git reads `GIT_CONFIG_COUNT` from 2.31 on; an image with an older git that
  carries the ownership check (Debian 11's 2.30.2, Ubuntu 20.04's 2.25.1)
  still refuses the checkout. The image's own `ENV GIT_CONFIG_*` entries are
  replaced, not appended to: set them in the workspace environment instead.

Each member's persistent home is mounted as `$HOME` only into that member's
own containers: their environment and the runs they launch. An
account share is the one exception, and it reaches only the owner's agent
login, except that an `omp` share reaches the owner's whole `~/.omp/agent`;
see [Account sharing](#account-sharing).

Real names and email addresses cross into the container with the run. The
git identity of the member who launched it is baked into the container's
`GIT_AUTHOR_*` and `GIT_COMMITTER_*` at creation, and
`/run/aether/co-authors` holds one `Co-authored-by: Name <email>` line per
member who messaged the run, readable by the agent like any other file under
the coordination mount. A merged branch credits a real upstream account only
if it carries that account's address.

Each member controls their own address, not the operator. `aether member
git` sets your own identity with no admin check - only setting someone
else's needs the admin role (`internal/sshd/gitidentity.go`) - and the local
dashboard's onboarding wizard asks every new member for one. Setting none
withholds the address alone: the synthetic `<member-id>@aether.local`
fallback credits nobody upstream, but `domain.Member.GitIdentity` still
falls back to the display name, or the member id when that cannot be a git
author name, so a name reaches `GIT_AUTHOR_NAME` and the trailers either
way.

### Account sharing

Sharing your agent account with a member (**Share account** under **Agent
account sharing** in **Profile**, or `aether account share <member-id>`) lets
them launch runs on it from the launch dialog's **Account** picker (or
`aether run --account <your-member-id>`), so they use your agent CLI
subscription. The launcher remains the run owner and actor; usage and cost
are attributed to the selected account.

A run always starts from its launcher's saved image, or the standard image,
with the launcher's home as `$HOME`. Git identity, `.gitconfig`, the gh login,
the signing key, SSH files, shell history, agent configuration (settings,
hooks, MCP servers, history), installed executables (except a borrowed agent
installation, below), imported configuration, terminal image uploads, and the
profile snapshot pin are all the launcher's.
Candidate verification started by a run's agent runs in the home that run's
container mounts, which Aether records on the run when it is launched; a
handoff (`aether handoff`) changes the run's owner but not that home, and
verification does not need the run's container to still be alive. On a shared
account, Aether additionally mounts that agent's login path from the
owner's home over the same path in the launcher's home, read-write. The
**Login state** column of [harnesses.md](harnesses.md#shipped-harnesses) lists
each path; for `claude` it is `~/.claude/.credentials.json`.

When the launcher's home has no `~/.local/bin/<executable>` for the agent,
the run borrows the owner's installation. The owner's `~/.local/bin` and, when
it exists, `~/.local/lib` are mounted read-only at `~/.aether/account/bin` and
`~/.aether/account/lib`, and `~/.aether/account/bin` is appended to the end of
`PATH`. Each install directory the agent declares
(`harness.Profile.InstallPaths`; `~/.local/share/claude` for `claude`) is
mounted read-only at its own path. A borrowed login also gets the
launcher's own copy of the agent's state file the CLI consults before
using a login (`harness.Profile.BorrowedState`; for `claude`,
`hasCompletedOnboarding: true` in `~/.claude.json`, created with mode 0600 or
rewritten in place, never replaced), so the CLI starts signed in rather than
running first-time setup; Claude Code then writes the owner's account email
into that file. Nothing else of the owner's home is
mounted, and `~/.local/share` as a whole never is: opencode keeps its login
there. The owner's `~/.local/bin/<executable>` counts as an installation only
when every link it follows stays inside those mounted directories; a link
into anything else would dangle in the run, so the agent is reported as not
installed instead. When neither home has the executable, nothing is borrowed
and the run starts whatever the image provides.

Two limits follow from mounting the owner's files unchanged. Claude Code's
installer links by absolute path (`/root/...` or `/home/aether/...`), so an
owner who installed as root and a recipient whose image runs as a non-root
user, or the reverse, get a link that does not resolve and the container's
own `not found` error. A recipient whose image runs as a different non-root
uid can only execute what the owner's file modes allow others to; the
read-only mounts are never re-owned.

A borrowed installation means the owner's executables run in the recipient's
container, with the recipient's home, GitHub login, and signing key. Every
executable in the owner's `~/.local/bin` is on `PATH`, after the recipient's
own and the image's, so a recipient who launches without their own
installation trusts the owner's. The mounts are read-only, so the recipient's
run cannot alter the owner's installation. A recipient who installs the agent
in their own `~/.local/bin` runs their own copy and borrows nothing.

`omp` is the exception. It keeps its login in a SQLite WAL database,
`agent.db`, beside `config.yml`, `mcp.json`, its extensions, usage tables, and
`sessions/`, and a WAL database cannot be shared file by file. An `omp` share
therefore mounts the owner's whole `~/.omp/agent` directory read-write, and
omp loads extensions, MCP server commands, and settings from it. That cuts
both ways:

- The recipient's run can plant an extension or MCP server command there that
  runs in the owner's next omp session, inside the owner's environment with
  the owner's whole home, gh token, and signing key. Against a recipient who
  does that, an `omp` share is as broad as sharing your home.
- The owner's extensions and MCP servers run in the recipient's container,
  with the recipient's GitHub login. The recipient's run can also read the
  owner's omp sessions.

Share an `omp` account only with someone you would give your home to.

Only the shipped agents and server-wide definitions
([harnesses.md](harnesses.md#custom-agents)) declare a login path to share. A
member's own definition (`aether agent add`) runs only on that member's own
account; a launch of it on a shared account is refused:

```
scheduler: harness "<name>" is your own agent definition, which runs only on your own account; on a shared account, only a server-wide definition (aether-server --harness-definitions) can declare the login it shares
```

A launch is also refused when a definition's login path is the home itself,
when the owner has no login at any declared path (a missing or empty file is
no login), when any component of that path in the owner's home is a symlink,
and when the login file has another hard link.

What a share still hands over: the recipient's run holds the owner's login
itself, including the refresh token. Root in that run can copy it, and
revoking the share does not recall a copied token; the owner rotates it by
logging out and in again. The login is writable because a token refresh
rewrites it, so a recipient's run can also overwrite it, log out (`/logout` in
Claude Code and `codex logout` revoke the owner's session at the vendor), or
replace it: `/login` in Claude Code or `codex login` in a shared run writes
the recipient's own login into the owner's file. For the file logins
(`claude`, `codex`, `pi`, `opencode`) that is loss of the login, not access to
anything else in the owner's home. For `omp`, see above.

**How the path is mounted.** The owner's own containers control every path
inside the owner's home, so a plain bind of a path in it could be swapped for
a symlink between Aether's check and container start, exposing another
member's home or a host path. Aether instead mounts the login path as a
Docker volume subpath of the owner's home, which the engine resolves beneath
the home at every container start. On Docker Engine 28.0.4, a symlink in the
path that leaves the owner's home is refused at container start (`path
concatenation escapes the base directory` or `cannot access path`), including
one swapped in after the container was created or stopped; a symlink that
stays inside the owner's home is followed. Aether refuses any symlink in the
path at launch, but the owner's own containers can plant one afterwards,
before a start or restart, and so redirect the mount to another file in the
owner's own home, never outside it.

This needs Docker Engine 26.0 or newer (API 1.45) for every container that
mounts a path beneath a member home: runs on a shared account, and every
container that mounts a sharing owner's home - their runs, their environment
terminal, and candidate verification started by them or by their runs. An
older engine ignores the subpath and would mount the owner's whole home, so
Aether reads the engine's API version first and refuses:

```
runtime: docker engine API "1.44" cannot mount a path beneath a member home; that needs API 1.45 (Docker Engine 26.0) or newer
```

A `DOCKER_API_VERSION` below 1.45 in the server's environment is refused the
same way, because requests sent at it ignore the subpath too:

```
runtime: docker client sends API "1.44" (DOCKER_API_VERSION), which cannot mount a path beneath a member home; that needs API 1.45 or newer
```

Aether creates one volume per sharing owner, named
`aether-home-<hash>` and labelled `aether.managed=true`, as a bind of that
owner's home; removing the volume does not delete the home.

**Claude's login file.** Claude Code replaces `~/.claude/.credentials.json` by
rename on every token refresh and writes in place only when the rename fails,
so a refresh in the owner's own container would leave recipients' runs holding
the old file. Once a member has shared their account, Aether therefore also
mounts that file in place in the member's own runs and environment,
creating an empty file when none exists, so every writer updates the one file
recipients' runs hold. The empty file is not a login: until the owner logs in,
a recipient's `claude` launch is refused. A login file with another hard link
is neither mounted in place nor shared. A container the owner started before
sharing, typically the long-lived environment, lacks that mount, so stop it
and open it again. When the environment is running at a first share, or its
state cannot be read, **Profile** says so and offers **Stop environment**;
**Open** on the **Environment** page starts it again. From the CLI: `aether terminal stop`, then
`aether terminal`. In a container with the mount, Claude's `/logout` revokes
the login at Anthropic and reports success but cannot delete the file; the
dead tokens are cleared on the next refresh. Members who share nothing are
unaffected.

**Non-root images.** With a non-root image, a run hands the login path to its
own uid before it starts. For `omp` that is the whole `~/.omp/agent`, so a
large or deeply nested one slows non-root launches on that account, the
owner's own included, but not the rest of the server. The owner's containers are never refused because of
a recipient's run. A recipient's launch is refused while its uid differs from
that of the owner's live containers, or of another recipient's live run, on
that login; the error names `the login <member-id> shares is held by`. The
handover is checked again against those containers when it happens, so a
recipient launch that the owner's container overtakes is refused rather than
taking the login back. If the owner starts a container with a different
non-root uid while a recipient's run is live, the owner's container takes the
login back and that run loses access to it. A profile push or rollback by the
owner (`aether profile push`, `aether profile rollback`) re-owns every entry
under that agent's profile root in the owner's home, the login included, to
the uid:gid of the owner's home directory unless that is the server's own
uid, and a recipient's live run with another uid loses access the same way.

Revoking a grant blocks later launches and reopens. It does not stop an
already-running container or remove the login mounted into it. Stop those
runs before revoking access when immediate removal matters: **Kill run** in
the run header's **More** menu, or `aether kill <run-id>`.

Containers created before shares were narrowed to the login path still mount
the account owner's whole home until they end. They stay supervised, and
reopening one is refused:

```
scheduler: invalid run state transition: run <run-id> predates the narrowed account share, and its container still mounts the account owner's whole home; it stays closed, so launch a new run
```

Stop those runs, before or after upgrading, to end that exposure
immediately: **Kill run** in the run header's **More** menu, or
`aether kill <run-id>`.

This narrows what an account share exposes. It does not change the role
model: a collaborator can still message and control another member's live run
([teams.md](teams.md#roles)) and reach the home its container mounts, and a
handoff transfers a run whose container keeps the home it was created with.
The development terminal and run repository gates still also check access to
the run's account, as before; that check is not what protects a run owner's
home. The Steer capability is. The share boundary is not a per-member
sandbox.

### GitHub credentials and signing keys

Connecting GitHub (`aether github connect`, see
[environment-home.md](environment-home.md#connect-github)) puts two secrets,
and the settings that use them, in the member home on the server:

- **The gh token**, at `homes/<member>/.config/gh/hosts.yml`. `gh auth
  login` runs inside the member's environment container, which has no
  keyring, so gh falls back to writing the token to that file in plain
  text.
- **The signing key pair**, at `homes/<member>/.ssh/aether_signing` (mode
  `0600`) and `.pub`. Aether generates the ed25519 key itself and registers
  only the public half on GitHub with `gh ssh-key add --type signing`.
- **The settings that use them**, in `homes/<member>/.gitconfig`: gh's
  credential helper for `https://github.com`, plus `gpg.format=ssh`,
  `user.signingkey=~/.ssh/aether_signing`, and `commit.gpgsign=true`.

None of it is in `aether.db`, and none of it is in a saved environment
image. The member home is a bind mount, and Docker's commit records only
the container's own layer, never a bind mount, so `aether env save` cannot
capture the token, the key, or the `.gitconfig`. An integration test starts
a container from a saved image with no home mounted and asserts all three
are absent (`TestIntegrationMemberEnvironmentImage`).

The token is in the home because the run itself has to hold the credential:
the agent pushes its own branch and opens its own pull request. Keeping it
out of the container behind a host-side proxy was considered and rejected -
the agent asks that proxy for the same pushes and pull requests, so it has
the same reach by a longer path.

What that grants an agent is the point of the feature, so it is worth
stating plainly. **Container root in any of that member's runs can read and
replace the token, the private key, and `.gitconfig`.** With the token it
can act as the member on GitHub within the token's scopes - `gh auth login`
as documented in [environment-home.md](environment-home.md#connect-github)
requests gh's defaults (`repo`, `read:org`, `gist`) plus
`admin:ssh_signing_key` - so it can push to the workspace's recorded
`origin`, open pull requests, and register or remove signing keys on the
account. That push is HTTPS: gh's
credential helper authenticates `https://github.com` and nothing in the home
authenticates SSH, which is why a `github.com` origin is recorded in its
https form ([teams.md](teams.md#workspaces)).

Replacing reaches further than reading. The home is mounted read-write and
its files are owned by the container user, so an agent can swap the signing
key for one of its own, and Aether then signs its own end-of-run commits
with the replacement; rewrite `.gitconfig`, which decides the credential
helper and the identity commits are made under; and plant `~/.local/bin/gh`,
first on the container's `PATH`, so the next `aether github connect` - or
the `gh --version` the dashboard's GitHub step runs on its own to check
that environment - runs the agent's program instead of gh. An account share
does not extend this reach: a recipient's run has the recipient's own home,
token, and key, never the owner's. An `omp` share is the exception: code the
recipient's run plants in the owner's `~/.omp/agent` runs in the owner's own
omp sessions ([Account sharing](#account-sharing)).

Aether signs its own end-of-run commits with that key while still treating
the home as hostile. It opens the key through a root-confined open on the
home directory, so a symlink planted inside the container cannot lead the
server to a file outside it, and copies the bytes into a private temp file
of the server's own for the length of the commit. `gpg.format`,
`gpg.ssh.program` and `commit.gpgsign` are all passed with `git -c` on the
command line, because the run checkout's `.git/config` is agent-writable:
without pinning them, a planted `gpg.ssh.program` would name a program the
server then runs.

GitHub marks a signature **Verified** only when the commit's committer
address is a verified address on the account that registered the key. So
the agent's own commits, which the container makes as the member, show as
verified only when that member's git email is on their GitHub account: the
synthetic `<member-id>@aether.local` fallback, and any address they have
not added there, read Unverified. Aether's end-of-run commits, which are
committed as Aether, never verify on GitHub. Those still verify locally
against the member's public key with `git verify-commit`, given a
`gpg.ssh.allowedSignersFile` that lists the address and the key. Signing
does not change who the commit is authored as; see
[teams.md](teams.md#attribution).

To revoke: run `gh auth logout --hostname github.com` in the environment
terminal, remove the signing key from
[github.com/settings/keys](https://github.com/settings/keys), and delete
`.config/gh/hosts.yml`, `.ssh/aether_signing` and `.ssh/aether_signing.pub`
from the member home. Then clear the signing settings in the environment
terminal, or every `git commit` inside a run starts failing with `Load key
...: No such file or directory`:

```sh
git config --global --unset commit.gpgsign
git config --global --unset user.signingkey
git config --global --unset gpg.format
```

`aether env reset` does none of this - it forgets the saved image and never
touches the home.

## Remote development and browser isolation

Development terminals, the shared app browser and transient captures require
**Steer**, including reads and captures: an app session can already be
authenticated. Ordinary permission to view a run is not permission to inspect
that app session. The server also rechecks access to the run's backing account.
Local SSH gateways and the server-hosted dashboard use the same checks.

Explicitly retaining selected captures crosses a sharing boundary: retained
bytes and verification notes become ordinary workspace evidence, readable with
**View** under the packet's existing scope and expiry, without live Steer or
backing-account access. Retain only reviewed content before headless completion
or other cleanup. Capture bytes and observation metadata are immutable; the
packet's later retained Git revision does not establish capture-time Git state.
Notes are at most 4096 UTF-8 bytes; retained plus staged captures are bounded to
64 files and 128 MiB per run, with at most 8 MiB per capture.

Retention re-resolves current authority after taking the per-run evidence lock
and before opening each selected source. Immediately before publishing the
packet it rechecks under the shared `authorizationMu` admission gate, which
also serializes account, membership, role and workspace-policy changes. A busy
gate refuses publication with a visible retry-retention conflict and rolls
back staged work; it never silently retries. After an uncertain result, inspect
evidence and retry explicitly with the same key and exact request if needed.

A human member and a run agent are distinct principals. The agent's identity
comes from its run socket, not a member ID in a request. Each app terminal
and browser session has its own writer lease and resource incarnation;
commands carry that lease's generation. A human can explicitly take over,
which fences stale writes. An agent cannot force a takeover. Taking an app
surface does not take the primary agent terminal or release its swarm
hold; primary-terminal control and swarm dispatch retain their own rules.
Held browser keys, buttons and touches are cleared server-side when the
controller releases, loses authority or disconnects, and before replacement
control is admitted. A cleanup failure fences new control rather than handing
the next controller potentially held input. A disconnected observer is not a
controller release.

This is shared-input ownership, **not a restricted execution sandbox**.
An app terminal executes in the live run container, under the run owner's
ordinary home and credentials, the selected account's agent login, and the
container's filesystem and network authority.
An agent or human can still execute native tools outside the managed surface.
Taking a writer lease does not suspend every process already running there.

The Chromium companion has a different policy from the agent container above:

- It runs as UID/GID `1000:1000`, with a read-only root filesystem, all
  capabilities dropped, `no-new-privileges`, private IPC and 256 MiB shared
  memory, bounded memory/CPU, and a private temporary filesystem.
- Chromium runs genuinely headless with its namespace and seccomp sandboxes
  enabled. A scoped seccomp profile permits the required user-namespace
  operations; Docker's stock AppArmor policy remains in place. There is no
  privileged, unconfined, `--no-sandbox`, Xvfb, or host-display fallback.
- Its only bind mount is a private control directory. It cannot mount the
  checkout, member home, signing keys, agent credentials, or Docker socket.
  It joins the run's network namespace, never host networking; this lets it
  reach the app's loopback ports without exposing a browser port on the host.
- Playwright controls Chromium through a debugging **pipe**, not a CDP TCP
  listener. The server brokers typed commands and bounded frames over a Unix
  socket; clients do not receive unrestricted CDP access.

This separation protects member files from browser code; it does not make
pages safe to publish. An app test login may contain cookies, tokens,
customer data, or other secrets. The browser shares the run's network reach.
Use dedicated test accounts, never paste production credentials for recording,
and inspect every selected capture before sharing it. Console warnings/errors,
failed requests, page URLs, and visible page or terminal content can also
contain secrets. There is no automatic public pull-request image upload.
See [privacy.md](privacy.md#remote-development-data) for retention and
[install.md](install.md#headless-browser-companion) for deployment failures.

### Managed selected-path Git commits

These commits execute Git inside the live run environment, using its native
identity and signing configuration and the run's coauthor trailers. Publication
uses one native `git update-ref --stdin` prepared transaction, with a single
dereferencing `update HEAD NEW OLD`. Git compares the branch's object ID with
the expected old value as a native compare-and-swap. After Git acknowledges
`prepare`, Aether verifies symbolic `HEAD` still names the expected branch
while Git holds both the `HEAD` and branch locks, then commits or aborts within
that transaction. A changed branch or HEAD fails closed; Aether does not
emulate the guarantee with its own lock or update whichever branch is current
later. Stock Ubuntu 24.04 Git 2.43 supports this primitive; it does not require
a special Git build or new host upgrade prerequisite.

The selected-path index is isolated while constructing the commit: only the
explicitly selected paths enter the commit, and unrelated staging is preserved.
This is not a snapshot lock over files being edited concurrently: selected
content can change while Git reads it. Updating the selected entries in the live
index after publication is a separate step. A result with `committed=true`
and `index_updated=false` means the commit exists but index reconciliation
failed; inspect the reported error and repository status rather than blindly
retrying the commit.

The native `commit-tree` path deliberately does **not** run commit hooks
(`hooks_run=false`). Use `git commit` in the run terminal when commit hooks
are required. Native `reference-transaction` hooks are not disabled or replaced;
their preparation veto remains effective. Custom environments without the
required native transaction support fail closed, preserving native stderr and
reporting missing protocol acknowledgements rather than falling back to an
unchecked update. Other repository reads remain available. See
[install.md](install.md#git-inside-run-environments) for exact diagnostics.

## Workspace source mirrors

A workspace without a mirror is **local-only**. A configured mirror is an
administrator-only, read-only fetch from its source branch into the
workspace's protected base. It is not the checkout `Origin`: Origin remains
the independent push destination for run branches and pull requests. A mirror
source URL must not contain credentials, query strings, or fragments.

Public mode fetches credential-free HTTPS. Deploy-key mode generates a
dedicated Ed25519 key for each mirror configuration generation. For GitHub,
the operator installs only the printed public half as a repository deploy key
and should leave **Allow write access** off. Generic SSH sources use the
operator-supplied `known_hosts` contents; GitHub uses Aether's pinned
`github.com` host key rather than the server's global `known_hosts`.

The private key is stored on the server below
`<data-dir>/mirrors/<workspace>/private_key.<generation>` with mode `0600` and
a mode-`0700` parent. It is not in `aether.db`, a member home, a saved image,
an RPC result, or the dashboard. The server uses it only for the mirror fetch.
The server administrator and any process that can read the server data
directory can nevertheless copy it, and backups of `mirrors/` are therefore
credential backups. A compromised server can read the upstream with that key;
for generic SSH, the account's server-side permissions define the scope. Use a
repository-scoped, read-only key wherever the provider supports it.

Reconfiguring rotates to a new generation and removes the old local key files,
but it cannot remove a key already installed at GitHub or another provider.
Revoke old keys there, using the GitHub deploy-key settings URL or the
provider's equivalent. Disabling a mirror removes its local key and restores
client-writable base behavior; it also cannot revoke a remote key. Treat a
deploy-key public key and its fingerprint as operational metadata, but never
publish the private key or the `known_hosts` file.

Configuration starts **pending**; it does not fetch until Verify/`refresh`.
Each launch refreshes exactly the configured branch before a run row is
created. `ready` accepts an unchanged or forward-only source, while
`auth-failed`, `offline`, `source-missing`, `rewritten`, `diverged`, and
`error` explain a failed or intentionally held observation. Rewrites and
divergence retain an observed candidate without moving the accepted base.
Every failed refresh blocks that launch and never silently starts from stale
data. When an accepted commit is available, an operator may make one explicit
`--cached-base <sha>` retry; Aether verifies that the protected base is still
exactly that commit and never reuses the cache automatically.

Direct writes to a mirrored base - including `git push aether <base>`,
dashboard **Push now**, `repo.push`, or a daemon base push - are rejected by
the server. Only a forward refresh or an administrator's explicit candidate
adoption can move it. Run branches remain publishable to the workspace, and
`aether pull` remains the safe review path before a human merges locally and
pushes the reviewed branch to checkout Origin.

## Candidate verification and delivery

Candidate operations use the existing workspace capabilities, not a new
integration role. Reading a candidate requires the caller's normal view
authority; preparing, resolving, verifying, requesting delivery, and executing
delivery use the existing **Push** capability. The service resolves the
current member, workspace, run ownership, and candidate state itself. It
rechecks the caller and the approved human approver at the actual delivery,
so an old page, role change, or stale request cannot turn into authority.
`integration.decide` is human-only, and an optional swarm identifier is
context rather than a permission grant. The one delivery without a human
decision is a swarm integrator's: its request is recorded as approved by the
swarm's accountable human, who must hold Push when it is requested and again
when it is delivered, and only while the swarm is active. Cancelling the
swarm stops it.

Verification runs against a server-owned isolated candidate revision and a
disposable verification tree; candidate inputs and retained evidence are not
re-read from a mutable live checkout. The isolation protects the source tree
and post-execution integrity check, not the credentials of the environment it
runs in. A human's verification runs in their own environment; a run actor's
runs in the home that run's container mounts, recorded on the run at launch,
which a handoff does not change; it runs whether or not that container is
still alive. That home's gh login, signing key, and other files are available
to the verification, so do not treat it as a per-candidate credential
boundary.

Delivery to a local workspace target is an expected-old atomic ref update.
Delivery to a mirrored target must use the `proposal` action: it creates a
public `refs/heads/aether/proposal-<request-id>` ref and a private receipt,
without pushing the upstream or moving its protected mirror base. The proposal
is labelled proposed, not landed; a human fetches and pushes it through the
normal upstream review route. The mirror's read-only deploy key is only for
server fetches and is never reused as a delivery credential.

Nothing here blocks native credential use outside Aether. An agent with a
member home can still run its own `git push`, `gh` operation, or pull-request
flow under that member's credentials, subject to the upstream's permissions.
Candidate delivery's Push checks govern only the Aether-managed operation.

See [teams.md](teams.md#candidate-integration) for the operator flow,
[integration.md](integration.md) for the exact wire contract, and
[failure-handling.md](failure-handling.md#candidate-assembly-verification-and-delivery)
for restart and cleanup behavior.

### Hostile agents

If you run agents you do not trust, put the `--data-dir` on a filesystem
mounted `nosuid,nodev`. Docker exposes no per-bind `nosuid`/`nodev` controls, so
without that a root agent can plant a setuid binary through a writable bind
mount and have it survive on the host. See the security note on
`ValidateMounts` in `internal/runtime/mounts.go`.

### Subscription quota reads

The read-only `account.usage` control method makes the server, not the
browser, read native Claude Code and Codex OAuth files from the selected
member home and call fixed vendor HTTPS usage endpoints. This is server-side
token use for status reporting, not credential extraction: Aether never copies
the credential bytes, refreshes or rewrites native OAuth files, or sends tokens
or provider response bodies to clients. API-key logins and unsupported
agents do not become quota collectors. Account selection uses the same
explicit directional grant as launches; administrators do not gain implicit
access, and membership/share authorization is checked before and after the
provider read.
## The dashboard gateways


The dashboard runs over one of two gateways, which share their handlers and
differ only in who they trust (`internal/webgate` is the shared core;
[local-gateway.md](local-gateway.md)). An edge serves no dashboard.

### `aether gui`, on the user's own machine

`aether gui` serves the dashboard from the user's machine and proxies the API
shape over that machine's SSH connection to the linked server
(`internal/localgw`).

- **It binds 127.0.0.1 and nothing else.** There is no exposure flag; the
  listener is loopback or it does not exist. Nothing about the dashboard
  widens what the server listens on. A contributor testing on a phone can
  put the development proxy in front of it on a LAN address, which gives up
  this boundary for as long as that proxy runs; what that costs is spelled
  out in [dashboard-frontend.md](dashboard-frontend.md#testing-on-a-phone).
- **Every request needs a token, loopback included.** HTTP cannot identify
  a member on its own and any local process can reach a loopback port, so
  the gateway mints a bearer token per process (32 random bytes) that every
  API and WebSocket request must carry - as `Authorization: Bearer`, or
  `?token=` on WebSocket handshakes and the initial browser tab. The token
  dies with the process: there is nothing to revoke, and nothing survives a
  restart.
- **The full method map is reachable, not an allowlist.** The identity is
  the member's own SSH key, held by the same process that serves the page,
  and the bearer token never crosses a network or lands anywhere shareable -
  it lives in one process and one local browser tab. A method call carries
  exactly the authority that SSH key already has from a terminal on the same
  machine, and every call still passes the same capability checks the CLI's
  calls do. There is no path by which the browser surface can exceed the
  person sitting at it.
- **`/local/v1` executes with the user's own filesystem and git
  authority** - link config, `git fetch`/`push` on the linked clone,
  systemd user units, scaffold files. That is the point of the surface: it
  does what the CLI does, for the person already at the keyboard.

### The server's own listener, for tailnet devices

With `web-port` set, `aether-server` serves the same dashboard itself
(`internal/servergw`, [networking.md](networking.md#the-dashboard)). It is the
only HTTP listener the server has, and it exists only where a tailnet can
identify its callers.

- **Tailnet addresses only, HTTPS only.** It binds the host's tailnet
  addresses and nothing else, with the certificate tailscaled issues for the
  node's MagicDNS name. There is no cleartext port and no redirect, and a
  server that cannot fetch that certificate refuses to start.
- **Identity is WhoIs, per request, with no token at all.** Every call and
  every WebSocket handshake is resolved through the same tailnet WhoIs
  lookup and member mapping the SSH `none` auth uses. Nothing is issued to
  the browser, so there is no credential to leak, copy, or forget to revoke:
  losing the tailnet loses the dashboard on the next request. A tagged node
  is refused `403`, a failed lookup `503`.
- **A cross-site page cannot act as the member.** With no token, the
  browser's tailnet position is the whole credential, so the gateway refuses
  any request whose `Origin` is not its own host and any `POST /api/v1` body
  not declared `application/json`; a foreign page can neither send the
  simple request that skips the CORS preflight nor pass the preflight, which
  the gateway never answers. WebSocket handshakes apply the same origin rule.
  Both gateways enforce it.
- **Who the tailnet address vouches for.** WhoIs names the owner of the
  node the request came from, so any process on the server host that
  connects to the host's own tailnet address is served as the node's owner,
  usually the admin, with no credential; a device behind a Tailscale subnet
  router arrives as the router node and is served as the router's owner.
  SSH on `:2222` has had exactly the same boundary since tailnet identity
  shipped; the dashboard adds no new one. Loopback, LAN and container
  addresses resolve to nobody and are refused.
- **The same capability checks, run by the same code.** Each identified
  member is served in-process through `internal/sshd`'s `Local` client,
  which runs the handlers an SSH channel runs - pending gating, per-method
  capability checks, the steer check on a shell tab, and the same live
  revalidation. A request carries what that member's SSH session would
  carry, no more.
- **No machine-local verbs.** `/local/v1` does not exist
  here: nothing on the server is the caller's own machine, so there is no
  surface that would act as them on it.
- **The Android app adds nothing to this boundary.** The APK on every release
  ([install.md](install.md#android-app)) is a WebView on one origin. The only
  thing of its own it stores is the server's address, beside the WebView's
  ordinary cache of the dashboard's files and the dashboard's local storage
  of view preferences: no token, no cookie jar it shares with anything, no
  key, and no JavaScript bridge into the app. What leaves
  the phone, and where, is [privacy.md](privacy.md). HTTPS is
  pinned in three places, so there is no way to point it at a cleartext
  listener: the address screen refuses a `http://` URL, the app's network
  security config forbids cleartext for the whole process, and the WebView
  refuses mixed content. Certificate errors are never offered to the user to
  click through. A link or script navigation off the dashboard's origin,
  `target=_blank` included, is handed to the phone's browser instead of being
  loaded with the member's tailnet position behind it, and a scheme that is
  neither - an `intent://` URL that would start another app with page-chosen
  extras - is dropped. WebView does not run that check for a POST, so a form
  on the page could otherwise submit to any origin. Two more gates catch
  that. `shouldInterceptRequest` runs before the request is sent and answers
  an off-origin main-frame request with an empty response, so neither the
  form's fields nor the member's tailnet position reaches the other origin.
  The refused navigation still commits, on that empty document, so
  `onPageStarted` puts the dashboard back; it hands nothing to the browser,
  because anything arriving there was not a link the member tapped.
  Subresources are untouched: those are the dashboard loading its own files.
  Nothing the app stores leaves it through Google's cloud backup or through
  device-to-device transfer: the app opts out of both, naming every domain
  it can store in, because the backup agent walks each one separately. In a
  release build the page's console output is not written to logcat, where
  any app holding `READ_LOGS`, or a connected `adb`, would read it. WebView
  Safe Browsing is turned off in the manifest.
  It matches each URL against a hash-prefix list held on the device and, on a
  match, asks Google about that 4-byte prefix - never the URL or the host. On
  a WebView that only ever loads the member's own server it protects nothing,
  because every other link goes to the phone's browser, which runs its own
  check; off is the setting under which nothing about a navigation reaches
  Play services at all.

### Both

- **The SPA files are served without identity.** The bundle is not secret and
  has to load before it can present anything; everything behind `/api/`,
  `/ws/` and `/local/` is gated.
- **Live sockets are re-checked by the server, not by the transport.** The
  server re-runs its capability checks on every call and on each subsystem
  channel, and re-checks live attach, terminal, event and sync channels
  every few seconds. A write attach that loses the steer capability is
  dropped exactly as a CLI attach would be - the terminal view falls back to
  a mirror - and a member removed or set back to pending loses every open
  channel within that interval. On the local gateway the token cannot be
  revoked out from under a socket, because it lives and dies with the
  process serving it. On the server gateway a socket keeps the member it
  was opened as; the tailnet is asked again on the next request or
  reconnect, and the server's revalidation is what ends an open socket
  when the membership itself is removed or set back to pending.
- **Every socket is pinged every 30 seconds** and closed when the pong does
  not arrive within 10. A phone that changed networks or went to sleep
  leaves a half-open connection that reads as live on both ends; without the
  ping it would keep holding a PTY client whose geometry clamps every other
  viewer.

### Browser configuration imports

The shared Configuration importer prepares user-selected file metadata only
after `config.roots` returns and a destination is known. The root's
`credential_names` combines shared denials and the agent's `DenyNames`;
names match any component case-insensitively. Those names and `*.pem` files
are filtered before browser reads. A missing credential list fails closed.
Runtime/history paths use the selected root's `runtime_ignores`. Neither list
can be overridden by file checkboxes or `.aether-profile-ignore`, which is
itself excluded. The server independently enforces policy and scans the
remaining uploaded bytes.
Unknown or ambiguous basenames require a destination choice. Changing any
destination recomputes metadata from retained `File` handles without reading
bytes and resets checkboxes. Generation guards prevent a preview for one
destination from being submitted to another. Both dashboards can read a
directory explicitly chosen through the browser picker, not arbitrary paths.

### Terminal image uploads

`terminal.image` accepts image bytes, not a client path. The dashboard sends
only the bytes in a user-selected browser `File` (including an actual image
`File` from native paste); a text clipboard value that happens to be a local
path remains text. There is no RPC that asks the server to read an arbitrary
client path, and the upload action only inserts the returned shell-quoted path
into the focused terminal - it does not press Enter or run the command.

The web gateway permits a 12 MiB request for `terminal.image` to leave room
for base64 and JSON framing. The server validates the decoded bytes as a
non-empty PNG, JPEG, GIF, or WebP image no larger than 8 MiB, then writes a
generated `.aether/terminal-images/image-<random>.<ext>` file with mode `0600`
in the persistent member home the target container mounts as `$HOME`, not in a
workspace checkout or source tree. The returned absolute path is the path visible inside the target
container at its `$HOME`; the client cannot choose the destination or filename.
An upload with no `run_id` targets the authenticated member's running
environment. A run target requires `Steer` and writes into the home
that run's container mounts: its launcher's, also on a shared account.

These files follow member-home retention: stopping or resetting an environment
does not remove the home, so images remain until they are removed from that
home or the member is deleted. A member-home bind mount is not part of
`env.save`'s Docker image, so terminal images are not copied into the saved
environment image. Like every file in that home, the images are readable in
each container that mounts it: the member's environment and the runs
they launch.

## Browser configuration and Files

**Agents > Agent config files** provides explicit, repeatable browser
directory import on both local and server-hosted dashboards when `config.roots`
and `config.import` are advertised. Onboarding's Agent step shows the same
importer, and the command palette reaches it through **Agents**; no workspace
or onboarding progress is required. After a result,
the user can select another directory or choose **Open remote files** to visit
the existing **Files** editor. The browser waits for `config.roots` and a known
destination before previewing or reading bytes. A known unique basename selects
its destination automatically; an unknown or ambiguous basename requires an
explicit choice. Credential names in any path component and `*.pem` files are
always skipped before upload. Runtime/history paths come from the selected
root's `runtime_ignores` metadata and match exact, root-relative paths or
component prefixes case-sensitively after trailing slashes are trimmed.
Changing the destination recomputes the preview from retained browser `File`
handles without reading their bytes. Users can uncheck eligible paths; the
preview lists every local omission and its reason. No directory-wide count or
byte ceiling silently discards files. The browser reads and encodes one
bounded batch at a time, targeting 20 MiB decoded and at most 2,000 files;
a larger individual file travels alone. Requests permit 64 MiB decoded and
each file has a 64 MiB ceiling. Oversized eligible files block confirmation
until explicitly omitted; invalid paths and destination collisions fail
before any upload. None of these controls bypasses server validation.
Owner-scoped progress and results survive dashboard navigation. Identity changes
discard preparation and prevent subsequent batches, including after an awaited
file read. An already submitted request may finish for its original owner;
its result is never shown to the new identity. There is no watcher, automatic
configuration synchronization, or automatic import retry.

The browser reads accepted regular-file bytes and sends them to the server,
where they are scanned before writing; a secret finding is therefore not proof
that the content stayed local. Empty files and arbitrary binary regular bytes
are preserved. Server exclusions remain visible alongside local omissions.
A failed or interrupted batch stops further requests. The result separates
confirmed writes, failed/unattempted paths, and a submitted batch whose
outcome is unknown. The server reports exact committed paths and exclusions
even when a preflight failure confirms zero writes. Recovery reviews only
failed/unattempted paths; it never automatically replays confirmed or
unknown-outcome paths. A read failure can be retried or explicitly omitted.
Original errors remain available after recovery. A page reload loses the
in-memory review and result; inspect Files before a new explicit import.
The shared HTTP gateway permits a 96 MiB request for `config.import`,
385 MiB for `config.write` and `files.write`, and 1 MiB for ordinary methods.
Decoded file and import-request limits remain authoritative. The authenticated
SSH control channel caps a JSON line at 96 MiB; the larger framing budget does
not remove decoded import bounds or filesystem validation.
Before reading an import body, each HTTP gateway admits at most two imports;
admission lasts through backend processing and the response. Excess requests
receive HTTP 503 without their bodies being read. Body reads expire after
30 seconds without progress or 15 minutes total and return HTTP 408.
HTTP admission, body, and JSON-validation refusals carry
`error.data.config_import_not_started: true` before backend dispatch,
so the importer can report failed/unattempted paths rather than unknown writes.
A status code or message alone is not no-write evidence: backend or transport
errors, including HTTP 503 without that marker, retain an unknown outcome.
SSH control frames that grow beyond 64 KiB require admission: at most two
globally and one per member, held through dispatch and the response. Small
control requests do not consume those slots. Partial-frame reads have a
30-second idle timeout and a 15-minute total timeout. Expanded frames retain
the total timeout through dispatch and response; complete small requests do not.
Expiry closes the offending SSH connection, including its other channels, so
a peer cannot retain the large buffer by ignoring channel closure. Idle
channels between frames do not start a frame timer.

Browser metadata is intentionally limited. New imported files are `0644`;
existing modes are preserved even when the server uses a restrictive umask.
The browser cannot preserve executable mode or symlinks. The server rejects
unsafe paths, symlink components, hardlinks, and nonregular destinations,
and retains directory and staged-file ownership. Every `config.*` method
requires the `Launch` capability and targets only the authenticated member's
own home; an admin cannot select another member or account.

The imported and edited files are in the member's shared read-write home,
mounted into that member's environment and the active and future
runs they launch, including runs on a shared account. A member's account
share does not expose them, except the `~/.omp/agent` directory an `omp`
share mounts; otherwise only the agent login path is shared
([Account sharing](#account-sharing)). The home is not a per-run isolated
configuration copy. A snapshot pin records
optional launch provenance, not an isolation boundary or a promise that home
edits wait for later runs. Browser imports and Files edits do not create or
update CLI snapshot history; the HOME persists independently. Manual profile
push and rollback overlay snapshot files into that same HOME and leave paths
absent from the snapshot untouched. Rollback is not an exact-tree restore.
Files edits do not rebuild the installed-agent image.

The Files editor accepts complete UTF-8 text without NUL bytes up to 64 MiB.
Binary and oversized files are read-only. Run and configuration saves recheck
SHA-256 revisions immediately before atomic rename under Aether's root lock;
base-branch commits compare-and-swap the branch head. These locks do not
exclude arbitrary live-agent filesystem writers. A stale or failed save leaves
the browser draft available. Repeat imports do not replace open editor buffers;
**Reload from server** explicitly discards a draft and reads remote content.

## SSH port forwarding

Port forwarding is limited to direct-tcpip channels whose destination is
`run:<run-id>` or exactly `terminal`. Run targets require the Steer capability
for the authenticated member; the terminal target resolves that member's own
live environment container and requires current membership. The server
revalidates that authorization while the channel is open and closes the tunnel
when membership or Steer is withdrawn. It resolves addresses itself and dials
only the requested container port. Arbitrary hosts and ports are not targets,
and reverse forwarding is disabled: global forwarding requests are denied.

The local dashboard recognizes OAuth authorization links whose redirect URI is
an HTTP loopback address. It binds the matching local callback port before
opening the authorization page, then forwards that port only to the terminal
where the link appeared. Other links keep the normal browser behavior.

## Edge remote access

The edge ([edge.md](edge.md)) is an identity broker, not an authority. The
server believes it about who signed in; whether that admits a new device is
the server's access policy, and membership, role and device status are
always the server's own. [edge.md](edge.md#what-an-attacker-can-do)
tabulates what a taken-over GitHub account and a compromised edge
can do under each policy, with the tests that show it; the reasons follow.

- **A server uses an edge only when its operator chose one.** `edge-url` is
  empty unless the config file or a flag names an edge. `aether-server
  setup` is the only command that sets it without being told: on a host
  without tailscaled it turns on the project's edge and prints what that
  edge sees and how to turn it off; with tailscaled it asks, defaulting to
  no. An upgraded server whose config never named an edge stays off it and
  discloses nothing (`TestIntegrationUpgradeFromMainWithoutTheEdge`).
- **The server trusts an edge by its signing key, not its host name.** It
  keeps one pinned edge key, whatever `edge-url` names, and its owner per
  edge key. A new host name of the same edge keeps both. A different key,
  at any host name, is refused until `sudo aether-server edge trust` pins
  it after a person compared fingerprints; the server then has no owner
  for that key and reports itself ownerless to an edge that records one.
  Changing `edge-url` alone never makes the server trust another key.
- **SSH is end to end.** The relay splices bytes between two outbound
  WebSockets and never holds a key that could decrypt them. Clients check
  the host key against the server id on every path, so an edge cannot pose
  as a server.
- **Grants are checked by the server.** Each relayed connection carries a
  grant signed by the edge key the server pinned at enrollment. The server
  checks the signature, its own id, the connection id and a 60-second
  lifetime, and refuses a connection id it has seen. The key the client
  offers must be the grant's device key, so a stolen device token alone
  authenticates nothing.
- **Membership stays on the server.** The server maps the account to a
  member itself and checks the device. A compromised edge can forge a grant
  for any account, but that account gets in only as a member or through an
  open invitation the server holds. The SSH user name of a relayed
  connection names the account the device signed in as, under the device's
  own signature, and the server refuses a grant naming another before it
  records anything; so an edge cannot file the key of a device it relays
  under another account unless it also lied to that device at sign-in. Under `approved-devices` a grant for an
  invited account changes nothing on the server but a device waiting on
  the invitation: no member, role or bound account exists, and the
  invitation stays open, until a person approves that device. Approving it
  creates the member and uses the invitation in one transaction; the other
  devices waiting on it are deleted.
- **The access policy decides what a grant is worth.** Under `edge-access
  account`, a grant for a member's account admits a new device of that
  member: an attacker gets in when either the member's GitHub account or
  the edge is taken over. Under `approved-devices`, every new
  device key, a member's first included, waits until a person approves it:
  the member from an approved device, SSH key or tailnet connection, an
  admin, or `sudo aether-server device approve <code>` on the server. An
  attacker then also needs an approver to type the code their device
  shows. The policy is set only on the server's host, and a missing
  setting means `approved-devices`. Under either policy a device that only
  signed in cannot mint an invite code, a bearer credential that would
  outlive a switch to `approved-devices`. Neither policy assumes OAuth is
  weaker or SSH keys stronger; they differ in how many parties must fail.
- **An approved device key is a credential on its own on the direct
  path.** The direct SSH port admits approved device keys, under both
  policies, without asking the edge, as it admits a member SSH key.
  Through the edge the key also needs the device's token or the edge.
- **Approval codes are typed, not listed.** The code is derived from the
  device key and shown to that device inside SSH; `aether device list`
  never carries it, so approving proves the approver was handed the code
  of the device in front of the person. Approve only a code read from a
  device you are holding. Two keys can derive one code; such a code
  approves neither, and `sudo aether-server device review` approves by
  choosing the device instead. An approval from a connection that signed
  in with an unapproved device is refused under either policy.
- **An approver sees what a code admits before approving.** The member a
  waiting device belongs to follows from the account it signed in as,
  which the edge vouches for, so a compromised edge, or someone holding a
  member's GitHub account, can make a code admit their own key as that
  member. `aether device approve`, `sudo aether-server device approve` and
  the dashboard first look the code up (`member.device.lookup`), show the
  device, the account, and the member and role approving admits it as, and
  approve only after a yes; `member.device.approve` takes the device id the
  lookup returned and refuses a code that names another. Someone handed a
  code that admits their own member, or an admin, should refuse it.
- **Waiting devices are bounded.** A relayed connection with a new key
  records a device. The server keeps at most 10 pending devices per account
  and 10 per invitation, and refuses more with a banner naming `aether
  device list` and `sudo aether-server device review`.
- **Enrollment signatures cannot become host signatures.** The host key
  signs `aether-edge-enroll-v1\x00`, the edge origin, the server id and a
  nonce. That message is longer than any SSH exchange hash, so an edge that
  collects it cannot use it in a handshake.
- **Claims travel inside SSH.** `aether link --claim` opens a claim
  connection through the edge and offers the code, with the account this
  device signed in as, in the SSH user name. An SSH client sends the user
  name only after the key exchange has checked the host key, and the
  client accepts only a host key that derives the server id in the code,
  so the code reaches that server encrypted and nothing else. The client's
  SSH signature covers the user name, and the server refuses a claim whose
  grant names another account before it tries the code. The edge relays
  the bytes without reading them; to take a claim it would need the code,
  which only the server's console printed. An edge that also lied to the
  client about who signed in could make the claim for its own account:
  the person then has to notice the account shown by `aether login`,
  `aether link --claim` and the dashboard's claim form, which is the one
  the edge reported, before the code is sent.
- **Console recovery restores an existing admin and nothing else.** `sudo
  aether-server edge claim-code --admin <member id>` issues a code whose
  claim binds the claiming account to that admin and approves the claiming
  device. The code is refused for a member who is not an admin, when issued
  and again when used; it creates no member, raises no role, and cannot
  move an account that is another member's. The server logs each use.
- **One account of a member can be removed.** An account linked to a
  member while someone else held it is unlinked with `aether member unlink`
  by the member or an admin, from a credential a person approved: the
  devices that signed in with it are revoked, not deleted, so their keys
  stay refused if the account is linked again.
- **The first link decides which server a client pins.** Every later
  connection must present a host key that derives the linked id. An id
  from the server's admin, or from `sudo aether-server edge status`, rules
  out being sent elsewhere. An id from `aether servers` comes from the edge,
  and an edge that lists a false id can send that first link to another
  server; `aether link --from-edge` and the wizard show the id and host key
  fingerprint and ask before they pin it.
- **Device tokens go to two origins only.** A token is sent to the relay
  origin it is stored under and to the sign-in origin that issued it, never
  to an origin the edge's metadata names later, and the client follows no
  redirect.
- **The edge stores bearer secrets hashed** (device tokens, session
  cookies, device codes) and rate-limits sign-in, device
  codes and claim connections per address; claim connections also per
  account and per server. An IPv6 address counts against its /64,
  its /56 (4 times the budget) and its /48 (16 times), so one allocation
  cannot spend more than its /48's share; a full limiter evicts the block
  with the most budget left instead of refusing new addresses. Device-code
  entry is also limited per account. The address a relayed connection came from is for
  logs and rate limits only, never for authentication. Behind a reverse
  proxy the edge reads the address from the right-most `X-Forwarded-For`
  entry, and only when the connection comes from a loopback address. With
  `--trusted-proxies` it takes the right-most entry outside the named
  networks, only from a peer inside them, and refuses every other peer,
  loopback included; a list that trusts every address is refused. A
  request whose header is missing or not an IP address is refused. The
  packaged nginx configuration sets the header to the address nginx saw,
  discarding the client's.
- **Sign-in and relay are two host names.** The sign-in cookies are
  `__Host-` cookies with no `Domain` attribute, bound to the sign-in host
  alone, and each host refuses the other's paths. A server's ownership is
  recorded only from that server's own report on its control connection,
  for the account whose claim connection presented the code; no client
  request or operator command records an owner or approves a device.
- **Deleting an account needs a fresh sign-in in the same browser.** The
  edge deletes an account only on its Account page, from a browser that
  itself signed in with GitHub in the last 5 minutes, with the login typed
  back. A device token cannot delete it, and neither can a
  session cookie from a browser that has not signed in since, even after
  the person signs in elsewhere. Deleting takes the person's GitHub
  sign-in, or a cookie stolen within 5 minutes of its sign-in.
- **Invitations match only a recently confirmed login or email.** The edge
  learns an account's GitHub login and verified email only when that person
  signs in with GitHub in a browser. Signing in takes the login from
  any other account that held it, but someone who renamed on GitHub, or
  whose email moved to another account, keeps the old value until they
  sign in again. So an invitation matches an account only within 24 hours
  of its last sign-in: the edge refuses such an account with `your login
  and email were last confirmed over 24 hours ago; open this edge in a
  browser to confirm them, then retry`, any edge page opened later asks
  GitHub again, and the server checks the same age in the grant, which
  carries when GitHub last confirmed the account. Within those 24 hours an
  old login or email can still match. Members match by GitHub's immutable
  user id and are unaffected.
- **Recovering a GitHub account removes nothing at the edge or on a
  server.** Device tokens do not expire, so devices signed in while the
  account was taken over keep working until revoked
  ([edge.md](edge.md#recovering-a-github-account)).
- **Only self-hosted servers exist.** The protocol reserves a `hosted`
  kind for a workspace Aether would operate. Such a workspace would not be
  protected end to end against Aether's operators, who would hold its host
  key and data ([edge.md](edge.md#trust-models)).

What the edge operator does see: server ids and host names, account
identities, device labels, client addresses, timing and byte counts
([privacy.md](privacy.md#what-an-edge-stores)).

## Conflict coordination

When two runs edit the same file, each container may receive a run-scoped unix
socket for coordination. Detail is in `docs/coordination.md` (host side and
wire) and `docs/mcp-bridge.md` (the optional in-container bridge); the
operator-facing stances are these.

- **Binary availability is not run identity.** The canonical
  `/usr/local/bin/aether-internal` CLI is an Aether-provided, version-matched
  executable available in managed containers, and the staged server binary
  may also be present for the optional bridge and lifecycle plumbing. Neither
  path authenticates a caller. The socket at `/run/aether/coord3.sock` is the
  run identity: a connection accepted there is treated as that run. An
  identity-less environment or container can use general help or
  non-run skill guidance, but status, messaging, reporting, and swarm
  operations are unavailable.
- **The mount is the authentication, so no token enters a container.** Each run
  gets its own socket; there is nothing inside the container to steal, and
  nothing to rotate. The host-side modes (`0700` on the coordination root,
  `0755` on the per-run directory, `0666` on the socket, `0444` on the config
  and the co-author list, `0555` on the staged binary) are a contract with a
  semi-trusted container that may not run as root - they are not the access
  control. Both container paths are reserved:
  `runtime.ValidateMounts` refuses any caller-supplied mount that targets or
  nests under them, so a credential home cannot shadow either.
- **Disabling coordination still disables coordination.** With
  `--conflict-coordination=false`, the read-only canonical CLI mount remains
  available, but no usable run socket, borrowed run identity, or MCP bridge is
  available. Run-bound CLI calls and bridge calls return unavailable; the
  identity-free CLI can still provide general help and non-run skill guidance.
  The overlap radar remains active.
- **The run socket exposes no general control verbs.** Its six advertised
  coordination methods are `coord.status`, `coord.send`, `coord.inbox`,
  `coord.ask`, `coord.reply`, and `coord.report`. Native hooks also use the
  internal read-only `coord.hook.status` endpoint, with the same run identity
  and status authorization but an independent bounded request budget.
  Malformed envelopes and unknown methods still consume the ordinary budget;
  the hook endpoint cannot dispatch mutations. A swarm-assigned run
  additionally receives only the current assignment's `task.*` and `worker.*`
  methods over that same run-authenticated socket; those methods are not a
  general control API. There is no `run.kill`, no Git access, and no other
  run's transcript. Messages are capped at 4 KiB, rate-limited per run,
  bounded at 100 unread per inbox, and every one is recorded on the workspace
  timeline. The optional bridge is manual and still exposes only its six
  existing tools; it is not automatic registration or a Release B
  mission/worker interface.
- **A run can widen its own peer set, and the cap is what bounds it.** For
  ordinary runs, the overlap that authorizes a message is computed from the two
  runs' own diff snapshots, so a run that touches every tracked file is
  reported as overlapping with every other run in the workspace. Swarm
  assignments instead provide a server-derived peer set that may authorize
  active integrator and worker runs before file overlap; neither set is
  caller-selected, and both remain bounded. The server limits each run to 8
  distinct correspondents instead of trying to infer intent from a wide
  refactor. Read this as defence in depth, not a boundary: runs in one
  workspace already share a repository, so influencing each other through file
  contents needs no authorization at all. Turn the feature off if that is not
  acceptable.
- **The staged bridge binary is the server's own binary.** It is mounted
  read-only at `/opt/aether/aether-server` so a container can run the optional
  MCP bridge without shipping an extra artifact. A container therefore holds a
  copy of the server's code and can run its available subcommands. This grants
  nothing new: the binary carries no credentials, reaches no host state that
  the container was not already given, and the isolation is still the
  container, exactly as in "The agent container" above.

## Server self-update

`aether server update` (see [install.md](install.md#upgrading)) lets an admin
replace the running server's own binaries and restart onto them, from their
laptop, with no shell on the server box. That is inside the existing trust
model, not outside it: an admin can already run arbitrary code on the
server's Docker daemon through environment builds, so choosing which release
binary runs grants nothing new.

What bounds it: the `version` a client supplies is validated as a release tag
- `v` plus semver - and only ever names a release in the pinned
`3xDevOps/Aether` GitHub repository; the client can never supply a URL.

Both binaries are downloaded and verified against that release's
`checksums.txt` before either is replaced, and each is then renamed into
place from a staging file in its own directory. So a bad tag, a network
error, or a checksum mismatch leaves both binaries exactly as they were.
Only the renames at the end could leave `aether-server` updated and the
`aether` beside it not, and a rename within one directory fails only when
the filesystem does; the recorded failure then names which binaries were
already replaced. `aether server update --status` shows it.

## Client self-update on macOS

The dashboard's **Update now** button runs from `aether gui`, an
unprivileged process. On macOS it can still replace a CLI in a directory
the user cannot write, such as `/usr/local/bin`
([install.md](install.md#upgrading),
[local-gateway.md](local-gateway.md#localv1-verbs)). No Aether code runs as
root; root is left exactly one thing to do.

**When the dialog is offered.** Only when the directory is not writable by
this user, only root can write the binary's directory or any directory
above it, this is macOS, and the gateway is in a GUI session. Otherwise the
banner shows `sudo aether update`. The full rule, and why the privileged
command depends on it, is in
[local-gateway.md](local-gateway.md#localv1-verbs).

**What runs as root.** One `sh` command of system tools, addressed by
absolute path so nothing is looked up on root's `PATH`, with the staged
file, the destination and the release digest baked into its text
(`internal/macinstall`). For `/usr/local/bin/aether` it is, verbatim:

```sh
set -e; t=$(/usr/bin/mktemp '/usr/local/bin/.aether.update.XXXXXX'); trap '/bin/rm -f "$t"' EXIT; /usr/bin/install -m 0600 '/Users/you/Library/Caches/aether/update/.aether.update-123456789' "$t"; h=$(/usr/bin/openssl dgst -sha256 "$t"); [ "${h##* }" = '<sha256 of the release asset>' ] || { /bin/echo 'copied binary does not match the release checksum' >&2; exit 65; }; /bin/chmod 0755 "$t"; /bin/mv -f "$t" '/usr/local/bin/aether'
```

Only the staged file's name and the digest vary. `/usr/bin/osascript -e
'do shell script "<command>" with prompt "<text>" with administrator
privileges'` runs it, with the environment
`PATH=/usr/bin:/bin:/usr/sbin:/sbin`, `LANG=C`, and `HOME`, working
directory `/`, and nothing else from the user's shell - no `TMPDIR`, no
`DYLD_*`, no Homebrew `PATH`. The command is decided before the user is
asked and cannot change after: what the dialog authorizes is that text.

**Three checksum checks, in three places.** The gateway downloads the
release as the user and compares it with `checksums.txt` before anything
is staged. Root hashes its own copy and exits `65` on a mismatch, so a
staged file swapped while the dialog is up installs nothing. The gateway
then re-reads the installed file as the user - a regular file, mode
`0755`, root-owned, the release digest - before it rebuilds the desktop
app or exits; a mismatch there is reported as `installed
/usr/local/bin/aether does not match the release checksum; do not run it`.

**`0600` until verified.** Root copies into a temp file in the destination
directory, which the user cannot write, and keeps it `0600` until the hash
matches. A staged file replaced with a symlink to a root-only file would be
copied, fail the hash, and be removed without ever having been readable.
The final step is `mv -f` within one directory: atomic, and a running
`aether` keeps its old inode.

**The staging directory is private.** The download goes to
`<user cache>/aether/update` (`~/Library/Caches/aether/update`), created
`0700`, and refused when the path is a symlink, not a directory, or owned by
another user, because root reads from it. `install` copies the staged file;
root never executes or renames it.

**No password passes through Aether.** The dialog is macOS's own; the
password goes to the system's authorization service and is not read,
stored, piped, or logged by any Aether process. macOS asks on every click:
each click runs a new `osascript` process with a new command text (the
staged file's name differs), and Apple's TN2065 says the authentication
applies to that specific script text. Nothing is cached across clicks by
Aether or by the system for this right - `system.privilege.admin` is not
shared across processes.

**Why the dialog says osascript.** The dialog is titled `osascript`
because the app is built locally and unsigned. A dialog in Aether's own
name needs a privileged helper installed through `SMJobBless` or
`SMAppService`, and both require a Developer ID signed helper. Aether's
prompt text sits beneath the title and says so, naming the file and the
version being installed.

**What a same-user process can still do.** A process running as the same
user can write the staging directory and can kill or block the gateway.
That lets it make the root step fail - a swapped staged file fails the
hash - or stall it, by putting a FIFO where the staged file was so root's
`install` blocks on the open. Both are denial of service against this
user's own update, not escalation: nothing that process does can make
root install bytes other than the ones whose digest is in the command text.
That rests on the root-only path rule above: the directory root writes
into, and every directory above it, is root's alone
([local-gateway.md](local-gateway.md#localv1-verbs)).

## Dependency and toolchain vulnerability scanning

`make vulncheck` runs `govulncheck` over the whole module. CI runs it in the
`build-and-test` job. A docs-only pull request does not run it, because it
does not change module dependencies.

The step is **advisory** (`continue-on-error: true`), not a gate. Two reachable
Moby CVEs in the Docker SDK (`GO-2026-4887`, `GO-2026-4883`) have no fixed
release, and govulncheck has no way to suppress an individual finding, so a hard
gate would leave CI permanently red and train everyone to ignore it. Read the
step output on each PR instead: anything beyond those two Docker findings is new
and should be fixed or explicitly accepted here.

The SSH dependency `golang.org/x/crypto` must be at least `v0.56.0` to fix
`GO-2026-6303`, `GO-2026-6354`, and `GO-2026-6355`. These fixes require
Go 1.26; the module and toolchain requirements in `go.mod` must not be
downgraded independently of the dependency.

The Go toolchain is part of the attack surface. `go.mod` carries a `toolchain`
directive alongside the `go` directive so that CI, which selects its Go version
from `go.mod`, builds release binaries with a patched toolchain rather than the
oldest version the module happens to be compatible with. Bump the `toolchain`
line whenever a Go patch release fixes a standard-library CVE.
