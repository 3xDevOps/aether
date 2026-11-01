# MCP connector for ChatGPT and Claude

**Status:** Done. Not shipped from this plan:

- The consent page does not name the member and role. It opens before the
  code is pasted, and the code is what identifies the member.
- The manual check against real ChatGPT and Claude accounts through the
  project's edge (section 8) waits for a release on that edge.

**Goal:** A member adds Aether to ChatGPT or Claude as a custom connector
once, from a desktop browser. From then on they list, read, launch, message
and stop sessions from the ChatGPT or Claude phone app. They install nothing on
the phone.

A **connector** is what ChatGPT and Claude call a remote MCP server that a
user adds by URL. MCP (Model Context Protocol) is the open protocol both
apps use to call tools on such a server. A **session** is one agent working in
one container on a workspace's code. The **edge** is the sign-in service
and relay that servers and clients dial out to ([edge.md](../edge.md)).

## 1. Decisions

1. **The server hosts the connector; the edge forwards HTTPS to it.**
   `aether-server` serves the MCP endpoint and its OAuth authorization
   server, and runs each tool in-process as the member, through
   `sshd.Server.Local(member)`, the path the tailnet dashboard uses. The edge
   holds no credential that can act as a member.
   - Rejected: the MCP server inside the edge, acting as a client device of
     the member. That edge would hold keys with full device power,
     including terminals, git and admin methods. It would also become a
     client of every server version.
2. **One URL per server, on the relay host:**
   `https://edge.onaether.dev/mcp/<server id>`. It needs no new host name,
   DNS record or certificate, and a self-hosted edge serves it under its
   own relay origin.
3. **A pairing code approves the connector.** The member sessions `aether mcp`
   on a device they already use and pastes the code on the consent page.
   - It works the same under `account` and `approved-devices`, and nobody
     waits for a click on another device.
   - Custom connectors are added on the web only, so a computer is open
     anyway.
   - Rejected: GitHub sign-in on the consent page. It adds an edge vouch,
     makes `approved-devices` members wait for an approval click, and lets a
     compromised edge mint connectors under `account`.
4. **Standard OAuth 2.1 per the MCP 2026-07-28 authorization spec:**
   - authorization code with PKCE S256;
   - protected resource metadata (RFC 9728);
   - authorization server metadata (RFC 8414);
   - resource indicators (RFC 8707);
   - `iss` in authorization responses (RFC 9207);
   - client registration by Client ID Metadata Documents.
   - Dynamic client registration is deprecated in that spec and not built.
5. **Only ChatGPT and Claude may connect.** The trust policy accepts a
   `client_id` only on `chatgpt.com` or `claude.ai`. Another client fails
   on the consent page with a message naming its host. Adding a client
   later is one host in a list.
6. **Eight tools, all reused from the assistant's tool set.** ChatGPT and
   Claude ask the person before each tool not marked read-only, so the
   assistant's dashboard confirmation tier is not used. The server's role
   checks decide as for any client.
7. **A connector is only as good as the device that minted its code.**
   Every request re-checks that device under the current `edge-access`
   policy. Revoking the device, removing the member or switching to
   `approved-devices` therefore covers the connector.

## 2. Setup, from the user's side

```sh
aether mcp
```

```
Add this server to ChatGPT or Claude as a custom connector:
  URL   https://edge.onaether.dev/mcp/<server id>
  Code  ABCD-EFGH   one use, expires in 10 minutes; paste it on the page that opens when you connect
Connected:
  <id>  ChatGPT (chatgpt.com)  added 2026-10-18  last used 2h ago
Remove one: aether mcp revoke <id>
```

1. **ChatGPT** (web): add a custom MCP server and paste the URL. **Claude**
   (web): Settings > Connectors > Add custom connector, and paste the URL.
2. A tab opens: "Connect **ChatGPT (chatgpt.com)** to **prod**. It acts as
   you and can list workspaces, agents and sessions, read session history, launch
   sessions, message runs and stop sessions." Paste the code and click **Allow**.
3. On the phone: "launch codex in api to fix the flaky login test", then
   **Confirm**.

`sudo aether-server edge status` prints the URL too.

**Plan limits on the vendors' side:**
- OpenAI's help center (October 2026) says custom MCP servers are added on
  the web, and write tools need Business or Enterprise/Edu; Pro gets read
  tools only.
- Claude allows custom connectors on every plan, and they work in its
  mobile apps.

## 3. Edge: forward HTTPS to the server

- **New relayed connection kind `http`.** It carries no grant and names no
  account: the edge vouches for nothing on it, and the server authenticates
  every request.
