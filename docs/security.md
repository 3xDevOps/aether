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

Each member's persistent home is mounted only into that member's containers.
The server never mounts one member's home into another member's run or terminal,
even when both members use the same workspace or harness.

### Hostile agents

If you run agents you do not trust, put the `--data-dir` on a filesystem
mounted `nosuid,nodev`. Docker exposes no per-bind `nosuid`/`nodev` controls, so
without that a root agent can plant a setuid binary through a writable bind
mount and have it survive on the host. See the security note on
`ValidateMounts` in `internal/runtime/mounts.go`.

## The dashboard gateway

There is no server-side HTTP listener. The dashboard is served from the
user's own machine by `aether gui`, which proxies the API shape over that
machine's SSH connection to the linked server (`internal/localgw`,
[local-gateway.md](local-gateway.md)). SSH stays the only network surface
the server exposes, and the browser surface inherits its boundary.

- **It binds 127.0.0.1 and nothing else.** There is no exposure flag; the
  listener is loopback or it does not exist. Nothing about the dashboard
  widens what the server listens on.
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
- **The SPA files are served without a token.** The bundle is not secret and
  has to load before it can present one; everything behind `/api/`, `/ws/`
  and `/local/` is gated.
- **Live sockets carry no separate re-authorization clock.** The server
  re-runs its own capability checks on every proxied call and on each
  subsystem channel, and re-checks live attach and sync channels every few
  seconds; the token cannot be revoked out from under a socket because it
  lives and dies with the process serving it. A write attach that loses the
  steer capability is dropped by the server exactly as a CLI attach would
  be - the terminal view falls back to a mirror - and a removed member
  loses every open channel.

## Conflict coordination

When two runs edit the same file, each container gets a unix socket it can
message the other run through. Detail is in `docs/coordination.md` (host side
and wire) and `docs/mcp-bridge.md` (the in-container half); the operator-facing
stances are these.

- **The mount is the authentication, so no token enters a container.** Each run
  gets its own socket at `/run/aether/coord2.sock`; whoever connects on it *is*
  that run. There is nothing inside the container to steal, and nothing to
  rotate. The host-side modes (`0700` on the coordination root, `0755` on the
  per-run directory, `0666` on the socket, `0444` on the config, `0555` on the
  staged binary) are a contract with a semi-trusted container that may not run
  as root - they are not the access control. Both container paths are reserved:
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
