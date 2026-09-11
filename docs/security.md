# Security posture

Operational guidance for people running an Aether server. This file records the
stances that are deliberate, so they are not repeatedly re-raised as findings.

## The agent container

The container **is** the isolation boundary. Aether does not try to build a
second sandbox inside it.

- **Agents run as root by default.** A harness may map a run to a non-root
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

Each member's persistent home is mounted only into that member's environment
terminal and runs that use their agent account. Account sharing is the sole
exception: `aether account share <member-id>` lets that member launch runs with
the owner's home, saved image, configuration, custom harness definitions, and
vendor login. This is equivalent to handing them every credential and file in
that home.
The authenticated launcher remains the run owner, and the run's commits are
authored as that member's git identity, not the account owner's; usage and
cost are attributed to the selected account.

Revoking a grant blocks later launches and relaunches. It does not stop an
already-running container or remove the home mounted into it. Stop those runs
before revoking access when immediate removal matters.

Real names and email addresses cross into the container with the run. The
git identity of the member who launched it is baked into the container's
`GIT_AUTHOR_*` and `GIT_COMMITTER_*` at creation, and
`/run/aether/co-authors` holds one `Co-authored-by: Name <email>` line per
member who steered the run, readable by the agent like any other file under
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

### GitHub credentials and signing keys