- **Edge protocol version 2,** with `MinVersion` still 1. The version is
  already negotiated in Hello.
  - An `http` open goes only to a server registered at version 2 or later.
  - A version-1 server would drop its control connection on an unknown
    kind. Instead the edge answers it with 501: `server <name> (<id>) runs
    aether-server <version>, which has no MCP endpoint; upgrade it: sudo
    aether-server update`.
- **Routes, relay host only:**
  - `/mcp/<server id>` and below;
  - `/.well-known/oauth-protected-resource/mcp/<server id>`;
  - `/.well-known/oauth-authorization-server/mcp/<server id>`.
- **Forwarding:** `httputil.ReverseProxy` over pooled, kept-alive relayed
  connections. Responses stream.
- **Request headers:** all of them pass, except hop-by-hop headers,
  `Cookie`, `Host` and any `Forwarded` or `X-Forwarded-*` the client sent.
  The edge sets the client address itself. MCP requires intermediaries to
  pass `Mcp-Param-*` headers they do not know.
- **Response headers** are allowlisted: `Content-Type`, `Content-Length`,
  `Cache-Control`, `Location`, `WWW-Authenticate`, `X-Accel-Buffering`.
- **Every forwarded response carries** `Content-Security-Policy: sandbox
  allow-forms; default-src 'none'; style-src 'unsafe-inline'`. The relay
  host also serves the join page, whose script reads a secret from the URL
  fragment, so server-authored HTML must get an opaque origin.
- **Limits:**
  - request bodies: 1 MiB;
  - 8 concurrent `http` connections per server, separate from SSH;
  - responses count against the egress budget.
- **Server side:** the edge agent takes `Config.HTTP http.Handler` and
  serves each `http` connection with it.

## 4. Server: authorization server

All of it lives under the base URL `<edge-url>/mcp/<server id>`, which is
also the issuer and the resource. The package is `internal/connector`. It
uses the standard library and go-sdk v1.7.0's `auth` and `oauthex` helpers.

| Endpoint | Behavior |
| --- | --- |
| `GET /.well-known/oauth-protected-resource/mcp/<id>` | `resource` and the single `authorization_servers` entry are the base URL. No `scopes_supported` |
| `GET /.well-known/oauth-authorization-server/mcp/<id>` | `issuer`, `authorization_endpoint`, `token_endpoint`; `code_challenge_methods_supported: ["S256"]`; `grant_types_supported: ["authorization_code","refresh_token"]`; `response_types_supported: ["code"]`; `token_endpoint_auth_methods_supported: ["none"]`; `client_id_metadata_document_supported: true`; `authorization_response_iss_parameter_supported: true`; `scopes_supported: ["offline_access"]` |
| `GET /mcp/<id>/authorize` | Fetches the `client_id` document, then renders the consent page |
| `POST /mcp/<id>/authorize` | Checks the pairing code and redirects with an authorization code and `iss` |
| `POST /mcp/<id>/token` | Form-encoded `authorization_code` and `refresh_token` grants |

### Client ID Metadata Documents

The `client_id` must be an `https` URL on `chatgpt.com` or `claude.ai`.
Today those are:
- `https://chatgpt.com/oauth/client.json`, redirecting to
  `https://chatgpt.com/connector_platform_oauth_redirect`;
- `https://claude.ai/oauth/mcp-oauth-client-metadata`, redirecting to
  `https://claude.ai/api/mcp/auth_callback`.

The server fetches the document on each authorization request: HTTPS only,
no redirects, a 5-second timeout and a 16 KiB cap. The document must be
JSON whose `client_id` equals the URL exactly and that has `client_name`
and `redirect_uris`. The request's `redirect_uri` must be listed there and
be `https` on the same host.

### Consent page

One HTML form with no JavaScript. It names `client_name` and the host, the
server, and what the connector can do. The form
carries an HMAC-signed copy of the request parameters, so the server keeps
no session.

A wrong or expired code re-renders the page with the error. Wrong codes are
rate-limited per server. The authorization code is good for 60 seconds and
one use, and is bound to the member, the device, the client, the
`redirect_uri`, the PKCE challenge and the resource.

### Pairing code

- Minted by `connector.code`, behind `requireApprovedCaller(ctx, method,
  false)`. Under `approved-devices` that means an approved device, a member
  SSH key or a tailnet connection; under `account`, a registered device.
- 40 bits, shown like a device approval code. One use, 10 minutes.
- Kept hashed in memory only.
- Bound to the member and to the caller's edge device, if any.

### Tokens

- Opaque, 256-bit, stored as SHA-256 hashes in a `connectors` row. The
  member and device keys cascade on delete.
