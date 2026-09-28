# Edge access policies and hosted-workspace boundaries

**Status:** Proposed. Nothing here is implemented. It revises
[Edge remote access](2026-09-27-edge-remote-access.md), which describes what
PR #249 contains today.

**Goal:** The owner of a server decides whether signing in with GitHub or
Google is enough to reach a workspace. The relay is usable under either
answer. The same identity, membership and discovery design later serves
workspaces that Aether operates.

An **edge** is the sign-in service and relay that servers and clients dial
out to. An **account** is a person signed in to it. A **device** is one
client install holding its own key. A **grant** is the edge's signed
statement that an account, on a device, is opening a connection. An **access
policy** is the server's rule for what a grant is worth.

## 1. Decisions

1. Two policies, chosen per server, enforced by the server:
   `account` and `approved-devices`.
2. The relay is transport under both. The edge admits a connection; the
   server authorizes it. Admission never implies authorization.
3. The policy is set only on the server host, by `aether-server setup` or
   `aether-server config set edge-access`. No RPC, edge message, dashboard
   action or client command changes it.
4. A missing `edge-access` key means `approved-devices`. The stricter policy
   is what a server falls back to, never the looser one.
5. Claiming moves inside SSH. The claim secret travels end to end, so the
   edge cannot redirect a claim to another account or device.
6. Tailnet identity and direct SSH keys are independent of the edge and of
   the policy. Neither policy changes them, and turning the edge off leaves
   them working.
7. Pending the owner's confirmation: the dashboard through the edge
   (per-server host names, TLS passthrough, browser sessions, the Android
   return link) is removed from this release. One host name serves the edge.

## 2. The two policies

| | `account` | `approved-devices` |
| --- | --- | --- |
| What signing in gives | Access, with the member's role | Identification only |
| New device | Registered on first connection | Pending until approved |
| Who approves | Nobody | The same member from an approved device, an admin from an approved device, or the machine's administrator with `sudo aether-server device approve <code>` |
| A member's first device | Registered | Pending, approved by an admin or on the machine |
| The claiming device | Registered by the claim | Approved by the claim: the code comes from the machine's console |
| Device keys | Created and managed by Aether | Created by Aether, admitted by a person |
| Lost every device | Sign in on a new one | An admin or the machine's administrator approves the new one |
| Suits | One person, or a team that already relies on its GitHub or Google accounts for source access | A server holding code or credentials the team must protect beyond that |

Under both: the server maps `(provider, subject)` to a member, roles and
capabilities are unchanged, every device is listed and revocable, and the
device key is verified by the server on every connection.

### Enforcement rules for `approved-devices`

Each rule closes a route that would otherwise admit a device on sign-in
alone.

1. **No first-device exception.** A member with no device gets a pending
   one. Today's code accepts it; that changes.
2. **An approved key names its member.** The grant's account must map to the
   member that owns the key. A grant naming another account with that key is
   refused.
3. **Approval is by code, typed.** The code is derived from the device key
   and shown on the requesting device, inside SSH. Lists never carry it. The
   approver gets it from the person, not from Aether.
4. **Approvers are already approved.** An approval RPC is accepted only on a
   connection authenticated by an approved device key, a member SSH key, or a
   tailnet identity. A pending device cannot approve.
5. **Invitations create members, not access.** Accepting an invitation
   creates the member with a pending device.
6. **Linking an identity to an existing member** creates no device. That
   member approves their first device from their tailnet or key connection.
7. **Replacing a key is enrolling a device.** A reinstall, a new machine or
   a rotated key is a new device and waits for approval.
8. **Another provider is another account.** The edge offers no account
   linking and no email match. Signing in with Google as a person known by
   GitHub reaches nothing.
9. **Provider account recovery gives an account, nothing more.** Whoever
   recovers or takes over the account meets rule 1 or 7.
10. **No recovery at the edge.** The edge has no reset, no support override
    and no operator command that approves a device. `aether-edge` operator
    commands remove and block; they never grant.
11. **The direct path accepts approved device keys only.**
12. **The policy cannot be loosened remotely** (decision 3). Every change is
    written to the server's log with the previous value.
13. **Tightening lists what it inherits.** Switching from `account` to
    `approved-devices` prints every device registered without approval and
    asks whether to keep or revoke each. Non-interactive, it keeps them and
    logs the list.

## 3. What an attacker can do

### With a member's GitHub or Google account, edge honest

| | `account` | `approved-devices` |
| --- | --- | --- |
| Sign in to the edge as the member | Yes | Yes |
| See the member's servers: names, ids, roles, online state | Yes | Yes |
| Reach a workspace | **Yes**, with the member's role on every server they belong to. As an admin: read and change repositories, run code, invite and remove members | No. The attacker's device is pending |
| Accept an invitation addressed to the member | Yes, and gains its role | Creates the member with a pending device; no access |
| Persist after the member recovers the account | Yes, until the device is revoked at the server | Nothing to persist |
| Trick someone into approving | Not needed | Must get an approver to type the code shown on the attacker's device |
| Disrupt | Revoke the member's device tokens, delete the edge account | The same. The member signs in again; approved keys stay approved |
| Be noticed | The device appears in `aether device list` | The pending request appears in `aether device list` |

### With the edge or its signing key

| | `account` | `approved-devices` |
| --- | --- | --- |
| Forge a grant for any account | Yes | Yes |
| Reach a workspace | **Yes**, as any member of any server on that edge using this policy | No. A forged grant yields a pending device |
| Read or alter an existing SSH session | No | No |
| Impersonate a server to a linked client | No: the host key is pinned by server id | No |
| Send a client that is linking for the first time to another server | Yes, by listing a false server id, unless the person links by an id their admin gave them | The same |
| Take a claim | No: the secret is inside SSH | No |
| Learn who connects to which server, when, from where | Yes | Yes |
| Deny service | Yes | Yes |

