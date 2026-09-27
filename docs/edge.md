# Edge remote access

An **edge** is a relay and sign-in service that Aether servers and clients
both dial out to, so a laptop or phone reaches a server with no Tailscale,
no open router port and no SSH key to copy. The project runs one at
`https://edge.onaether.dev`; `aether-edge` runs your own. Tailnet and direct
SSH keep working unchanged beside it.

Four terms recur below:

- An **account** is a person signed in to the edge with GitHub or Google. It
  is keyed by the provider and the provider's immutable user id; two
  providers with the same email are two accounts.
- A **device** is one client install or one browser. A client install
  holds a device key (an Ed25519 key of its own, not your `~/.ssh` key) and
  a device token from the edge; a browser holds a device cookie and a
  session cookie from the server.
- A **grant** is a statement the edge signs for one connection: "this
  account, on this device, is opening this connection to this server". It
  lives 60 seconds.
- The **directory** is the list of members and open invitations a server
  pushes to the edge, so the edge can refuse strangers early.

## What the edge can and cannot see

The relay never decrypts SSH. It splices two outbound WebSocket connections,
and SSH runs end to end between your client and the server. Dashboard
traffic is TLS that the server itself terminates; the edge routes it by
the server name in the TLS ClientHello.

| Party | Can | Cannot |
| --- | --- | --- |
| Edge operator, honest | See server ids and host names, account identities, device labels, client IP addresses, timing and byte counts | Read or alter SSH or dashboard traffic |
| Edge, compromised | Deny service. Forge a grant for any account. Obtain a certificate for a server's dashboard host name and intercept its browser sessions; the certificate is logged in Certificate Transparency | Read or alter SSH traffic. Impersonate a server to a client. Claim an unclaimed server without its claim code (a code you send through it, it can use for another account). Add a device to a member who has one while device approval is on. Let in an account the server has not made a member |
| Holder of a stolen device token | Open a relayed connection to servers that account reaches | Authenticate to the server: the device key is also required |
| Holder of a stolen claim code | Claim the server with a signed-in account before its owner does | Claim after the code was used, expired (30 minutes) or failed five times |

