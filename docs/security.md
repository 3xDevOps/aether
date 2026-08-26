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

### Hostile agents

If you run agents you do not trust, put the `--data-dir` on a filesystem
mounted `nosuid,nodev`. Docker exposes no per-bind `nosuid`/`nodev` controls, so
without that a root agent can plant a setuid binary through a writable bind
mount and have it survive on the host. See the security note on
`ValidateMounts` in `internal/runtime/mounts.go`.

## The dashboard gateway

The web dashboard is loopback-bound by default and reached through the SSH
port-forward `aether dash` opens, so SSH stays the only required network
surface.

- **Every request needs a bearer token, loopback included.** HTTP cannot
  identify a member on its own and any local process can reach a loopback
  port, so identity always comes from a token minted on the authenticated SSH
  control channel (`dash.token.mint`). A token carries exactly its member's
  authority and passes the same capability checks. Tokens are stored hashed, in
  memory only, expire (12h default, 24h maximum), and can be revoked by their
  member. A server restart invalidates all of them.
- **The HTTP transport reaches an allowlist of methods, not the whole SSH
  method map.** A token travels in a URL - browser history, `xdg-open`
  argv, a shared screen - so it is far easier to capture than an SSH key,
  and it must not be a route to a durable one. The allowlist is what the
  dashboard renders and steers: `server.info`, `workspace.list`,
  `session.list`, `session.get`, `member.list`, the nine `run.*` methods
  the GUI spec names (`launch`, `list`, `get`, `kill`, `pause`, `resume`,
  `inject`, `close`, `handoff`), `approval.list`, `approval.decide`,
  `presence.roster`, `presence.heartbeat`, `session.timeline`,
  `cost.report`, `budget.get`, `run.overlaps`, `template.list`, and
  `template.launch`. Everything else answers `403`. The excluded ones are
  excluded because they issue or widen a credential (`member.invite`,
  whose one-time code becomes a persisted collaborator holding the
  bearer's own SSH key, plus `member.approve`, `member.remove` and
  `dash.token.*`), replace the agent credential profile mounted into run
  containers (`profile.*`), or administer the deployment (`workspace.add`,
  `session.new`, `session.settings`, `budget.set`, `template.save`,
  `template.delete`, `schedule.*`). Those need the SSH key, which is the
  point: a stolen dashboard token expires and can be revoked, an SSH
  membership it minted could not be.
- **Live sockets are re-authorized, not just re-tokened.** Event and
  attach WebSockets re-run the handshake's gates every few seconds and
  close with a policy-violation status the moment one stops holding: the
  token revoked or expired, the member removed or un-approved, and for a
  write attach the steer capability withdrawn by a `protect`, a
  `steer_others` change, or a handoff. Otherwise a socket would keep the
  authority it was opened with - a removed member still on the event feed,
  or someone still typing into an agent an admin has since walled off. A
  downgrade from write to read is a close, not a silent demotion; the
  browser reconnects read-only.
- **`--dashboard-addr` serves plain HTTP.** Aether ships no certificate
  handling: terminate TLS in front of the gateway, or keep the address on a
  tailnet. Without the flag no direct listener exists at all.
- **The SPA files are served without a token.** The bundle is not secret and
  has to load before it can present one; everything behind `/api/` and `/ws/`
  is gated.

### The local gateway

`aether gui` serves the same SPA from the user's own machine, proxying the
API shape over that machine's SSH connection (`internal/localgw`,
[local-gateway.md](local-gateway.md)). Its stances differ from the remote
gateway's in exactly the ways the trust model differs:

- **It binds 127.0.0.1 and nothing else.** There is no exposure flag; the
  listener is loopback or it does not exist.
- **Every request still needs a token, loopback included** - the same
  rationale as above: any local process can reach a loopback port. The
  token is minted per process (32 random bytes) and dies with it; there is
  nothing to revoke and nothing survives a restart.
- **The full method map is reachable, not an allowlist.** The remote
  allowlist exists because a dashboard token travels in a URL and must not
  be a route to a durable credential. Here that rationale does not apply:
  the identity is the member's own SSH key, held by the same process that
  serves the page, and the bearer token never crosses a network or lands
  anywhere shareable - it lives in one process and one local browser tab.
  A method call carries exactly the authority the SSH key already has from
  a terminal on the same machine.
- **`/local/v1` executes with the user's own filesystem and git
  authority** - link config, `git fetch`/`push` on the linked clone,
  systemd user units, scaffold files. That is the point of the surface: it
  does what the CLI does, for the person already at the keyboard.
- **The remote gateway's allowlist is unchanged.** Nothing about the local
  gateway widens what a server-side dashboard token can reach.

## Conflict coordination

When two runs edit the same file, each container gets a unix socket it can
message the other run through. Detail is in `docs/coordination.md` (host side
and wire) and `docs/mcp-bridge.md` (the in-container half); the operator-facing
stances are these.

- **The mount is the authentication, so no token enters a container.** Each run
  gets its own socket at `/run/aether/coord.sock`; whoever connects on it *is*
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
  bounded at 100 unread per inbox, and every one is recorded on the session
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
