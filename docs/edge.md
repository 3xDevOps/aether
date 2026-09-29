# Edge remote access

An **edge** is a sign-in service and relay that Aether servers and clients
both dial out to, so a laptop reaches a server with no Tailscale, no open
router port and no SSH key to copy. The project runs one; `aether-edge`
runs your own. Tailnet identities, member SSH keys, invite codes, the local
gateway and the tailnet dashboard work beside it exactly as they do without
it.

Terms used below:

- An **account** is a person signed in to the edge with GitHub. It is
  keyed by the pair `github:<user id>`, GitHub's immutable numeric id, and
  the edge gives it an account id of its own. A login or email identifies
  no account: two GitHub accounts that held the same email are two
  accounts.
- A **device** is one client install. It holds a **device key**, an Ed25519
  key of its own (not your `~/.ssh` key), and a **device token** from the
  edge.
- A **grant** is a statement the edge signs for one connection: "this
  account, on this device, is opening this connection to this server". It
  lives 60 seconds.
- The **server id** is 26 characters derived from the server's SSH host
  key. Clients check the host key against it on every connection.
- The **directory** is the list of members and open invitations a server
  pushes to the edge, so the edge can refuse strangers early.
- The **access policy**, the server's `edge-access` setting, decides what a
  grant is worth ([Access policies](#access-policies)).
- The **owner** is the account that claimed the server at the edge.

The relay never decrypts SSH. It splices two outbound WebSocket
connections, and SSH runs end to end between your client and the server.
The server believes the edge about who signed in; under `account` that is
enough for a member's device, under `approved-devices` it is not. Role,
membership and device status are always the server's own decision.

## Two host names

An edge answers on two origins, served by one process:

| Origin | Project's edge | Serves |
| --- | --- | --- |
| Sign-in | `https://auth.onaether.dev` | Sign-in pages, OAuth callbacks, the edge's session cookie, the Devices, Servers and Account pages, and the client API: device sign-in, logout, server list, account deletion |
| Relay | `https://edge.onaether.dev` | Server enrollment and control connections, client connections, and the edge's metadata, `GET /v1/edge` |

Clients and servers are configured with the relay origin. A client reads
the sign-in origin from the metadata when you run `aether login`. A path
requested on the wrong host is refused with `421 Misdirected Request` and
`<path> is served on <origin>, not on this host`; any other host name with
`this edge serves <signin origin> and <relay origin>, not the host
"<host>"`. `GET /healthz` answers on both. The edge's cookies are `__Host-`
cookies with no `Domain` attribute, so a browser sends them to the sign-in
host only, never to a sibling host under the same domain.

### What each identity and pin depends on

| Thing | Bound to |
| --- | --- |
| OAuth callback addresses, the edge session cookie, the client API | The sign-in host |
| A client's stored device token | The relay origin it is filed under and the sign-in origin that issued it; it is sent to those two and nowhere else |
| A server's enrollment signature | The relay origin the server dials: it signs `aether-edge-enroll-v1`, that origin, its id and a nonce |
| A server's pin of the edge | The edge signing key: one pin per server, whatever host name `edge-url` names |
| A server's own record of its owner | The edge signing key, under `<data-dir>/edge/keys/sha256-<fingerprint>/` |
| A server's claim code | The server, under `<data-dir>/edge/` |
| Grants | The edge signing key, and the relay origin, which each grant names as its issuer |
| The edge's record of a server: claim, owner, policy | The server id, in the edge's database |
| A client's pin of a server | The server id |
| An account | GitHub's numeric user id. It does not depend on the OAuth app |

### If you change one of them

| Change | Servers | Clients and people |
| --- | --- | --- |
| Sign-in host name | Nothing: servers never contact the sign-in origin | Browsers sign in again. Relayed connections keep working with the stored token, but `aether servers`, `aether logout` and `--delete-account` go to the old sign-in origin and fail with its error until `aether login` runs again. The replaced token stays valid at the edge until it is revoked on the Devices page. The operator changes the OAuth app's callback URL on GitHub |
| Relay host name | Each operator sets `edge-url` to the new origin and restarts; a server still on the old origin is refused. The edge key is the same, so the server keeps its pin and its owner, and the edge keeps it claimed under the server id: relaying, `aether member transfer` and the ownerless recovery work as before (`TestRelayHostNameChangeKeepsPinAndOwner`) | `aether login --edge <new relay origin>`, then `aether link <server id> --edge <new relay origin>` again. The server id and each device's approval stay: the server knows a device by its key |
| Edge signing key | Every server refuses the edge with `edge key changed: ...` until its operator runs `sudo aether-server edge trust` and types `yes`. The server keeps its owner per edge key and has none for the new one, so when it reconnects it tells the edge it is ownerless; an admin claims it again with a code from `sudo aether-server edge claim-code`. Members, roles and devices stay (`TestPinAndOwnerFollowTheEdgeKey` in `internal/edge/agent`) | Nothing: clients do not pin the edge key |
| A server's host key | New server id. At the edge it is a new, unclaimed server: an admin whose account is linked claims it with a code from `sudo aether-server edge claim-code`. Members, roles and devices stay on the server. The old id stays listed until its owner removes it on the Servers page | Every link refuses the new key; link again by the new id |
| OAuth app (client id or secret) | Nothing | Nothing: accounts, tokens, sessions and server identities stay. The next GitHub sign-in may show the authorization page again |

Back up the edge signing key and each server's host key
([Backup and recovery](#backup-and-recovery),
[Server id and claiming](#server-id-and-claiming)) so that neither has to
change.

## Access policies

Each server chooses its policy, and enforces it itself. The relay carries
connections under both: the edge admits a connection, the server
authorizes it.

| | `account` | `approved-devices` |
| --- | --- | --- |
| What signing in gives | Access, with the member's role | Identification only |
| A member's new device | `registered` on its first connection, and admitted | `pending`, and refused with its approval code |
| A member's first device | Registered | Pending, like every other |
| Who approves | Nobody needs to | The member from an approved device, SSH key or tailnet connection; an admin the same way; or `sudo aether-server device approve <code>` on the machine |
| The claiming device | Approved: the code came from the machine's console | Approved, the same way |
| Lost every device | Sign in on a new one | An admin, or the machine's administrator, approves the new one |
| Suits | One person, or a team that already relies on its GitHub accounts for source access | A server holding code or credentials that must stay protected even if a GitHub account, or the edge, is taken over |

Under both: the server maps the account to a member itself, roles and
capabilities are the usual ones, every device is listed with `aether device
list` and revocable, and the server checks the device key on every
connection.

### Who must fail for access to be gained

- **`account`:** an attacker gets in when a member's GitHub account is
  taken over, or when the edge is (its host, its database or
  its signing key). Either one is enough, and a compromised edge reaches
  every member of every server on it that chose this policy.
- **`approved-devices`:** one of those, and also a person who approves the
  attacker's device by typing the code that device shows. The edge and
  GitHub each identify; neither can admit a device.
- **Under both:** a stolen device key alone reaches the server's direct
  address once that device is approved, like a member SSH key; through the
  edge it also needs the device's token or the edge. A stolen device token
  alone authenticates nothing: the server also requires the device key.

Neither policy rests on OAuth being weaker or SSH keys stronger. The
difference is how many parties must fail.

### What an attacker can do

With a member's GitHub account, the edge honest:

| | `account` | `approved-devices` |
| --- | --- | --- |
| Sign in and list the member's servers: names, ids, roles, online state, policy | Yes | Yes |
| Reach a workspace | **Yes**, with the member's role on each server they belong to (`TestTakenOverProviderAccount`) | No: the new device is pending (`TestTakenOverProviderAccount`) |
| As an admin | Invite accounts, mint invite codes, change roles, link accounts, transfer ownership. Not approve a device: a registered device approves nothing (`TestAccountAccess`) | Nothing: a pending device completes no handshake (`TestApprovedDevices`) |
| Accept an invitation addressed to the member | Yes, with its role (`TestAccountAccess`) | No: the device waits on the invitation. No member is created, no account bound and the invitation stays open until an approver types the device's code (`TestInvitationWaitsUntilOneDeviceIsApproved` in `internal/sshd`) |
| Persist after the member recovers the account | Yes: the device token does not expire and the device stays registered until revoked at the edge and on each server, along with anything done as an admin ([Recovering a GitHub account](#recovering-a-github-account)) | A pending device and a device token remain, with no access |
| Get a device approved | Not needed | Only by having an approver type the code shown on the attacker's device. The approver is shown the member and role the code admits the device as before anything is approved |
| Disrupt | Revoke the member's device tokens; delete the account, which removes its identity and edge devices on every server | The same |
| Be noticed | The device is listed with the account it signed in as in `aether device list`, `aether member identities <id>`, the dashboard's Devices view and `sudo aether-server device review`, and on the edge's Devices page | The same, as pending |

With the edge, or its signing key:

| | `account` | `approved-devices` |
| --- | --- | --- |
| Forge a grant for any account | Yes | Yes |
| Reach a workspace | **Yes**, as any member of any server on that edge using this policy, with that member's role (`TestMaliciousEdgeAccountAccess`) | No: a forged grant yields a pending device (`TestMaliciousEdgeApprovedDevices`) |
| Accept an open invitation | Yes, with its role | No: the device waits on the invitation, and no member, administrator or bound account exists until an approver types that device's code (`TestMaliciousEdgeApprovedDevices`) |
| Record a device an honest client relays under another account than the one it signed in as | No: the client names its account in the SSH user name, under its own signature, and the server refuses before recording anything: `this device connects as "github:<user id>", but the edge signed the connection in as <account> (github:<user id>); nothing was recorded` (both tests). An edge that also lied to the client at sign-in about which account it is gets past this check, as for a claim | The same |
| Record its own key under a member's account | Yes, admitted as that member | Pending: `aether device approve`, `sudo aether-server device approve` and the dashboard show whoever is handed its code that it admits the key as that member, with that role, and approve only after a yes (`TestMaliciousEdgeApprovedDevices`) |
| Grow the server's device table | Registered devices are admitted devices, not bounded | At most 10 pending devices per account and 10 per invitation (`TestWaitingDevicesAreBounded` in `internal/sshd`) |
| Replay a connection, or change the policy, a member, a role or the owner by altering or injecting control messages | No: the server refuses a used connection id and takes no instruction from the edge's messages | No (`TestMaliciousEdgeApprovedDevices`, `TestServerVerifiesGrants`) |
| Send a forged account deletion | Removes that identity and its edge devices on the server, including approved device keys used on the direct path: access lost, none gained. An admin restores it with `aether member link` | The same (`TestMaliciousEdgeApprovedDevices`) |
| Read or alter an SSH session | No: SSH is end to end | No |
| Impersonate a server to a linked client | No: the host key must derive the linked id (`TestEnrollmentSignatureIsNotAHostSignature`) | No |
| Send a client that is linking for the first time to another server | Yes, by listing a false id, unless the person links by an id from the server's admin ([First link](#first-link-and-server-identity)) | The same |
| Take a claim | No: the code travels inside SSH, and the server refuses a claim whose grant names another account than the client's (`TestClaim`, `TestMaliciousEdgeSubstitutesTheClaimingAccount`). An edge that also lied to the client at sign-in about which account it is could claim for its own account; `aether login`, `aether link --claim` and the dashboard's claim form show the account the edge reported before the code is sent | The same |
| Learn who connects to which server, when, from where; deny service | Yes | Yes |

The tests named are in `internal/edge/edgetest` ([testing.md](testing.md#the-edge-suite)).

### Setup and defaults

`aether-server setup` asks after it turns the edge on, and repeats the
question until you answer `1` or `2`; Enter selects nothing:

```
Who may reach this server through the edge?

  1) Account access
     Signing in with GitHub is enough. People you invite start on a new
     device by signing in, and Aether creates and manages the device key.
     Access is as strong as each person's GitHub account and the edge that
     vouches for it.

  2) Approved devices
     Signing in says who someone is. It does not admit a device. Each new
     device waits until it is approved: by that person from a device they
     already use, by an admin, or by you on this machine. A taken-over
     account, or a compromised edge, cannot add a device on its own.
     Recommended for a team workspace holding code or credentials that
     must stay protected.

Both leave Tailscale and direct SSH key access as they are.
Change it on this machine: aether-server config set edge-access <account|approved-devices>

Choose 1 or 2:
```

| Setting | Default |
| --- | --- |
| `edge-url` | Empty: the edge is off |
| `edge-access`, interactive setup | None: the question repeats until answered |
| `edge-access`, key missing from the config | `approved-devices` |
| `aether-server install --edge-url <url>` without `--edge-access` | Refused: `--edge-url turns the edge on and needs --edge-access: account (...) or approved-devices (...)` |

### Changing the policy

Only on the server's host; no RPC, edge message, dashboard action or
client command changes it.

```sh
sudo aether-server config set edge-access approved-devices
sudo systemctl restart aether-server
```

```
/etc/aether/server.conf: edge-access = approved-devices (was account)
apply it with:
  systemctl restart aether-server
```

At each start the server logs the policy, and the previous one when it
changed, whether `config set`, `config edit` or a hand edit changed it:
`edge: access policy changed since the last start from=account
to=approved-devices`. The server announces its policy to the edge, which
shows it in `aether servers`; the edge's copy is never an input to the
server's decision. Setting it to an empty value is refused.

Switching changes no device's status. A device registered under `account`
is refused under `approved-devices` with `device "<label>" was admitted by
signing in alone and is waiting for approval: this server now admits
approved devices only`, followed by its approval commands. An approval is
never made by signing in alone, under either policy, and neither is an
invite code: `aether invite` needs an approved device, a member SSH key or
a tailnet connection. A switch inherits nothing. To see what a switch
affects:

```sh
sudo aether-server device review
```

It walks through every registered and pending device, with its member, the
account it signed in as, its key fingerprint and when it was first and
last seen, and asks `approve, revoke or skip? [a/r/S]`; Enter skips. It
then lists what reaches the server whatever `edge-access` says:

```
These reach the server whatever edge-access says; an admin removes them with `aether member remove <id>`:
  SSH key SHA256:<fingerprint> of <name> (<member id>), admin
  tailnet login <login> of <name> (<member id>), collaborator
```

Member SSH keys and tailnet identities are independent of the edge and of
the policy. Turning the edge off (`edge-url ""`) leaves them working.
Removing one means removing the member with `aether member remove <id>`,
or, for tailnet access, removing the device from the tailnet.

## Server side

### Turning it on

The edge is off until you turn it on. `aether-server setup` offers it:
without tailscaled it turns on `https://edge.onaether.dev` and prints what
the edge can see; with tailscaled it asks, defaulting to no. Otherwise only
`aether-server install --edge-url <url> --edge-access <policy>` or the
config keys turn it on, so a config written before the edge existed never
dials one.

```sh
sudo aether-server config set edge-url https://edge.onaether.dev
sudo aether-server config set edge-access approved-devices
sudo aether-server config set edge-url ""     # off
```

The server keeps one outbound control connection to the edge, pinging
every 20 seconds and reconnecting with jittered backoff from 1 to 60
seconds. The agent honours `HTTPS_PROXY` from
`/etc/aether/aether-server.env`.

The server trusts an edge by its signing key, not its host name. It pins
the key on first connection and keeps one pin, whatever `edge-url` says,
and it keeps its owner per edge key ([Files](#files)). A new host name of
the same edge keeps both. An edge with another key is refused with `edge
key changed` until `sudo aether-server edge trust` pins it; the server then
has no owner there and is claimed with a new code. Trusting the first key
again finds its owner. To move an enrolled server to another edge:

```sh
sudo aether-server edge leave                 # unenroll at the old edge, forget its owner
sudo aether-server config set edge-url https://<edge-host>
sudo aether-server edge trust                 # compare the key with the one the edge's operator publishes, type yes
sudo systemctl restart aether-server
sudo aether-server edge claim-code
```

### Server id and claiming

The server id is derived from the SSH host key,
`<data-dir>/ssh/host_ed25519_key`. Back that file up: a new host key is a
new server, which must be claimed and linked again.

Until an account claims it, the edge relays nothing for a server but claim
connections. Setup prints:

```
edge: https://edge.onaether.dev
edge access: approved-devices
server id: <server id>
edge key: pinned when the server first connects; `aether-server edge status` shows it
claim code: <server id>-<secret> (valid until 3:04PM, 5 attempts)
claim this server with:
  aether link --claim <server id>-<secret>
```

The claim code lasts 30 minutes and five attempts; `sudo aether-server
edge claim-code` prints a new one and is refused while the server has an
owner. Start the server first: `aether link --claim <code>` opens an SSH
connection through the edge to the server the code names, and presents the
code inside it once the server's host key has proved the id
([Claiming](#claiming)). The server compares the code in constant time,
makes the claiming account its admin, and approves the claiming device
under either policy, because the code came from the machine's console.

A server that already has members is claimed by an admin who first links
their edge account over SSH key or tailnet
([teams.md](teams.md#linking-an-existing-member)), or by an admin the
machine's administrator names ([Console recovery](#console-recovery)). Any
other claim on such a server is refused with `server is already claimed`,
so a claim never makes a new admin there.

The edge records the owner only when the server reports the claim on its
own control connection, within 60 seconds of the claim connection opening,
and only as the account that connection was opened for. The server records
the claim only once that report is sent. When the edge cannot record a
claim, or a report that the server is ownerless, for example because the
account was deleted meanwhile, it closes the control connection with
`ownership report refused: <reason>`, which `sudo aether-server edge
status` shows; the server reconnects and learns whether the edge holds it
as claimed. A transfer is answered instead ([Transfer and the ownerless
state](#transfer-and-the-ownerless-state)).

### Commands

| Command | Does |
| --- | --- |
| `sudo aether-server edge status` | Edge, access policy, server id, pinned edge key, owner or claim code state, connection state |
| `sudo aether-server edge claim-code [--admin <member id>]` | Issue a new claim code, replacing the old one. Refused while the server has an owner. `--admin` names an existing admin the claim binds the claiming account to ([Console recovery](#console-recovery)) |
| `sudo aether-server edge trust` | Fetch the edge's signing key from `GET <edge>/v1/edge`, show both fingerprints, and pin it after you type `yes` |
| `sudo aether-server edge leave` | Unenroll at the edge, forget the owner and the claim code, set `edge-url ""` |
| `sudo aether-server device approve <code>` | Show the device a code names, the account it signed in as and the member and role approving admits it as, and approve it after you answer `y` |
| `sudo aether-server device review` | Approve, revoke or skip each registered and pending device; list member SSH keys and tailnet logins |

```
edge         https://edge.onaether.dev
edge access  approved-devices
server id    <server id>
edge key     SHA256:<fingerprint>
owner        github <login> (subject <user id>)
connection   connected since <time>
```

The server pins the edge's signing key on first enrollment. A different key
later is refused, and the server keeps retrying with this error in its log
and in `edge status`:

```
edge key changed: pinned SHA256:<old>, edge presents SHA256:<new>; if the edge's operator rotated its key, or edge-url now names another edge, run `aether-server edge trust`
```

Run `edge trust` only after the edge's operator has confirmed the new
fingerprint. The server has no owner for a key it has not been claimed
under; when the edge still records one, the server reports itself
ownerless as it reconnects, and `edge claim-code` gives it an owner again.
A server id the edge's operator blocked is refused at every enrollment,
and `edge status` shows the reason:

```
connection  disconnected since <time>: read ready: failed to get reader: received close frame: status = StatusPolicyViolation and reason = "server is blocked by this edge's operator"
```

Every control connection the edge closes carries its reason the same way,
logged as `edge: control connection ended; reconnecting`: for example
`replaced by a newer connection of this server` or `server silent for
1m0s`; a restarting edge is logged as `edge is restarting (drain)`. `EOF`
there means the connection was cut without the edge closing it, such as
by a network failure. `edge status` shows `connected` once the server has
reported its owner to the edge, and from then `aether member transfer`
works.

### Files

`<data-dir>/edge/` holds the server's edge state. None of it depends on
the edge's host names. Directories are mode 0700, files 0600:

| Path under `<data-dir>/edge/` | Holds |
| --- | --- |
| `edge_key.json` | The pinned edge signing key |
| `keys/sha256-<fingerprint>/owner.json` | The account that owns the server at the edge holding that key; the fingerprint is the key's, with `+` and `/` written `-` and `_` |
| `claim.json` | The claim code's SHA-256 hash, expiry, attempts left and the admin `--admin` named; never the code |
| `status.json` | Last connection state, for `edge status` |
| `lock` | Serializes claim attempts and owner changes between the server and the commands |
| `access_policy.json` | The policy of the last start, to log a change |

`aether-server edge` commands run as root leave every file they write here
owned by the data directory's owner, so the server keeps reading it.

Development builds before this layout kept a directory per relay origin,
such as `https_edge.onaether.dev/`. That layout never shipped and is not
read. A server with no `edge_key.json` that finds one in such a directory
refuses to connect rather than pin whatever key the edge presents, with
this error in its log and in `edge status`:

```
edgeagent: <data-dir>/edge/https_edge.onaether.dev/edge_key.json: an edge key pinned by an earlier development build, which this server does not read; pin the edge's key with `sudo aether-server edge trust`, or remove the old state with `sudo rm -r <data-dir>/edge/https_edge.onaether.dev` to pin the key the edge presents at the next connection
```

Either command resolves it. The server has no owner under the new layout,
so it reports itself ownerless and is claimed again.

## Client side

### Signing in

```sh
aether login              # prints an address and a code; confirm the code in the browser
aether servers            # servers your account reaches
aether logout             # revokes this device's token at the edge and deletes it here
```

```
auth.onaether.dev signs you in for the edge edge.onaether.dev
open https://auth.onaether.dev/device and enter the code ABCD-EFGH
waiting for you to confirm the code...
signed in to edge.onaether.dev as <login> (github); this device is "<host name>"
```

It then prints how long the token lasts and what ends it
([Credentials](#credentials-and-revocation)). The confirmation page shows
the address the sign-in started from and whether it is the browser's own;
confirm only a code you started. The client refuses metadata whose sign-in
origin is not `https://host[:port]` (plain `http` only for a loopback
host), a verification address on another origin, and an edge whose
protocol versions it does not share.

`--edge <url>` takes the relay origin; without it commands use the one edge
this machine is signed in to, else `https://edge.onaether.dev`. The config
directory holds `edge-device-key` (0600), `edge-tokens.json` (0600) and
`edge.lock`, which serializes concurrent sign-ins. A key or token file that
group or others can read is refused with the `chmod 600 <path>` to run (not
on Windows). A token never appears in output or errors, and the client
follows no redirect.

### Servers

```
ID                          NAME  ROLE   ONLINE  KIND         ACCESS
<server id>                 prod  admin  yes     self-hosted  approved-devices: a new device waits for approval
<server id>                 solo  admin  yes     self-hosted  account: signing in is enough
link by the id the server's admin gave you, or that aether-server edge status printed: aether link <id>
link from this list, which the edge supplies: aether link --from-edge <id>
```

Both `KIND` and `ACCESS` are what the edge reports; the server enforces its
own policy. `self-hosted` is a server its owner runs; `hosted` is reserved
([Trust models](#trust-models)). An entry naming no policy is shown as
`approved-devices`. `aether servers` also lists servers you are only
invited to.

When the server refuses this device, because it is pending or was
registered under `account` and the server now requires approval, the
command fails with the server's own message, line by line:

```
device "dana-laptop", signed in as github account dana, is waiting for approval. Approve it from an approved device, SSH key or tailnet connection of this account, or as an admin:
  aether device approve <code>
or on the server:
  sudo aether-server device approve <code>
```

`aether link` and `aether edge-ssh` (git) print it; the sync daemon logs it.

Every relayed connection names, in its SSH user name, the account this
device signed in as, `github:<user id>`. The device's SSH signature
covers it, so the server refuses a grant that names another account before
it records anything.

## First link and server identity

A link pins the server id: every later connection, direct or through the
edge, must present a host key that derives it. The first link therefore
decides which server you reach. There are two ways to make it, plus a
claim:

| Command | Where the id comes from | Who you trust for it |
| --- | --- | --- |
| `aether link <server id>` | The server's admin, who reads it from `sudo aether-server edge status` or setup's output | The admin and the channel they sent it over. The edge cannot substitute another server |
| `aether link --from-edge <server id> [--yes]` | `aether servers`, which the edge supplies | The edge: one that lists a false id can send this first link to another server. The command reads the host key through the edge first, checks it derives the id, and shows what it will pin before asking |
| `aether link --claim <code>` | The claim code, printed on the server's console | Whoever handed you the code; the id is inside it |

`--from-edge` prints:

```
server "prod"
  id:       <server id>
  host key: SHA256:<fingerprint>
  access:   approved-devices: a new device waits for approval
This id comes from edge.onaether.dev. From now on aether accepts only this host key for the link, directly
and through the edge. An edge that lists a false id can send a first link to another server: compare the
id with the one the server's admin gives you, or that aether-server edge status prints on the server.
link and pin this server? [y/N]:
```

Reading the host key is a relayed connection like any other, so the server
receives the edge's grant for it; it ends after the key exchange, before
this device authenticates. Without a terminal it refuses unless `--yes` is
given, and names `aether link <server id>` instead. The dashboard's link
step does the same: a **Server id from your admin** field links directly,
and a server picked from the list shows its id and host key and asks
**Link and pin** first.

On the server, the id is in `sudo aether-server edge status`, and the host
key fingerprint that `--from-edge` shows is:

```sh
sudo ssh-keygen -lf /var/lib/aether/ssh/host_ed25519_key    # <data-dir>/ssh/host_ed25519_key
```

Only a 26-character server id goes through the edge. Any other argument,
including a bare name such as `my-server`, is an SSH address and never
reaches the edge: server names are chosen by each server's admin.

### Claiming

`aether link --claim <code>` dials the edge's claim path,
`/v1/connect/<server id>/claim`, with the id from the code, and offers the
code and the account this device signed in as in the SSH user name,
`claim:<code>:github:<user id>`. An SSH client sends the user name only
after the key exchange has verified the host key, so the code crosses the
edge encrypted, and only to a server whose host key derives the id in the
code. Another host key ends the connection with both server ids and the
presented key's fingerprint, before the code is sent. The server refuses a
claim whose grant names another account before it tries the code:

```
this device claims as github account <subject>, but the edge signed the connection in as github account <login>; the claim was not attempted
```

Before it dials, the command prints the account the claim is made for,
the one the edge reported when this device signed in:

```
claiming server <server id> as <login> (github), the account edge.onaether.dev reported when this device signed in
```

On success the same connection becomes the link:

```
claimed server <server id>
linked to server <server id> through edge.onaether.dev as <your name> (admin)
```

The edge admits a claim connection only from a signed-in device, only to a
connected server without an owner, and within limits per account, per
client address and per server ([Limits](#limits)).

### Direct address and git

```sh
aether link <server id> --addr host:2222    # also try this SSH address first
```

With `--addr`, the client dials that address first (3-second timeout) and
falls back to the edge; when both fail the error names both causes, with
what the server said, and when only the address fails its error goes to
stderr followed by `aether: reached server <id> through <edge> instead`.
That stderr line leaves out the server's approval banner: under `account`
the edge admits a device the direct path refuses until it is approved, and
the banner would repeat on every command. On both paths the host key
must derive the server id and nothing is read from or written to
`known_hosts`. The device key authenticates on both, but the direct path
accepts approved devices only, under either policy.

A linked repository's `aether` remote uses the logical host
`<server id>.edge.aether.invalid`, which never resolves. Aether's own git
processes set `GIT_SSH_COMMAND` to `aether edge-ssh`. For `git push aether`
typed by hand, `aether link` sets the repository's `core.sshCommand` to the
same command, which handles edge hosts itself and runs the system `ssh`
unchanged for every other host. It names `aether` by its `PATH` entry when
that entry is the running binary. An existing `core.sshCommand` is
overwritten only when it is exactly `<path to aether> edge-ssh` from an
earlier link; otherwise `aether link` prints the line to add. The sync
daemon takes the same link: `aether daemon install --server-id <id>
--edge-url <url> [--server host:port]`.

Linking another server in place of a default link that has a server id
keeps the old one as a saved link named by its server id, so the old
repository's `git push aether` and `aether --server <server id>` keep
working.

## Credentials and revocation

| Credential | Where | Lifetime | Ended by |
| --- | --- | --- | --- |
| Device key | `edge-device-key` on the client | Until you delete the file | Nothing in Aether deletes it. Servers know the device by it; a new key is a new device |
| Device token | `edge-tokens.json` on the client, a SHA-256 hash at the edge | No expiry, never refreshed | `aether logout`, the edge's Devices page, deleting the account, or the operator blocking the account |
| Sign-in code from `aether login` | Edge | 10 minutes | Confirming or refusing it |
| Edge session (browser cookie) | Edge | 30 days after last use | Signing out, deleting the account, the operator blocking it |
| Grant | Signed by the edge | 60 seconds, admitting one connection; 30 seconds of clock skew allowed | Its first use. It does not bound a connection already open |
| Claim code | Server console | 30 minutes, 5 attempts | Its use, or a new code |
| Invitation | Server | 7 days | `aether invite revoke`, its use, or a demotion of its creator |
| Device status on a server | Server | Until changed | `aether device revoke`, `device review`, `aether member unlink`, removing the member, or deleting the account at the edge |

`aether logout` revokes the token at the sign-in origin that issued it,
then deletes it from `edge-tokens.json`. When the edge cannot be reached it
keeps the token and says so, so you can retry. It never deletes the device
key or a link.

### What ends a live connection, and how fast

| Action | Relayed connections | Direct connections with the device key | Measured in `TestRevocationClosesLiveConnections` |
| --- | --- | --- | --- |
| `aether member remove <id>` | Closed at once | Closed at once | 9 ms |
| `aether device revoke <id>` | Closed at once | Closed at once | 7 ms |
| Revoking in `sudo aether-server device review` | Closed at the running server's next check, at most 3 seconds | The same | 1.0 s |
| `aether logout`, or revoking on the edge's Devices page | Closed at once by the edge and the server | Stay open, and new ones are admitted: the device is still approved on the server | 11 ms |
| Deleting the account ([Deleting an account](#deleting-an-account)) | Closed at once | Closed: each server removes the account's identity and edge devices | 35 to 40 ms |
| Deleting the account while a server is offline | Closed at once | That server closes them when it next connects to the edge and receives the deletion | `TestAccountDeletionReachesAnOfflineServer` |
| `aether-edge accounts block` or `accounts delete` by the operator | Stay open until they end or the edge restarts | `accounts delete` reaches each server at its next enrollment | Not measured |
| Member removal or device revocation while the edge is down | Closed at once | Closed at once, and still refused once the edge is back | 6 ms (`TestRevocationWhileTheEdgeIsDown`) |

The times are from one run of the suite on a development machine, with
`-race`. A revocation made on the server takes effect without the edge.

A relayed connection the edge closes ends with the edge's reason in the
client's error, such as `received close frame: status =
StatusPolicyViolation and reason = "not a member of this server"` when the
server's directory drops the account mid-handshake. The server logs the
same reason as `edge: the edge closed a relayed connection`. One the server
closes ends with `EOF`.

A link survives a revoked token and a deleted account:

| Event | Through the edge | A link's `--addr` | SSH keys and tailnet identities |
| --- | --- | --- | --- |
| Token revoked | Refused: `device token revoked` | Still works with the approved device key | Unaffected |
| Account deleted | Refused the same way | Refused: the server removed the account's edge devices | Unaffected |

### Recovering a GitHub account

Recovering a GitHub account ends the attacker's use of it for new
sign-ins, and nothing else. Devices the attacker signed in while holding it
keep their device tokens, which do not expire, and stay on every server
until revoked. After recovery:

1. Sign in on the edge's Devices page (`https://auth.onaether.dev/devices`
   on the project's edge) and revoke every device you do not recognize.
   That ends its token and its relayed connections.
2. On each server, run `aether device list` and `aether device revoke
   <device-id>` for each unknown device; an admin sees every member's. On
   the machine, `sudo aether-server device review` shows each registered
   or pending device with the account it signed in as and when it was
   first seen.
3. Under `account`, if the account was an admin's, check what an admin
   could have done: `aether member list`, `aether invite list` (open
   invitations and account links), and the SSH keys and tailnet logins
   `device review` lists at its end. Revoke invitations with `aether invite
   revoke <id>` and remove unknown members with `aether member remove
   <id>`. A device that only signed in cannot mint invite codes, but one an
   approved device, SSH key or tailnet connection minted is a file in the
   server's invites directory until redeemed or expired; no command lists
   them.
4. If the account was linked to a member while someone else held it, list
   that member's accounts and devices and remove the account without
   removing the member:

   ```sh
   aether member identities <member-id>
   aether member unlink <member-id> github:<user id>
   ```

   ```
   unlinked github:<user id> from member <member id>; revoked 2 device(s) that signed in with it
   ```

   The account's devices are revoked, not deleted, so their keys stay
   refused, and their connections close. The member, its role, its other
   accounts, SSH keys and tailnet identity stay. The member themself or an
   admin may do it; under `approved-devices` not from a pending or
   registered device.

Under `approved-devices` the attacker's devices are pending and had no
access; revoke them anyway so nobody approves one by mistake.

### During an edge outage

- Servers keep running and retry every 1 to 60 seconds. Running work is
  untouched.
- Relayed connections drop. Links with `--addr`, tailnet members, member
  SSH keys, invite codes, the local gateway and the tailnet dashboard work
  as before.
- Every revocation and approval made on a server works, from a direct or
  tailnet connection or the machine.
- `aether login`, `aether servers`, `aether logout` and account deletion
  fail with the edge's error. `aether logout` keeps the token so it can be
  retried.

## Ownership and deleting an account

### Transfer and the ownerless state

An admin hands ownership at the edge to another admin who has a linked
GitHub account:

```sh
aether member transfer <member-id>
```

```
member <member id> now owns this server at its edge, as github account <login>
```

An admin with two linked GitHub accounts is refused until one is removed
with `aether member unlink`. The server reports the
transfer to the edge and keeps the new owner only once the edge answers
that it recorded it; a refusal fails the command with `ownership report
refused: <reason>`, and no answer within 10 seconds fails it too. Either
way the server keeps its previous owner, and at every enrollment it
reports its owner again, so the edge ends up recording the server's
(`TestTransferStandsOnlyOnceTheEdgeRecordsIt`). The exception is an edge
that recorded a transfer whose answer was lost and then refuses the
previous owner, because its operator blocked that account meanwhile: the
two disagree until an admin runs the transfer again. The
command is refused while the server is not connected to the edge or has no
owner. A transfer never creates an admin or changes a role. Each report
carries an id that the edge's answer repeats, so a transfer takes only the
answer to its own report, never the answer to the enrollment report or a
repeated one (`TestTransferTakesOnlyItsOwnAnswer`).

When the owner's identity leaves the server, because the member was
removed or the account deleted, the server forgets the owner and reports
itself ownerless. An ownerless server stays enrolled and keeps relaying for
its members. Only a new claim code gives it an owner again:

```sh
sudo aether-server edge claim-code      # on the machine
aether member link --github <login>     # an admin, over SSH key or tailnet, when the admin's account is not linked
aether login
aether link --claim <code>
```

A member who is not an admin cannot become owner this way: the claim is
refused and makes no admin (`TestDeleteAccountLeavesTheServerOwnerless`).
An admin with no SSH key or tailnet identity to link from is restored on
the machine instead ([Console recovery](#console-recovery)).

### Deleting an account

The Account page, `https://auth.onaether.dev/account` on the project's
edge, lists the servers the account owns and the servers it is a member
of, and deletes it. Only that page deletes an account, and it needs:

- A sign-in with GitHub in this browser in the last 5 minutes. A
  sign-in in another browser does not count, and neither does a device
  token. Without one the deletion is refused with `deleting an account
  needs a sign-in in this browser from the last 5m0s: sign in again at
  <url>, then delete it within 5m0s`; open `<url>`, sign in, and delete
  again.
- The account's GitHub login typed back, and the page's form token. An
  account that lost its login here, because another account signed in
  holding it after a rename on GitHub, types its email instead, or its
  account id when it has no verified email.

So deleting takes the person's GitHub sign-in, or a session cookie
stolen within 5 minutes of its sign-in. `aether logout --delete-account`
lists the same servers and where to delete:

```
account <login> (github) at edge.onaether.dev
servers it owns (each becomes ownerless: transfer first with `aether member transfer <member id>` on that server):
  prod  <server id>  admin
servers it is a member of: none
deleting it ends its sign-ins and device tokens, and each server above removes this account's identity and
its edge devices. No server loses a member, a role or data: an SSH key or tailnet identity of yours keeps
working there until an admin removes it. A server left without an owner is claimed again with a code from
`aether-server edge claim-code` on its machine.
delete it in a browser at https://auth.onaether.dev/account: sign in with GitHub there and type <login>. Then run `aether logout` here to
delete this machine's token.
```

The edge deletes the account, its device tokens and its edge sessions, and
closes its relayed connections. Each server the account owned, was a member
of, or was admitted to through this edge is told at once, or when it next
connects. A deletion stays owed, and is sent again at each enrollment,
until the server answers that it applied it
(`TestLostAccountDeletionIsSentAgain`); the edge keeps at most 1000
deletions owed to one server. A server that fails to apply one closes its
control connection and receives it again when it reconnects; applying one
again changes nothing. The server removes that identity and its
edge devices and closes their connections. It removes no member, changes no role and deletes no data.

What stays: the member itself, with its runs and role; its SSH keys and
tailnet identity; invitations to the login or email, until an admin revokes
them. Signing in again creates a new account with a new account id, which
no server knows: an admin links it to themselves with `aether member link`,
or invites it, which creates a new member.

Before deleting an account that is the only way an admin reaches a server,
make sure another admin exists, or that the admin also has an SSH key or a
tailnet identity there. Otherwise only the machine's administrator can
restore them ([Console recovery](#console-recovery)).

### Console recovery

An admin who reached a server only through the edge, and lost that
account, is restored on the machine. The server must have no owner, as
after the owner deletes their account:

```sh
sudo aether-server edge claim-code --admin <member id>
```

```
claim code: <server id>-<secret> (valid until 3:04PM, 5 attempts)
claim this server with:
  aether link --claim <server id>-<secret>
the claim binds the claiming account to admin <name> (<member id>) and approves the claiming device; it creates no member and changes no role
```

The admin signs in with the account they now use and claims with that code.
The server binds the account to that member, approves the claiming device
and becomes owned by that account, and logs `sshd: console recovery bound
an edge account to an admin`. It creates no member and raises no role: a
member who is not an admin is refused when the code is issued and again at
the claim (`is collaborator, not an admin; the claim was refused`), and an
account bound to another member is refused
(`TestConsoleRecoveryOfAnAdminWithoutAnAccount`). The member id is in
`aether member list`; an id that names no member is refused with the list
of admins: `--admin: no member <id>; the admins are: <member id> (<name>)`.

## Trust models

| | User-operated server (available) | Aether-operated workspace (not available) |
| --- | --- | --- |
| Runs the workspace | Its owner | Aether |
| Runs the edge | Aether, or the owner | Aether |
| Who can read the workspace | The owner's machine administrators | Aether's operators |
| What separating edge and server protects against | A compromised edge, under `approved-devices` | Nothing against Aether: one party runs both |
| What `approved-devices` protects against | A taken-over GitHub account, and a compromised edge | A taken-over GitHub account only |
| Server id | Derived from a host key the owner holds | Derived from a host key Aether would hold |

Every server today is user-operated, and `aether servers` shows it as
`self-hosted`. The `hosted` kind is reserved in the protocol for a
workspace Aether would operate; no edge produces it and no hosted feature
exists. Such a workspace would not be protected end to end against Aether's
operators: they would run the machine that holds its host key, its data and
its policy.

## Members, invitations and devices

Invitations by GitHub login or email, device approval and revocation are
in [teams.md](teams.md#through-an-edge).

## The dashboard

The edge does not serve a dashboard in this release. A phone reaches a
server's dashboard over a tailnet, as before
([networking.md](networking.md#the-dashboard)); the Android app does not
use the edge. On a computer, `aether gui` serves the dashboard locally and
reaches the server over the same link as the CLI, through the edge when the
link goes through one ([local-gateway.md](local-gateway.md)).

## Running an edge

`aether-edge serve` is one static Linux binary (amd64 or arm64) with an
embedded SQLite store, installed with a systemd unit
([Installing](#installing)) or run from the image
`ghcr.io/3xdevops/aether-edge` ([In a container](#in-a-container)). It
answers on its sign-in host and its relay host
([Two host names](#two-host-names)) in one of two modes:

- **Behind a reverse proxy** (`--proxy-listen`): plain HTTP on a loopback
  port; nginx on the same machine terminates TLS with certbot certificates.
  The packaged unit and environment file are set up for this mode. A proxy
  in another container or on another machine is named with
  `--trusted-proxies`.
- **Without a reverse proxy** (`--listen`, the flag default): the edge
  terminates TLS on `:443` itself, with Let's Encrypt certificates for
  exactly its two names.

Values you supply are written as placeholders: `<signin-host>` and
`<relay-host>` are the two public host names, such as `auth.example.com`
and `edge.example.com`.

### What it needs

- A Linux host with systemd 248 or newer, or a container runtime, and a
  public IPv4 address (IPv6 optional). The edge limits requests by client
  address, so no other proxy or load balancer may stand between clients
  and the edge's own proxy.
- Outbound HTTPS to `github.com` and `api.github.com`, and to Let's
  Encrypt for certificates.
- Two DNS names ([DNS](#dns)).
- A GitHub OAuth app.
- About 64 KiB of copy buffers per open relayed connection, plus socket
  buffers. Relaying is byte copying, so egress bandwidth runs out first
  ([egress budget](#egress-budget)).

### GitHub OAuth app

Name the app **Aether**, not after the edge: GitHub's authorization page
then stays accurate if other Aether services use the same sign-in. The
edge asks only for the account's identity and its verified primary email.
The callback URL is on the sign-in host and fixed by the code; it must
match exactly, scheme and path included.

In the account or organization that should own it, open Settings >
Developer settings > OAuth Apps > New OAuth App:

| Field | Value |
| --- | --- |
| Application name | `Aether` |
| Homepage URL | `https://<signin-host>` |
| Authorization callback URL | `https://<signin-host>/signin/github/callback` |
| Enable Device Flow | Off: the edge runs its own device flow |

The edge requests the scope `user:email`. Generate a client secret and keep
it for [Installing](#installing).

### DNS

| Name | Type | Value |
| --- | --- | --- |
| `<signin-host>` | A (and AAAA for IPv6) | The edge's address |
| `<relay-host>` | A (and AAAA for IPv6) | The edge's address |
| `<signin-host>`, `<relay-host>` | CAA | `0 issue "letsencrypt.org"` |

- **No wildcard record.** The edge answers only its two names.
- **DNS only, never proxied by a CDN.** A proxying CDN terminates TLS
  itself, so it would see sign-in cookies, device tokens and OAuth codes in
  plain text (SSH stays encrypted inside the relayed stream); it replaces
  the client address the edge limits by with its own, so every client
  would share its few addresses' limits; and its own timeouts would apply
  to the relay's long-lived WebSockets.
- **Leave `accounturi` out of CAA.** A certbot or edge ACME account is
  replaced whenever its directory is lost. CAA records on the two names
  themselves leave other names under the domain to their own CA.

### Behind a reverse proxy

With `--proxy-listen 127.0.0.1:8443` (`AETHER_EDGE_PROXY_LISTEN`), the edge
serves plain HTTP on that address and obtains no certificate. The origins
stay `https://`. Without `--trusted-proxies`, any address that is not
loopback is refused: `--proxy-listen "0.0.0.0:8443" must be a loopback
address such as 127.0.0.1:8080, or name the proxies' networks with
--trusted-proxies`.

The edge takes each client's address from the `X-Forwarded-For` header,
only on a request whose TCP peer is a loopback address. It uses the
right-most entry, the address the proxy accepted the connection from;
entries to its left are whatever the client sent and are ignored. A
request from the proxy without the header, or whose right-most entry is not
a bare IP address, is refused with `400` and logged as `relay: request
from the proxy refused`:

```
the proxy in front of this edge sent no X-Forwarded-For header; configure the proxy with `proxy_set_header X-Forwarded-For $remote_addr`
```

The edge never falls back to the proxy's address, which would put every
client in one address's limits. The limit of 1024 open connections per
address becomes a count of requests in progress per forwarded address, a
relayed connection counting for as long as it is open. A local `curl
http://127.0.0.1:8443/healthz` is refused for want of the header; add
`-H 'Host: <relay-host>' -H 'X-Forwarded-For: 127.0.0.1'`, or probe through
nginx.

[`packaging/nginx/aether-edge.conf.example`](../packaging/nginx/aether-edge.conf.example)
has one `server` block per host name, each passing requests to
`127.0.0.1:8443` over HTTP/1.1:

| Line | Why |
| --- | --- |
| `proxy_set_header Host $host` | The edge picks the origin by the host name |
| `proxy_set_header X-Forwarded-For $remote_addr` | Sets the header to the address nginx accepted the connection from, discarding any value the client sent |
| `Upgrade` and `Connection` headers (relay host) | Servers and clients hold WebSockets there |
| `proxy_read_timeout 7d`, `proxy_send_timeout 7d` (relay host) | A relayed terminal may send nothing for hours; nginx's default of 60 seconds would cut it |
| `proxy_buffering off`; `proxy_request_buffering off` (relay host) | Relayed bytes and API answers pass through as they arrive |

Forwarding the client's header unchanged (`$http_x_forwarded_for`) would
let a client choose its address; the edge cannot tell. Every relayed
connection holds two nginx connections, and every enrolled server one
more pair, so raise `worker_connections` in `/etc/nginx/nginx.conf` above
the 768 Debian and Ubuntu ship with when the edge serves more than a few
hundred of them. `scripts/edge-nginx-test.sh` runs the example file in
front of a real edge ([testing.md](testing.md#behind-nginx)).

#### A proxy that is not on this host

A proxy in another container reaches the edge from that container's
address, not from loopback. Name the proxies with `--trusted-proxies`
(`AETHER_EDGE_TRUSTED_PROXIES`), a comma-separated list of networks such as
`10.0.0.5/32`. `--proxy-listen` may then be any address, such as `:8443`,
and:

- A request from a peer in those networks takes the right-most
  `X-Forwarded-For` entry that is not itself in them, so a chain of proxies
  in the list passes the client's address along.
- A request from any other peer, loopback included, is refused with `403`,
  counted as `not from a trusted proxy` and logged as `relay: request from
  outside the trusted proxies refused`:

  ```
  this edge serves requests only through its reverse proxy, and 198.51.100.7 is not in the edge's --trusted-proxies
  ```

Every peer in the listed networks can set the client address the edge
rate-limits by. List each proxy's own address as a `/32` or `/128`, or a
network that holds only proxies, and let nothing but the proxies reach the
edge's `--proxy-listen` port; in a container, do not publish it.
`0.0.0.0/0` and `::/0` are refused.

A malformed entry to the left of trusted addresses is refused with `400`,
naming its position from the right:

```
X-Forwarded-For entry 2 from the right, "unknown", is not an IP address; the edge read past the entries to its right because they are in --trusted-proxies, so it took 172.18.0.7 for a proxy: list only the proxies' own addresses in --trusted-proxies, and have each proxy append the address it accepted the connection from
```

### Without a reverse proxy

Delete `AETHER_EDGE_PROXY_LISTEN` from the environment file and set
`AETHER_EDGE_ACME_EMAIL`. The edge then obtains a certificate for each of
its two names from `--acme-directory` with TLS-ALPN-01 on `:443`, on the
first handshake for that name, and caches them in `<data>/acme/`. Port 80
stays closed. The packaged unit grants no capability, so give it the one
that binds `:443`:

```sh
sudo systemctl edit aether-edge
```

```ini
[Service]
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
```

The certificate library (`golang.org/x/crypto/acme/autocert`) renews 30
days before expiry and reports a failed renewal nowhere; probe the expiry
from outside ([Monitoring](#monitoring)).

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

Put the GitHub client secret in a root-only file. The command reads the
secret from the terminal, so it lands in no shell history; paste it, then
press Ctrl-D:

```sh
sudo sh -c 'umask 077 && cat > /etc/aether-edge/github-client-secret'
```

[`packaging/systemd/aether-edge.service`](../packaging/systemd/aether-edge.service)
runs the edge as the `aether-edge` user with no capabilities, a read-only
system, no home directories, private `/tmp` and devices, and
`/var/lib/aether-edge` as its only writable path. It passes the secret
file with `LoadCredential=`: systemd reads the file as root and gives the
service a private copy, and the unit points
`AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE` at it. When the file is missing,
`SetCredential=` supplies a single newline instead, which the edge reads as
no secret, and the start fails:

```
aether-edge: --github-client-id is set but /run/credentials/aether-edge.service/github-client-secret, named by --github-client-secret-file, holds no secret
```

The unit restarts the edge 5 seconds after any exit, allows 1048576 open
files, and gives a stop 30 seconds.

### In a container

The release publishes the image `ghcr.io/3xdevops/aether-edge` for
linux/amd64 and linux/arm64: the `aether-edge` binary and CA certificates on
a distroless base, with no shell. It runs `aether-edge serve` as uid and gid
65532 and carries no configuration.

Each release tags it with the release tag, the full commit SHA,
`sha-<short-sha>` and `latest`. Pin a release tag, or better its digest,
which no later push can move; `docker pull
ghcr.io/3xdevops/aether-edge:<release-tag>` prints it as `Digest:`.
`latest` moves with every release, so an unattended restart can upgrade the
database schema under you ([Upgrades](#upgrades)); do not run it in
production.

```
ghcr.io/3xdevops/aether-edge:<release-tag>@sha256:<digest>
```

| What | How |
| --- | --- |
| Configuration | Environment variables. The [Configuration](#configuration) table lists every one the binary reads. Leave `AETHER_EDGE_DATA` unset |
| Data directory | `/var/lib/aether-edge`, declared as a volume ([What the data directory holds](#what-the-data-directory-holds)) |
| Client secret | A file, named by `AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE` ([The client secret](#the-client-secret)) |
| Listening | Behind a reverse proxy on a container network, or with the edge's own certificates on a published port ([Listening](#listening)) |
| Health | The image's `HEALTHCHECK` runs `aether-edge healthcheck` every 30 seconds, after a 30-second start period, and marks the container unhealthy after 3 failures. On a runtime that ignores `HEALTHCHECK`, use the same command as the probe. It reads `AETHER_EDGE_METRICS_LISTEN`, so change the metrics address with that variable, not with a `--metrics-listen` flag on `serve` |
| Stop | `SIGTERM`: the edge sends `drain` to every server and exits 0 within 10 seconds, or exits 1 if shutdown takes longer. Docker's default grace period is also 10 seconds, so give it more: `--stop-timeout 30` |
| Metrics | Loopback only, inside the container: scrape them from a container that shares its network namespace (`--network container:aether-edge`) ([Monitoring](#monitoring)) |
| Root filesystem | May be read-only (`--read-only`): the edge writes only to the data directory, and SQLite keeps its temporary tables in memory |
| Operator commands | `docker exec <container> aether-edge servers list`, as the image's user ([Operator commands](#operator-commands)) |

#### What the data directory holds

`/var/lib/aether-edge` holds `edge.db`, the database of accounts, device
tokens, owners and owed deletions; `edge_key`, the edge signing key every
enrolled server pins; and, when the edge obtains its own certificates, the
ACME cache in `acme/` ([Backup and recovery](#backup-and-recovery)). It
must outlive every container. Mount a named volume, which takes the image's
ownership; a directory owned by uid 65532 (`chown -R 65532:65532 <dir> &&
chmod 0700 <dir>`); or a directory the edge's uid writes through its group,
such as `root:<gid>` mode `2770` on a platform that runs the container with
that supplementary group. The edge refuses a directory its uid cannot
write, one that others can enter, and an `edge_key` or `edge.db` another
uid owns, since it creates both 0600:

```
aether-edge: data directory /var/lib/aether-edge (uid 0, gid 0, mode 0755) is not writable by uid 65532, the user aether-edge runs as: permission denied; give that user write access as the directory's owner or through its group, or name another with --data or AETHER_EDGE_DATA
aether-edge: data directory /var/lib/aether-edge has mode 0775, which lets every user on this machine into the directory of the edge's signing key; run chmod o-rwx /var/lib/aether-edge
aether-edge: /var/lib/aether-edge/edge_key belongs to uid 1000 and aether-edge runs as uid 65532; the edge opens only files it created, so run chown -R 65532 /var/lib/aether-edge
```

A replacement container without the directory makes a new signing key and
an empty database. Every enrolled server then refuses the edge with `edge
key changed` until its operator runs `sudo aether-server edge trust`, and
has no owner there until an admin claims it again with a code from `sudo
aether-server edge claim-code`; every client runs `aether login` again.

A copy of the directory is a secret. `edge_key` signs the grants servers
accept, so whoever holds it can act as the edge
([What an attacker can do](#what-an-attacker-can-do)). Encrypt backups and
store them where only the edge's operators can read them.

The image has no `sqlite3`: back up by copying the volume while the
container is stopped, or run `sqlite3 edge.db ".backup <file>"` as uid 65532
from another container that mounts the volume. The fingerprint servers pin
is in `GET /v1/edge` ([First start and checks](#first-start-and-checks)).
To upgrade, back up, then start the new tag's container with the same
volume.

#### The client secret

Put the GitHub client secret in a file mounted read-only from your
platform's secret store, such as a Compose, Swarm or Kubernetes secret, and
name it with `AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE`; a trailing newline is
ignored. With plain `docker run`, mount a file only uid 65532 can read
(`chown 65532 <secret-file> && chmod 0400 <secret-file>`); anything else is
refused with its reason:

```
aether-edge: --github-client-secret-file: open /run/secrets/github-client-secret: permission denied; make it readable by uid 65532, the user aether-edge runs as
```

Never put the secret in an image layer, a build argument or a committed
file. `AETHER_EDGE_GITHUB_CLIENT_SECRET` works too, but `docker inspect`
shows it.

#### Listening

Behind a reverse proxy in another container, put both on one container
network, publish only the proxy's ports, and trust the proxy's own address
on that network, not the network's subnet: every container on a trusted
network could set the client address
([A proxy that is not on this host](#a-proxy-that-is-not-on-this-host)).
Give the proxy a fixed address on that network, or use a network that holds
only the proxy and the edge. The proxy passes both host names to
`http://aether-edge:8443` with the headers and timeouts of
[Behind a reverse proxy](#behind-a-reverse-proxy):

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

With its own certificates, a non-root user cannot bind port 443 on every
runtime. Replace the proxy variables with a high port published as 443:

```sh
  -e AETHER_EDGE_LISTEN=:8443 -e AETHER_EDGE_ACME_EMAIL=<acme-contact-email> -p 443:8443
```

The TLS-ALPN-01 challenge arrives on 443 and reaches `:8443`, which
answers it. The edge limits requests by client address, so the published
port must keep the client's source address; a userland port forwarder, as
some rootless runtimes use, replaces it with its own, and every client then
shares one address's limits.

### Configuration

Every option is a flag whose default comes from the environment variable
beside it. The unit starts `aether-edge serve` with no flags, so the
environment file
([`packaging/edge/aether-edge.env.example`](../packaging/edge/aether-edge.env.example))
is the whole configuration. Replace every `<...>` in it.

| Flag | Environment | Default |
| --- | --- | --- |
| `--signin-origin` | `AETHER_EDGE_SIGNIN_ORIGIN` | required: `https://<signin-host>` |
| `--relay-origin` | `AETHER_EDGE_RELAY_ORIGIN` | required: `https://<relay-host>`, another host than the sign-in origin's |
| `--proxy-listen` | `AETHER_EDGE_PROXY_LISTEN` | none; `127.0.0.1:8443` in the environment file |
| `--trusted-proxies` | `AETHER_EDGE_TRUSTED_PROXIES` | none: the proxy is on loopback ([A proxy that is not on this host](#a-proxy-that-is-not-on-this-host)) |
| `--listen` | `AETHER_EDGE_LISTEN` | `:443`, with the edge's own certificates; unused with `--proxy-listen` |
| `--data` | `AETHER_EDGE_DATA` | `/var/lib/aether-edge` |
| `--metrics-listen` | `AETHER_EDGE_METRICS_LISTEN` | `127.0.0.1:9464`, loopback only; serves `/metrics` and `/healthz` |
| `--acme-email` | `AETHER_EDGE_ACME_EMAIL` | none |
| `--acme-directory` | `AETHER_EDGE_ACME_DIRECTORY` | `https://acme-v02.api.letsencrypt.org/directory` |
| `--egress-budget` | `AETHER_EDGE_EGRESS_BUDGET` | `0`: bytes per UTC month, a plain integer; 0 = none |
| `--github-client-id` | `AETHER_EDGE_GITHUB_CLIENT_ID` | required |
| `--github-client-secret-file` | `AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE` | none; set by the unit |
| `--dev-listen` | `AETHER_EDGE_DEV_LISTEN` | none |
| none | `AETHER_EDGE_GITHUB_CLIENT_SECRET` | none; the secret itself, instead of a file |

The client secret is never a flag, because a flag's value shows in the
process list. It comes from the file `--github-client-secret-file` names,
or from `AETHER_EDGE_GITHUB_CLIENT_SECRET`; setting both is refused. These
are all the variables the edge reads, apart from Go's standard ones, such
as `HTTPS_PROXY` and `NO_PROXY` for its requests to GitHub and Let's
Encrypt.

The edge checks its options before it binds anything. Among the errors:

```
aether-edge: edge: no GitHub OAuth app is configured; set --github-client-id and its client secret
aether-edge: --github-client-id is set but its secret is not; name a file holding it with AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE (--github-client-secret-file), or set AETHER_EDGE_GITHUB_CLIENT_SECRET
aether-edge: --github-client-secret-file: open /run/secrets/github-client-secret: no such file or directory; put the secret there, or name the file that holds it
aether-edge: data directory /var/lib/aether-edge (uid 0, gid 0, mode 0755) is not writable by uid 65532, the user aether-edge runs as: permission denied; give that user write access as the directory's owner or through its group, or name another with --data or AETHER_EDGE_DATA
aether-edge: --egress-budget "10G" is not a byte count
aether-edge: --relay-origin must be https://host[:port], not "edge.example.com"; for a local plain-HTTP edge use --dev-listen
aether-edge: --signin-origin https://edge.example.com and --relay-origin https://edge.example.com must name different hosts, such as auth.example.com and edge.example.com
aether-edge: --dev-listen and --proxy-listen are two ways to serve plain HTTP; set one
```

`--dev-listen 127.0.0.1:8080 --signin-origin http://localhost:8080
--relay-origin http://127.0.0.1:8080` serves plain HTTP on a loopback
address for local testing, with no certificates; the two origins are two
loopback names of that address.

### Firewall

Inbound, allow TCP 443 on IPv4 and IPv6, TCP 80 for certbot's HTTP-01
challenge behind nginx, and your own administration access; refuse
everything else. The metrics listener is loopback only, and so is
`--proxy-listen` without `--trusted-proxies`. With `ufw`:

```sh
sudo ufw default deny incoming
sudo ufw allow from <admin-address> to any port 22 proto tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
```

Without a reverse proxy, leave port 80 closed.

### First start and checks

```sh
sudo systemd-analyze verify /etc/systemd/system/aether-edge.service   # prints nothing when the unit is valid
sudo systemctl daemon-reload
sudo systemctl enable --now aether-edge
journalctl -u aether-edge -f
```

Behind a proxy the start prints:

```
INFO aether-edge: behind a reverse proxy: plain HTTP, client addresses from X-Forwarded-For listen=127.0.0.1:8443 proxies=loopback
INFO aether-edge: serving signin=https://<signin-host> relay=https://<relay-host> metrics=127.0.0.1:9464
```

Then check, from another machine:

1. `curl -fsS https://<signin-host>/healthz` and `curl -fsS
   https://<relay-host>/healthz` each print `ok`.
2. `curl -sS https://<relay-host>/signin` prints
   `{"error":"/signin is served on https://<signin-host>, not on this host"}`.
3. `curl -sS -H 'Aether-Edge-Version: 1' https://<relay-host>/v1/edge`
   prints `{"signin_origin":"https://<signin-host>","key":"...","fingerprint":"SHA256:...","version":1,"min_version":1}`.
   On the edge, `sudo ssh-keygen -lf /var/lib/aether-edge/edge_key` prints
   the same fingerprint. That is the key servers pin: publish it where
   server operators compare it during `aether-server edge trust`, and back
   the key up now ([Backup and recovery](#backup-and-recovery)).
4. `curl -sS -o /dev/null -D - https://<signin-host>/signin/github` answers
   `302` with a `Location` on `github.com` whose `redirect_uri` is
   `https%3A%2F%2F<signin-host>%2Fsignin%2Fgithub%2Fcallback`, and
   `Set-Cookie: __Host-aether_edge_signin=...; Path=/; Max-Age=600; HttpOnly; Secure; SameSite=Lax`.
5. Open `https://<signin-host>/signin` and sign in with GitHub; the
   Servers page opens. On a laptop, `aether login --edge
   https://<relay-host>`, confirm the code, then `aether servers --edge
   https://<relay-host>`.
6. On a separate server machine:

   ```sh
   sudo aether-server install --edge-url https://<relay-host> --edge-access approved-devices
   sudo systemctl daemon-reload && sudo systemctl enable --now aether-server
   sudo aether-server edge status        # edge key matches step 3; connection: connected since ...
   sudo aether-server edge claim-code
   ```

   On the laptop, `aether link --claim <code> --edge https://<relay-host>`,
   then `aether runs`. Keep this server running as a canary.
7. On the edge, `curl -sS http://127.0.0.1:9464/metrics | grep refusals`
   shows no `no forwarded client address` refusals.

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
| `aether_edge_refusals_total{reason="..."}` | Refused connections and enrollments by reason, such as `enrollment refused`, `upgrade required`, `too many connections from one address` or `no forwarded client address` |

Scrape it from the edge host itself or over an SSH tunnel; it is never
exposed. `aether-edge healthcheck` asks the edge on the same machine for
`/healthz` on that listener, reading `AETHER_EDGE_METRICS_LISTEN` or
`--metrics-listen` as `serve` does, and exits 0, or 1 with the reason:

```
aether-edge: healthcheck: no edge answers on 127.0.0.1:9464: Get "http://127.0.0.1:9464/healthz": dial tcp 127.0.0.1:9464: connect: connection refused; when serve was given --metrics-listen, give this command the same address or set AETHER_EDGE_METRICS_LISTEN for both
```

Alert on `aether_edge_throttled` equal to 1 and on
`aether_edge_egress_month_bytes` passing a fraction of the budget; on a fall
in `aether_edge_servers{state="claimed"}` to near zero, which means servers
cannot enroll; and on a sustained rise in one refusal reason. Probe from
outside the edge's network, so DNS and routing are tested too:

```sh
for host in <signin-host> <relay-host>; do
  curl -fsS "https://$host/healthz"
  # fails when fewer than 21 days remain, a week after renewal should have happened
  echo | openssl s_client -connect "$host:443" -servername "$host" 2>/dev/null |
    openssl x509 -noout -checkend 1814400 || echo "certificate for $host expires within 21 days"
done
```

### Logs

The edge logs to standard error, which the unit sends to the journal:
`journalctl -u aether-edge`. Entries carry server ids and, for refused
enrollments (`relay: enrollment refused client=<address>`) and failed TLS
handshakes without a proxy, client addresses. A request that fails on the
edge's side or at GitHub logs `edge: request failed route=<route>
status=<5xx> error=<error>`; refusals the client can fix are not logged. A
refused claim or ownerless report logs `relay: control channel closed server=<id>
error="ownership report refused: <reason>"`. nginx keeps its own access log
of client addresses in `/var/log/nginx/access.log`.

Client addresses are personal data. journald keeps entries until its size
limits push them out, which can be months on a quiet host, so set a
retention period and state it in your privacy notice:

```sh
sudo install -d /etc/systemd/journald.conf.d
printf '[Journal]\nMaxRetentionSec=<retention such as 14day>\n' |
  sudo tee /etc/systemd/journald.conf.d/retention.conf
sudo systemctl restart systemd-journald
```

That setting applies to the host's whole journal; nginx's logs follow
`/etc/logrotate.d/nginx`. What the database keeps is in
[privacy.md](privacy.md#what-an-edge-stores).

### Backup and recovery

The data directory, `/var/lib/aether-edge`:

| Path | Holds |
| --- | --- |
| `edge.db` (with `edge.db-wal`, `edge.db-shm`) | Accounts, edge sessions, devices, claimed servers with owners, kinds, announced policies and directories, which servers each account was admitted to, deletions owed to servers, egress counters, blocked server ids and accounts. Bearer secrets are stored only as SHA-256 hashes |
| `edge_key` | The Ed25519 key grants are signed with, OpenSSH format, 0600. The edge refuses to start if group or others can read it: `edge: signing key <path> has mode 0644; run chmod 600 <path>` |
| `acme/` | Without a proxy: the two names' certificates, their keys and the ACME account key |

Keep:

- **`edge_key`, once, offline**, right after the first start. It never
  changes. If it is lost, the edge makes a new one and every server refuses
  the edge with `edge key changed` until its operator runs `sudo
  aether-server edge trust`. Clients need no action.
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
  older backup brings back device tokens revoked since it was taken; a
  device revoked on a server stays revoked there.
- **`/etc/aether-edge/`** (the environment file and secret files) and, behind
  nginx, `/etc/letsencrypt/` and `/etc/nginx/conf.d/aether-edge.conf`, or
  the steps to recreate them.

### Upgrades

```sh
sudo -u aether-edge sqlite3 /var/lib/aether-edge/edge.db ".backup /var/lib/aether-edge/edge-pre-<release-tag>.db"
# download and check the new binary as in Installing, then:
sudo install -m 0755 aether-edge-linux-<arch> /usr/local/bin/aether-edge
sudo systemctl restart aether-edge
```

Fetch the unit and the environment example of the new release too, and
compare them with yours. On `SIGTERM` the edge sends `drain` to every
server, then closes within 10 seconds. Every relayed connection drops;
servers reconnect with backoff, clients redial, and runs are unaffected.
The database schema migrates forward on start. An older binary refuses a
migrated database with `edgestore: database schema version <n> is newer
than this binary supports (<m>)`; to roll back, restore the backup taken
before the upgrade.

An edge, servers and clients built from the `v0.5.2-alpha.3` tag must all
be replaced by this release together; a mix of that build and this one
does not interoperate.

Every handshake carries a protocol version. The edge accepts any server or
client at or above its minimum version, including newer ones, which speak
down. An older one is refused with `upgrade required: <minimum>` (HTTP 426
for clients). Protocol version 1 is the only version and the minimum.

### Operator commands

Run them as the edge's user. They refuse any other user than the owner of
`edge.db`, because SQLite creates its `-wal` and `-shm` files as whoever
opens the database, and a root-owned one would lock the edge out. `--data`
(default `AETHER_EDGE_DATA`, else `/var/lib/aether-edge`) goes before the
argument.

The commands list, remove, block and delete. None grants anything: no
command approves a device, records an owner or admits an account to a
server. `unblock` lifts only a block the operator set.

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

An account is `github:<user id>`, as the `ACCOUNT` column of `accounts
list` prints it.

Google sign-in existed only in builds from the `v0.5.2-alpha.3` tag, which
never had a published binary or image. An edge built from that tag may
list accounts as `google:<subject>`. They cannot sign in: their sessions and device tokens
are refused with `sign-in provider "google" is not supported: Aether signs
in with GitHub only; this edge's operator removes the account with:
aether-edge accounts delete google:<subject>`. `accounts delete` and
`accounts unblock` take such an account; `accounts block` does not. No
server is sent its deletion, because none accepts one: each server's admin
removes the identity with `aether member unlink <member-id>
google:<subject>` ([teams.md](teams.md#a-members-edge-accounts)).

| Command | Effect |
| --- | --- |
| `servers remove` | Forgets the server's claim and directory. New connections are refused. The server learns it is unclaimed when it next connects; its administrator claims it again with a new code |
| `servers block` | Does what `remove` does, and refuses the id at enrollment with `server is blocked by this edge's operator` |
| `servers unblock` | The id can enroll and be claimed again |
| `accounts block` | Deletes the account's edge sessions, device tokens and pending sign-ins, and refuses its sign-ins with `account is blocked by this edge's operator`. Servers it owns stay claimed |
| `accounts unblock` | The account can sign in again; its devices sign in anew |
| `accounts delete` | What the person's own deletion does, except that it closes no live connection and each server receives the deletion at its next enrollment. It prints every server owed the deletion. A block stays |

Each change is one SQLite transaction, and the edge reads blocks, claims
and owed deletions from the database at every enrollment, sign-in and
connection. Connections already open stay up until they end or the edge
restarts; `sudo systemctl restart aether-edge` ends them, and each server
reconnects and receives the deletions it is owed.

A server id is derived from a host key anyone can generate, so blocking an
id stops that one server; likewise a blocked person can sign in with
another GitHub account. Anyone who signs in can claim a server
and relay SSH to it; when a server or an account misuses the relay, block
it.

### Egress budget

`--egress-budget` counts the bytes the relay copies, in both directions,
per UTC calendar month. It does not count TLS and WebSocket framing, the
edge's pages and API, or certificate traffic, so a provider meters more
than `aether_edge_egress_month_bytes` shows. Once the month's count reaches
the budget, every relayed connection together is paced to 256 KiB/s until
the next month; nothing is cut off. Servers take turns one read of at most
4 KiB at a time. At that rate a 31-day month adds at most about 650 GiB
after the budget is spent, and a crash loses up to one minute of counting.
For a provider ceiling of C bytes a month, set the budget below C minus
700 GiB (751619276800 bytes), with margin for framing. The count survives
restarts.

## Limits

| Limit | Value |
| --- | --- |
| Concurrent SSH connections per server | 48 |
| Connections per device | 16 |
| Unclaimed servers per client address | 3, each dropped after 30 minutes; for IPv6, 3 per /64, 12 per /56 and 48 per /48 |
| Unclaimed servers per edge | 10000 |
| Open connections per client address (IPv6 per /64) | 1024; behind a proxy, requests in progress per forwarded address |
| Server attaching a connection | 10 seconds, then `server did not attach` |
| Sign-in, device codes and claim connections | Rate-limited per address; an IPv6 address counts against its /64, its /56 with 4 times the budget and its /48 with 16 times. Device-code entry is also limited per account, token polling per address. Claim connections: 5 at once then 1 a minute per address and per account, 10 at once then 1 every 30 seconds per server |
| Ownership report after a claim connection opened | 60 seconds |
| Account deletions owed to one offline server | 1000; past that the oldest is dropped |
| Sign-in before an account deletion | 5 minutes |
| Directory stores per server | 1 per 5 seconds; only the latest waiting push is stored |
| Members and open invitations per server directory | 1000 |
| Login and email matching invitations | 24 hours after the account's last GitHub sign-in |

No idle timeout applies to a relayed stream: an idle terminal is
legitimate.

## Failure modes

| Failure | Behaviour |
| --- | --- |
| Edge down | [During an edge outage](#during-an-edge-outage) |
| Edge restart | Relayed connections drop; servers and clients reconnect; runs are unaffected |
| Server offline | `503 server is not connected to the edge` |
| Claim or ownerless report refused | The edge closes the control connection with `ownership report refused: <reason>`; the server reconnects and learns whether the edge holds it as claimed |
| Transfer refused | The edge answers with the reason; `aether member transfer` fails with `ownership report refused: <reason>` and the server keeps its previous owner |
| GitHub sign-in down | No new sign-ins. Device tokens and edge sessions keep working, but invitations stop matching accounts whose last sign-in is over 24 hours old |
| Host key lost or rotated | New server id: claim again and link again |
| Edge signing key lost | Every server refuses the edge until `aether-server edge trust` |

## Owner checklist: auth.onaether.dev and edge.onaether.dev

The project's own edge, behind nginx on Debian or Ubuntu, in order. Every
value you supply is a placeholder:

| Placeholder | Value |
| --- | --- |
| `<vps-ipv4>`, `<vps-ipv6>` | The host's public addresses |
| `<arch>` | `amd64` or `arm64` |
| `<release-tag>` | The release to install |
| `<admin-address>` | The address you administer the host from |
| `<certbot-email>` | Contact for the Let's Encrypt account |
| `<github-owner>` | The GitHub account or organization that owns the OAuth app |
| `<github-client-id>` | From step 4; the secret goes only into a file in step 8 |
| `<egress-budget-bytes>` | Below the provider's monthly egress ceiling ([Egress budget](#egress-budget)) |
| `<worker-connections>` | nginx connections per worker, at least twice the relayed connections and servers you expect |
| `<retention>` | Log retention, such as `14day` |

Steps marked **tested** are exercised in this repository; the rest can
only be verified on the live host.

1. **Host.** Check `systemctl --version` prints 248 or newer. Install the
   tools:

   ```sh
   sudo apt-get update
   sudo apt-get install nginx certbot python3-certbot-nginx sqlite3 curl
   ```

2. **Firewall** ([Firewall](#firewall)): 22 from `<admin-address>`, 80 and
   443 from anywhere. On a host that already serves other sites, check the
   rules it has with `sudo ufw status` and add only what is missing:
   `ufw default deny incoming` cuts off every service the rules do not
   name.
3. **Cloudflare DNS** for `onaether.dev`. Proxy status **DNS only** on
   every record ([DNS](#dns) says why):

   | Type | Name | Content |
   | --- | --- | --- |
   | A | `auth` | `<vps-ipv4>` |
   | AAAA | `auth` | `<vps-ipv6>` |
   | A | `edge` | `<vps-ipv4>` |
   | AAAA | `edge` | `<vps-ipv6>` |
   | CAA | `auth` | flags `0`, tag `issue`, value `letsencrypt.org` |
   | CAA | `edge` | flags `0`, tag `issue`, value `letsencrypt.org` |

   Check: `dig +short auth.onaether.dev A` and `dig +short
   edge.onaether.dev A` print `<vps-ipv4>`, not a Cloudflare address;
   `dig +short auth.onaether.dev CAA` prints `0 issue "letsencrypt.org"`.
4. **GitHub OAuth app** in `<github-owner>`: name `Aether`, homepage
   `https://auth.onaether.dev`, callback
   `https://auth.onaether.dev/signin/github/callback`, Enable Device Flow
   off. Generate a client secret; do not store it anywhere but step 8.
5. **Certificates**, one per name, reloading nginx after each renewal:

   ```sh
   sudo certbot certonly --nginx --email <certbot-email> --agree-tos -d auth.onaether.dev --deploy-hook "systemctl reload nginx"
   sudo certbot certonly --nginx --email <certbot-email> --agree-tos -d edge.onaether.dev --deploy-hook "systemctl reload nginx"
   sudo certbot renew --dry-run
   ```

6. **nginx.** Install the server blocks, then check and reload
   (**tested**: `scripts/edge-nginx-test.sh` runs this file with real
   nginx, `nginx -t`, WebSockets idle for 75 seconds and a forged
   `X-Forwarded-For`, on the names `localhost` and `127.0.0.1` with a
   self-signed certificate):

   ```sh
   src=https://raw.githubusercontent.com/3xDevOps/Aether/<release-tag>/packaging
   curl -fsSL "$src/nginx/aether-edge.conf.example" |
     sed -e 's/<signin-host>/auth.onaether.dev/g' -e 's/<relay-host>/edge.onaether.dev/g' |
     sudo tee /etc/nginx/conf.d/aether-edge.conf >/dev/null
   sudo nginx -t
   sudo systemctl reload nginx
   ```

   The edge listens on `127.0.0.1:8443`. Check nothing else on the host
   does, with `sudo ss -ltnp 'sport = :8443'`, which prints only its header
   line when the port is free. For another port, change `proxy_pass` in
   both `server` blocks and `AETHER_EDGE_PROXY_LISTEN` in step 7. The file
   adds two `server` blocks and one `map` whose variable,
   `$aether_edge_connection`, no other site's configuration uses; it
   changes no existing site.

   `nginx -t` prints `syntax is ok` and `test is successful`. Set
   `worker_connections <worker-connections>;` in the `events` block of
   `/etc/nginx/nginx.conf`, run `nginx -t` again, and reload.
7. **Binary, unit, user and environment file**, as in
   [Installing](#installing). In `/etc/aether-edge/aether-edge.env`:

   ```sh
   AETHER_EDGE_SIGNIN_ORIGIN=https://auth.onaether.dev
   AETHER_EDGE_RELAY_ORIGIN=https://edge.onaether.dev
   AETHER_EDGE_PROXY_LISTEN=127.0.0.1:8443
   AETHER_EDGE_EGRESS_BUDGET=<egress-budget-bytes>
   AETHER_EDGE_GITHUB_CLIENT_ID=<github-client-id>
   ```

8. **Secret** into `/etc/aether-edge/github-client-secret`, as in
   [Installing](#installing). The unit hands it to the service through
   systemd credentials (live host only).
9. **First start** ([First start and checks](#first-start-and-checks)):
   `systemd-analyze verify`, then `enable --now`, then checks 1 to 7, with
   a canary server claimed by a project account.
10. **Signing key.** Record the fingerprint from `sudo ssh-keygen -lf
    /var/lib/aether-edge/edge_key`, publish it on this page, and store
    `edge_key` offline.
11. **Backups.** Schedule the daily `edge.db` backup and copy it off the
    machine ([Backup and recovery](#backup-and-recovery)).
12. **Log retention** for the journal (`<retention>`) and nginx's access
    log ([Logs](#logs)).
13. **Monitoring**: the external probes and metric alerts in
    [Monitoring](#monitoring).
14. **Privacy.** Put the retention period in
    [privacy.md](privacy.md#what-an-edge-stores); privacy requests go to
    `team@onaether.dev`.
15. **Upgrades** follow [Upgrades](#upgrades).
