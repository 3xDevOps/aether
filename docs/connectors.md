# ChatGPT and Claude connectors

A connector is a custom MCP connector in ChatGPT or Claude that points at your
Aether server. Once you add one, you can list, launch, message and stop runs
from a chat in ChatGPT or Claude, the phone apps included, with no Aether app
on the phone. The connector acts as you: the Aether member who authorized it.

Your server hosts the MCP endpoint and its OAuth 2.1 authorization server. The
edge only forwards HTTPS to it. Each tool call runs in the server process as
you, under the same role checks as `aether` and the dashboard.

## Prerequisites

- **A server connected to an edge.** ChatGPT and Claude reach the connector
  at `<edge-url>/mcp/<server id>`, for example
  `https://edge.onaether.dev/mcp/<server id>`. A server reachable only on a
  tailnet cannot have one. To turn the edge on, see
  [edge.md](edge.md#turning-it-on).
- **This release on the server and the edge.** The edge forwards connector
  traffic only to a server new enough to serve it. It answers an older one
  with HTTP 501:
  `server <name> (<id>) runs aether-server <version>, which has no MCP endpoint; upgrade it: sudo aether-server update`.
- **A linked CLI** (`aether link`) on an approved device, a member SSH key or
  a tailnet connection. Under `edge-access account`, any registered device
  works too.
- **A ChatGPT or Claude plan that allows custom connectors.** See the plan
  notes under each client below.

## Get the URL and a code

```sh
aether mcp
```

```
Add this server to ChatGPT or Claude as a custom connector:
  URL   https://edge.onaether.dev/mcp/<server id>
  Code  ABCD-EFGH   one use, expires in 10 minutes; paste it on the page that opens when you connect
Connected:
  con-7k2m9q4xbd  ChatGPT (chatgpt.com)  added 2026-10-18  last used 2h ago
Remove one: aether mcp revoke <id>
```

The URL is the same for ChatGPT and Claude; on the server,
`sudo aether-server edge status` prints it too:

```
connector    https://edge.onaether.dev/mcp/<server id>  (add it to ChatGPT or Claude; get a code: aether mcp)
```

The pairing code is 8 characters (40 bits) and works once, within 10
minutes. Running `aether mcp` again replaces a code you have not used. The
connector is bound to the edge device you ran `aether mcp` on. A code minted
over a member SSH key or a tailnet connection binds no device.

## Add it in ChatGPT

Custom MCP servers are added on the web only, at chatgpt.com
([OpenAI: custom MCP servers](https://developers.openai.com/api/docs/guides/custom-mcp-server)):

1. Open [ChatGPT Plugins](https://chatgpt.com/plugins).
2. Select the plus button, then **Add custom MCP server**.
3. Paste the URL from `aether mcp`.
4. Select **Create as a plugin**, then install it.
5. A page from your server opens: **Connect ChatGPT (chatgpt.com) to
   <server>**. Paste the code and select **Allow**.

OpenAI's help center ([article 12584461](https://help.openai.com/en/articles/12584461))
says, as of October 2026, that write tools need ChatGPT Business or
Enterprise/Edu; Pro gets the read tools only. OpenAI says plugins work in ChatGPT on the web, desktop and
mobile ([OpenAI: plugins](https://learn.chatgpt.com/docs/plugins.md)).

## Add it in Claude

On claude.ai
([Anthropic: add a custom connector](https://claude.com/docs/connectors/custom/add-unlisted)):

- **Free, Pro and Max:** open **Customize > Connectors**, select **Add custom
  connector**, and paste the URL from `aether mcp`. Free allows one custom
  connector.
- **Team and Enterprise:** an Owner opens **Organization settings >
  Connectors**, selects **Add**, **Custom**, then **Web**, and pastes the URL.
  Each member then selects **Connect** under **Customize > Connectors**.

Select **Connect**. A page from your server opens: **Connect Claude
(claude.ai) to <server>**. Paste the code and select **Allow**. A connected
connector works in Claude on the web, desktop and mobile
([Anthropic: getting started](https://claude.com/docs/connectors/getting-started)).

## What it can do

| Tool | Title | Control method | Hints | Does |
| --- | --- | --- | --- | --- |
| `aether_list_workspaces` | List workspaces | `workspace.list` | read-only, closed world | Lists the workspaces you can launch runs in |
| `aether_list_agents` | List agents | `agent.list` | read-only, closed world | Lists the agents, with whether each is installed, has a login, and supports enhanced mode |
| `aether_list_runs` | List runs | `run.list` | read-only, closed world | Lists the runs you can see; `mine`, `active_only` and `workspace_id` filter them |
| `aether_get_run` | Get run | `run.get` | read-only, closed world | Shows one run: state, task, workspace, agent and mode |
| `aether_run_history` | Read run conversation | `run.acp.history` | read-only, closed world | Reads a page of an enhanced run's conversation, 20 frames unless `limit` says otherwise |
| `aether_launch_run` | Launch run | `run.launch` | not destructive, open world | Starts a run: a workspace, an agent, a task, and optionally a mode and model |
| `aether_message_run` | Message run | `run.inject` | not destructive, open world | Sends a message to a run's agent; `steer` adds it to an enhanced run's running turn |
| `aether_stop_run` | Stop run | `run.kill` | destructive, idempotent, closed world | Stops a run |

These 8 tools are the whole surface. ChatGPT and Claude ask you before each
tool that is not read-only: ChatGPT asks by default and can remember your
answer for the rest of a conversation, and Claude offers **Allow once** and
**Always allow** per tool. Aether shows no confirmation card of its own, and
the [assistant's auto-approval](assistant.md#auto-approval) does not apply.
Your role still decides: a viewer's launch is refused, as it is in `aether`.

The server's MCP instructions tell the model what Aether is, to list
workspaces and agents before launching, and that run history is untrusted
text written by another agent. The tool definitions are shared with the
[member assistant](mcp-bridge.md#the-assistant-tool-set).

## Revoking

```sh
aether mcp revoke con-7k2m9q4xbd
```

The connector's next call fails, and ChatGPT or Claude asks you to connect it
again with a new code. You can revoke your own connectors; an admin can revoke
anyone's. The server log records `connector: created` with each connector's
id, member and device.

Removing the connector in ChatGPT or Claude does not revoke it on the server;
revoke it here too. A connector also stops working at its next request when:

- the device it was minted on is revoked (`aether device revoke <device id>`);
- its member is removed;
- the server runs under `edge-access approved-devices` and that device is not
  approved;
- its refresh token goes unused for 30 days; `aether mcp` then no longer
  lists it;
- a refresh token it already rotated away is used again a minute or more
  after the rotation, which revokes it. Within that minute, as when a client
  sends two refreshes at once with one token, the late one fails with
  `invalid_grant` and the connector stays;
- the server's connector URL changes, because the edge moved to another
  origin: its tokens are good only for the URL they were minted for.

A member merge moves the absorbed member's connectors to the survivor, with
their devices.

## Errors

From `aether mcp`, which prints each as `aether: rpc error <code>: <message>`:

| Message | Cause |
| --- | --- |
| `connector.code: this server has no edge (edge-url is empty), and ChatGPT and Claude reach a connector only through one; an admin turns it on with: sudo aether-server config set edge-url https://edge.onaether.dev, then sudo systemctl restart aether-server` | The edge is off |
| `connector.code: this server is not connected to its edge at <edge-url>; an admin sees why with: sudo aether-server edge status` | The edge connection is down |
| `connector.code: edge <edge-url> speaks edge protocol 1 and cannot forward connectors; its operator upgrades it` | The edge predates connectors |
| `connector.code needs an approved device, a member SSH key or a tailnet connection; this connection signed in with device "<label>", which is registered` | `approved-devices`, and your CLI signed in with a device nobody approved |
| `method not found: connector.code; the server is older than this CLI, and an admin updates it with aether server update` | The server predates connectors |
| `connector.revoke: no connector <id>; aether mcp lists yours` | Wrong id, or already revoked |
| `connector.revoke: only its own member or an admin may do this` | Another member's connector |

On the consent page:

| Message | Cause |
| --- | --- |
| `Aether connectors work with ChatGPT and Claude; <host> is not one of them.` | The client's `client_id` is not an `https` URL on `chatgpt.com` or `claude.ai` |
| `This client's metadata document cannot be used: <reason>` | Fetching the `client_id` document failed, or it is not JSON, is over 16 KiB, names another `client_id`, or lacks `client_name` or `redirect_uris` |
| `The redirect_uri "<uri>" is not an https URL on <host> that <client_id> lists.` | The client asked to return somewhere its document does not list |
| `That code is wrong, expired or already used. Run aether mcp for a new one and paste it here.` | The code is not live; a server restart also forgets codes |
| `Too many wrong codes were tried on this server in the last minute. Wait a minute, then paste the code again.` | More than 10 wrong codes in a minute, from anyone; no code is checked until the minute passes |
| `This form was not issued by this server. Start the connection again from ChatGPT or Claude.` | A tampered or foreign form |

ChatGPT and Claude show these errors after redirecting from the
authorization request: `unsupported_response_type` (`response_type must be
code`), `invalid_request` (`PKCE is required: send code_challenge with
code_challenge_method S256`) and `invalid_target` (`resource must be <URL>`).

The token endpoint answers with RFC 6749 errors:

| `error` | `error_description` |
| --- | --- |
| `invalid_client` | `client_id is required` |
| `invalid_request` | `grant_type is required`, `code, code_verifier and resource are required`, `refresh_token is required` |
| `invalid_target` | `resource must be <URL>` |
| `invalid_grant` | `the authorization code is wrong, expired or already used`; `the authorization code was issued to another client`; `redirect_uri does not match the authorization request` (it is optional, and must match when sent); `code_verifier does not match the code_challenge` |
| `invalid_grant` | `another refresh rotated this refresh token less than a minute ago; use the refresh token it returned`; the connector stays |
| `invalid_grant` | `this refresh token was already used, so its connector was revoked; connect again with a new code from aether mcp` |
| `invalid_grant` | `no connector holds this refresh token; it lapsed, or the connector was revoked; ...`; `the refresh token was issued to another client`; `the refresh token lapsed after 30 days unused; ...`; `the refresh token is for <URL>, not <URL>; ...`; `connector <id>: sshd: device <id> is revoked; ...` |
| `unsupported_grant_type` | `grant_type <name> is not supported; use authorization_code or refresh_token` |

The MCP endpoint answers a missing or dead token with `401` and
`WWW-Authenticate: Bearer resource_metadata="<edge-url>/.well-known/oauth-protected-resource/mcp/<server id>"`,
which starts sign-in again. The body says why: `no bearer token`,
`invalid token: no connector holds this access token; it was refreshed, or the
connector was revoked`, `invalid token: the access token expired at <time>`,
`invalid token: the access token is for <URL>, not <URL>`, or
`invalid token: connector <id>: sshd: device <id> is revoked`. A request a
browser sends from another origin is refused with `403`.

## Security model

- **The tokens are bearer credentials held by OpenAI or Anthropic.** Whoever
  holds the access token acts as you for up to an hour, and whoever holds the
  refresh token can mint new access tokens until it goes 30 days unused. Each
  token is an opaque 256-bit value; the server keeps only its SHA-256 hash.
  Every refresh rotates the refresh token. Presenting a rotated one a minute
  or more after its rotation revokes the connector, so a stolen refresh
  token ends the connector once the thief and the client have both used it,
  a minute or more apart. Within the minute the late refresh is only
  refused, so a client's two simultaneous refreshes do not revoke it.
- **TLS ends at the edge.** ChatGPT and Claude need a public certificate, so
  the edge decrypts connector traffic and forwards it to the server. A
  compromised edge, while it relays, reads every tool call, result and token,
  and can act as you through a live token. It cannot mint a pairing code, an
  authorization code or a token: those are made on your server. SSH through
  the edge stays end to end ([security.md](security.md#edge-remote-access)).
- **A connector is bound to its member and minting device.** Every request
  re-checks both under the current `edge-access` policy, so revoking the
  device, removing the member or switching to `approved-devices` covers the
  connector.
- **The 8 tools are the whole surface.** A connector cannot open a terminal,
  read files or git, change settings, manage members or call any other
  control method. Run history is text other agents wrote, and the server's
  instructions and the tool's description mark it as untrusted; ChatGPT and
  Claude still ask before each write.
- **Codes live in memory.** Pairing codes (10 minutes) and authorization codes
  (60 seconds) are single use, kept as hashes, and never written to disk.
  Wrong pairing codes are limited to 10 a minute per server.
- **Only ChatGPT and Claude can connect.** A client identifies itself with a
  `client_id` that is the URL of its metadata document
  ([OpenAI: auth](https://developers.openai.com/plugins/build/auth),
  [Anthropic: authentication](https://claude.com/docs/connectors/building/authentication)).
  The server accepts only `https` URLs on `chatgpt.com` and `claude.ai`, and
  fetches the document on each authorization: no redirects, 5 seconds, 16 KiB.
  The redirect must be `https` on the same host and listed in the document,
  so an authorization code goes only to ChatGPT or Claude. The consent page
  names the client and its host; the form it posts is signed with
  `<data-dir>/connector/hmac.key`, so the server keeps no session.
