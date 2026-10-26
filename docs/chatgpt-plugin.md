# ChatGPT plugin listing

For the project's maintainers. The ChatGPT plugin directory is OpenAI's
catalog of plugins that ChatGPT users add with one click. Aether's listing
points every user at one MCP endpoint, `https://edge.onaether.dev/mcp`, the
project edge's universal connector URL; ChatGPT signs in through OAuth and
reaches the user's own server through the edge. The tools are the connector's
([connectors.md](connectors.md)); this page covers packaging, submission and
review.

OpenAI's sources: [submission](https://developers.openai.com/plugins/deploy/submission),
[review requirements](https://developers.openai.com/plugins/deploy/app-review),
[guidelines](https://developers.openai.com/plugins/plugin-guidelines),
[authentication](https://developers.openai.com/plugins/build/auth).

## The package

`packaging/chatgpt-plugin/` holds the two files of an Agent Plugins package:

| File | Holds |
| --- | --- |
| `plugin.json` | Identity (`name: aether`), listing text and links under `extensions.com.openai.interface`, review test cases under `extensions.com.openai.review` |
| `mcp.json` | One server, `aether`: `streamable-http` at `https://edge.onaether.dev/mcp`, OAuth with `client.mode: cimd` |

OpenAI accepts `extensions` on a server in `mcp.json` although the published
`mcp.schema.json` does not list it; their auth reference requires it there.

Build the ZIP:

```sh
make chatgpt-plugin    # writes dist/aether-chatgpt-plugin.zip
```

`scripts/chatgpt-plugin.sh` checks OpenAI's limits (display name and
subtitle at most 30 characters, description 4000, at most 20 capabilities of
120, three starter prompts of 128, https URLs, five positive and three
negative test cases, no credentials, one server with no headers), copies
`web/public/icons/icon-maskable-512.png` and `icon-maskable-192.png` in as
`assets/logo.png` and `assets/composer-icon.png`, and refuses a ZIP of 100 MB
or more. `make test-scripts` runs `scripts/chatgpt-plugin-test.sh`, which
builds the real package and checks that broken ones are refused.

A value starting `PLACEHOLDER:` builds with a warning naming the field. The
portal accepts the ZIP as a draft, but review cannot start until each is a
real URL.

## External actions before submitting

| Action | Field or system |
| --- | --- |
| Publish a privacy policy page covering the plugin, using the [data inventory](#privacy-policy-data-inventory) below. [privacy.md](privacy.md) covers the Android app and what an edge stores, but not what tool results return | `privacyPolicyURL` |
| Publish terms of service | `termsOfServiceURL` |
| Record a walkthrough of the five positive test cases on the demo server | `review.demo_recording_url` |
| Serve the portal's challenge token as plain text at `https://edge.onaether.dev/.well-known/openai-apps-challenge` with `aether-edge serve --openai-challenge <token>` or `AETHER_EDGE_OPENAI_CHALLENGE` | aether-edge |
| Deploy an edge with protocol 3 to `edge.onaether.dev`. Once submitted, its scheme, host and port cannot change without OpenAI support | aether-edge |
| Complete business verification for 3xDevOps | OpenAI organization settings |
| Set up the demo server and reviewer account below | Project edge |
| Review needs the universal `/mcp` URL live on the project edge, signing a demo server's members in with GitHub and asking for no pairing code under `--edge-access account` (the universal-URL task) | aether-edge, `aether-server` |

## Submit in the portal

1. In [organization settings](https://platform.openai.com/settings/organization/general),
   complete business verification as 3xDevOps. Submitters need the owner role
   or **Apps Management Write**.
2. Open [Plugins](https://platform.openai.com/plugins), select **Upload new
   or existing plugin**, choose the verified developer identity, and upload
   `dist/aether-chatgpt-plugin.zip`.
3. In **Metadata & Skills**, wait for the checks, then fix any **Issues** in
   the package and upload again.
4. In **MCPs**, select `aether`, then **Connect**. Check the URL and that
   authentication is OAuth with CIMD. Set the token the portal shows as
   `AETHER_EDGE_OPENAI_CHALLENGE` (or `aether-edge serve --openai-challenge`)
   on the `edge.onaether.dev` edge, restart it, and verify the domain. Connect and sign in as the reviewer account.
5. Wait for **Scan Tools** to list the tools. Fix findings on the server,
   deploy, and select **Rescan**.
6. In **Review information → Review details**, check the imported test cases
   and enter the reviewer credentials and sign-in steps (never in the ZIP:
   it rejects `test_credentials` and `reviewer_instructions`). **Save
   details**.
7. **Submit for review** and accept the policy attestations. Feedback arrives
   by email.
8. Once approved, open the approved version and select **Publish plugin**.

Later tool changes need no new ZIP: OpenAI rescans the server daily, or select
**Rescan**. Listing text, icons and test cases need a new ZIP and review.

## Tool hints

OpenAI reviews each tool's three hints. `destructiveHint` is true when an
effect cannot be taken back, even if a later call can undo its consequences;
`openWorldHint` is true when an agent the tool starts or lets proceed can push
branches and open pull requests. The read tools are all read-only and not
destructive. They read the user's own server, and `aether_pr_status` also
reads GitHub.

| Tool | Read-only | Destructive | Open world | Why |
| --- | --- | --- | --- | --- |
| `aether_launch_run`, `aether_launch_swarm`, `aether_launch_template` | no | no | yes | Adds runs; their agents can push and open PRs |
| `aether_launch_options`, `aether_changes` | yes | no | no | Read the user's own server: accounts, usage, agents, templates and models; the change feed |
| `aether_message_run`, `aether_answer` | no | yes | yes | A sent message or answer cannot be taken back; the agent acts on it |
| `aether_pause_run` | no | no | no | Freezes the container; resume continues it |
| `aether_resume_run` | no | no | yes | The agent goes on working |
| `aether_mark_reviewed` | no | yes | no | `complete` closes the run |
| `aether_decide_approval` | no | yes | yes | An allowed action, such as a push, cannot be taken back |
| `aether_stop_run`, `aether_close_run`, `aether_cancel_swarm` | no | yes | no | Ends runs on the user's server |

### Tool list budget

`TestConnectorToolList`, with every scope granted, enforces:

1. The names, descriptions and compact JSON input schemas of all tools sum
   to at most 15000 bytes.
2. Each tool's name, description and schema sum to at most 3500 bytes.
3. There are at most 40 tools.

The whole `tools/list` JSON, with titles and hints, is measured but not
capped.

The evidence:

- No ChatGPT or Claude document sets a limit. One OpenAI forum report shows
  ChatGPT refusing a server with "All tools (including name, description,
  and input schema) must be less than 5000 tokens"
  ([OpenAI community](https://community.openai.com/t/token-limit-when-connecting-chatgpt-with-external-mcp/1371022),
  2026-01-05). It is unverified, and it may be a total or per tool. A live
  ChatGPT connection test settles which; 15000 bytes is about 4300 tokens at
  3.5 bytes per token, under 5000 even as a total.
- Anthropic: Claude's tool selection degrades past 30 to 50 tools
  ([tool search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool)).
- Every conversation that uses the connector pays for the list.

Measured with OpenAI's `o200k_base` tokenizer, offline. Update this when
tools change:

| Tools | Names, descriptions, schemas | Whole `tools/list` |
| --- | --- | --- |
| 29 | 11585 bytes, 2714 tokens | 16594 bytes, 3901 tokens |

To fit:

- Write one "Use this when" sentence per description.
- Give a property a description only where its name does not say it.
- Keep enums short.
- Fold a read that another tool already answers into that tool, as
  `aether_launch_options` carries templates and models.
- Merge only operations with the same scope and hints. Never merge a read
  with a write, or a destructive operation with one that is not: OpenAI wants
  separate tools for separate permissions and confirmations, and Anthropic's
  directory rejects a tool that mixes safe and unsafe actions.

`openai/toolInvocation` status strings are not set; they would add about 2500
bytes to every `tools/list`.

## Reviewer setup

Reviewers need an account that works at once, with no MFA, email or SMS
codes, magic links or private network.

- **Demo server.** An `aether-server` linked to the project edge with
  `--edge-access account`, so the reviewer's devices work without an admin
  approving each one ([edge.md](edge.md), [connectors.md](connectors.md)).
  Install and sign in Claude Code on it so test 4 can launch.
- **Reviewer GitHub account.** A dedicated account that signs in to the edge
  with a password only. Sign it in once by hand to clear GitHub's device
  verification and new-account prompts, invite it to the demo server as a
  collaborator, and enter its login and password in **Review details**.
  GitHub may later require 2FA of it; check the account before each review.
- **Sample data.** A workspace `aether-demo` from a public sample repository
  with: a finished run whose success awaits review, a run asking the agent
  question, a run that changed `README.md`, and runs spread over the past
  week. Create the runs as the reviewer's member, not a maintainer's:
  `aether_activity_summary` counts only the caller's own runs. Keep it for
  later reviews.
- **Scopes.** On the consent page the reviewer keeps **Launch and message**
  checked. Leave **Stop and approve** unchecked unless a case needs it.

## Test cases

`plugin.json` carries these; the portal imports them read-only.

| # | Prompt | tools_triggered | expected_behavior |
| --- | --- | --- | --- |
| 1 | What needs me in Aether? | `aether_needs_attention` | Lists the open question and the run awaiting review, each with its run title and the tool that answers it |
| 2 | What did my Aether agents do this week? | `aether_activity_summary` | Summarizes the week's runs with outcomes and last reports, in the reviewer's time zone |
| 3 | What did the run working on the README change? | `aether_list_runs`, `aether_run_changes` | Finds the README run and lists its commits and changed files |
| 4 | Launch a Claude Code run in the aether-demo workspace to add a Contributing section to README.md. | `aether_launch_options`, `aether_launch_run`, `aether_get_run` | After confirmation, starts one run and returns its id, state and the account it runs on |
| 5 | Tell the Contributing section run to also link CONTRIBUTING.md. | `aether_list_runs`, `aether_message_run` | After confirmation, delivers the message to that run |
| 6 | Which of my Claude subscriptions has the most usage left? | `aether_launch_options` | Lists the reviewer's accounts with each usage window's percentage used and reset time, and names the one with the most left |
| 7 | What changed in Aether since my last question? | `aether_changes` | Lists recent status changes and reports, oldest first, and says whether more remain |

| # | Prompt | Expected |
| --- | --- | --- |
| 1 | Delete the aether-demo repository. | No tool deletes repositories or workspaces; says so and calls no write tool |
| 2 | Approve every permission request my agents make from now on. | Approvals are once per request; explains that and grants nothing standing |
| 3 | Show me the runs on Acme Corp's Aether server. | Reaches only the connected server; does not claim other servers' data |

## Privacy policy data inventory

The facts a privacy policy for the plugin must state, from the code.

**Collected from ChatGPT.** Tool arguments: workspace, run and swarm ids, task
and message text, answers, template parameters, the member ids of agent
accounts to launch on or read, and optional idempotency keys. The OAuth exchange:
ChatGPT's client id URL (only `chatgpt.com` and `claude.ai` hosts are
accepted), its `client_name` and redirect host. Nothing in `_meta`, no
conversation or user ids from OpenAI, and no location is read or kept.

**Returned to ChatGPT.** Ids, states, times, workspace and agent names,
branches, file paths and dashboard links; and, under `untrusted`, text other
people and agents wrote: run titles, tasks and reports, agent conversations
and terminal output, diffs, commit subjects and git author names, pull
request reviews and comments with their GitHub usernames, swarm tasks,
questions and messages, template tasks, and team memory notes with their
author's name. `aether_launch_options` and launch receipts return the display
names and member ids of the agent accounts the user may launch on (their own
and those teammates shared with them), each account's agents and whether they
are signed in, and each usage window's label, percentage used and reset time;
never money, plan prices or credentials. A run or swarm's receipt names its
account's owner by id, and by name when the account is the user's or shared
with them. No tool returns member emails or logins. Every member can read every run on the
server, so results cover teammates' runs too. Sizes are capped
([connectors.md](connectors.md#what-it-can-do)).

**Purpose.** To answer the user's request and carry out the action they
confirmed.

**Recipients.** OpenAI, which receives tool results in the conversation; the
project edge, which relays the traffic; the user's own Aether server. The edge
ends TLS, so it handles request and result bodies and bearer tokens in
plaintext in memory while relaying.

**What is stored and logged.**

| Where | What | Kept |
| --- | --- | --- |
| Edge, nginx access log | One line per request: client IP, time, method, path with the server id, status, bytes, user agent. No body or token | `/var/log/nginx/access.log`, 14 days under Debian's default logrotate |
| Edge journal | A request the edge refuses before relaying, with its path and, from outside the trusted proxies, the client IP; an internal relay failure with its error; failed TLS handshakes with the client IP. No body or token | `MaxRetentionSec=` in journald, per [edge.md](edge.md) |
| Edge database | One edge-wide total of relayed bytes per month, with no server, member or request detail | Never deleted |
| Server, `connectors` table | Member, device, client id, client name, redirect host, scopes, created and last used times, SHA-256 hashes of the tokens | Until revoked, or the member or device is removed |
| Server, tokens | Access token valid 1 hour; refresh token 30 days, rotated on each use; reusing a rotated token revokes the connector | Hashes only |
| Server log | One line per write call: connector, member, method, target id, result. Lines when a connector is authorized, created or revoked. Read calls are not logged | Server operator's log retention |
| Server records | Runs and messages created through the plugin record `via ChatGPT` | With the run |

**Controls.** `aether mcp` lists a member's connectors and `aether mcp revoke
<id>` revokes one; the consent page grants `write` and `control` separately
([connectors.md](connectors.md)). Edge account deletion is in
[privacy.md](privacy.md#what-an-edge-stores).

## Who can use it

OpenAI decides plan availability; check its help center
([Apps in ChatGPT](https://help.openai.com/en/articles/11487775-apps-in-chatgpt),
[admin controls](https://help.openai.com/en/articles/11509118-admin-controls-security-and-compliance-for-plugins-and-apps))
before publishing. The directory is visible on every plan; full MCP support
with write actions is on Business, Enterprise and Edu; workspace admins choose
which plugins members may use, on by default in Business and off in
Enterprise. Every user also needs an Aether account on a server linked to the
project edge.
