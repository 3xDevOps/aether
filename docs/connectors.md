# ChatGPT and Claude connectors

A connector is a custom MCP connector in ChatGPT or Claude that points at your
Aether server. Once you add one, you can list, launch, message and stop runs
from a chat in ChatGPT or Claude, the phone apps included, with no Aether app
on the phone. The connector acts as you: the Aether member who authorized it,
under the same role checks as `aether` and the dashboard.

```mermaid
flowchart LR
    A[ChatGPT or Claude] -->|HTTPS tool calls| E[Edge]
    E -->|forwards| S[Your Aether server]
```

The edge is a public relay your server connects out to ([edge.md](edge.md)). Your
server hosts the connector endpoint and signs you in.

## Prerequisites

- A server connected to an edge. ChatGPT and Claude reach the connector at
  `<edge-url>/mcp/<server id>`. A server reachable only on a tailnet cannot have
  one. To turn the edge on, see [edge.md](edge.md#turning-it-on).
- A server new enough to serve connectors. Otherwise the edge answers HTTP 501:
  `server <name> (<id>) runs aether-server <version>, which has no MCP endpoint; upgrade it: sudo aether-server update`.
- A linked CLI (`aether link`) on an approved device, a member SSH key or a
  tailnet connection. Under `edge-access account`, any registered device works.
- A ChatGPT or Claude plan that allows custom connectors (see below).

## Get the URL and a code

```sh
aether mcp
```

```
Add this server to ChatGPT or Claude as a custom connector:
  URL   https://edge.onaether.dev/mcp/<server id>
  Code  ABCD-EFGH   one use, expires in 10 minutes; paste it on the page that opens when you connect
Connected:
  con-7k2m9q4xbd  ChatGPT (chatgpt.com)  scopes read,write  added 2026-10-18  last used 2h ago
Remove one: aether mcp revoke <id>
```

The URL is the same for both apps. On the server, `sudo aether-server edge
status` also prints it. The code works once, within 10 minutes. Running
`aether mcp` again replaces a code you have not used.

## Add it in ChatGPT

Custom MCP servers are added on the web only, at chatgpt.com
([OpenAI: custom MCP servers](https://developers.openai.com/api/docs/guides/custom-mcp-server)):

1. Open [ChatGPT Plugins](https://chatgpt.com/plugins).
2. Select the plus button, then **Add custom MCP server**.
3. Paste the URL from `aether mcp`.
4. Select **Create as a plugin**, then install it.
5. A page from your server opens: **Connect ChatGPT (chatgpt.com) to
   <server>**. Paste the code, choose its [scopes](#scopes) and select
   **Allow**.

As of October 2026, OpenAI says write tools need ChatGPT Business or
Enterprise/Edu. Pro gets the read tools only
([OpenAI help center](https://help.openai.com/en/articles/12584461)). The
plugin then works in ChatGPT on the web, desktop and mobile.

## Add it in Claude

On claude.ai
([Anthropic: add a custom connector](https://claude.com/docs/connectors/custom/add-unlisted)):

1. Free, Pro and Max: open **Customize → Connectors**, select **Add custom
   connector**, and paste the URL from `aether mcp`. Free allows one custom
   connector.
2. Team and Enterprise: an Owner opens **Organization settings → Connectors**,
   selects **Add**, **Custom**, then **Web**, and pastes the URL. Each member
   then selects **Connect** under **Customize → Connectors**.
3. Select **Connect**. A page from your server opens: **Connect Claude
   (claude.ai) to <server>**. Paste the code, choose its [scopes](#scopes)
   and select **Allow**.

The connector then works in Claude on the web, desktop and mobile.

## Scopes

The page you paste the code on also sets the connector's scopes. ChatGPT and
Claude do not choose them; the server ignores any scope they request other
than `offline_access`.

| Scope | On the page | Grants |
| --- | --- | --- |
| `read` | Always granted | Every read-only tool |
| `write` | **Launch and message**, checked by default | Launching runs and swarms, messaging runs, pausing and resuming them, answering agents' questions, marking runs reviewed |
| `control` | **Approve, stop and close**, unchecked by default | Deciding permission and approval requests, stopping and closing runs, cancelling swarms |

Every member can read every run on the server, so a connector can read all of
them. A connector lists only the tools of its scopes, and `aether mcp` shows
each connector's scopes. To change them, revoke the connector and connect
again. Connectors added before scopes existed have all three.

## What it can do

The connector has eight tools and no others:

| Tools | What they do | Scope | Asks you first? |
| --- | --- | --- | --- |
| `aether_list_workspaces`, `aether_list_agents`, `aether_list_runs`, `aether_get_run`, `aether_run_history` | List workspaces, agents (installed, signed in, Enhanced support) and runs. Show one run. Read a page of an Enhanced run's conversation | `read` | No |
| `aether_launch_run` | Starts a run with a workspace, agent and task, and optionally a mode and model | `write` | Yes |
| `aether_message_run` | Sends a message to a run's agent | `write` | Yes |
| `aether_stop_run` | Stops a run | `control` | Yes |

ChatGPT and Claude ask before each tool that is not read-only. ChatGPT asks by
default and can remember your answer for a conversation. Claude offers **Allow
once** and **Always allow** per tool. Aether shows no confirmation card of its
own, and the [assistant's auto-approval](assistant.md#auto-approval) does not
apply. Your role still decides: a viewer's launch is refused, as it is in
`aether`. A connector's calls are judged by the device its code was minted on:
a method that needs an approved device refuses a connector minted on a
registered one.

Each connector makes at most 30 write calls a minute, enough for a swarm of
parallel launches. Past that, the call fails with
`connector <id> is limited to 30 write calls a minute and used them; retry in <seconds>, at <time>`.

## Attribution

A run launched through a connector shows **Launched via ChatGPT** (or Claude)
in its header, a run a swarm created through one does too, and a message sent
through one shows `<member> via ChatGPT` as its author. The server logs one
line per write call:

```
connector: write call connector=con-7k2m9q4xbd member=mem-... method=run.launch target=ws-... result=ok
```

`result` is `ok` or the call's error. Audit records, such as a member merge's
`confirmed_by`, name the connector: `ChatGPT connector con-7k2m9q4xbd of member mem-...`.

## Revoke a connector

```sh
aether mcp revoke con-7k2m9q4xbd
```

The connector's next call fails, and the app asks you to connect again with a
new code. You can revoke your own connectors, and an admin can revoke anyone's.
Removing the connector in ChatGPT or Claude does not revoke it on the server.
Revoke it here too.

A connector also stops working at its next request when:

- the device it was minted on is revoked (`aether device revoke <device id>`)
- its member is removed
- the server runs under `edge-access approved-devices` and that device is not
  approved
- its refresh token goes unused for 30 days (`aether mcp` then no longer lists
  it)
- the edge moved to another origin, which changes the connector URL

A connector whose code was minted over a member SSH key or a tailnet
connection has no device, so revoking a device never cuts it off. Revoke it
with `aether mcp revoke`.

## Troubleshooting

`aether mcp` prints errors as `aether: rpc error <code>: <message>`.

| Message | Fix |
| --- | --- |
| `connector.code: this server has no edge (edge-url is empty), and ChatGPT and Claude reach a connector only through one; an admin turns it on with: sudo aether-server config set edge-url https://edge.onaether.dev, then sudo systemctl restart aether-server` | Turn the edge on |
| `connector.code: this server is not connected to its edge at <edge-url>; an admin sees why with: sudo aether-server edge status` | Fix the edge connection |
| `connector.code needs an approved device, a member SSH key or a tailnet connection; this connection signed in with device "<label>", which is registered` | An admin approves your device, or run `aether mcp` from an approved one |
| `method not found: connector.code; the server is older than this CLI, and an admin updates it with aether server update` | Update the server |
| `connector.revoke: no connector <id>; aether mcp lists yours` | The id is wrong or already revoked |

On the page that opens when you connect:

| Message | Cause |
| --- | --- |
| `That code is wrong, expired or already used. Run aether mcp for a new one and paste it here.` | The code is not live. A server restart also forgets codes |
| `10 wrong codes came from your address in the last minute. Wait a minute, then paste the code again.` | 10 wrong codes in a minute from your address (your IPv6 /64). Other addresses are not affected |
| `Aether connectors work with ChatGPT and Claude; <host> is not one of them.` | Only `chatgpt.com` and `claude.ai` can connect |

If an app asks you to sign in again, the connector was revoked. Run `aether mcp`
for a new code.

## Security model

The connector acts as you through the tools of its scopes and nothing else. The
edge decrypts connector traffic, because ChatGPT and Claude need a public
certificate, so a compromised edge can read tool calls and act as you through a
live token. [security.md](security.md#chatgpt-and-claude-connectors) lists what
each token allows and what cuts a connector off.
