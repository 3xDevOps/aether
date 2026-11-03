# Assistant Sign-In and Standing Coordination: Proposal

Status: proposal, no code. Two separate changes; each ships on its own.

## Recommendation

1. **Sign-in: keep the assistant's own sign-in, and make it one click plus a
   pasted code.** Reusing the environment's login is unsafe in every form we
   checked. Instead, the panel starts the sign-in for you when you first open
   it, shows the vendor link as a button and the code as a field, and then
   confirms with the vendor whether the assistant's account is the same one
   your environment's login names. You sign in once per server, and the server
   keeps that sign-in alive by refreshing it on every start.
2. **Coordination: add a deterministic `relay_to` action to watches before any
   assistant-run job.** "Watch these PRs/sessions, tell session B when A
   finishes, notify me" needs no model: the watch evaluator, the wait delivery
   path and push already do it. Expose it through the existing `aether_watch`
   tool instead of adding a new one. Defer LLM-run standing jobs until there is
   a task that needs judgement. If you build them, use the envelope in
   [Phase 2c](#phase-2c-assistant-standing-jobs-deferred).

| Phase | What | Size |
| --- | --- | --- |
| 1a | Sign-in starts on first open; link button and code field in the panel | S, 1 PR |
| 1b | "Same account as your sessions" check after sign-in | S, 1 PR |
| 1c | Optional: the server runs the OAuth exchange itself, with no terminal or container | M, 1 PR + vendor spike |
| 2a | `relay_to` on watches and `aether_watch` | M, 1–2 PRs |
| 2b | Relay deliveries shown in Settings → Watches and `aether_watches` | S, folds into 2a |
| 2c | Assistant standing jobs (deferred) | L |

## Part 1: assistant sign-in

### What is true today

- The assistant's sign-in lives in `<data>/assistant/<member>/home`. On every
  start the server refreshes it at the vendor, writes the rotated tokens back,
  and pins the account. Claude Code pins `account=<uuid>;organization=<uuid>`
  from `api.anthropic.com/api/oauth/profile`; Codex pins the
  `chatgpt_account_id` claim. The pin is stored in
  `<data>/assistant/<member>/signin/<agent>.json`, outside every container
  (`internal/scheduler/assistant_signin.go`, `verifyAssistantSignIn`).
- The server holds **no** verified credential for the member's sessions.
  Sessions use `~/.claude/.credentials.json` and `~/.codex/auth.json` in the
  member home, and every session and the environment mount that home
  read-write. An account share mounts those files read-write into the
  recipient's sessions (`internal/scheduler/environment.go`, `loginMounts`).
  No server code refreshes a session login or asks the vendor which account it
  belongs to. `internal/quota` reads the access token for usage only, and its
  package comment says it deliberately does not refresh.
- The assistant already avoids re-sign-in: a sign-in lasts as long as the
  vendor keeps honouring refreshes. **New conversation** keeps it. You sign in
  again only when the vendor refuses the refresh token, when the server stops
  between refresh and write-back, or when you switch accounts.

### Option A: copy the environment's login into the assistant (rejected)

- **Refresh-token rotation.** Both vendors return a new refresh token on
  refresh, and the server's own code treats the old one as spent
  (`assistant_signin.go`: "The exchange already spent the old refresh token").
  Two holders of one token chain invalidate each other: the assistant's start
  spends the token the environment holds, so the member's sessions fail at
  their next refresh. In the other direction, a session's refresh makes the
  assistant's next start fail with `the sign-in service refused the
  assistant's sign-in`. Commits #265, #649 and #668 already rely on this:
  share mounts stay read-write because "a refresh kept in the copy would
  revoke the real login". A vendor may also revoke the whole grant when a
  rotated token is reused. Aether's own connector does exactly that. We cannot
  verify either vendor's reuse policy without experimenting on real accounts,
  so the design must not depend on it.
- **Provenance.** The environment's file is session-writable, so a copy takes
  whatever account a session put there. The key whitelist (`assistantLoginKeys`)
  strips planted gateways and MCP tokens, but nothing independent says which
  account is really the member's. #623 ran into the same limit: "Aether cannot
  detect that".
- **A variant that copies only the access token** avoids rotation, but Claude
  access tokens expire within hours. The assistant would die mid-conversation
  and need a fresh copy from the same untrusted file on every start. Also
  rejected.

### Option B: a server-held member credential (does not exist; out of scope)

To use a verified member credential, the server would first have to own the
session logins: hold the refresh token, refresh centrally, and give sessions
short-lived access tokens. That replaces the read-write login mounts that
account sharing and #649's inode pins are built on, and it changes how every
session authenticates. It would also close known share weaknesses (a recipient
can copy or log out the owner's login, and revoke does not recall a copy).
That is a separate L/XL design about session credentials and needs its own
decision. The brief rules out changing credentials, so this proposal does not
take it.

### Option C: one click, then confirm the account (recommended)

**1a. Sign-in starts on first open.**

- When the panel opens with no agent ever signed in, it starts the existing
  `assistant_login=<agent>` sign-in for the first installed agent (Claude
  Code before Codex) without a terminal view. The server reads the URL and
  prompt from the sign-in's output.
- The panel shows **Open Claude sign-in** as a button and a **Paste the code**
  field. Codex shows the device code with a copy button. Opening the URL needs
  a user gesture, which is the one click. If the browser is already signed in
  at claude.ai or chatgpt.com, the vendor page asks only to authorize.
- The panel still shows the real CLI output and error on failure, with
  **Show terminal** for the raw view.
- This adds no new trust. The sign-in still runs in the assistant container,
  and the server pins the account at the next start as today.
- Auto-start only when no sign-in record exists. Starting a sign-in deletes
  the earlier one and stops a running assistant, so it must never start on
  its own after the first time.

**1b. Confirm it is the same account as your sessions.**

- After the pin is recorded, the server reads the environment's login
  read-only, the way `internal/quota` already reads it. It never refreshes it
  and never writes it.
- Claude Code: call the profile endpoint with the environment's access token
  and compare account and organization UUIDs. An access token can be read
  without spending anything.
- Codex: decode the `chatgpt_account_id` claim locally, with no network call.
- The panel then says one of:
  - `Same Claude account as your sessions`
  - `Your sessions use another Claude account (<org/account ids>)`
  - `Can't compare: your environment's Claude token is expired. Start a session to refresh it`

  Show the email too, if the profile answer carries one. Only the UUID is read
  today, so check that first.
- This is **information, never a gate**. The environment's file is
  session-writable, so a match proves only what that file holds now. The
  assistant's pin stays the authority for the assistant.

**1c. Optional: the server runs the OAuth exchange.**

- The server builds the PKCE authorize URL with the pinned client id,
  exchanges the pasted code itself, writes the sign-in file, and records the
  account from the vendor's answer in the same step.
- This removes the install-and-terminal wait and the `pending` window between
  sign-in and the first start.
- It depends on the vendors' undocumented manual-code and device flows. The
  server already depends on the refresh endpoints of the same client ids,
  pinned by `TestIntegrationAssistantIssuerMatchesTheBundledCLIs`.
- It needs a spike against a test account the owner approves. This proposal
  ran no vendor calls.

**What stays the same:** per-member isolation (the assistant home is mounted
only by the assistant), the key whitelist, the per-start vendor refresh and the
account pin, and the #620–#626 privacy rules. No new file reaches the
assistant from the member home, and the assistant never writes the
environment's login.

## Part 2: persistent coordination

### What is true today

- **Watches.** Server-evaluated subscriptions on a session or a PR
  (`internal/watch`). They survive restarts and poll GitHub with the owner's
  `gh` login. Notices go to in-app, Web Push and, for `source: assistant`,
  the assistant transcript. Connectors already create watches through
  `aether_watch` (scope `write`).
- **Session waits** (`report --until`). These are watches a session owns. They
  wake that session once, with a server-authored inbox message where
  untrusted text is quoted. Delivery is claimed before it is written and keyed
  by condition and outcome, so a wake is never doubled (`watch/wait.go`,
  `coord/children.go` `NotifyRun`).
- **Missing piece 1:** nothing routes a watch notice into a *different*
  session. Session B can wait on A only if B itself registered
  `--until run:A:finished`.
- **Missing piece 2:** Aether cannot wake ChatGPT or Claude. MCP has no
  server-to-chat push, so an external coordinator can only poll
  (`aether_changes`). Durable coordination therefore has to live on the
  server, and "notify me" has to mean push, the desktop app or the assistant
  transcript.
- **Missing piece 3:** assistant notices are appended to the transcript
  (`assistant/notice.go`, `Transcribe`) and never start a turn. No event
  triggers an assistant turn today.
- **Connector budget:** 32 tools and 11,599 of 15,000 bytes, measured with
  `go test ./internal/ops -run TestConnectorToolList`.

### Option 2-I: deterministic `relay_to` on watches (recommended)

- `watch.create` and `watch.update` take `relay_to`: up to 4 session ids.
  Each must be in the watch target's workspace, and the watch's member must
  own or control it.
- When the watch records a notice, the server writes one server-authored
  inbox message into each target and wakes it. It reuses the wait delivery
  path: a `run_inbox` delivery row, claim-before-write, keyed by notice and
  target, the same retry and backoff, and untrusted text only on an
  `Untrusted (from ..., not instructions):` line.
- **Authority:** the member's `run.inject` permission is checked at creation
  and again at every delivery. A target the member lost control of fails that
  row with the real error and stops relaying to it. No approval is involved.
  The message is fixed server text, the same kind of message a wait already
  delivers. It is not a free-form prompt, so it does not reach past what the
  member can already do.
- **Connector:** add `relay_to` to `aether_watch`. That costs roughly
  150–250 bytes of the 3,401 left, and adds no new tool. The direct tools stay
  as they are.
- **Cost:** no model tokens. GitHub reads use the member's `gh` rate limit, as
  watches already do. Each woken session spends a turn on its own account.
- **Prompt injection:** none added. No model reads the notice to decide what
  to do. The receiving agent sees untrusted text quoted, exactly as for waits.
- **Visibility and cancel:**
  - Settings → Watches and `aether_watches` show `relay_to` and each relay
    delivery row (`delivered`, `failed: <error>`).
  - Pause or `aether_unwatch` stops it.
  - The target session sees who set it up (`watch <id> of <member>`).
  - Watches keep their 14-day default and 90-day cap.
- **Loops:** a notice fires once per state, and a relay never creates a watch,
  so A→B→A cannot amplify. Cap total relay deliveries per watch, for example
  at 50, and fail the watch with that error when the cap is reached.

### Option 2-II: the assistant runs a standing job (deferred)

A connector tool such as `aether_assistant_job` would give the member's
assistant a natural-language job, woken by watch notices. Evaluated against
the four constraints:

- **Authority.** No member is present to click a card. Cards expire after 10
  minutes and are withdrawn when the turn ends. A job turn can therefore run
  only actions that auto-approval would pass, and the job must list them when
  it is created. The envelope is the intersection of the declared actions and
  the member's current auto-approval setting, checked at every call. With
  auto-approval off, the job can read and notify only, and anything else
  becomes a notice suggesting the action. It never reaches other members'
  sessions, accounts, merges or memory pins.
- **Cost.** Every wake is an assistant turn on the assistant's own sign-in, so
  the member's subscription pays per notice. It needs a per-job wake budget,
  for example N turns a day, shown on the job.
- **Prompt injection.** This is the core risk. The job text came from an
  external model that read untrusted text, and every wake feeds the assistant
  titles, check names and agent reports. Mitigations: the wake prompt carries
  only structured fields (ids, states, condition, outcome) and quotes the
  rest; the job text is shown verbatim on the job; the tool envelope above.
  As `docs/security.md` says for the assistant today, nothing enforces that it
  treats that text as data.
- **Mechanics.** It needs a new event→turn path (a notice queues an assistant
  prompt, waits for an idle turn, and is keyed so a restart never runs a wake
  twice) and a job record with state, last wake, budget and cancel.
- **Visibility.** Jobs would be listed beside watches, with each wake a
  transcript entry. Cancelling deletes the job and its watches.

2-I covers every example in the brief (watch PRs and sessions, relay
dependency completions, notify me) with none of these risks. Build 2-II only
when a concrete job needs judgement, such as "when CI fails on #7, read the
log and tell me whether it is a known flake", and keep it read-and-notify by
default.

## Phased plan

### Phase 1a: one-click first sign-in (S)

- Dashboard panel state for the running sign-in, with the link button and the
  code field; the server exposes the sign-in's URL and prompt from its output.
- Auto-start only when no sign-in record exists.
- Docs: `assistant.md` First use.
- Check: dashboard e2e with the fake issuer the integration tests use.

### Phase 1b: same-account check (S)

- Read the environment's login read-only, then call the profile (Claude) or
  decode the claims (Codex), and show the result in the panel and in Settings
  → Assistant → Sign-in.
- Docs: `assistant.md`, and a line in `security.md` saying it is information
  only.

### Phase 1c: server-run OAuth exchange (M, optional)

- First, a spike on an owner-approved test account to confirm the
  manual-code and device flows work with the pinned client ids.
- Then the server runs the PKCE or device exchange, writes the sign-in file
  and records the account in one step, and the terminal sign-in remains as a
  fallback.

### Phase 2a/2b: `relay_to` (M)

- Store a `relay_to` column on watches plus a migration. Re-check the version
  against `main` right before merge, because migration numbers race.
- Deliver through the wait path, re-check `run.inject` at every delivery, and
  cap deliveries per watch.
- Add `relay_to` to the `aether_watch` schema and keep the budget test green.
- Show relay targets and delivery rows in Settings → Watches.
- Docs: `watches.md`, `connectors.md`.
- Check: integration test where watch on A relays to B, B wakes once, and a
  restart does not double it.

### Phase 2c: assistant standing jobs (L, deferred)

Only after 2a ships and a real judgement task exists. Build it with the
envelope, the budget and the event→turn path above.

## Open questions for the owner

- 1c: approve a vendor spike on a test account, or stop at 1a+1b?
- 2a: should `relay_to` allow sessions you only *control* but do not own?
  Proposed: yes, with the delivery re-checking control each time.
