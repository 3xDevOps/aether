# Aether connector for ChatGPT and Claude: privacy policy

> **DRAFT. Not published and not in effect.** The project owner and legal
> counsel must approve this text before it is hosted at the URL entered as
> `privacyPolicyURL` ([chatgpt-plugin.md](../chatgpt-plugin.md)) and as the
> privacy policy URL of the Claude listing
> ([claude-connector.md](../claude-connector.md)). The facts below describe
> origin/main at cabcf962; recheck each against the code at the commit that
> is deployed to `edge.onaether.dev` before publishing. Bracketed items are
> decisions for the owner.

Effective: [date of publication]

## What this policy covers

The Aether connector lets ChatGPT, from OpenAI, and Claude, from Anthropic,
read and act on the coding agents running on an Aether server you are a
member of. It is reached at `https://edge.onaether.dev/mcp`. This policy
covers what the connector collects, returns, stores and logs. The Aether
Android app, the dashboard and the edge's other functions are covered by
the [Aether privacy policy](../privacy.md).

Aether is self-hosted. Three parties handle connector data:

| Party | Role |
| --- | --- |
| 3xDevOps, publisher of Aether, operating `edge.onaether.dev` and `auth.onaether.dev` (the project edge) | Signs you in with GitHub and relays each connector request to your server |
| The administrator of the Aether server you connect | Runs the server that executes every tool call and holds your runs. 3xDevOps does not operate it unless it is your administrator |
| OpenAI (ChatGPT) or Anthropic (Claude), whichever app you connect | Sends tool calls and receives tool results inside your conversation, under its own privacy policy |

Contact for this policy: [team@onaether.dev]. Issues:
<https://github.com/3xDevOps/Aether/issues>.

## What the connector receives from ChatGPT or Claude

- Tool arguments the assistant chooses to send: workspace, run and swarm
  ids, task and message text, answers to agent questions, search words,
  filters such as a time range, and optional idempotency keys.
- During sign-in: the app's client id URL (only `https://chatgpt.com/...`
  and `https://claude.ai/...` are accepted), its display name and redirect
  address, and the PKCE challenge.
- Bearer tokens on every request.

The connector does not request or read your conversation history, memory,
files, location, or any OpenAI or Anthropic user id.

## What it returns to ChatGPT or Claude

- Ids, states, times, workspace and agent names, branch names, file paths,
  counts and links to the Aether dashboard.
- Text written by you, your teammates, agents and GitHub users: run titles,
  tasks, reports, agent conversations and terminal output, diffs, commit
  subjects and git author names, pull request reviews and comments with the
  GitHub usernames that wrote them, swarm tasks, questions and messages, and
  team memory notes with their author's name.

No tool returns member email addresses or logins. Every member of an Aether
server can read every run on it, so results can include your teammates'
work. Once a result reaches OpenAI or Anthropic, its handling is governed
by their policies, not this one.

## What is stored and logged

| Where | What | Kept |
| --- | --- | --- |
| Project edge, nginx access log | Per request: client IP address, time, request line (method, path with the server id, and query string), status, bytes and user agent. During sign-in the query strings include the OAuth authorization request parameters, GitHub's one-time authorization code, and the edge's signed sign-in assertion naming your edge account and the server you chose. No request bodies | 14 days ([confirm the deployed logrotate setting]) [TODO: update when the universal sign-in change (fix/universal-mcp-signin) moves those parameters out of URLs] |
| Project edge, journal | Requests the edge refuses before relaying, with path and client IP; relay errors; failed TLS handshakes with client IP. No request bodies or tokens | [journald retention on the deployed edge] |
| Project edge, database | Your edge account (GitHub user id, login, verified primary email, display name), browser sessions (hashed, 30 days after last use), the servers you can reach, and one monthly total of relayed bytes | As described in [What an edge stores](../privacy.md#what-an-edge-stores) |
| Project edge, memory | Request and result bodies and tokens while relaying them. TLS ends at the edge so it can route the request | Not written to disk |
| Your Aether server, connectors table | Your member id, device, app client id and name, redirect host, granted scopes, created and last-used times, and SHA-256 hashes of the tokens | Until revoked, or your member or device is removed |
| Your Aether server, tokens | Access tokens valid 1 hour; refresh tokens valid 30 days and replaced on every use | Hashes only |
| Your Aether server, log | One line per write call: connector id, member id, method, target id and result. A line when a connector is authorized, created or revoked. Read calls are not logged | The server administrator's log retention |
| Your Aether server, audit history | Each call that changes something, made through a connector or the built-in assistant, and each connector created or revoked: time, member, which connector, method, target ids and outcome. Never tokens, message bodies or secrets. Read calls are not recorded. [TODO: confirm against the merged audit change (fix/connector-authz-audit)] | [retention, not yet decided] |
| Your Aether server, runs and messages | Runs launched and messages sent through the connector, marked `via ChatGPT` or `via Claude` | With the run, under the server's retention |

## Why

To sign you in, route each request to the server you chose, answer your
request and carry out the actions you confirmed, enforce rate limits and
access rules, and keep the edge running and secure. [Owner: commit to
whether 3xDevOps sells connector data, uses it for advertising, or uses it
to train models. The code does none of these.]

## Your choices

- Choose scopes on the consent page: reading is always granted; launching
  and messaging and stopping and approving are granted separately.
- Revoke a connector in the Aether app under **Settings → ChatGPT and
  Claude**, or with `aether mcp revoke <id>`. Removing it in ChatGPT or
  Claude alone does not revoke it on the server.
- Delete your edge account at `https://auth.onaether.dev/account`.
- Ask your server administrator for data held on your server, and
  [team@onaether.dev] for data held by the project edge.

## Children

The connector is for software developers and is not directed at children.
[Owner: confirm the minimum age stated in the terms.]

## Changes

Changes are published at this URL with a new effective date.
[Owner: decide whether material changes are also announced elsewhere.]