Under `account` the edge is part of the authorization path, so compromising
it is compromising every server that chose the policy. Under
`approved-devices` it is a directory and a relay. Neither statement depends
on OAuth or SSH keys being stronger than the other: the difference is how
many parties must fail. `account` fails when the provider account or the
edge fails. `approved-devices` also needs an approver to admit the device.

The first-link row is the residual risk under both. `aether link <server id>`
with an id read from `aether-server edge status`, or passed on by the admin,
closes it; `docs/security.md` says so.

## 4. Claiming inside SSH

1. `aether-server setup` prints `<server id>-<secret>`.
2. The client dials `/v1/connect/<server id>`. The edge admits a signed-in
   device to an unclaimed server only as `kind: "claim"`, rate-limited.
3. The client pins the host key to the id in the code and offers the secret
   as the SSH user, the way invite codes travel today.
4. The server checks the secret, five attempts, then creates the admin
   member from the grant's account and approves the connecting device key.
5. The server tells the edge it is claimed and by whom.

The edge sees neither the secret nor a moment at which it could substitute
an account and keep the device. The edge's **Add a server** page is removed:
a claim needs a device.

## 5. Setup wording

Shown by `aether-server setup` after the edge is enabled.

```
Who may reach this server through the edge?

  1) Account access
     Signing in with GitHub or Google is enough. People you invite start on
     a new device by signing in, and Aether creates and manages the device
     key. Access is as strong as each person's GitHub or Google account and
     the edge that vouches for it.

  2) Approved devices
     Signing in says who someone is. It does not admit a device. Each new
     device waits until it is approved: by that person from a device they
     already use, by an admin, or by you on this machine. A taken-over
     account, or a compromised edge, cannot add a device on its own.
     Recommended when this server holds code or credentials your team must
     protect.

Both leave Tailscale and direct SSH key access as they are.
Change it on this machine: aether-server config set edge-access <account|approved-devices>

Choose [1]:
```

| Setting | Default |
| --- | --- |
| `edge-url` | Empty. Set by setup, or `--edge-url` |
| `edge-access`, interactive setup | `account` on Enter |
| `edge-access`, key missing from the config | `approved-devices` |
| `aether-server install` with `--edge-url` and no `--edge-access` | Refused, naming the flag |

`edge-device-approval` is replaced by `edge-access`. It has not shipped.

## 6. Two kinds of server

| | User-operated | Aether-operated (later) |
| --- | --- | --- |
| Runs the workspace | The owner | Aether |
| Runs the edge | Aether, or the owner | Aether |
| Who can read the workspace | The owner's machine administrators | Aether's operators |
| What separating edge from server protects against | A compromised edge, under `approved-devices` | Nothing against Aether: one party runs both |
| What `approved-devices` protects against | A taken-over account and a compromised edge | A taken-over account only |
| Policy chosen by | The owner, on the machine | The team, through Aether |
| Server id | Derived from a host key the owner holds | Derived from a host key Aether holds |

Documentation and clients must not describe an Aether-operated workspace as
end to end against Aether. The client shows each server's kind and policy
wherever it lists servers.

## 7. Establish now

Each of these is cheap today and expensive after servers and clients have
shipped.

| Decision | Why now |
| --- | --- |
| An internal account id at the edge, distinct from `(provider, subject)`; grants keep carrying provider and subject | Later explicit account linking or SSO does not invalidate identities stored on servers |
| `kind` on a server record and in the servers list: `self-hosted` now, `hosted` reserved | Clients and docs can state the trust model without a protocol change |
| `access_policy` announced by the server at enrollment and returned in the servers list | The client can say what signing in will do before it connects |
| Ownership recorded as a principal with a type: `account` now, `team` reserved | Team-owned workspaces need no migration of owner rows |
| The server remains the only place that authorizes | A hosted workspace is the same binary with `account` policy, not a second authorization system |
| Separate origins for sign-in and relay, served by one process today | OAuth callback addresses, pinned edge origins and enrollment signatures name an origin. Moving sign-in later would re-enroll every server |
| Host-only cookies, never `Domain=onaether.dev` | A future app on a sibling host cannot read or set the edge's session |
| OAuth applications named for Aether, not for the edge | The consent page stays accurate when hosted workspaces use the same sign-in |
| Roles stay the server's existing ones | Hosted teams reuse them |

## 8. Can wait

Teams above the server, shared across workspaces. Hosted provisioning and
billing. A hosted dashboard. Account linking and SSO. Relay regions. Audit
log export. Policy per role. A native mobile client.

## 9. Deployment

- DNS: `A`/`AAAA` for the sign-in host and the relay host, DNS only, and a
  CAA record for each. No wildcard.
- TLS: terminated by the owner's nginx with a certbot certificate, proxying
  to `aether-edge` on a loopback port with WebSocket upgrade. The edge reads
  the client address from the forwarded header only when the peer is
  loopback. This mode is to be built; the built-in certificate mode stays
  for operators without a proxy.
- OAuth callbacks are on the sign-in host.
- Account deletion is self-service on the account page and in the client.

## 10. Testing

`internal/edge/edgetest` gains, for each policy: a forged grant for an
admin; a taken-over account enrolling a device; invitation acceptance; a
key presented with another member's account; approval attempted from a
pending device; a policy change attempted by RPC; a claim through an edge
that substitutes the account; a tightened policy listing inherited devices.
