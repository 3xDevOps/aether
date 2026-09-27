# Edge remote access

An **edge** is a relay and sign-in service that Aether servers and clients
both dial out to, so a laptop reaches a server with no Tailscale, no open
router port and no SSH key to copy. The project runs one at
`https://edge.onaether.dev`; `aether-edge` runs your own. Tailnet and direct
SSH keep working unchanged beside it.

Four terms recur below:

- An **account** is a person signed in to the edge with GitHub or Google. It
  is keyed by the provider and the provider's immutable user id; two
  providers with the same email are two accounts.
- A **device** is one client install. It holds a device key (an Ed25519 key
  of its own, not your `~/.ssh` key) and a device token from the edge.
- A **grant** is a statement the edge signs for one connection: "this
  account, on this device, is opening this connection to this server". It
  lives 60 seconds.
- The **directory** is the list of members and open invitations a server
  pushes to the edge, so the edge can refuse strangers early.

## What the edge can and cannot see

The relay never decrypts SSH. It splices two outbound WebSocket connections,
and SSH runs end to end between your client and the server.

| Party | Can | Cannot |
| --- | --- | --- |
| Edge operator, honest | See server ids, account identities, device labels, client IP addresses, timing and byte counts | Read or alter SSH traffic |
| Edge, compromised | Deny service. Forge a grant for any account | Impersonate a server to a client. Claim an unclaimed server. Add a device to a member who has one while device approval is on. Let in an account the server has not made a member |
| Holder of a stolen device token | Open a relayed connection to servers that account reaches | Authenticate to the server: the device key is also required |
| Holder of a stolen claim code | Claim the server with a signed-in account before its owner does | Claim after the code was used, expired (30 minutes) or failed five times |

The server believes the edge about *who* signed in. It does not believe the
edge about *what that person may do*: the server checks membership, role and
device on every connection. Clients check the server's SSH host key against
the server id on every path, so a linked server is pinned, never trusted on
first use.

## Server side

### Turning it on

`aether-server setup` asks about the edge. Without tailscaled it enables the
edge and says so; with tailscaled it asks, defaulting to no. Two config
keys control it:

| Key | Default | Meaning |
| --- | --- | --- |
| `edge-url` | `https://edge.onaether.dev` | Edge to enroll with. `""` turns the edge off |
| `edge-device-approval` | `true` | Hold a member's second and later devices pending until approved |

```sh
sudo aether-server config set edge-url ""     # off
sudo aether-server config set edge-url https://edge.example.com
```

The server keeps one outbound control connection to the edge, reconnecting
with backoff from 1 to 60 seconds. An edge outage never stops the server:
direct and tailnet paths and running work are untouched. The agent honours
`HTTPS_PROXY` from `/etc/aether/aether-server.env`.

### Server id and claiming

The **server id** is derived from the SSH host key
(`<data>/ssh/host_ed25519_key`), 26 base32 characters. Back that key up: a
new host key is a new server, which must be claimed again and linked again
by every client.

Until an account claims it, the edge carries nothing for a server. Setup
prints a **claim code**, `<first 8 characters of the id>-<secret>`, valid
for 30 minutes and five attempts. The owner claims with either:

```sh
aether link --claim <code>
```