Connecting GitHub (`aether github connect`, see
[environment-home.md](environment-home.md#connect-github)) puts two secrets,
and the settings that use them, in the member home on the server:

- **The gh token**, at `homes/<member>/.config/gh/hosts.yml`. `gh auth
  login` runs inside the environment terminal container, which has no
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
that environment - runs the agent's program instead of gh. Account sharing
hands the recipient's runs the same reach, exactly as it does every other
credential in the home.

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

### Hostile agents

If you run agents you do not trust, put the `--data-dir` on a filesystem
mounted `nosuid,nodev`. Docker exposes no per-bind `nosuid`/`nodev` controls, so
without that a root agent can plant a setuid binary through a writable bind
mount and have it survive on the host. See the security note on
`ValidateMounts` in `internal/runtime/mounts.go`.

## The dashboard gateways

The dashboard runs over one of two gateways, which share their handlers and
differ only in who they trust (`internal/webgate` is the shared core;
[local-gateway.md](local-gateway.md)).

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

The local onboarding importer reads a directory only after `config.roots`
returns and a destination is known. Credential names and `*.pem` files are
filtered before any browser read, while runtime/history paths use the selected
root's `runtime_ignores`; the server remains authoritative and scans all bytes
that survive those local exclusions. Unknown or ambiguous directory basenames
must be assigned explicitly, and changing the destination re-reads the retained
browser `File` handles. Generation guards discard stale reads and prevent a
preview prepared for one destination from being submitted to another. The
server-hosted dashboard cannot read a directory on the user's machine, so this
surface exists only in `aether gui`.

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
in the target account's persistent member home, not in a workspace checkout or
source tree. The returned absolute path is the path visible inside the target
container at its `$HOME`; the client cannot choose the destination or filename.
An upload with no `run_id` targets the authenticated member's running
environment terminal. A run target requires `Steer` and writes into that run's
account member home, including the owner's home when the run uses an explicit
account share.

These files follow member-home retention: stopping or resetting an environment
does not remove the home, so images remain until they are removed from that
home or the member is deleted. A member-home bind mount is not part of
`env.save`'s Docker image, so terminal images are not copied into the saved
environment image. Account sharing therefore has the same implication as for
other home files and credentials: a recipient's run can read images in the
shared account's home.

## Browser configuration and Files

The local dashboard's onboarding directory picker is an explicit, one-time
browser import. A server-hosted dashboard has no laptop directory picker; use
`aether gui` for this step. The browser waits for `config.roots` and a known
destination before previewing or reading bytes. A known unique basename selects
its destination automatically; an unknown or ambiguous basename requires an
explicit choice. Credential names in any path component and `*.pem` files are
always skipped before upload. Runtime/history paths come from the selected
root's `runtime_ignores` metadata and match exact, root-relative paths or
component prefixes case-sensitively after trailing slashes are trimmed.
Changing the destination clears the old preview and re-reads retained browser
`File` handles; generation guards discard stale reads. It reads remaining
selected regular-file bytes and sends them to the server, where they are
scanned before writing; a secret finding is therefore not proof that the
content stayed local. Empty files and arbitrary binary regular bytes are
preserved under the 1 MiB/file, 20 MiB decoded aggregate, and 2,000-file
limits. The shared HTTP gateway permits a 30 MiB request for `config.import`,
4 MiB for `config.write` and `files.write`, and 1 MiB for ordinary methods;
these are framing limits, not larger decoded configuration allowances.

Browser metadata is intentionally limited. New imported files are `0644`;
existing modes are preserved even when the server uses a restrictive umask.
The browser cannot preserve executable mode or symlinks. The server rejects
unsafe paths, symlink components, hardlinks, and nonregular destinations,
and retains directory and staged-file ownership. Every `config.*` method
requires the `Launch` capability and targets only the authenticated member's
own home; an admin cannot select another member or account.

The imported and edited files are in the member's shared read-write home,
mounted into that member's environment terminal and runs, including active
runs. An account share grants another member's run that same home; it is not a
per-run isolated configuration copy. A snapshot pin records launch provenance,
not an isolation boundary or a promise that home edits wait for later runs.
Files edits do not rebuild the installed-agent image.

The Files editor accepts complete UTF-8 text without NUL bytes up to 512 KiB.
Binary and truncated files are read-only. Run and configuration saves recheck
SHA-256 revisions immediately before atomic rename under Aether's root lock;
base-branch commits compare-and-swap the branch head. These locks do not
exclude arbitrary live-agent filesystem writers. A stale or failed save leaves
the browser draft available.

## SSH port forwarding

Port forwarding is limited to direct-tcpip channels whose destination is
`run:<run-id>` or exactly `terminal`. Run targets require the Steer capability
for the authenticated member; the terminal target resolves that member's own
live environment container and does not require Steer. The server resolves
addresses itself and dials only the requested container port. Arbitrary hosts
and ports are not targets, and reverse forwarding is disabled: global
forwarding requests are denied.

The local dashboard recognizes OAuth authorization links whose redirect URI is
an HTTP loopback address. It binds the matching local callback port before
opening the authorization page, then forwards that port only to the terminal
where the link appeared. Other links keep the normal browser behavior.

## Conflict coordination

When two runs edit the same file, each container gets a unix socket it can
message the other run through. Detail is in `docs/coordination.md` (host side
and wire) and `docs/mcp-bridge.md` (the in-container half); the operator-facing
stances are these.

- **The mount is the authentication, so no token enters a container.** Each run
  gets its own socket at `/run/aether/coord2.sock`; whoever connects on it *is*
  that run. There is nothing inside the container to steal, and nothing to
  rotate. The host-side modes (`0700` on the coordination root, `0755` on the
  per-run directory, `0666` on the socket, `0444` on the config and the
  co-author list, `0555` on the staged binary) are a contract with a
  semi-trusted container that may not run as root - they are not the access
  control. Both container paths are reserved:
  `runtime.ValidateMounts` refuses any caller-supplied mount that targets or
  nests under them, so a credential home cannot shadow either.
- **The socket exposes three methods and no control verbs.** `coord.status`,
  `coord.send`, `coord.inbox`, and nothing else - no `run.kill`, no git, no
  other run's transcript. Messages are capped at 4 KiB, rate-limited per run,
  bounded at 100 unread per inbox, and every one is recorded on the workspace
  timeline.
- **A run can widen its own peer set, and the cap is what bounds it.** The
  overlap that authorizes a message is computed from the two runs' own diff
  snapshots, so a run that touches every tracked file is reported as
  overlapping with every other run in the workspace. The server cannot tell
  that from a wide refactor, so it limits each run to 8 distinct
  correspondents instead of trying to. Read this as defence in depth, not a
  boundary: runs in one workspace already share a repository, so influencing
  each other through file contents needs no authorization at all. Turn the
  feature off with `--conflict-coordination=false` if that is not acceptable.
- **The staged bridge binary is the server's own binary.** It is mounted
  read-only at `/opt/aether/aether-server` so any image can run the MCP bridge
  without shipping an extra artifact. A container therefore holds a copy of the
  server's code and can run any of its subcommands - `serve`, `mcp --socket
  <path>`, the rest. This grants nothing new: the binary carries no
  credentials, reaches no host state that the container was not already given,
  and the isolation is still the container, exactly as in "The agent container"
  above.

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

`make vulncheck` runs `govulncheck` over the whole module. CI runs it on every
PR in the `build-and-test` job.

The step is **advisory** (`continue-on-error: true`), not a gate. Two reachable
Moby CVEs in the Docker SDK (`GO-2026-4887`, `GO-2026-4883`) have no fixed
release, and govulncheck has no way to suppress an individual finding, so a hard
gate would leave CI permanently red and train everyone to ignore it. Read the
step output on each PR instead: anything beyond those two Docker findings is new
and should be fixed or explicitly accepted here.

The Go toolchain is part of the attack surface. `go.mod` carries a `toolchain`
directive alongside the `go` directive so that CI, which selects its Go version
from `go.mod`, builds release binaries with a patched toolchain rather than the
oldest version the module happens to be compatible with. Bump the `toolchain`
line whenever a Go patch release fixes a standard-library CVE.
