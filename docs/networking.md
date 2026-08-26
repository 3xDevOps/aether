# Networking and identity

Aether needs exactly one thing from your network: **the CLI must be able to
reach the server's SSH port.** Git transport, the control channel, event
streams, PTY attach and the dashboard forward all multiplex over that one
connection. There is no second port to open and no HTTP surface you have to
expose.

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
aether init --data-dir /var/lib/aether
```

`aether init` reports what it found:

```
tailscale: socket /var/run/tailscale/tailscaled.sock detected
tailscale: tailnet hostname my-server.tailnet-name.ts.net
```

That hostname is what you hand teammates. Start the server as usual:

```sh
aether-server serve --data-dir /var/lib/aether --addr :2222
```

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

- **First contact bootstraps the admin.** The first tailnet identity to link a
  fresh server is registered as an admin, not pending. Solo developers never
  see a join step.
- **Everyone after that joins pending.** They are registered as collaborators
  with `Pending` set, and can authenticate but not act until an admin runs
  `aether member approve <member-id>`. Until then commands fail with
  `membership pending admin approval`.
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
There is no `--key` flag: to use a key at another path, or a
passphrase-protected one, `ssh-add` it first. Without a usable key the
handshake fails with `attempted methods [none]`.

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

The dashboard is not a server-side listener at all. `aether gui` serves it
from your own machine, bound to `127.0.0.1`, and reaches the server over the
same SSH connection the CLI uses. There is no dashboard port to open, no
forward to hold, and no exposure flag - the server's only listener is SSH.

That also means nothing about the dashboard changes the server's network
shape: whatever reachability you arranged for `--addr` is the whole story.
See [local-gateway.md](local-gateway.md) and [security.md](security.md).

---

## Other tunnels

The reachability seam inside the server covers announcement and address
discovery - Tailscale first-class, plain host/port always available - and
leaves room for a tunnel adapter later. **Aether ships no relay infrastructure
of its own** and does not plan to. Anything that gets a TCP port from your
laptop to the server box works today; only Tailscale gets the keyless identity
integration.
