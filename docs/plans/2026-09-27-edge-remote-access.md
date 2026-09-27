# Edge remote access

**Status:** Implemented in PR #249
(https://github.com/3xDevOps/aether/pull/249). This document describes what
was built; `docs/edge.md` is the operator and user guide.

**Goal:** A developer reaches their Aether server from a laptop, a phone, or a
browser without configuring Tailscale, SSH, router ports, or certificates.
Tailnet and direct SSH-key connections keep working unchanged.

An **edge** is a relay and sign-in service that Aether servers and clients
both dial out to. The project runs one at `edge.onaether.dev`; anyone can run
their own from the same binary. An **account** is a person signed in to the
edge with GitHub or Google. A **device** is one client install, or one
browser, holding its own credential. A **grant** is a short-lived statement
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
   to refuse connections early. A compromised edge cannot add a member, and
   cannot add a device to a member who already has one without that member's
   approval.
5. User identity and credentials are separate objects with separate
   lifecycles: account (edge), device token (edge, per device), device key
   (client, verified by the server), browser device and browser session
   (server).
6. Browsers reach the server's own HTTPS listener through TLS passthrough.
   The server holds its own certificate. The edge does not see dashboard
   plaintext unless it misissues a certificate, which Certificate
   Transparency records. `docs/security.md` states this plainly.
7. `edge-url` is empty, and the edge off, unless the operator names one.
   `aether-server setup` is the only command that sets it unasked: when
   tailscaled is absent it enables the project's edge and says what that
   edge sees and how to turn it off; with tailscaled it asks, defaulting to
   no. `aether-server install` takes it only from `--edge-url`, and an
   upgrade never enrolls an existing server.
8. One new Go module: `golang.org/x/net` v0.57.0, indirect, which
   `x/crypto/acme/autocert` pulls in through `x/net/idna` and which is
   compiled into `aether-edge` and `aether-server`. `coder/websocket`,
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
| Edge operator, honest | See server ids, account identities, device labels, client IPs, timing, byte counts | Read SSH or dashboard traffic |
| Edge, compromised | Deny service. Forge a grant for any account. Obtain a certificate for a server hostname and intercept browser sessions (logged in CT) | Read or alter SSH traffic. Impersonate a server to a client. Claim an unclaimed server without its claim code (a code entered through it, it can use). Add a device to a member who has one, when device approval is on (the default) |
| Holder of a stolen device token | Open a splice to servers that account can reach | Authenticate to the server: the device key is also required |
| Holder of a stolen claim code | Nothing without a signed-in account; with one, claim that server before its owner does | Claim after the code is used, expired, or five attempts failed |

The edge is a trusted identity broker: the server believes the edge about
*who* signed in. It does not believe the edge about *what that person may
do*.

## 3. Components

| Component | Package | New or changed |
| --- | --- | --- |
| Wire types, ids, signatures | `internal/edgeproto` | new |
| Edge binary | `cmd/aether-edge` | new |
| Edge store (SQLite) | `internal/edge/edgestore` | new |
| Edge sign-in, pages, API | `internal/edge` | new |
| Edge relay and TLS router | `internal/edge/relay` | new |
| Server agent | `internal/edgeagent` | new |
| Relayed SSH transport, grant auth, device approval | `internal/sshd` | changed |
| Identities, devices, identity invitations | `internal/store` | changed (migration) |
| Edge dashboard listener, sessions, certificate | `internal/servergw` | changed |
| Shared dashboard core: exported same-origin check, handler wrap | `internal/webgate` | changed |
| Client sign-in, dialer | `internal/edgeclient`, `internal/cli`, `internal/syncd` | new, changed |
| Desktop onboarding: sign in, claim, link | `internal/localgw` | changed |
| Commands | `cmd/aether`, `cmd/aether-server` | changed |
| Dashboard sign-in, devices, onboarding | `web/` | changed |
| Android sign-in return | `android/` | changed |
| Unit, env example, guides | `packaging/`, `docs/` | new, changed |

## 4. Identifiers and signatures

- **Server id.** `lower(base32-nopad(sha256(ssh-wire-bytes(host public key))))[:26]`,
  130 bits. Derived, never assigned. Clients verify the SSH host key against
  it on every path, so a linked server is pinned, not trusted on first use.
- **Signatures are domain-separated.** The SSH host key signs SSH exchange
  hashes, which are at most 64 bytes. Every edge message it signs starts with
  a context string and is longer than 64 bytes, so an edge cannot obtain a
  signature usable in an SSH handshake.
  - Enrollment: `"aether-edge-enroll-v1\x00" || edge origin || "\x00" || server id || "\x00" || nonce`.
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

1. `aether-server` dials `wss://<edge>/v1/server/control`.
2. Edge sends `challenge{version, nonce, origin}`.
3. Server sends `hello{version, host_key, signature, agent_version, name}`.
4. Edge verifies, derives the id, replies
   `ready{server_id, state, edge_key, server_domain}`. A newer registration
   for the same id replaces the older one. `server_domain` is the domain the
   edge passes dashboards through; the server learns it here and has no
   setting of its own for it.
5. Ping every 20 s; 60 s of silence closes. Reconnect with jittered
   exponential backoff, 1 s to 60 s. The agent never stops the server: an
   edge outage leaves direct and tailnet paths and running work untouched.

### Client to server (SSH)

1. Client dials `wss://<edge>/v1/connect/<server id>` with
   `Authorization: Bearer <device token>`.
2. Edge checks the token, then the directory. Refusals are HTTP errors with
   the reason: `401 device token revoked`, `403 not a member of this server`,
   `404 unknown server`, `503 server is not connected to the edge`.
3. Edge sends `open{conn_id, ticket, kind: "ssh", grant}` on the control
   socket.
4. Server verifies the grant (edge key, its own id, `conn_id`, expiry of
   60 s) and dials `wss://<edge>/v1/server/data/<conn_id>` with the ticket.
5. Edge splices. Each connection has its own sockets, so TCP flow control is
   per stream and a large fetch does not stall a terminal.
6. SSH handshake. The relayed transport has its own `ssh.ServerConfig`: no
   `none` method, no tailnet WhoIs, no first-key bootstrap, no invite-code
   user names, and its own handshake budget. The offered key must equal the
   grant's device key.
7. Server maps `(provider, subject)` to a member, then checks the device.

The edge-asserted client address is used for logs and rate limits only. It
is never the connection's `RemoteAddr` and never an input to authentication.

### Browser to server (dashboard)

1. DNS `*.<server domain>` points at the edge. The edge reads the TLS
   ClientHello. SNI equal to the edge host is served locally; SNI
   `<server id>.<server domain>` is passed through as `open{kind: "web"}`;
   anything else is closed.
2. The server terminates TLS with a certificate for exactly its own hostname,
   obtained by TLS-ALPN-01 through the same passthrough. Issuance starts
   once the server is claimed, at an enrollment the edge answers as claimed
   or when a claim succeeds, and the edge announced a server domain; never
   on first request; until it succeeds, ordinary handshakes are
   refused so browsers cannot start orders of their own. Failure is retried
   from 1 minute to 1 hour and never stops the server. An edge that
   announces no domain gets no dashboard; a changed domain takes effect at
   the next server restart.
3. No session: every `/api/v1` call answers 401 with `data.login`, and the
   dashboard sends the browser to `/auth/login`. The server sets
   `__Host-aether_signin` (state, PKCE verifier and the browser's device
   cookie when it has one, 10 minutes, SameSite=Lax) and redirects to `https://<edge>/authorize?server=<id>&state=…&challenge=…`.
4. The edge signs the person in, checks the directory, shows the server name
   and id, and redirects to `https://<id>.<server domain>/auth/callback`
   with a one-time code. The return address is computed by the edge, never
   taken from the request.
5. The server redeems the code over its control socket, presenting the
   verifier. The code is useless to anyone else. It receives a grant.
6. The server continues on the browser's device, or creates one, and
   starts a session bound to it. The device is identified by
   `__Host-aether_device` and the session by `__Host-aether_session`, both
   random, HttpOnly, Secure and SameSite=Strict, and stored hashed. The
   Strict device cookie does not survive the edge's cross-site redirect,
   which is why step 3 copies it into the sign-in cookie. A device cookie of
   an unrevoked browser device of the same member continues on that device;
   any other is a new device. Sessions idle out after 30 days. Browser
   devices are listed and revocable beside device keys. State changes and
   WebSocket handshakes without an `Origin` naming the server's host are
   refused. An ended session, a revoked device or a removed member loses
   its WebSockets within 3 seconds; sign-out ends the session only, and
   closes its WebSockets at once.

The Android app opens step 3 in the system browser, because Google refuses
OAuth inside a WebView, adding `return=app`. The edge returns to
`aether://auth/callback?code=…&state=…`, and the app loads the callback on
the saved server's host in its WebView. The code in that link is bound to
the state cookie and verifier held by the WebView, so another app that
intercepts the link cannot use it. It can if the attacker started the
sign-in and talked the victim into confirming it; verified app links would
close that and are not built.

## 6. Authentication and authorization

### Accounts and devices at the edge

- `aether login` runs a device authorization flow: the CLI shows a URL and a
  short code, the person signs in at the edge and confirms the code, the CLI
  receives a device token. Nothing secret is in the URL.
- The device key is a dedicated Ed25519 key in the Aether config directory,
  mode 0600. It is not the person's `~/.ssh` key.
- The token is 32 random bytes, stored hashed at the edge, bound to the
  account and the device key.
- `aether logout` revokes the token. The edge's **Devices** page lists and
  revokes them. Revoking closes that device's live splices.
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
2. The owner runs `aether link --claim <code>` or enters the code on the
   edge's **Add a server** page.
3. The edge forwards the attempt with a grant. The server compares the
   secret itself; the edge holds no copy or hash before the attempt.
4. On success the server creates the admin member with that identity and
   tells the edge. The edge records the owner, and only then treats the
   server as claimed. When recording fails, or the server's answer does not
   arrive, the edge closes the server's control socket; on reconnecting the
   server is told it is unclaimed and drops the owner it recorded, and the
   same account claims it again.

An unclaimed server may hold a control socket and nothing else. Unclaimed
registrations are limited per address and dropped after 30 minutes.

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

- The first device of a member is accepted. It is authorized by the claim
  code or the invitation.
- With `edge-device-approval` on (default), a later device is recorded as
  pending and refused with the command that approves it:
  `aether device approve <code>` from a device that member already uses, an
  admin doing the same, or `sudo aether-server device approve <code>` on the
  server.
- `aether device list`, `aether device revoke`.
- A revoked device still counts as the member's device, so revoking every
  device does not reopen the first-device window. Signing out ends a
  browser's session, not its device: signing in again on that browser
  needs no approval.

### Revocation

| Revoke | Command | Effect |
| --- | --- | --- |
| A member | `aether member remove <id>` | Identity, devices and sessions deleted; directory updated; live channels close at the next revalidation |
| A device or browser | `aether device revoke <id>` | That credential, and a browser's sessions, refused; its connections closed |
| A device token | `aether logout`, edge Devices page | No further splices; live ones closed |
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
| Certificate issuance fails | Dashboard over the edge unavailable; SSH unaffected; retried with backoff |
| Edge signing key lost | Every server refuses the edge with both fingerprints until its operator runs `aether-server edge trust` |
| OAuth provider down | No new sign-ins. Existing device tokens and sessions keep working |
| Host key lost or rotated | New server id. The server must be claimed again and devices re-linked. `docs/edge.md` covers backing up the key |
| Clock skew | Grants allow 30 s |

## 9. Limits

| Limit | Value |
| --- | --- |
| Concurrent SSH connections per server | 48, below sshd's relayed handshake budget |
| Concurrent dashboard connections per server | 48, a budget apart from SSH's, at the edge and at the server |
| Concurrent dashboard connections per server from one address | 16; IPv6 by /64 |
| Connections per device | 16 |
| Unclaimed registrations per address | 3; IPv6 3 per /64, 12 per /56, 48 per /48 |
| Unclaimed registrations per edge | 10000 |
| Open connections to `:443` per address | 1024; IPv6 by /64 |
| Attach deadline | 10 s |
| ClientHello read | 5 s, 16 KiB |
| Sign-in, device-code and claim attempts | rate-limited per address; IPv6 by /64, /56 (4x) and /48 (16x). Device-code entry also per account; token polling per address |
| Monthly egress budget | configured; once reached, every splice together is paced to 256 KiB/s until the UTC month ends |
| Idle dashboard connection at the server | 30 s |

No idle timeout applies to a spliced stream: an idle terminal is legitimate.

## 10. Testing

- Unit tests beside each package.
- `internal/edge/edgetest`: a real edge, a real `sshd` server and a real
  client in one process, with a fake OAuth provider. It covers:
  unauthenticated connect; a member of server A refused on server B; a
  forged, expired, replayed or wrong-server grant; a key that differs from
  the grant; claim with a wrong, expired and exhausted code; invitation
  accept, expiry and revoke; member, device and token revocation closing
  live connections; pending device approval; server reconnect after an edge
  restart; client fallback to a direct address while the edge is down; the
  enrollment signature refused as an SSH host signature.
- The same package drives the dashboard path with a cookie-jar browser
  against the edge's SNI router and servers holding a test CA's
  certificate: sign-in, passthrough by SNI, sessions bound to one server,
  the app return link, pending browsers, revocation and invitations.
- `internal/server`: an integration test with a real edge and a failing ACME
  directory shows certificate failure never stops the server.
- Dashboard: Vitest for the sign-in and device views.
- Android: unit tests for the return link.
- Existing suites unchanged.

## 11. Rollout

1. Owner creates the OAuth applications, DNS records and the VPS unit
   (`packaging/systemd/aether-edge.service`, following the owner checklist in
   `docs/edge.md`). Nothing in this change touches a live host.
2. Release with the edge off on every existing server. `aether-server
   setup` turns it on, with a notice, on hosts without Tailscale and offers
   it on hosts with Tailscale.
3. Per-server hostnames need a wildcard record. They belong on a domain
   separate from `onaether.dev`, so that a Safe Browsing flag on one server
   cannot reach the project's site. Let's Encrypt issues 50 new certificates
   per registered domain per week; the owner requests an increase before
   announcing the feature.
4. Later: a second region, graceful splice hand-over on deploy, Google ID
   tokens verified by the server itself.
