# Edge access policies and hosted-workspace boundaries

**Status:** Implemented in PR #249
(https://github.com/3xDevOps/aether/pull/249). It revises
[Edge remote access](2026-09-27-edge-remote-access.md), which describes the
first version of that change. This document now describes what was built;
`docs/edge.md` is the operator and user guide.

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
7. The dashboard through the edge is deferred: per-server host names, TLS
   passthrough, server certificates, browser devices and sessions, and the
   Android return link are removed. The local gateway, the tailnet dashboard
   and the Android app over a tailnet are unchanged. The edge keeps the
   browser pages sign-in needs.
8. Two host names: `auth.onaether.dev` signs people in and
   `edge.onaether.dev` relays. One process serves both. Section 9 lists what
   depends on each name.
9. Interactive setup requires a policy choice; Enter selects nothing.
10. The edge's **Add a server** page is removed. A claim is made from the
    app or the CLI, by a device.
11. Edge packages are grouped under `internal/edge/`.
12. Deleting an account is confirmed after the person has seen what it
    affects, and never changes a server's members, roles or data
    (section 11).

## 2. The two policies

| | `account` | `approved-devices` |
| --- | --- | --- |
| What signing in gives | Access, with the member's role | Identification only |
| New device | Registered on first connection | Pending until approved |
| Who approves | Nobody | The same member from an approved device, an admin from an approved device, or the machine's administrator with `sudo aether-server device approve <code>` |
| A member's first device | Registered | Pending, approved by an admin or on the machine |
| The claiming device | Approved by the claim: the code comes from the machine's console | The same |
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
   approver gets it from the person, not from Aether. At 40 bits a key can
   be ground to match another device's code, so a code naming two waiting
   devices approves neither; `sudo aether-server device review` approves by
   choosing the device.
4. **Approvers are already approved.** An approval RPC is accepted only on a
   connection authenticated by an approved device key, a member SSH key, or a
   tailnet identity, under either policy. A pending or registered device
   cannot approve. Under `approved-devices` the same holds for inviting,
   linking an account, changing a role, approving a tailnet member and
   transferring ownership; a waiting device does not complete the SSH
   handshake at all.
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
   recovers or takes over the account meets rule 1 or 7. Recovery also
   removes nothing: devices signed in during a takeover keep their device
   tokens, which do not expire, until revoked at the edge and on each
   server.
10. **No recovery at the edge.** The edge has no reset, no support override
    and no operator command that approves a device. `aether-edge` operator
    commands remove and block; they never grant.
11. **The direct path accepts approved device keys only**, under either
    policy: it never asks the edge whether the account is still signed in.
12. **The policy cannot be loosened remotely** (decision 3). The server
    logs the policy at each start, with the previous one when it changed,
    so hand edits are recorded too; `config set` prints the value it
    replaced.
13. **Tightening inherits nothing.** A device registered under `account`
    is recorded as registered, not approved. Under `approved-devices` it is
    refused like a pending device until someone approves it.
    `sudo aether-server device review` lists each one to approve or revoke.
    Member SSH keys and tailnet identities are independent of the edge and
    keep working; the review prints them so the owner sees what remains.
14. **A device belongs to the identity it first signed in with**, not only
    to its member, so an account deletion finds its devices.

## 3. What an attacker can do

As built and tested in `internal/edge/edgetest`; the test proving each row
is named where one exists.

### With a member's GitHub or Google account, edge honest

| | `account` | `approved-devices` |
| --- | --- | --- |
| Sign in to the edge as the member | Yes | Yes |
| See the member's servers: names, ids, roles, online state, policy | Yes | Yes |
| Reach a workspace | **Yes**, with the member's role on every server they belong to (`TestTakenOverProviderAccount`) | No. The attacker's device is pending (`TestTakenOverProviderAccount`) |
| As an admin | Invite, mint invite codes, change roles, link accounts, transfer ownership; not approve a device (`TestAccountAccess`) | Nothing: a pending device completes no handshake (`TestApprovedDevices`) |
| Accept an invitation addressed to the member | Yes, and gains its role | Creates the member with a pending device; no access |
| Persist after the member recovers the account | Yes: the device token does not expire and the device stays registered until revoked at the edge and on each server, with anything done as an admin | A pending device and a token remain, with no access |
| Trick someone into approving | Not needed | Must get an approver to type the code shown on the attacker's device |
| Disrupt | Revoke the member's device tokens; delete the edge account, which removes the member's edge identity and edge devices on every server | The same |
| Be noticed | The device appears in `aether device list` and `aether-server device review` with the account it signed in as (`TestTakenOverProviderAccount`) | The pending request appears there |

### With the edge or its signing key

| | `account` | `approved-devices` |
| --- | --- | --- |
| Forge a grant for any account | Yes | Yes |
| Reach a workspace | **Yes**, as any member of any server on that edge using this policy (`TestMaliciousEdgeAccountAccess`) | No. A forged grant yields a pending device (`TestMaliciousEdgeApprovedDevices`) |
| Accept an open invitation | Yes, with its role | Creates the invited member, bound to the attacker's account, with a pending device; no access (`TestMaliciousEdgeApprovedDevices`) |
| Present a member's approved key under another account | No (`TestMaliciousEdgeAccountAccess`) | No (`TestMaliciousEdgeApprovedDevices`) |
| Replay a connection; change the policy, a member or the owner through control messages | No | No (`TestMaliciousEdgeApprovedDevices`, `TestServerVerifiesGrants`) |
| Send a forged account deletion | Removes access only | Removes access only (`TestMaliciousEdgeApprovedDevices`) |
| Read or alter an existing SSH session | No | No |
| Impersonate a server to a linked client | No: the host key is pinned by server id | No |
| Send a client that is linking for the first time to another server | Yes, by listing a false server id, unless the person links by an id their admin gave them | The same |
| Take a claim | No: the secret is inside SSH and the server refuses a grant naming another account than the client's (`TestMaliciousEdgeSubstitutesTheClaimingAccount`, `TestClaim`), unless the edge also lied to the device at sign-in about its account, which `aether login` prints | The same |
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
closes it; `aether link --from-edge` shows the id and host key and asks
before it pins them; `docs/security.md` says so.

## 4. Claiming inside SSH

1. `aether-server setup` prints `<server id>-<secret>`.
2. The client dials `/v1/connect/<server id>/claim`. The edge admits a
   signed-in device there, to a server without an owner, only as
   `kind: "claim"`, rate-limited. The path says the connection is a claim
   because the edge cannot read the SSH user.
3. The client pins the host key to the id in the code and offers the secret
   as the SSH user, `claim:<code>:<provider>:<subject>`, the way invite
   codes travel, together with the account it signed in as. The client's
   SSH signature covers the user name. The server refuses a grant naming
   another account before it spends an attempt.
4. The server checks the secret in constant time, five attempts, only once
   the client has signed with the grant's device key. On a server without
   members it creates the admin from the grant's account; on one with
   members the account must already be an admin's identity or match an
   admin's open link. It approves the connecting device key under either
   policy, and the same connection continues as that admin.
5. The server reports `claimed` on its own control connection. The edge
   records the owner only for a claim connection it opened to that server
   within 60 seconds, and only as that connection's account.

The edge sees neither the secret nor a moment at which it could substitute
an account and keep the device, unless it also told the client at sign-in
that it is another account; `aether login` prints the account the edge
reported. The edge's **Add a server** page is removed:
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
     Recommended for a team workspace holding code or credentials that
     must stay protected.

Both leave Tailscale and direct SSH key access as they are.
Change it on this machine: aether-server config set edge-access <account|approved-devices>

Choose 1 or 2:
```

| Setting | Default |
| --- | --- |
| `edge-url` | Empty. Set by setup, or `--edge-url` |
| `edge-access`, interactive setup | None: the question repeats until answered `1` or `2` |
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

## 9. Host names and what depends on them

As found in the code:

| Thing | Bound to |
| --- | --- |
| OAuth callback addresses, edge session cookie | Sign-in host |
| Device authorization pages, the account page and the client API | Sign-in host |
| A client's device token | The relay origin it is filed under and the sign-in origin that issued it |
| Server enrollment signature, relay endpoints, `GET /v1/edge` | Relay origin |
| Grants | The edge signing key, and the relay origin each names as its issuer |
| A server's pin, recorded owner and claim code | The edge signing key, kept per relay origin on the server |
| The edge's record of a server | The server id |
| A client's pin of a server | The server id, not a host name |
| An account | The provider's user id, which does not depend on the OAuth application |

What a change forces:

- **Sign-in host:** no server enrolls again. Browsers sign in again, the
  OAuth callbacks are registered again, and each client runs `aether
  login` before the client API works again; relayed connections keep
  working with the old token.
- **Relay host:** each server's `edge-url` changes and the server enrolls at
  the new origin, pinning the edge key again and holding no owner record
  there, while the edge keeps the claim under the server id. Ownership
  changes are refused until the operator removes the server and an admin
  claims it again. Clients sign in and link again by the same id. Not
  covered by a test.
- **Edge signing key:** every server runs `aether-server edge trust`.
- **A server's host key:** a new server id: claim and link again.
- **OAuth application:** nothing beyond the new client id and secret.

## 10. Deployment

- DNS: `A`/`AAAA` for the sign-in host and the relay host, DNS only, and a
  CAA record for each. No wildcard.
- TLS: terminated by the owner's nginx with a certbot certificate, proxying
  to `aether-edge --proxy-listen` on a loopback port with WebSocket upgrade
  (`packaging/nginx/aether-edge.conf.example`). The edge reads the client
  address from the right-most `X-Forwarded-For` entry only when the peer is
  loopback, and refuses a request from the proxy without a usable one. The
  built-in certificate mode stays for operators without a proxy; the
  packaged unit grants no capability, and that mode adds
  `CAP_NET_BIND_SERVICE` with a drop-in.
- OAuth callbacks are on the sign-in host.
- `docs/edge.md` has the owner checklist.

## 11. Deleting an account

1. The page, or `aether logout --delete-account` (`aether account` already
   shares agent accounts), lists the servers the account owns and the
   servers it is a member of.
2. For each owned server it names the transfer command, `aether member
   transfer <member id>`. A transfer is an admin's request to the server
   (`server.owner.transfer`), names an existing admin with a linked
   account, and is recorded by the edge only when the server reports it.
3. The person confirms by typing the account's login or email, within 5
   minutes of a sign-in with the provider.
4. The edge deletes the account, its device tokens and its sessions, closes
   its relayed connections, and tells each server it owned, was a member
   of or was admitted to, at once or at the server's next enrollment (at
   most 1000 owed per server). The server removes that identity and its
   edge devices and closes their connections, direct ones included. It
   removes no member, changes no role and deletes no data.
5. The confirmation states what remains: a member's SSH key and tailnet
   identity on each server keep working until an admin removes them there.
6. A server left without an owner stays enrolled and ownerless. Its
   administrator recovers it on the machine with a new claim code, used by
   an admin whose account is linked. No account becomes owner or admin
   without that.

## 12. Existing installations

- A server that never enabled the edge is untouched by an upgrade: with
  `edge-url` empty no agent is created, and the store migration keeps every
  row. No test starts an upgraded server without the edge.
- Enabling it later goes through the same policy question.
- Existing tailnet and key members stay as they are. An admin links a
  member to an account; under `approved-devices` that member approves their
  first device from their tailnet or key connection.
- The server's store migration and the edge's are each tested on a
  populated database.

## 13. Testing

`internal/edge/edgetest` covers, end to end: a forged grant for an admin
and a forged invitation acceptance under each policy
(`TestMaliciousEdgeApprovedDevices`, `TestMaliciousEdgeAccountAccess`); a
taken-over account enrolling a device (`TestTakenOverProviderAccount`); a
key presented with another member's account (both malicious-edge tests);
approval attempted from a pending or registered device
(`TestApprovedDevices`, `TestAccountAccess`); a claim through an edge that
substitutes the account (`TestMaliciousEdgeSubstitutesTheClaimingAccount`);
a tightened policy refusing inherited devices until reviewed
(`TestPolicySwitchToApprovedDevices`); revocation, deletion and ownership
(`TestRevocationClosesLiveConnections`, `TestDeleteAccount*`,
`TestAccountDeletionReachesAnOfflineServer`); and the nginx configuration
(`TestBehindNginx`, `scripts/edge-nginx-test.sh`). A policy change by RPC
or by edge message is refused for every control method
(`TestNoControlMethodChangesEdgeAccess` in `internal/sshd`) and every
message type (`TestNoEdgeMessageChangesThePolicy` in `internal/edge/agent`). `docs/testing.md` lists every test.