or the code on the edge's Add a server page, `<edge>/servers/add`. The
server compares the code itself and makes the claiming account its admin;
the edge holds no copy of the code. A server that already has members is
claimed by an admin who first links their edge account (see
[Linking an existing member](#linking-an-existing-member)).

### Commands

| Command | Does |
| --- | --- |
| `sudo aether-server edge status` | Edge, server id, pinned edge key, owner, claim code state, connection state |
| `sudo aether-server edge claim-code` | Issue a new claim code, replacing the old one |
| `sudo aether-server edge trust` | Fetch the edge's current signing key and pin it after you type `yes` |
| `sudo aether-server edge leave` | Unenroll at the edge, forget the owner, set `edge-url ""` |
| `sudo aether-server device approve <code>` | Approve a member's pending device from the server itself |

The server pins the edge's signing key on first enrollment. A different key
later is refused with both fingerprints; `edge trust` re-pins it after you
have confirmed the change with the edge operator.

### Files

`<data>/edge/` is mode 0700 and every file in it 0600:

| File | Holds |
| --- | --- |
| `edge_key.json` | The pinned edge signing key |
| `owner.json` | The account that claimed the server |
| `claim.json` | The claim code's SHA-256 hash, expiry and attempts left; never the code |
| `status.json` | Last connection state, for `edge status` |
| `lock` | Serializes claim attempts between the server and the commands |

## Client side

### Signing in

```sh
aether login              # prints a URL and a code; confirm the code in the browser
aether servers            # servers your account reaches, with your role
aether logout             # revokes this device's token at the edge
```

`--edge <url>` selects an edge; without it commands use the one edge this
machine is signed in to, else `https://edge.onaether.dev`. The config
directory holds `edge-device-key` (the device key, 0600) and
`edge-tokens.json` (device tokens, 0600). A token never appears in output or
errors. `aether logout` keeps the token when the edge cannot be reached, so
you can retry the revocation; it never deletes the device key.

### Linking

```sh
aether link --claim <code>               # claim a new server and link it
aether link <server name | server id>    # link a server your account reaches
aether link <id> --addr host:2222        # also try this SSH address first
```

A link through the edge stores the server id and the edge URL. With
`--addr`, the client dials that address first (3-second timeout) and falls
back to the edge; when both fail the error names both causes. On either
path the host key must derive the server id, and nothing is read from or
written to `known_hosts`. The device key authenticates on both paths: a
server accepts it directly once the device has connected through the edge
once and is approved.

### Git

A linked repository's `aether` remote uses the logical host
`<server id>.edge.aether.invalid`, which never resolves. Aether's own git
processes set `GIT_SSH_COMMAND` to `aether edge-ssh`. For `git push aether`
typed by hand, `aether link` sets the repository's `core.sshCommand` to the
same command, which handles edge hosts itself and runs the system `ssh`
unchanged for every other host. An existing `core.sshCommand` is never
overwritten; `aether link` prints the line to add instead.

The sync daemon takes the same link: `aether daemon install --server-id <id>
--edge-url <url> [--server host:port]`.

## Members, invitations and devices

### Inviting

```sh
aether invite --github octocat --role collaborator
aether invite --email dev@example.com [--provider github|google] --role viewer
aether invite list
aether invite revoke <invitation-id>
```

No code changes hands. The invitation is in the directory, so the invitee
sees the server in `aether servers` after `aether login`. Their first
connection creates their member with the invited role, binds it to their
account, and uses the invitation up. Email matches only a
provider-verified address. Invitations expire after 7 days. Without
`--github` or `--email`, `aether invite` still mints a one-time invite code
for SSH-key joins.

### Linking an existing member

A member who joined by key or tailnet names their edge account first:

```sh
aether member link --github octocat
```

That account then connects through the edge as the same member. An admin
does this before claiming a server that already has members.

### Devices

A member's first device is accepted. With `edge-device-approval` on, a
later device is recorded as pending and refused with the command that
approves it:

```sh
aether device list                 # yours; an admin sees every member's
aether device approve <code>       # from a device you already use, or as an admin
aether device revoke <device-id>
sudo aether-server device approve <code>   # on the server
```

### Revocation

| Revoke | Command | Effect |
| --- | --- | --- |
| A member | `aether member remove <id>` | Identity and devices deleted, directory updated, live connections closed |
| A device | `aether device revoke <id>` | That device key refused on every path; its connections closed |
| A device token | `aether logout`, or the edge's Devices page | No further relayed connections; live ones closed |
| An invitation | `aether invite revoke <id>` | Removed from the directory |
| A server | `aether-server edge leave`, or the edge's Servers page | Unenrolled; members keep direct and tailnet access |

## Running an edge

`aether-edge serve` is one Linux binary. It listens on `:443` and routes
each TLS connection by SNI without terminating it: the edge's own host name
is served locally with a certificate obtained through TLS-ALPN-01, and
`<server id>.<server domain>` is passed through to that server. Anything
else is closed.

### Before the first start

1. DNS: an A/AAAA record for the edge host, and a wildcard
   `*.<server domain>` pointing at the same machine. Keep the server domain
   separate from your main site's domain.
2. Port 443 reachable from the internet. Nothing else is needed; the
   metrics listener is loopback-only.
3. OAuth applications, one or both:
   - GitHub OAuth app, callback `<origin>/signin/github/callback`, scope
     `user:email`.
   - Google OAuth client, redirect URI `<origin>/signin/google/callback`,
     scopes `openid email profile`.

### Options

Every option is a flag whose default comes from the environment variable
beside it.

| Flag | Environment | Default |
| --- | --- | --- |
| `--origin` | `AETHER_EDGE_ORIGIN` | required: `https://edge.example.com` |
| `--server-domain` | `AETHER_EDGE_SERVER_DOMAIN` | required: `servers.example.com` |
| `--data` | `AETHER_EDGE_DATA` | `/var/lib/aether-edge` |
| `--listen` | `AETHER_EDGE_LISTEN` | `:443` |
| `--metrics-listen` | `AETHER_EDGE_METRICS_LISTEN` | `127.0.0.1:9464`, loopback only |
| `--acme-email` | `AETHER_EDGE_ACME_EMAIL` | none |
| `--acme-directory` | `AETHER_EDGE_ACME_DIRECTORY` | Let's Encrypt production |
| `--egress-budget` | `AETHER_EDGE_EGRESS_BUDGET` | `0` (bytes per UTC month; 0 = none) |
| `--github-client-id` | `AETHER_EDGE_GITHUB_CLIENT_ID` | none |
| `--github-client-secret-file` | `AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE` | none |
| `--google-client-id` | `AETHER_EDGE_GOOGLE_CLIENT_ID` | none |
| `--google-client-secret-file` | `AETHER_EDGE_GOOGLE_CLIENT_SECRET_FILE` | none |
| `--dev-listen` | `AETHER_EDGE_DEV_LISTEN` | none |

Client secrets are never flags, because a flag's value shows in the process
list. Set `AETHER_EDGE_GITHUB_CLIENT_SECRET` and
`AETHER_EDGE_GOOGLE_CLIENT_SECRET`, or point the `*-secret-file` flags at
files, such as systemd credentials:

```ini
[Service]
LoadCredential=github-client-secret:/etc/aether-edge/github-client-secret
ExecStart=/usr/local/bin/aether-edge serve \
  --origin https://edge.example.com --server-domain servers.example.com \
  --github-client-id <client id> \
  --github-client-secret-file ${CREDENTIALS_DIRECTORY}/github-client-secret
```

`--dev-listen 127.0.0.1:8080 --origin http://127.0.0.1:8080` serves plain
HTTP on a loopback address for local testing, with no certificates and no
browser passthrough. Any other address is refused.

### Data directory

| Path | Holds |
| --- | --- |
| `edge.db` | Accounts, sessions, devices, claimed servers and their directories, egress counters. Bearer secrets are stored only as SHA-256 hashes |
| `edge_key` | The Ed25519 key grants are signed with, OpenSSH format, 0600. `ssh-keygen -lf edge_key` prints the fingerprint servers pin. The edge refuses to start if it is readable by group or others |
| `acme/` | The edge host's certificate and ACME account |

Back up `edge_key` and `edge.db`. A lost `edge_key` makes every server
refuse the edge until its operator runs `aether-server edge trust`. A lost
`edge.db` forgets every owner. The edge records an owner only from a claim
it carried, so each server drops its owner when it reconnects, and the
owner claims again with a code from `aether-server edge claim-code`. The
same happens to a server removed on the Servers page while it was offline.

### Operating it

- `GET /healthz` answers `ok`.
- `GET /metrics` on the metrics listener serves servers, relayed
  connections, bytes relayed, this month's egress, whether the budget is
  spent, and refusals by reason, in the Prometheus text format.
- `SIGTERM` or `SIGINT` sends `drain` to every server before closing, so
  servers reconnect with backoff instead of seeing an outage.
- Once the monthly egress budget is spent, every relayed connection is
  paced to 32 KiB/s per direction until the month ends.

## Limits

| Limit | Value |
| --- | --- |
| Concurrent relayed connections per server | 48 |
| Connections per device | 16 |
| Unclaimed servers per client address (IPv6 per /64) | 3, each dropped after 30 minutes |
| Server attaching a connection | 10 seconds, then `server did not attach` |
| TLS ClientHello read | 5 seconds, 16 KiB |
| Sign-in, device codes and claims | rate-limited per address; IPv6 per /64 |
| Grant lifetime | 60 seconds, 30 seconds of clock skew allowed |

No idle timeout applies to a relayed stream: an idle terminal is legitimate.

## Failure modes

| Failure | Behaviour |
| --- | --- |
| Edge down | Servers keep running and retry. Links with `--addr` use it; others fail with the edge's error |
| Edge restart | Relayed connections drop; servers and clients reconnect; runs are unaffected |
| Server offline | `503 server is not connected to the edge` |
| OAuth provider down | No new sign-ins; existing device tokens keep working |
| Host key lost or rotated | New server id: claim again and link again |

The dashboard through the edge, `https://<server id>.<server domain>`, is
not available yet: the relay passes that traffic through, but the server
does not serve it. Use the dashboard over the tailnet or `aether gui`.
