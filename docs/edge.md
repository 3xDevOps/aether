# Edge remote access

An **edge** is a relay that lets members reach a server through a public host
name with a GitHub sign-in. The server and the client both dial out to it, so
a laptop reaches a server with no Tailscale, no open router port and no SSH
key to copy. The project runs one at `https://edge.onaether.dev`, and
`aether-edge` runs your own.

- [Using an edge](#using-an-edge) is for members and server administrators.
- [Running your own edge](#running-your-own-edge) is for operators.

Other ways to reach a server are in [networking.md](networking.md).

## Using an edge

### How it works

```mermaid
flowchart LR
    C[Client] -->|dials out| E[Edge]
    S[Server] -->|dials out| E
    C -. SSH, end to end .- S
    G[GitHub] -->|who signed in| E
```

The edge splices the two outbound connections. SSH runs end to end between
client and server, so the edge cannot read it. The edge says who signed in; the
server alone decides what that person may do. Tailnet identities, member SSH
keys, invite codes and `aether gui` keep working beside the edge.

- An **account** is a person signed in to the edge with GitHub, identified by
  GitHub's numeric user id. A login or email does not identify an account.
- A **device** is one client install. It holds its own device key (not your
  `~/.ssh` key) and a device token from the edge.
- The **server id** is 26 characters derived from the server's SSH host key.
  Clients check the host key against it on every connection.
- The **owner** is the account that claimed the server at the edge.
- The **access policy** is the server's `edge-access` setting
  ([Access policies](#access-policies)).
- An **invitation** names a GitHub account that may join with a role. Its
  **join link** carries a secret the server checks and the edge never sees.

On the project's edge, `https://auth.onaether.dev` serves sign-in and the
Devices, Servers and Account pages, and `https://edge.onaether.dev` serves the
relay. Clients and servers are configured with the relay name. A path asked for
on the wrong host gets `421 Misdirected Request`.

### Turning it on

The edge is off until you turn it on. `aether-server setup` offers it: with no
tailscaled, or when you answer that people will not reach the server over the
tailnet, it turns on `https://edge.onaether.dev` and prints what the edge can
see. On a tailnet it asks, defaulting to no. Otherwise use `aether-server
install --edge-url <url> --edge-access <policy>`, which refuses `--edge-url`
without `--edge-access`, or the config keys:

```sh
sudo aether-server config set edge-url https://edge.onaether.dev
sudo aether-server config set edge-access approved-devices
sudo aether-server config set edge-url ""     # off
sudo systemctl restart aether-server          # apply the change
```

`edge-access` defaults to `approved-devices` when missing from the config.
The server honours `HTTPS_PROXY` from `/etc/aether/aether-server.env`.

The server trusts an edge by its signing key, not its host name, and pins the
key on first connection. A new host name for the same edge keeps the pin and
the owner. An edge with another key is refused with `edge key changed` until
you run `sudo aether-server edge trust`. To move to another edge:

```sh
sudo aether-server edge leave                 # unenroll at the old edge, forget its owner
sudo aether-server config set edge-url https://<edge-host>
sudo aether-server edge trust                 # compare the key with the one the edge's operator publishes, type yes
sudo systemctl restart aether-server
sudo aether-server edge claim-code --admin <member id>   # or, from an admin's linked terminal: aether edge connect
```

### Server id and claiming

The server id comes from `<data-dir>/ssh/host_ed25519_key`. Back that file up:
a new host key is a new server that must be claimed and linked again.

Until an account claims it, the edge relays nothing for a server except claim
connections. Setup ends with a claim link,
`aether://claim?edge=...&server=<server id>#<secret>`, and the claim code
`<server id>-<secret>`. The code lasts 30 minutes and 5 attempts. `sudo
aether-server edge claim-code` prints a new one, and is refused while the
server has an owner, and without `--admin` while it has members.

1. Start the server: `sudo systemctl daemon-reload && sudo systemctl enable
   --now aether-server`.
2. On your computer, open the claim link (the desktop app opens it at the
   Connect step, which also accepts it pasted), or run `aether link --claim
   <code>`.

The claiming account becomes the owner and an admin, and its device is approved
under either policy, because the code came from the machine's console. A claim
from a device that signed in as another account is refused with `this device
claims as github account <subject>, but the edge signed the connection in as
github account <login>; the claim was not attempted`.

**A server that already has members** (its first admin joined with an SSH key
or over the tailnet) is claimed by one of its admins: in the dashboard under
**Settings → Remote access → Connect my GitHub account**, or from a linked
terminal:

```sh
aether login --edge <edge url>   # if this machine is not signed in there
aether edge connect
```

This binds your signed-in GitHub account to your own member. It adds no member
and changes no role. It keeps the current link, adds the edge to it, and moves
the clone's `aether` remote to the edge host (if that fails it ends with `the
link is saved, but the clone's aether remote was not updated: ...`). On a server
that already has an owner it prints `this server is already connected to <edge
url> as github <login>` and changes nothing. Any other claim on a server with
members is refused with `server is already claimed`.

Until the edge has recorded the server, `aether invite --github|--email`, the
Invite dialog and `aether member link` are refused:

```
member.invitation.create: the edge has not recorded this server yet, so it relays nothing for it and keeps no directory; connect your GitHub account first: Settings › Remote access › Connect my GitHub account in the dashboard, or aether edge connect
```

If the edge cannot record a claim, it closes the connection with `ownership
report refused: <reason>`, shown by `sudo aether-server edge status`. The server
reconnects and learns whether the edge holds it as claimed.

### Checking the connection

`sudo aether-server edge status` prints the edge, policy, server id, the
[connector](#mcp-connectors) URL, the pinned edge key, the owner and the
connection:

```
connection   connected since <time>; the edge last answered 12s ago
measures     the server->edge control connection; it does not prove clients can reach the server
```

`connection` reads `stale: ...` when the server process is gone or the edge has
not answered for 40 seconds, and `never connected: ...` when the service has not
started since `edge-url` was set. Then run `sudo systemctl status aether-server`
and `sudo journalctl -u aether-server -n 50 --no-pager`. A connected server can
still be unreachable for a client: only a connection opened through the edge
tests the whole path.

When the edge's key changed, the server keeps retrying and logs this. Run `edge
trust` only after the edge's operator confirms the new fingerprint:

```
edge key changed: pinned SHA256:<old>, edge presents SHA256:<new>; if the edge's operator rotated its key, or edge-url now names another edge, run `aether-server edge trust`
```

The server has no owner under a key it was not claimed with. When the edge still
records one, the server reports itself ownerless as it reconnects, and an
admin's **Connect my GitHub account**, or `edge claim-code --admin <member id>`,
gives it an owner again.

### Signing in and linking

```sh
aether login              # prints an address and a code; confirm the code in the browser
aether servers            # servers your account reaches
aether logout             # revokes this device's token at the edge and deletes it here
```

```
open https://auth.onaether.dev/device and enter the code ABCD-EFGH
waiting for you to confirm the code...
signed in to edge.onaether.dev as <login> (github); this device is "<host name>"
```

Confirm only a code you started. `--edge <url>` takes the relay origin. Without
it commands use the one edge this machine is signed in to, else
`https://edge.onaether.dev`. The device key `edge-device-key` and token file
`edge-tokens.json` live in the config directory with mode 0600, and a file
others can read is refused with the `chmod 600 <path>` to run. `aether servers`
lists name, role, online state and the access policy the edge reports. The
server enforces its own policy.

Only a 26-character server id goes through the edge. Any other argument, such
as `my-server`, is an SSH address. A link pins the server id: every later
connection, direct or through the edge, must present a host key that derives it.
Ways to link, safest first:

| Command | Where the id comes from | Who you trust |
| --- | --- | --- |
| `aether link <join link>` | The invitation link an admin sent | The admin and the channel they used |
| `aether link --claim <code>` | The claim link printed on the server's console | Whoever handed it to you |
| `aether link <server id>` | The admin, from Settings → Remote access or `sudo aether-server edge status` | The admin and the channel. The edge cannot substitute another server |
| `aether link --from-edge <server id>` | `aether servers`, which the edge supplies | The edge. One that lists a false id can send your first link to another server |

`--from-edge` reads the host key first and shows the server's name, id, host
key and policy before it asks `link and pin this server? [y/N]`. Compare the
host key with `sudo ssh-keygen -lf /var/lib/aether/ssh/host_ed25519_key` on the
server. Without a terminal it refuses unless you pass `--yes`.

To also try a direct address first (3-second timeout) and fall back to the edge,
add `--addr host:2222`. The direct path accepts approved devices only, so under
`account` a new device works through the edge but not directly until approved.
A linked repository's `aether` remote uses the host
`<server id>.edge.aether.invalid`, which never resolves, and `aether link` sets
the repository's `core.sshCommand` to `aether edge-ssh` so `git push aether`
works. An existing `core.sshCommand` is overwritten only when it is exactly that
command from an earlier link. Otherwise `aether link` prints the line to add.

When the server refuses a device because it is pending, or was registered under
`account` and the server now requires approval, the command fails with:

```
device "dana-laptop", signed in as github account dana, is waiting for approval. An admin approves it from the dashboard's Needs you card, or you do, from an approved device of yours, or with:
  aether device approve <device id>
or on the server:
  sudo aether-server device review
```

If the device would be the server's only admin and that admin has no approved
device, the refusal carries an approval code for `sudo aether-server device
approve <code>` instead.

### Members, invitations and devices

Invitations by GitHub login or email are in [teams.md](teams.md#through-an-edge).

| Command | Does |
| --- | --- |
| `aether device list` | Every device with the account it signed in as (an admin sees every member's) |
| `aether device approve <code or device id>` | From an approved device, member SSH key or tailnet connection: show what approving admits, and approve after `y`. `XXXX-XXXX` is a code |
| `aether device revoke <device id>` | Revoke a device and close its connections |
| `sudo aether-server device approve <code>` | The machine's administrator approves a device after `y` |
| `sudo aether-server device review` | Approve, revoke or skip each registered and pending device, then list member SSH keys and tailnet logins |
| `aether member identities <id>`, `aether member unlink <id> github:<user id>` | List or remove a member's GitHub accounts |

#### Join page

An invitation's link is `https://<relay host>/join/<server id>/<invitation
id>#<secret>`. Opening it shows a page that opens the desktop app (**Open in
Aether**) and offers the link to **Copy**, or run `aether link <link>`. The
secret is the link's fragment, which a browser never sends, so no request to the
edge carries it. There is no claim page, so no claim secret passes through the
edge.

### Access policies

Each server chooses its policy and enforces it itself. `aether-server setup`
asks `Who may reach this server through the edge?` and repeats until you answer
`1` (account access) or `2` (approved devices).

| Policy | Who gets in | What must be compromised for an attacker to get in |
| --- | --- | --- |
| `account` | Any invited member who signs in with GitHub, from any new device, with their role | One of: the member's GitHub account, or the edge (its host, database or signing key). A compromised edge reaches every member of every `account` server on it |
| `approved-devices` | A member whose device a person approved: an admin, the member from an approved device, or the machine's administrator | One of those, and also a person who approves the attacker's device. The **Needs you** card shows the device label, account, key fingerprint and the member and role it admits as |

Choose `account` for one person, or a team that already relies on GitHub
accounts for source access. Choose `approved-devices` for a server holding code
or credentials that must stay protected even if a GitHub account or the edge is
taken over.

Under both, the server maps the account to a member, roles are the usual ones,
and every device is listed and revocable. Under `approved-devices` a new device
is `pending` until approved with **Approve** on the **Needs you** card, `aether
device approve`, or `sudo aether-server device approve <code>`. If you lose
every device, under `account` sign in on a new one; under `approved-devices` an
admin approves the new one, and an admin with no approved device left gets a
code that the machine's administrator approves.

#### Changing the policy

Only on the server's host. No dashboard action or client command changes it.

```sh
sudo aether-server config set edge-access approved-devices
sudo aether-server device review     # before restarting: see what a switch affects
sudo systemctl restart aether-server
```

Switching changes no device's status. A device registered under `account` is
refused under `approved-devices` with `device "<label>" was admitted by signing
in alone and is waiting for approval: this server now admits approved devices
only`. `device review` asks `approve, revoke or skip? [a/r/S]` for each device,
then lists the member SSH keys and tailnet logins that reach the server whatever
`edge-access` says. Those do not depend on the edge or the policy, and
`edge-url ""` leaves them working.

#### What an attacker can do

Neither policy rests on OAuth being weaker or SSH keys stronger. The difference
is how many parties must fail.

| An attacker with | `account` | `approved-devices` |
| --- | --- | --- |
| A member's GitHub account | Signs in and lists the member's servers. Reaches a workspace with the member's role. If the member is an admin, can invite accounts, change roles and transfer ownership, but not approve a device | Signs in and lists servers. The new device is pending and reaches nothing |
| That account plus the join link sent to it | Completes the join with the invited role | A pending device that waits for an admin's click |
| That account without the join link | Refused: `<account> is invited to this server; open the invitation link the admin sent, or run: aether link <link>` | The same |
| A compromised edge or its signing key | Forges a grant for any account and reaches the workspace of every `account` server as any member | Forged grants yield pending devices. It cannot admit one |
| A stolen device key | Reaches the direct address once the device is approved, like an SSH key. Through the edge it also needs the token or the edge | The same |
| A stolen device token | Nothing: the server also requires the device key | Nothing |

Under both, a compromised edge cannot read SSH, impersonate a server to a linked
client (the host key must derive the linked id), or take a claim (the code
travels inside SSH). It can learn who connects to which server and deny service.
It can send a client that links for the first time to another server by listing
a false id: a join or claim link, or an id from the server's admin, prevents
that. It can serve a join page whose script reads an invitation's secret, then
accept that one invitation: under `account` with its role, under
`approved-devices` as a pending device. A forged account deletion removes access
and gains none. [MCP connector](#mcp-connectors) traffic is the exception to end
to end encryption, under both policies.

### Revoking access

| Action | Relayed connections | Direct connections with the device key |
| --- | --- | --- |
| `aether member remove <id>`, `aether device revoke <id>` | Closed at once | Closed at once |
| Revoking in `sudo aether-server device review` | Closed within 3 seconds | The same |
| `aether logout`, or revoking on the edge's Devices page | Closed at once | Stay open, and new ones are admitted: the device is still approved on the server |
| Deleting the account ([Deleting an account](#deleting-an-account)) | Closed at once | Closed: each server removes the account's edge devices, when it next connects to the edge if it is offline |
| `aether-edge accounts block` or `delete` by the edge's operator | Stay open until they end or the edge restarts | `delete` reaches each server at its next enrollment |

A revocation made on a server works without the edge. To end a device
completely, revoke it on both the edge and the server. A relayed connection the
edge closes ends with the edge's reason, such as `received close frame: status =
StatusPolicyViolation and reason = "not a member of this server"`.

| Credential | Lifetime | Ended by |
| --- | --- | --- |
| Device key | Until you delete `edge-device-key` | Nothing in Aether. A new key is a new device |
| Device token | No expiry | `aether logout`, the Devices page, deleting the account, or an operator block |
| Sign-in code from `aether login` | 10 minutes | Confirming or refusing it |
| Edge session (browser cookie) | 30 days after last use | Signing out, deleting the account, a block |
| Claim code | 30 minutes, 5 attempts | Its use, or a new code |
| Invitation | 7 days | `aether invite revoke`, its use, or a demotion of its creator |

`aether logout` revokes the token at the edge, then deletes it locally. If the
edge cannot be reached it keeps the token so you can retry. It never deletes the
device key or a link. After a revoked token a link fails through the edge with
`device token revoked`, and its `--addr` path still works with the approved
device key. SSH keys and tailnet identities are unaffected.

### During an edge outage

- Servers keep running and reconnect on their own. Running work is untouched.
- Relayed connections drop. Links with `--addr`, tailnet members, member SSH
  keys, invite codes, `aether gui` and the tailnet dashboard work as before.
- Every revocation and approval made on a server works.
- `aether login`, `aether servers`, `aether logout` and account deletion fail
  with the edge's error.

When the edge has no live registration for the linked id but sees other servers
of your account online, it lists them:

```
connect to server <old id>: edge.onaether.dev refused: server is not connected to the edge (HTTP 503)
  this link names server <old id>, but edge.onaether.dev sees these servers of github account dana online: <new id> (prod)
  if one is this server under a new id, link it with: aether link --from-edge <id>
```

A server reinstalled with a new data directory has a new host key and so a new
id. Compare it with `sudo aether-server edge status` before linking again.

### Recovering a GitHub account

Recovering a GitHub account ends the attacker's use of it for new sign-ins and
nothing else. Devices the attacker signed in keep their tokens, which do not
expire, and stay on every server until revoked.

1. Sign in on the edge's Devices page (`https://auth.onaether.dev/devices` on
   the project's edge) and revoke every device you do not recognize.
2. On each server, run `aether device list` and `aether device revoke
   <device-id>` for each unknown device. On the machine, `sudo aether-server
   device review` shows each device with its account and first-seen time.
3. Under `account`, if the account was an admin's, check what an admin could
   have done: `aether member list`, `aether invite list`, and the SSH keys and
   tailnet logins `device review` lists. Revoke with `aether invite revoke <id>`
   and `aether member remove <id>`. An invite code minted by an approved device
   is a file in the server's invites directory until redeemed or expired, and no
   command lists it.
4. If the account was linked to a member while someone else held it, remove the
   account without removing the member:

   ```sh
   aether member identities <member-id>
   aether member unlink <member-id> github:<user id>
   ```

   The account's devices are revoked and their connections close. The member,
   its role, other accounts, SSH keys and tailnet identity stay. The member or
   an admin may do this, but under `approved-devices` not from a pending or
   registered device.

Under `approved-devices` the attacker's devices are pending and had no access.
Revoke them anyway so nobody approves one by mistake.

### Console recovery

An admin who reached the server only through the edge and lost that account is
restored on the machine. The server must have no owner, as after the owner
deletes their account:

```sh
sudo aether-server edge claim-code --admin <member id>
```

This prints a claim link bound to that admin. The admin opens it signed in with
the account they now use, or runs `aether link --claim <code>`. The server binds
the account to that member, approves the device and becomes owned by that
account. It creates no member and raises no role. A member who is not an admin
is refused (`is collaborator, not an admin; the claim was refused`), as is an
account bound to another member. Find the member id with `aether member list`. An
unknown id is refused with `--admin: no member <id>; the admins are: <member id>
(<name>)`.

### Ownership and deleting an account

An admin hands ownership at the edge to another admin who has a linked GitHub
account:

```sh
aether member transfer <member-id>
```

An admin with two linked GitHub accounts is refused until one is removed with
`aether member unlink`. The command fails with `ownership report refused:
<reason>` when the edge refuses, or after 10 seconds without an answer, and the
server keeps its previous owner. It is refused while the server is not connected
to the edge or has no owner. A transfer never creates an admin or changes a role.

When the owner's identity leaves the server (the member was removed or the
account deleted), the server is **ownerless**. It stays enrolled and keeps
relaying for its members. Give it an owner again with **Connect my GitHub
account** or `aether edge connect`, or with
[Console recovery](#console-recovery).

#### Deleting an account

Only the Account page deletes an account (`https://auth.onaether.dev/account` on
the project's edge). It lists the servers the account owns and belongs to, and
needs a GitHub sign-in in this browser from the last 5 minutes, plus the
account's login typed back. Otherwise it is refused with `deleting an account
needs a sign-in in this browser from the last 5m0s: sign in again at <url>, then
delete it within 5m0s`. `aether logout --delete-account` lists the same servers
and where to delete.

Transfer ownership of each server the account owns first. Deleting ends the
account's sign-ins and device tokens, and each server (told at once, or when it
next connects) removes that identity and its edge devices. No server loses a
member, role or data. An SSH key or tailnet identity of the same person keeps
working until an admin removes it, and invitations to the login or email stay
until revoked. Signing in again creates a new account that no server knows: link
it with `aether member link`, or an admin invites it, which creates a new member.

Before deleting the only way an admin reaches a server, make sure another admin
exists or that the admin also has an SSH key or tailnet identity there.
Otherwise only the machine's administrator can restore them
([Console recovery](#console-recovery)).

### The dashboard

The edge serves no dashboard. A dashboard that reaches a server through an edge
is `aether gui` on a computer, or the [Android app](install.md#android-app) in
Edge mode ([local-gateway.md](local-gateway.md)). A phone without that mode
reaches a dashboard over a tailnet
([networking.md](networking.md#the-dashboard)).

### MCP connectors

A server's **MCP connector** lets ChatGPT or Claude list, launch, message and
stop runs as a member ([connectors.md](connectors.md)). The edge forwards the
requests at `https://<relay host>/mcp/<server id>`, which `aether mcp` and `sudo
aether-server edge status` print. The server authenticates every request itself.

TLS ends at the edge, because ChatGPT and Claude connect only to a host with a
public certificate. Connector traffic is therefore not end to end. A compromised
edge, while it runs, reads and can alter every connector request and answer,
including tokens and the pairing code typed on the consent page, and acts as that
connector's member with no approval in the way, under either policy.

| Answer | Cause |
| --- | --- |
| `404 unknown server` | An id of the wrong shape, or no server claimed under it |
| `503 server is not connected to the edge` | Server offline |
| `409 server is connected to the edge but not claimed` | Claim recorded but not yet applied. Retry |
| `501 server <name> (<id>) runs aether-server <version>, which has no MCP endpoint; upgrade it: sudo aether-server update` | An old server |
| `403 this server does not serve MCP connectors` | Connectors are off on the server |
| `503 this server already has 8 MCP connector requests in progress through the edge; retry shortly` | Too many at once |
| `413 request body is over the edge's limit of 1048576 bytes` | Body over 1 MiB |

### Limits

- 48 concurrent SSH connections and 8 MCP connector requests per server, and 16
  connections per device.
- 3 unclaimed servers per client address, each dropped after 30 minutes.
- Claim connections: 5 at once, then 1 a minute per address and per account.
- 1000 members and open invitations per server.
- Login and email invitations match only within 24 hours of the account's last
  GitHub sign-in.
- Every server today is `self-hosted`. The `hosted` kind is reserved for a
  workspace Aether would run for you, and no such feature exists.

An idle relayed terminal is never cut off.

### Troubleshooting

| Symptom | Fix |
| --- | --- |
| `edge key changed: ...` in the server log | Confirm the new fingerprint with the edge's operator, then `sudo aether-server edge trust` and claim again |
| `device token revoked` | `aether login` again |
| `server is not connected to the edge (HTTP 503)` | `sudo aether-server edge status` on the server. The id may have changed after a reinstall |
| `upgrade required: <minimum>` | The server or client is too old for this edge. Update it |
| `this edge serves <signin origin> and <relay origin>, not the host "<host>"` | Use one of the two names |
| `server is blocked by this edge's operator` | The operator blocked this server id. Ask them |
| `server is already claimed` | Use **Connect my GitHub account** or [Console recovery](#console-recovery) |

## Running your own edge

`aether-edge serve` is one static Linux binary (amd64 or arm64) with an embedded
SQLite database. Install it with a systemd unit ([Installing](#installing)) or
run the image `ghcr.io/3xdevops/aether-edge` ([In a container](#in-a-container)).
It answers on two host names in one of two modes:

- Behind a reverse proxy (`--proxy-listen`): plain HTTP on a loopback port, with
  nginx on the same machine terminating TLS with certbot certificates. The
  packaged unit and environment file are set up for this.
- Without a reverse proxy (`--listen`, the default): the edge terminates TLS on
  `:443` itself, with Let's Encrypt certificates for exactly its two names.

`<signin-host>` and `<relay-host>` below are your two public names, such as
`auth.example.com` and `edge.example.com`.

### Requirements

- A Linux host with systemd 248 or newer, or a container runtime, and a public
  IPv4 address. The edge limits requests by client address, so no other proxy or
  load balancer may sit between clients and the edge's own proxy.
- Outbound HTTPS to `github.com`, `api.github.com` and Let's Encrypt.
- Two DNS names and a GitHub OAuth app (below).
- Egress bandwidth: relaying is byte copying, so bandwidth runs out first
  ([Egress budget](#egress-budget)).

### GitHub OAuth app

In the account or organization that should own it, open **Settings → Developer
settings → OAuth Apps → New OAuth App**. Name it `Aether`. The edge asks for the
scope `user:email`.

| Field | Value |
| --- | --- |
| Application name | `Aether` |
| Homepage URL | `https://<signin-host>` |
| Authorization callback URL | `https://<signin-host>/signin/github/callback` (must match exactly) |
| Enable Device Flow | Off: the edge runs its own device flow |

Generate a client secret and keep it for [Installing](#installing).

### DNS

| Name | Type | Value |
| --- | --- | --- |
| `<signin-host>`, `<relay-host>` | A (and AAAA for IPv6) | The edge's address |
| `<signin-host>`, `<relay-host>` | CAA | `0 issue "letsencrypt.org"` |

Use no wildcard record, and never proxy these names through a CDN. A proxying
CDN terminates TLS, so it would see sign-in cookies, device tokens and OAuth
codes in plain text. It also replaces the client address the edge limits by, and
its timeouts would cut the relay's long-lived WebSockets. Leave `accounturi` out
of CAA.

### Behind a reverse proxy

With `--proxy-listen 127.0.0.1:8443` (`AETHER_EDGE_PROXY_LISTEN`) the edge serves
plain HTTP and obtains no certificate. The origins stay `https://`.
[`packaging/nginx/aether-edge.conf.example`](../packaging/nginx/aether-edge.conf.example)
has one `server` block per host name passing requests to `127.0.0.1:8443`. Its
lines that matter:

| Line | Why |
| --- | --- |
| `proxy_set_header Host $host` | The edge picks the origin by the host name |
| `proxy_set_header X-Forwarded-For $remote_addr` | Gives the edge the address nginx accepted, discarding any value the client sent. Forwarding `$http_x_forwarded_for` would let a client choose its address |
| `Upgrade` and `Connection` headers (relay host) | Servers and clients hold WebSockets there |
| `proxy_read_timeout 7d`, `proxy_send_timeout 7d` (relay host) | A relayed terminal may be quiet for hours. nginx's 60 seconds would cut it |
| `proxy_buffering off`, `proxy_request_buffering off` (relay host) | Relayed bytes pass as they arrive |

A request from the proxy without the header is refused with `400`:

```
the proxy in front of this edge sent no X-Forwarded-For header; configure the proxy with `proxy_set_header X-Forwarded-For $remote_addr`
```

A local `curl http://127.0.0.1:8443/healthz` is refused the same way. Add `-H
'Host: <relay-host>' -H 'X-Forwarded-For: 127.0.0.1'`, or probe through nginx.
Raise `worker_connections` in `/etc/nginx/nginx.conf` above the 768 Debian and
Ubuntu ship with when the edge serves more than a few hundred connections.

A proxy on another host or in another container reaches the edge from its own
address. Name it with `--trusted-proxies` (`AETHER_EDGE_TRUSTED_PROXIES`), a
comma-separated list such as `10.0.0.5/32`; `--proxy-listen` may then be any
address, such as `:8443`. A request from any other peer, loopback included, is
refused with `403`:

```
this edge serves requests only through its reverse proxy, and 198.51.100.7 is not in the edge's --trusted-proxies
```

Every listed peer can set the client address the edge rate-limits by. List each
proxy's own address as a `/32` or `/128`, let nothing else reach
`--proxy-listen`, and do not publish it from a container. `0.0.0.0/0` and `::/0`
are refused. Without `--trusted-proxies`, a `--proxy-listen` address that is not
loopback is refused.

### Without a reverse proxy

Delete `AETHER_EDGE_PROXY_LISTEN` from the environment file and set
`AETHER_EDGE_ACME_EMAIL`. The edge obtains a certificate for each name with
TLS-ALPN-01 on `:443` and caches them in `<data>/acme/`. Leave port 80 closed.
The packaged unit grants no capability, so add the one that binds `:443` with
`sudo systemctl edit aether-edge`:

```ini
[Service]
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
```

Renewal happens 30 days before expiry, and a failed renewal is reported
nowhere. Probe the expiry from outside ([Monitoring](#monitoring)).

### Installing

Download the binary for the host's architecture from a release, check it, and
install it with the unit and the environment file. `<release-tag>` is a release
tag; `<arch>` is `amd64` or `arm64`.

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

Edit `/etc/aether-edge/aether-edge.env` and replace every `<...>`. Put the GitHub
client secret in a root-only file. Paste it, then press Ctrl-D, so it lands in
no shell history:

```sh
sudo sh -c 'umask 077 && cat > /etc/aether-edge/github-client-secret'
```

The unit runs the edge as the `aether-edge` user with no capabilities and
`/var/lib/aether-edge` as its only writable path, and passes the secret in as a
systemd credential. If the file is missing the start fails with `aether-edge:
--github-client-id is set but /run/credentials/aether-edge.service/github-client-secret,
named by --github-client-secret-file, holds no secret`.

### In a container

The release publishes `ghcr.io/3xdevops/aether-edge` for linux/amd64 and
linux/arm64: the binary on a distroless base with no shell, running as uid and
gid 65532 and carrying no configuration. Tags:

| Tag | Moves to | Use |
| --- | --- | --- |
| `<release-tag>`, the commit SHA, `sha-<short-sha>` | Never moves | Pinning |
| `latest` | Every release, alphas included | Trying a release. Never production |
| `stable` | A release an admin promoted after it qualified and soaked ([Promoting an edge release to stable](install.md#promoting-an-edge-release-to-stable)) | Production |

`latest` can migrate the database on an unattended restart. In production pin
`stable` by digest (`ghcr.io/3xdevops/aether-edge:stable@sha256:<digest>`, from
`docker buildx imagetools inspect ghcr.io/3xdevops/aether-edge:stable`) and
change the digest deliberately.

Behind a proxy in another container, put both on one container network, publish
only the proxy's ports, and trust the proxy's own address (not the network's
subnet) with a fixed address for the proxy. The proxy passes both host names to
`http://aether-edge:8443`:

```sh
docker volume create aether-edge-data
docker run -d --name aether-edge --network <network> \
  --restart unless-stopped --stop-timeout 30 --read-only \
  -v aether-edge-data:/var/lib/aether-edge \
  -v <secret-file>:/run/secrets/github-client-secret:ro \
  -e AETHER_EDGE_SIGNIN_ORIGIN=https://auth.example.com \
  -e AETHER_EDGE_RELAY_ORIGIN=https://edge.example.com \
  -e AETHER_EDGE_PROXY_LISTEN=:8443 \
  -e AETHER_EDGE_TRUSTED_PROXIES=<proxy-address>/32 \
  -e AETHER_EDGE_GITHUB_CLIENT_ID=<github-client-id> \
  -e AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE=/run/secrets/github-client-secret \
  ghcr.io/3xdevops/aether-edge:<release-tag>@sha256:<digest>
```

With the edge's own certificates, replace the proxy variables with `-e
AETHER_EDGE_LISTEN=:8443 -e AETHER_EDGE_ACME_EMAIL=<email> -p 443:8443`. The
published port must keep the client's source address: a userland port forwarder
(some rootless runtimes) replaces it, and every client then shares one address's
limits.

- The data directory `/var/lib/aether-edge` is a volume that must outlive every
  container. Use a named volume, or a directory owned by uid 65532 (`chown -R
  65532:65532 <dir> && chmod 0700 <dir>`).
- The client secret is a file mounted read-only from your secret store, readable
  by uid 65532. Never put it in an image layer, a build argument or a committed
  file. `AETHER_EDGE_GITHUB_CLIENT_SECRET` works, but `docker inspect` shows it.
- Docker's 10-second stop grace period is too short. Use `--stop-timeout 30` or
  `stop_grace_period: 30s`.
- The image's `HEALTHCHECK` runs `aether-edge healthcheck`. Keep `--ready` out of
  it ([Monitoring](#monitoring)).
- Metrics listen on loopback inside the container. Scrape from a container that
  shares its network (`--network container:aether-edge`).
- Operator commands run as `docker exec <container> aether-edge servers list`.

The edge refuses a data directory it cannot use, with the fix in the message:

```
aether-edge: data directory /var/lib/aether-edge (uid 0, gid 0, mode 0755) is not writable by uid 65532, the user aether-edge runs as: permission denied; give that user write access as the directory's owner or through its group, or name another with --data or AETHER_EDGE_DATA
```

A replacement container without the data directory makes a new signing key and
an empty database. Every server then refuses the edge with `edge key changed`
until its operator runs `sudo aether-server edge trust`, and every client runs
`aether login` again.

#### Verifying an image

The release signs each image digest in `aether-release.json`
([Verified releases](install.md#verified-releases)). Before you run a new digest,
ask the edge you already run whether a release signed it and whether an upgrade
from your release needs manual steps:

```sh
docker exec <container> aether-edge release-check \
  --tag <release-tag> --digest sha256:<digest> --from <running-tag>
```

It exits 0 when the upgrade may go ahead, or 3 with each reason on stderr
(`aether-edge: hold: ...`) when it must wait: a blocking manual step, or a
`--from` below the release's `min_upgrade_from`. A digest that `stable` names
also carries a promotion attestation from `promote-edge.yml` on `main`. A host
that follows `stable` requires it, since repository write access alone can move a
tag:

```sh
cosign verify-attestation --new-bundle-format \
  --type https://github.com/3xDevOps/aether/edge-promotion/v1 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/3xDevOps/aether/.github/workflows/promote-edge.yml@refs/heads/main \
  ghcr.io/3xdevops/aether-edge@sha256:<digest>
```

The printed predicate must name `stable`, the digest and the tag `release-check`
verified. `gh attestation verify oci://ghcr.io/3xdevops/aether-edge@sha256:<digest>
--repo 3xDevOps/aether --signer-workflow
3xDevOps/aether/.github/workflows/release.yml --source-ref refs/tags/<release-tag>`
checks the build provenance.

### Configuration

Every option is a flag whose default comes from the environment variable beside
it. The unit starts `aether-edge serve` with no flags, so the environment file
([`packaging/edge/aether-edge.env.example`](../packaging/edge/aether-edge.env.example))
is the whole configuration.

| Flag | Environment | Default |
| --- | --- | --- |
| `--signin-origin` | `AETHER_EDGE_SIGNIN_ORIGIN` | Required: `https://<signin-host>` |
| `--relay-origin` | `AETHER_EDGE_RELAY_ORIGIN` | Required: `https://<relay-host>`, another host than the sign-in origin's |
| `--proxy-listen` | `AETHER_EDGE_PROXY_LISTEN` | None; `127.0.0.1:8443` in the environment file |
| `--trusted-proxies` | `AETHER_EDGE_TRUSTED_PROXIES` | None: the proxy is on loopback |
| `--listen` | `AETHER_EDGE_LISTEN` | `:443`, with the edge's own certificates. Unused with `--proxy-listen` |
| `--data` | `AETHER_EDGE_DATA` | `/var/lib/aether-edge` |
| `--metrics-listen` | `AETHER_EDGE_METRICS_LISTEN` | `127.0.0.1:9464`, loopback only |
| `--acme-email` | `AETHER_EDGE_ACME_EMAIL` | None |
| `--acme-directory` | `AETHER_EDGE_ACME_DIRECTORY` | `https://acme-v02.api.letsencrypt.org/directory` |
| `--egress-budget` | `AETHER_EDGE_EGRESS_BUDGET` | `0`: bytes per UTC month, a plain integer. 0 means none |
| `--github-client-id` | `AETHER_EDGE_GITHUB_CLIENT_ID` | Required |
| `--github-client-secret-file` | `AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE` | None; the unit sets it |
| `--dev-listen` | `AETHER_EDGE_DEV_LISTEN` | None. Plain HTTP on a loopback address for local testing |
| none | `AETHER_EDGE_GITHUB_CLIENT_SECRET` | None. The secret itself, instead of a file; setting both is refused |

The secret is never a flag, because a flag shows in the process list. The edge
checks its options before it binds anything. Among the errors:

```
aether-edge: edge: no GitHub OAuth app is configured; set --github-client-id and its client secret
aether-edge: --github-client-id is set but its secret is not; name a file holding it with AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE (--github-client-secret-file), or set AETHER_EDGE_GITHUB_CLIENT_SECRET
aether-edge: --relay-origin must be https://host[:port], not "edge.example.com"; for a local plain-HTTP edge use --dev-listen
aether-edge: --signin-origin https://edge.example.com and --relay-origin https://edge.example.com must name different hosts, such as auth.example.com and edge.example.com
```

### Firewall

Allow inbound TCP 443 on IPv4 and IPv6, TCP 80 for certbot behind nginx, and SSH
on TCP 22. Refuse everything else:

```sh
sudo ufw default deny incoming
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
```

> [!WARNING]
> Never limit port 22 to a source address in `ufw`. When your address changes,
> SSH times out for you and for the provider's browser SSH, and the way back in
> is a rebuild from a snapshot. Limit port 22 in the provider's firewall
> instead, such as an AWS security group.

With port 22 open to every address, SSH must refuse passwords: `sudo sshd -T |
grep -E '^(password|kbdinteractive)authentication'` must print `no` on both
lines. Before you close the session that ran `ufw enable`, open a second SSH
connection to check port 22 still answers. Without a reverse proxy, leave port
80 closed.

### First start and checks

```sh
sudo systemd-analyze verify /etc/systemd/system/aether-edge.service   # prints nothing when the unit is valid
sudo systemctl daemon-reload
sudo systemctl enable --now aether-edge
journalctl -u aether-edge -f
```

Check from another machine:

1. `curl -fsS https://<signin-host>/healthz` and `curl -fsS
   https://<relay-host>/healthz` each print `ok`.
2. `curl -sS https://<relay-host>/signin` prints `{"error":"/signin is served on
   https://<signin-host>, not on this host"}`.
3. `curl -sS -H 'Aether-Edge-Version: 1' https://<relay-host>/v1/edge` prints the
   signing key and its fingerprint. On the edge, `sudo ssh-keygen -lf
   /var/lib/aether-edge/edge_key` prints the same one. Servers pin that key:
   publish the fingerprint where server operators compare it during
   `aether-server edge trust`, and back the key up now.
4. `curl -sS -o /dev/null -D - https://<signin-host>/signin/github` answers `302`
   to `github.com` with your callback as `redirect_uri`.
5. Open `https://<signin-host>/signin` and sign in. On a laptop, run `aether
   login --edge https://<relay-host>`, confirm the code, then `aether servers
   --edge https://<relay-host>`.
6. On a separate server machine, enroll a canary:

   ```sh
   sudo aether-server install --edge-url https://<relay-host> --edge-access approved-devices
   sudo systemctl daemon-reload && sudo systemctl enable --now aether-server
   sudo aether-server edge status        # edge key matches step 3; connection: connected since ...
   sudo aether-server edge claim-code
   ```

   On the laptop, `aether link --claim <code> --edge https://<relay-host>`, then
   `aether runs`. Keep this server running.

### Monitoring

The metrics listener serves Prometheus text at `/metrics`. Scrape it from the
edge host or over an SSH tunnel. It is never exposed.

| Metric | Meaning |
| --- | --- |
| `aether_edge_servers{state="claimed"}`, `{state="unclaimed"}` | Servers holding a control connection |
| `aether_edge_egress_month_bytes` | Bytes relayed this UTC month, the figure the budget counts |
| `aether_edge_throttled` | `1` once the monthly egress budget is spent |
| `aether_edge_github_errors_total` | Failed calls to GitHub during sign-in |
| `aether_edge_refusals_total{reason="..."}` | Refused connections by reason |

`/healthz` (`aether-edge healthcheck`) is liveness. `/readyz` (`aether-edge
healthcheck --ready`) answers 200 when the database answers within a second, the
public listener serves and the edge is not stopping, and 503 otherwise. Neither
asks GitHub or the ACME authority, so watch `aether_edge_github_errors_total`
and certificate expiry. Alert on `aether_edge_throttled` equal to 1, on a fall
in claimed servers to near zero, and on a sustained rise in one refusal reason.
Probe from outside the edge's network, so DNS and routing are tested too:

```sh
for host in <signin-host> <relay-host>; do
  curl -fsS "https://$host/healthz"
  echo | openssl s_client -connect "$host:443" -servername "$host" 2>/dev/null |
    openssl x509 -noout -checkend 1814400 || echo "certificate for $host expires within 21 days"
done
```

The edge logs to the journal (`journalctl -u aether-edge`) with server ids and,
for refused enrollments and failed TLS handshakes, client addresses. Client
addresses are personal data and journald can keep them for months, so set a
retention period and state it in your privacy notice (what the database keeps is
in [privacy.md](privacy.md#what-an-edge-stores)):

```sh
sudo install -d /etc/systemd/journald.conf.d
printf '[Journal]\nMaxRetentionSec=<retention such as 14day>\n' |
  sudo tee /etc/systemd/journald.conf.d/retention.conf
sudo systemctl restart systemd-journald
```

nginx keeps its own access log in `/var/log/nginx/access.log`, rotated by
`/etc/logrotate.d/nginx`.

### Egress budget

`--egress-budget` counts the bytes the relay copies, in both directions, per UTC
month. Once the count reaches the budget, all relayed connections together are
paced to 256 KiB/s until the next month, adding at most about 650 GiB. Nothing is
cut off, and the count survives restarts. It does not count TLS framing or the
edge's pages, so a provider meters more than `aether_edge_egress_month_bytes`
shows. For a provider ceiling of C bytes a month, set the budget below C minus
700 GiB (751619276800 bytes).

### Backup and recovery

The data directory `/var/lib/aether-edge` holds `edge.db` (accounts, devices,
claimed servers and owners, owed deletions, blocks; bearer secrets only as
SHA-256 hashes), `edge_key` (the Ed25519 key grants are signed with, mode 0600),
`backups/` (copies taken before each migration, and the files a restore
replaced), lock files held by the running edge, and `acme/` when the edge
obtains its own certificates. Keep the directory on a local filesystem: `flock`
does not lock across NFS.

- **`edge_key`, once, offline**, right after the first start. It never changes. A
  copy is a secret: whoever holds it can act as the edge, so encrypt backups. If
  it is lost while `edge.db` holds accounts or servers, the edge does not start
  (`signing key /var/lib/aether-edge/edge_key is missing, and edge.db holds
  accounts or servers that pin it ...`). Put it back with `aether-edge restore
  --key <key file> <database backup>`. With no copy, start over with a new data
  directory: every server refuses the new key with `edge key changed` until its
  operator runs `sudo aether-server edge trust`.
- **`edge.db`, daily.** `aether-edge backup` copies a consistent snapshot while
  the edge runs. Name a directory, a new file, or `-` for standard output;
  `--with-key` copies `edge_key` too:

  ```sh
  sudo install -d -o aether-edge -m 0700 /var/backups/aether-edge
  sudo -u aether-edge aether-edge backup /var/backups/aether-edge/
  docker exec aether-edge aether-edge backup - > edge-$(date -u +%Y%m%d).db
  ```

  `backup` never removes old copies, and a full volume stops the next upgrade.
  Rotate them and copy them off the machine. A lost `edge.db` forgets every
  account, device token and owner: each client runs `aether login` again, and
  each server drops its owner and logs ``edge: the edge does not know this
  server's owner; claim it again with a code from `aether-server edge
  claim-code` ``. Members, roles and devices live on the servers and survive.
  Restoring an older backup brings back device tokens revoked since; a device
  revoked on a server stays revoked there.
- **`/etc/aether-edge/`** and, behind nginx, `/etc/letsencrypt/` and
  `/etc/nginx/conf.d/aether-edge.conf`, or the steps to recreate them.

To restore, stop the edge and run `aether-edge restore` as its user. It checks
the copy, saves the current database under `backups/edge-before-restore-<UTC
time>.db` and puts the copy in place. `--key <file>` restores `edge_key` too.

```sh
sudo systemctl stop aether-edge
sudo -u aether-edge aether-edge restore /var/lib/aether-edge/backups/<backup>.db
sudo systemctl start aether-edge
```

With the edge running, a restore is refused: `another aether-edge process holds
the lock: ...; an edge is running on /var/lib/aether-edge: stop it first`.

### Upgrades

```sh
sudo -u aether-edge aether-edge backup /var/lib/aether-edge/backups/
# download and check the new binary as in Installing, then:
sudo install -m 0755 aether-edge-linux-<arch> /usr/local/bin/aether-edge
sudo systemctl restart aether-edge
```

In a container, back up with `docker exec <container> aether-edge backup
/var/lib/aether-edge/backups/`, then start the new tag's container with the same
volume. Fetch the new release's unit and environment example too, and compare
them with yours. Relayed connections drop for a few seconds while the servers
reconnect. Runs are unaffected. Upgrade the edge before its servers: an older
server or client is refused with `upgrade required: <minimum>`.

The database migrates forward on start, after the edge copies `edge.db` to
`backups/edge-v<from>-v<to>-<UTC time>.db`. Without a good copy it does not
migrate and exits with `the edge does not migrate without a verified backup`
(usually a full disk or an unwritable `backups/`). An older binary can refuse a
migrated database, and the error names the backup to restore:

```
aether-edge: edgestore: database schema version 2 is newer than this binary supports (1); run a binary that supports schema 2 or later, or stop the edge and restore the backup taken before the upgrade: aether-edge restore --data /var/lib/aether-edge /var/lib/aether-edge/backups/edge-v1-v2-20261017T050000.000000Z.db
```

To roll back, stop the edge, restore that backup with the new binary or image
(older releases have no `restore`), then start the older one. Writes made since
the upgrade are lost. To check first, ask the old image whether it can run the
database with `docker run --rm -v <data>:/var/lib/aether-edge <old image> schema
--json`, and restore the backup when `supported` is below `compat_floor`.

### Automatic updates

`aether-edge-update` ([`scripts/edge-update.sh`](../scripts/edge-update.sh))
updates an edge that runs in Docker Compose to the newest release on a channel
and rolls back when the new release fails its checks. A systemd timer runs it
daily at 04:00 UTC with a random delay up to an hour. It never downgrades and
never runs a digest that a signed release does not name. Each run:

1. Pulls the channel's image and skips a digest that is already running or that
   failed before.
2. Verifies with `cosign` that `promote-edge.yml` on `main` promoted the digest
   to the channel (`latest` skips this).
3. Runs `aether-edge release-check` from the running image. A blocking manual
   step, or a running release older than `min_upgrade_from`, holds the update
   and exits 3 with nothing changed.
4. Backs up the database to
   `/var/lib/aether-edge/backups/pre-update-<version>-<UTC time>.db`.
5. Writes the new digest to `.env`, runs `docker compose up -d --wait`, and checks
   that the edge is ready, serves the same key fingerprint through the proxy and
   that the claimed servers come back (within a 10% tolerance).
6. If a check fails, rolls back to the old digest, restoring the backup only when
   the old release cannot run the migrated database, and records the failed
   digest so it is not tried again.

Logs are in `journalctl -u aether-edge-update`.

To install it you need Docker with the Compose plugin, `curl`, `flock`, `jq`
1.6 or later and [cosign](https://github.com/sigstore/cosign) 3 on the host, and
the edge running from
[`packaging/edge/compose.yaml`](../packaging/edge/compose.yaml) in
`/opt/aether-edge` behind Traefik on the external network `proxy`. That file pins
the image by digest. Give Traefik a fixed address and put it in
`AETHER_EDGE_TRUSTED_PROXIES`, and keep `forwardedHeaders.trustedIPs` and
`forwardedHeaders.insecure` unset on the entry point the edge uses. Copy
`scripts/edge-update.sh` to `/usr/local/sbin/aether-edge-update`, and
`packaging/edge/aether-edge-update.env.example` to
`/etc/aether-edge-update.env`, and the units
`aether-edge-update.service`, `.timer` and `aether-edge-update-recover.service`
from `packaging/systemd/` to `/etc/systemd/system/` at the release tag you
install. Then:

```sh
docker pull ghcr.io/3xdevops/aether-edge:stable                        # prints Digest: sha256:<digest>
echo 'AETHER_EDGE_DIGEST=sha256:<digest>' | sudo tee /opt/aether-edge/.env
sudo aether-edge-update --check            # verifies the running digest before anything from the pulled image runs
(cd /opt/aether-edge && sudo docker compose up -d --wait aether-edge)
sudo systemctl daemon-reload
sudo systemctl enable --now aether-edge-update.timer
sudo systemctl enable aether-edge-update-recover.service
```

`--check` prints `verified the running <version> (<digest>)`, then `up to date`
or `would update ...`, and changes nothing. Without cosign or jq it fails with
`cosign not found` or `jq not found`. Set `AETHER_EDGE_UPDATE_RELAY_URL` in
`/etc/aether-edge-update.env`. Optional settings are
`AETHER_EDGE_UPDATE_CHANNEL` (default `stable`; `latest` takes pre-releases with
no promotion check), `AETHER_EDGE_UPDATE_PUBLIC_WAIT` (300 seconds),
`AETHER_EDGE_UPDATE_SERVER_WAIT` (120) and `AETHER_EDGE_UPDATE_SERVER_TOLERANCE`
(`10%`; `0` requires every server). To be told about failures, add `OnFailure=`
to the service.

#### First upgrade

The updater runs `version --json`, `release-check`, `backup` and `schema` from
the image the edge runs now. An older image makes every run exit 1 with
`<image>@<digest> predates aether-edge version --json, release-check, backup and
schema`. Do this once, in `/opt/aether-edge`, after checking the promotion
attestation of the new digest as in [Verifying an image](#verifying-an-image):

```sh
sudo docker compose stop aether-edge
sudo cp -a data data.pre-update
sudo sed -i 's/^AETHER_EDGE_DIGEST=.*/AETHER_EDGE_DIGEST=sha256:<digest>/' .env
sudo docker compose up -d --wait aether-edge
```

`data.pre-update` is the copy to restore if the release fails. From then on the
updater does the rest.

#### When an update stops

- **`held`**: the release needs a manual step. Read it in the journal (the
  `aether-edge: hold: <reason>` lines). Each run holds again and exits 3 until you
  take it, then run `sudo aether-edge-update --manual-steps-done`. It applies
  only to the digest that was held. A hold because the running release is older
  than `min_upgrade_from` is refused with `--manual-steps-done passes only
  blocking manual steps`: upgrade by hand to a release at or above it first.
- **`broken`**: the rollback failed, and every run exits 1 with `state is broken
  since <time>: <reason>`. Run the edge on a release you trust (put its digest in
  `.env`, restore a backup first if `schema --json` asks for it, then `docker
  compose up -d --wait aether-edge`), and delete
  `/var/lib/aether-edge-update/state` so the next run starts over.
- A digest in `/var/lib/aether-edge-update/failed` is skipped on every run.
  Delete its line to try it again.
- A host that reboots mid-update is recovered at boot by
  `aether-edge-update-recover.service`, or by `sudo aether-edge-update
  --recover`.

To go back to the previous release by hand, run `sudo aether-edge-update
--rollback`. If the older release cannot run the migrated database, it changes
nothing and exits 1 with the backup's age, because restoring loses every account,
device and server change since that backup. To roll back anyway, add
`--restore-backup`. The digest it leaves goes into `failed`.

### Operator commands

Run them as the edge's user: they refuse any other user than the owner of
`edge.db`. `--data` (default `AETHER_EDGE_DATA`, else `/var/lib/aether-edge`) goes
before the argument. While an edge runs they never migrate, so run them with the
running edge's binary or image, as `docker exec <container> aether-edge servers
list` does. Otherwise they are refused with `... an edge is running on
/var/lib/aether-edge, so run this command with the same image or binary as the
running edge`.

None of the commands grants anything: no command approves a device, records an
owner or admits an account to a server.

```sh
sudo -u aether-edge aether-edge servers list
sudo -u aether-edge aether-edge servers remove <server id>
sudo -u aether-edge aether-edge servers block <server id>
sudo -u aether-edge aether-edge servers unblock <server id>
sudo -u aether-edge aether-edge accounts list
sudo -u aether-edge aether-edge accounts block github:<user id>
sudo -u aether-edge aether-edge accounts unblock github:<user id>
sudo -u aether-edge aether-edge accounts delete github:<user id>
```

| Command | Effect |
| --- | --- |
| `servers remove` | Forgets the server's claim and directory and refuses new connections. The server learns it is unclaimed when it next connects, and its administrator claims it again with a new code |
| `servers block` | Does what `remove` does, and refuses the id at enrollment with `server is blocked by this edge's operator` |
| `servers unblock` | The id can enroll and be claimed again |
| `accounts block` | Deletes the account's sessions, device tokens and pending sign-ins, and refuses its sign-ins with `account is blocked by this edge's operator`. Servers it owns stay claimed |
| `accounts unblock` | The account can sign in again. Its devices sign in anew |
| `accounts delete` | What the person's own deletion does, except that it closes no live connection and each server receives the deletion at its next enrollment. A block stays |

Connections already open stay up until they end or the edge restarts. `sudo
systemctl restart aether-edge` ends them, and each server reconnects and
receives the deletions it is owed.

A server id comes from a host key anyone can generate, so blocking an id stops
that one server, and a blocked person can sign in with another GitHub account.
Anyone who signs in can claim a server and relay SSH to it. When a server or an
account misuses the relay, block it.

### Files

The edge's own files are described under
[Backup and recovery](#backup-and-recovery). Each server keeps its edge state in
`<data-dir>/edge/` (directories 0700, files 0600), independent of the edge's
host names:

| Path under `<data-dir>/edge/` | Holds |
| --- | --- |
| `edge_key.json` | The pinned edge signing key |
| `keys/sha256-<fingerprint>/owner.json` | The account that owns the server at the edge holding that key |
| `keys/sha256-<fingerprint>/edge_state.json` | Whether that edge last reported the server `claimed` or `unclaimed`. While `unclaimed`, invitations and account links are refused |
| `claim.json` | The claim code's hash, expiry, attempts left and the admin `--admin` named. Never the code |
| `status.json` | Last connection state, for `edge status` |
| `access_policy.json` | The policy of the last start, to log a change |
