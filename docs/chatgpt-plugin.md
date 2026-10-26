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
[authentication](https://developers.openai.com/plugins/build/auth),
[submission errors](https://developers.openai.com/plugins/deploy/submission-errors),
read 2026-10-26. The same URL is listed in Claude's directory
([claude-connector.md](claude-connector.md)).

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
or more. A test case that triggers a tool no connector registers yet builds
with a warning naming the tool. `make test-scripts` runs
`scripts/chatgpt-plugin-test.sh`, which builds the real package and checks
that broken ones are refused.

A value starting `PLACEHOLDER:` builds with a warning naming the field. The
portal accepts the ZIP as a draft, but review cannot start until each is a
real URL.

## External prerequisites

Nothing here is submitted or approved. "Merged" is origin/main; "deployed" is
what `edge.onaether.dev` serves.

| Item | Field or system | Merged | Deployed | Submitted | Approved |
| --- | --- | --- | --- | --- | --- |
| Edge with protocol 3 serving the universal `/mcp` URL: CIMD, S256, `iss`, `resource`. OpenAI: "To change an existing MCP server's URL, contact support; the current update flow does not support URL changes." ([submission](https://developers.openai.com/plugins/deploy/submission)) | aether-edge | Yes (#554) | Yes, verified with `curl` 2026-10-26 | No | No |
| Sign-in hardening: the sign-in assertion bound to the browser and kept out of URLs and logs | aether-edge, server | No: #590 open | No | n/a | n/a |
| Per-method scope enforcement: every connector call checked against its granted scopes. The listing's scope sentences are true only once it is deployed | Server | No: #591 open | No | n/a | n/a |
| `scopes_supported` in the protected resource metadata. OpenAI lists it under "Key fields you must populate" as an "optional list that helps ChatGPT explain the permissions it is going to ask the user for" ([authentication](https://developers.openai.com/plugins/build/auth)); the live document has only `resource` and `authorization_servers` | aether-edge | No: pending code change (edge metadata) | No | n/a | n/a |
| Challenge token served as plain text at `https://edge.onaether.dev/.well-known/openai-apps-challenge` with `aether-edge serve --openai-challenge <token>` or `AETHER_EDGE_OPENAI_CHALLENGE` | aether-edge | Yes | No: answers 404 (2026-10-26) | No | No |
| Business verification for 3xDevOps | OpenAI organization settings | n/a | n/a | No | No |
| Privacy policy at a public URL, from [legal/privacy-connectors.md](legal/privacy-connectors.md) | `privacyPolicyURL` | Draft | No | No | No |
| Terms of service at a public URL, from [legal/terms-connectors.md](legal/terms-connectors.md) | `termsOfServiceURL` | Draft | No | No | No |
| Demo server with sample data ([Reviewer setup](#reviewer-setup)) | Project edge | n/a | No | n/a | n/a |
| Reviewer account that needs no email code | Review details | No: needs an edge sign-in other than GitHub | No | No | No |
| Walkthrough video of the five positive test cases | `review.demo_recording_url` | n/a | No | No | No |
| Live test from chatgpt.com | n/a | n/a | Not run | n/a | n/a |

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
   deploy, and select **Rescan**. Enter each tool's hint justifications from
   [Tool hints](#tool-hints).
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

OpenAI requires "explicit `readOnlyHint`, `openWorldHint`, and
`destructiveHint` values and a justification for each value on every MCP
tool" ([submission errors](https://developers.openai.com/plugins/deploy/submission-errors)).
Claude requires a `title` and the applicable hint on every tool
([claude-connector.md](claude-connector.md#requirements-and-where-aether-stands)).
Every tool has a title and all three hints; `TestConnectorToolList` checks
each tool's triple. Enter the justifications below in the portal.

OpenAI's definitions ([app review](https://developers.openai.com/plugins/deploy/app-review)):
read-only "strictly fetches/looks up/lists/retrieves data and does not modify
anything"; destructive "can cause irreversible outcomes (deleting,
overwriting, sending messages or transactions you can't undo, revoking
access...)"; open world "accesses the public internet or open-ended external
entities". Here a tool is open world when an agent it starts or lets proceed
can push branches and open pull requests.

| Tool | Read-only | Destructive | Open world | Justification |
| --- | --- | --- | --- | --- |
| `aether_list_workspaces`, `aether_list_agents`, `aether_list_runs`, `aether_run_history`, `aether_needs_attention`, `aether_activity_summary`, `aether_list_swarms`, `aether_get_swarm`, `aether_run_changes`, `aether_overlaps`, `aether_memory_search`, `aether_launch_options`, `aether_changes`, `search`, `fetch` | yes | no | no | Reads records on the user's own Aether server and changes nothing |
| `aether_get_run` | yes | no | no | Reads one run on the user's server. It also reads the run's pull request state, which the server fetches from GitHub; open world `yes` is proposed |
| `aether_pr_status` | yes | no | no | Reads a run's pull request checks and reviews, which the server fetches from GitHub; open world `yes` is proposed |
| `aether_launch_run`, `aether_launch_swarm` | no | no | yes | Adds a run or swarm, which can be stopped; its agents can push branches and open pull requests |
| `aether_message_run`, `aether_answer` | no | yes | yes | A sent message or answer cannot be taken back, and the agent acts on it |
| `aether_pause_run` | no | no | no | Freezes the run's container; resume continues it |
| `aether_resume_run` | no | no | yes | The agent goes on working and can push |
| `aether_mark_reviewed` | no | yes | no | `complete` closes the run, which cannot be reopened; records state on the server only |
| `aether_decide_approval` | no | yes | yes | An allowed action, such as a push, cannot be taken back |
| `aether_stop_run`, `aether_close_run`, `aether_cancel_swarm` | no | yes | no | Ends runs on the user's server; cannot be undone |

Tools that later changes add need a row here before the next review.

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

OpenAI: "our review team must be able to log into a demo account with no
further configuration required ... do not feature MFA (including requiring
SMS codes, login through systems that require SMS, email or other
verification schemes)" ([app review](https://developers.openai.com/plugins/deploy/app-review)).

**Blocker.** The edge signs in with GitHub only. GitHub asks a password-only
account for a code sent by email on each new browser, and skips it only with
2FA or a passkey
([GitHub](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/verifying-new-devices-when-signing-in)).
Reviewers use new browsers, so a GitHub reviewer account fails either way.
A pre-authorized demo path with no sign-in would give anyone on the internet
a member that launches agents. The proposed fix is a reviewer sign-in on the
edge: off unless `aether-edge` is given a reviewer account file, a separate
identity provider that can never match a GitHub account, offered only the
one demo server, accepted only by a server that opts in, rate-limited,
logged and revocable. It is not built; until it is, review cannot pass.

The rest of the setup:

- **Demo server.** An `aether-server` linked to the project edge with
  `--edge-access account`, so the reviewer's devices work without an admin
  approving each one ([edge.md](edge.md), [connectors.md](connectors.md)).
  Install and sign in Claude Code on it so test 1 can launch, and give it
  GitHub access to the sample repository only.
- **Reviewer member.** Invite the reviewer identity as a collaborator. Enter
  its login, password and these steps in **Review details**, never in the
  ZIP. Rotate the password after each review.
- **Sample data.** A workspace `aether-demo` from a public sample repository
  with: two active runs that both edit `README.md`; a run with an open pull
  request whose checks have finished and that has one review comment; a run
  whose agent asks which licence to use; a finished run whose success awaits
  review; and runs spread over the past week. Create the runs as the
  reviewer's member: `aether_activity_summary` and `aether_list_runs` show
  the caller's own runs by default. Restore it before each review.
- **Scopes.** On the consent page the reviewer keeps **Launch and message**
  checked. Leave **Stop and approve** unchecked unless a case needs it.

## Test cases

`plugin.json` carries these; the portal imports them read-only. They follow
a run from launch to acting on it.

| # | Step | Prompt | tools_triggered | expected_behavior |
| --- | --- | --- | --- | --- |
| 1 | Launch | Launch a Claude Code run in the aether-demo workspace to add a Contributing section to README.md. | `aether_launch_options`, `aether_launch_run` | After confirmation, starts one run and returns its id, state and dashboard link |
| 2 | Track | What changed in my Aether runs since we last checked? | `aether_changes` | Lists state changes, new reports and messages, and pull request changes since the previous cursor |
| 3 | Coordinate | Which runs touch the same files as the Contributing section run? | `aether_list_runs`, `aether_overlaps` | Names the other run editing `README.md` and the shared file |
| 4 | Review and CI | Is CI green on the README run's pull request, and what did reviewers say? | `aether_list_runs`, `aether_pr_status` | Counts of passed, pending and failing checks, each failing check's name and result, the review state and the comment, with the pull request link |
| 5 | Act | What needs me in Aether? Answer the licence question with MIT. | `aether_needs_attention`, `aether_answer` | Lists the question and the run awaiting review; after confirmation, answers with MIT |

| # | Prompt | Expected |
| --- | --- | --- |
| 1 | Delete the aether-demo repository. | No tool deletes repositories or workspaces; says so and calls no write tool |
| 2 | Approve every permission request my agents make from now on. | Approvals are once per request; explains that and grants nothing standing |
| 3 | Show me the runs on Acme Corp's Aether server. | Reaches only the connected server; does not claim other servers' data |

Example prompts for the video, beyond the test cases: the three
`defaultPrompt` entries and the list in
[connectors.md](connectors.md#what-it-can-do). Record from a fresh browser
signing in as the reviewer, and show a confirmation prompt before each write.

## Privacy policy data inventory

The facts a privacy policy for the connector must state, from the code. They
cover both listings, ChatGPT and Claude, since both reach the same URL.
OpenAI requires "the categories of personal data collected, the purposes of
use, the categories of recipients, data retention timelines"
([guidelines](https://developers.openai.com/plugins/plugin-guidelines)).
[legal/privacy-connectors.md](legal/privacy-connectors.md) drafts the policy
from them.

**Collected from ChatGPT or Claude.** Tool arguments: workspace, run and swarm
ids, task and message text, answers, template parameters, the member ids of
agent accounts to launch on or read, and optional idempotency keys. The OAuth
exchange: the client id URL (only `chatgpt.com` and `claude.ai` hosts are
accepted), its `client_name` and redirect host. Nothing in `_meta`, no
conversation, memory or user ids from OpenAI or Anthropic, and no location is
read or kept.

**Returned to ChatGPT or Claude.** Ids, states, times, workspace and agent names,
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

**Recipients.** OpenAI or Anthropic, whichever app the user connected, which
receives tool results in the conversation; the project edge, which relays the
traffic; the user's own Aether server. The edge
ends TLS, so it handles request and result bodies and bearer tokens in
plaintext in memory while relaying.

**What is stored and logged.**

| Where | What | Kept |
| --- | --- | --- |
| Edge, nginx access log | One line per request: client IP, time, request line (method, path with the server id, and query string), status, bytes, user agent. During sign-in the query strings include the OAuth authorization request parameters, GitHub's one-time authorization code, and the edge's signed sign-in assertion (edge account id, server id, client id, PKCE challenge; valid 5 minutes). No body | `/var/log/nginx/access.log`, 14 days under Debian's default logrotate |
| Edge journal | A request the edge refuses before relaying, with its path and, from outside the trusted proxies, the client IP; an internal relay failure with its error; failed TLS handshakes with the client IP. No body or token | `MaxRetentionSec=` in journald, per [edge.md](edge.md) |
| Edge database | The edge account (GitHub user id, login, verified primary email, display name), hashed browser sessions, the servers the account reached, and one edge-wide total of relayed bytes per month | Account until deleted; sessions 30 days after last use; byte totals never deleted ([privacy.md](privacy.md#what-an-edge-stores)) |
| Server, `connectors` table | Member, device, client id, client name, redirect host, scopes, created and last used times, SHA-256 hashes of the tokens | Until revoked, or the member or device is removed |
| Server, tokens | Access token valid 1 hour; refresh token 30 days, rotated on each use; reusing a rotated token revokes the connector | Hashes only |
| Server log | One line per write call: connector, member, method, target id, result. Lines when a connector is authorized, created or revoked. Read calls are not logged | Server operator's log retention |
| Server records | Runs and messages created through a connector record `via ChatGPT` or `via Claude` | With the run |
| Server, audit history | Not yet implemented (#592). Planned: each write call through a connector or the assistant, refused ones included; and, from any client including the dashboard and CLI, connector creation and revocation, account shares and revocations, role changes and accepted member merges. Each entry: time, member, how the call arrived, method, workspace, target id and outcome; no tokens, parameters or message bodies | Planned: each member's newest 10,000 entries |

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
