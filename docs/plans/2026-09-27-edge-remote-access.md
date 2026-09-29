# Edge remote access

**Status:** Implemented in PR #249
(https://github.com/3xDevOps/aether/pull/249), as revised by
[Edge access policies](2026-09-28-edge-access-policies.md) in the same
PR: the dashboard through the edge, browser devices and the Android return
link were removed; claiming moved inside SSH; the server chooses an access
policy; sign-in and relay got two host names. The sections below describe
what shipped; `docs/edge.md` is the operator and user guide.

**Goal:** A developer reaches their Aether server from a computer without
configuring Tailscale, SSH, router ports, or certificates. Tailnet and
direct SSH-key connections, and the phone dashboard over a tailnet, keep
working unchanged.

An **edge** is a relay and sign-in service that Aether servers and clients
both dial out to. The project runs one, signing people in at
`auth.onaether.dev` and relaying at `edge.onaether.dev`; anyone can run
their own from the same binary. An **account** is a person signed in to the
edge with GitHub or Google. A **device** is one client install holding its
own key. A **grant** is a short-lived statement
signed by the edge: "this account, on this device, is opening this
connection".

## 1. Decisions

1. The relay never decrypts SSH. It splices bytes between two outbound
   WebSocket connections. SSH runs end to end between client and server.
2. GitHub and Google OAuth are the only sign-in methods of the edge flow.
   Accounts are keyed by `(provider, subject)`. Two providers with the same
   email are two accounts; nothing merges by email.
3. Enrollment is gated. The relay carries nothing for a server until a
   signed-in account has claimed it with a code printed on the server.
4. The server stays the authority on membership, roles and devices. The edge
   holds a copy (the **directory**) that the server pushes, and uses it only
   to refuse connections early. A compromised edge cannot add a member.
   Whether it can add a device is the server's access policy: under
   `approved-devices` it cannot; under `account` a grant admits a member's
   new device.
5. User identity and credentials are separate objects with separate
   lifecycles: account (edge), device token (edge, per device), device key
   (client, verified by the server).
6. The edge serves no dashboard. The TLS-passthrough design first built for
   it was removed; see the newer plan, decision 7.
7. `edge-url` is empty, and the edge off, unless the operator names one.
   `aether-server setup` is the only command that sets it unasked: when
   tailscaled is absent it enables the project's edge and says what that
   edge sees and how to turn it off; with tailscaled it asks, defaulting to
   no. `aether-server install` takes it only from `--edge-url`, and an
   upgrade never enrolls an existing server.
8. One new Go module: `golang.org/x/net` v0.57.0, indirect, which
   `x/crypto/acme/autocert` pulls in through `x/net/idna` and which is
   compiled into `aether-edge` only. `coder/websocket`,
   `x/crypto` and `modernc.org/sqlite` were already required; `x/oauth2`
   moves from indirect to direct.

Rejected: a TLS-terminating relay (reads terminals), porting ORCA's relay
(Node, Postgres, private auth service), embedded tsnet (phones still need the
Tailscale app), credentials in URLs or custom-scheme links (the one-time
sign-in code a callback carries is useless without the cookie and verifier it
is bound to).

## 2. Trust boundaries

| Party | Can | Cannot |
| --- | --- | --- |
| Edge operator, honest | See server ids, account identities, device labels, client IPs, timing, byte counts | Read SSH traffic |
| Edge, compromised | Deny service. Forge a grant for any account, which reaches a workspace on a server whose policy is `account` | Read or alter SSH traffic. Impersonate a server to a client. Take a claim. Admit a device on a server whose policy is `approved-devices` |
| Holder of a stolen device token | Open a splice to servers that account can reach | Authenticate to the server: the device key is also required |
| Holder of a stolen claim code | Nothing without a signed-in account; with one, claim that server before its owner does | Claim after the code is used, expired, or five attempts failed |

The edge is an identity broker: the server believes the edge about *who*
signed in. Whether that admits a device is the server's policy; what the
person may do is always the server's decision. The newer plan, section 3,
has the full attacker tables.

## 3. Components

| Component | Package | New or changed |
| --- | --- | --- |
| Wire types, ids, signatures, claim user name, account API types | `internal/edge/proto` (package `edgeproto`) | new |
| Edge binary and operator commands | `cmd/aether-edge` | new |
| Edge store (SQLite) | `internal/edge/store` (package `edgestore`) | new |
| Edge sign-in, pages, API, host routing, account deletion | `internal/edge/service` (package `edge`) | new |
| Edge relay, forwarded client address | `internal/edge/relay` | new |
| Server agent: enrollment, pin, claim, ownership reports | `internal/edge/agent` (package `edgeagent`) | new |
| Client sign-in, token store, dialer | `internal/edge/client` (package `edgeclient`) | new |
| End-to-end tests | `internal/edge/edgetest` | new |
| Relayed SSH transport, grant auth, access policy, claims, device approval | `internal/sshd` | changed |
| Identities, devices, identity invitations | `internal/store` | changed (migration) |
| Access policy, device status | `internal/domain`, `internal/protocol` | changed |
| Client link and claim | `internal/cli`, `internal/syncd` | changed |
| Desktop onboarding: sign in, claim, link | `internal/localgw` | changed |
| Commands | `cmd/aether`, `cmd/aether-server` | changed |
| Dashboard devices and onboarding | `web/` | changed |
| Unit, environment example, nginx example, guides | `packaging/`, `scripts/edge-nginx-test.sh`, `docs/` | new, changed |

## 4. Identifiers and signatures

- **Server id.** `lower(base32-nopad(sha256(ssh-wire-bytes(host public key))))[:26]`,
  130 bits. Derived, never assigned. Clients verify the SSH host key against
  it on every path, so a linked server is pinned, not trusted on first use.
- **Signatures are domain-separated.** The SSH host key signs SSH exchange
  hashes, which are at most 64 bytes. Every edge message it signs starts with
  a context string and is longer than 64 bytes, so an edge cannot obtain a
  signature usable in an SSH handshake.
  - Enrollment: `"aether-edge-enroll-v1\x00" || relay origin || "\x00" || server id || "\x00" || nonce`.
  - Grant (signed by the edge's Ed25519 key): `"aether-edge-grant-v1\x00" || canonical JSON`.
- **Edge key.** An Ed25519 key created on first start in the edge data
  directory. The server pins it when it enrolls and prints its fingerprint.
  A changed edge key is refused with the old and new fingerprints;
  `aether-server edge trust` re-pins. The pin, the recorded owner and the
  claim code are kept per edge origin, so a server pointed at another edge
  enrolls there unclaimed with a new pin, and finds the first edge's pin
  and owner again when pointed back.
- **Protocol version.** Every handshake carries `version`. The edge accepts
  every version at or above `edgeproto.MinVersion`, newer ones included (they
  speak down); a release raises the minimum only with notice. An older peer
  is refused with `upgrade required: <minimum>`, HTTP 426 for clients.

## 5. Connection lifecycle

### Server to edge

1. `aether-server` dials `wss://<relay>/v1/server/control`.
2. Edge sends `challenge{version, nonce, origin}`.
3. Server sends `hello{version, host_key, signature, agent_version, name,
   access_policy}`, signing the origin it dialed, never the challenge's.
4. Edge verifies, derives the id, replies `ready{server_id, state,
   edge_key}`. A newer registration for the same id replaces the older one.
   The announced policy is for display; the server enforces its own.
5. Ping every 20 s; 60 s of silence closes. Reconnect with jittered
   exponential backoff, 1 s to 60 s. The agent never stops the server: an
   edge outage leaves direct and tailnet paths and running work untouched.

### Client to server (SSH)

1. Client dials `wss://<relay>/v1/connect/<server id>` with
   `Authorization: Bearer <device token>`.
2. Edge checks the token, then the directory. Refusals are HTTP errors with
   the reason: `401 device token revoked`, `403 not a member of this server`,
   `404 unknown server`, `503 server is not connected to the edge`.
3. Edge sends `open{conn_id, ticket, kind: "ssh", grant}` on the control
   socket (`kind: "claim"` for `/v1/connect/<server id>/claim`).
4. Server verifies the grant (edge key, issuer equal to the relay origin it
   enrolled with, its own id, `conn_id`, kind, expiry of 60 s) and dials
   `wss://<relay>/v1/server/data/<conn_id>` with the ticket.
5. Edge splices. Each connection has its own sockets, so TCP flow control is
   per stream and a large fetch does not stall a terminal.
6. SSH handshake. The relayed transport has its own `ssh.ServerConfig`: no
   `none` method, no tailnet WhoIs, no first-key bootstrap, no invite-code
   user names, and its own handshake budget. The offered key must equal the
   grant's device key.
7. Server maps `(provider, subject)` to a member, then checks the device
   under its access policy.

The edge-asserted client address is used for logs and rate limits only. It
is never the connection's `RemoteAddr` and never an input to authentication.

The dashboard path first built here (TLS passthrough by SNI, server
certificates by TLS-ALPN-01, browser devices and sessions, and the Android
app's return link) was removed before release. A phone reaches the
dashboard over a tailnet; a computer through `aether gui`.

## 6. Authentication and authorization

### Accounts and devices at the edge

- `aether login` runs a device authorization flow on the sign-in origin,
  which the client reads from `GET <relay>/v1/edge`: the CLI shows a URL and
  a short code, the person signs in at the edge and confirms the code, the
  CLI receives a device token. Nothing secret is in the URL.
- The device key is a dedicated Ed25519 key in the Aether config directory,
  mode 0600. It is not the person's `~/.ssh` key.
- The token is 32 random bytes, stored hashed at the edge, bound to the
  account and the device key.
- `aether logout` revokes the token. The edge's **Devices** page lists and
  revokes them. Revoking closes that device's live splices; its direct
  connections stay, since the device is still approved on the server.
- The confirmation page shows the address the sign-in started from. Code
  entry is limited per address and per account; token polling per address.
- A GitHub login or email matches invitations only within 24 hours of the
  account's last provider sign-in (`identity_at`, carried in grants and
  checked by edge and server). Signing in also takes the login from any
  other account. A login renamed away, or an email moved to another
  account, therefore matches an invitation meant for its new holder for at
  most 24 hours after its previous holder last signed in.

### Claiming a server

1. `aether-server setup` prints a claim code, `<server id>-<secret>`, valid
   for 30 minutes and five attempts. The full id lets the client refuse an
   answer for any other server, which a host key ground to match a shorter
   prefix could otherwise give.
2. The owner runs `aether link --claim <code>`. The client dials
   `/v1/connect/<server id>/claim` and presents the code inside SSH once the
   host key has proved the id; the newer plan, section 4, has the steps.
   The edge has no **Add a server** page.
3. The server compares the secret itself; the edge never sees it.
4. On success the server creates the admin member with that identity,
   approves the claiming device, and reports the claim on its control
   connection. The edge records the owner from that report only. When
   recording fails, the edge closes the server's control socket; on
   reconnecting the server is told it is unclaimed and drops the owner it
   recorded, and the same account claims it again with a new code.

An unclaimed server may hold a control socket and receive claim
connections, nothing else. Unclaimed registrations are limited per address
and dropped after 30 minutes.

### Members and invitations

- `aether invite --github <login>` or `--email <address>`, with `--role`.
  The server stores the invitation and includes it in the directory. No code
  changes hands.
- The invited person signs in and sees the server in `aether servers`. On
  first connection the server matches the grant to the invitation, creates
  the member with the invited role, binds it to the immutable subject, and
  consumes the invitation.
- Email matching uses provider-verified addresses only.
- Invitations expire after 7 days. `aether invite list` and
  `aether invite revoke`.
- Roles and capabilities are the existing ones and are enforced where they
  always were.

### Devices at the server

- A new device key is `registered` and admitted under `edge-access
  account`, and `pending` and refused under `approved-devices`, a member's
  first device included. The refusal carries the command that approves it:
  `aether device approve <code>` from an approved device, SSH key or
  tailnet connection of that member or an admin, or `sudo aether-server
  device approve <code>` on the server.
- `aether device list`, `aether device revoke`, `sudo aether-server device
  review`.

### Revocation

| Revoke | Command | Effect |
| --- | --- | --- |
| A member | `aether member remove <id>` | Identities and devices deleted; directory updated; live connections closed at once |
| A device | `aether device revoke <id>` | That key refused on every path; its connections closed at once |
| A device token | `aether logout`, edge Devices page | No further splices; live ones closed |
| An account | `aether logout --delete-account`, edge Account page | Each server removes that identity and its edge devices; see the newer plan, section 11 |
| An invitation | `aether invite revoke <id>` | Removed from the directory |
| A server | `aether-server edge leave`, edge Servers page | Unenrolled; members keep direct and tailnet access |

## 7. Client behaviour

- A link stores the edge URL and server id beside the address. With an
  address configured, the client dials it first and falls back to the edge.
  The host key is checked against the server id on both.
- `internal/cli/dial.go` and `internal/syncd` share one dialer.
- Git: Aether sets `GIT_SSH_COMMAND` to `aether edge-ssh` on the git
  processes it starts. For `git push aether` typed by hand, `aether link`
  sets the repository's `core.sshCommand` to the same wrapper, which handles
  edge hosts itself and executes the system `ssh` unchanged for every other
  host. An existing `core.sshCommand` is overwritten only when it is an
  earlier `<path to aether> edge-ssh`; otherwise the link prints the line
  to add.
- `aether link` goes through the edge only for `--claim` or a 26-character
  server id. Any other argument, a bare name included, is an SSH address:
  server names are chosen by each server's admin, so a name must never
  send a link to a server someone else controls.
- Errors name the real cause, for example
  `connect to server <id>: edge.onaether.dev refused: server is not connected to the edge (HTTP 503)`.

## 8. Failure modes

| Failure | Behaviour |
| --- | --- |
| Edge down | Server keeps running and retries. Clients with a direct or tailnet address use it. Others fail with the edge's error |
| Edge restart | Every splice drops. Servers and clients reconnect. Runs are unaffected. The edge sends `drain` first on a clean stop |
| Server offline | `503 server is not connected to the edge` |
| Data socket not attached in 10 s | Client closed with `server did not attach` |
| Edge signing key lost | Every server refuses the edge with both fingerprints until its operator runs `aether-server edge trust` |
| OAuth provider down | No new sign-ins. Existing device tokens and sessions keep working |
| Host key lost or rotated | New server id. The server must be claimed again and clients linked again. `docs/edge.md` covers backing up the key |
| Clock skew | Grants allow 30 s |

## 9. Limits

| Limit | Value |
| --- | --- |
| Concurrent SSH connections per server | 48, below sshd's relayed handshake budget |
| Connections per device | 16 |
| Unclaimed registrations per address | 3; IPv6 3 per /64, 12 per /56, 48 per /48 |
| Unclaimed registrations per edge | 10000 |
| Open connections per address | 1024; IPv6 by /64; behind a reverse proxy, requests in progress per forwarded address |
| Attach deadline | 10 s |
| Sign-in, device-code and claim attempts | rate-limited per address; IPv6 by /64, /56 (4x) and /48 (16x). Device-code entry also per account; token polling per address |
| Monthly egress budget | configured; once reached, every splice together is paced to 256 KiB/s until the UTC month ends |

No idle timeout applies to a spliced stream: an idle terminal is legitimate.

## 10. Testing

- Unit tests beside each package.
- `internal/edge/edgetest`: a real edge on its two origins, real `sshd`
  servers under each access policy and a real client in one process, with
  a fake OAuth provider and a proxy that plays a compromised edge. It
  covers what this plan and the newer one promise; `docs/testing.md` lists
  each test.
- `TestBehindNginx` (`scripts/edge-nginx-test.sh`) runs the packaged nginx
  configuration in front of a real edge.
- Dashboard: Vitest for the device views and the onboarding link step.
- Existing suites unchanged.

## 11. Rollout

1. Owner creates the OAuth applications, DNS records, nginx and the VPS
   unit, following the owner checklist in `docs/edge.md`. Nothing in this
   change touches a live host.
2. Release with the edge off on every existing server. `aether-server
   setup` turns it on, with a notice, on hosts without Tailscale, offers it
   on hosts with Tailscale, and asks for the access policy.
3. Later: a second region, graceful splice hand-over on deploy.
