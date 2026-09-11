# Networking and identity

Aether needs exactly one thing from your network: **the CLI must be able to
reach the server's SSH port.** Git transport, the control channel, event
streams, PTY attach and the dashboard forward all multiplex over that one
connection. The server opens no HTTP port unless you set `web-port`, and that
one listens on the host's tailnet addresses only - see
[The dashboard](#the-dashboard).

How you make that port reachable is up to you. Tailscale is the recommended
answer, and it is also the recommended identity layer, because it removes SSH
key management entirely.

---

## Tailscale: the keyless path

Put the server on your tailnet and joining is three words long:

```sh
aether link my-server
```

No key generation. No `authorized_keys`. No invite code. Nothing to copy
between machines.

### Setting it up

On the server box:

```sh
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up
tailscale status --self
```

`tailscale status --self` prints the machine's tailnet hostname
(`my-server.tailnet-name.ts.net`); that is what you hand teammates. Then
install the server as usual (`sudo aether-server setup`; see
[install.md](install.md#first-boot)) - it detects the tailscaled socket and
reports the tailnet hostname itself.

The server checks for the tailscaled socket **at startup**. If it is there,
tailnet identity is on; if not, the server is key-only. Start Tailscale before
the server, and restart the server if you add Tailscale later.

On your machine, join the same tailnet and link:

```sh
sudo tailscale up
aether link my-server
```

A bare hostname picks up the default port `:2222`, so the MagicDNS name is the
whole address. On first contact the CLI records the server's host key in
`~/.ssh/known_hosts` and prints its fingerprint - which is also why plain
`git push aether main` works afterwards with no further setup.

### How it works

When a connection arrives, the server asks the local Tailscale daemon who is on
the other end - a WhoIs lookup against the connection's source address, the
same mechanism Tailscale SSH uses. Tailscale already authenticated that person
against your identity provider, so Aether reuses the answer as the member
identity. The tailnet login becomes the member; the part before the `@` becomes
the default display name.

Aether does **not** take over port 22 and does not use Tailscale SSH itself
(which targets OS accounts on the host). It keeps its own embedded SSH server
and borrows only the identity mechanism.

### Joining and approval

- **First contact creates the admin.** The first tailnet identity to link a
  fresh server is registered as an admin, not pending. Solo developers never
  see a join step.
- **Everyone after that joins pending.** They are registered as collaborators
  with `Pending` set, and can authenticate but not act until an admin runs
  `aether member approve <member-id>`. Until then commands fail with
  `membership pending admin approval`. That is the role they *join* with; an
  admin can change it afterwards with `aether member role` (see
  [teams.md](teams.md)).
- **`--tailnet-auto-join`** removes the approval step, for teams whose tailnet
  boundary already is the team boundary.
- **Revocation follows the tailnet.** Remove someone from the tailnet, or deny
  them the server in your Tailscale ACLs, and they are locked out of Aether.
  `aether member remove <member-id>` is the in-Aether equivalent.

Details of the team flow are in [teams.md](teams.md).

### The trust boundary

**WhoIs names the device's owner, not the person at the keyboard.** Any process
or OS user on that machine can open a connection attributed to them. This is
the same boundary Tailscale SSH has, and it is fine for single-user laptops -
the common case.

Two guards for when it is not:

- **Tagged nodes get no identity.** CI runners and shared boxes should be
  tagged. A tagged node is refused tailnet identity outright
  (`tagged tailnet node; key authentication required`) and must use a key.
- **`--tailnet-require-key`** demands a registered SSH key *in addition to*
  WhoIs on every tailnet connection. Use it when you cannot guarantee
  single-user devices.

### When tailscaled is not there

Every failure falls back to key authentication rather than locking anyone out:

| Message on the client | What happened |
| --- | --- |
| `tailnet identity unavailable; key authentication required` | The lookup failed - the connection did not arrive over the tailnet, or tailscaled is down. Informational; the key path then runs normally. |
| `tagged tailnet node; key authentication required` | The connecting node is tagged. |
| `tailnet login <x> must also present a registered SSH key` | `--tailnet-require-key` is set. |

You will see the first message routinely on a tailnet-enabled server whenever
something connects over loopback or the LAN. It is not an error.

Members who have **only** a tailnet identity and no registered key cannot open
new connections while tailscaled is down. Runs already going and PTYs already
attached are unaffected.

The server's user must be able to read `/var/run/tailscale/tailscaled.sock`.
Running as root (the shipped systemd unit) always can; for an unprivileged
server user, `sudo tailscale set --operator=<user>`.

---

## Plain LAN, VPN, or a cloud box

No tailnet? Everything still works; you manage identity with SSH keys and
invite codes instead.

### The admin

```sh
ssh-keygen -t ed25519          # if you do not already have a key
aether link 192.168.1.50:2222
```

The first key to link a fresh server is registered as the admin. The CLI uses
`~/.ssh/id_ed25519` by default and also offers any key loaded in your ssh-agent.
For a key at another path:

```sh
aether link 192.168.1.50:2222 --key ~/.ssh/aether_ed25519
```

`link` saves that path in `~/.config/aether/config.json`, and re-linking
without `--key` keeps it. Every command that dials the server's control
connection - `aether runs`, `attach`, `workspace`, `gui`, and the daemon the
dashboard installs - reads it from there. Two paths do not:

- git. `aether pull` and `git push aether` shell out to the system `ssh`
  client, which follows `~/.ssh/config`. Point it at the same key with an
  `IdentityFile` line for the server host.
- `aether daemon run` and `aether daemon install` on the command line, which
  take their own `--key` and otherwise default to `~/.ssh/id_ed25519`.

A `--key` path that does not exist fails before the dial, rather than falling
back to the agent. The CLI never prompts for a passphrase: to use a
passphrase-protected key, `ssh-add` it first. Without a usable key the
handshake fails with `attempted methods [none]`, followed by the reason the
key it found was rejected - unreadable, unparseable, or passphrase-protected.

### Everyone else

Unknown keys are refused (`no Aether member for this key`). An admin mints a
one-time code:

```sh
aether invite --ttl 3600
```

```
<invite-code>
expires <expiry-time>
```

The teammate redeems it once, which registers their key as a collaborator and
burns the code:

```sh
aether link <server-host> --invite <invite-code> --name "Example"
```

```
linked to <server-host> as Example (collaborator)
```

Invites default to a 24-hour TTL (`--ttl` is in seconds). Nobody needs shell
access to the server box to join.

### Exposure

Only the SSH port has to be reachable. Behind a VPN or on a private LAN, bind
it normally. On a public cloud box, put the port behind a firewall or a VPN
rather than opening it to the internet - Aether is an SSH server with a
container runtime behind it.

Both identity paths share one member table, so a tailnet server can still hand
out invites to someone connecting from outside the tailnet.

---

## The dashboard

There are two ways to reach the dashboard, and they are independent of each
other.

**From your own machine.** `aether gui` serves it from your laptop, bound to
`127.0.0.1`, over the same SSH connection the CLI uses. Nothing on the server
listens for it, and nothing about it changes the server's network shape. See
[local-gateway.md](local-gateway.md).

**From the server, for phones.** Set `web-port` and the server hosts the
dashboard itself, over HTTPS, on every tailnet address of the host (IPv4 and
IPv6):

```sh
sudo aether-server config set web-port 443
sudo systemctl restart aether-server
```

Any device already on the tailnet - a phone, a tablet, a borrowed laptop -
then opens the server's MagicDNS name and is already signed in:

```
https://my-server.tailnet-name.ts.net/
```

No token, no install, no `aether gui` anywhere. Port 443 gives that bare URL;
any other port appends `:<port>`. The startup line names the URL it bound:

```
aether-server <version> serving SSH on :2222 and the dashboard on https://my-server.tailnet-name.ts.net/ (data dir /var/lib/aether)
```

`web-port` defaults to `0`, which leaves the server SSH-only. `aether-server
setup` asks for it on a tailnet host; `aether-server install --web-port 443`
and `aether-server config set web-port 443` set it without questions
([install.md](install.md#first-boot)).

### What it needs

- **tailscaled on the server host**, running before the server starts. It is
  the same daemon and the same unix socket
  (`/var/run/tailscale/tailscaled.sock`) the WhoIs identity path uses.
- **MagicDNS and HTTPS certificates enabled for the tailnet**, in the
  Tailscale admin console on the DNS page. The certificate and key for the
  node's MagicDNS name come from tailscaled, which issues them through Let's
  Encrypt and renews them; the server re-fetches hourly in the background and
  keeps serving the cached pair if a refresh fails.
- **Permission to fetch that certificate**, which is root or the tailscaled
  operator (`sudo tailscale set --operator=<user>`). The shipped systemd unit
  runs as root.

Plain HTTP is never offered - there is no redirect and no cleartext port. A
server that cannot get the certificate does not start.

### Who you are on it

Every request is identified the same way an SSH connection is: a WhoIs lookup
on the request's source address, resolved afresh per request. There is no
session and no token, so nothing can outlive the tailnet's answer - remove
someone from the tailnet, or deny them the server in your ACLs, and their next
request is refused.

That means the joining, approval and capability rules above apply unchanged.
The first identity to reach a fresh server becomes the admin; later ones join
pending unless `--tailnet-auto-join`, and a pending member's dashboard shows
`membership pending admin approval` on every call until an admin runs
`aether member approve <member-id>`. Every call then passes the same
capability checks a CLI call does.

**The trust boundary is the same one, and it now covers the browser.** WhoIs
names the device's owner, not the person holding the phone. Tagged nodes are
refused here too. `--tailnet-require-key` and `web-port` are mutually
exclusive: HTTP cannot present a key, so a server set to require one refuses
to start with the dashboard on.

Key-only and invite-code servers - anything with no tailscaled - have no
phone dashboard in this release. `aether gui` is the whole story there.

### Where it refuses

The server refuses to **start** rather than serving something it cannot
identify or encrypt. Each message is printed on stderr and lands in
`journalctl -u aether-server`:

| Message | What to do |
| --- | --- |
| `the dashboard identifies members by tailnet WhoIs and tailscaled was not running when the server started (no /var/run/tailscale/tailscaled.sock); start Tailscale before the server, or set web-port to 0` | Start tailscaled first, then the server. |
| `tailnet-require-key demands an SSH key on every tailnet connection and the dashboard cannot present one; set one of tailnet-require-key and web-port off` | Pick one: keyed tailnet connections, or the dashboard. |
| `tailscaled did not report this node: status has no DNS name` | Enable MagicDNS for the tailnet. |
| `servergw: tailscaled reports no tailnet address for <name>` | The node is not up on the tailnet; `tailscale status --self`. |
| `servergw: HTTPS certificate for <name>: <error>; enable MagicDNS and HTTPS certificates for the tailnet in the Tailscale admin console (DNS page), or set web-port to 0` | Enable HTTPS certificates, or run as root or the tailscaled operator. The error tailscaled gave is quoted in place of `<error>`. |
| `servergw: listen: no tailnet address could be bound: <errors>` | The port is taken on every tailnet address, or binding it needs privileges the server does not have. One address that will not bind - the IPv6 one on a host with IPv6 disabled - is only a warning in the journal (`servergw: tailnet address not bound`); the others still serve. |

Once it is serving, a request that cannot be identified is refused per
request, with the JSON error body the dashboard shows:

| Status | Message | What happened |
| --- | --- | --- |
| `403` | `tagged tailnet node; the dashboard identifies members by their tailnet login and a tagged node has none` | The request came from a tagged node. Tagged nodes use a key over SSH; they have no dashboard. |
| `503` | `tailnet identity unavailable: <error>` | The lookup failed - tailscaled is down, or the source address is not on the tailnet. |

### Why it is off by default

The listener has hard prerequisites (tailscaled, MagicDNS, HTTPS
certificates) that an existing install may not have, and a server that failed
to start after an upgrade would be the wrong way to find that out. So
`web-port` defaults to `0` and turning it on is a deliberate act. `aether-server setup`
offers 443 by default on a host that already has tailscaled, because a phone
reaching the dashboard is the point of putting the server on a tailnet, and a
tailnet that is not ready says exactly what to enable on the first start.

---

## Other tunnels

The reachability seam inside the server covers announcement and address
discovery - Tailscale first-class, plain host/port always available - and
leaves room for a tunnel adapter later. **Aether ships no relay infrastructure
of its own** and does not plan to. Anything that gets a TCP port from your
laptop to the server box works today; only Tailscale gets the keyless identity
integration.