The server believes the edge about *who* signed in. It does not believe the
edge about *what that person may do*: the server checks membership, role and
device on every connection. Clients check the server's SSH host key against
the server id on every path, so a linked server is pinned, never trusted on
first use. [security.md](security.md#edge-remote-access) has the details.

## Server side

### Turning it on

The edge is off until you turn it on. `aether-server setup` is the one step
that offers it: without tailscaled it enables `https://edge.onaether.dev`
and prints what the edge can see; with tailscaled it asks, defaulting to
no. Otherwise only `aether-server install --edge-url <url>` or the config
key turns it on, so a config without `edge-url`, including one written
before the edge existed, never dials an edge. These config keys control it:

| Key | Default | Meaning |
| --- | --- | --- |
| `edge-url` | empty (off) | Edge to enroll with. Empty turns the edge off |
| `edge-device-approval` | `true` | Hold a member's second and later devices, browsers included, pending until approved |
| `edge-acme-directory` | Let's Encrypt production | ACME directory that issues the [dashboard](#dashboard-through-the-edge) certificate |

```sh
sudo aether-server config set edge-url ""     # off
sudo aether-server config set edge-url https://<edge-host>
```

Restart the server after a change. It keeps one outbound control connection
to the edge, reconnecting with backoff from 1 to 60 seconds. An edge outage
never stops the server: direct and tailnet paths and running work are
untouched. The agent honours `HTTPS_PROXY` from
`/etc/aether/aether-server.env`.

The server keeps the pinned edge key and the owner apart for each edge
([Files](#files)). Pointing `edge-url` at another edge starts clean there:
the server pins that edge's key when it first connects and enrolls
unclaimed. Pointing it back finds the first edge's pin and owner again. To
move an enrolled server to another edge:

```sh
sudo aether-server edge leave                 # unenroll at the old edge, forget its owner
sudo aether-server config set edge-url https://<edge-host>
sudo systemctl restart aether-server
sudo aether-server edge status                # compare the edge key with the one the edge's operator publishes
sudo aether-server edge claim-code
```

### Server id and claiming

The **server id** is derived from the SSH host key
(`<data>/ssh/host_ed25519_key`), 26 base32 characters. Back that key up: a
new host key is a new server, which must be claimed again and linked again
by every client.

Until an account claims it, the edge carries nothing for a server. Setup
prints the edge, the server id and a **claim code**, `<server id>-<secret>`,
valid for 30 minutes and five attempts:

```
edge: https://edge.onaether.dev
server id: <server id>
edge key: pinned when the server first connects; `aether-server edge status` shows it
dashboard: https://<server id>.<the edge's server domain>/; aether-server edge status shows the address once the server connects
claim code: <code> (valid until 3:04PM, 5 attempts)
claim this server with:
  aether link --claim <code>
or enter it at https://edge.onaether.dev/servers/add
```

Start the server first: the edge forwards the claim to the connected server
the code names, which compares the code itself and makes the claiming
account its admin. The edge stores no copy of the code, but the code passes
through it. `aether link --claim` refuses an answer naming any other server
id than the code's. A server that already has members is claimed
by an admin who first links their edge account
([teams.md](teams.md#linking-an-existing-member)).

The edge treats the server as claimed only once it has recorded the owner.
When the server may have accepted a claim the edge could not record, because
recording failed or the server's answer did not arrive within 10 seconds,
the edge disconnects the server and answers:

```
the claim of server <server id> did not complete: <reason>. The edge disconnected the server so that it drops this claim when it reconnects; then claim it again, with a new code from `sudo aether-server edge claim-code` if this one is refused
```

On reconnecting, the edge reports the server unclaimed and the server drops
the owner it recorded. The same account then claims it again.

### Commands

| Command | Does |
| --- | --- |
| `sudo aether-server edge status` | Edge, server id, pinned edge key, owner, claim code state, connection state, dashboard address |
| `sudo aether-server edge claim-code` | Issue a new claim code, replacing the old one |
| `sudo aether-server edge trust` | Fetch the edge's current signing key and pin it after you type `yes` |
| `sudo aether-server edge leave` | Unenroll at the edge, forget the owner, set `edge-url ""` |
| `sudo aether-server device approve <code>` | Approve a member's pending device from the server itself |

The server pins the edge's signing key on first enrollment. A different key
later is refused, and the server keeps retrying with this error in its log
and in `edge status`:

```
edge key changed: pinned SHA256:<old>, edge presents SHA256:<new>; if the edge operator rotated its key, run `aether-server edge trust`
```

Run `edge trust` only after the edge's operator has confirmed the new
fingerprint.

A server id the edge's operator blocked is refused at every enrollment. The
server keeps retrying, and `edge status` shows the edge's reason:

```
connection  disconnected since <time>: read ready: failed to get reader: received close frame: status = StatusPolicyViolation and reason = "server is blocked by this edge's operator"
```

### Files

`<data>/edge/` holds one directory for each edge the server has used,
named after the edge's origin with `://` replaced by `_`, such as
`<data>/edge/https_edge.onaether.dev/`. Every directory is mode 0700 and
every file 0600:

| Path under `<data>/edge/` | Holds |
| --- | --- |
| `<origin>/edge_key.json` | The pinned edge signing key |
| `<origin>/owner.json` | The account that claimed the server at that edge |
| `<origin>/claim.json` | The claim code's SHA-256 hash, expiry and attempts left; never the code |
| `<origin>/status.json` | Last connection state and the edge's server domain, for `edge status` |
| `<origin>/lock` | Serializes claim attempts between the server and the commands |
| `certs/` | The dashboard certificate, its key and the ACME account key |

## Client side

### Signing in

```sh
aether login              # prints a URL and a code; confirm the code in the browser
aether servers            # servers your account reaches, with your role and dashboard address
aether logout             # revokes this device's token at the edge
```

The edge's confirmation page shows the address the sign-in started from and
whether it is the browser's own; confirm only a code you started.

`--edge <url>` selects an edge; without it commands use the one edge this
machine is signed in to, else `https://edge.onaether.dev`. The config
directory holds `edge-device-key` (the device key, 0600),
`edge-tokens.json` (device tokens, 0600) and `edge.lock`, which serializes
concurrent sign-ins. A key or token file that group or others can read is
refused with the `chmod 600 <path>` to run (not on Windows). A token never
appears in output or errors. `aether logout` keeps the token when the edge
cannot be reached, so you can retry the revocation; it never deletes the
device key.

### Linking

```sh
aether link --claim <code>               # claim a new server and link it
aether link <server id>                  # link a server from aether servers
aether link <server id> --addr host:2222 # also try this SSH address first
```

Only a 26-character server id goes through the edge. Any other argument,
including a bare name such as `my-server`, is an SSH address and never
reaches the edge: server names are chosen by each server's admin, and
`aether servers` also lists servers you are only invited to.

A link through the edge stores the server id and the edge URL. With
`--addr`, the client dials that address first (3-second timeout) and falls
back to the edge; when both fail the error names both causes. When only
the address fails, its error goes to stderr followed by `aether: reached
server <id> through <edge> instead`. On either
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
unchanged for every other host. The command names `aether` by its `PATH`
entry when that entry is the running binary, so an upgrade that moves the
real file does not break it. An existing `core.sshCommand` is overwritten
only when it is exactly `<path to aether> edge-ssh` from an earlier link;
any other value is left alone and `aether link` prints the line to add
instead.

The sync daemon takes the same link: `aether daemon install --server-id <id>
--edge-url <url> [--server host:port]`.

## Members, invitations and devices

Invitations by GitHub login or email, device approval and revocation are
in [teams.md](teams.md#through-an-edge).

## Dashboard through the edge

A browser reaches the dashboard at `https://<server id>.<server domain>/`.
`aether servers` prints that address in its `DASHBOARD` column, and
`aether-server edge status` on its `dashboard` line. The edge's server list,
`GET /v1/servers`, carries the domain as `server_domain`; an edge older
than that field leaves it out, and `aether servers` prints `-`. The server
learns the server domain from the edge when it enrolls, then
serves the dashboard on connections the edge passes through, with a
certificate for exactly that host name. The server logs both parts when it
connects:

```sh
journalctl -u aether-server | grep 'edge: connected'
# ... INFO edge: connected edge=https://edge.onaether.dev server_id=<server id> state=claimed server_domain=<server domain>
```

An edge that announces no server domain passes no dashboard through; SSH
through it still works. If the edge later announces a different domain,
restart `aether-server` to serve the dashboard there.

The certificate is issued with TLS-ALPN-01 through the same passthrough,
starting once the server is claimed: at an enrollment the edge answers as
claimed, or when a claim succeeds. An unclaimed server requests nothing
from the CA. The certificate is cached in
`<data>/edge/certs/`. A failed issuance is retried after 1 minute, doubling
to 1 hour. Until it succeeds, browser handshakes are refused; SSH is never
affected.

Signing in:

1. Without a session, every `/api/v1` call answers 401 and the dashboard
   links to `/auth/login`.
2. `/auth/login` sets `__Host-aether_signin` (SameSite=Lax, 10 minutes)
   holding a fresh state, a PKCE verifier and the browser's device token
   if it has one, and redirects to `<edge>/authorize`.
3. The edge signs you in, checks the server's directory, shows the server's
   name and id, and on **Continue** returns to
   `https://<server id>.<server domain>/auth/callback` with a one-time code.
   The code is bound to that server and the PKCE challenge and expires
   after 2 minutes.
4. The server checks the state against its cookie, redeems the code with
   the verifier over its control connection, maps the account to a member
   (accepting an open invitation, as SSH does) and sets
   `__Host-aether_device` and `__Host-aether_session` (both HttpOnly,
   Secure, SameSite=Strict).

`__Host-aether_device` makes the browser a device of that member:
`aether device list` shows it and `aether device revoke` revokes it. The
edge's redirect to the callback is cross-site, so the browser does not send
this Strict cookie there; the device token rides in the Lax sign-in cookie
instead. `__Host-aether_session` is one sign-in on that device and expires
after 30 days without use. The server stores only the SHA-256 hash of
either. A second browser of a member waits for approval like any later
device, and the dashboard shows the approval commands. Signing in again on
the same browser continues on its device and needs no new approval; a
device cookie of another member, or of a revoked device, gets a new device.
**Sign out** (`POST /auth/logout`) ends the session and closes its live
connections; the device and its approval stay. Revoking the device ends
every session on it.

The Android app opens step 3 in the system browser, because Google refuses
OAuth inside a WebView. The edge then returns to `aether://auth/callback`,
and the app loads the server's callback in its WebView, which holds the
state cookie. For a sign-in the app started, another app that intercepts
the link cannot use the code without that cookie and the verifier. For a
sign-in someone else started and sent you as an edge link, the cookie and
verifier are theirs: if you confirm it, an app on your phone that claims
`aether://` can hand them the code, and they are signed in as you.
Confirm only a sign-in you started; the edge's confirmation page names the
server and says it returns to the Aether app.

## Running an edge

`aether-edge serve` is one static Linux binary (amd64 or arm64) with an
embedded SQLite store. It owns `:443`: it reads each connection's TLS
ClientHello without terminating it, serves the edge's own host name
locally, passes `<server id>.<server domain>` through to that server, and
closes anything else. Every value you supply below is written as a
placeholder: `<edge-host>` is the edge's public host name,
`<server-domain>` the domain the dashboard host names live under.

### What it needs

- A Linux host with systemd 248 or newer, a public IPv4 address (IPv6
  optional), and `:443` on that address free for the edge alone. The router
  needs the raw TLS stream and the real client address, so it cannot sit
  behind another reverse proxy or load balancer on the same address.
- Port 80 is not used: certificates come through TLS-ALPN-01 on `:443`.
- Outbound HTTPS to Let's Encrypt, `github.com` and `api.github.com`, and
  `oauth2.googleapis.com` and `openidconnect.googleapis.com` for the
  providers you enable.
- Two DNS names on two registrable domains: `<edge-host>` and a wildcard
  for `<server-domain>` ([DNS](#dns)).
- A GitHub OAuth app, a Google OAuth client, or both.
- Memory for about 64 KiB of copy buffers per open relayed connection, plus
  socket buffers. Relaying is byte copying, so egress bandwidth is what an
  edge runs out of first ([egress budget](#egress-budget)).

### OAuth applications

The edge asks each provider only for the account's identity and verified
email. The callback addresses are fixed by the code; each must match
exactly, scheme and path included.

**GitHub.** In the account or organization that should own it, open
Settings > Developer settings > OAuth Apps > New OAuth App:

| Field | Value |
| --- | --- |
| Application name | Shown on GitHub's consent page |
| Homepage URL | `https://<edge-host>` |
| Authorization callback URL | `https://<edge-host>/signin/github/callback` |
| Enable Device Flow | Off; the edge runs its own device flow |

The edge requests the scope `user:email`; nothing is configured on GitHub's
side for it. Generate a client secret and keep it for
[Installing](#installing).

**Google.** In a Google Cloud project, configure the OAuth consent screen
(Google Auth Platform): user type External, the app name and support email,
`<edge-host>`'s registrable domain as an authorized domain, and a privacy
policy URL. Add the scopes `openid`, `email` and `profile`; they are
non-sensitive, so they need no scope verification. Publish the app:
in Testing status only listed test users can sign in. Then create an OAuth
client ID:

| Field | Value |
| --- | --- |
| Application type | Web application |
| Authorized redirect URI | `https://<edge-host>/signin/google/callback` |

No JavaScript origin is needed. Keep the client id and secret.

### DNS

| Name | Type | Value |
| --- | --- | --- |
| `<edge-host>` | A (and AAAA for IPv6) | The edge's address |
| `*.<server-domain>` | A (and AAAA) | The same address |
| `<server-domain>` | CAA | `0 issue "letsencrypt.org"` and `0 issuewild ";"` |
| `<edge-host>` | CAA | `0 issue "letsencrypt.org"` |

- **Keep `<server-domain>` on its own registrable domain**, never a
  subdomain of `<edge-host>`'s domain or of your website's. Every server's
  dashboard is a host name under it. A browser safety list that flags one
  server's page, and Let's Encrypt's limit of new certificates per
  registered domain ([Certificate rate limits](#certificate-rate-limits)),
  then reach only the other servers' names, not your edge or your site.
- **Use A/AAAA records for the wildcard, not a CNAME**: a CA follows a CNAME
  for its CAA lookup, and the target's CAA policy would then apply.
- **Leave `accounturi` out of CAA.** Every server registers its own ACME
  account with the key in its `<data>/edge/certs/`, so no single account
  covers the server domain; the edge's account is created on first start and
  replaced whenever `acme/` is lost. `issuewild ";"` refuses wildcard
  certificates for the server domain, which nothing legitimate needs. A CAA
  record on `<edge-host>` itself leaves your other names under that domain
  to their own CA.
- **Behind a proxying CDN, set every record above to DNS only.** A proxy
  terminates TLS itself, which breaks routing by ClientHello, TLS-ALPN-01,
  and the server's end-to-end dashboard TLS, and would give the CDN the
  dashboard's plaintext.

### TLS certificates

The edge obtains the certificate for exactly `<edge-host>` from the ACME
directory in `--acme-directory` with TLS-ALPN-01, on the first handshake
for that name, and caches it in `<data>/acme/`. Each server obtains its own
dashboard certificate the same way through the passthrough; the edge never
holds a server's key.

Port 80 stays closed. On a TLD in the browsers' HSTS preload list, such as
`.dev` or `.app`, a browser never tries `http://` for any name, so nothing
is lost; choose such a TLD for `<server-domain>` too, since people type
those names on phones.

The certificate library (`golang.org/x/crypto/acme/autocert`) renews 30 days
before expiry and reports a failed renewal nowhere: it logs nothing and
retries later. Let's Encrypt no longer emails expiry warnings either. Probe
the expiry from outside ([Monitoring](#monitoring)).

### Installing

Download the binary for the host's architecture from a release, check it,
and install it with the unit and the environment file. `<release-tag>` is a
tag such as the one `/releases/latest` redirects to; `<arch>` is `amd64` or
`arm64`.

```sh
base=https://github.com/3xDevOps/Aether/releases/download/<release-tag>
curl -fLO "$base/aether-edge-linux-<arch>"
curl -fLO "$base/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
sudo install -m 0755 aether-edge-linux-<arch> /usr/local/bin/aether-edge
aether-edge version

src=https://raw.githubusercontent.com/3xDevOps/Aether/<release-tag>/packaging
sudo curl -fsSL -o /etc/systemd/system/aether-edge.service "$src/systemd/aether-edge.service"
sudo useradd --system --home-dir /var/lib/aether-edge --no-create-home --shell /usr/sbin/nologin aether-edge
sudo install -d -m 0700 /etc/aether-edge
sudo curl -fsSL -o /etc/aether-edge/aether-edge.env "$src/edge/aether-edge.env.example"
sudo chmod 0600 /etc/aether-edge/aether-edge.env
```

The secrets go in root-only files, one for each provider you offer; set
only those providers' client ids in the environment file. Each command
reads the secret from the terminal, so it lands in no shell history; paste
it, then press Ctrl-D:

```sh
sudo sh -c 'umask 077 && cat > /etc/aether-edge/github-client-secret'
sudo sh -c 'umask 077 && cat > /etc/aether-edge/google-client-secret'
```

[`packaging/systemd/aether-edge.service`](../packaging/systemd/aether-edge.service)
runs the edge as the `aether-edge` user with only `CAP_NET_BIND_SERVICE`,
a read-only system, no home directories, private `/tmp` and devices, and
`/var/lib/aether-edge` as its only writable path. It passes each secret file
with `LoadCredential=`: systemd reads the file as root and gives the
service a private copy, and the unit points
`AETHER_EDGE_*_CLIENT_SECRET_FILE` at it. For a provider without a file,
the unit's `SetCredential=` supplies a single newline instead, which the
edge reads as no secret, so an edge offering one provider runs the unit
unchanged. A client id whose secret file is missing still fails the start:

```
aether-edge: --google-client-id is set but /run/credentials/aether-edge.service/google-client-secret, named by --google-client-secret-file, holds no secret
```

The unit restarts the edge 5 seconds after any exit, allows 1048576 open
files, and gives a stop 30 seconds.

### Configuration

Every option is a flag whose default comes from the environment variable
beside it. The unit starts `aether-edge serve` with no flags, so the
environment file
([`packaging/edge/aether-edge.env.example`](../packaging/edge/aether-edge.env.example))
is the whole configuration. Replace every `<...>` in it.

| Flag | Environment | Default |
| --- | --- | --- |
| `--origin` | `AETHER_EDGE_ORIGIN` | required: `https://<edge-host>` |
| `--server-domain` | `AETHER_EDGE_SERVER_DOMAIN` | required: `<server-domain>` |
| `--data` | `AETHER_EDGE_DATA` | `/var/lib/aether-edge` |
| `--listen` | `AETHER_EDGE_LISTEN` | `:443` |
| `--metrics-listen` | `AETHER_EDGE_METRICS_LISTEN` | `127.0.0.1:9464`, loopback only |
| `--acme-email` | `AETHER_EDGE_ACME_EMAIL` | none |
| `--acme-directory` | `AETHER_EDGE_ACME_DIRECTORY` | `https://acme-v02.api.letsencrypt.org/directory` |
| `--egress-budget` | `AETHER_EDGE_EGRESS_BUDGET` | `0`: bytes per UTC month, a plain integer; 0 = none |
| `--github-client-id` | `AETHER_EDGE_GITHUB_CLIENT_ID` | none |
| `--github-client-secret-file` | `AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE` | none |
| `--google-client-id` | `AETHER_EDGE_GOOGLE_CLIENT_ID` | none |
| `--google-client-secret-file` | `AETHER_EDGE_GOOGLE_CLIENT_SECRET_FILE` | none |
| `--dev-listen` | `AETHER_EDGE_DEV_LISTEN` | none |

Client secrets are never flags, because a flag's value shows in the process
list. They come from `AETHER_EDGE_GITHUB_CLIENT_SECRET` and
`AETHER_EDGE_GOOGLE_CLIENT_SECRET`, or from the files the `*-secret-file`
options name; setting both for one provider is refused. A secret file that
holds only white space holds no secret. A provider is offered when it has
both a client id and a secret. Without the unit, keep either in a root-only
file.

The edge checks its options before it binds anything. Among the errors:

```
aether-edge: edge: no sign-in provider is configured; configure a GitHub or Google OAuth application
aether-edge: --github-client-id is set but its secret is not; set AETHER_EDGE_GITHUB_CLIENT_SECRET or --github-client-secret-file
aether-edge: --egress-budget "10G" is not a byte count
aether-edge: --origin must be https://host[:port], not "edge.example.com"; for a local plain-HTTP edge use --dev-listen
aether-edge: --origin: edgeproto: edge url "https://<edge-host>": host "<edge-host>" is not a DNS name or an IP address
```

`--dev-listen 127.0.0.1:8080 --origin http://127.0.0.1:8080` serves plain
HTTP on a loopback address for local testing, with no certificates and no
dashboard passthrough. Any other address is refused.

### Firewall

Inbound, allow TCP 443 on IPv4 and IPv6 and your own administration access;
refuse everything else. The metrics listener is loopback-only and needs no
rule. With `ufw`:

```sh
sudo ufw default deny incoming
sudo ufw allow from <admin-address> to any port 22 proto tcp
sudo ufw allow 443/tcp
sudo ufw enable
```

### First start and checks

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now aether-edge
journalctl -u aether-edge -f
```

The start line names what it serves:

```
INFO aether-edge: serving origin=https://<edge-host> server_domain=<server-domain> metrics=127.0.0.1:9464
```

Then check, in order:

1. **Health and certificate.** From another machine,
   `curl -fsS https://<edge-host>/healthz` prints `ok`. The first request
   obtains the certificate, so it can take a few seconds.
2. **The signing key.** On the edge,
   `sudo ssh-keygen -lf /var/lib/aether-edge/edge_key` prints the
   fingerprint servers pin. Publish it where server operators can compare it
   during `aether-server edge trust`, and back the key up now
   ([Backup and recovery](#backup-and-recovery)).
3. **A test sign-in.** Open `https://<edge-host>/signin` and sign in with
   each provider you configured; the Servers page opens. On a laptop,
   `aether login --edge https://<edge-host>`, confirm the code, then
   `aether servers --edge https://<edge-host>` prints
   `no servers yet on <edge-host>; claim one with: aether link --claim <code>`.
4. **A canary server.** On a separate server machine with `aether-server`
   installed:

   ```sh
   sudo aether-server install --edge-url https://<edge-host>
   sudo systemctl daemon-reload && sudo systemctl enable --now aether-server
   sudo aether-server edge claim-code
   ```

   A server already enrolled with another edge moves as in
   [Turning it on](#turning-it-on).

   On the laptop, `aether link --claim <code> --edge https://<edge-host>`
   with the code it printed, then `aether runs`. Open `https://<canary server id>.<server-domain>/` in
   a browser and sign in. Keep this server running: monitoring probes it.

### Monitoring

`GET /metrics` on the metrics listener serves, in the Prometheus text
format:

| Metric | Meaning |
| --- | --- |
| `aether_edge_servers{state="claimed"}`, `{state="unclaimed"}` | Servers holding a control connection |
| `aether_edge_splices` | Relayed connections with both ends attached |
| `aether_edge_relayed_bytes_total` | Bytes relayed since the edge started |
| `aether_edge_egress_month_bytes` | Bytes relayed this UTC month, the figure the budget counts |
| `aether_edge_throttled` | `1` once the monthly egress budget is spent |
| `aether_edge_refusals_total{reason="..."}` | Refused connections and enrollments by reason, such as `unknown host`, `upgrade required` or `too many connections from one address` |

Scrape it from the edge host itself, or over an SSH tunnel; it is never
exposed. Alert on:

- `aether_edge_throttled` equal to 1, and on `aether_edge_egress_month_bytes`
  passing a fraction of the budget you choose.
- A fall in `aether_edge_servers{state="claimed"}` to near zero, which
  means servers cannot enroll.
- A sustained rise in one refusal reason.

And probe from outside the edge's network, so DNS and routing are tested
too:

```sh
curl -fsS https://<edge-host>/healthz
# the canary's dashboard answers 401 before sign-in: passthrough and its certificate work
curl -s -o /dev/null -w '%{http_code}\n' https://<canary server id>.<server-domain>/api/v1/capabilities
# fails when fewer than 21 days remain, a week after renewal should have happened
for host in <edge-host> <canary server id>.<server-domain>; do
  echo | openssl s_client -connect "$host:443" -servername "$host" 2>/dev/null |
    openssl x509 -noout -checkend 1814400 || echo "certificate for $host expires within 21 days"
done
```

### Logs

The edge logs to standard error, which the unit sends to the journal:
`journalctl -u aether-edge`. Entries carry server ids and, for refused
enrollments (`relay: enrollment refused client=<address>`) and failed TLS
handshakes (`http: TLS handshake error from <address>:<port>`), client IP
addresses. A page or API request that fails on the edge's side, such as a
database error, or at the sign-in provider logs
`edge: request failed route=<route> status=<5xx> error=<error>`; refusals
the client can fix, such as a revoked token or a wrong code, are not
logged. Client addresses are personal data. journald keeps entries until
its size limits push them out, which can be months on a quiet host, so set a
retention period and state it in your privacy notice:

```sh
sudo install -d /etc/systemd/journald.conf.d
printf '[Journal]\nMaxRetentionSec=<retention such as 14day>\n' |
  sudo tee /etc/systemd/journald.conf.d/retention.conf
sudo systemctl restart systemd-journald
```

That setting applies to the host's whole journal. What the database keeps,
and for how long, is in [privacy.md](privacy.md#what-an-edge-stores).

### Backup and recovery

The data directory, `/var/lib/aether-edge`:

| Path | Holds |
| --- | --- |
| `edge.db` (with `edge.db-wal`, `edge.db-shm`) | Accounts, edge sessions, devices, claimed servers and their directories, egress counters, blocked server ids and accounts. Bearer secrets are stored only as SHA-256 hashes |
| `edge_key` | The Ed25519 key grants are signed with, OpenSSH format, 0600. The edge refuses to start if group or others can read it: `edge: signing key <path> has mode 0644; run chmod 600 <path>` |
| `acme/` | The edge host's certificate, its key and the ACME account key |

Keep three things:

- **`edge_key`, once, offline**, right after the first start. It never
  changes. Every server pins it. If it is lost, the edge makes a new one
  and **every server refuses the edge** with `edge key changed` until its
  operator runs `sudo aether-server edge trust`, compares the new
  fingerprint with the one you publish, and types `yes`. Until then nobody
  reaches those servers through the edge. Clients need no action.
- **`edge.db`, daily.** `.backup` copies a consistent snapshot while the
  edge runs (needs the `sqlite3` package):

  ```sh
  sudo -u aether-edge sqlite3 /var/lib/aether-edge/edge.db ".backup /var/lib/aether-edge/edge-backup.db"
  ```

  Copy `edge-backup.db` off the machine. A lost `edge.db` forgets every
  account, device token and owner: each client runs `aether login` again,
  and each server drops its owner when it reconnects and logs
  ``edge: the edge does not know this server's owner; claim it again with a code from `aether-server edge claim-code` ``.
  Members, roles and devices live on the servers and survive. Restoring an
  older backup also brings back device tokens revoked since it was taken; a
  device a member revoked on a server stays revoked there.
- **`acme/`**, optional. Without it the edge registers a new ACME account and
  requests a new certificate, which counts against Let's Encrypt's limits.

### Upgrades

```sh
sudo -u aether-edge sqlite3 /var/lib/aether-edge/edge.db ".backup /var/lib/aether-edge/edge-pre-<release-tag>.db"
# download and check the new binary as in Installing, then:
sudo install -m 0755 aether-edge-linux-<arch> /usr/local/bin/aether-edge
sudo systemctl restart aether-edge
```

On `SIGTERM` the edge sends `drain` to every server, then closes within 10
seconds. Every relayed connection drops; servers reconnect with backoff,
clients redial, and runs on the servers are unaffected. The database schema
migrates forward on start. An older binary refuses a migrated database with
`edgestore: database schema version <n> is newer than this binary supports (<m>)`;
to roll back, restore the backup taken before the upgrade.

Every handshake carries a protocol version. The edge accepts any server or
client at or above its minimum version, including newer ones, which speak
down to the edge. An older one is refused with `upgrade required: <minimum>`
(HTTP 426 for clients). Protocol version 1 is the only version and the
minimum; a release that raises the minimum says so in its notes, so upgrade
servers and clients before the edge in that case.

### Certificate rate limits

Let's Encrypt issues at most 50 new certificates per registered domain every
7 days. Every newly claimed server needs one for `<server-domain>`; the
limit does not block renewing an existing one. A server over the limit logs
`servergw: dashboard certificate for the edge not issued`, retries with its
backoff, and keeps SSH through the edge. Before inviting more than a few
dozen new servers a week, request a higher limit for `<server-domain>` with
the form linked from <https://letsencrypt.org/docs/rate-limits/>.

### Operator commands

Run them as the edge's user. They refuse any other user than the owner of
`edge.db`, because SQLite creates its `-wal` and `-shm` files as whoever
opens the database, and a root-owned one would lock the edge out.
`--data` (default `AETHER_EDGE_DATA`, else `/var/lib/aether-edge`) names the
data directory; flags go before the argument.

```sh
sudo -u aether-edge aether-edge servers list
sudo -u aether-edge aether-edge servers remove <server id>
sudo -u aether-edge aether-edge servers block <server id>
sudo -u aether-edge aether-edge servers unblock <server id>
sudo -u aether-edge aether-edge accounts list
sudo -u aether-edge aether-edge accounts block github:<user id>
sudo -u aether-edge aether-edge accounts unblock github:<user id>
sudo -u aether-edge aether-edge accounts delete google:<subject>
```

An account is `<provider>:<subject>`, the provider's immutable user id, as
the `ACCOUNT` column of `accounts list` prints it; `servers list` names each
owner the same way.

| Command | Effect |
| --- | --- |
| `servers remove` | Forgets the server's claim and directory. New SSH and dashboard connections are refused. The server learns it is unclaimed the next time it connects, drops its owner, and can be claimed again with a new code |
| `servers block` | Does what `remove` does, and refuses the server id at enrollment with `server is blocked by this edge's operator` |
| `servers unblock` | The id can enroll and be claimed again |
| `accounts block` | Deletes the account's edge sessions, device tokens and pending device sign-ins, and refuses its sign-ins with `account is blocked by this edge's operator`. An account that never signed in can be blocked too. Servers it owns stay claimed and their members keep access |
| `accounts unblock` | The account can sign in again; its devices sign in anew |
| `accounts delete` | Deletes the account with its sessions, devices, the servers it owns (claim and directory) and the entries of other servers' directories that name it: its memberships, and invitations to its login or email. A block stays, holding only the provider and subject |

The commands are safe while the edge runs: each change is one SQLite
transaction, and the edge reads blocks and claims from the database at
every enrollment, sign-in, connection and dashboard passthrough.
Connections already open, and a removed server's control connection, stay
up until they end or the edge restarts; `sudo systemctl restart aether-edge`
ends them, with every other relayed connection.

A server id is derived from a host key anyone can generate, so blocking an
id stops that one server; its operator can enroll another under a new key.
Likewise a blocked person can sign in with another GitHub or Google account.

For a deletion request, `accounts delete` is the whole edge side. A server
sends its directory again whenever it changes, so a membership or invitation
of the person comes back until an admin of that server removes it there
(`aether member remove`, `aether invite revoke`). The edge's log keeps what
it logged for its [retention period](#logs). A server the account owned
keeps running; its admins claim it again with a new code.

### Abuse

Anyone who signs in can claim a server, and a claimed server's dashboard
host name, `<server id>.<server-domain>`, serves whatever that server sends
under a valid certificate. An abuse report names such a host. Publish an
abuse contact, and act on a report in two steps:

1. **Stop the name resolving.** Add a TXT record for exactly
   `<server id>.<server-domain>`. A name that exists is no longer covered by
   the wildcard, so it gets no address; browsers holding it cached stop
   within the record's old TTL.
2. **Block the server**, and its owner if the report warrants it
   ([Operator commands](#operator-commands)):

   ```sh
   sudo -u aether-edge aether-edge servers list
   sudo -u aether-edge aether-edge servers block <server id>
   sudo -u aether-edge aether-edge accounts block <owner account>
   ```

   The edge passes nothing more through to that server and refuses its id
   when it next enrolls. Its operator can start a new server with a new host
   key, a new id and a new dashboard name; blocking the owner stops that
   account from claiming one.

### Egress budget

`--egress-budget` counts the bytes the relay copies, in both directions,
per UTC calendar month. It does not count TLS and WebSocket framing, the
edge's own pages and API, or certificate traffic, so a provider meters more
than `aether_edge_egress_month_bytes` shows. Once the month's count reaches
the budget, every relayed connection together is paced to 256 KiB/s until
the next month; nothing is cut off. SSH and dashboard passthrough each get
half, so passthrough, which needs no sign-in, cannot slow SSH. Within each
half, servers take turns one read of at most 4 KiB at a time: a read waits
behind at most one read of each other busy server, however many
connections that server holds. At that rate a 31-day month adds at most
about 650 GiB after the budget is spent, and a crash loses up to one minute
of counting. So for a provider ceiling of C bytes a month, set the budget
below C minus 700 GiB (751619276800 bytes), with margin for framing. The
count survives restarts and resets at the month boundary.

## Limits

| Limit | Value |
| --- | --- |
| Concurrent SSH connections per server | 48 |
| Concurrent dashboard connections per server | 48, a budget apart from SSH's |
| Concurrent dashboard connections per server from one client address (IPv6 per /64) | 16 |
| Connections per device | 16 |
| Unclaimed servers per client address | 3, each dropped after 30 minutes; for IPv6, 3 per /64, 12 per /56 and 48 per /48 |
| Unclaimed servers per edge | 10000 |
| Open connections to `:443` per client address (IPv6 per /64) | 1024 |
| Server attaching a connection | 10 seconds, then `server did not attach` |
| TLS ClientHello read | 5 seconds, 16 KiB |
| Sign-in, device codes and claims | rate-limited per address. An IPv6 address counts against its /64, its /56 with 4 times the budget, and its /48 with 16 times; a request passes only when all three have budget left. Device-code entry is also limited per account, and device-token polling per address. Each limit remembers 65536 address blocks; past that, a new block replaces the one with the most budget left among 64 sampled |
| Directory stores per server | 1 per 5 seconds; a push in between waits, and only the latest waiting push is stored |
| Grant lifetime | 60 seconds, 30 seconds of clock skew allowed |
| Login and email matching invitations | 24 hours after the account's last provider sign-in; an edge page opened later signs in with the provider again |

No idle timeout applies to a relayed stream: an idle terminal is legitimate.

## Failure modes

| Failure | Behaviour |
| --- | --- |
| Edge down | Servers keep running and retry. Links with `--addr` use it; others fail with the edge's error |
| Edge restart | Relayed connections drop; servers and clients reconnect; runs are unaffected |
| Server offline | `503 server is not connected to the edge` |
| Claim did not complete | The edge disconnects the server; after it reconnects, claim it again ([Server id and claiming](#server-id-and-claiming)) |
| OAuth provider down | No new sign-ins; existing device tokens and dashboard sessions keep working, but invitations stop matching accounts whose last sign-in is over 24 hours old, and edge pages opened after that fail at the provider |
| Host key lost or rotated | New server id: claim again and link again |
| Edge signing key lost | Every server refuses the edge until `aether-server edge trust` |
| Dashboard certificate not issued | Dashboard through the edge unavailable; SSH unaffected; retried with backoff |

## Owner checklist: edge.onaether.dev

The project's own edge, in order. Values in `<...>` are the facts below,
still to be decided.

**Facts still needed about the host and accounts:**

- VPS provider and region.
- OS and its systemd version (248 or newer).
- CPU architecture (`amd64` or `arm64`).
- Whether the box is dedicated to the edge, and whether `:80` and `:443`
  are free on its IPv4 and IPv6 addresses.
- The IPv4 and IPv6 addresses: `<vps-ipv4>`, `<vps-ipv6>`.
- The DNS host of `onaether.dev`, whether it proxies records, and any CAA
  records already on `onaether.dev`.
- The server domain, `<server-domain>`: a registrable domain other than
  `onaether.dev`, preferably on an HSTS-preloaded TLD, and its DNS host.
- The provider's monthly egress ceiling, for `<egress-budget-bytes>`.
- An ACME contact address, `<acme-contact-email>`, and an abuse and
  privacy contact address.
- The log retention period for the privacy notice.
- The privacy policy URL, `<privacy-policy-url>`, for Google's consent
  screen; Google can require it on an authorized domain such as
  `onaether.dev`.
- The GitHub account or organization that owns the OAuth app, and the
  Google Cloud project for the OAuth client.

**Steps:**

1. Register `<server-domain>`.
2. Create the GitHub OAuth app: homepage `https://edge.onaether.dev`,
   callback `https://edge.onaether.dev/signin/github/callback`, Device Flow
   off. Generate a client secret.
3. Configure the Google consent screen (External; authorized domain
   `onaether.dev`; privacy policy `<privacy-policy-url>`; scopes `openid`,
   `email`, `profile`), publish it, and create a Web application client with
   redirect URI `https://edge.onaether.dev/signin/google/callback`.
4. DNS, every record DNS only:
   - `edge.onaether.dev` A `<vps-ipv4>`, AAAA `<vps-ipv6>`
   - `*.<server-domain>` A `<vps-ipv4>`, AAAA `<vps-ipv6>`
   - `edge.onaether.dev` CAA `0 issue "letsencrypt.org"`
   - `<server-domain>` CAA `0 issue "letsencrypt.org"` and
     `0 issuewild ";"`
5. On the VPS, install the binary, unit, user, environment file and secret
   files as in [Installing](#installing), with
   `AETHER_EDGE_ORIGIN=https://edge.onaether.dev`,
   `AETHER_EDGE_SERVER_DOMAIN=<server-domain>`,
   `AETHER_EDGE_ACME_EMAIL=<acme-contact-email>`,
   `AETHER_EDGE_EGRESS_BUDGET=<egress-budget-bytes>` and both client ids.
6. Apply the [firewall](#firewall) and the [log retention](#logs).
7. `sudo systemctl enable --now aether-edge`, then run
   [every check](#first-start-and-checks), with a canary server claimed by
   a project account.
8. Record `sudo ssh-keygen -lf /var/lib/aether-edge/edge_key`, add the
   fingerprint to this page, and store `edge_key` offline.
9. Install `sqlite3` and schedule the daily `edge.db` backup off the
   machine.
10. Set up the external probes and metric alerts in
    [Monitoring](#monitoring).
11. Request a higher Let's Encrypt limit for `<server-domain>`.
12. Before announcing: put the abuse contact on this page and the privacy
    contact, retention period and account-deletion route in
    [privacy.md](privacy.md#what-an-edge-stores).
