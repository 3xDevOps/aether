# Claude connector listing

For the project's maintainers. Claude's connector directory is Anthropic's
catalog of MCP servers that Claude users add with one click. Aether's listing
points at the same URL as the [ChatGPT plugin](chatgpt-plugin.md),
`https://edge.onaether.dev/mcp`, the project edge's universal connector URL
([connectors.md](connectors.md#the-universal-url)). This page covers the
listing, Anthropic's requirements, testing and review.

Anthropic's sources, read 2026-10-26:
[building](https://claude.com/docs/connectors/building),
[authentication](https://claude.com/docs/connectors/building/authentication),
[submission](https://claude.com/docs/connectors/building/submission),
[pre-submission checklist](https://claude.com/docs/connectors/building/review-criteria),
[testing](https://claude.com/docs/connectors/building/testing),
[troubleshooting](https://claude.com/docs/connectors/building/troubleshooting),
[directory policy](https://support.claude.com/en/articles/13145358-anthropic-software-directory-policy).
Recheck them before submitting; the quotes below are what they said then.

## Requirements and where Aether stands

"Code" is origin/main at cabcf962; "live" is `curl` against
`edge.onaether.dev` on 2026-10-26.

| Requirement | Anthropic | Aether |
| --- | --- | --- |
| Transport | "Use Streamable HTTP, which the MCP specification defines for remote servers." ([building](https://claude.com/docs/connectors/building)) | Streamable HTTP at `/mcp` (live) |
| IPv4 | "Connectors are IPv4-only, so a hostname that only publishes `AAAA` records can't be reached" ([troubleshooting](https://claude.com/docs/connectors/building/troubleshooting)) | `edge.onaether.dev` has an A record (live) |
| 401 starts sign-in | "A `401` is required to start sign-in, and Claude ignores a `WWW-Authenticate` header on a `200` response" ([authentication](https://claude.com/docs/connectors/building/authentication)) | `401` with `WWW-Authenticate: Bearer resource_metadata="https://edge.onaether.dev/.well-known/oauth-protected-resource/mcp"` (live) |
| Resource | "the document's `resource` field must equal the URL as the user enters it in Claude, including any path component" ([authentication](https://claude.com/docs/connectors/building/authentication)) | `"resource":"https://edge.onaether.dev/mcp"` (live) |
| PKCE | "Your authorization server must support S256 PKCE." ([authentication](https://claude.com/docs/connectors/building/authentication)) | `code_challenge_methods_supported: ["S256"]` (live) |
| CIMD | "Claude selects CIMD only when your authorization server metadata advertises both `"client_id_metadata_document_supported": true` and `"none"` in `token_endpoint_auth_methods_supported`" ([authentication](https://claude.com/docs/connectors/building/authentication)) | Both advertised, no registration endpoint (live). The listing's auth type is `oauth_cimd` |
| Callback | "register exactly this redirect URI: `https://claude.ai/api/mcp/auth_callback`" ([authentication](https://claude.com/docs/connectors/building/authentication)) | Taken from Claude's client metadata document, which lists it; only `claude.ai` and `chatgpt.com` clients are accepted (code: `internal/connector/authorize.go`) |
| Claude Code | "Claude Code declares `http://localhost/callback` and `http://127.0.0.1/callback` in its Client ID Metadata Document" ([authentication](https://claude.com/docs/connectors/building/authentication)) | **Refused**: the server requires an https redirect on the client's own host. Claude Code cannot connect. See [Claude Code](#claude-code) |
| Refresh | "Rotate refresh tokens for public-client connections"; invalid ones return `invalid_grant` ([authentication](https://claude.com/docs/connectors/building/authentication)) | Rotated on each use; reusing a rotated token a minute or more later revokes the connector; `invalid_grant` (code, live) |
| Timeouts | "up to 10 seconds for a response from your OAuth discovery, registration, and token endpoints" ([authentication](https://claude.com/docs/connectors/building/authentication)) | The server bounds token handling to 5 seconds; the edge relays it (code) |
| Egress | "Anthropic's outbound traffic to your server originates from `160.79.104.0/21`." ([authentication](https://claude.com/docs/connectors/building/authentication)) | No allowlist on the edge. Strict `Origin` checks reject Anthropic ([testing](https://claude.com/docs/connectors/building/testing)); the server's MCP handler uses Go's cross-origin protection, so confirm live calls pass |
| Tool names | "Tool names must be 64 characters or fewer." ([checklist](https://claude.com/docs/connectors/building/review-criteria)) | Longest is 23 (code) |
| Tool hints | "Every tool must include a `title` and the applicable hint: `readOnlyHint: true` for read-only tools, and `destructiveHint: true` for tools that modify or delete data." ([checklist](https://claude.com/docs/connectors/building/review-criteria)) | Every tool has a title and all three hints; `TestConnectorToolList` checks them ([Tool hints](chatgpt-plugin.md#tool-hints)) |
| No catch-all | "Don't ship a catch-all `api_request` tool with a `method` parameter." ([checklist](https://claude.com/docs/connectors/building/review-criteria)) | One tool per operation (code) |
| Errors | "generic errors such as "Internal Server Error" or "Bad Request" with no detail fail review" ([checklist](https://claude.com/docs/connectors/building/review-criteria)) | Errors carry the server's message ([connectors.md](connectors.md#troubleshooting)) |
| No conversation data | "Don't query Claude's memory, chat history, conversation summaries, or user files" ([checklist](https://claude.com/docs/connectors/building/review-criteria)) | No tool asks for them (code) |
| Result size | "~150,000 characters" on claude.ai and Desktop ([building](https://claude.com/docs/connectors/building)) | Capped well below ([connectors.md](connectors.md#what-it-can-do)) |
| Privacy policy | The listing materials include a "privacy policy URL", entered in the Listing step ([submission](https://claude.com/docs/connectors/building/submission)) | Draft only: [legal/privacy-connectors.md](legal/privacy-connectors.md) |
| Public docs | "required by your publish date. A blog post or help-center article is sufficient" ([checklist](https://claude.com/docs/connectors/building/review-criteria)) | [connectors.md](connectors.md) on GitHub |
| Test account | "Test credentials: required, and they must be for a fully populated account" ([checklist](https://claude.com/docs/connectors/building/review-criteria)) | Built, not deployed: the edge's reviewer account; see [Reviewer account](#reviewer-account) |
| Try it first | "Exercise every tool through the MCP Inspector and as a custom connector in Claude before you submit." ([checklist](https://claude.com/docs/connectors/building/review-criteria)) | See [Test before submitting](#test-before-submitting) |

## The listing

`packaging/claude-connector/listing.json` holds every field the portal
asks for, by step of the
[submission flow](https://claude.com/docs/connectors/building/submission):

| Field | Portal step | Limit or rule |
| --- | --- | --- |
| `server_url` | 1. Connection | **Universal URL**: "every user connects to the same URL". Must equal the ChatGPT plugin's URL |
| `auth_type`, `callback_url` | 6. Authentication | `oauth_cimd`: OAuth sign-in with no client ID or secret entered, since Claude identifies itself with its client metadata document. `callback_url` is the redirect Claude uses; nothing to enter |
| `name`, `one_liner`, `description` | 3. Listing | "server name up to 100 characters, one-liner up to 200 characters, description up to 2,000 characters" |
| `categories` | 3. Listing | "one to five categories". Anthropic publishes no list; pick the closest to `Developer tools` in the portal |
| `documentation_url`, `privacy_policy_url`, `support_contact` | 3. Listing | https URLs; support may be an email |
| `icon` | 3. Listing | Reuses `web/public/icons/icon-512.png`. Anthropic states no size |
| `slug` | 3. Listing | "The slug is permanent once published." The directory page is `https://claude.ai/directory/connectors/<slug>` |
| `use_cases`, `prerequisites`, `access` | 4. Use cases | Use cases, "what users need before they can connect", and whether it "reads data, writes data, or both". The policy asks for "at least three working examples" |
| `example_prompts` | 4. Use cases | At least three |
| `company` | 5. Company | "Company name and website, plus a primary contact" |
| `data_handling` | 7. Data handling | API ownership, personal health data, sponsored content. The directory accepts no connector that moves money or generates media |
| `compliance_acknowledgments` | 9. Compliance | "Seven policy acknowledgments ... All seven are required." The owner accepts each in the portal; the file only lists them |

Step 2, Tools, syncs from the server, and step 8, Test & launch, takes the
reviewer account ([Reviewer account](#reviewer-account)); neither has a field
in the file. The scope sentences in `description` hold only once #591, which
checks every connector call against its granted scopes, is deployed.

Check it and stage it for the portal:

```sh
make claude-connector    # writes dist/aether-claude-connector/listing.json and icon.png
```

`scripts/claude-connector.sh` enforces the limits above, requires the seven
acknowledgments by name, refuses credential fields and an output directory
that is the package itself, and prints a warning for each `PLACEHOLDER:`
value. `make test-scripts` runs `scripts/claude-connector-test.sh`, which
checks the shipped listing passes and broken ones are refused. The portal is a form, so
copy each field in by hand.

## Claude Code

Claude Code connects with its own client,
`https://claude.ai/oauth/claude-code-client-metadata`, whose only redirects
are `http://localhost/callback` and `http://127.0.0.1/callback` with any
port. The server refuses both: it accepts only an https redirect on the
client id's host. The directory listing works in Claude on the web, desktop
and mobile; Claude Code users get the consent page error `The redirect_uri
"http://localhost:<port>/callback" is not an https URL on claude.ai that
https://claude.ai/oauth/claude-code-client-metadata lists.` Allowing it means
accepting loopback redirects (RFC 8252) for that one client id; that is a
server change, not part of the listing.

## Test before submitting

There is no staging: "Custom connectors use the exact same runtime as
directory connectors"
([testing](https://claude.com/docs/connectors/building/testing)).

**In Claude.** As a maintainer on the demo server:

1. In claude.ai, **Customize → Connectors → Add custom connector**, URL
   `https://edge.onaether.dev/mcp`, no advanced settings.
2. **Connect**, sign in at the edge, pick the demo server, **Allow** with
   both scopes.
3. Check the connector shows **Connected** and all 26 tools appear under
   **Tool permissions**, read tools without a confirmation prompt.
4. Run every [example prompt](#the-listing) and each tool at least once
   through **+ → Connectors**. Revoke the connector afterwards (`aether mcp
   revoke <id>`).

**MCP Inspector.** The Inspector's own OAuth client is not `claude.ai` or
`chatgpt.com`, so the server refuses it. Check discovery without a token,
then call tools with a token from a sign-in done by hand as Claude. The
token acts as you with the scopes you allow, so allow only the ones under
test, run this on a machine no one else uses (`ps` shows the token in the
Inspector's arguments), and revoke the connector when done:

```sh
url=https://edge.onaether.dev/mcp
curl -si -X POST "$url" -H 'content-type: application/json' -d '{}' | grep -i www-authenticate
curl -s https://edge.onaether.dev/.well-known/oauth-protected-resource/mcp
curl -s https://edge.onaether.dev/.well-known/oauth-authorization-server/mcp

verifier=$(openssl rand -base64 48 | tr -d '=+/\n' | cut -c1-64)
challenge=$(printf %s "$verifier" | openssl dgst -sha256 -binary | openssl base64 | tr '+/' '-_' | tr -d '=\n')
client=https://claude.ai/oauth/mcp-oauth-client-metadata
redirect=https://claude.ai/api/mcp/auth_callback
echo "$url/authorize?response_type=code&client_id=$client&redirect_uri=$redirect&code_challenge=$challenge&code_challenge_method=S256&state=inspector&resource=$url"
```

Open the printed address, sign in and allow. Claude's callback page then
shows an error, as it did not start this sign-in; copy the `code` parameter
from its address bar within a minute and exchange it. The tokens go to a
`mktemp` file outside the checkout, deleted when the shell exits:

```sh
tokens=$(mktemp)
trap 'rm -f "$tokens"' EXIT
curl -s "$url/token" -d grant_type=authorization_code -d "code=<code>" \
  --data-urlencode "client_id=$client" --data-urlencode "redirect_uri=$redirect" \
  -d "code_verifier=$verifier" --data-urlencode "resource=$url" >"$tokens"
token=$(jq -r .access_token "$tokens")
inspector=@modelcontextprotocol/inspector@2.12.0
npx "$inspector" --cli "$url" --transport http \
  --header "Authorization: Bearer $token" --method tools/list
npx "$inspector" --cli "$url" --transport http \
  --header "Authorization: Bearer $token" --method tools/call --tool-name aether_needs_attention
```

Then check rotation, which ends the connector:

```sh
old=$(jq -r .refresh_token "$tokens")
curl -s "$url/token" -d grant_type=refresh_token --data-urlencode "refresh_token=$old" \
  --data-urlencode "client_id=$client" --data-urlencode "resource=$url" | jq -r .error
# prints null: a new pair. Wait more than a minute, then send the old token again:
curl -s "$url/token" -d grant_type=refresh_token --data-urlencode "refresh_token=$old" \
  --data-urlencode "client_id=$client" --data-urlencode "resource=$url"
# invalid_grant: this refresh token was already used, so its connector was revoked
rm -f "$tokens"
```

Checklist:

- [ ] Unauthenticated `POST /mcp` answers 401 with `resource_metadata`.
- [ ] Both metadata documents match the [requirements](#requirements-and-where-aether-stands).
- [ ] `tools/list` returns every tool with a `title` and `readOnlyHint`,
      `destructiveHint` and `openWorldHint`.
- [ ] Each read tool returns a result with no generic error.
- [ ] Each write and control tool, called on a throwaway demo run, does what
      its description says.
- [ ] `aether mcp revoke <id>` on a second connector makes its next call
      answer 401.
- [ ] Last: reusing a rotated refresh token a minute later answers
      `invalid_grant` and `aether mcp` no longer lists the connector.

## Reviewer account

Anthropic wants "a fully populated account rather than an empty shell" and
"step-by-step setup instructions for someone unfamiliar with your service"
([testing](https://claude.com/docs/connectors/building/testing)), entered in
the portal's Test & launch step, which "only reviewers see". Use the demo
server, sample data and edge reviewer account in
[chatgpt-plugin.md](chatgpt-plugin.md#reviewer-setup): a GitHub account
fails, since GitHub emails a code to a password-only account on each new
browser. An edge has one reviewer account, so a review running beside
OpenAI's shares it; give each new review a fresh password.

## Submit in the portal

"Anyone on a paid Claude plan can submit through the portal"
([submission](https://claude.com/docs/connectors/building/submission)). The
plan must be "Pro, Max, Team, or Enterprise"; "On Team and Enterprise, an
Owner can submit", or a member an Owner granted the **Directory** permission
([publish](https://claude.com/docs/directory/publish#confirm-you-can-submit-to-the-directory)).

At [claude.ai/directory/manage](https://claude.ai/directory/manage), select
**Submit new**, choose **MCP connector**, and fill the steps from
`dist/aether-claude-connector/listing.json`:

1. **Connection**: paste `server_url`. Leave **Users connect to different
   URLs** unselected: Aether is a **Universal URL** listing.
2. **Tools**: check all tools synced with titles and annotations.
3. **Listing**: copy the listing fields and upload `icon.png`.
4. **Use cases**: copy `use_cases`, `prerequisites`, `access` and
   `example_prompts`.
5. **Company**: copy `company`.
6. **Authentication**: under **Authentication requirement** choose
   **Always required**; under **Authentication methods** use the **OAuth
   sign-in** card (**Add OAuth sign-in** if there is none). Do not choose
   **Anthropic-held client credentials** or enter a client ID or secret:
   Claude identifies itself with its client metadata document (CIMD).
7. **Data handling**: copy `data_handling`.
8. **Test & launch**: enter the reviewer credentials and steps, and confirm
   you ran every tool.
9. **Compliance**: accept the seven acknowledgments.
10. **Review and submit**: read the quality warnings, then submit.

Status moves through Draft, In review, Changes requested or Not approved,
Approved and Published
([status](https://claude.com/docs/directory/submission-status)); write to
`mcp-review@anthropic.com` to escalate.

Renaming a published listing "requires re-review"
([managing](https://claude.com/docs/connectors/building/managing-your-listing)),
and the policy requires telling Anthropic about "material changes" to tools,
data practices or endpoints.

## External prerequisites

Nothing here is submitted or approved. "Merged" is origin/main; "deployed" is
what `edge.onaether.dev` serves.

| Item | Merged | Deployed | Submitted | Approved |
| --- | --- | --- | --- | --- |
| Universal `/mcp` URL with CIMD, S256, `iss` | Yes (#554) | Yes, verified with `curl` 2026-10-26 | No | No |
| Sign-in hardening: the sign-in assertion bound to the browser and kept out of URLs and logs | No: #590 open | No | n/a | n/a |
| Per-method scope enforcement: every connector call checked against its granted scopes; the listing's scope sentences depend on it | No: #591 open | No | n/a | n/a |
| Listing package and check (this page) | This change | n/a | No | No |
| Privacy policy hosted at a public URL | Draft in [legal/](legal/privacy-connectors.md) | No | No | No |
| Terms hosted at a public URL | Draft in [legal/](legal/terms-connectors.md) | No | No | No |
| Demo server with sample data | n/a | No | n/a | n/a |
| Reviewer account that needs no email code | Built in Aether; not enabled on edge.onaether.dev: `aether-edge serve --reviewer-account-file` | No | No | No |
| Claude Code loopback redirects | No | No | n/a | n/a |
| Live test from claude.ai through Anthropic's egress `160.79.104.0/21` | n/a | Not run | n/a | n/a |
| Owner accepts the seven acknowledgments | n/a | n/a | No | No |
