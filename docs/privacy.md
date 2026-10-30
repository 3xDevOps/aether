# Privacy policy

Aether is self-hosted software. Nothing about you reaches the people who
publish it unless you sign in to the edge they run, at `auth.onaether.dev`
and `edge.onaether.dev` ([What an edge stores](#what-an-edge-stores)). This page is the privacy
policy for the Aether Android app (package `io.aether.android`), whether it
came from a GitHub release or from Google Play, for the dashboard the app
shows, and for that edge. Effective 2026-10-16.

## Who publishes it

Aether is published by 3xDevOps, <https://github.com/3xDevOps>. Questions
about this policy go to <https://github.com/3xDevOps/Aether/issues>.
Privacy requests about what the project's edge holds, such as a copy or a
deletion, go to <team@onaether.dev>; deleting your own account needs no
request ([What an edge stores](#what-an-edge-stores)).

## What the app stores on the phone

- **Which mode you chose and, in Tailscale mode, the server name you type on
  the first screen**, in the app's private storage.
- **In Aether Edge mode, the CLI's state**, under the app's private files
  directory in `.config/aether/`: `config.json` (the servers you linked),
  `edge-device-key` (this phone's SSH private key, mode `0600`),
  `edge-tokens.json` (the edge's sign-in token for this device, mode `0600`)
  and `edge.lock`. Beside them, `resolv.conf` holds the DNS servers of the
  phone's current network, which the app reads (permission
  `ACCESS_NETWORK_STATE`) and rewrites when the network changes. Nothing else
  the app writes is its own.
- **The WebView's ordinary cache** of the dashboard's files (none in Edge
  mode, which turns it off), and the
  dashboard's own local record of how you use it. That record holds how the
  dashboard looks (theme, text size, sidebar width and whether it is
  collapsed, whether a session's Details panel is open, the embedded
  Environment terminal's height, terminal font size, line wrapping in
  Changes, whether single-key shortcuts are on); where you were (the
  workspace you last opened, whether the sidebar lists only your own sessions);
  the mode you last launched each agent in; which update notices you
  dismissed, by version; and the onboarding wizard's progress. The dashboard writes these
  preferences in origin-local `aether.ui` storage: they are separate for each
  server origin, including its port. Nothing reads them but the dashboard,
  and they last until you uninstall the app. Both are private to the app.

In Tailscale mode the app stores no account, password, token, cookie or key:
there is no dashboard sign-in, and the phone's Tailscale login identifies it
to your server ([networking.md](networking.md#the-dashboard)). In Edge mode the
device key and token above are credentials, kept only on the phone. An app
login in the remote development browser is separate server-side state, as
described below. Uninstalling the app deletes everything stored on the phone,
which in Edge mode includes the device key: the phone then signs in again as a
new device and needs approval again where the server requires it. None of it
is backed up: the app opts out of Google's cloud backup and of
device-to-device transfer (`allowBackup=false` and
`data_extraction_rules.xml`), so setting up a new phone starts again.

## What leaves the phone

- **To your server.** In Tailscale mode every request the dashboard makes goes
  over HTTPS to the server whose name you typed, which you or your team run;
  in Edge mode the dashboard talks to the gateway on the phone
  (`127.0.0.1`), which reaches the server over SSH through the edge (next
  items).
  That includes what you type into a terminal, the messages you send an
  agent, an image you pick from the phone to paste into a terminal, the
  contents of files you edit in the dashboard, approvals, and the session controls
  you use. The server keeps them as part of each session's record, on its own disk
  ([install.md](install.md#what-lives-in-the-data-directory)); who on your
  team can see them is in [teams.md](teams.md#roles) and
  [security.md](security.md#the-dashboard-gateways). The app refuses a plain
  `http://` address, permits cleartext only for `127.0.0.1` (the Edge mode
  gateway), and refuses mixed content, so nothing leaves the phone
  unencrypted.
- **In Edge mode, to the edge and through it.** The bundled CLI contacts the
  edge to sign in, list your servers and relay the SSH connection to the one
  you linked; the edge holds what [What an edge
  stores](#what-an-edge-stores) lists and cannot read the stream, which is
  encrypted end to end between the phone and your server. The GitHub sign-in
  happens in the phone's browser, under GitHub's own policy. While the
  dashboard is open, the gateway also asks `github.com` for the newest
  release's tag, as the next item describes, reusing an answer for an hour,
  retrying a failed request after five minutes at the soonest, and asking
  again when it restarts. The CLI resolves host names with plain DNS over
  port 53 to the active network's DNS servers, bypassing Android Private DNS
  (DNS-over-TLS), so that network can see which names it looks up.
- **In both modes, to `github.com` for the newest release.** When the app
  comes to the front, about once an hour, and when you tap **Check for
  updates**, the bundled CLI asks `github.com` for the tag of the newest
  Aether release, so the app can say an update exists. GitHub sees the
  phone's IP address and that request, nothing about you or your server.
  The app downloads nothing itself: you install a release from Google Play,
  or download its APK in the browser.
- **In Tailscale mode, your identity reaches the server through Tailscale, not
  through the app.** The server asks its own tailscaled which tailnet login
  owns the connecting device. The app sends no name, email, or identifier of
  its own. In Edge mode the phone's device key and your edge account identify
  you instead ([edge.md](edge.md)).
- **Your server records you as a member on the app's first request.** In
  Tailscale mode, from that answer it stores your tailnet login, which is an email address, and a
  display name taken from the part before the `@`, and writes one log line
  carrying that login and your device's Tailscale node ID. The record is how
  your administrator approves you and how your teammates see who did what
  ([teams.md](teams.md#roles)). It is kept by your own server, not by the
  publisher, and lasts until an administrator removes it.
- **Your teammates see when you are online and what you have open.** The
  dashboard tells the server every 15 seconds that you are there and which
  workspace you have open, and an open session terminal adds you to that session's
  **Watching** list; the other members of that server see both
  ([teams.md](teams.md)). It is not kept as history.
- **Optional GitHub authorization opens GitHub in your external browser.**
  If an administrator chooses **Connect GitHub** during onboarding or in
  Settings, that browser visits GitHub to approve the native GitHub CLI's
  device authorization. GitHub receives the browser's requests under its own
  policy; the resulting token is kept in the member home on your server,
  not in the dashboard or Android shell.
- **No app analytics or advertising.** The app has no analytics, no crash
  reporting, no advertising, and no third-party library that talks to a
  network. WebView Safe Browsing is turned off in the app, so no visited URL,
  and no hash of one, is sent to Google. A link that leaves the dashboard
  opens in the phone's browser, under that browser's own policy.

Dashboard requests above go to the server you chose, not to an
Aether-operated analytics service. Optional GitHub authorization and the
server-side connections described below are separate external requests.
Nothing is sold.

## Optional GitHub repository access

Connecting GitHub uses the native `gh` CLI in your member Environment on
your own server. Your server contacts GitHub to verify the account, list
repositories the account can access (including private collaborator and
organization repositories), look up a selected repository, and fetch its
selected source branch over HTTPS. GitHub receives these authenticated
requests under its own policy. There is no new Aether-operated authorization
broker or analytics service, and this connection is separate from signing
in to an edge and from an agent's model-provider account.

The native token is stored in
`homes/<member>/.config/gh/hosts.yml` on your server. The server reads it for
GitHub API requests and source fetches. A source mirror stores only the
authorizing member and numeric GitHub account binding with its source
metadata, not a copy of the token. Aether does not send that token to the
dashboard, the edge, or other members, or put it in workspace database
records, on-disk Git configuration, URLs, or command arguments. It is used
in server memory and the fetch child's temporary environment.

This is fetch-only source access, **not a read-only token scope**: native gh
permissions can include broad repository access. Your own agents already
have access to your native login in your member home. Imported code is
deliberately shared with the workspace's authorized members and sessions; the
token is not. Repository data remains on your server until the relevant
workspace/session data is deleted; removing upstream access does not erase
already imported code.

Logout, token expiry or revocation, member removal, or an account switch
prevents further refresh with that binding without moving the accepted
source revision. Disabling or deleting a source mirror does not delete the
native login or revoke authorization at GitHub. To remove the native login
and revoke GitHub authorization, see
[GitHub credentials and signing keys](security.md#github-credentials-and-signing-keys).

## Push notifications

Off until you turn on **Notify this device when a session needs me** in the
dashboard's Settings, in a browser or in the dashboard added to a home screen
([networking.md](networking.md#notifications)). The Android app cannot
receive them, so it never registers with a push service; the one thing in
this section it does send is the last paragraph's report to your server.

Turning it on makes that browser register with its vendor's push service -
Google's for Chrome, Mozilla's for Firefox, Apple's for Safari, Microsoft's
for Edge - and hand your server what the service issued: an address for that
browser and two keys. Your server stores those three values with your member
ID. It removes them when you turn the switch off on that device, when the
push service reports the address gone, and when your member record is
removed.

When one of your sessions starts needing you, or a session your
[assistant](assistant.md#notifications) launched or messaged finishes, or
needs you and is another member's, your server sends one HTTPS request per
subscribed browser to that browser's address, and one more to each of those
browsers when the session stops needing you. Nothing is sent for a member
with no subscribed browser, and nothing is sent to the publisher. The
push service receives:

- **The address**, which is its own identifier for your browser, and your
  server's IP address.
- **The notification, encrypted** on your server so that only that browser
  can read it (RFC 8291): the session's title or the first line of its task, one
  line saying why it needs you, and the session's ID. The push service sees its
  size, at most 4096 bytes, and when it was sent.
- **A signed token** (RFC 8292): the public half of a key your server
  generated for this purpose, the push service's own address, an expiry, and
  the fixed contact `https://github.com/3xDevOps/Aether`. It names neither
  your server nor you.
- **Delivery settings**: a 24-hour time to live, high urgency (normal for a
  reported success and for the message that clears a need), and a topic,
  which is a hash of the session's ID, so that a newer notification for a session
  replaces an undelivered older one.

The push service relays the message to your browser under its own policy.
**Send test notification** sends the same request with the text
`Notifications work on this device.` and no session.

While you use a dashboard, in any browser, the desktop app or the Android
app, it tells your server so at most every 20 seconds. Your server keeps the
time of the latest such report in memory, to hold your notifications while
you are there. It is not stored, and no other member sees it.

## Slack

Off until an admin runs `sudo aether-server slack setup`
([slack.md](slack.md)) and a member links a Slack account. For each linked
member the server sends Slack the titles of the sessions that need them, the
need lines (which can quote an agent's reason), the text and options of
pending requests, session IDs and the dashboard's address, and, in the
link confirmation, the member's Aether display name and the server's host name.
Replies and button presses pass through Slack to the server. Nothing is sent
to Slack for members who never linked.

Slack keeps the messages under the workspace's retention settings. Unlinking
or removing the bot does not delete messages already sent. The tokens stay in
0600 files under `<data-dir>/slack` on the server.

## What an edge stores

An **edge** is the relay and sign-in service that lets a computer running
`aether` reach a server over SSH without Tailscale ([edge.md](edge.md)). The
Android app uses it only in Edge mode. Whoever runs an edge, the publisher for
`auth.onaether.dev` and `edge.onaether.dev`, holds the following. The edge
cannot read terminal, agent, file or dashboard content you reach over SSH,
which is encrypted end to end between your computer and your server. An MCP
connector's traffic is different: TLS ends at the edge, which decrypts each
request, answer and token in memory to relay it ([edge.md](edge.md#mcp-connectors))
and keeps none of them. Its journal logs requests it refuses before relaying,
with the path and, from outside its trusted proxies, the client address, and
internal relay failures; the nginx in front of it logs every request's client
address, path, status and user agent.

| What | Why | Kept |
| --- | --- | --- |
| Your GitHub account: an account id the edge assigns, GitHub's user id, your login, verified primary email and display name, and when you last signed in with GitHub | To sign you in and to match invitations | Until you delete it on the edge's Account page, or its operator deletes it with `aether-edge accounts delete` |
| Each signed-in device: its label (the machine's host name unless you chose one), its public key, a hash of its token, when it signed in and was last used | To let that device connect | Until you revoke it with `aether logout` or the edge's Devices page |
| Browser sign-ins at the edge: a hash of the cookie | To keep you signed in there | 30 days after last use |
| A pending sign-in from `aether login`: the device label, public key and the IP address it started from | To show you on the confirmation page where the sign-in came from | Until the device collects its token or is denied; an expired one (after 10 minutes) until the next sign-in starts |
| Each server you claim: its id, its host name, the access policy it announces, you as its owner | To route connections and show you your servers | Until the owner removes it, or the edge's operator removes or blocks it. Deleting your account removes you as its owner |
| Each server the edge let your account connect to: its id and your GitHub user id | To tell every server that may hold your identity when you delete your account | Until you delete your account |
| A deletion owed to a server that was offline when you deleted your account: that server's id and your GitHub user id | To tell that server to remove your identity | Until it is sent, when the server next connects; at most 1000 per server |
| Each server's directory: the GitHub user id, login, email and role of its members and open invitations | To refuse strangers before they reach the server | Replaced every time the server sends it |
| Bytes relayed per month, not per person | To enforce the operator's bandwidth budget | Indefinitely |
| A server id the operator blocked, and when | To refuse that server | Until the operator unblocks it |
| An account the operator blocked: its GitHub user id, and when | To refuse its sign-ins and claims | Until the operator unblocks it, also after the account is deleted |
| The edge's log: server ids, error messages, and the IP addresses of refused connections and failed TLS handshakes | To operate and defend the edge | For the retention period its operator sets |

Rate limits count requests per IP address, or per /64 for IPv6, in memory
only; nothing is written. While you are connected, the edge also sees your
IP address, which server you reach, when, and how many bytes flow. Your
server logs the IP address of each connection the edge relays to it
(`edge: relayed connection ... client=<address>`), as it does for direct
connections.

Revoking your devices removes them from the edge; the account record stays.
To delete it yourself, open the edge's Account page,
`https://auth.onaether.dev/account` for the project's edge; `aether logout
--delete-account` prints that address. The page asks you to sign in with
GitHub in that browser within the last 5 minutes and to type your
login ([edge.md](edge.md#deleting-an-account)). The edge then deletes the
account, its devices and sessions, and tells each server it reached to
remove your identity there; what a server holds is its administrator's.
You can also ask the edge's operator, `team@onaether.dev` for the
project's edge, who runs `aether-edge accounts delete`.

## Remote-development data

An [enhanced session](enhanced-sessions.md), and a background session over ACP, records its agent session on your server
in `<data-dir>/transcripts/<run_id>.items.jsonl`: the prompts members send, the
agent's messages and thoughts, tool call inputs, command output and diffs,
permission requests and their answers. Whatever the agent or a tool prints,
a secret included, can land there. Anyone with **View** on the session can read
it through the dashboard; answering or messaging the agent needs the `steer`
permission and the session's control lease. The log and terminal recordings persist
until the session is explicitly deleted, including through workspace
deletion. Checkout cleanup and archiving do not remove them. Standard sessions
keep their terminal recording; older servers may already have trimmed history.

Files selected or pasted into chat are uploaded to the session's persistent member
home before sending. So is the picture of each page element annotated in a
session's Browser, when its note is sent; the note's text names that file and
carries the element's markup and visible text as the page had them. Sending an Enhanced prompt passes image bytes to the agent
and its configured model provider; any other file reaches the agent as its
path, which the agent can read and pass on. Room records and the session item
log retain small attachment references and file paths, not copies of the bytes. Members with
**View** can fetch images attached to that session's retained messages. Removing a
draft thumbnail or deleting a session does not remove the member-home file; see
[image retention](security.md#terminal-image-uploads).

The shared app browser runs on your server, not in the phone's WebView.
Browser input, app-terminal input, observations, frames, and explicit capture
requests travel through your Aether server. A page opened there makes its
own network requests from the session's network namespace; those app endpoints
and any third parties the page contacts receive the usual browser requests
under their own policies. This is separate from the Android shell's lack of
analytics or third-party reporting.

The app browser can contain test-account cookies, tokens, personal data, page
URLs, or secrets printed by an app or terminal. Reading the live session,
streaming it, or taking a transient capture requires the `steer` permission
and access to the backing account, not merely permission to view a session.
Deliberately retained copies instead use capture access and
expiry; review what you retain for that audience. Human members and the session
agent have distinct
per-surface control identities; taking over a shared app surface does not
isolate that session's other processes or credentials from it.

The companion has no member-home or source-checkout mount. Its browser
context/profile is transient companion state, outside source control.
Resetting the browser context removes its pages and session state; a lost
browser process does not silently restore an authenticated session. Private
control sockets and lifecycle journals live below
`<data-dir>/scheduler/browser/`, not in the checkout or the session's visible
coordination files. See
[security.md](security.md#remote-development-and-browser-isolation).

Captures are explicit PNG files, not continuous recording. Each session can keep
at most **64 captures**, **128 MiB of PNG bytes in total**, and **8 MiB per
image**. Reaching either aggregate limit refuses a new capture with
`session capture limit reached; delete captures before taking another`; it does
not silently evict old captures. Files and their metadata are stored below
`<data-dir>/coord/<run-id>/captures/`, exposed read-only to the session as
`/run/aether/captures/<random-id>.png`. Metadata identifies the session,
terminal or page, session/revision, capture time, dimensions, and page URL
where applicable. Optional Git HEAD and dirty-state fields describe only what
was actually observed at capture time; absent fields mean **unknown**, not a
clean checkout or the packet's later retained revision.

These captures remain transient session-owned files until explicit deletion or
cleanup of that session's coordination mount. A retained Standard session
keeps its mount and captures until expiry or deletion. The live browser's
frame stream is not automatically archived. Browser diagnostics are bounded
observations, not a full traffic recording: the companion retains up to 100
console warning/error entries and 100 failed/error-response request entries
per page.
URLs and logged messages can still reveal sensitive information.

To preserve a reviewed capture before cleanup, open **Captures…** from the
session's **More** menu or a terminal's **Tools**, select it under **Select
transient captures to retain**, optionally enter verification notes, and
choose **Retain selected captures**. An agent with the advertised capability uses
`aether-internal artifact retain` with selected `artifact_ids`, optional
`verification_notes`, and an `idempotency_key`. This copies only the selected
PNGs into the existing retained packet storage and returns a
`packet_id`; it does not verify the application or report an outcome.
Agents can pass that packet ID to the existing report `--evidence-ref`.
Retain before a success/failure report, capture deletion, or other cleanup
can remove the transient originals. A capture handle or live frame alone is
not a durable record.

Retained copies remain readable after development processes stop, subject
to capture access and expiry. Each session's retained copies are separately
bounded to **64 captures**, **128 MiB total**, and **8 MiB per PNG** across
packets; deleting transient captures does not reclaim retained-copy space.
Verification notes are bounded to **4096 UTF-8 bytes**. Original source,
capture time, session, geometry, URL and any observed Git boundary travel
with the copy. The packet's later retained Git revision does not rewrite or
attest to an earlier screenshot's Git boundary. Missing, truncated and
expired captures are not a successful verification result.

Nothing is retained automatically. Resetting the browser does not delete
already-written PNGs, and retaining an image does not upload it to a public
pull request. There is no credential-entry recording. Do not capture
credentials or publish app logins, cookies, tokens, or customer data; Aether
cannot reliably redact secrets an app renders in pixels or text, or those
you include in verification notes. A member who explicitly downloads,
exports or publishes a capture creates a separate copy under that
destination's access and retention rules.

Messages one agent sends another stay on your server as workspace history
until the sending or receiving session is deleted. Every member of the workspace
can read their bodies, the same audience the **Activity** page has. Do not
ask agents to pass credentials to each other.

## The assistant

Your [assistant](assistant.md) keeps its conversation on your server under
`<data-dir>/assistant/<member>/`:

- `items.jsonl` is the transcript the panel shows: your prompts, the agent's
  replies, its tool calls and their results, and the action cards.
- `home/` is the agent's home: the sign-in you made for the assistant
  (`.claude/.credentials.json` or `.codex/auth.json`) and the conversation's
  session files (`.claude/projects/` or `.codex/sessions/`). History, todos,
  file history, shell snapshots and debug logs exist only while a session
  runs; each start deletes them.
- `signin/` records the account of the sign-in you made: the Claude account
  and organization ids, the ChatGPT account id, or a SHA-256 hash of a Codex
  API key.

On every assistant start the server sends the sign-in's refresh token to the
vendor's sign-in service and the new access token to its profile endpoint
(Claude Code), to learn which account the sign-in belongs to.
- `install/` holds each agent's installed adapter, and `install/<agent>.installed`
  its version.
- `instructions/` holds the instructions the server gives it, and `tools/` its
  tool socket.

Only the server process and your assistant's container read or write them;
no session, environment or other member can, including members you
[share your account](security.md#account-sharing) with. Anyone with access to
the server's data directory, such as its administrator, can read them. Your
prompts and the files the agent reads go to the agent's vendor under the
account you signed the assistant in to.

**New conversation** deletes `items.jsonl` and everything in `home/` but the
sign-in. The output of a sign-in terminal, which shows the sign-in URL and,
for Codex, a one-time code, is recorded like any terminal in
`<data-dir>/transcripts/assistant-login-<member>-<agent>.cast` and deleted
when the sign-in exits; if the dashboard was not attached then, at the next
start or sign-in. Removing a member deletes the whole directory. Assistant state
from before the assistant had its own container is in your home;
[assistant.md](assistant.md#earlier-assistant-state-in-your-home) says how to
delete it.

## Team memory

Team memory is a store of short notes per workspace, kept in
`<data-dir>/aether.db` (tables `memory` and its full-text index
`memory_fts`). A note holds its text, its kind (decision, convention,
gotcha, preference or outcome), up to 20 file paths it cites, the member it
is attributed to, the session, agent and commit it came from, and who pinned
it and when. Notes reach the store three ways:

- An agent writes one with `aether-internal memory add`, or a member with
  `aether memory add`: at most 1024 bytes of text.
- When a session's agent reports success or failure, the server writes one
  outcome note for that session: the report's summary and next action, with the
  files and HEAD commit of the session's latest snapshot. A later report
  rewrites it and clears its pin.

Every member of the server, viewers included, can read every note, and
every session in the workspace can search them. The first line of each pinned
note also goes into the brief each new session in the workspace receives at
launch; a note a member pinned for themselves goes only into their own sessions'
briefs. That limits whose briefs carry the note, not who reads it: team
memory is never private. Only the note's owner or an admin may change a
member note, and only an admin may read another member's brief.

Team memory never reads transcripts, terminal recordings, messages between
sessions, session-room chat, the files in a checkout, or an agent's own memory
files such as those under `~/.claude` or `~/.codex`. Nothing leaves your
server: there is no embedding service and no model call.

Notes are scanned with gitleaks. A note an agent or member writes is refused
when gitleaks finds a secret, with the rule and the line:col it matched. A
report or answer with a secret is still recorded, with its text replaced by
`(withheld: gitleaks rule <rule> at <line:col>)`. gitleaks only finds the
secrets it has rules for, so keep credentials out of reports and answers.

Notes do not expire. A collaborator or admin deletes one with
`aether memory rm <id>`, or every note attributed to a session with `aether memory rm --run <session>`. Deleting a session deletes its unpinned notes; pinned notes stay until
removed. Deleting the workspace deletes all of its notes. A replaced note stays, hidden from
search unless asked for with `--all`, until it is removed. Removing a member
keeps the notes attributed to them.

Before a release migrates the schema, `aether-server update` and **Update
now** copy the database to `<data-dir>/aether.db.pre-update` and delete
that copy once the new release has started. A note deleted while the copy
exists is still in it, and a rollback restores it.

## Assistant preferences

Preferences you ask the assistant to remember are kept in
`<data-dir>/aether.db` (table `member_preferences`): the text, the
workspace it applies to, whether it is on, where it came from (the assistant
or Settings) with the id of the message it was quoted from, and when it was
created, changed and last read.

Only you can read, change or delete your preferences. Teammates and admins
cannot through Aether, nor can your sessions or connected apps. Server operators
with access to the database file can read it.

Forgetting a preference deletes its row. SQLite overwrites the deleted text
(`secure_delete`) and the server checkpoints and truncates the write-ahead
log; while another connection is reading, the forget still succeeds and the
server retries the checkpoint in the background. The server also appends
the preference's id, never its text, to `<data-dir>/preference-tombstones`
before the delete commits, and does the same for an edit of its text,
recording when the replaced row was last changed. When the server starts it
deletes again every preference recorded as deleted, and every row no newer
than one a committed edit replaced, so restoring an older database does not
bring it back. If a forget's database commit fails after its tombstone was
written, the next server start still deletes the preference. The file gains
one short line per forget or edit and is never compacted. Forgetting does
not remove:

- the `aether.db.pre-update` copy an update keeps until the new release
  starts;
- backups an operator made of the data directory;
- sessions already launched with the text in their task;
- the assistant transcript (`<data-dir>/assistant/<member>/items.jsonl`)
  and the agent's session files in `home/` until you start a **New
  conversation**, which deletes both but the sign-in, as it does for a
  temporary conversation;
- copies a connected app or an agent made elsewhere.

The dashboard keeps, in the browser's `localStorage` under
`aether.preferences.undone.<member>`, the ids and versions of the last 200
changes whose **Undo** you used, so a card still reads **Undone** after a
reload. It holds no preference text and is not sent to the server.

Removing a member deletes their preferences in the same transaction and
records each as deleted in the same file before it commits. Merging two members moves all of them to the member that
remains, even past the 200 limit; new saves are then refused until enough
are forgotten.

## Permissions

`INTERNET` lets the app reach your server, or in Edge mode the edge.
`ACCESS_NETWORK_STATE` lets Edge mode read the active network's DNS servers,
because Go's resolver finds no `/etc/resolv.conf` on Android; the app reads
no other network state. The androidx library
adds one signature-level permission the app defines for its own receivers,
which no other app can hold and which grants nothing.

## Deleting your data

- On the phone: uninstall the app, or clear its storage. In Edge mode, also
  revoke the device at the edge ([What an edge stores](#what-an-edge-stores)),
  since the edge keeps its record of the device until you do.
- At an edge: see [What an edge stores](#what-an-edge-stores).
- On the server: the server's administrator owns the data directory and can
  delete a session, a member home, or the whole directory
  ([install.md](install.md#uninstalling)). `aether member remove` removes
  your environment container, deletes your member record and erases your
  member home. It refuses while anything still points at you - a session you
  launched, a schedule you own, or an agent profile you pushed - so an
  administrator deletes those first; what a session wrote stays in the data
  directory until it is deleted too ([teams.md](teams.md#roles)). The
  publisher holds no copy and cannot delete anything on your behalf.
- Evidence: deleting a session, or the packet's 30-day expiry, removes its
  evidence packets at once. The Git objects they retained stay in the
  workspace repository until one background `git gc` runs there, 30 seconds
  after the last such removal in that workspace. A gc that fails or sessions
  past 5 minutes logs `gitengine: evidence prune failed` and is retried at
  the next hourly evidence sweep or server start.

## Subsessions

The app is not directed at subsessions and has no age-specific content or
features.

## Licence and open source notices

The app and the dashboard are under the GPL-3.0, and the libraries they use
are listed with their licences in [notices.md](notices.md). The app's first
screen links to both that page and the licence text.

## Changes

Changes to this policy are commits to this file; its history is public at
<https://github.com/3xDevOps/Aether/commits/main/docs/privacy.md>.