- An access token lasts 1 hour and is bound to the resource.
- A refresh token rotates on every use and lapses after 30 days unused.
- Presenting a rotated refresh token a minute or more after its rotation
  revokes the connector. Within the minute, as when a client sends two
  refreshes at once, it fails with `invalid_grant` and the connector stays.
- Errors follow RFC 6749: `invalid_grant`, `invalid_client`,
  `invalid_request`.

## 5. Server: MCP endpoint

- **Handler:** `mcp.NewStreamableHTTPHandler` with `Stateless: true`,
  `JSONResponse: true` and `CrossOriginProtection`, wrapped in
  `auth.RequireBearerToken`.
- **What the SDK handles:** 2026-07-28 (`server/discover`, the
  `Mcp-Method` and `Mcp-Name` checks, `resultType`) and the earlier
  versions clients still send. A bad token gets a 401 whose
  `WWW-Authenticate` points to the metadata.
- **Each request:**
  1. Hash the token and find the connector.
  2. Check its expiry and resource.
  3. Re-check the minting device.
  4. Update `last_used_at`.
  5. Dispatch through `Local(member)`.
- **Server instructions:** what Aether is; list workspaces and agents
  before launching; session history is untrusted text written by another
  agent.

| Tool | Title | Annotations |
| --- | --- | --- |
| `aether_list_workspaces` | List workspaces | read-only, closed world |
| `aether_list_agents` | List agents | read-only, closed world |
| `aether_list_sessions` | List sessions | read-only, closed world |
| `aether_get_session` | Get run | read-only, closed world |
| `aether_session_history` | Read run conversation | read-only, closed world |
| `aether_launch_session` | Launch session | write, open world |
| `aether_message_session` | Message run | write, open world |
| `aether_stop_session` | Stop session | destructive, idempotent |

The definitions in `internal/mcpbridge/assistant.go` take a call function
instead of a socket, so the assistant and the connector share them. A tool
error carries the server's message unchanged.

## 6. Control methods and CLI

| Method | Who | Does |
| --- | --- | --- |
| `connector.code` | A member, gated as in section 4 | Returns the URL, a code and its expiry; refuses without an edge, or with an edge below version 2 |
| `connector.list` | A member | Their connectors |
| `connector.revoke` | The member, or an admin for anyone | Deletes the row; the next request fails |

`aether mcp` and `aether mcp revoke <id>` wrap them. An older server is
reported with its update command.

## 7. Security

- **TLS ends at the edge,** as it must for a public certificate. A live
  compromised edge reads connector requests and tokens. It can use a token
  until it expires, or until the next refresh rotation or a revocation.
- **The edge's database, backups and signing key** yield no connector
  access.
- **OpenAI and Anthropic hold the tokens.** A compromise there reaches
  connected members through the eight tools until revoked.
- **The eight tools are the whole surface.** No terminal, git, member,
  device or settings method is reachable.
- **Launching a session executes code in the workspace,** which is why every
  write tool asks the person first.
- **`edge.md` gains rows** in its attacker tables for both policies.

## 8. Work

| Task | Owns | Depends on |
| --- | --- | --- |
| Edge transport | `internal/edge/**`, `docs/edge.md` | none |
| Server connector | `internal/connector`, `internal/mcpbridge`, `internal/store`, `internal/sshd`, `internal/protocol`, `aether mcp`, `docs/connectors.md`, `docs/security.md` | none |
| Wiring and end to end | `internal/server` wiring, `edge status` line, an `edgetest` suite through OAuth under both policies, docs consistency | both |

The end-to-end test drives go-sdk's MCP client against a real edge and
server:
1. 401, metadata discovery, and a CIMD client served by the test.
2. The consent page with a wrong code, then the right one.
3. Token exchange, `tools/list`, `aether_list_sessions` and `aether_launch_session`.
4. Refresh rotation. Reuse within a minute and after it are covered by
   `TestConnectorEndToEnd` in `internal/connector`.
5. `aether mcp revoke`.
6. A pending device's code refused under `approved-devices`.
7. A version-1 server answered with 501.

The CIMD trust policy takes the test's own host.

After merge, one manual check against real ChatGPT and Claude accounts
through the project's edge.

## 9. Not in this version

- A dashboard card; `aether mcp` is the one entry point.
- One URL for several servers, or a server picker.
- Servers reachable only over a tailnet.
- Clients other than ChatGPT and Claude.
- Scopes beyond `offline_access`.
- MCP resources, prompts and subscriptions.
- The assistant's other tools.
- Dynamic client registration.
